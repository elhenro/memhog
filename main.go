package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
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
	vm             *mem.VirtualMemoryStat
	swap           *mem.SwapMemoryStat
	cpuTotal       float64
	load           *load.AvgStat
	procs          []procRow
	groups         []groupStat
	memHist        []float64
	cpuHist        []float64
	cursor         int
	status         string
	statusAt       time.Time
	w, h           int
	showHelp       bool
	showDetail     bool
	grouped        bool            // app-family rows (default) vs one row per process
	expanded       map[string]bool // expanded family keys
	filter         string
	catFilter      string // exact category selected via 1–9; intersects with filter
	filtering      bool
	sortBy         string // mem | cpu | name | category | pid
	guardOn        bool
	guardCap       float64
	confirm        confirmAction
	confirmP       procRow
	confirmTargets []procRow
	cleanupPl      cleanupPlan
	nukePl         nukePlan
	ancestors      map[int32]bool
	rawNameMode    bool
	refreshMs      int
	err            error
}

func initialModel() model {
	return model{
		sortBy:    "mem",
		guardCap:  4096,
		refreshMs: int(tickInterval / time.Millisecond),
		memHist:   make([]float64, 0, histLen),
		cpuHist:   make([]float64, 0, histLen),
		ancestors: buildAncestors(int32(os.Getpid())),
		grouped:   true,
		expanded:  map[string]bool{},
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
		sort.SliceStable(m.procs, func(i, j int) bool { return m.procs[i].cpu > m.procs[j].cpu })
	case "name":
		sort.SliceStable(m.procs, func(i, j int) bool {
			return strings.ToLower(m.procs[i].name) < strings.ToLower(m.procs[j].name)
		})
	case "category":
		sort.SliceStable(m.procs, func(i, j int) bool {
			a, b := m.procs[i], m.procs[j]
			if a.category != b.category {
				return catOrder[a.category] < catOrder[b.category]
			}
			return a.memMB > b.memMB
		})
	case "pid":
		sort.SliceStable(m.procs, func(i, j int) bool { return m.procs[i].pid < m.procs[j].pid })
	case "time":
		sort.SliceStable(m.procs, func(i, j int) bool {
			return m.procs[i].createMs < m.procs[j].createMs
		})
	case "delta":
		sort.SliceStable(m.procs, func(i, j int) bool { return m.procs[i].deltaMB > m.procs[j].deltaMB })
	default:
		sort.SliceStable(m.procs, func(i, j int) bool { return m.procs[i].memMB > m.procs[j].memMB })
	}
}

func (m model) filtered() []procRow {
	f := strings.ToLower(m.filter)
	if f == "" && m.catFilter == "" {
		return m.procs
	}
	out := make([]procRow, 0, len(m.procs))
	for _, p := range m.procs {
		if m.catFilter != "" && p.category != m.catFilter {
			continue
		}
		if f != "" {
			hay := strings.ToLower(p.name + " " + p.category + " " + p.rawName + " " + p.user)
			if !strings.Contains(hay, f) {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

// clampCursor keeps the cursor inside the current (filtered) row set.
func (m *model) clampCursor() {
	n := len(m.viewRows())
	if m.cursor >= n {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

// restoreCursor re-selects pid after procs/sort/filter changed; falls back to
// clamping when the process is gone.
func (m *model) restoreCursor(pid int32) {
	if pid != 0 {
		for i, v := range m.viewRows() {
			if !v.group && v.pid == pid {
				m.cursor = i
				return
			}
			for _, p := range v.members {
				if p.pid == pid {
					m.cursor = i
					return
				}
			}
		}
	}
	m.clampCursor()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case refreshMsg:
		if msg.err != nil {
			// keep last good snapshot; only surface the error once there is
			// nothing to render, so one failed sysctl doesn't blank the UI.
			if m.vm == nil {
				m.err = msg.err
			} else {
				m.err = nil
				m.setStatus("refresh failed: " + msg.err.Error())
			}
			return m, nil
		}
		m.err = nil
		prevPid := int32(0)
		if v, ok := m.rowAt(m.cursor); ok {
			prevPid = rowSummary(v).pid
		}
		m.vm = msg.vm
		m.swap = msg.swap
		m.cpuTotal = msg.cpu
		m.load = msg.load
		m.procs = msg.procs
		m.applySort()
		m.groups = computeGroups(m.procs)
		m.restoreCursor(prevPid)
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
		for _, t := range msg.targets {
			if !stillSameProcess(t) {
				continue // already gone, or the pid was reused
			}
			if sendSignal(t.pid, actKill) == nil {
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
	case "enter":
		m.filtering = false
	case "esc":
		m.filtering = false
		m.filter = ""
	case "ctrl+c":
		return m, tea.Quit
	case "backspace":
		rs := []rune(m.filter)
		if len(rs) > 0 {
			m.filter = string(rs[:len(rs)-1])
		}
	default:
		if s := msg.String(); printable(s) {
			m.filter += s
		}
	}
	m.clampCursor()
	return m, nil
}

// printable: full rune sequence with no control chars (pastes arrive as one
// KeyMsg carrying several runes).
func printable(s string) bool {
	rs := []rune(s)
	if len(rs) == 0 {
		return false
	}
	for _, r := range rs {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
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
		act := actTerm
		label := "term"
		if m.confirm == confirmKill {
			act = actKill
			label = "killed"
		}
		targets := m.confirmTargets
		if len(targets) == 0 {
			targets = []procRow{m.confirmP}
		}
		sent, refused := m.signalAll(targets, act)
		switch {
		case sent == 0:
			m.setStatus(fmt.Sprintf("refused: %s is protected", m.confirmP.name))
		case refused > 0:
			m.setStatus(fmt.Sprintf("%s %d/%d %s (kept %d protected)",
				label, sent, len(targets), m.confirmP.name, refused))
		case len(targets) > 1:
			m.setStatus(fmt.Sprintf("%s %s, %d proc(s)", label, m.confirmP.name, sent))
		default:
			m.setStatus(fmt.Sprintf("%s %s (%d)", label, m.confirmP.name, m.confirmP.pid))
		}
		m.confirm = confirmNone
		m.confirmTargets = nil
	case "n", "N", "esc", "q":
		m.confirm = confirmNone
		m.confirmTargets = nil
		m.setStatus("cancelled")
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

// signalAll signals every target it is allowed to touch; returns counts.
func (m model) signalAll(targets []procRow, act signalAction) (sent, refused int) {
	for _, p := range targets {
		if m.refuseSignal(p) != "" {
			refused++
			continue
		}
		if sendSignal(p.pid, act) == nil {
			sent++
		}
	}
	return sent, refused
}

// refuseSignal: never signal memhog itself, its ancestor chain (the shell /
// terminal the user is sitting in), or kernel-critical processes.
func (m model) refuseSignal(p procRow) string {
	switch {
	case p.pid == int32(os.Getpid()):
		return "refused: that's memhog"
	case m.ancestors[p.pid]:
		return fmt.Sprintf("refused: %s (%d) is in memhog's ancestor chain", p.name, p.pid)
	case isCritical(p.name):
		return fmt.Sprintf("refused: %s is critical", p.name)
	}
	return ""
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	rows := m.viewRows()
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		m.showHelp = !m.showHelp
	case "esc":
		m.showHelp = false
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
		m.catFilter = ""
	case "c", "0":
		m.filter = ""
		m.catFilter = ""
		m.clampCursor()
		m.setStatus("filters cleared")
	case "s":
		prevPid := int32(0)
		if v, ok := m.rowAt(m.cursor); ok {
			prevPid = rowSummary(v).pid
		}
		m.sortBy = nextSort(m.sortBy)
		m.applySort()
		m.restoreCursor(prevPid)
	case "u":
		m.grouped = !m.grouped
		m.setStatus(map[bool]string{true: "grouped by app", false: "one row per process"}[m.grouped])
		m.clampCursor()
	case "e", "enter":
		if v, ok := m.rowAt(m.cursor); ok {
			switch {
			case v.group:
				m.expanded[v.key()] = !m.expanded[v.key()]
				m.clampCursor()
			case v.child:
				// collapsing from a member row returns the cursor to its group
				m.expanded[v.parentKey] = false
				for i, r := range m.viewRows() {
					if r.group && r.key() == v.parentKey {
						m.cursor = i
						break
					}
				}
				m.clampCursor()
			}
		}
	case "K":
		if v, ok := m.rowAt(m.cursor); ok {
			m.confirm = confirmKill
			m.confirmP = rowSummary(v)
			m.confirmTargets = rowTargets(v)
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
		m.confirmTargets = nil
		m.confirm = confirmCleanup
	case "X":
		pl := planClaudeNuke(m.procs, int32(os.Getpid()), m.ancestors)
		if len(pl.targets) == 0 {
			m.setStatus(fmt.Sprintf("nuke: nothing to kill (kept %d self/ancestor)", pl.keptSelf))
			return m, nil
		}
		m.nukePl = pl
		m.confirmTargets = nil
		m.confirm = confirmNuke
	case "t":
		if v, ok := m.rowAt(m.cursor); ok {
			m.confirm = confirmTerm
			m.confirmP = rowSummary(v)
			m.confirmTargets = rowTargets(v)
		}
	case "p", " ":
		if v, ok := m.rowAt(m.cursor); ok {
			n, refused := m.signalAll(rowTargets(v), actStop)
			if n == 0 && refused > 0 {
				m.setStatus("refused: nothing pausable in " + v.name)
				return m, nil
			}
			m.setStatus(fmt.Sprintf("paused %d/%d %s", n, len(rowTargets(v)), v.name))
		}
	case "r":
		if v, ok := m.rowAt(m.cursor); ok {
			n, _ := m.signalAll(rowTargets(v), actCont)
			m.setStatus(fmt.Sprintf("resumed %d/%d %s", n, len(rowTargets(v)), v.name))
		}
	case "o":
		for i, v := range rows {
			l := strings.ToLower(v.name + " " + v.cmdline)
			if v.category == "AI · LLM" || strings.Contains(l, "ollama") || strings.Contains(l, "omlx") {
				m.cursor = i
				m.setStatus("→ " + v.name)
				break
			}
		}
	case "R":
		n := 0
		for _, p := range m.procs {
			if !p.stopped {
				continue
			}
			if sendSignal(p.pid, actCont) == nil {
				n++
			}
		}
		m.setStatus(fmt.Sprintf("resumed %d paused proc(s)", n))
	case "L":
		if !pauseSupported {
			m.setStatus("guard needs SIGSTOP — unsupported on this platform")
			break
		}
		m.guardOn = !m.guardOn
	case "+", "=":
		m.guardCap += 512
	case "-":
		if m.guardCap > 512 {
			m.guardCap -= 512
		}
	case "n":
		m.rawNameMode = !m.rawNameMode
	case "d":
		m.showDetail = !m.showDetail
	case "<":
		if m.refreshMs < 10000 {
			m.refreshMs += 500
		}
	case ">":
		if m.refreshMs > 500 {
			m.refreshMs -= 500
		}
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(msg.String()[0] - '1')
		if idx >= 0 && idx < len(m.groups) {
			cat := m.groups[idx].cat
			if m.catFilter == cat {
				m.catFilter = ""
				m.setStatus("filter cleared")
			} else {
				m.catFilter = cat
				m.filter = ""
				m.cursor = 0
				m.setStatus("filter → " + cat)
			}
		}
	default:
		// pasted text arrives as one multi-rune KeyMsg; treat it as filter input
		if s := msg.String(); len([]rune(s)) > 1 && printable(s) {
			m.filtering = true
			m.filter = s
			m.catFilter = ""
			m.clampCursor()
		}
	}
	return m, nil
}

func nextSort(s string) string {
	switch s {
	case "mem":
		return "cpu"
	case "cpu":
		return "delta"
	case "category":
		return "name"
	case "delta":
		return "category"
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

// guard: pause the biggest non-LLM-server (ollama/omlx), non-critical proc
// when free RAM below cap. never touches memhog or its ancestor shell chain.
func (m *model) runGuard() {
	if m.vm == nil || !pauseSupported {
		return
	}
	freeMB := float64(m.vm.Available) / 1024 / 1024
	if freeMB >= m.guardCap {
		return
	}
	self := int32(os.Getpid())
	var best *procRow
	for i := range m.procs {
		p := &m.procs[i]
		if p.pid == self || m.ancestors[p.pid] {
			continue
		}
		if p.category == "AI · LLM" || p.category == "AI · Agent" || p.stopped || p.memMB < 200 {
			continue
		}
		if isCritical(p.name) || isCritical(p.rawName) {
			continue
		}
		if best == nil || p.memMB > best.memMB {
			best = p
		}
	}
	if best == nil {
		return
	}
	_ = sendSignal(best.pid, actStop)
	m.setStatus(fmt.Sprintf("guard paused %s (%d) %.0fMB", best.name, best.pid, best.memMB))
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
