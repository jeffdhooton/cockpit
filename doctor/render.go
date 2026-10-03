package doctor

import (
	"fmt"
	"io"
	"strings"
)

// Render writes the human report.
func Render(w io.Writer, r Report) {
	fmt.Fprintln(w, "Cockpit doctor")
	fmt.Fprintln(w)
	scope := ""
	for _, ch := range r.Checks {
		if ch.Scope != scope {
			scope = ch.Scope
			if scope != "local" {
				fmt.Fprintf(w, "\nHost %s\n", scope)
			}
		}
		fmt.Fprintf(w, "%-4s  %-14s %s\n", strings.ToUpper(string(ch.Status)), ch.ID, ch.Summary)
		for _, e := range ch.Evidence {
			fmt.Fprintf(w, "      %s\n", e)
		}
		if ch.Remedy != "" && ch.Status != Pass {
			fmt.Fprintf(w, "      Next: %s\n", ch.Remedy)
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, Summary(r))
}

// Summary is the one-line verdict.
func Summary(r Report) string {
	var parts []string
	if r.CoreReady {
		parts = append(parts, "Core ready")
	} else {
		parts = append(parts, "Core not ready")
	}
	if n := r.Counts[Warn]; n == 1 {
		parts = append(parts, "1 optional feature needs attention")
	} else if n > 1 {
		parts = append(parts, fmt.Sprintf("%d optional features need attention", n))
	}
	unchecked := 0
	for _, ch := range r.Checks {
		if ch.ID == "hosts" && ch.Status == Skip && strings.Contains(ch.Summary, "not checked") {
			unchecked++
		}
	}
	if unchecked > 0 {
		parts = append(parts, "hosts unchecked")
	}
	if len(r.Incomplete) > 0 {
		parts = append(parts, fmt.Sprintf("%d %s incomplete", len(r.Incomplete), plural(len(r.Incomplete), "check")))
	}
	return strings.Join(parts, " · ")
}
