package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// eighth-block chars for sub-cell-precision bars and sparklines.
var blockEighths = []rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉', '█'}
var sparkBars = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// sparkline renders a compact unicode sparkline from a 0..maxVal series.
// width caps the rendered cells; oldest data is left-truncated.
func sparkline(series []float64, width int, c lipgloss.Color) string {
	if width <= 0 || len(series) == 0 {
		return strings.Repeat(" ", width)
	}
	start := 0
	if len(series) > width {
		start = len(series) - width
	}
	s := series[start:]
	maxV := 1.0
	for _, v := range s {
		if v > maxV {
			maxV = v
		}
	}
	st := lipgloss.NewStyle().Foreground(c)
	var b strings.Builder
	for _, v := range s {
		idx := int(v / maxV * float64(len(sparkBars)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkBars) {
			idx = len(sparkBars) - 1
		}
		b.WriteString(st.Render(string(sparkBars[idx])))
	}
	if pad := width - len(s); pad > 0 {
		b.WriteString(strings.Repeat(" ", pad))
	}
	return b.String()
}

// trendGlyph: arrow + delta. flat returns empty string for quiet rows.
func trendGlyph(trend int8, deltaMB float64) string {
	switch {
	case trend > 0:
		return lipgloss.NewStyle().Foreground(colWarn).Render(fmt.Sprintf("▲%.0f", deltaMB))
	case trend < 0:
		return lipgloss.NewStyle().Foreground(colOk).Render(fmt.Sprintf("▼%.0f", -deltaMB))
	}
	return ""
}

// palette — neutral chrome, single warm accent, plus per-category hues
var (
	colFg     = lipgloss.Color("#e6e6e6")
	colMute   = lipgloss.Color("#a8a8a8")
	colDim    = lipgloss.Color("#6c6c6c")
	colDimmer = lipgloss.Color("#2a2a2a")
	colBg     = lipgloss.Color("#0a0a0a")
	colAccent = lipgloss.Color("#ff9f0a") // honey
	colWarn   = lipgloss.Color("#ffb340")
	colCrit   = lipgloss.Color("#ff453a")
	colCool   = lipgloss.Color("#64d2ff")
	colOk     = lipgloss.Color("#30d158")
	colSel    = lipgloss.Color("#ffffff")

	// category hues — desaturated, distinct
	colCatAgent   = lipgloss.Color("#bf8cf2") // violet
	colCatLLM     = lipgloss.Color("#64d2ff") // cyan
	colCatBrowser = lipgloss.Color("#ffd060") // amber
	colCatEditor  = lipgloss.Color("#7fd182") // sage
	colCatVM      = lipgloss.Color("#ff8a65") // coral
	colCatSystem  = lipgloss.Color("#7a8290") // slate
	colCatRuntime = lipgloss.Color("#e6c66b") // wheat
	colCatOther   = lipgloss.Color("#9a9a9a") // grey

	styleTitle   = lipgloss.NewStyle().Bold(true).Foreground(colFg)
	styleSub     = lipgloss.NewStyle().Foreground(colMute)
	styleDim     = lipgloss.NewStyle().Foreground(colDim)
	styleDimmer  = lipgloss.NewStyle().Foreground(colDimmer)
	styleAccent  = lipgloss.NewStyle().Foreground(colAccent)
	styleWarn    = lipgloss.NewStyle().Foreground(colWarn)
	styleCrit    = lipgloss.NewStyle().Foreground(colCrit).Bold(true)
	styleOk      = lipgloss.NewStyle().Foreground(colOk)
	styleSelRow  = lipgloss.NewStyle().Foreground(colSel).Bold(true)
	styleHint    = lipgloss.NewStyle().Foreground(colDim)
	styleSection = lipgloss.NewStyle().Foreground(colMute)
	stylePane    = lipgloss.NewStyle().Padding(1, 2)
	styleConfirm = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colAccent).
			Padding(0, 2).
			Foreground(colFg)
	styleDetail = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, false, true).
			BorderForeground(colDimmer).
			PaddingLeft(2).
			Foreground(colFg)
	styleSelBg = lipgloss.NewStyle().
			Background(lipgloss.Color("#1a1410")).
			Foreground(colSel).
			Bold(true)
)

func memColor(pct float64) lipgloss.Color {
	switch {
	case pct >= 90:
		return colCrit
	case pct >= 75:
		return colWarn
	case pct >= 50:
		return colAccent
	default:
		return colOk
	}
}

func cpuColor(pct float64) lipgloss.Color {
	switch {
	case pct >= 80:
		return colCrit
	case pct >= 60:
		return colWarn
	case pct >= 30:
		return colCool
	default:
		return colOk
	}
}
