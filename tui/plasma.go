package tui

import (
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// PlasmaModel renders a continuously evolving plasma field.
// Pure ambient chrome — no data, no meaning, just motion.
type PlasmaModel struct {
	Frame int
}

func NewPlasmaModel() *PlasmaModel {
	return &PlasmaModel{}
}

func (m *PlasmaModel) Name() string { return "Plasma" }

func (m *PlasmaModel) Tick() {
	m.Frame++
}

// Palette: 80s synthwave neon. Deep purple midnight sliding through magenta
// and hot pink to sunset orange, with a cyan neon highlight at the top end.
// Skips green entirely; orange/amber are fine in isolation.
var plasmaPalette = []lipgloss.Color{
	lipgloss.Color("#3d0066"), // deep purple
	lipgloss.Color("#5a189a"), // royal purple
	lipgloss.Color("#7b2cbf"), // purple
	lipgloss.Color("#9d4edd"), // lavender-purple
	lipgloss.Color("#c77dff"), // light purple
	lipgloss.Color("#ff10f0"), // neon magenta
	lipgloss.Color("#ff006e"), // hot pink
	lipgloss.Color("#ff4d8d"), // pink
	lipgloss.Color("#ff5e00"), // neon orange
	lipgloss.Color("#ff9e00"), // amber
	lipgloss.Color("#00f5ff"), // electric cyan
	lipgloss.Color("#4cc9f0"), // sky
}

// Density ramp: low-intensity cells are spaces so the terminal background
// shows through, high-intensity cells are solid neon blocks.
var plasmaChars = []rune{' ', '·', '░', '▒', '▓', '█'}

func (m PlasmaModel) View(width, height int, focused bool) string {
	innerW := width - 4
	innerH := height - 2
	if innerW < 2 || innerH < 1 {
		return ""
	}

	// Tuned to match 8fps feel at 16fps (half the per-frame delta).
	t := float64(m.Frame) * 0.04
	cx := float64(innerW) / 2
	cy := float64(innerH) / 2

	// Compute intensity per cell, then render row by row, grouping consecutive
	// cells with the same color for fewer style escapes.
	var sb strings.Builder
	for y := 0; y < innerH; y++ {
		fy := float64(y) * 1.6 // rows are ~2x taller than cols in a terminal; stretch y for circular symmetry
		var prevColor lipgloss.Color = ""
		var runBuf strings.Builder
		flush := func() {
			if runBuf.Len() == 0 {
				return
			}
			if prevColor == "" {
				sb.WriteString(runBuf.String())
			} else {
				sb.WriteString(lipgloss.NewStyle().Foreground(prevColor).Render(runBuf.String()))
			}
			runBuf.Reset()
		}

		for x := 0; x < innerW; x++ {
			fx := float64(x)

			// Four overlapping sine waves — classic plasma.
			dx := fx - cx
			dy := fy - cy*1.6
			r := math.Sqrt(dx*dx + dy*dy)

			v := math.Sin(fx*0.22+t) +
				math.Sin(fy*0.18+t*1.1) +
				math.Sin((fx+fy)*0.12+t*0.7) +
				math.Sin(r*0.25+t*0.6)

			// v ∈ [-4, 4] → norm ∈ [0, 1]
			norm := (v + 4) / 8
			if norm < 0 {
				norm = 0
			}
			if norm > 1 {
				norm = 1
			}

			colorIdx := int(norm * float64(len(plasmaPalette)-1))
			charIdx := int(norm * float64(len(plasmaChars)-1))
			color := plasmaPalette[colorIdx]
			ch := plasmaChars[charIdx]

			if color != prevColor {
				flush()
				prevColor = color
			}
			runBuf.WriteRune(ch)
		}
		flush()
		if y < innerH-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
