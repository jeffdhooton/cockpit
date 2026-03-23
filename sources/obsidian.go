package sources

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var taskPattern = regexp.MustCompile(`^(\s*- \[)([ x])(\] .+)$`)

// Task represents a single checkbox task from an Obsidian markdown file.
type Task struct {
	Text string
	Done bool
	Line int // 1-indexed line number in source file
}

// ReadTasks reads all checkbox tasks from a markdown file.
func ReadTasks(filePath string) ([]Task, error) {
	filePath = expandTildePath(filePath)

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var tasks []Task
	for i, line := range strings.Split(string(data), "\n") {
		m := taskPattern.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		tasks = append(tasks, Task{
			Text: strings.TrimSpace(m[3][2:]), // strip "] " prefix
			Done: m[2] == "x",
			Line: i + 1,
		})
	}
	return tasks, nil
}

// ToggleTask toggles the checkbox at the given 1-indexed line number.
func ToggleTask(filePath string, lineNum int) error {
	filePath = expandTildePath(filePath)

	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	lines := strings.Split(string(data), "\n")
	if lineNum < 1 || lineNum > len(lines) {
		return fmt.Errorf("line %d out of range (file has %d lines)", lineNum, len(lines))
	}

	idx := lineNum - 1
	line := lines[idx]

	if strings.Contains(line, "- [ ] ") {
		lines[idx] = strings.Replace(line, "- [ ] ", "- [x] ", 1)
	} else if strings.Contains(line, "- [x] ") {
		lines[idx] = strings.Replace(line, "- [x] ", "- [ ] ", 1)
	} else {
		return fmt.Errorf("line %d is not a task line", lineNum)
	}

	// Atomic write: unique temp file then rename
	dir := filepath.Dir(filePath)
	tmpFile, err := os.CreateTemp(dir, ".cockpit_toggle*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.WriteString(strings.Join(lines, "\n")); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, filePath)
}

// AppendInbox appends a new task line to the inbox file.
func AppendInbox(filePath string, text string) error {
	filePath = expandTildePath(filePath)

	timestamp := time.Now().Format("2006-01-02 15:04")
	entry := fmt.Sprintf("- [ ] %s — %s\n", text, timestamp)

	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(entry)
	return err
}

func expandTildePath(path string) string {
	if !strings.HasPrefix(path, "~") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}
