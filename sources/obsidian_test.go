package sources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadTasks(t *testing.T) {
	tasks, err := ReadTasks("../testdata/today.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("got %d tasks, want 3", len(tasks))
	}

	// First task: done
	if tasks[0].Text != "Ship auth fix" {
		t.Errorf("task[0].Text = %q, want %q", tasks[0].Text, "Ship auth fix")
	}
	if !tasks[0].Done {
		t.Error("task[0] should be done")
	}
	if tasks[0].Line != 5 {
		t.Errorf("task[0].Line = %d, want 5", tasks[0].Line)
	}

	// Second task: not done
	if tasks[1].Text != "Review epub export PR" {
		t.Errorf("task[1].Text = %q, want %q", tasks[1].Text, "Review epub export PR")
	}
	if tasks[1].Done {
		t.Error("task[1] should not be done")
	}
	if tasks[1].Line != 6 {
		t.Errorf("task[1].Line = %d, want 6", tasks[1].Line)
	}

	// Third task
	if tasks[2].Text != "Call Jake re: partnership" {
		t.Errorf("task[2].Text = %q, want %q", tasks[2].Text, "Call Jake re: partnership")
	}
	if tasks[2].Line != 7 {
		t.Errorf("task[2].Line = %d, want 7", tasks[2].Line)
	}
}

func TestReadTasksIgnoresNonTaskLines(t *testing.T) {
	tasks, err := ReadTasks("../testdata/today.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// File has headings, prose, blank lines, notes section — only 3 tasks
	if len(tasks) != 3 {
		t.Errorf("got %d tasks, want 3 (non-task lines ignored)", len(tasks))
	}
}

func TestReadTasksNonexistentFile(t *testing.T) {
	tasks, err := ReadTasks("/nonexistent/path/today.md")
	if err != nil {
		t.Fatalf("expected nil error for nonexistent file, got: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("got %d tasks, want 0", len(tasks))
	}
}

func TestToggleTaskCheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "today.md")
	content := "# Today\n\n- [ ] Do something\n- [x] Done thing\n"
	os.WriteFile(path, []byte(content), 0644)

	// Toggle unchecked → checked
	if err := ToggleTask(path, 3); err != nil {
		t.Fatalf("toggle error: %v", err)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "- [x] Do something") {
		t.Errorf("expected toggled task, got:\n%s", data)
	}
	// Ensure other lines preserved
	if !strings.Contains(string(data), "# Today") {
		t.Error("heading was not preserved")
	}
	if !strings.Contains(string(data), "- [x] Done thing") {
		t.Error("other task was not preserved")
	}
}

func TestToggleTaskUncheck(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "today.md")
	content := "# Today\n\n- [x] Done thing\n"
	os.WriteFile(path, []byte(content), 0644)

	if err := ToggleTask(path, 3); err != nil {
		t.Fatalf("toggle error: %v", err)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "- [ ] Done thing") {
		t.Errorf("expected unchecked task, got:\n%s", data)
	}
}

func TestAppendInbox(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inbox.md")
	// Copy existing content
	os.WriteFile(path, []byte("- [ ] existing item\n"), 0644)

	if err := AppendInbox(path, "new thought"); err != nil {
		t.Fatalf("append error: %v", err)
	}

	data, _ := os.ReadFile(path)
	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "existing item") {
		t.Error("existing content was not preserved")
	}
	if !strings.Contains(lines[1], "- [ ] new thought") {
		t.Errorf("appended line = %q, want to contain '- [ ] new thought'", lines[1])
	}
	if !strings.Contains(lines[1], "—") {
		t.Error("appended line missing timestamp separator")
	}
}

func TestAppendInboxCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new_inbox.md")

	if err := AppendInbox(path, "first thought"); err != nil {
		t.Fatalf("append error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if !strings.Contains(string(data), "- [ ] first thought") {
		t.Errorf("content = %q, want to contain task", string(data))
	}
}
