package tui

import (
	"math"
	"math/rand"

	"github.com/charmbracelet/lipgloss"
)

// StarfieldModel renders a radial warp starfield: stars spawn near center,
// accelerate outward, respawn when they exit the frame.
type StarfieldModel struct {
	Frame int
	stars []warpStar
	w, h  int
	rng   *rand.Rand
}

type warpStar struct {
	angle float64
	r     float64
	speed float64
}

const warpStarCount = 60

var warpColors = []lipgloss.Color{
	lipgloss.Color("#394b70"), // far: dim indigo
	lipgloss.Color("#6c7086"),
	lipgloss.Color("#7287fd"),
	lipgloss.Color("#89b4fa"),
	lipgloss.Color("#b4befe"),
	lipgloss.Color("#cdd6f4"), // near: bright
}
var warpChars = []rune{'·', '·', '∘', '○', '●', '✦'}

func NewStarfieldModel() *StarfieldModel {
	return &StarfieldModel{rng: rand.New(rand.NewSource(42))}
}

func (m *StarfieldModel) Name() string { return "Stars" }

func (m *StarfieldModel) Tick() { m.Frame++ }

func (m *StarfieldModel) View(width, height int, focused bool) string {
	innerW := width - 4
	innerH := height - 2
	if innerW < 4 || innerH < 3 {
		return ""
	}
	if innerW != m.w || innerH != m.h || len(m.stars) == 0 {
		m.w, m.h = innerW, innerH
		m.respawnAll()
	}
	m.update()

	buf := newVizBuf(innerW, innerH)

	cx := float64(innerW) / 2
	cy := float64(innerH) / 2
	// stretch so motion looks radially symmetric despite 2:1 cell aspect
	const yStretch = 2.0

	for _, s := range m.stars {
		x := int(math.Round(cx + s.r*math.Cos(s.angle)))
		y := int(math.Round(cy + s.r*math.Sin(s.angle)/yStretch))
		if x < 0 || x >= innerW || y < 0 || y >= innerH {
			continue
		}
		// Depth bucket: normalize radius against the frame diagonal (scaled).
		maxR := math.Hypot(cx, cy*yStretch)
		depth := s.r / maxR
		if depth > 1 {
			depth = 1
		}
		bucket := int(depth * float64(len(warpChars)-1))
		if bucket >= len(warpChars) {
			bucket = len(warpChars) - 1
		}
		buf[y][x] = vizCell{ch: warpChars[bucket], color: warpColors[bucket]}
	}

	return renderVizBuf(buf)
}

func (m *StarfieldModel) respawnAll() {
	m.stars = make([]warpStar, warpStarCount)
	for i := range m.stars {
		m.stars[i] = m.birth(m.rng.Float64() * 3)
	}
}

func (m *StarfieldModel) birth(startR float64) warpStar {
	return warpStar{
		angle: m.rng.Float64() * 2 * math.Pi,
		r:     startR,
		speed: 0.08 + m.rng.Float64()*0.07,
	}
}

func (m *StarfieldModel) update() {
	cx := float64(m.w) / 2
	cy := float64(m.h) / 2
	const yStretch = 2.0
	maxR := math.Hypot(cx, cy*yStretch) * 1.1

	for i := range m.stars {
		// Accelerate with distance — closer to edge moves faster.
		m.stars[i].r += m.stars[i].speed * (1 + m.stars[i].r*0.12)
		if m.stars[i].r > maxR {
			m.stars[i] = m.birth(0)
		}
	}
}

