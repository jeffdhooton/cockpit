package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jhoot/cockpit/config"
)

func TestCalculateLayoutFloors(t *testing.T) {
	// All sizes should meet minimum floors
	for _, h := range []int{20, 26, 35, 45, 60, 80} {
		l := CalculateLayout(100, h, 3)
		if l.SessionsH < 10 {
			t.Errorf("height=%d: SessionsH = %d, want >= 10", h, l.SessionsH)
		}
		if l.MiddleH < 6 {
			t.Errorf("height=%d: MiddleH = %d, want >= 6", h, l.MiddleH)
		}
		if l.BottomH < 5 {
			t.Errorf("height=%d: BottomH = %d, want >= 5", h, l.BottomH)
		}
		if l.KeyhintsH != 1 {
			t.Errorf("height=%d: KeyhintsH = %d, want 1", h, l.KeyhintsH)
		}
	}
}

func TestCalculateLayoutScaling(t *testing.T) {
	// Bigger terminals should give sessions more space
	small := CalculateLayout(100, 45, 3)
	big := CalculateLayout(100, 80, 3)
	if big.SessionsH <= small.SessionsH {
		t.Errorf("bigger terminal should have larger sessions: small=%d big=%d", small.SessionsH, big.SessionsH)
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

	if m.focused != PanelSessions {
		t.Errorf("default focus = %d, want PanelSessions(%d)", m.focused, PanelSessions)
	}

	// Tab cycles through all panels
	m.handleNavKey(keyMsg("tab"))
	if m.focused != PanelRepos {
		t.Errorf("after 1 tab, focus = %d, want PanelRepos(%d)", m.focused, PanelRepos)
	}

	// Full cycle back to start
	for i := 0; i < 4; i++ {
		m.handleNavKey(keyMsg("tab"))
	}
	if m.focused != PanelSessions {
		t.Errorf("after full cycle, focus = %d, want PanelSessions(%d)", m.focused, PanelSessions)
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
