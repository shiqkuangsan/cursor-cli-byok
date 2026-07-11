package command

import (
	"errors"
	"fmt"
	"os"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/daemon"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/paths"
)

func (a App) runStatus(args []string) int {
	if len(args) != 0 {
		return usageExitCode
	}
	runtimePaths, err := paths.Resolve(a.Getenv)
	if err != nil {
		return a.fail(fmt.Errorf("status: %w", err))
	}
	state, err := daemon.NewStateStore(daemon.StatePath(runtimePaths)).Load()
	if errors.Is(err, os.ErrNotExist) {
		if _, writeError := fmt.Fprintln(a.Stdout, "daemon: stopped"); writeError != nil {
			return a.fail(errors.New("status: write output"))
		}
		return 0
	}
	if err != nil {
		return writeStaleStatus(a, "state file is invalid")
	}
	if !a.ProcessAlive(state.PID) {
		return writeStaleStatus(a, "process is not running")
	}
	if err := a.DaemonProbe.Check(a.Context, state); err != nil {
		return writeStaleStatus(a, "health check failed")
	}
	if _, err := fmt.Fprintf(a.Stdout,
		"daemon: running\npid: %d\nendpoint: %s\nversion: %s\n",
		state.PID, state.EndpointURL(), state.DaemonVersion,
	); err != nil {
		return a.fail(errors.New("status: write output"))
	}
	return 0
}

func writeStaleStatus(a App, reason string) int {
	if _, err := fmt.Fprintf(a.Stdout, "daemon: stale\nreason: %s\n", reason); err != nil {
		return a.fail(errors.New("status: write output"))
	}
	return errorExitCode
}
