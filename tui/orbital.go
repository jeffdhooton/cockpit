package tui

import (
	"hash/fnv"
	"math"

	"github.com/charmbracelet/lipgloss"
	"github.com/jhoot/cockpit/sources"
)

// OrbitalModel renders each repo as a planet orbiting a central pulsing star.
// Orbit radius is stable per-repo (hash derived), angular speed falls off
// with radius (pseudo-Kepler), and glyph/brightness encodes activity.
type OrbitalModel struct {
	Frame int
	repos []sources.GitRepoStatus
}

func NewOrbitalModel() *OrbitalModel { return &OrbitalModel{} }

func (m *OrbitalModel) Name() string { return "Orbital" }

func (m *OrbitalModel) Tick() { m.Frame++ }

func (m *OrbitalModel) SetRepos(repos []sources.GitRepoStatus) {
	m.repos = repos
}

// Planet color ramp — inner orbits hotter, outer cooler. No yellow/green pair.
var orbitalColors = []lipgloss.Color{
	lipgloss.Color("#ff5e9e"), // hot pink
	lipgloss.Color("#ff8fb1"),
	lipgloss.Color("#cba6f7"), // lavender
	lipgloss.Color("#b4befe"),
	lipgloss.Color("#89b4fa"), // blue
	lipgloss.Color("#6c7086"), // dim
}

var (
	orbitStarColor  = lipgloss.Color("#ffc857")
	orbitPathColor  = lipgloss.Color("#313244")
	orbitDirtyColor = lipgloss.Color("#ff5e9e")
)

const orbitalYStretch = 2.0

func (m *OrbitalModel) View(width, height int, focused bool) string {
	innerW := width - 4
	innerH := height - 2
	if innerW < 8 || innerH < 5 {
		return ""
	}
	if len(m.repos) == 0 {
		return MutedText.Render("(no projects)")
	}

	buf := newVizBuf(innerW, innerH)
	cx := float64(innerW-1) / 2
	cy := float64(innerH-1) / 2

	// Radius budget: usable half-extent in visual (yStretch-corrected) units.
	maxR := math.Min(cx, cy*orbitalYStretch) - 1
	if maxR < 3 {
		maxR = 3
	}

	n := len(m.repos)

	// Draw faint orbit rings for visual context.
	for i := 0; i < n; i++ {
		r := orbitRadius(i, n, maxR)
		drawRing(buf, cx, cy, r, orbitPathColor, innerW, innerH)
	}

	// Central star — simple pulse.
	pulse := (math.Sin(float64(m.Frame)*0.12) + 1) / 2
	star := '✦'
	if pulse > 0.66 {
		star = '✸'
	} else if pulse < 0.33 {
		star = '✧'
	}
	cxi, cyi := int(math.Round(cx)), int(math.Round(cy))
	if inBounds(cxi, cyi, innerW, innerH) {
		buf[cyi][cxi] = vizCell{ch: star, color: orbitStarColor}
	}

	// Planets.
	for i, r := range m.repos {
		orbit := orbitRadius(i, n, maxR)
		// Angular speed: smaller radius → faster sweep (loose Kepler feel).
		angSpeed := 0.012 * math.Pow(maxR/math.Max(orbit, 1), 0.7)
		phase := float64(repoHash(r.Label, 1)%1000) / 1000 * 2 * math.Pi
		angle := phase + float64(m.Frame)*angSpeed

		vx := math.Cos(angle) * orbit
		vy := math.Sin(angle) * orbit / orbitalYStretch
		x := int(math.Round(cx + vx))
		y := int(math.Round(cy + vy))
		if !inBounds(x, y, innerW, innerH) {
			continue
		}

		active := r.Dirty || r.Unpushed > 0
		behind := r.Behind > 0

		var glyph rune
		var color lipgloss.Color
		switch {
		case active:
			glyph = '●'
			color = orbitDirtyColor
		case behind:
			glyph = '◉'
			color = orbitalColors[2]
		default:
			denom := n - 1
			if denom < 1 {
				denom = 1
			}
			idx := i * (len(orbitalColors) - 1) / denom
			if idx >= len(orbitalColors) {
				idx = len(orbitalColors) - 1
			}
			color = orbitalColors[idx]
			glyph = '○'
		}

		buf[y][x] = vizCell{ch: glyph, color: color}

		// Label: 2-char initials trailing the planet.
		label := labelInitials(r.Label, 2)
		for k, ch := range label {
			lx := x + 2 + k
			if !inBounds(lx, y, innerW, innerH) {
				break
			}
			if buf[y][lx].ch != ' ' {
				continue
			}
			buf[y][lx] = vizCell{ch: ch, color: lipgloss.Color("#6c7086")}
		}
	}

	return renderVizBuf(buf)
}

// orbitRadius maps slot i of n onto [minR, maxR], leaving an inner gap.
func orbitRadius(i, n int, maxR float64) float64 {
	minR := 2.5
	if n <= 1 {
		return maxR * 0.7
	}
	t := float64(i) / float64(n-1)
	return minR + t*(maxR-minR)
}

// drawRing plots a faint ellipse (circle in yStretch-corrected coords) at the
// given radius.
func drawRing(buf [][]vizCell, cx, cy, r float64, color lipgloss.Color, w, h int) {
	// Step angles so horizontal arc length ~ 1 cell.
	steps := int(math.Max(12, math.Ceil(2*math.Pi*r)))
	for i := 0; i < steps; i++ {
		a := float64(i) / float64(steps) * 2 * math.Pi
		x := int(math.Round(cx + math.Cos(a)*r))
		y := int(math.Round(cy + math.Sin(a)*r/orbitalYStretch))
		if !inBounds(x, y, w, h) {
			continue
		}
		if buf[y][x].ch != ' ' {
			continue
		}
		buf[y][x] = vizCell{ch: '·', color: color}
	}
}

func repoHash(label string, salt byte) uint32 {
	h := fnv.New32a()
	h.Write([]byte(label))
	_, _ = h.Write([]byte{salt})
	return h.Sum32()
}

func inBounds(x, y, w, h int) bool {
	return x >= 0 && x < w && y >= 0 && y < h
}

