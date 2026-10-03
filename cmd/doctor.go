package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/jeffdhooton/cockpit/config"
	"github.com/jeffdhooton/cockpit/doctor"
	"github.com/spf13/cobra"
)

// exitError carries an explicit process exit code out of a command. The
// root's blanket exit-1 must not swallow doctor's contract: 0 no failures,
// 1 diagnostic failures, 2 usage error.
type exitError struct {
	code int
	msg  string
}

func (e exitError) Error() string { return e.msg }

var (
	doctorHost     string
	doctorAllHosts bool
	doctorJSON     bool
	doctorNoHosts  bool
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose this installation without changing anything",
	Long: `Check config, tmux, repositories, the daemon, agent hooks and enabled
integrations, and say what is working, what is unavailable, and the smallest
next action. Doctor never installs, starts, edits, trusts or launches anything.

Exit 0: no failures (warnings allowed). Exit 1: diagnostic failures.
Exit 2: invalid usage.`,
	SilenceErrors: true,
	RunE:          runDoctor,
}

func init() {
	doctorCmd.Flags().StringVar(&doctorHost, "host", "", "also diagnose one configured ssh host")
	doctorCmd.Flags().BoolVar(&doctorAllHosts, "all-hosts", false, "also diagnose every configured ssh host")
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "print one JSON document on stdout")
	doctorCmd.Flags().BoolVar(&doctorNoHosts, "no-hosts", false, "never probe hosts (set by a remote doctor)")
	_ = doctorCmd.Flags().MarkHidden("no-hosts")
	rootCmd.AddCommand(doctorCmd)
}

func runDoctor(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return exitError{2, "doctor takes no positional arguments"}
	}
	if doctorHost != "" && doctorAllHosts {
		return exitError{2, "--host and --all-hosts are mutually exclusive"}
	}
	if doctorHost != "" {
		// An unknown host is a usage error, decided before any probe. A
		// missing or broken config cannot answer, and doctor then reports
		// that instead of guessing.
		if cfg, err := config.Load(getConfigPath()); err == nil {
			if _, ok := cfg.Host(doctorHost); !ok {
				return exitError{2, fmt.Sprintf("host %q is not declared under [[hosts]] in %s", doctorHost, getConfigPath())}
			}
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	deps := doctor.SystemDeps(version)
	if !doctorJSON {
		deps.Progress = func(line string) { fmt.Fprintln(os.Stderr, "  "+line) }
	}
	rep := doctor.Run(ctx, doctor.Options{
		ConfigPath: getConfigPath(),
		Host:       doctorHost,
		AllHosts:   doctorAllHosts,
		NoHosts:    doctorNoHosts,
	}, deps)

	if doctorJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return exitError{1, err.Error()}
		}
	} else {
		fmt.Fprintln(os.Stderr)
		doctor.Render(os.Stdout, rep)
	}
	if rep.Failed() {
		return exitError{1, ""}
	}
	return nil
}
