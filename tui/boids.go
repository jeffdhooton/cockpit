package tui

import (
	"math"
	"math/rand"

	"github.com/charmbracelet/lipgloss"
)

// BoidsModel renders a flock of prey plus a couple of predators. Prey follow
// Reynolds' three rules (separation/alignment/cohesion) and flee from any
// predator in sight; predators lazily pursue the nearest prey.
type BoidsModel struct {
	Frame int
	boids []boid
	w, h  int
	rng   *rand.Rand
}

type boid struct {
	x, y     float64
	vx, vy   float64
	predator bool
}

const (
	boidCount     = 40
	predatorCount = 2

	boidMaxSpeed  = 0.9
	boidMinSpeed  = 0.25
	boidVisionR   = 6.0
	boidSeparateR = 2.2
	boidSeparateK = 0.18
	boidAlignK    = 0.05
	boidCohesionK = 0.01
	boidYStretch  = 2.0 // rows are ~2x taller than cols

	// Predator dynamics.
	predatorMaxSpeed  = 1.1
	predatorVisionR   = 14.0
	predatorPursueK   = 0.03
	preyFleeVisionR   = 10.0
	preyFleeK         = 0.35
)

// Prey palette: deep indigo → pink (colorblind-safe, no green).
var boidColors = []lipgloss.Color{
	lipgloss.Color("#394b70"),
	lipgloss.Color("#5e7ce2"),
	lipgloss.Color("#7287fd"),
	lipgloss.Color("#89b4fa"),
	lipgloss.Color("#b4befe"),
	lipgloss.Color("#ff5e9e"),
}

// Predators wear an angry amber-orange so they read as hostile.
var predatorColor = lipgloss.Color("#ff5e00")

// Direction glyphs — 8 compass points.
var boidGlyphs = []rune{
	'→', '↘', '↓', '↙', '←', '↖', '↑', '↗',
}

// Predators use filled arrow heads so they read as bigger / heavier.
var predatorGlyphs = []rune{
	'▶', '◢', '▼', '◣', '◀', '◤', '▲', '◥',
}

func NewBoidsModel() *BoidsModel {
	return &BoidsModel{rng: rand.New(rand.NewSource(7))}
}

func (m *BoidsModel) Name() string { return "Boids" }

func (m *BoidsModel) Tick() { m.Frame++ }

func (m *BoidsModel) View(width, height int, focused bool) string {
	innerW := width - 4
	innerH := height - 2
	if innerW < 6 || innerH < 4 {
		return ""
	}
	if innerW != m.w || innerH != m.h || len(m.boids) == 0 {
		m.w, m.h = innerW, innerH
		m.spawn()
	}
	m.update()

	buf := newVizBuf(innerW, innerH)
	for _, b := range m.boids {
		x := int(math.Round(b.x))
		y := int(math.Round(b.y))
		if x < 0 || x >= innerW || y < 0 || y >= innerH {
			continue
		}
		if b.predator {
			buf[y][x] = vizCell{ch: glyphFor(b.vx, b.vy, predatorGlyphs), color: predatorColor}
			continue
		}
		speed := math.Hypot(b.vx, b.vy*boidYStretch)
		bucket := int(speed / boidMaxSpeed * float64(len(boidColors)-1))
		if bucket < 0 {
			bucket = 0
		}
		if bucket >= len(boidColors) {
			bucket = len(boidColors) - 1
		}
		buf[y][x] = vizCell{ch: glyphFor(b.vx, b.vy, boidGlyphs), color: boidColors[bucket]}
	}
	return renderVizBuf(buf)
}

func (m *BoidsModel) spawn() {
	m.boids = make([]boid, boidCount+predatorCount)
	for i := range m.boids {
		angle := m.rng.Float64() * 2 * math.Pi
		speed := boidMaxSpeed * 0.6
		isPred := i >= boidCount
		if isPred {
			speed = predatorMaxSpeed * 0.5
		}
		m.boids[i] = boid{
			x:        m.rng.Float64() * float64(m.w),
			y:        m.rng.Float64() * float64(m.h),
			vx:       math.Cos(angle) * speed,
			vy:       math.Sin(angle) * speed / boidYStretch,
			predator: isPred,
		}
	}
}

func (m *BoidsModel) update() {
	fw := float64(m.w)
	fh := float64(m.h)
	visSq := boidVisionR * boidVisionR
	sepSq := boidSeparateR * boidSeparateR
	fleeSq := preyFleeVisionR * preyFleeVisionR
	predVisSq := predatorVisionR * predatorVisionR

	next := make([]boid, len(m.boids))
	for i, b := range m.boids {
		if b.predator {
			next[i] = m.stepPredator(b, fw, fh, predVisSq)
			continue
		}

		var avgVX, avgVY, avgX, avgY, sepX, sepY, fleeX, fleeY float64
		neighbors := 0

		for j, o := range m.boids {
			if i == j {
				continue
			}
			dx := o.x - b.x
			dy := (o.y - b.y) * boidYStretch
			d2 := dx*dx + dy*dy

			if o.predator {
				if d2 < fleeSq && d2 > 0 {
					// Strong repulsion from predators, falls off with distance.
					fleeX -= dx / d2
					fleeY -= dy / boidYStretch / d2
				}
				continue
			}
			if d2 > visSq {
				continue
			}
			neighbors++
			avgVX += o.vx
			avgVY += o.vy
			avgX += o.x
			avgY += o.y
			if d2 < sepSq && d2 > 0 {
				sepX -= dx / d2
				sepY -= dy / boidYStretch / d2
			}
		}

		nb := b
		if neighbors > 0 {
			inv := 1 / float64(neighbors)
			avgVX *= inv
			avgVY *= inv
			avgX *= inv
			avgY *= inv

			nb.vx += (avgVX - b.vx) * boidAlignK
			nb.vy += (avgVY - b.vy) * boidAlignK
			nb.vx += (avgX - b.x) * boidCohesionK
			nb.vy += (avgY - b.y) * boidCohesionK / boidYStretch
		}

		nb.vx += sepX * boidSeparateK
		nb.vy += sepY * boidSeparateK
		nb.vx += fleeX * preyFleeK
		nb.vy += fleeY * preyFleeK

		nb = clampSpeed(nb, boidMaxSpeed, boidMinSpeed)
		nb = advance(nb, fw, fh)
		next[i] = nb
	}
	m.boids = next
}

func (m *BoidsModel) stepPredator(p boid, fw, fh, visSq float64) boid {
	// Find nearest prey within vision; steer toward it.
	bestD := math.Inf(1)
	var targetDX, targetDY float64
	for _, o := range m.boids {
		if o.predator {
			continue
		}
		dx := o.x - p.x
		dy := (o.y - p.y) * boidYStretch
		d2 := dx*dx + dy*dy
		if d2 > visSq || d2 <= 0 {
			continue
		}
		if d2 < bestD {
			bestD = d2
			targetDX = dx
			targetDY = dy
		}
	}

	np := p
	if !math.IsInf(bestD, 1) {
		np.vx += targetDX * predatorPursueK
		np.vy += targetDY / boidYStretch * predatorPursueK
	}
	np = clampSpeed(np, predatorMaxSpeed, boidMinSpeed)
	np = advance(np, fw, fh)
	return np
}

func clampSpeed(b boid, maxSpeed, minSpeed float64) boid {
	sp := math.Hypot(b.vx, b.vy*boidYStretch)
	if sp > maxSpeed {
		scale := maxSpeed / sp
		b.vx *= scale
		b.vy *= scale
	} else if sp < minSpeed && sp > 0 {
		scale := minSpeed / sp
		b.vx *= scale
		b.vy *= scale
	}
	return b
}

func advance(b boid, fw, fh float64) boid {
	b.x += b.vx
	b.y += b.vy
	if b.x < 0 {
		b.x += fw
	} else if b.x >= fw {
		b.x -= fw
	}
	if b.y < 0 {
		b.y += fh
	} else if b.y >= fh {
		b.y -= fh
	}
	return b
}

func glyphFor(vx, vy float64, glyphs []rune) rune {
	angle := math.Atan2(vy*boidYStretch, vx)
	idx := int(math.Round(angle/(math.Pi/4))+8) % 8
	return glyphs[idx]
}
