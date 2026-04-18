package tui

import (
	"hash/fnv"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/jhoot/cockpit/sources"
)

// ConstellationModel plots each repo as a named star. Positions are stable
// (derived from label hash), brightness twinkles over time, and glyph size
// encodes whether the repo has activity (dirty/unpushed/behind).
type ConstellationModel struct {
	Frame int
	repos []sources.GitRepoStatus
}

func NewConstellationModel() *ConstellationModel { return &ConstellationModel{} }

func (m *ConstellationModel) Name() string { return "Constellation" }

func (m *ConstellationModel) Tick() { m.Frame++ }

func (m *ConstellationModel) SetRepos(repos []sources.GitRepoStatus) {
	m.repos = repos
}

var constellationColors = []lipgloss.Color{
	lipgloss.Color("#45475a"), // dim
	lipgloss.Color("#6c7086"),
	lipgloss.Color("#7287fd"),
	lipgloss.Color("#89b4fa"),
	lipgloss.Color("#b4befe"),
	lipgloss.Color("#cdd6f4"), // bright
}

func (m *ConstellationModel) View(width, height int, focused bool) string {
	innerW := width - 4
	innerH := height - 2
	if innerW < 6 || innerH < 3 {
		return ""
	}
	if len(m.repos) == 0 {
		return MutedText.Render("(no projects)")
	}

	buf := newVizBuf(innerW, innerH)

	phase := float64(m.Frame) * 0.12

	for i, r := range m.repos {
		px, py := starPosition(r.Label, innerW, innerH, i)
		if px < 0 || px >= innerW || py < 0 || py >= innerH {
			continue
		}

		active := r.Dirty || r.Unpushed > 0 || r.Behind > 0

		// Twinkle: 0..1 from a per-star phase offset.
		starPhase := phasePerStar(r.Label)
		twinkle := (math.Sin(phase+starPhase) + 1) / 2

		// Active stars stay bright; dormant fade more.
		var bucket int
		if active {
			bucket = 3 + int(twinkle*float64(len(constellationColors)-4))
		} else {
			bucket = int(twinkle * float64(len(constellationColors)-2))
		}
		if bucket >= len(constellationColors) {
			bucket = len(constellationColors) - 1
		}

		var glyph rune
		switch {
		case active && twinkle > 0.75:
			glyph = '✦'
		case active:
			glyph = '●'
		case twinkle > 0.5:
			glyph = '○'
		default:
			glyph = '·'
		}
		buf[py][px] = vizCell{ch: glyph, color: constellationColors[bucket]}

		// Label: two-char initials to the right of the star when room allows.
		label := labelInitials(r.Label, 2)
		for k, ch := range label {
			lx := px + 2 + k
			if lx < 0 || lx >= innerW {
				break
			}
			// Don't overwrite another star that already claimed the cell.
			if buf[py][lx].ch != ' ' {
				continue
			}
			buf[py][lx] = vizCell{ch: ch, color: lipgloss.Color("#6c7086")}
		}
	}

	return renderVizBuf(buf)
}

// starPosition maps a repo label to a stable (x, y) within the pane. The
// index is mixed in so duplicate labels (shouldn't happen, but still) don't
// collide on the exact same cell.
func starPosition(label string, w, h, idx int) (int, int) {
	h1 := fnv.New32a()
	h1.Write([]byte(label))
	_, _ = h1.Write([]byte{byte(idx)})
	v := h1.Sum32()

	// Avoid the outer 1-cell margin.
	xSpan := w - 2
	ySpan := h - 1
	if xSpan < 1 {
		xSpan = 1
	}
	if ySpan < 1 {
		ySpan = 1
	}
	x := 1 + int(v%uint32(xSpan))
	y := int((v/uint32(xSpan))%uint32(ySpan)) + 0
	return x, y
}

func phasePerStar(label string) float64 {
	h := fnv.New32a()
	h.Write([]byte(label))
	return float64(h.Sum32()%1000) / 1000 * 2 * math.Pi
}

// labelInitials returns up to n characters for a star label: first letter plus
// trailing consonants ("cockpit" -> "cp" at n=2) for better disambiguation than
// a raw prefix. Falls back to the prefix.
func labelInitials(s string, n int) string {
	if n <= 0 {
		return ""
	}
	s = strings.ToLower(s)
	if len(s) <= n {
		return s
	}
	if n >= 2 && len(s) >= 4 {
		first := s[0]
		var tail []byte
		for i := len(s) - 1; i >= 1 && len(tail) < n-1; i-- {
			c := s[i]
			if !isConstellationVowel(c) && c >= 'a' && c <= 'z' {
				tail = append([]byte{c}, tail...)
			}
		}
		if len(tail) == n-1 {
			return string(first) + string(tail)
		}
	}
	return s[:n]
}

func isConstellationVowel(c byte) bool {
	return c == 'a' || c == 'e' || c == 'i' || c == 'o' || c == 'u'
}
