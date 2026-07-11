package command

import (
	"bytes"
	"errors"
	"testing"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/buildinfo"
)

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func TestRunPrintsDevelopmentVersion(t *testing.T) {
	originalVersion := buildinfo.Version
	buildinfo.Version = "dev"
	t.Cleanup(func() {
		buildinfo.Version = originalVersion
	})

	tests := []string{"--version", "-v"}

	for _, arg := range tests {
		t.Run(arg, func(t *testing.T) {
			var stdout bytes.Buffer

			exitCode := Run([]string{arg}, &stdout)

			if exitCode != 0 {
				t.Fatalf("Run() exit code = %d, want 0", exitCode)
			}
			const want = "cursor-cli-byok dev\n"
			if got := stdout.String(); got != want {
				t.Fatalf("Run() output = %q, want %q", got, want)
			}
		})
	}
}

func TestRunPrintsConfiguredBuildVersion(t *testing.T) {
	originalVersion := buildinfo.Version
	buildinfo.Version = "v1.2.3"
	t.Cleanup(func() {
		buildinfo.Version = originalVersion
	})

	var stdout bytes.Buffer
	exitCode := Run([]string{"--version"}, &stdout)

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0", exitCode)
	}
	const want = "cursor-cli-byok v1.2.3\n"
	if got := stdout.String(); got != want {
		t.Fatalf("Run() output = %q, want %q", got, want)
	}
}

func TestRunRejectsUnsupportedInvocations(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "version with extra argument", args: []string{"--version", "extra"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer

			exitCode := Run(tt.args, &stdout)

			const wantExitCode = 2
			if exitCode != wantExitCode {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, wantExitCode)
			}
		})
	}
}

func TestRunReturnsNonzeroWhenVersionOutputFails(t *testing.T) {
	exitCode := Run([]string{"--version"}, failingWriter{})

	if exitCode == 0 {
		t.Fatal("Run() exit code = 0, want nonzero")
	}
}
