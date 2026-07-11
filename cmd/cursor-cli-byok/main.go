package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/command"
)

func main() {
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signals)
	app := command.App{
		Context:    context.Background(),
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Getenv:     os.Getenv,
		Environ:    os.Environ,
		Executable: os.Executable,
		Signals:    signals,
	}
	os.Exit(app.Run(os.Args[1:]))
}
