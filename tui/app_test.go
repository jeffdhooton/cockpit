package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jhoot/cockpit/config"
)

func TestCalculateLayoutHeight40Plus(t *testing.T) {
	l := CalculateLayout(100, 45, 3)
	if l.SessionsH < 5 {
		t.Errorf("SessionsH = %d, want >= 5", l.SessionsH)
	}
	if l.MiddleH < 4 {
		t.Errorf("MiddleH = %d, want >= 4", l.MiddleH)
	}
	if l.BottomH < 4 {
		t.Errorf("BottomH = %d, want >= 4", l.BottomH)
	}
	total := l.SessionsH + l.MiddleH + l.BottomH + l.KeyhintsH
	if total != 45 {
		t.Errorf("total height = %d, want 45", total)
	}
}

func TestCalculateLayoutHeight30to39(t *testing.T) {
	l := CalculateLayout(100, 35, 3)
	if l.SessionsH < 5 {
		t.Errorf("SessionsH = %d, want >= 5", l.SessionsH)
	}
	total := l.SessionsH + l.MiddleH + l.BottomH + l.KeyhintsH
	if total != 35 {
		t.Errorf("total height = %d, want 35", total)
	}
}

func TestCalculateLayoutHeight24to29(t *testing.T) {
	l := CalculateLayout(100, 26, 3)
	total := l.SessionsH + l.MiddleH + l.BottomH + l.KeyhintsH
	if total != 26 {
		t.Errorf("total height = %d, want 26", total)
	}
}

func TestCalculateLayoutHeightBelow24(t *testing.T) {
	l := CalculateLayout(100, 20, 3)
	if l.SessionsH != 5 {
		t.Errorf("SessionsH = %d, want 5 (floor)", l.SessionsH)
	}
	if l.MiddleH < 4 {
		t.Errorf("MiddleH = %d, want >= 4 (floor)", l.MiddleH)
	}
	if l.BottomH < 4 {
		t.Errorf("BottomH = %d, want >= 4 (floor)", l.BottomH)
	}
}

func TestCalculateLayoutWidth(t *testing.T) {
	l := CalculateLayout(100, 40, 3)
	if l.LeftW != 50 {
		t.Errorf("LeftW = %d, want 50", l.LeftW)
	}
	if l.RightW != 50 {
		t.Errorf("RightW = %d, want 50", l.RightW)
	}
}

func TestCalculateLayoutWidthOdd(t *testing.T) {
	l := CalculateLayout(101, 40, 3)
	if l.LeftW+l.RightW != 101 {
		t.Errorf("LeftW(%d) + RightW(%d) = %d, want 101", l.LeftW, l.RightW, l.LeftW+l.RightW)
	}
}

func TestMinimumTerminalWidth(t *testing.T) {
	cfg := testConfig()
	m := NewModel(cfg)
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

func TestFocusCycling(t *testing.T) {
	cfg := testConfig()
	m := NewModel(cfg)
	m.width = 100
	m.height = 40

	if m.focused != PanelToday {
		t.Errorf("default focus = %d, want PanelToday(%d)", m.focused, PanelToday)
	}

	m.handleNavKey(keyMsg("tab"))
	if m.focused != PanelInbox {
		t.Errorf("after tab from Today, focus = %d, want PanelInbox(%d)", m.focused, PanelInbox)
	}

	m.handleNavKey(keyMsg("tab"))
	if m.focused != PanelSignals {
		t.Errorf("after 2 tabs, focus = %d, want PanelSignals(%d)", m.focused, PanelSignals)
	}

	m.handleNavKey(keyMsg("tab"))
	if m.focused != PanelSessions {
		t.Errorf("after 3 tabs, focus = %d, want PanelSessions(%d)", m.focused, PanelSessions)
	}
}

func TestQuitReturnsQuit(t *testing.T) {
	cfg := testConfig()
	m := NewModel(cfg)
	cmd := m.handleNavKey(keyMsg("q"))
	if cmd == nil {
		t.Error("expected quit cmd")
	}
}

func TestTickTriggersSourceFetches(t *testing.T) {
	cfg := testConfig()
	m := NewModel(cfg)
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
	m := NewModel(cfg)
	m.width = 100
	m.height = 40

	m.handleNavKey(keyMsg("c"))
	if m.mode != ModeCapture {
		t.Errorf("mode = %d, want ModeCapture(%d)", m.mode, ModeCapture)
	}
	if m.focused != PanelToday {
		t.Errorf("focused = %d, want PanelToday(%d)", m.focused, PanelToday)
	}

	m.handleCaptureKey(keyMsg("esc"))
	if m.mode != ModeNavigation {
		t.Errorf("mode = %d, want ModeNavigation(%d)", m.mode, ModeNavigation)
	}
}

func TestCaptureModeBlocksNavKeys(t *testing.T) {
	cfg := testConfig()
	m := NewModel(cfg)
	m.width = 100
	m.height = 40

	m.handleNavKey(keyMsg("c"))
	if m.mode != ModeCapture {
		t.Fatal("should be in capture mode")
	}

	previousFocus := m.focused
	m.handleKey(keyMsg("tab"))
	if m.focused != previousFocus {
		t.Errorf("Tab changed focus in capture mode: was %d, now %d", previousFocus, m.focused)
	}
}

func TestRefreshKey(t *testing.T) {
	cfg := testConfig()
	m := NewModel(cfg)
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
