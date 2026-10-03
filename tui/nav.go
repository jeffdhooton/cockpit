package tui

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/sources"
)

// returnPoint is where Esc goes back to: the exact view, host grid,
// selection and focus that were on screen when a panel opened. Panels push
// one when they open and pop it when they close, so attention → processes →
// output → Esc → Esc → Esc lands on the tile the user started from.
type returnPoint struct {
	view          ViewMode
	gridHost      string
	gridCursor    string
	gridIndex     int
	attentionItem string // selected attention item id, when leaving the queue
	sessKey       string // session view selection
	sessPane      string
	sessFocus     bool
}

func (m *Model) pushReturn() {
	m.nav = append(m.nav, returnPoint{
		view:          m.view,
		gridHost:      m.gridHost,
		gridCursor:    m.gridCursor,
		gridIndex:     m.gridIndex,
		attentionItem: m.attn.selected,
		sessKey:       m.sess.selected,
		sessPane:      m.sess.pane,
		sessFocus:     m.sess.paneFocus,
	})
}

// popReturn restores the previous view. With nothing to pop it falls back to
// the grid, which is the root of everything.
func (m *Model) popReturn() {
	if len(m.nav) == 0 {
		m.view = ViewGrid
		return
	}
	rp := m.nav[len(m.nav)-1]
	m.nav = m.nav[:len(m.nav)-1]
	m.view = rp.view
	m.gridHost = rp.gridHost
	m.gridCursor = rp.gridCursor
	m.gridIndex = rp.gridIndex
	if rp.view == ViewAttention {
		m.attn.select_(rp.attentionItem)
	}
	if rp.view == ViewSessions {
		m.sess.selected, m.sess.pane, m.sess.paneFocus = rp.sessKey, rp.sessPane, rp.sessFocus
		m.sess.preview = panePreview{}
		m.sess.resolve()
	}
}

// attachResultMsg reports an attach-existing attempt. Gone means the target
// was validated absent: the queue says so and refreshes rather than
// creating anything.
type attachResultMsg struct {
	Err  error
	Gone bool
}

// attachCmd reaches an existing pane, window or session without creating
// anything. Local targets switch the client directly; remote ones go through
// a local view window that attaches to the existing remote session.
func (m Model) attachCmd(t sources.AttachTarget) tea.Cmd {
	local := sources.DefaultRunner()
	var host config.HostConfig
	var remote sources.Runner
	if t.Host != "" {
		hc, ok := m.config.Host(t.Host)
		if !ok {
			return func() tea.Msg { return attachResultMsg{Err: fmt.Errorf("host %q is not configured", t.Host)} }
		}
		host = hc
		r, err := m.svc.RunnerFor(config.RepoConfig{Host: t.Host})
		if err != nil {
			return func() tea.Msg { return attachResultMsg{Err: err} }
		}
		remote = r
	}
	return func() tea.Msg {
		ctx := context.Background()
		var err error
		if remote != nil {
			err = sources.AttachRemote(ctx, local, remote, host, t)
		} else {
			err = sources.AttachLocal(ctx, local, t)
		}
		return attachResultMsg{Err: err, Gone: err == sources.ErrTargetGone}
	}
}

// openURLCmd opens a validated https GitHub URL with the platform opener. It
// is only ever run on an explicit keypress inside CI details.
func openURLCmd(url string) tea.Cmd {
	if !validRunURL(url) {
		return func() tea.Msg { return sourceErrMsg{Source: "open", Err: fmt.Errorf("not a github https url")} }
	}
	return func() tea.Msg {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", url)
		default:
			cmd = exec.Command("xdg-open", url)
		}
		if err := cmd.Start(); err != nil {
			return sourceErrMsg{Source: "open", Err: err}
		}
		return nil
	}
}

// validRunURL accepts only https://github.com/... so a crafted run URL from
// a poisoned API response cannot open anything else.
func validRunURL(u string) bool {
	return strings.HasPrefix(u, "https://github.com/") && !strings.ContainsAny(u, " \t\n\r'\"`<>")
}

// clean strips control sequences from text that came from outside — a
// session name, a window name, a log line — before it is drawn.
func clean(s string) string {
	return sources.StripControl(s)
}

// clip truncates text to width cells with an ellipsis. It is ANSI-aware:
// styled text keeps its escapes intact and only printable cells count.
func clip(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return ansi.Truncate(s, width, "…")
}

// wrap breaks text into lines no wider than width, on spaces where it can.
func wrap(s string, width int) []string {
	if width <= 0 {
		return nil
	}
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := ""
		for _, w := range words {
			if line == "" {
				line = w
			} else if lipgloss.Width(line)+1+lipgloss.Width(w) <= width {
				line += " " + w
			} else {
				lines = append(lines, line)
				line = w
			}
			for lipgloss.Width(line) > width {
				lines = append(lines, clip(line, width))
				line = ""
				break
			}
		}
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// padLine pads or clips a line to exactly width cells.
func padLine(s string, width int) string {
	s = clip(s, width)
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// hostLabel is the host name shown to a person: "local" for this machine.
func hostLabel(host string) string {
	if host == "" {
		return "local"
	}
	return host
}

// lipglossWidth is lipgloss.Width, named for tests.
func lipglossWidth(s string) int { return lipgloss.Width(s) }
