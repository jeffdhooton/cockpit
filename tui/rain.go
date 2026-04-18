package tui

import (
	"math"
	"math/rand"

	"github.com/charmbracelet/lipgloss"
	"github.com/jhoot/cockpit/sources"
)

// RainModel renders diagonal rain streaks with occasional lightning flashes.
// Intensity (density of drops) tracks recent git activity: more dirty or
// unpushed repos → heavier rain.
type RainModel struct {
	Frame int
	drops []raindrop
	w, h  int
	rng   *rand.Rand

	intensity float64 // 0..1, smoothed activity proxy

	// Lightning: countdown until next potential strike, current flash fade.
	strikeCooldown int
	flashT         int
	flashCol       int
	flashBranches  [][2]int // (x,y) pairs along the fork
}

type raindrop struct {
	x, y   float64
	speed  float64
	length int
}

const (
	rainSlopeX = 1.0 // rightward drift per tick (cells)
	rainMinSpeed = 0.8
	rainMaxSpeed = 1.8
)

var rainColors = []lipgloss.Color{
	lipgloss.Color("#394b70"), // far streaks
	lipgloss.Color("#5e7ce2"),
	lipgloss.Color("#7287fd"),
	lipgloss.Color("#89b4fa"),
	lipgloss.Color("#b4befe"), // near, bright
}

var (
	rainFlashBright = lipgloss.Color("#f5f5ff")
	rainFlashBolt   = lipgloss.Color("#cba6f7")
	rainFlashGlow   = lipgloss.Color("#7287fd")
)

func NewRainModel() *RainModel {
	return &RainModel{
		rng:            rand.New(rand.NewSource(11)),
		strikeCooldown: 120,
		intensity:      0.35, // default if we never get repo data
	}
}

func (m *RainModel) Name() string { return "Rain" }

func (m *RainModel) Tick() { m.Frame++ }

// SetRepos lets activity drive rain density.
func (m *RainModel) SetRepos(repos []sources.GitRepoStatus) {
	if len(repos) == 0 {
		return
	}
	active := 0
	for _, r := range repos {
		if r.Dirty || r.Unpushed > 0 || r.Behind > 0 {
			active++
		}
	}
	target := 0.25 + 0.75*float64(active)/float64(len(repos))
	// EMA toward target so transitions are smooth.
	m.intensity = 0.9*m.intensity + 0.1*target
}

func (m *RainModel) View(width, height int, focused bool) string {
	innerW := width - 4
	innerH := height - 2
	if innerW < 6 || innerH < 4 {
		return ""
	}
	if innerW != m.w || innerH != m.h {
		m.w, m.h = innerW, innerH
		m.drops = nil
	}

	m.spawn()
	m.advance()
	m.maybeLightning()

	buf := newVizBuf(innerW, innerH)

	// Base atmospheric tint: faint dots on a few random cells for texture.
	// (Skipped when flashing so the flash dominates.)

	// Draw drops as short diagonal trails.
	for _, d := range m.drops {
		trailLen := d.length
		for k := 0; k < trailLen; k++ {
			px := int(math.Round(d.x - float64(k)*rainSlopeX*0.5))
			py := int(math.Round(d.y - float64(k)))
			if !inBounds(px, py, innerW, innerH) {
				continue
			}
			depth := float64(k) / float64(trailLen)
			bucket := int((1 - depth) * float64(len(rainColors)-1))
			if bucket < 0 {
				bucket = 0
			}
			if bucket >= len(rainColors) {
				bucket = len(rainColors) - 1
			}
			ch := '╲'
			if k == 0 {
				ch = '╲'
			} else if depth > 0.6 {
				ch = '·'
			}
			buf[py][px] = vizCell{ch: ch, color: rainColors[bucket]}
		}
	}

	// Lightning flash on top.
	if m.flashT > 0 {
		m.drawFlash(buf, innerW, innerH)
		m.flashT--
	}

	return renderVizBuf(buf)
}

func (m *RainModel) spawn() {
	// Target count scales with area × intensity.
	area := m.w * m.h
	target := int(float64(area) * (0.015 + m.intensity*0.05))
	for len(m.drops) < target {
		m.drops = append(m.drops, m.birthDrop(true))
	}
}

func (m *RainModel) birthDrop(anyY bool) raindrop {
	y := -1.0
	if anyY {
		// On first population, seed y across the pane so we don't have a blank
		// scene for a couple seconds.
		y = m.rng.Float64() * float64(m.h)
	}
	return raindrop{
		x:      m.rng.Float64()*float64(m.w+m.h) - float64(m.h), // allow negative so diagonal enters from top-left
		y:      y,
		speed:  rainMinSpeed + m.rng.Float64()*(rainMaxSpeed-rainMinSpeed),
		length: 2 + m.rng.Intn(3),
	}
}

func (m *RainModel) advance() {
	kept := m.drops[:0]
	for _, d := range m.drops {
		d.y += d.speed
		d.x += rainSlopeX * d.speed * 0.5
		if d.y-float64(d.length) > float64(m.h) || d.x > float64(m.w+d.length) {
			continue // fell off — drop it
		}
		kept = append(kept, d)
	}
	m.drops = kept
}

func (m *RainModel) maybeLightning() {
	if m.strikeCooldown > 0 {
		m.strikeCooldown--
		return
	}
	// Probability per tick scales with intensity; cap so it stays rare.
	p := 0.004 + m.intensity*0.006
	if m.rng.Float64() >= p {
		return
	}
	m.flashT = 4 + m.rng.Intn(3)
	m.flashCol = m.rng.Intn(m.w)
	m.flashBranches = m.generateBolt()
	m.strikeCooldown = 60 + m.rng.Intn(180)
}

func (m *RainModel) generateBolt() [][2]int {
	path := make([][2]int, 0, m.h)
	x := m.flashCol
	for y := 0; y < m.h; y++ {
		path = append(path, [2]int{x, y})
		// Random jitter left/right with a slight rightward bias matching drops.
		jit := m.rng.Intn(3) - 1
		x += jit
		if x < 0 {
			x = 0
		}
		if x >= m.w {
			x = m.w - 1
		}
	}
	return path
}

func (m *RainModel) drawFlash(buf [][]vizCell, w, h int) {
	// Full-pane brighten on first frame or two.
	if m.flashT >= 4 {
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if buf[y][x].ch == ' ' {
					buf[y][x] = vizCell{ch: '·', color: rainFlashGlow}
				}
			}
		}
	}
	// Bolt path on top in all frames.
	boltColor := rainFlashBolt
	if m.flashT >= 3 {
		boltColor = rainFlashBright
	}
	for _, p := range m.flashBranches {
		if inBounds(p[0], p[1], w, h) {
			buf[p[1]][p[0]] = vizCell{ch: '│', color: boltColor}
		}
	}
}
