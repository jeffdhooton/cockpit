package sources

import (
	"testing"
)

func TestParseGitHubRepo(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		expect string
		hasErr bool
	}{
		{"https", "https://github.com/jhoot/cockpit.git", "jhoot/cockpit", false},
		{"https no .git", "https://github.com/jhoot/cockpit", "jhoot/cockpit", false},
		{"ssh", "git@github.com:jhoot/cockpit.git", "jhoot/cockpit", false},
		{"ssh no .git", "git@github.com:jhoot/cockpit", "jhoot/cockpit", false},
		{"invalid", "https://gitlab.com/foo/bar", "", true},
		{"empty", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseGitHubRepo(tt.input)
			if tt.hasErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.expect {
				t.Errorf("got %q, want %q", got, tt.expect)
			}
		})
	}
}

func TestParsePRList(t *testing.T) {
	jsonData := []byte(`[
		{"number": 1, "title": "Fix bug", "isDraft": false, "reviewDecision": "REVIEW_REQUIRED"},
		{"number": 2, "title": "WIP feature", "isDraft": true, "reviewDecision": ""},
		{"number": 3, "title": "Ready", "isDraft": false, "reviewDecision": "APPROVED"}
	]`)

	prs, err := ParsePRList(jsonData)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(prs) != 3 {
		t.Fatalf("got %d PRs, want 3", len(prs))
	}

	// Count review required
	reviewRequired := 0
	drafts := 0
	for _, pr := range prs {
		if pr.ReviewDecision == "REVIEW_REQUIRED" {
			reviewRequired++
		}
		if pr.IsDraft {
			drafts++
		}
	}
	if reviewRequired != 1 {
		t.Errorf("review_required = %d, want 1", reviewRequired)
	}
	if drafts != 1 {
		t.Errorf("drafts = %d, want 1", drafts)
	}
}

func TestParseRunList(t *testing.T) {
	tests := []struct {
		name       string
		json       string
		expectLen  int
		conclusion string
	}{
		{
			"failing",
			`[{"status": "completed", "conclusion": "failure"}]`,
			1, "failure",
		},
		{
			"passing",
			`[{"status": "completed", "conclusion": "success"}]`,
			1, "success",
		},
		{
			"empty",
			`[]`,
			0, "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runs, err := ParseRunList([]byte(tt.json))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(runs) != tt.expectLen {
				t.Fatalf("got %d runs, want %d", len(runs), tt.expectLen)
			}
			if tt.expectLen > 0 && runs[0].Conclusion != tt.conclusion {
				t.Errorf("conclusion = %q, want %q", runs[0].Conclusion, tt.conclusion)
			}
		})
	}
}
