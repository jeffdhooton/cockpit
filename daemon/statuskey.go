package daemon

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// statusKeyName is the file under the config directory holding the key that
// derives per-target status tokens.
const statusKeyName = "status-key"

// statusKeyLen is the key size. A short file is a broken key rather than a
// weak one: every hook would derive a token the daemon cannot reproduce.
const statusKeyLen = 32

// StatusToken derives the token a hook must present to write one target's
// status. Deriving rather than storing is what keeps the daemon stateless —
// nothing is recorded per run, so a restart loses nothing.
//
// Say plainly what this does and does not do. Any process running as you can
// read the key, so it is no defence against local code. It stops a request
// from another user on the machine, and it means the endpoint cannot be driven
// by accident. The browser case is closed a layer up by guard, so this token
// is not carrying that weight.
func StatusToken(key []byte, target string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(target))
	return hex.EncodeToString(mac.Sum(nil))
}

// LoadOrCreateStatusKey reads the key from dir, creating it on first use.
//
// The hook and the daemon both call this, and either may be first: a hook can
// fire before the daemon has ever written to the config directory.
func LoadOrCreateStatusKey(dir string) ([]byte, error) {
	path := filepath.Join(dir, statusKeyName)

	key, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(key) != statusKeyLen {
			return nil, fmt.Errorf("status key %s: want %d bytes, got %d",
				path, statusKeyLen, len(key))
		}
		return key, nil
	case !errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("read status key: %w", err)
	}

	key = make([]byte, statusKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate status key: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("write status key: %w", err)
	}
	return key, nil
}
