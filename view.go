package main

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// fmtSize prints MB or GB, compact (no space).
func fmtSize(mb float64) string {
	if mb >= 1024 {
		gb := mb / 1024
		if gb >= 100 {
			return fmt.Sprintf("%.0fGB", gb)
		}
		return fmt.Sprintf("%.1fGB", gb)
	}
	if mb >= 100 {
		return fmt.Sprintf("%.0fMB", mb)
	}
	return fmt.Sprintf("%.0fMB", mb)
}

// fmtUptime: hh:mm:ss or d Hh
func fmtUptime(createMs int64) string {
	if createMs <= 0 {
		return "—"
	}
	sec := int64(time.Since(time.UnixMilli(createMs)).Seconds())
	if sec < 0 {
		return "—"
	}
	d := sec / 86400
	if d > 0 {
		h := (sec % 86400) / 3600
		return fmt.Sprintf("%dd%02dh", d, h)
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	s := sec % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

// truncRight trims s to n terminal cells (not runes: CJK and emoji names are
// double-width and would otherwise wrap the row).
func truncRight(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > n-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}

// padRight pads s with spaces to n terminal cells.
func padRight(s string, n int) string {
	if d := n - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// bar: sub-cell precision unicode bar with centered percent label.
// uses eighth-block runes so fill resolution is 8× the cell count.
func bar(pct float64, w int, c lipgloss.Color) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	label := fmt.Sprintf(" %.0f%% ", pct)
	lw := len(label)
	if w < lw+4 {
		w = lw + 4
	}
	eighths := int(pct / 100 * float64(w*8))
	fullCells := eighths / 8
	rem := eighths % 8
	if fullCells > w {
		fullCells = w
		rem = 0
	}
	midStart := (w - lw) / 2

	stFill := lipgloss.NewStyle().Foreground(c).Background(c)
	stEmpty := lipgloss.NewStyle().Foreground(colDimmer).Background(colDimmer)
	stEdge := lipgloss.NewStyle().Foreground(c).Background(colDimmer)
	stLblOn := lipgloss.NewStyle().Foreground(colBg).Background(c).Bold(true)
	stLblOff := lipgloss.NewStyle().Foreground(c).Background(colDimmer).Bold(true)

	var b strings.Builder
	for i := 0; i < w; i++ {
		inLabel := i >= midStart && i < midStart+lw
		filled := i < fullCells
		edge := i == fullCells && rem > 0
		switch {
		case inLabel && filled:
			b.WriteString(stLblOn.Render(string(label[i-midStart])))
		case inLabel:
			b.WriteString(stLblOff.Render(string(label[i-midStart])))
		case filled:
			b.WriteString(stFill.Render(" "))
		case edge:
			b.WriteString(stEdge.Render(string(blockEighths[rem])))
		default:
			b.WriteString(stEmpty.Render(" "))
		}
	}
	return b.String()
}

// listLimit: terminal rows minus every fixed line View() emits.
// base chrome = pane pad(2) + title/mem/cpu(3) + blanks(2) + table hdr(1)
//
//   - blank+hint(2) + overflow note(1) = 11
func (m model) listLimit() int {
	h := m.h
	if h <= 0 {
		h = 24
	}
	reserved := 11
	if len(m.groups) > 0 {
		reserved += 2 // band + blank
	}
	if m.showDetail {
		reserved += 7 // blank + 6 detail lines
	}
	if m.renderMeta() != "" {
		reserved += 2
	}
	if m.renderStatus() != "" {
		reserved += 1
	}
	n := h - reserved
	if n < 3 {
		n = 3
	}
	if n > 200 {
		n = 200
	}
	return n
}

// contentW is the usable line width: one column short of the terminal, so a
// line that fills it exactly never triggers the terminal's pending autowrap
// (which drops/garbles the last cell of every row).
func (m model) contentW() int {
	w := m.w
	if w <= 0 {
		w = 80
	}
	return w - 1
}

func (m model) barWidth() int {
	// reserve: label(4) + gap(1) + spark(16) + gap(2) + suffix(~22) + pane pad(4)
	bw := m.contentW() - 4 - 1 - 16 - 2 - 22 - 4
	if bw < 10 {
		bw = 10
	}
	if max := m.contentW() - 4 - 8; bw > max {
		bw = max
	}
	if bw > 100 {
		bw = 100
	}
	return bw
}

// meterLine: label + bar, then as much of sparkline/suffix as actually fits.
// narrow terminals drop the extras instead of letting the terminal clip them.
func (m model) meterLine(label string, pct float64, c lipgloss.Color, spark []float64, suffix string) string {
	line := styleSection.Render(fmt.Sprintf("%-*s", 4, label)) + bar(pct, m.barWidth(), c)
	room := m.contentW() - 4 - lipgloss.Width(line)
	for _, sparkW := range []int{16, 10} {
		if room >= 1+sparkW+2+lipgloss.Width(suffix) {
			return line + " " + sparkline(spark, sparkW, c) + "  " + suffix
		}
	}
	if lipgloss.Width(suffix) > 0 && room >= 2+lipgloss.Width(suffix) {
		return line + "  " + suffix
	}
	return line
}

func (m model) View() string {
	if m.err != nil {
		return stylePane.Render(lipgloss.JoinVertical(lipgloss.Left,
			styleCrit.Render("memhog: ")+m.err.Error(),
			"",
			styleDim.Render("retrying every tick · q to quit")))
	}
	if m.vm == nil {
		return styleDim.Render("loading…")
	}
	w := m.w
	if w <= 0 {
		w = 80
	}

	// help is its own screen: the key list is taller than the list chrome it
	// would otherwise push off-screen on short terminals.
	if m.showHelp {
		body := lipgloss.JoinVertical(lipgloss.Left,
			renderHelp(),
			"",
			styleDim.Render("?  close"))
		return m.overlay(stylePane.Render(body), w)
	}

	parts := []string{m.renderHeader(w), ""}
	if gb := m.renderGroupBand(w); gb != "" {
		parts = append(parts, gb, "")
	}
	parts = append(parts, m.renderList(w))
	if dp := m.renderDetail(w); dp != "" {
		parts = append(parts, "", dp)
	}
	if meta := m.renderMeta(); meta != "" {
		parts = append(parts, "", meta)
	}
	if s := m.renderStatus(); s != "" {
		parts = append(parts, s)
	}
	parts = append(parts, "", m.renderHint())
	body := lipgloss.JoinVertical(lipgloss.Left, parts...)
	return m.overlay(stylePane.Render(body), w)
}

// overlay stacks the active confirmation dialog under the base view.
func (m model) overlay(out string, w int) string {
	switch m.confirm {
	case confirmCleanup:
		return overlayCleanup(out, m.cleanupPl, w, m.h)
	case confirmNuke:
		return overlayNuke(out, m.nukePl, w, m.h)
	case confirmNone:
		return out
	}
	return overlayConfirm(out, m.confirm, m.confirmP, len(m.confirmTargets), w, m.h)
}

// header: title row + mem row + cpu row + sparklines.
func (m model) renderHeader(w int) string {
	usedPct := m.vm.UsedPercent
	totalGB := float64(m.vm.Total) / 1024 / 1024 / 1024
	availGB := float64(m.vm.Available) / 1024 / 1024 / 1024

	host, _ := os.Hostname()
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	title := styleTitle.Render("⬢ memhog") + styleDim.Render("  "+host)
	right := styleDim.Render(fmt.Sprintf("%d procs · %s",
		len(m.procs), time.Now().Format("15:04:05")))
	pad := m.contentW() - 4 - lipgloss.Width(title) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	titleLine := title + strings.Repeat(" ", pad) + right

	memSuffixText := fmt.Sprintf("%.0fGB · %.0fGB free", totalGB, availGB)
	if m.swap != nil && m.swap.Used > 0 {
		memSuffixText += fmt.Sprintf(" · swap %.1fGB", float64(m.swap.Used)/1024/1024/1024)
	}
	memSuffix := styleDim.Render(memSuffixText)
	memLine := m.meterLine("mem", usedPct, memColor(usedPct), m.memHist, memSuffix)

	cpuSuffix := styleDim.Render(fmt.Sprintf("%dc", runtime.NumCPU()))
	if m.load != nil {
		cpuSuffix = styleDim.Render(fmt.Sprintf("%.2f %.2f %.2f · %dc",
			m.load.Load1, m.load.Load5, m.load.Load15, runtime.NumCPU()))
	}
	cpuLine := m.meterLine("cpu", m.cpuTotal, cpuColor(m.cpuTotal), m.cpuHist, cpuSuffix)

	return lipgloss.JoinVertical(lipgloss.Left, titleLine, "", memLine, cpuLine)
}

// renderGroupBand: single horizontal line of categories.
func (m model) renderGroupBand(w int) string {
	if len(m.groups) == 0 {
		return ""
	}
	label := styleSection.Render("groups ")
	used := lipgloss.Width(label)
	parts := []string{}
	dot := styleDimmer.Render(" · ")
	dotW := lipgloss.Width(dot)
	limit := m.contentW() - 6 - used
	shown := 0
	for i, g := range m.groups {
		gly := lipgloss.NewStyle().Foreground(catColor(g.cat)).Render(catGlyph(g.cat))
		seg := fmt.Sprintf("%s %s%s %s",
			gly,
			styleSub.Render(catShort(g.cat)),
			styleDim.Render(fmt.Sprintf(":%d", g.count)),
			lipgloss.NewStyle().Foreground(colFg).Render(fmtSize(g.memMB)))
		segW := lipgloss.Width(seg)
		add := segW
		if i > 0 {
			add += dotW
		}
		if used+add > limit && shown > 0 {
			parts = append(parts, styleDim.Render(fmt.Sprintf(" +%d", len(m.groups)-shown)))
			break
		}
		if i > 0 {
			parts = append(parts, dot)
			used += dotW
		}
		parts = append(parts, seg)
		used += segW
		shown++
	}
	return label + strings.Join(parts, "")
}

// sortDesc: which columns sort descending. name/pid/time are ascending.
func sortDesc(key string) bool {
	switch key {
	case "name", "pid", "time":
		return false
	}
	return true
}

func sortDirGlyph(key string) string {
	if sortDesc(key) {
		return "↓"
	}
	return "↑"
}

// listCols: which optional columns fit the current terminal width. keeping
// this in one place stops narrow terminals from clipping whole columns.
type listCols struct {
	mem, trend, cpu, time, pid bool
}

func (m model) listCols() listCols {
	w := m.contentW()
	return listCols{
		mem:   w >= 34,
		trend: w >= 44,
		cpu:   w >= 54,
		time:  w >= 66,
		pid:   w >= 76,
	}
}

func (m model) renderList(w int) string {
	rows := m.viewRows()
	lim := m.listLimit()
	cursor := m.cursor
	if cursor >= len(rows) {
		cursor = len(rows) - 1
	}
	if cursor < 0 {
		cursor = 0
	}

	// dense layout: mark(2) + glyph(1) + space + name(flex) + optional columns
	const (
		memW   = 8
		trendW = 6
		cpuW   = 7
		timeW  = 9
		pidW   = 7
		gap    = 3
	)
	cols := m.listCols()
	fixed := 0
	for _, on := range []bool{cols.mem, cols.trend, cols.cpu, cols.time, cols.pid} {
		if on {
			fixed += gap
		}
	}
	if cols.mem {
		fixed += memW
	}
	if cols.trend {
		fixed += trendW
	}
	if cols.cpu {
		fixed += cpuW
	}
	if cols.time {
		fixed += timeW
	}
	if cols.pid {
		fixed += pidW
	}
	nameW := m.contentW() - 4 - fixed - 4
	if nameW < 12 {
		nameW = 12
	}

	label := "process"
	if m.grouped {
		label = "app · ×procs"
	}
	hdr := "    " + padRight(label, nameW)
	if cols.mem {
		hdr += fmt.Sprintf("   %*s%s", memW-1, "mem", sortIndicator(m.sortBy, "mem"))
	}
	if cols.trend {
		hdr += fmt.Sprintf("   %*s%s", trendW-1, "Δ", sortIndicator(m.sortBy, "delta"))
	}
	if cols.cpu {
		hdr += fmt.Sprintf("   %*s%s", cpuW-1, "cpu", sortIndicator(m.sortBy, "cpu"))
	}
	if cols.time {
		hdr += fmt.Sprintf("   %*s%s", timeW-1, "time", sortIndicator(m.sortBy, "time"))
	}
	if cols.pid {
		hdr += fmt.Sprintf("   %*s%s", pidW-1, "pid", sortIndicator(m.sortBy, "pid"))
	}

	var list strings.Builder
	list.WriteString(styleDim.Render(hdr) + "\n")

	start := 0
	if cursor >= lim {
		start = cursor - lim + 1
	}
	if start < 0 {
		start = 0
	}
	end := start + lim
	if end > len(rows) {
		end = len(rows)
	}

	lineW := m.contentW() - 4 // strip pane pad
	for i := start; i < end; i++ {
		p := rows[i]
		selected := i == cursor

		mark := "  "
		if selected {
			mark = styleAccent.Render("▎ ")
		}
		gly := lipgloss.NewStyle().Foreground(catColor(p.category)).Render(catGlyph(p.category))
		name := p.name
		switch {
		case p.group:
			arrow := "▸ "
			if p.expanded {
				arrow = "▾ "
			}
			name = arrow + name + " ×" + strconv.Itoa(p.count)
		case m.rawNameMode && p.rawName != "":
			name = p.rawName
		}
		if p.child {
			name = "  " + name
		}
		if p.stopped {
			name = "‖ " + name
		}
		name = truncRight(name, nameW)

		line := padRight(truncRight(name, nameW), nameW)
		if cols.mem {
			line += fmt.Sprintf("   %*s", memW, fmtSize(p.memMB))
		}
		if cols.trend {
			trend := trendGlyph(p.trend, p.deltaMB)
			trendCell := fmt.Sprintf("%*s", trendW, "")
			if trend != "" {
				pad := trendW - lipgloss.Width(trend)
				if pad < 0 {
					pad = 0
				}
				trendCell = strings.Repeat(" ", pad) + trend
			}
			line += "   " + trendCell
		}
		if cols.cpu {
			line += fmt.Sprintf("   %*s", cpuW, fmt.Sprintf("%.1f%%", p.cpu))
		}
		if cols.time {
			line += fmt.Sprintf("   %*s", timeW, fmtUptime(p.createMs))
		}
		if cols.pid {
			if p.group {
				line += fmt.Sprintf("   %*s", pidW, "")
			} else {
				line += fmt.Sprintf("   %*d", pidW, p.pid)
			}
		}

		row := mark + gly + " " + line
		switch {
		case selected:
			// full-width band — pad row to lineW then apply selected bg
			padN := lineW - lipgloss.Width(row)
			if padN < 0 {
				padN = 0
			}
			row = styleSelBg.Render(row + strings.Repeat(" ", padN))
		case p.group:
			row = lipgloss.NewStyle().Foreground(catColor(p.category)).Bold(true).Render(row)
		case p.category == "Claude Code":
			row = lipgloss.NewStyle().Foreground(colCatClaude).Render(row)
		case p.category == "AI · LLM":
			row = lipgloss.NewStyle().Foreground(colCatLLM).Render(row)
		case p.memMB >= 1024:
			row = lipgloss.NewStyle().Foreground(colFg).Render(row)
		default:
			row = styleSub.Render(row)
		}
		list.WriteString(row + "\n")
	}
	if len(rows) == 0 {
		list.WriteString("    " + styleDim.Render("no matching process — c clears filters, / edits") + "\n")
	}
	rest := len(rows) - end
	var notes []string
	if start > 0 {
		notes = append(notes, fmt.Sprintf("↑%d", start))
	}
	if rest > 0 {
		notes = append(notes, fmt.Sprintf("↓%d", rest))
	}
	if len(notes) > 0 {
		list.WriteString("    " + styleDim.Render(strings.Join(notes, " · ")+" hidden") + "\n")
	}
	return strings.TrimRight(list.String(), "\n")
}

func sortIndicator(active, key string) string {
	if active != key {
		return " "
	}
	return styleAccent.Render(sortDirGlyph(key))
}

// renderMeta: only emit non-default state (filter, guard on, raw names, custom tick).
// keeps chrome quiet when nothing's overridden.
func (m model) renderMeta() string {
	dot := styleDimmer.Render(" · ")
	bits := []string{}
	if m.guardOn {
		bits = append(bits, styleOk.Render("guard ≥ "+fmtSize(m.guardCap)+" free"))
	}
	if m.rawNameMode {
		bits = append(bits, styleAccent.Render("raw-names"))
	}
	if m.catFilter != "" {
		bits = append(bits, lipgloss.NewStyle().Foreground(catColor(m.catFilter)).Render(
			catGlyph(m.catFilter)+" "+m.catFilter))
	}
	if m.filtering {
		bits = append(bits, styleAccent.Render("filter: ")+m.filter+styleDim.Render("█"))
	} else if m.filter != "" {
		bits = append(bits, styleDim.Render("filter: ")+m.filter)
	}
	if m.sortBy != "mem" {
		bits = append(bits, styleDim.Render("sort ")+lipgloss.NewStyle().Foreground(colFg).Render(
			m.sortBy+" "+sortDirGlyph(m.sortBy)))
	}
	if d := time.Duration(m.refreshMs) * time.Millisecond; d != tickInterval && m.refreshMs > 0 {
		bits = append(bits, styleDim.Render(fmt.Sprintf("tick %dms", m.refreshMs)))
	}
	if len(bits) == 0 {
		return ""
	}
	return strings.Join(bits, dot)
}

func (m model) renderStatus() string {
	if m.status == "" || time.Since(m.statusAt) >= 4*time.Second {
		return ""
	}
	st := styleWarn
	if l := strings.ToLower(m.status); strings.Contains(l, "failed") || strings.Contains(l, "refused") {
		st = styleCrit
	}
	return st.Render("· " + m.status)
}

func (m model) renderHint() string {
	w := m.contentW()
	hint := styleDim.Render("?  help") + styleDimmer.Render("  ·  ") +
		styleDim.Render("/  filter") + styleDimmer.Render("  ·  ") +
		styleDim.Render("s  sort") + styleDimmer.Render("  ·  ") +
		styleDim.Render("d  detail") + styleDimmer.Render("  ·  ") +
		styleDim.Render("q  quit")
	if lipgloss.Width(hint) > w-4 {
		hint = styleDim.Render("? help · q quit")
	}
	pad := w - 4 - lipgloss.Width(hint)
	if pad < 0 {
		pad = 0
	}
	return strings.Repeat(" ", pad) + hint
}

// renderDetail: 6-line right-padded pane below the list when showDetail=true.
func (m model) renderDetail(w int) string {
	if !m.showDetail {
		return ""
	}
	v, ok := m.rowAt(m.cursor)
	if !ok {
		return styleDetail.Render(styleDim.Render("no process selected"))
	}
	// detail box: border(1) + padding(2) + key column(9) + pane pad(4)
	innerW := m.contentW() - 16
	if innerW < 24 {
		innerW = 24
	}
	kv := func(k, v string) string {
		return styleSection.Render(fmt.Sprintf("%-9s", k)) + lipgloss.NewStyle().Foreground(colFg).Render(v)
	}
	gly := lipgloss.NewStyle().Foreground(catColor(v.category)).Bold(true).Render(catGlyph(v.category))

	if v.group {
		header := gly + " " + styleTitle.Render(v.name) +
			styleDim.Render(fmt.Sprintf("  ×%d procs", v.count))
		stopped := 0
		for _, p := range v.members {
			if p.stopped {
				stopped++
			}
		}
		distinct := map[string]bool{}
		for _, p := range v.members {
			distinct[p.name] = true
		}
		// largest first, independent of the active list sort
		top := append([]procRow(nil), v.members...)
		sort.SliceStable(top, func(i, j int) bool { return top[i].memMB > top[j].memMB })
		if len(top) > 4 {
			top = top[:4]
		}
		sizes := make([]string, 0, len(top))
		for _, p := range top {
			sizes = append(sizes, fmt.Sprintf("%s (%d)", fmtSize(p.memMB), p.pid))
		}
		lines := []string{
			header,
			kv("mem·cpu", fmt.Sprintf("%s total   cpu %.1f%%   paused %d/%d",
				fmtSize(v.memMB), v.cpu, stopped, v.count)),
			kv("oldest", fmtUptime(v.createMs)+styleDim.Render(" · "+v.category)),
			kv("members", styleDim.Render(fmt.Sprintf("%d procs · %d distinct names",
				v.count, len(distinct)))),
			kv("keys", styleDim.Render("e expand/collapse · t/K/p act on all members")),
			kv("largest", styleSub.Render(strings.Join(sizes, " · "))),
		}
		return styleDetail.Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
	}

	p := v.members[0]
	cmd := p.cmdline
	if cmd == "" {
		cmd = p.exe
	}
	cmd = strings.ReplaceAll(cmd, "\x00", " ")
	cmd = truncRight(cmd, innerW)
	exe := truncRight(p.exe, innerW)

	header := gly + " " + styleTitle.Render(p.name)
	if p.version != "" {
		header += styleDim.Render("  v" + p.version)
	}
	stateMark := "•"
	stateCol := colOk
	if p.stopped {
		stateMark = "‖"
		stateCol = colWarn
	}
	state := lipgloss.NewStyle().Foreground(stateCol).Render(stateMark)

	lines := []string{
		header,
		kv("pid·ppid", fmt.Sprintf("%d  ↰ %d   %s   user %s",
			p.pid, p.ppid, state, p.user)),
		kv("mem·cpu", fmt.Sprintf("%s  (%.1f%% sys)   cpu %.1f%%   threads %d",
			fmtSize(p.memMB), p.memPct, p.cpu, p.threads)),
		kv("uptime", fmtUptime(p.createMs)+styleDim.Render(" · "+p.category)),
		kv("exe", styleDim.Render(exe)),
		kv("cmd", styleDim.Render(cmd)),
	}
	return styleDetail.Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}

func renderHelp() string {
	lines := []string{
		styleTitle.Render("keys"),
		styleDim.Render("  jk / ↑↓        move          gG  top/bottom"),
		styleDim.Render("  ctrl-d/u       half-page     ctrl-f/b  page"),
		styleDim.Render("  K              kill (SIGKILL, confirm)"),
		styleDim.Render("  t              term (SIGTERM, confirm)"),
		styleDim.Render("  C              claude-cleanup: reap idle bg-spare/pty-host"),
		styleDim.Render("  X              NUKE: kill ALL claude/anthropic procs (free RAM)"),
		styleDim.Render("  space / p      pause (SIGSTOP)         r  resume (SIGCONT)"),
		styleDim.Render("  u              ungroup: one row per process"),
		styleDim.Render("  o              jump to ollama/omlx     n  toggle raw process names"),
		styleDim.Render("  d              toggle detail pane (selected row)"),
		styleDim.Render("  e / enter      expand/collapse selected app group"),
		styleDim.Render("  s              cycle sort (mem→cpu→Δ→category→name→time→pid)"),
		styleDim.Render("  /              filter (esc cancels)   c  clear filters"),
		styleDim.Render("  1-9            filter to nth category (again to clear)"),
		styleDim.Render("  R              resume every paused process"),
		styleDim.Render("  L              guard toggle      + -  guard cap step ±512MB"),
		styleDim.Render("  < >            slower / faster tick"),
		styleDim.Render("  q / ctrl-c     quit"),
	}
	return strings.Join(lines, "\n")
}

func overlayNuke(base string, pl nukePlan, w, h int) string {
	header := fmt.Sprintf("NUKE  ·  kill ALL claude/anthropic  ·  %d proc(s)  ~%s",
		len(pl.targets), fmtSize(pl.totalMB))
	lines := []string{styleSelRow.Render(header), ""}
	maxRows := 10
	for i, p := range pl.targets {
		if i >= maxRows {
			lines = append(lines, styleDim.Render(fmt.Sprintf("  … +%d more", len(pl.targets)-maxRows)))
			break
		}
		label := p.name
		if label == "" {
			label = p.rawName
		}
		lines = append(lines, styleSub.Render(fmt.Sprintf("  pid %-6d  %-22s  %s  %s",
			p.pid, truncRight(label, 22), fmtSize(p.memMB), fmtUptime(p.createMs))))
	}
	if pl.keptSelf > 0 {
		lines = append(lines, "", styleDim.Render(fmt.Sprintf(
			"kept: %d self/ancestor (memhog + shell chain)", pl.keptSelf)))
	}
	lines = append(lines,
		"",
		styleDim.Render("this kills daemon + foreground claude + MCP helpers + claude.app."),
		styleDim.Render("y to nuke   n to cancel   (SIGKILL escalation in 1s)"))
	box := styleConfirm.Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
	return base + "\n" + lipgloss.NewStyle().PaddingLeft(3).Render(box)
}

func overlayCleanup(base string, pl cleanupPlan, w, h int) string {
	header := fmt.Sprintf("claude-cleanup  ·  SIGTERM %d proc(s)", len(pl.targets))
	lines := []string{styleSelRow.Render(header), ""}
	maxRows := 8
	for i, p := range pl.targets {
		if i >= maxRows {
			lines = append(lines, styleDim.Render(fmt.Sprintf("  … +%d more", len(pl.targets)-maxRows)))
			break
		}
		kind := "spare"
		if isClaudePtyHost(p.cmdline) {
			kind = "pty-host"
		}
		lines = append(lines, styleSub.Render(fmt.Sprintf("  %-9s  pid %-6d  %s  %s",
			kind, p.pid, fmtSize(p.memMB), fmtUptime(p.createMs))))
	}
	if pl.keptDaemon > 0 || pl.keptTTY > 0 {
		lines = append(lines, "", styleDim.Render(fmt.Sprintf(
			"kept: %d daemon root · %d live pty-host", pl.keptDaemon, pl.keptTTY)))
	}
	lines = append(lines, "", styleDim.Render("y to confirm   n to cancel   (SIGKILL escalation in 1s)"))
	box := styleConfirm.Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
	return base + "\n" + lipgloss.NewStyle().PaddingLeft(3).Render(box)
}

func overlayConfirm(base string, kind confirmAction, p procRow, count int, w, h int) string {
	verb := "terminate"
	if kind == confirmKill {
		verb = "kill"
	}
	msg := fmt.Sprintf("%s  %s  (%d)?", verb, p.name, p.pid)
	switch {
	case count > 1:
		msg = fmt.Sprintf("%s  %s  ·  %d proc(s)  ~%s?", verb, p.name, count, fmtSize(p.memMB))
	case count == 1:
		msg = fmt.Sprintf("%s  %s  (%d)?", verb, p.name, p.pid)
	}
	hint := styleDim.Render("y to confirm   n to cancel")
	box := styleConfirm.Render(
		lipgloss.JoinVertical(lipgloss.Left,
			styleSelRow.Render(msg),
			"",
			hint,
		),
	)
	return base + "\n" + lipgloss.NewStyle().PaddingLeft(3).Render(box)
}
