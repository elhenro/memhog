package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
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
	pid      int32
	ppid     int32
	name     string // display name (post-resolve)
	family   string // app family: display name with Helper/suffix stripped
	rawName  string // original p.Name() — e.g. "2.1.143"
	user     string
	cmdline  string
	exe      string
	memMB    float64
	memPct   float32
	cpu      float64
	stopped  bool
	category string
	version  string // optional version suffix (e.g. claude code 2.1.143)
	createMs int64  // process start, unix ms
	threads  int32
	trend    int8 // -1 down, 0 flat, +1 up vs prev tick
	deltaMB  float64
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
	vm    *mem.VirtualMemoryStat
	swap  *mem.SwapMemoryStat
	cpu   float64
	load  *load.AvgStat
	procs []procRow
	err   error
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
		sw, _ := mem.SwapMemory()
		ps, err := process.Processes()
		if err != nil {
			return refreshMsg{err: err}
		}
		rows := fetchProcsParallel(ps, stoppedPids())
		return refreshMsg{vm: vm, swap: sw, cpu: cpuTotal, load: la, procs: rows}
	}
}

// stoppedPids returns the set of stopped (SIGSTOP'd) processes using one
// `ps` call. gopsutil's per-process Status() costs ~3.5ms each on macOS,
// which made a single refresh take longer than the tick interval.
func stoppedPids() map[int32]bool {
	if runtime.GOOS == "windows" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,stat=").Output()
	if err != nil {
		return nil
	}
	m := make(map[int32]bool, len(out)/16)
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		if strings.Contains(f[1], "T") {
			m[int32(pid)] = true
		}
	}
	return m
}

// fetchProcsParallel pulls per-proc dynamic fields concurrently. The fixed
// fields (exe/cmdline/name/createTime) live in procCache and are only fetched
// once per pid; on subsequent ticks we only pull MemoryInfo + CPUPercent +
// Status, which are the hot syscalls.
func fetchProcsParallel(ps []*process.Process, stopped map[int32]bool) []procRow {
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
				if r, ok := buildRow(p, stopped[p.Pid]); ok {
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
	// evict against the enumerated pid set, not the rows we managed to read:
	// protected processes (MemoryInfo can fail) would otherwise be re-resolved
	// from scratch and lose their trend baseline on every tick.
	seen := make(map[int32]bool, len(ps))
	for _, p := range ps {
		seen[p.Pid] = true
	}
	for r := range out {
		rows = append(rows, r)
	}
	evictMissing(seen)
	return rows
}

func buildRow(p *process.Process, isStopped bool) (procRow, bool) {
	mi, err := p.MemoryInfo()
	if err != nil || mi == nil {
		return procRow{}, false
	}
	e := getOrFillCache(p)
	mp, _ := p.MemoryPercent()
	cpuP, _ := p.CPUPercent()
	stopped := isStopped
	th, _ := p.NumThreads()
	ppid, _ := p.Ppid()
	memMB := float64(mi.RSS) / 1024 / 1024
	prev, had := updateLastMB(p.Pid, memMB)
	var trend int8
	delta := 0.0
	if had {
		delta = memMB - prev
		switch {
		case delta >= 1:
			trend = +1
		case delta <= -1:
			trend = -1
		}
	}
	return procRow{
		pid:      p.Pid,
		ppid:     ppid,
		name:     e.disp,
		family:   familyName(e.disp),
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

// familyName collapses per-helper process names into the owning app, so
// "Brave Browser Helper (Renderer)" and "Brave Browser" become one row.
func familyName(n string) string {
	n = strings.TrimSpace(n)
	for _, suf := range []string{
		" Helper (Renderer)",
		" Helper (GPU)",
		" Helper (Plugin)",
		" Helper (Network)",
		" Helper (Alerts)",
		" Helper",
		" Renderer",
		" (Renderer)",
	} {
		if strings.HasSuffix(n, suf) {
			n = strings.TrimSuffix(n, suf)
			break
		}
	}
	if n == "" {
		return "?"
	}
	return n
}

// resolveDisplay returns (displayName, optionalVersion).
// when raw name is version-only (e.g. "2.1.143"), we look at exe/cmdline to
// give it a meaningful prefix; that version becomes the suffix.
func resolveDisplay(raw, exe, cmd string) (string, string) {
	// omlx ships as `python3 -m omlx.cli serve` inside oMLX.app — raw name is
	// "python3" which hides the LLM server. override before the meaningful-raw
	// early return.
	if omlxMatch(exe, cmd) {
		return "oMLX", ""
	}
	ver := ""
	if versionRe.MatchString(raw) {
		ver = raw
	}
	// coding agents often run as `node /…/opencode`; the raw name would be
	// "node", so resolve them from exe/cmdline before the raw-name shortcut.
	if label, ok := agentLabel(exe, cmd); ok {
		if ver != "" {
			return label + " · " + ver, ver
		}
		return label, ver
	}
	if looksMeaningful(raw) {
		return raw, ""
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

// omlxMatch — oMLX.app bundles python3; inspect exe/cmd for the app path or
// the `omlx.cli` module marker.
func omlxMatch(exe, cmd string) bool {
	l := strings.ToLower(exe + "\x00" + cmd)
	return strings.Contains(l, "/omlx.app/") || strings.Contains(l, "omlx.cli")
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

// agentTools maps coding-agent binaries to display names. Tokens match only on
// a path or word boundary, so "compose" does not look like "omp".
var agentTools = []struct{ tok, label string }{
	{"opencode", "opencode"},
	{"omp", "omp"},
	{"dsh", "dsh"},
	{"codex", "codex"},
	{"aider", "aider"},
	{"goose", "goose"},
	{"crush", "crush"},
}

// agentMarkers catch agents that run behind a node/bun wrapper: the binary is
// the interpreter, the agent is the script path.
var agentMarkers = []struct{ marker, label string }{
	{"@sst/opencode", "opencode"},
	{"opencode/bin", "opencode"},
	{"@openai/codex", "codex"},
	{"codex-cli", "codex"},
}

// agentLabel returns the display name for a coding agent, if this process is
// one. Only the executable and the program being run are inspected, so an
// editor that merely has a file called codex.md open is not mislabelled.
// Claude Code is matched by its install paths, never by the bare word, for the
// same reason.
func agentLabel(exe, cmd string) (string, bool) {
	if claudeCodeMatch(exe, cmd) {
		return "Claude Code", true
	}
	head := exe
	if f := strings.Fields(cmd); len(f) > 0 {
		head += "\x00" + f[0]
	}
	l := strings.ToLower(head)
	for _, t := range agentTools {
		if wordContains(l, t.tok) {
			return t.label, true
		}
	}
	lc := strings.ToLower(cmd)
	for _, m := range agentMarkers {
		if strings.Contains(lc, m.marker) {
			return m.label, true
		}
	}
	return "", false
}

// wordContains reports whether tok appears in s as a whole path component or
// word, not as part of a longer word.
func wordContains(s, tok string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], tok)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(tok)
		if (start == 0 || isWordSep(s[start-1])) && (end == len(s) || isWordSep(s[end])) {
			return true
		}
		i = start + 1
	}
}

func isWordSep(b byte) bool {
	switch b {
	case '/', '\\', ' ', '\t', 0, '.', '-', '_', ':', '=', '(', ')', '\'', '"':
		return true
	}
	return false
}

// isAgentProc also recognises rows whose display name was already resolved.
func isAgentProc(exe, cmd, disp string) bool {
	if _, ok := agentLabel(exe, cmd); ok {
		return true
	}
	for _, t := range agentTools {
		if disp == t.label || strings.HasPrefix(disp, t.label+" \u00b7 ") {
			return true
		}
	}
	return strings.HasPrefix(disp, "Claude Code")
}

// detectCategory groups a process into a coarse bucket for the summary band.
func detectCategory(rawName, exe, cmd, disp string) string {
	l := strings.ToLower(rawName + "\x00" + exe + "\x00" + cmd + "\x00" + disp)
	switch {
	case isAgentProc(exe, cmd, disp):
		return "AI · Agent"
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
	case "AI · Agent":
		return colCatAgent
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
	case "AI · Agent":
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
	case "AI · Agent":
		return "agents"
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
	"AI · Agent":     0,
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
