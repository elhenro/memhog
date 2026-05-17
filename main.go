package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
)

type confirmAction int

const (
	confirmNone confirmAction = iota
	confirmKill
	confirmTerm
	confirmCleanup
	confirmNuke
)

type model struct {
	vm          *mem.VirtualMemoryStat
	cpuTotal    float64
	load        *load.AvgStat
	procs       []procRow
	groups      []groupStat
	memHist     []float64
	cpuHist     []float64
	cursor      int
	status      string
	statusAt    time.Time
	w, h        int
	showHelp    bool
	showDetail  bool
	filter      string
	filtering   bool
	sortBy      string // mem | cpu | name | category | pid
	guardOn     bool
	guardCap    float64
	confirm     confirmAction
	confirmP    procRow
	cleanupPl   cleanupPlan
	nukePl      nukePlan
	ancestors   map[int32]bool
	rawNameMode bool
	refreshMs   int
	err         error
}

func initialModel() model {
	return model{
		sortBy:    "mem",
		guardCap:  4096,
		refreshMs: int(tickInterval / time.Millisecond),
		memHist:   make([]float64, 0, histLen),
		cpuHist:   make([]float64, 0, histLen),
		ancestors: buildAncestors(int32(os.Getpid())),
	}
}

func (m model) Init() tea.Cmd { return tea.Batch(refreshCmd(), tickCmd(m.tickDur())) }

func (m model) tickDur() time.Duration {
	if m.refreshMs <= 0 {
		return tickInterval
	}
	return time.Duration(m.refreshMs) * time.Millisecond
}

func (m *model) applySort() {
	switch m.sortBy {
	case "cpu":
		sort.Slice(m.procs, func(i, j int) bool { return m.procs[i].cpu > m.procs[j].cpu })
	case "name":
		sort.Slice(m.procs, func(i, j int) bool {
			return strings.ToLower(m.procs[i].name) < strings.ToLower(m.procs[j].name)
		})
	case "category":
		sort.Slice(m.procs, func(i, j int) bool {
			a, b := m.procs[i], m.procs[j]
			if a.category != b.category {
				return catOrder[a.category] < catOrder[b.category]
			}
			return a.memMB > b.memMB
		})
	case "pid":
		sort.Slice(m.procs, func(i, j int) bool { return m.procs[i].pid < m.procs[j].pid })
	case "time":
		sort.Slice(m.procs, func(i, j int) bool {
			return m.procs[i].createMs < m.procs[j].createMs
		})
	default:
		sort.Slice(m.procs, func(i, j int) bool { return m.procs[i].memMB > m.procs[j].memMB })
	}
}

func (m model) filtered() []procRow {
	if m.filter == "" {
		return m.procs
	}
	f := strings.ToLower(m.filter)
	out := make([]procRow, 0, len(m.procs))
	for _, p := range m.procs {
		hay := strings.ToLower(p.name + " " + p.category + " " + p.rawName)
		if strings.Contains(hay, f) {
			out = append(out, p)
		}
	}
	return out
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case refreshMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.vm = msg.vm
		m.cpuTotal = msg.cpu
		m.load = msg.load
		m.procs = msg.procs
		m.applySort()
		m.groups = computeGroups(m.procs)
		m.memHist = pushHist(m.memHist, msg.vm.UsedPercent)
		m.cpuHist = pushHist(m.cpuHist, msg.cpu)
		if m.guardOn {
			m.runGuard()
		}
		return m, nil
	case tickMsg:
		return m, tea.Batch(refreshCmd(), tickCmd(m.tickDur()))
	case cleanupEscalateMsg:
		killed := 0
		for _, pid := range msg.pids {
			if syscall.Kill(int(pid), 0) == nil {
				_ = syscall.Kill(int(pid), syscall.SIGKILL)
				killed++
			}
		}
		if killed > 0 {
			m.setStatus(fmt.Sprintf("cleanup: hard-killed %d straggler(s)", killed))
		}
		return m, nil
	case tea.KeyMsg:
		if m.confirm != confirmNone {
			return m.handleConfirm(msg)
		}
		if m.filtering {
			return m.handleFilter(msg)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func pushHist(h []float64, v float64) []float64 {
	h = append(h, v)
	if len(h) > histLen {
		h = h[len(h)-histLen:]
	}
	return h
}

func (m *model) setStatus(s string) {
	m.status = s
	m.statusAt = time.Now()
}

func (m model) handleFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "esc":
		m.filtering = false
	case "backspace":
		if len(m.filter) > 0 {
			m.filter = m.filter[:len(m.filter)-1]
		}
	default:
		if len(msg.String()) == 1 {
			m.filter += msg.String()
		}
	}
	return m, nil
}

func (m model) handleConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y", "enter":
		if m.confirm == confirmCleanup {
			targets := m.cleanupPl.targets
			cleanupTermAll(targets)
			m.setStatus(fmt.Sprintf("cleanup: SIGTERM → %d proc(s)", len(targets)))
			m.confirm = confirmNone
			return m, cleanupEscalateCmd(targets)
		}
		if m.confirm == confirmNuke {
			targets := m.nukePl.targets
			cleanupTermAll(targets)
			m.setStatus(fmt.Sprintf("nuke: SIGTERM → %d claude/anthropic proc(s), ~%.0fMB",
				len(targets), m.nukePl.totalMB))
			m.confirm = confirmNone
			return m, cleanupEscalateCmd(targets)
		}
		pid := m.confirmP.pid
		var sig syscall.Signal = syscall.SIGTERM
		label := "term"
		if m.confirm == confirmKill {
			sig = syscall.SIGKILL
			label = "killed"
		}
		err := syscall.Kill(int(pid), sig)
		if err != nil {
			m.setStatus(fmt.Sprintf("%s %d failed: %v", label, pid, err))
		} else {
			m.setStatus(fmt.Sprintf("%s %s (%d)", label, m.confirmP.name, pid))
		}
		m.confirm = confirmNone
	case "n", "N", "esc", "q":
		m.confirm = confirmNone
		m.setStatus("cancelled")
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.filtered()
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.showHelp = !m.showHelp
	case "j", "down":
		if m.cursor < len(rows)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "ctrl+d":
		step := m.listLimit() / 2
		m.cursor = minInt(len(rows)-1, m.cursor+step)
	case "ctrl+u":
		step := m.listLimit() / 2
		m.cursor -= step
		if m.cursor < 0 {
			m.cursor = 0
		}
	case "ctrl+f", "pgdown":
		m.cursor = minInt(len(rows)-1, m.cursor+m.listLimit())
	case "ctrl+b", "pgup":
		m.cursor -= m.listLimit()
		if m.cursor < 0 {
			m.cursor = 0
		}
	case "g":
		m.cursor = 0
	case "G":
		m.cursor = len(rows) - 1
		if m.cursor < 0 {
			m.cursor = 0
		}
	case "/":
		m.filtering = true
		m.filter = ""
	case "c":
		m.filter = ""
	case "s":
		m.sortBy = nextSort(m.sortBy)
		m.applySort()
		m.setStatus("sort by " + m.sortBy)
	case "K":
		if p, ok := procAtCursor(rows, m.cursor); ok {
			m.confirm = confirmKill
			m.confirmP = p
		}
	case "C":
		pl := planClaudeCleanup(m.procs, m.ancestors)
		if len(pl.targets) == 0 {
			msg := "claude-cleanup: nothing to reap"
			if pl.keptTTY > 0 {
				msg += fmt.Sprintf(" (kept %d live pty-host)", pl.keptTTY)
			}
			m.setStatus(msg)
			return m, nil
		}
		m.cleanupPl = pl
		m.confirm = confirmCleanup
	case "X":
		pl := planClaudeNuke(m.procs, int32(os.Getpid()), m.ancestors)
		if len(pl.targets) == 0 {
			m.setStatus(fmt.Sprintf("nuke: nothing to kill (kept %d self/ancestor)", pl.keptSelf))
			return m, nil
		}
		m.nukePl = pl
		m.confirm = confirmNuke
	case "t":
		if p, ok := procAtCursor(rows, m.cursor); ok {
			m.confirm = confirmTerm
			m.confirmP = p
		}
	case "p", " ":
		if p, ok := procAtCursor(rows, m.cursor); ok {
			err := syscall.Kill(int(p.pid), syscall.SIGSTOP)
			if err != nil {
				m.setStatus(fmt.Sprintf("pause %d failed: %v", p.pid, err))
			} else {
				m.setStatus(fmt.Sprintf("paused %s (%d)", p.name, p.pid))
			}
		}
	case "r":
		if p, ok := procAtCursor(rows, m.cursor); ok {
			err := syscall.Kill(int(p.pid), syscall.SIGCONT)
			if err != nil {
				m.setStatus(fmt.Sprintf("resume %d failed: %v", p.pid, err))
			} else {
				m.setStatus(fmt.Sprintf("resumed %s (%d)", p.name, p.pid))
			}
		}
	case "o":
		for i, p := range rows {
			if p.category == "AI · LLM" {
				m.cursor = i
				m.setStatus("→ " + p.name)
				break
			}
		}
	case "L":
		m.guardOn = !m.guardOn
		if m.guardOn {
			m.setStatus(fmt.Sprintf("guard on, free ≥ %.0fMB", m.guardCap))
		} else {
			m.setStatus("guard off")
		}
	case "+", "=":
		m.guardCap += 512
		m.setStatus(fmt.Sprintf("guard ≥ %.0fMB", m.guardCap))
	case "-":
		if m.guardCap > 512 {
			m.guardCap -= 512
		}
		m.setStatus(fmt.Sprintf("guard ≥ %.0fMB", m.guardCap))
	case "n":
		m.rawNameMode = !m.rawNameMode
		if m.rawNameMode {
			m.setStatus("raw process names on")
		} else {
			m.setStatus("resolved names on")
		}
	case "d":
		m.showDetail = !m.showDetail
		if m.showDetail {
			m.setStatus("detail pane on")
		} else {
			m.setStatus("detail pane off")
		}
	case "<":
		if m.refreshMs < 10000 {
			m.refreshMs += 500
			m.setStatus(fmt.Sprintf("tick %dms", m.refreshMs))
		}
	case ">":
		if m.refreshMs > 500 {
			m.refreshMs -= 500
			m.setStatus(fmt.Sprintf("tick %dms", m.refreshMs))
		}
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(msg.String()[0] - '1')
		if idx >= 0 && idx < len(m.groups) {
			cat := m.groups[idx].cat
			m.filter = strings.ToLower(catShort(cat))
			m.cursor = 0
			m.setStatus("filter → " + cat)
		}
	case "0":
		m.filter = ""
		m.setStatus("filter cleared")
	}
	return m, nil
}

func nextSort(s string) string {
	switch s {
	case "mem":
		return "cpu"
	case "cpu":
		return "category"
	case "category":
		return "name"
	case "name":
		return "time"
	case "time":
		return "pid"
	default:
		return "mem"
	}
}

func procAtCursor(rows []procRow, c int) (procRow, bool) {
	if c < 0 || c >= len(rows) {
		return procRow{}, false
	}
	return rows[c], true
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// guard: pause biggest non-LLM-server (ollama/omlx), non-critical proc when free RAM below cap
func (m *model) runGuard() {
	if m.vm == nil {
		return
	}
	freeMB := float64(m.vm.Available) / 1024 / 1024
	if freeMB >= m.guardCap {
		return
	}
	self := int32(os.Getpid())
	for _, p := range m.procs {
		if p.pid == self {
			continue
		}
		if p.category == "AI · LLM" {
			continue
		}
		if p.stopped {
			continue
		}
		if p.memMB < 200 {
			continue
		}
		if isCritical(p.name) {
			continue
		}
		_ = syscall.Kill(int(p.pid), syscall.SIGSTOP)
		m.setStatus(fmt.Sprintf("guard paused %s (%d) %.0fMB", p.name, p.pid, p.memMB))
		return
	}
}

// build-time vars, set via -ldflags by goreleaser.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-v", "--version", "version":
			fmt.Printf("memhog %s (%s, %s)\n", version, commit, date)
			return
		case "-h", "--help", "help":
			fmt.Println("memhog — terminal memory & process inspector")
			fmt.Println("usage: memhog        (no flags; ? in-app for keys)")
			fmt.Println("       memhog -v     print version")
			return
		}
	}
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
