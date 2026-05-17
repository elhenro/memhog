package main

import (
	"fmt"
	"os"
	"runtime"
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

// truncRight: trim user name when long.
func truncRight(s string, n int) string {
	if n <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(rs[:n-1]) + "…"
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

// reserved-rows: header(4) + blank + group(1) + blank + tbl-hdr(1) + blank + hint(1) + pane pad(2) = 11
func (m model) listLimit() int {
	h := m.h
	if h <= 0 {
		h = 24
	}
	reserved := 11
	if m.showHelp {
		reserved += 14
	}
	if m.showDetail {
		reserved += 8
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

func (m model) barWidth() int {
	w := m.w
	if w <= 0 {
		w = 80
	}
	// reserve: label(4) + gap(1) + spark(16) + gap(2) + suffix(~22) + pane pad(4)
	bw := w - 4 - 1 - 16 - 2 - 22 - 4
	if bw < 22 {
		bw = 22
	}
	if bw > 100 {
		bw = 100
	}
	return bw
}

func (m model) View() string {
	if m.err != nil {
		return styleCrit.Render("error  ") + m.err.Error()
	}
	if m.vm == nil {
		return styleDim.Render("loading…")
	}
	w := m.w
	if w <= 0 {
		w = 80
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

	out := stylePane.Render(body)
	if m.confirm == confirmCleanup {
		out = overlayCleanup(out, m.cleanupPl, w, m.h)
	} else if m.confirm == confirmNuke {
		out = overlayNuke(out, m.nukePl, w, m.h)
	} else if m.confirm != confirmNone {
		out = overlayConfirm(out, m.confirm, m.confirmP, w, m.h)
	}
	return out
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
	pad := w - 4 - lipgloss.Width(title) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	titleLine := title + strings.Repeat(" ", pad) + right

	bw := m.barWidth()
	labelW := 4
	sparkW := 16
	if bw < 40 {
		sparkW = 10
	}

	memBar := bar(usedPct, bw, memColor(usedPct))
	memSpark := sparkline(m.memHist, sparkW, memColor(usedPct))
	memSuffix := styleDim.Render(fmt.Sprintf("%.0fGB · %.0fGB free", totalGB, availGB))
	memLine := styleSection.Render(fmt.Sprintf("%-*s", labelW, "mem")) +
		memBar + " " + memSpark + "  " + memSuffix

	cpuPct := m.cpuTotal
	cpuBar := bar(cpuPct, bw, cpuColor(cpuPct))
	cpuSpark := sparkline(m.cpuHist, sparkW, cpuColor(cpuPct))
	cpuSuffix := ""
	if m.load != nil {
		cpuSuffix = styleDim.Render(fmt.Sprintf("%.2f %.2f %.2f · %dc",
			m.load.Load1, m.load.Load5, m.load.Load15, runtime.NumCPU()))
	} else {
		cpuSuffix = styleDim.Render(fmt.Sprintf("%dc", runtime.NumCPU()))
	}
	cpuLine := styleSection.Render(fmt.Sprintf("%-*s", labelW, "cpu")) +
		cpuBar + " " + cpuSpark + "  " + cpuSuffix

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
	limit := w - 6 - used
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
			parts = append(parts, styleDim.Render(fmt.Sprintf("+%d", len(m.groups)-shown)))
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

func sortGlyph(active bool, key, current string) string {
	if !active || key != current {
		return ""
	}
	return styleAccent.Render("↓")
}

func (m model) renderList(w int) string {
	rows := m.filtered()
	lim := m.listLimit()
	cursor := m.cursor
	if cursor >= len(rows) {
		cursor = len(rows) - 1
	}
	if cursor < 0 {
		cursor = 0
	}

	// dense layout: mark(2) + glyph(1) + space + name(flex) + mem(8) + trend(6) + cpu(7) + time(9) + pid(7)
	memW := 8
	trendW := 6
	cpuW := 7
	timeW := 9
	pidW := 7
	gap := 3
	fixed := memW + trendW + cpuW + timeW + pidW + 4*gap
	nameW := w - 4 - fixed - 4
	if nameW < 14 {
		nameW = 14
	}

	hdr := fmt.Sprintf("    %-*s   %*s%s   %*s   %*s%s   %*s   %*s%s",
		nameW, "process",
		memW-1, "mem", sortIfActive(m.sortBy, "mem"),
		trendW, "Δ",
		cpuW-1, "cpu", sortIfActive(m.sortBy, "cpu"),
		timeW, "time",
		pidW-1, "pid", sortIfActive(m.sortBy, "pid"),
	)
	subHdr := styleDim.Render(hdr)

	var list strings.Builder
	list.WriteString(subHdr + "\n")

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

	lineW := w - 4 // strip pane pad
	for i := start; i < end; i++ {
		p := rows[i]
		selected := i == cursor

		mark := "  "
		if selected {
			mark = styleAccent.Render("▎ ")
		}
		gly := lipgloss.NewStyle().Foreground(catColor(p.category)).Render(catGlyph(p.category))
		name := p.name
		if m.rawNameMode && p.rawName != "" {
			name = p.rawName
		}
		if p.stopped {
			name = "‖ " + name
		}
		name = truncRight(name, nameW)

		trend := trendGlyph(p.trend, p.deltaMB)
		trendCell := fmt.Sprintf("%*s", trendW, "")
		if trend != "" {
			pad := trendW - lipgloss.Width(trend)
			if pad < 0 {
				pad = 0
			}
			trendCell = strings.Repeat(" ", pad) + trend
		}

		line := fmt.Sprintf("%-*s   %*s   %s   %*s   %*s   %*d",
			nameW, name,
			memW, fmtSize(p.memMB),
			trendCell,
			cpuW, fmt.Sprintf("%.1f%%", p.cpu),
			timeW, fmtUptime(p.createMs),
			pidW, p.pid)

		row := mark + gly + " " + line
		switch {
		case selected:
			// full-width band — pad row to lineW then apply selected bg
			padN := lineW - lipgloss.Width(row)
			if padN < 0 {
				padN = 0
			}
			row = styleSelBg.Render(row + strings.Repeat(" ", padN))
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
	rest := len(rows) - end
	if rest > 0 {
		list.WriteString("    " + styleDim.Render(fmt.Sprintf("+%d more  ↓ scroll", rest)) + "\n")
	}
	if start > 0 {
		list.WriteString("    " + styleDim.Render(fmt.Sprintf("↑ %d hidden above", start)) + "\n")
	}
	return strings.TrimRight(list.String(), "\n")
}

func sortIfActive(active, key string) string {
	if active == key {
		return styleAccent.Render("↓")
	}
	return " "
}

// renderMeta: only emit non-default state (filter, guard on, raw names, custom tick).
// keeps chrome quiet when nothing's overridden.
func (m model) renderMeta() string {
	dot := styleDimmer.Render(" · ")
	bits := []string{}
	if m.guardOn {
		bits = append(bits, styleOk.Render(fmt.Sprintf("guard ≥ %.0fGB free", m.guardCap/1024)))
	}
	if m.rawNameMode {
		bits = append(bits, styleAccent.Render("raw-names"))
	}
	if m.filtering {
		bits = append(bits, styleAccent.Render("filter: ")+m.filter+styleDim.Render("█"))
	} else if m.filter != "" {
		bits = append(bits, styleDim.Render("filter: ")+m.filter)
	}
	if m.sortBy != "mem" {
		bits = append(bits, styleDim.Render("sort ")+lipgloss.NewStyle().Foreground(colFg).Render(m.sortBy))
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
	return styleWarn.Render("· " + m.status)
}

func (m model) renderHint() string {
	if m.showHelp {
		return renderHelp()
	}
	w := m.w
	if w <= 0 {
		w = 80
	}
	hint := styleDim.Render("?  help")
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
	rows := m.filtered()
	p, ok := procAtCursor(rows, m.cursor)
	if !ok {
		return styleDetail.Render(styleDim.Render("no process selected"))
	}
	innerW := w - 8
	if innerW < 40 {
		innerW = 40
	}
	cmd := p.cmdline
	if cmd == "" {
		cmd = p.exe
	}
	cmd = strings.ReplaceAll(cmd, "\x00", " ")
	cmd = truncRight(cmd, innerW)
	exe := truncRight(p.exe, innerW)

	gly := lipgloss.NewStyle().Foreground(catColor(p.category)).Bold(true).Render(catGlyph(p.category))
	header := gly + " " + styleTitle.Render(p.name)
	if p.version != "" {
		header += styleDim.Render("  v"+p.version)
	}

	kv := func(k, v string) string {
		return styleSection.Render(fmt.Sprintf("%-9s", k)) + lipgloss.NewStyle().Foreground(colFg).Render(v)
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
		styleAccent.Render("keys"),
		styleDim.Render("  jk / ↑↓        move          gG  top/bottom"),
		styleDim.Render("  ctrl-d/u       half-page     ctrl-f/b  page"),
		styleDim.Render("  K              kill (SIGKILL, confirm)"),
		styleDim.Render("  t              term (SIGTERM, confirm)"),
		styleDim.Render("  C              claude-cleanup: reap idle bg-spare/pty-host"),
		styleDim.Render("  X              NUKE: kill ALL claude/anthropic procs (free RAM)"),
		styleDim.Render("  space / p      pause (SIGSTOP)         r  resume (SIGCONT)"),
		styleDim.Render("  o              jump to ollama/omlx     n  toggle raw process names"),
		styleDim.Render("  d              toggle detail pane (selected proc)"),
		styleDim.Render("  s              cycle sort (mem→cpu→category→name→pid)"),
		styleDim.Render("  /              filter            c  clear filter"),
		styleDim.Render("  1-9            filter to nth category from groups band"),
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

func overlayConfirm(base string, kind confirmAction, p procRow, w, h int) string {
	verb := "terminate"
	if kind == confirmKill {
		verb = "kill"
	}
	msg := fmt.Sprintf("%s  %s  (%d)?", verb, p.name, p.pid)
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
