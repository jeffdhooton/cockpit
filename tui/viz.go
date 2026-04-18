package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/jhoot/cockpit/sources"
)

// Visualizer is one interchangeable ambient pane.
type Visualizer interface {
	Name() string
	View(width, height int, focused bool) string
	Tick()
}

// ReposAware is implemented by visualizers that want repo data.
type ReposAware interface {
	SetRepos([]sources.GitRepoStatus)
}

// VizModel holds the active set and the current selection.
type VizModel struct {
	Visualizers []Visualizer
	Current     int
}

func NewVizModel() VizModel {
	return VizModel{
		Visualizers: []Visualizer{
			NewPlasmaModel(),
			NewLifeModel(),
			NewStarfieldModel(),
			NewConstellationModel(),
			NewBoidsModel(),
			NewOrbitalModel(),
			NewRainModel(),
			NewClockModel(),
		},
	}
}

func (m *VizModel) Tick() {
	if len(m.Visualizers) == 0 {
		return
	}
	m.Visualizers[m.Current].Tick()
}

func (m *VizModel) Next() {
	if len(m.Visualizers) == 0 {
		return
	}
	m.Current = (m.Current + 1) % len(m.Visualizers)
}

func (m *VizModel) Select(i int) {
	if i < 0 || i >= len(m.Visualizers) {
		return
	}
	m.Current = i
}

// ActiveClock returns the clock visualizer if it is currently selected,
// otherwise nil. Used to route pomodoro key bindings.
func (m *VizModel) ActiveClock() *ClockModel {
	if len(m.Visualizers) == 0 {
		return nil
	}
	c, _ := m.Visualizers[m.Current].(*ClockModel)
	return c
}

func (m VizModel) Name() string {
	if len(m.Visualizers) == 0 {
		return "Visualizer"
	}
	return m.Visualizers[m.Current].Name()
}

func (m VizModel) View(width, height int, focused bool) string {
	if len(m.Visualizers) == 0 {
		return ""
	}
	return m.Visualizers[m.Current].View(width, height, focused)
}

// SetRepos forwards repo data to any visualizer that needs it.
func (m *VizModel) SetRepos(repos []sources.GitRepoStatus) {
	for _, v := range m.Visualizers {
		if r, ok := v.(ReposAware); ok {
			r.SetRepos(repos)
		}
	}
}

// ---- shared rendering helpers used by cell-buffer visualizers ----

type vizCell struct {
	ch    rune
	color lipgloss.Color
}

func newVizBuf(w, h int) [][]vizCell {
	buf := make([][]vizCell, h)
	for r := range buf {
		buf[r] = make([]vizCell, w)
		for c := range buf[r] {
			buf[r][c] = vizCell{ch: ' '}
		}
	}
	return buf
}

// renderVizBuf flattens a cell buffer into a colored string, grouping runs of
// matching color to cut style escape overhead.
func renderVizBuf(buf [][]vizCell) string {
	var sb strings.Builder
	for y, row := range buf {
		i := 0
		for i < len(row) {
			j := i
			for j < len(row) && row[j].color == row[i].color {
				j++
			}
			segment := make([]rune, 0, j-i)
			for k := i; k < j; k++ {
				segment = append(segment, row[k].ch)
			}
			s := string(segment)
			if row[i].color == "" {
				sb.WriteString(s)
			} else {
				sb.WriteString(lipgloss.NewStyle().Foreground(row[i].color).Render(s))
			}
			i = j
		}
		if y < len(buf)-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
