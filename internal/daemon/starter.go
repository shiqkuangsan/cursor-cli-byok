package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const inheritedLockFDEnvironment = "CURSOR_CLI_BYOK_LOCK_FD"

type ExecStarter struct {
	Executable            string
	ParentEnv             []string
	ProviderSecretEnvKeys []string
}

func AdoptInheritedLock(getenv func(string) string, expectedPath string) (*FileLock, error) {
	if getenv == nil {
		return nil, errors.New("adopt inherited daemon lock: environment lookup is required")
	}
	if !filepath.IsAbs(expectedPath) {
		return nil, errors.New("adopt inherited daemon lock: expected path must be absolute")
	}
	rawFD := getenv(inheritedLockFDEnvironment)
	fd, err := strconv.ParseUint(rawFD, 10, 31)
	if err != nil || fd < 3 {
		return nil, errors.New("adopt inherited daemon lock: lock descriptor is invalid")
	}
	file := os.NewFile(uintptr(fd), "inherited-daemon-lock")
	if file == nil {
		return nil, errors.New("adopt inherited daemon lock: lock descriptor is unavailable")
	}
	fileInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("adopt inherited daemon lock: inspect descriptor: %w", err)
	}
	expectedInfo, err := os.Stat(expectedPath)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("adopt inherited daemon lock: inspect expected path: %w", err)
	}
	if !os.SameFile(fileInfo, expectedInfo) {
		_ = file.Close()
		return nil, errors.New("adopt inherited daemon lock: descriptor does not match expected lock path")
	}
	return AdoptLockFile(file)
}

func (s ExecStarter) Start(ctx context.Context, lockFile *os.File) (Child, error) {
	if ctx == nil {
		return nil, errors.New("start daemon: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}
	if !filepath.IsAbs(s.Executable) {
		return nil, errors.New("start daemon: executable path must be absolute")
	}
	if lockFile == nil {
		return nil, errors.New("start daemon: inherited lock file is required")
	}
	if _, err := lockFile.Stat(); err != nil {
		return nil, fmt.Errorf("start daemon: inspect lock file: %w", err)
	}
	for _, key := range s.ProviderSecretEnvKeys {
		if !validDaemonEnvironmentKey(key) || key == inheritedLockFDEnvironment {
			return nil, errors.New("start daemon: provider secret environment name is invalid")
		}
	}

	command := exec.Command(s.Executable, "serve", "--background-child")
	command.Env = daemonEnvironment(s.ParentEnv, s.ProviderSecretEnvKeys)
	command.ExtraFiles = []*os.File{lockFile}
	configureDetached(command)
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("start daemon: execute background child: %w", err)
	}
	waitResult := make(chan error, 1)
	go func() {
		waitResult <- command.Wait()
	}()
	return &execChild{process: command.Process, waitResult: waitResult}, nil
}

type execChild struct {
	mu         sync.Mutex
	process    *os.Process
	waitResult <-chan error
}

func (c *execChild) PID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.process == nil {
		return 0
	}
	return c.process.Pid
}

func (c *execChild) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("stop daemon process: context is required")
	}
	c.mu.Lock()
	if c.process == nil {
		c.mu.Unlock()
		return nil
	}
	process := c.process
	waitResult := c.waitResult
	c.process = nil
	c.mu.Unlock()
	killError := killDetached(process)
	if errors.Is(killError, os.ErrProcessDone) {
		killError = nil
	}
	select {
	case waitError := <-waitResult:
		var exitError *exec.ExitError
		if errors.As(waitError, &exitError) || errors.Is(waitError, os.ErrProcessDone) {
			waitError = nil
		}
		return errors.Join(killError, waitError)
	case <-ctx.Done():
		return errors.Join(killError, ctx.Err())
	}
}

func (c *execChild) Detach() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.process == nil {
		return nil
	}
	c.process = nil
	return nil
}

func daemonEnvironment(parent, providerSecretKeys []string) []string {
	preserved := make(map[string]struct{}, len(providerSecretKeys))
	for _, key := range providerSecretKeys {
		preserved[key] = struct{}{}
	}
	environment := make(map[string]string, len(parent)+1)
	for _, entry := range parent {
		key, value, found := strings.Cut(entry, "=")
		if !found || key == "" {
			continue
		}
		_, preserve := preserved[key]
		if daemonEnvironmentReserved(key) && !preserve {
			continue
		}
		environment[key] = value
	}
	environment[inheritedLockFDEnvironment] = "3"

	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+environment[key])
	}
	return result
}

func validDaemonEnvironmentKey(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' {
			continue
		}
		if index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func daemonEnvironmentReserved(key string) bool {
	if strings.HasPrefix(key, "CURSOR_CLI_BYOK_") || strings.HasPrefix(key, "CURSOR_LOCAL_AGENT_") {
		return true
	}
	switch key {
	case "CURSOR_AGENT_CLI_AUTHLESS_MODE",
		"CURSOR_AGENT_CLI_LOCAL_MODE",
		"CURSOR_API_BASE_URL",
		"CURSOR_API_ENDPOINT",
		"CURSOR_API_KEY",
		"CURSOR_AUTH_TOKEN",
		"CURSOR_ENABLE_AUTHLESS",
		"CURSOR_ENABLE_BEDROCK",
		"CURSOR_ENABLE_LOCAL_BEDROCK":
		return true
	default:
		return false
	}
}
