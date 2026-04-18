package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// ClockModel is a digital clock / pomodoro timer. It shows wall-clock time
// when idle, and runs a classic 4x (25/5) + 15 pomodoro cycle when started.
type ClockModel struct {
	mode     clockMode
	phase    pomoPhase
	running  bool
	phaseEnd time.Time     // when running
	remain   time.Duration // when paused
	workDone int           // 0..3 completed work phases in current cycle
}

type clockMode int

const (
	clockIdle clockMode = iota
	clockPomo
)

type pomoPhase int

const (
	phaseWork pomoPhase = iota
	phaseShortBreak
	phaseLongBreak
)

const (
	workDur       = 25 * time.Minute
	shortBreakDur = 5 * time.Minute
	longBreakDur  = 15 * time.Minute
	cycleWorks    = 4 // 4 work phases per cycle (last break is long)
)

func NewClockModel() *ClockModel { return &ClockModel{} }

func (m *ClockModel) Name() string { return "Clock" }

// Tick auto-advances the pomodoro phase when the timer runs out.
func (m *ClockModel) Tick() {
	if m.mode != clockPomo || !m.running {
		return
	}
	if time.Now().Before(m.phaseEnd) {
		return
	}
	m.advancePhase()
}

// TogglePomo: idle → start work, running → pause, paused → resume.
func (m *ClockModel) TogglePomo() {
	switch {
	case m.mode == clockIdle:
		m.mode = clockPomo
		m.phase = phaseWork
		m.workDone = 0
		m.startPhase(workDur)
	case m.running:
		m.remain = time.Until(m.phaseEnd)
		if m.remain < 0 {
			m.remain = 0
		}
		m.running = false
	default:
		m.phaseEnd = time.Now().Add(m.remain)
		m.running = true
	}
}

// Reset returns to the idle wall clock.
func (m *ClockModel) Reset() {
	m.mode = clockIdle
	m.running = false
	m.phase = phaseWork
	m.workDone = 0
	m.remain = 0
}

// SkipPhase ends the current phase immediately and advances to the next.
func (m *ClockModel) SkipPhase() {
	if m.mode != clockPomo {
		return
	}
	m.advancePhase()
}

func (m *ClockModel) advancePhase() {
	switch m.phase {
	case phaseWork:
		m.workDone++
		if m.workDone >= cycleWorks {
			m.phase = phaseLongBreak
			m.startPhase(longBreakDur)
		} else {
			m.phase = phaseShortBreak
			m.startPhase(shortBreakDur)
		}
	case phaseShortBreak:
		m.phase = phaseWork
		m.startPhase(workDur)
	case phaseLongBreak:
		m.phase = phaseWork
		m.workDone = 0
		m.startPhase(workDur)
	}
}

func (m *ClockModel) startPhase(d time.Duration) {
	m.phaseEnd = time.Now().Add(d)
	m.remain = d
	m.running = true
}

// ---- rendering ----

var (
	clockHeader   = lipgloss.Color("#b4befe")
	clockIdleCol  = lipgloss.Color("#b4befe")
	clockWorkCol  = lipgloss.Color("#ff5e9e")
	clockBreakCol = lipgloss.Color("#89b4fa")
	clockLongCol  = lipgloss.Color("#cba6f7")
	clockPauseCol = lipgloss.Color("#6c7086")
)

// 5-row big digits, 3 cols wide. Colon is 1 col.
var bigGlyphs = map[rune][5]string{
	'0': {"███", "█ █", "█ █", "█ █", "███"},
	'1': {"  █", "  █", "  █", "  █", "  █"},
	'2': {"███", "  █", "███", "█  ", "███"},
	'3': {"███", "  █", "███", "  █", "███"},
	'4': {"█ █", "█ █", "███", "  █", "  █"},
	'5': {"███", "█  ", "███", "  █", "███"},
	'6': {"███", "█  ", "███", "█ █", "███"},
	'7': {"███", "  █", "  █", "  █", "  █"},
	'8': {"███", "█ █", "███", "█ █", "███"},
	'9': {"███", "█ █", "███", "  █", "███"},
	':': {" ", " ", "█", " ", "█"},
	' ': {"   ", "   ", "   ", "   ", "   "},
}

func (m *ClockModel) View(width, height int, focused bool) string {
	innerW := width - 4
	innerH := height - 2
	if innerW < 10 || innerH < 3 {
		return ""
	}

	now := time.Now()
	var header, timeStr, footer string
	var mainColor lipgloss.Color

	switch m.mode {
	case clockIdle:
		header = strings.ToUpper(now.Format("Mon Jan 2"))
		timeStr = now.Format("15:04")
		mainColor = clockIdleCol
		footer = "p start  V pick"
	case clockPomo:
		var d time.Duration
		if m.running {
			d = time.Until(m.phaseEnd)
		} else {
			d = m.remain
		}
		if d < 0 {
			d = 0
		}
		timeStr = formatMMSS(d)

		switch m.phase {
		case phaseWork:
			header = fmt.Sprintf("WORK %d/%d", m.workDone+1, cycleWorks)
			mainColor = clockWorkCol
		case phaseShortBreak:
			header = "BREAK"
			mainColor = clockBreakCol
		case phaseLongBreak:
			header = "LONG BREAK"
			mainColor = clockLongCol
		}
		if !m.running {
			header += " · PAUSED"
			mainColor = clockPauseCol
		}
		if m.running {
			footer = "p pause  . skip  R reset"
		} else {
			footer = "p resume  R reset"
		}
	}

	// Scale vertical and horizontal independently so the glyphs can fatten
	// up to match terminal cell aspect (cells are ~2:1 tall).
	budgetW := innerW
	base := bigWidth(timeStr, 1)
	yScale := 1
	if base > 0 {
		yScale = innerH / 5
	}
	if yScale < 1 {
		yScale = 1
	}
	if yScale > 20 {
		yScale = 20
	}
	xScale := 1
	if base > 0 {
		xScale = budgetW / base
	}
	if xScale < 1 {
		xScale = 1
	}
	if xScale > 20 {
		xScale = 20
	}

	digitRows := 5 * yScale
	leftover := innerH - digitRows
	if leftover < 0 {
		leftover = 0
	}
	showHeader := leftover >= 1 && header != ""
	extraAfterHeader := leftover
	if showHeader {
		extraAfterHeader--
	}
	showFooter := extraAfterHeader >= 1 && footer != ""
	// Optional blank gap rows if there's still room to breathe.
	gapAboveDigits := 0
	gapBelowDigits := 0
	extra := leftover
	if showHeader {
		extra--
	}
	if showFooter {
		extra--
	}
	if extra >= 2 && showHeader {
		gapAboveDigits = 1
		extra--
	}
	if extra >= 1 && showFooter {
		gapBelowDigits = 1
		extra--
	}
	topPad := extra / 2
	bottomPad := extra - topPad

	var sb strings.Builder
	write := func(s string) { sb.WriteString(s); sb.WriteByte('\n') }
	blank := func() { sb.WriteByte('\n') }

	for i := 0; i < topPad; i++ {
		blank()
	}
	if showHeader {
		write(centerLine(header, innerW, lipgloss.NewStyle().Foreground(clockHeader).Bold(true)))
		for i := 0; i < gapAboveDigits; i++ {
			blank()
		}
	}

	// Big digits (or compact fallback if no width for even scale 1).
	if budgetW >= base {
		big := renderBig(timeStr, mainColor, xScale, yScale)
		w := bigWidth(timeStr, xScale)
		pad := (innerW - w) / 2
		if pad < 0 {
			pad = 0
		}
		padStr := strings.Repeat(" ", pad)
		for _, line := range big {
			write(padStr + line)
		}
	} else {
		write(centerLine(timeStr, innerW, lipgloss.NewStyle().Foreground(mainColor).Bold(true)))
	}

	if showFooter {
		for i := 0; i < gapBelowDigits; i++ {
			blank()
		}
		write(centerLine(footer, innerW, lipgloss.NewStyle().Foreground(clockPauseCol)))
	}
	for i := 0; i < bottomPad; i++ {
		blank()
	}

	rendered := strings.TrimRight(sb.String(), "\n")
	for strings.Count(rendered, "\n")+1 < innerH {
		rendered += "\n"
	}
	return rendered
}

// bigWidth returns the rendered cell width of `s` as big digits at the given
// scale (sum of per-glyph widths plus one separator per gap).
func bigWidth(s string, scale int) int {
	if scale < 1 {
		scale = 1
	}
	w := 0
	for i, r := range s {
		g, ok := bigGlyphs[r]
		if !ok {
			g = bigGlyphs[' ']
		}
		w += len(g[0]) * scale
		if i < len(s)-1 {
			w += scale
		}
	}
	return w
}

// renderBig produces 5*yScale colored lines. Each source cell is expanded
// to xScale columns wide × yScale rows tall.
func renderBig(s string, color lipgloss.Color, xScale, yScale int) []string {
	if xScale < 1 {
		xScale = 1
	}
	if yScale < 1 {
		yScale = 1
	}
	rowsOut := 5 * yScale
	rows := make([]string, rowsOut)
	style := lipgloss.NewStyle().Foreground(color)
	sep := strings.Repeat(" ", xScale)

	for outRow := 0; outRow < rowsOut; outRow++ {
		srcRow := outRow / yScale
		var parts []string
		for _, r := range s {
			g, ok := bigGlyphs[r]
			if !ok {
				g = bigGlyphs[' ']
			}
			line := g[srcRow]
			if xScale == 1 {
				parts = append(parts, style.Render(line))
				continue
			}
			var expanded strings.Builder
			for _, c := range line {
				for k := 0; k < xScale; k++ {
					expanded.WriteRune(c)
				}
			}
			parts = append(parts, style.Render(expanded.String()))
		}
		rows[outRow] = strings.Join(parts, sep)
	}
	return rows
}

func centerLine(text string, width int, style lipgloss.Style) string {
	if len(text) >= width {
		return style.Render(text)
	}
	pad := (width - len(text)) / 2
	return strings.Repeat(" ", pad) + style.Render(text)
}

func formatMMSS(d time.Duration) string {
	total := int(d.Round(time.Second).Seconds())
	if total < 0 {
		total = 0
	}
	m := total / 60
	s := total % 60
	return fmt.Sprintf("%02d:%02d", m, s)
}
