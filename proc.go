package main

import (
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/process"
)

type procRow struct {
	pid       int32
	ppid      int32
	name      string  // display name (post-resolve)
	rawName   string  // original p.Name() — e.g. "2.1.143"
	user      string
	cmdline   string
	exe       string
	memMB     float64
	memPct    float32
	cpu       float64
	stopped   bool
	category  string
	version   string // optional version suffix (e.g. claude code 2.1.143)
	createMs  int64  // process start, unix ms
	threads   int32
	trend     int8  // -1 down, 0 flat, +1 up vs prev tick
	deltaMB   float64
}

// procCache holds per-pid memo of fields that never change for a given pid.
// also stores last memMB so we can derive trend arrows.
type procCacheEntry struct {
	rawName  string
	exe      string
	cmd      string
	disp     string
	ver      string
	cat      string
	createMs int64
	username string
	lastMB   float64
}

var (
	procCacheMu sync.Mutex
	procCache   = map[int32]procCacheEntry{}
)

func getOrFillCache(p *process.Process) procCacheEntry {
	procCacheMu.Lock()
	e, ok := procCache[p.Pid]
	procCacheMu.Unlock()
	if ok && e.rawName != "" {
		return e
	}
	raw, _ := p.Name()
	exe, _ := p.Exe()
	cmd, _ := p.Cmdline()
	user, _ := p.Username()
	disp, ver := resolveDisplay(raw, exe, cmd)
	cat := detectCategory(raw, exe, cmd, disp)
	ct, _ := p.CreateTime()
	e = procCacheEntry{
		rawName: raw, exe: exe, cmd: cmd, disp: disp,
		ver: ver, cat: cat, createMs: ct, username: user,
	}
	procCacheMu.Lock()
	procCache[p.Pid] = e
	procCacheMu.Unlock()
	return e
}

func updateLastMB(pid int32, mb float64) (prev float64, had bool) {
	procCacheMu.Lock()
	defer procCacheMu.Unlock()
	e, ok := procCache[pid]
	if !ok {
		return 0, false
	}
	prev, had = e.lastMB, e.lastMB != 0
	e.lastMB = mb
	procCache[pid] = e
	return
}

func evictMissing(seen map[int32]bool) {
	procCacheMu.Lock()
	defer procCacheMu.Unlock()
	for pid := range procCache {
		if !seen[pid] {
			delete(procCache, pid)
		}
	}
}

type refreshMsg struct {
	vm   *mem.VirtualMemoryStat
	cpu  float64
	load *load.AvgStat
	procs []procRow
	err  error
}

func refreshCmd() tea.Cmd {
	return func() tea.Msg {
		vm, err := mem.VirtualMemory()
		if err != nil {
			return refreshMsg{err: err}
		}
		cpuPcts, _ := cpu.Percent(0, false) // since last call
		var cpuTotal float64
		if len(cpuPcts) > 0 {
			cpuTotal = cpuPcts[0]
		}
		la, _ := load.Avg()
		ps, err := process.Processes()
		if err != nil {
			return refreshMsg{err: err}
		}
		rows := fetchProcsParallel(ps)
		return refreshMsg{vm: vm, cpu: cpuTotal, load: la, procs: rows}
	}
}

// fetchProcsParallel pulls per-proc dynamic fields concurrently. The fixed
// fields (exe/cmdline/name/createTime) live in procCache and are only fetched
// once per pid; on subsequent ticks we only pull MemoryInfo + CPUPercent +
// Status, which are the hot syscalls.
func fetchProcsParallel(ps []*process.Process) []procRow {
	workers := runtime.NumCPU() * 2
	if workers > 32 {
		workers = 32
	}
	in := make(chan *process.Process, len(ps))
	out := make(chan procRow, len(ps))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range in {
				if r, ok := buildRow(p); ok {
					out <- r
				}
			}
		}()
	}
	for _, p := range ps {
		in <- p
	}
	close(in)
	go func() { wg.Wait(); close(out) }()
	rows := make([]procRow, 0, len(ps))
	seen := make(map[int32]bool, len(ps))
	for r := range out {
		rows = append(rows, r)
		seen[r.pid] = true
	}
	evictMissing(seen)
	return rows
}

func buildRow(p *process.Process) (procRow, bool) {
	mi, err := p.MemoryInfo()
	if err != nil || mi == nil {
		return procRow{}, false
	}
	e := getOrFillCache(p)
	mp, _ := p.MemoryPercent()
	cpuP, _ := p.CPUPercent()
	st, _ := p.Status()
	stopped := false
	for _, s := range st {
		if s == "T" || s == "stop" || strings.Contains(strings.ToLower(s), "stop") {
			stopped = true
		}
	}
	th, _ := p.NumThreads()
	ppid, _ := p.Ppid()
	memMB := float64(mi.RSS) / 1024 / 1024
	prev, had := updateLastMB(p.Pid, memMB)
	var trend int8
	delta := 0.0
	if had {
		delta = memMB - prev
		switch {
		case delta > 8:
			trend = +1
		case delta < -8:
			trend = -1
		}
	}
	return procRow{
		pid:      p.Pid,
		ppid:     ppid,
		name:     e.disp,
		rawName:  e.rawName,
		user:     e.username,
		cmdline:  e.cmd,
		exe:      e.exe,
		memMB:    memMB,
		memPct:   mp,
		cpu:      cpuP,
		stopped:  stopped,
		category: e.cat,
		version:  e.ver,
		createMs: e.createMs,
		threads:  th,
		trend:    trend,
		deltaMB:  delta,
	}, true
}

var versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(\.\d+)?$`)

// resolveDisplay returns (displayName, optionalVersion).
// when raw name is version-only (e.g. "2.1.143"), we look at exe/cmdline to
// give it a meaningful prefix; that version becomes the suffix.
func resolveDisplay(raw, exe, cmd string) (string, string) {
	if looksMeaningful(raw) {
		return raw, ""
	}
	ver := ""
	if versionRe.MatchString(raw) {
		ver = raw
	}
	if claudeCodeMatch(exe, cmd) {
		if ver != "" {
			return "Claude Code · " + ver, ver
		}
		return "Claude Code", ver
	}
	// fall back to original chain
	if exe != "" {
		base := filepath.Base(exe)
		if looksMeaningful(base) {
			if ver != "" {
				return base + " · " + ver, ver
			}
			return base, ver
		}
		dir := exe
		for i := 0; i < 6; i++ {
			dir = filepath.Dir(dir)
			if strings.HasSuffix(dir, ".app") {
				name := strings.TrimSuffix(filepath.Base(dir), ".app")
				if ver != "" {
					return name + " · " + ver, ver
				}
				return name, ver
			}
		}
	}
	if cmd != "" {
		first := strings.Fields(cmd)
		if len(first) > 0 {
			b := filepath.Base(first[0])
			if ver != "" {
				return b + " · " + ver, ver
			}
			return b, ver
		}
	}
	if raw != "" {
		return raw, ver
	}
	return "?", ver
}

func looksMeaningful(n string) bool {
	if n == "" {
		return false
	}
	digits, dots := 0, 0
	for _, r := range n {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.':
			dots++
		default:
			return true
		}
	}
	return digits == 0 || dots == 0
}

// claudeCodeMatch — heuristics for the official `claude` CLI (npm
// @anthropic-ai/claude-code). It typically execs `node cli.js`, and the
// process title is set to the version string ("2.1.143").
func claudeCodeMatch(exe, cmd string) bool {
	l := strings.ToLower(exe + "\x00" + cmd)
	switch {
	case strings.Contains(l, "@anthropic-ai/claude-code"):
		return true
	case strings.Contains(l, "anthropic-ai/claude-code"):
		return true
	case strings.Contains(l, "claude-code/cli"):
		return true
	case strings.Contains(l, "/.claude/local/") && strings.Contains(l, "node"):
		return true
	case strings.Contains(l, "/.npm-global/") && strings.Contains(l, "claude"):
		return true
	case strings.Contains(l, "/bin/claude") && !strings.Contains(l, "/bin/claude-"):
		return true
	}
	return false
}

// detectCategory groups a process into a coarse bucket for the summary band.
func detectCategory(rawName, exe, cmd, disp string) string {
	l := strings.ToLower(rawName + "\x00" + exe + "\x00" + cmd + "\x00" + disp)
	switch {
	case strings.Contains(l, "claude code"), strings.Contains(l, "@anthropic-ai/claude-code"),
		strings.Contains(l, "claude-code/cli"), strings.Contains(l, "/.claude/local/"),
		strings.Contains(l, "bee ") && strings.Contains(l, "run "):
		return "Claude Code"
	case strings.Contains(l, "ollama"), strings.Contains(l, "omlx"),
		strings.Contains(l, "llama-server"),
		strings.Contains(l, "lm studio"), strings.Contains(l, "lmstudio"),
		strings.Contains(l, "llamafile"), strings.Contains(l, "vllm"),
		strings.Contains(l, "mlx_lm"):
		return "AI · LLM"
	case strings.Contains(l, "code helper"), strings.Contains(l, "cursor helper"),
		strings.Contains(l, "/visual studio code"), strings.Contains(l, "vscode"),
		strings.Contains(l, "code - insiders"), strings.Contains(l, "windsurf"),
		strings.Contains(l, "zed"), strings.Contains(l, "sublime"),
		strings.Contains(l, "neovim"), strings.Contains(l, "nvim"):
		return "Editor"
	case strings.Contains(l, "browser helper"), strings.Contains(l, "google chrome"),
		strings.Contains(l, "safari"), strings.Contains(l, "firefox"),
		strings.Contains(l, "/arc"), strings.Contains(l, "microsoft edge"),
		strings.Contains(l, "brave"), strings.Contains(l, "vivaldi"),
		strings.Contains(l, "webkit"), strings.Contains(l, "chromium"):
		return "Browser"
	case strings.Contains(l, "docker"), strings.Contains(l, "virtualization"),
		strings.Contains(l, "qemu"), strings.Contains(l, "podman"),
		strings.Contains(l, "vmware"), strings.Contains(l, "parallels"),
		strings.Contains(l, "orbstack"), strings.Contains(l, "lima-"):
		return "VM · Container"
	case isCritical(rawName), isCritical(disp), strings.Contains(l, "/system/library/"),
		strings.Contains(l, "windowserver"), strings.Contains(l, "launchd"),
		strings.Contains(l, "kernel_task"), strings.Contains(l, "loginwindow"):
		return "System"
	case strings.Contains(l, "/node"), strings.Contains(l, " node "),
		strings.Contains(l, "python"), strings.Contains(l, "ruby"),
		strings.Contains(l, "/go/"), strings.Contains(l, "java"),
		strings.Contains(l, "deno"), strings.Contains(l, "bun"):
		return "Runtime"
	case strings.Contains(l, "slack"), strings.Contains(l, "discord"),
		strings.Contains(l, "telegram"), strings.Contains(l, "whatsapp"),
		strings.Contains(l, "signal"), strings.Contains(l, "zoom"),
		strings.Contains(l, "teams"):
		return "Chat · Meet"
	}
	return "Other"
}

func catColor(cat string) lipgloss.Color {
	switch cat {
	case "Claude Code":
		return colCatClaude
	case "AI · LLM":
		return colCatLLM
	case "Browser":
		return colCatBrowser
	case "Editor":
		return colCatEditor
	case "VM · Container":
		return colCatVM
	case "System":
		return colCatSystem
	case "Runtime":
		return colCatRuntime
	case "Chat · Meet":
		return colCool
	default:
		return colCatOther
	}
}

// catGlyph: braille tags, monospace, no emoji presentation.
func catGlyph(cat string) string {
	switch cat {
	case "Claude Code":
		return "⢿"
	case "AI · LLM":
		return "⣿"
	case "Browser":
		return "⠶"
	case "Editor":
		return "⠟"
	case "VM · Container":
		return "⣶"
	case "System":
		return "⡇"
	case "Runtime":
		return "⠿"
	case "Chat · Meet":
		return "⠉"
	default:
		return "⠂"
	}
}

// catShort: compact name for the horizontal group band.
func catShort(cat string) string {
	switch cat {
	case "Claude Code":
		return "claude"
	case "AI · LLM":
		return "ai"
	case "Browser":
		return "browser"
	case "Editor":
		return "editor"
	case "VM · Container":
		return "vm"
	case "System":
		return "sys"
	case "Runtime":
		return "runtime"
	case "Chat · Meet":
		return "chat"
	default:
		return "other"
	}
}

// catOrder controls the band ordering when ties on memMB are broken.
var catOrder = map[string]int{
	"Claude Code":    0,
	"AI · LLM":       1,
	"Browser":        2,
	"Editor":         3,
	"VM · Container": 4,
	"Runtime":        5,
	"Chat · Meet":    6,
	"System":         7,
	"Other":          8,
}

type groupStat struct {
	cat   string
	memMB float64
	cpu   float64
	count int
}

func computeGroups(procs []procRow) []groupStat {
	m := map[string]*groupStat{}
	for _, p := range procs {
		g, ok := m[p.category]
		if !ok {
			g = &groupStat{cat: p.category}
			m[p.category] = g
		}
		g.memMB += p.memMB
		g.cpu += p.cpu
		g.count++
	}
	out := make([]groupStat, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	// sort: by memMB desc, then by predefined catOrder
	sortGroups(out)
	return out
}

func sortGroups(g []groupStat) {
	for i := 1; i < len(g); i++ {
		for j := i; j > 0; j-- {
			a, b := g[j-1], g[j]
			if a.memMB < b.memMB ||
				(a.memMB == b.memMB && catOrder[a.cat] > catOrder[b.cat]) {
				g[j-1], g[j] = g[j], g[j-1]
			} else {
				break
			}
		}
	}
}

func isCritical(name string) bool {
	n := strings.ToLower(name)
	bad := []string{"kernel_task", "launchd", "windowserver", "loginwindow", "coreaudio", "syslogd", "configd", "powerd", "memhog"}
	for _, b := range bad {
		if strings.Contains(n, b) {
			return true
		}
	}
	return false
}

// keep ticker constants colocated with refresh
const (
	tickInterval = 1500 * time.Millisecond
	histLen      = 32
)

type tickMsg time.Time

func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}
