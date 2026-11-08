package main

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func testModel() model {
	return model{
		procs: []procRow{
			{pid: 1, name: "chrome", category: "Browser", memMB: 800, rawName: "chrome"},
			{pid: 2, name: "chrome helper", category: "Browser", memMB: 300, rawName: "chrome"},
			{pid: 3, name: "claude", category: "Claude Code", memMB: 500, rawName: "claude"},
			{pid: 4, name: "syscored", category: "Other", memMB: 50, rawName: "syscored"},
		},
		ancestors: map[int32]bool{1: true},
	}
}

func TestGroupFilterIsCategoryExact(t *testing.T) {
	m := testModel()
	m.catFilter = "Browser"
	rows := m.filtered()
	if len(rows) != 2 {
		t.Fatalf("want 2 browser rows, got %d", len(rows))
	}
	// "sys" must not leak in via a process name (old text-filter bug).
	m.catFilter = "System"
	if rows := m.filtered(); len(rows) != 0 {
		t.Fatalf("category filter matched %d rows by name", len(rows))
	}
}

func TestFilterIntersectsCategory(t *testing.T) {
	m := testModel()
	m.catFilter = "Browser"
	m.filter = "helper"
	rows := m.filtered()
	if len(rows) != 1 || rows[0].pid != 2 {
		t.Fatalf("want only chrome helper, got %+v", rows)
	}
}

func TestCursorClampedAfterFilterNarrows(t *testing.T) {
	m := testModel()
	m.cursor = 3
	m.catFilter = "Claude Code"
	m.clampCursor()
	if m.cursor != 0 {
		t.Fatalf("cursor not clamped, got %d", m.cursor)
	}
}

func TestCursorRestoredByPidAfterSort(t *testing.T) {
	m := testModel()
	m.cursor = 2 // claude, pid 3
	m.applySort()
	m.restoreCursor(3)
	if p, ok := procAtCursor(m.filtered(), m.cursor); !ok || p.pid != 3 {
		t.Fatalf("cursor lost selected pid, got %+v", p)
	}
}

func TestSortDirection(t *testing.T) {
	if sortDirGlyph("mem") != "↓" || sortDirGlyph("pid") != "↑" || sortDirGlyph("name") != "↑" {
		t.Fatal("sort direction glyphs wrong")
	}
}

func TestFilterBackspaceIsRuneSafe(t *testing.T) {
	m := testModel()
	m.filtering = true
	m.filter = "café"
	next, _ := m.handleFilter(tea.KeyMsg{Type: tea.KeyBackspace})
	got := next.(model).filter
	if got != "caf" {
		t.Fatalf("want caf, got %q", got)
	}
}

func TestRefuseSignalGuardsSelfAndAncestors(t *testing.T) {
	m := testModel()
	if m.refuseSignal(procRow{pid: 1, name: "shell"}) == "" {
		t.Fatal("ancestor not refused")
	}
	if m.refuseSignal(procRow{pid: 99, name: "kernel_task"}) == "" {
		t.Fatal("critical proc not refused")
	}
	if m.refuseSignal(procRow{pid: 77, name: "random"}) != "" {
		t.Fatal("ordinary proc refused")
	}
}

func TestGroupKeyTogglesCategoryFilter(t *testing.T) {
	m := testModel()
	m.groups = computeGroups(m.procs)
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m = next.(model)
	if m.catFilter != m.groups[0].cat {
		t.Fatalf("key 1 set catFilter=%q want %q", m.catFilter, m.groups[0].cat)
	}
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	if got := next.(model).catFilter; got != "" {
		t.Fatalf("second key 1 should clear, got %q", got)
	}
}

func TestSortKeyKeepsSelectedPid(t *testing.T) {
	m := testModel()
	m.cursor = 2 // pid 3
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = next.(model)
	if p, ok := procAtCursor(m.filtered(), m.cursor); !ok || p.pid != 3 {
		t.Fatalf("sort lost selection: %+v", p)
	}
}

func TestBrowserHelpersCollapseIntoOneFamily(t *testing.T) {
	m := testModel()
	m.grouped = true
	m.expanded = map[string]bool{}
	m.procs = []procRow{
		{pid: 10, name: "Brave Browser Helper (Renderer)", family: familyName("Brave Browser Helper (Renderer)"), category: "Browser", memMB: 400},
		{pid: 11, name: "Brave Browser Helper (GPU)", family: familyName("Brave Browser Helper (GPU)"), category: "Browser", memMB: 200},
		{pid: 12, name: "Brave Browser", family: familyName("Brave Browser"), category: "Browser", memMB: 600},
		{pid: 13, name: "omp", family: "omp", category: "Runtime", memMB: 500},
	}
	rows := m.viewRows()
	if len(rows) != 2 {
		t.Fatalf("want 2 grouped rows, got %d: %+v", len(rows), rows)
	}
	if !rows[0].group || rows[0].name != "Brave Browser" || rows[0].count != 3 || rows[0].memMB != 1200 {
		t.Fatalf("bad family row: %+v", rows[0])
	}
	m.expanded[rows[0].key()] = true
	rows = m.viewRows()
	if len(rows) != 5 {
		t.Fatalf("expanded group should emit 3 children, got %d rows", len(rows))
	}
	if !rows[1].child || rows[1].pid != 12 {
		t.Fatalf("children not sorted by mem: %+v", rows[1])
	}
}

func TestFamilyNameStrip(t *testing.T) {
	cases := map[string]string{
		"Brave Browser Helper (Renderer)":  "Brave Browser",
		"Brave Browser Helper":             "Brave Browser",
		"Code Helper (GPU)":                "Code",
		"Google Chrome for Testing Helper": "Google Chrome for Testing",
		"omp":                              "omp",
	}
	for in, want := range cases {
		if got := familyName(in); got != want {
			t.Errorf("familyName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSortCycleIncludesDelta(t *testing.T) {
	got := []string{}
	s := "mem"
	for i := 0; i < 7; i++ {
		got = append(got, s)
		s = nextSort(s)
	}
	want := []string{"mem", "cpu", "delta", "category", "name", "time", "pid"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sort cycle = %v, want %v", got, want)
		}
	}
	if sortDirGlyph("delta") != "↓" {
		t.Fatal("delta sort should be descending")
	}
}

func TestDeltaSortRanksGrowthFirst(t *testing.T) {
	m := testModel()
	m.grouped = true
	m.expanded = map[string]bool{}
	m.sortBy = "delta"
	m.procs = []procRow{
		{pid: 1, name: "flat", family: "flat", category: "Other", memMB: 900, deltaMB: 0},
		{pid: 2, name: "leaky", family: "leaky", category: "Other", memMB: 100, deltaMB: 42},
	}
	rows := m.viewRows()
	if rows[0].name != "leaky" {
		t.Fatalf("delta sort put %q first", rows[0].name)
	}
}

func TestTruncRightUsesDisplayWidth(t *testing.T) {
	if got := truncRight("日本語abc", 5); got != "日本…" {
		t.Fatalf("truncRight = %q, want 日本…", got)
	}
	if w := lipgloss.Width(padRight("日", 4)); w != 4 {
		t.Fatalf("padRight width = %d, want 4", w)
	}
	if got := truncRight("short", 8); got != "short" {
		t.Fatalf("truncRight truncated unnecessarily: %q", got)
	}
}

func TestNukeSparesEditorsMentioningClaudePaths(t *testing.T) {
	vim := procRow{pid: 1, name: "vim", exe: "/usr/bin/vim",
		cmdline: "vim /Users/me/.claude/settings.json"}
	if isAnthropicOwned(vim) {
		t.Fatal("editor editing a claude path would be nuked")
	}
	mcp := procRow{pid: 2, name: "node", exe: "/usr/local/bin/node",
		cmdline: "node /Users/me/.claude/mcp/server.js"}
	if !isAnthropicOwned(mcp) {
		t.Fatal("claude MCP helper no longer detected")
	}
	claude := procRow{pid: 3, name: "claude", exe: "/Users/me/.local/bin/claude",
		cmdline: "claude daemon run"}
	if !isAnthropicOwned(claude) {
		t.Fatal("claude daemon not detected")
	}
}

func TestCollapseFromChildRowReturnsToGroup(t *testing.T) {
	m := testModel()
	m.grouped = true
	m.expanded = map[string]bool{}
	m.procs = []procRow{
		{pid: 21, name: "Brave Browser Helper (Renderer)", family: "Brave Browser", category: "Browser", memMB: 300},
		{pid: 22, name: "Brave Browser Helper (GPU)", family: "Brave Browser", category: "Browser", memMB: 200},
	}
	rows := m.viewRows()
	if len(rows) != 1 || !rows[0].group {
		t.Fatalf("expected one group, got %+v", rows)
	}
	m.expanded[rows[0].key()] = true
	rows = m.viewRows()
	if len(rows) != 3 {
		t.Fatalf("expected header + 2 children, got %d", len(rows))
	}
	m.cursor = 2 // a child row
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = next.(model)
	rows = m.viewRows()
	if len(rows) != 1 || rows[0].expanded {
		t.Fatalf("group should be collapsed, got %d rows (expanded=%v)", len(rows), rows[0].expanded)
	}
	if m.cursor != 0 {
		t.Fatalf("cursor should return to the group, got %d", m.cursor)
	}
}

func TestTTYDetectionFailureKeepsPtyHosts(t *testing.T) {
	old := lookPath
	lookPath = func(string) (string, error) { return "", fmt.Errorf("no lsof") }
	defer func() { lookPath = old }()

	procs := []procRow{{
		pid: 4242, name: "claude",
		cmdline: "claude --bg-pty-host",
	}}
	pl := planClaudeCleanup(procs, map[int32]bool{})
	if len(pl.targets) != 0 || pl.keptTTY != 1 {
		t.Fatalf("unknown tty state must keep pty-hosts, got targets=%d kept=%d",
			len(pl.targets), pl.keptTTY)
	}
}
