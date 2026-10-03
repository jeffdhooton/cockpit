package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jeffdhooton/cockpit/config"
)

func TestMinimumTerminalWidth(t *testing.T) {
	cfg := testConfig()
	m := NewModel(cfg, "/tmp/config.toml")
	m.width = 50
	m.height = 40
	view := m.View()
	if view == "" {
		t.Error("expected non-empty view for narrow terminal")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		expect string
	}{
		{"short", 10, "short"},
		{"hello world", 8, "hello…"},
		{"abc", 1, "…"},
		{"hello world test", 12, "hello…"},
		{"hello world test", 13, "hello world…"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := Truncate(tt.input, tt.maxLen)
			if got != tt.expect {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.expect)
			}
		})
	}
}

func TestQuitReturnsQuit(t *testing.T) {
	cfg := testConfig()
	m := NewModel(cfg, "/tmp/config.toml")
	cmd := m.handleNavKey(keyMsg("q"))
	if cmd == nil {
		t.Error("expected quit cmd")
	}
}

func TestTickTriggersSourceFetches(t *testing.T) {
	cfg := testConfig()
	m := NewModel(cfg, "/tmp/config.toml")
	m.width = 100
	m.height = 40

	newModel, cmd := m.Update(localTickMsg{})
	if cmd == nil {
		t.Error("localTickMsg should return batch cmd for source fetches")
	}
	_ = newModel
}

func TestCaptureModeEnterExit(t *testing.T) {
	cfg := testConfig()
	m := NewModel(cfg, "/tmp/config.toml")
	m.width = 100
	m.height = 40

	m.handleNavKey(keyMsg("c"))
	if m.mode != ModeCapture {
		t.Errorf("mode = %d, want ModeCapture(%d)", m.mode, ModeCapture)
	}
	if !strings.Contains(m.View(), "capture") {
		t.Error("the capture prompt should be visible")
	}

	m.handleCaptureKey(keyMsg("esc"))
	if m.mode != ModeNavigation {
		t.Errorf("mode = %d, want ModeNavigation(%d)", m.mode, ModeNavigation)
	}
}

func TestCaptureModeBlocksNavKeys(t *testing.T) {
	cfg := testConfig()
	m := NewModel(cfg, "/tmp/config.toml")
	m.width = 100
	m.height = 40

	m.handleNavKey(keyMsg("c"))
	if m.mode != ModeCapture {
		t.Fatal("should be in capture mode")
	}

	if cmd := m.handleKey(keyMsg("q")); cmd != nil {
		t.Error("q while capturing must not quit")
	}
	if m.view != ViewSessions {
		t.Errorf("g while capturing must not change the view: %v", m.view)
	}
}

func TestRefreshKey(t *testing.T) {
	cfg := testConfig()
	m := NewModel(cfg, "/tmp/config.toml")
	cmd := m.handleNavKey(keyMsg("r"))
	if cmd == nil {
		t.Error("r key should return refresh cmd")
	}
}

// helpers

func testConfig() *config.Config {
	return &config.Config{
		General: config.GeneralConfig{
			SessionName:     "cockpit",
			RefreshInterval: 5,
		},
		Obsidian: config.ObsidianConfig{
			VaultPath: "/tmp/vault",
			TodayFile: "/tmp/vault/today.md",
			InboxFile: "/tmp/vault/inbox.md",
		},
		GitHub: config.GitHubConfig{
			Enabled:         true,
			RefreshInterval: 60,
		},
		Signals: config.SignalsConfig{
			StaleSessionThreshold: "24h",
		},
	}
}

func keyMsg(key string) tea.KeyMsg {
	switch key {
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}

func TestTmuxJumpRepoRejectsInvalidLabel(t *testing.T) {
	err := tmuxJumpRepo(nil, config.RepoConfig{Label: "my app; rm -rf /", Path: "/tmp"})
	if err == nil {
		t.Fatal("an unsafe label must be rejected before it reaches tmux")
	}
	if !strings.Contains(err.Error(), "invalid session label") {
		t.Errorf("error = %q", err.Error())
	}
}

func TestRepoForLabelUsesConfiguredProcesses(t *testing.T) {
	m := Model{config: &config.Config{Repos: []config.RepoConfig{{
		Label:     "app",
		Path:      "/tmp/app",
		Processes: []config.ProcessConfig{{Name: "dev", Command: "npm run dev"}},
	}}}}

	got := m.repoForLabel("app", "/tmp/app")
	if len(got.Processes) != 1 {
		t.Errorf("a configured repo should bring its processes: %+v", got)
	}

	unknown := m.repoForLabel("ghost", "/tmp/ghost")
	if unknown.Label != "ghost" || unknown.Path != "/tmp/ghost" {
		t.Errorf("an unconfigured session should still be jumpable: %+v", unknown)
	}
	if len(unknown.Processes) != 0 {
		t.Errorf("an unconfigured session has no processes: %+v", unknown)
	}
}

func TestViewNeverWritesTheTerminalsLastColumn(t *testing.T) {
	// Cockpit draws a border, so the outermost cell of every line is a visible
	// character. Writing the final column leaves the terminal in a pending-wrap
	// state, which desynchronises Bubbletea's line diffing: tiles lose their
	// bottom border and stray blank lines appear mid-tile. Reserving the last
	// column costs one cell and keeps every frame aligned.
	for _, size := range []struct{ w, h int }{
		{80, 24}, {120, 30}, {200, 40}, {340, 37}, {400, 60},
	} {
		m := NewModel(&config.Config{General: config.GeneralConfig{SessionName: "self"}}, "")
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m = updated.(Model)

		for i, line := range strings.Split(m.View(), "\n") {
			if w := lipgloss.Width(line); w >= size.w {
				t.Errorf("%dx%d: line %d is %d wide, terminal is %d — the last column must stay unwritten",
					size.w, size.h, i, w, size.w)
				break
			}
		}
	}
}

func TestWindowSizeReservesOneColumn(t *testing.T) {
	m := NewModel(&config.Config{General: config.GeneralConfig{SessionName: "self"}}, "")
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updated.(Model)

	if m.width != 99 {
		t.Errorf("width = %d, want 99 (one column reserved)", m.width)
	}
	if m.height != 40 {
		t.Errorf("height = %d, want the full 40 — only the column needs reserving", m.height)
	}
}
