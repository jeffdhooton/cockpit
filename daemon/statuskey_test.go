package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatusTokenIsStableAndTargetBound(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")

	// The hook derives the token independently of the daemon, so the two must
	// agree from the same inputs with nothing shared between them.
	fromHook := StatusToken(key, "app:dev")
	fromDaemon := StatusToken(key, "app:dev")
	if fromHook != fromDaemon {
		t.Error("the same target must derive the same token")
	}
	if StatusToken(key, "app:dev") == StatusToken(key, "other:dev") {
		t.Error("a token for one target must not authorise another")
	}
}

func TestStatusTokenDependsOnTheKey(t *testing.T) {
	a := []byte("0123456789abcdef0123456789abcdef")
	b := []byte("fedcba9876543210fedcba9876543210")

	if StatusToken(a, "app:dev") == StatusToken(b, "app:dev") {
		t.Error("a different key must derive a different token")
	}
}

func TestStatusKeyIsCreatedPrivateAndReused(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrCreateStatusKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 {
		t.Errorf("key length = %d, want 32", len(first))
	}

	info, err := os.Stat(filepath.Join(dir, "status-key"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key mode = %o, want 600", perm)
	}

	second, err := LoadOrCreateStatusKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("a rotated key would invalidate every installed hook")
	}
}

func TestStatusKeyCreatesAMissingDirectory(t *testing.T) {
	// The hook may be the first thing to ask for the key, before the daemon
	// has ever written to the config directory.
	dir := filepath.Join(t.TempDir(), "nested", "cockpit")

	if _, err := LoadOrCreateStatusKey(dir); err != nil {
		t.Fatalf("a missing config directory is not a failure: %v", err)
	}
}

func TestStatusKeyRejectsATruncatedFile(t *testing.T) {
	// A short key is not a working key. Silently accepting one would mean
	// every hook derives a token the daemon cannot reproduce.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "status-key"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadOrCreateStatusKey(dir); err == nil {
		t.Error("a truncated key must be reported, not used")
	}
}
