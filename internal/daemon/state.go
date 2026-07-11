package daemon

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
)

const CurrentStateVersion = 1

var instanceIDPattern = regexp.MustCompile(`^[0-9a-f]{32,64}$`)

type State struct {
	Version       int       `json:"version"`
	PID           int       `json:"pid"`
	Port          int       `json:"port"`
	CACertPath    string    `json:"ca_cert_path"`
	DaemonVersion string    `json:"daemon_version"`
	InstanceID    string    `json:"instance_id"`
	AuthToken     string    `json:"auth_token"`
	StartedAt     time.Time `json:"started_at"`
}

func (s State) String() string {
	return fmt.Sprintf(
		"State{Version:%d PID:%d Port:%d CACertPath:%q DaemonVersion:%q InstanceID:%q AuthToken:%q StartedAt:%q}",
		s.Version,
		s.PID,
		s.Port,
		s.CACertPath,
		s.DaemonVersion,
		s.InstanceID,
		"[REDACTED]",
		s.StartedAt.Format(time.RFC3339Nano),
	)
}

func (s State) GoString() string {
	return s.String()
}

func (s State) Validate() error {
	if s.Version != CurrentStateVersion {
		return errors.New("validate daemon state: unsupported version")
	}
	if s.PID < 1 {
		return errors.New("validate daemon state: PID must be positive")
	}
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("validate daemon state: port is invalid")
	}
	if !filepath.IsAbs(s.CACertPath) {
		return errors.New("validate daemon state: CA certificate path must be absolute")
	}
	if s.DaemonVersion == "" || s.DaemonVersion != strings.TrimSpace(s.DaemonVersion) || containsControl(s.DaemonVersion) {
		return errors.New("validate daemon state: daemon version is invalid")
	}
	if !instanceIDPattern.MatchString(s.InstanceID) {
		return errors.New("validate daemon state: instance ID is invalid")
	}
	if !validJWTShape(s.AuthToken) {
		return errors.New("validate daemon state: auth token is invalid")
	}
	if s.StartedAt.IsZero() {
		return errors.New("validate daemon state: start time is required")
	}
	return nil
}

func (s State) EndpointURL() string {
	return fmt.Sprintf("https://127.0.0.1:%d", s.Port)
}

type StateStore struct {
	path string
}

func NewStateStore(path string) StateStore {
	return StateStore{path: path}
}

func (s StateStore) Load() (State, error) {
	if err := s.validatePath("load"); err != nil {
		return State{}, err
	}
	directory := filepath.Dir(s.path)
	if err := os.Chmod(directory, 0o700); err != nil {
		return State{}, fmt.Errorf("load daemon state: secure directory permissions: %w", err)
	}
	info, err := os.Lstat(s.path)
	if err != nil {
		return State{}, fmt.Errorf("load daemon state: inspect file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return State{}, errors.New("load daemon state: state file must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return State{}, errors.New("load daemon state: state file must be regular")
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return State{}, fmt.Errorf("load daemon state: secure file permissions: %w", err)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return State{}, fmt.Errorf("load daemon state: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state State
	if err := decoder.Decode(&state); err != nil {
		return State{}, errors.New("load daemon state: decode JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return State{}, errors.New("load daemon state: decode JSON")
	}
	if err := state.Validate(); err != nil {
		return State{}, fmt.Errorf("load daemon state: %w", err)
	}
	return state, nil
}

func (s StateStore) Save(state State) error {
	if err := s.validatePath("save"); err != nil {
		return err
	}
	if err := state.Validate(); err != nil {
		return fmt.Errorf("save daemon state: %w", err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("save daemon state: encode JSON: %w", err)
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("save daemon state: create directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("save daemon state: secure directory permissions: %w", err)
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("save daemon state: create temporary file: %w", err)
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
		return fmt.Errorf("save daemon state: secure temporary file permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("save daemon state: write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("save daemon state: sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("save daemon state: close temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("save daemon state: replace file: %w", err)
	}
	removeTemporary = false
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("save daemon state: secure file permissions: %w", err)
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("save daemon state: open directory for sync: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("save daemon state: sync directory: %w", err)
	}
	return nil
}

func (s StateStore) Remove() error {
	if err := s.validatePath("remove"); err != nil {
		return err
	}
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove daemon state: %w", err)
	}
	return nil
}

func (s StateStore) validatePath(action string) error {
	if s.path == "" || !filepath.IsAbs(s.path) {
		return fmt.Errorf("%s daemon state: path must be absolute", action)
	}
	cleanPath := filepath.Clean(s.path)
	if filepath.Dir(cleanPath) == cleanPath {
		return fmt.Errorf("%s daemon state: path must name a file", action)
	}
	return nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func validJWTShape(token string) bool {
	if token == "" || len(token) > 4096 || containsControl(token) {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		if _, err := base64.RawURLEncoding.DecodeString(part); err != nil {
			return false
		}
	}
	return true
}
