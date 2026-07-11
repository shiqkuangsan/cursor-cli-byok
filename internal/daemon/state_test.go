package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStateStoreSavesLoadsAndAtomicallyReplacesSecureFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics are required")
	}
	directory := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(directory, "daemon.json")
	store := NewStateStore(path)
	want := validState()

	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(before) error = %v", err)
	}
	assertMode(t, directory, 0o700)
	assertMode(t, path, 0o600)

	want.Port = 43124
	if err := store.Save(want); err != nil {
		t.Fatalf("Save(replacement) error = %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(after) error = %v", err)
	}
	if os.SameFile(before, after) {
		t.Fatal("Save() updated state in place, want atomic replacement")
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != want {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestStateStoreLoadRejectsUnknownFieldsAndSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink behavior is required")
	}
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	t.Run("unknown field", func(t *testing.T) {
		path := filepath.Join(directory, "unknown.json")
		data := `{"version":1,"pid":123,"port":43123,"ca_cert_path":"/tmp/ca.pem","daemon_version":"dev","instance_id":"0123456789abcdef0123456789abcdef","auth_token":"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjdXJzb3ItY2xpLWJ5b2sifQ.c2lnbmF0dXJl","started_at":"2026-07-11T00:00:00Z","unexpected":true}`
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		_, err := NewStateStore(path).Load()
		if err == nil || !strings.Contains(err.Error(), "decode") {
			t.Fatalf("Load() error = %v, want strict decode error", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		target := filepath.Join(directory, "target.json")
		if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		path := filepath.Join(directory, "link.json")
		if err := os.Symlink(target, path); err != nil {
			t.Fatalf("Symlink() error = %v", err)
		}
		_, err := NewStateStore(path).Load()
		if err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("Load() error = %v, want symlink rejection", err)
		}
		assertMode(t, target, 0o644)
	})
}

func TestStateValidationAndEndpoint(t *testing.T) {
	state := validState()
	if err := state.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if got := state.EndpointURL(); got != "https://127.0.0.1:43123" {
		t.Fatalf("EndpointURL() = %q, want loopback endpoint", got)
	}

	tests := []struct {
		name   string
		mutate func(*State)
	}{
		{name: "version", mutate: func(s *State) { s.Version = 2 }},
		{name: "PID", mutate: func(s *State) { s.PID = 0 }},
		{name: "port", mutate: func(s *State) { s.Port = 0 }},
		{name: "CA path", mutate: func(s *State) { s.CACertPath = "relative.pem" }},
		{name: "daemon version", mutate: func(s *State) { s.DaemonVersion = "" }},
		{name: "instance ID", mutate: func(s *State) { s.InstanceID = "short" }},
		{name: "auth token", mutate: func(s *State) { s.AuthToken = "" }},
		{name: "started at", mutate: func(s *State) { s.StartedAt = time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			invalid := state
			tt.mutate(&invalid)
			if err := invalid.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want invalid state error")
			}
		})
	}
}

func TestStateStringRedactsAuthToken(t *testing.T) {
	state := validState()
	output := fmt.Sprintf("%v\n%+v\n%#v", state, state, state)
	if strings.Contains(output, state.AuthToken) {
		t.Fatal("formatted state leaked auth token")
	}
	if !strings.Contains(output, "REDACTED") {
		t.Fatalf("formatted state = %q, want redaction marker", output)
	}
}

func TestStateStoreMissingFilePreservesNotExist(t *testing.T) {
	_, err := NewStateStore(filepath.Join(t.TempDir(), "missing", "daemon.json")).Load()
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load() error = %v, want os.ErrNotExist", err)
	}
}

func TestProcessAliveDetectsCurrentAndMissingPID(t *testing.T) {
	if !ProcessAlive(os.Getpid()) {
		t.Fatal("ProcessAlive(current PID) = false, want true")
	}
	if ProcessAlive(-1) {
		t.Fatal("ProcessAlive(-1) = true, want false")
	}
	if ProcessAlive(1 << 30) {
		t.Fatal("ProcessAlive(large missing PID) = true, want false")
	}
}

func validState() State {
	return State{
		Version:       CurrentStateVersion,
		PID:           123,
		Port:          43123,
		CACertPath:    "/tmp/cursor-cli-byok-ca.pem",
		DaemonVersion: "dev",
		InstanceID:    "0123456789abcdef0123456789abcdef",
		AuthToken:     "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjdXJzb3ItY2xpLWJ5b2sifQ.c2lnbmF0dXJl",
		StartedAt:     time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %q = %04o, want %04o", path, got, want)
	}
}
