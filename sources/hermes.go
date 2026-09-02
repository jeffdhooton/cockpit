package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jhoot/cockpit/config"
)

// hermesBodyLimit bounds the status document. It is a few hundred bytes; the
// cap exists so a misconfigured URL pointing at something large cannot cost a
// megabyte per poll.
const hermesBodyLimit = 64 << 10

// HermesStatus is what the tile shows: whether the dashboard answered, and
// if so whether the gateway is running and which platforms are connected.
// Unreachable and stopped are different facts and render differently.
type HermesStatus struct {
	Label     string
	Host      string // the [[hosts]] entry Enter opens a shell on, or empty
	Reachable bool
	Gateway   string   // "running", "stopped", or the raw gateway_state
	Platforms []string // connected platform names, sorted
	Version   string
	Err       error
}

// hermesDocument is the allowlist of fields read from /api/status. The real
// document carries paths and config details that a tile has no use for.
type hermesDocument struct {
	Version   string `json:"version"`
	Running   bool   `json:"gateway_running"`
	State     string `json:"gateway_state"`
	Platforms map[string]struct {
		State string `json:"state"`
	} `json:"gateway_platforms"`
}

// GetHermesStatus reads the dashboard's status endpoint. It needs no token
// and sends none.
func GetHermesStatus(ctx context.Context, client *http.Client, cfg config.HermesConfig) HermesStatus {
	st := HermesStatus{Label: cfg.Label, Host: cfg.Host}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(cfg.URL, "/")+"/api/status", nil)
	if err != nil {
		st.Err = err
		return st
	}
	resp, err := client.Do(req)
	if err != nil {
		st.Err = err
		return st
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		st.Err = fmt.Errorf("hermes %s: HTTP %d", cfg.Label, resp.StatusCode)
		return st
	}

	var doc hermesDocument
	if err := json.NewDecoder(io.LimitReader(resp.Body, hermesBodyLimit)).Decode(&doc); err != nil {
		st.Err = fmt.Errorf("hermes %s: %w", cfg.Label, err)
		return st
	}

	st.Reachable = true
	st.Version = doc.Version
	switch {
	case doc.Running:
		st.Gateway = "running"
	case doc.State != "":
		st.Gateway = doc.State
	default:
		st.Gateway = "stopped"
	}
	for name, p := range doc.Platforms {
		if p.State == "connected" {
			st.Platforms = append(st.Platforms, name)
		}
	}
	sort.Strings(st.Platforms)
	return st
}
