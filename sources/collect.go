package sources

import (
	"context"
	"time"

	"github.com/jeffdhooton/cockpit/config"
)

// ObserveHostReport reads everything the attention queue needs from one
// host: sessions and pane records, git status, and the process windows of
// every configured project there. A failed session read makes the whole
// report unavailable; a failed process read is recorded per project so
// the rest of the host still counts.
//
// hostNow is that machine's clock, which status stamps are aged against;
// observedAt is this machine's, which the queue shows as age.
func ObserveHostReport(ctx context.Context, r Runner, cr CommandRunner, host string, repos []config.RepoConfig, hostNow, observedAt time.Time) HostReport {
	rep := HostReport{Host: host, Outcome: ObservationFresh, ObservedAt: observedAt}

	obs, err := ObserveHost(ctx, r, host, hostNow)
	if err != nil {
		rep.Outcome = ObservationUnavailable
		rep.Err = err.Error()
		return rep
	}
	rep.Sessions = obs.Sessions
	rep.Panes = obs.Panes

	if cr != nil && len(repos) > 0 {
		rep.Git = GetGitStatus(ctx, cr, repos)
	}
	for _, repo := range repos {
		if len(repo.Processes) == 0 {
			continue
		}
		po, err := ObserveProcesses(ctx, r, repo, hostNow)
		if err != nil {
			if rep.ProcessErrs == nil {
				rep.ProcessErrs = map[string]string{}
			}
			rep.ProcessErrs[repo.Key()] = err.Error()
			continue
		}
		if rep.Processes == nil {
			rep.Processes = map[string]ProcessObservation{}
		}
		po.ObservedAt = observedAt
		rep.Processes[repo.Key()] = po
	}
	return rep
}
