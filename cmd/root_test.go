package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitCreatesConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfgPath = path
	SetConfigTemplate(func() string { return "[general]\nsession_name = \"test\"\n" })

	err := runInit(nil, nil)
	if err != nil {
		t.Fatalf("init error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}
	if len(data) == 0 {
		t.Error("config file is empty")
	}
}

func TestInitDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfgPath = path

	os.WriteFile(path, []byte("existing"), 0644)
	SetConfigTemplate(func() string { return "new content" })

	err := runInit(nil, nil)
	if err != nil {
		t.Fatalf("init error: %v", err)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "existing" {
		t.Errorf("config was overwritten: got %q", string(data))
	}
}
