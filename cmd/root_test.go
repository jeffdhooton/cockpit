package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jhoot/cockpit/config"
	"github.com/spf13/cobra"
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

func TestRuntimeErrorsDoNotPrintUsage(t *testing.T) {
	// A failing command should show its error, not bury it under twenty lines
	// of help text. Cobra prints usage on RunE errors unless told not to.
	failing := &cobra.Command{
		Use:  "boom",
		RunE: func(*cobra.Command, []string) error { return errors.New("cannot bind port 45679") },
	}
	rootCmd.AddCommand(failing)
	t.Cleanup(func() { rootCmd.RemoveCommand(failing) })

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs([]string{"boom"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	if err := rootCmd.Execute(); err == nil {
		t.Fatal("the command should have failed")
	}
	if strings.Contains(out.String(), "Usage:") {
		t.Errorf("a runtime error must not print usage, got:\n%s", out.String())
	}
}

func TestCapWithoutATodayFileSaysSo(t *testing.T) {
	err := capture(&config.Config{}, "a thought")
	if err == nil || !strings.Contains(err.Error(), "today_file") {
		t.Errorf("want a clear error naming today_file, got %v", err)
	}
}
