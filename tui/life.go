package tui

import (
	"math/rand"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// LifeModel renders Conway's Game of Life with toroidal wrapping. Cells are
// colored by how long they've been alive; dead or stagnant boards reseed.
type LifeModel struct {
	Frame  int
	w, h   int
	grid   []bool
	age    []int
	quietT int
	rng    *rand.Rand
}

func NewLifeModel() *LifeModel {
	return &LifeModel{rng: rand.New(rand.NewSource(1))}
}

func (m *LifeModel) Name() string { return "Life" }

func (m *LifeModel) Tick() { m.Frame++ }

// Step every N frames — Life at 16fps is too frenetic.
const lifeStepEvery = 3

// Colors: dim → bright as cells age.
var lifeAgeColors = []lipgloss.Color{
	lipgloss.Color("#394b70"), // new
	lipgloss.Color("#7287fd"),
	lipgloss.Color("#89b4fa"),
	lipgloss.Color("#b4befe"),
	lipgloss.Color("#cba6f7"),
	lipgloss.Color("#f5c2e7"), // ancient
}

var lifeAgeChars = []rune{'·', '∘', '○', '●', '●', '●'}

func (m *LifeModel) View(width, height int, focused bool) string {
	innerW := width - 4
	innerH := height - 2
	if innerW < 4 || innerH < 3 {
		return ""
	}
	if innerW != m.w || innerH != m.h || m.grid == nil {
		m.w, m.h = innerW, innerH
		m.reseed()
	}
	if m.Frame%lifeStepEvery == 0 {
		m.step()
	}

	var sb strings.Builder
	for y := 0; y < innerH; y++ {
		var prevColor lipgloss.Color = ""
		var run strings.Builder
		flush := func() {
			if run.Len() == 0 {
				return
			}
			if prevColor == "" {
				sb.WriteString(run.String())
			} else {
				sb.WriteString(lipgloss.NewStyle().Foreground(prevColor).Render(run.String()))
			}
			run.Reset()
		}
		for x := 0; x < innerW; x++ {
			idx := y*innerW + x
			if !m.grid[idx] {
				if prevColor != "" {
					flush()
					prevColor = ""
				}
				run.WriteRune(' ')
				continue
			}
			bucket := m.age[idx] - 1
			if bucket < 0 {
				bucket = 0
			}
			if bucket >= len(lifeAgeColors) {
				bucket = len(lifeAgeColors) - 1
			}
			color := lifeAgeColors[bucket]
			ch := lifeAgeChars[bucket]
			if color != prevColor {
				flush()
				prevColor = color
			}
			run.WriteRune(ch)
		}
		flush()
		if y < innerH-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func (m *LifeModel) reseed() {
	m.grid = make([]bool, m.w*m.h)
	m.age = make([]int, m.w*m.h)
	for i := range m.grid {
		if m.rng.Intn(4) == 0 {
			m.grid[i] = true
			m.age[i] = 1
		}
	}
	m.quietT = 0
}

func (m *LifeModel) step() {
	newGrid := make([]bool, len(m.grid))
	newAge := make([]int, len(m.age))
	changed := false
	alive := 0
	for y := 0; y < m.h; y++ {
		for x := 0; x < m.w; x++ {
			n := m.countNeighbors(x, y)
			idx := y*m.w + x
			wasAlive := m.grid[idx]
			var next bool
			if wasAlive {
				next = n == 2 || n == 3
			} else {
				next = n == 3
			}
			newGrid[idx] = next
			if next {
				alive++
				if wasAlive {
					newAge[idx] = m.age[idx] + 1
				} else {
					newAge[idx] = 1
				}
			}
			if next != wasAlive {
				changed = true
			}
		}
	}
	m.grid = newGrid
	m.age = newAge
	if !changed {
		m.quietT++
	} else {
		m.quietT = 0
	}
	// Reseed if the board dies off or stabilizes for too long.
	if alive < m.w*m.h/50 || m.quietT > 15 {
		m.reseed()
	}
}

func (m *LifeModel) countNeighbors(x, y int) int {
	n := 0
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			if dx == 0 && dy == 0 {
				continue
			}
			nx := (x + dx + m.w) % m.w
			ny := (y + dy + m.h) % m.h
			if m.grid[ny*m.w+nx] {
				n++
			}
		}
	}
	return n
}
