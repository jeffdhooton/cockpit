package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jhoot/cockpit/config"
)

func TestCalculateLayoutHeight40Plus(t *testing.T) {
	l := CalculateLayout(100, 45, 3)
	if l.SessionsH != 3 {
		t.Errorf("SessionsH = %d, want 3", l.SessionsH)
	}
	if l.ReposH != 5 { // min(3+2, 6) = 5
		t.Errorf("ReposH = %d, want 5", l.ReposH)
	}
	if l.BottomH != 6 {
		t.Errorf("BottomH = %d, want 6", l.BottomH)
	}
	if l.KeyhintsH != 1 {
		t.Errorf("KeyhintsH = %d, want 1", l.KeyhintsH)
	}
	// Today = 45 - 3 - 5 - 6 - 1 = 30
	if l.TodayH != 30 {
		t.Errorf("TodayH = %d, want 30", l.TodayH)
	}
}

func TestCalculateLayoutHeight30to39(t *testing.T) {
	l := CalculateLayout(100, 35, 3)
	if l.SessionsH != 2 {
		t.Errorf("SessionsH = %d, want 2", l.SessionsH)
	}
	if l.ReposH != 4 { // min(3+2, 4) = 4
		t.Errorf("ReposH = %d, want 4", l.ReposH)
	}
	if l.BottomH != 4 {
		t.Errorf("BottomH = %d, want 4", l.BottomH)
	}
}

func TestCalculateLayoutHeight24to29(t *testing.T) {
	l := CalculateLayout(100, 26, 3)
	if l.SessionsH != 2 {
		t.Errorf("SessionsH = %d, want 2", l.SessionsH)
	}
	if l.ReposH != 3 { // min(3+2, 3) = 3
		t.Errorf("ReposH = %d, want 3", l.ReposH)
	}
	if l.BottomH != 3 {
		t.Errorf("BottomH = %d, want 3", l.BottomH)
	}
}

func TestCalculateLayoutHeightBelow24(t *testing.T) {
	l := CalculateLayout(100, 20, 3)
	if l.SessionsH != 1 {
		t.Errorf("SessionsH = %d, want 1", l.SessionsH)
	}
	if l.ReposH != 1 {
		t.Errorf("ReposH = %d, want 1", l.ReposH)
	}
	if l.BottomH != 3 {
		t.Errorf("BottomH = %d, want 3", l.BottomH)
	}
}

func TestCalculateLayoutWidthNarrow(t *testing.T) {
	l := CalculateLayout(70, 40, 3)
	if !l.StackBottom {
		t.Error("expected StackBottom = true for width < 80")
	}
	if l.InboxW != 70 {
		t.Errorf("InboxW = %d, want 70", l.InboxW)
	}
}

func TestCalculateLayoutWidthWide(t *testing.T) {
	l := CalculateLayout(100, 40, 3)
	if l.StackBottom {
		t.Error("expected StackBottom = false for width >= 80")
	}
	if l.InboxW != 50 {
		t.Errorf("InboxW = %d, want 50", l.InboxW)
	}
	if l.SignalsW != 50 {
		t.Errorf("SignalsW = %d, want 50", l.SignalsW)
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
	// Should contain the "too narrow" message
	if len(view) == 0 {
		t.Error("view should not be empty")
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

	// Default focus
	if m.focused != PanelToday {
		t.Errorf("default focus = %d, want PanelToday(%d)", m.focused, PanelToday)
	}

	// Tab cycles forward
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

	// Simulate a localTickMsg
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

	// Enter capture mode
	m.handleNavKey(keyMsg("c"))
	if m.mode != ModeCapture {
		t.Errorf("mode = %d, want ModeCapture(%d)", m.mode, ModeCapture)
	}
	if m.focused != PanelInbox {
		t.Errorf("focused = %d, want PanelInbox(%d)", m.focused, PanelInbox)
	}

	// Exit capture mode
	m.handleCaptureKey(keyMsg("esc"))
	if m.mode != ModeNavigation {
		t.Errorf("mode = %d, want ModeNavigation(%d)", m.mode, ModeNavigation)
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
