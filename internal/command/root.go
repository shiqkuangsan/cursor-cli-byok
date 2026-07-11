package command

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/buildinfo"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/daemon"
)

const (
	errorExitCode = 1
	usageExitCode = 2
)

type App struct {
	Context       context.Context
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
	Getenv        func(string) string
	Environ       func() []string
	Executable    func() (string, error)
	Signals       <-chan os.Signal
	DaemonProbe   daemon.Probe
	ProcessAlive  func(int) bool
	SignalProcess func(int, os.Signal) error
}

// Run preserves the small dispatcher API used by embedders and tests.
func Run(args []string, stdout io.Writer) int {
	return App{
		Context:    context.Background(),
		Stdin:      strings.NewReader(""),
		Stdout:     stdout,
		Stderr:     io.Discard,
		Getenv:     os.Getenv,
		Environ:    os.Environ,
		Executable: os.Executable,
	}.Run(args)
}

func (a App) Run(args []string) int {
	a = a.withDefaults()
	if len(args) >= 1 && args[0] == "config" {
		return a.runConfig(args[1:])
	}
	if len(args) >= 1 && args[0] == "serve" {
		return a.runServe(args[1:])
	}
	if len(args) >= 1 && args[0] == "status" {
		return a.runStatus(args[1:])
	}
	if len(args) >= 1 && args[0] == "stop" {
		return a.runStop(args[1:])
	}
	if len(args) >= 1 && args[0] == "doctor" {
		return a.runDoctor(args[1:])
	}
	if len(args) >= 1 && (args[0] == "--version" || args[0] == "-v") {
		if len(args) != 1 {
			return usageExitCode
		}
		if _, err := fmt.Fprintf(a.Stdout, "cursor-cli-byok %s\n", buildinfo.Version); err != nil {
			return errorExitCode
		}
		return 0
	}
	return a.runAgent(args)
}

func (a App) withDefaults() App {
	if a.Context == nil {
		a.Context = context.Background()
	}
	if a.Stdin == nil {
		a.Stdin = strings.NewReader("")
	}
	if a.Stdout == nil {
		a.Stdout = io.Discard
	}
	if a.Stderr == nil {
		a.Stderr = io.Discard
	}
	if a.Getenv == nil {
		a.Getenv = os.Getenv
	}
	if a.Environ == nil {
		a.Environ = os.Environ
	}
	if a.Executable == nil {
		a.Executable = os.Executable
	}
	if a.DaemonProbe == nil {
		a.DaemonProbe = daemon.HTTPProbe{}
	}
	if a.ProcessAlive == nil {
		a.ProcessAlive = daemon.ProcessAlive
	}
	if a.SignalProcess == nil {
		a.SignalProcess = signalProcess
	}
	return a
}

func signalProcess(pid int, signal os.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(signal)
}

func (a App) fail(err error) int {
	_, _ = fmt.Fprintf(a.Stderr, "cursor-cli-byok: %v\n", err)
	return errorExitCode
}
