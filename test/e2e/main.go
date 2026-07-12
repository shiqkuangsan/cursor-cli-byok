package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type fakeProviderServerOptions struct {
	APIKey    string
	Workspace string
	LogPath   string
	ReadyPath string
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer cancel()
	os.Exit(runE2EHelper(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

func runE2EHelper(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	getenv func(string) string,
) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if getenv == nil {
		getenv = os.Getenv
	}
	if len(args) >= 1 && args[0] == "output-check" {
		if err := runHeadlessOutputCheck(args[1:], stdin); err != nil {
			_, _ = fmt.Fprintf(stderr, "cursor-cli-byok-e2e: output-check: %v\n", err)
			return 1
		}
		return 0
	}
	if len(args) == 1 && args[0] == "mcp" {
		if err := runMCPServer(stdin, stdout); err != nil {
			_, _ = fmt.Fprintln(stderr, "cursor-cli-byok-e2e: MCP server stopped")
			return 1
		}
		return 0
	}
	if len(args) >= 1 && args[0] == "provider" {
		options, err := parseFakeProviderServerOptions(args[1:], getenv)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "cursor-cli-byok-e2e: provider: %v\n", err)
			return 2
		}
		if err := serveFakeProvider(ctx, options); err != nil {
			_, _ = fmt.Fprintln(stderr, "cursor-cli-byok-e2e: provider stopped unexpectedly")
			return 1
		}
		return 0
	}
	_, _ = fmt.Fprintln(stderr, "usage: cursor-cli-byok-e2e <output-check|provider|mcp>")
	return 2
}

func parseFakeProviderServerOptions(args []string, getenv func(string) string) (fakeProviderServerOptions, error) {
	flags := flag.NewFlagSet("provider", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var apiKeyEnvironment string
	var options fakeProviderServerOptions
	flags.StringVar(&apiKeyEnvironment, "api-key-env", "", "API key environment variable")
	flags.StringVar(&options.Workspace, "workspace", "", "E2E workspace")
	flags.StringVar(&options.LogPath, "log-file", "", "audit log")
	flags.StringVar(&options.ReadyPath, "ready-file", "", "readiness URL file")
	if err := flags.Parse(args); err != nil {
		return fakeProviderServerOptions{}, errors.New("invalid options")
	}
	if flags.NArg() != 0 {
		return fakeProviderServerOptions{}, errors.New("unexpected arguments")
	}
	if !environmentNamePattern.MatchString(apiKeyEnvironment) {
		return fakeProviderServerOptions{}, errors.New("--api-key-env is required")
	}
	options.APIKey = getenv(apiKeyEnvironment)
	if options.APIKey == "" || strings.IndexFunc(options.APIKey, func(value rune) bool { return value < 0x20 || value == 0x7f }) >= 0 {
		return fakeProviderServerOptions{}, errors.New("API key environment variable is missing or invalid")
	}
	if err := validateExistingDirectory(options.Workspace, "--workspace"); err != nil {
		return fakeProviderServerOptions{}, err
	}
	for name, path := range map[string]string{
		"--log-file":   options.LogPath,
		"--ready-file": options.ReadyPath,
	} {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) == filepath.Dir(filepath.Clean(path)) {
			return fakeProviderServerOptions{}, fmt.Errorf("%s must be an absolute file path", name)
		}
		if err := validateExistingDirectory(filepath.Dir(path), name+" parent"); err != nil {
			return fakeProviderServerOptions{}, err
		}
	}
	if filepath.Clean(options.LogPath) == filepath.Clean(options.ReadyPath) {
		return fakeProviderServerOptions{}, errors.New("--log-file and --ready-file must differ")
	}
	return options, nil
}

func validateExistingDirectory(path, name string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be an absolute directory", name)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%s must be an existing directory", name)
	}
	return nil
}

func serveFakeProvider(ctx context.Context, options fakeProviderServerOptions) error {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	endpoint := "http://" + listener.Addr().String()
	if err := writePrivateFile(options.ReadyPath, []byte(endpoint+"\n")); err != nil {
		return err
	}
	defer os.Remove(options.ReadyPath)

	server := &http.Server{
		Handler: newFakeProvider(fakeProviderOptions{
			APIKey:    options.APIKey,
			Workspace: options.Workspace,
			LogPath:   options.LogPath,
		}),
		ReadHeaderTimeout: 3 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	result := make(chan error, 1)
	go func() {
		result <- server.Serve(listener)
	}()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownContext); err != nil {
			return err
		}
		err := <-result
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func writePrivateFile(path string, contents []byte) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	removeTemporary = false
	return os.Chmod(path, 0o600)
}
