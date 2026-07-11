package command

import (
	"context"
	"fmt"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/buildinfo"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/cursorcli"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/daemon"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/paths"
)

func (a App) runAgent(args []string) int {
	runtimePaths, err := paths.Resolve(a.Getenv)
	if err != nil {
		return a.fail(err)
	}
	cfg, err := config.NewStore(runtimePaths.ConfigFile).Load()
	if err != nil {
		return a.fail(fmt.Errorf("launch Cursor CLI: %w", err))
	}
	selectedModel, forwardedArguments, err := cursorcli.SplitModelArgument(cfg.DefaultModel, args)
	if err != nil {
		return a.fail(err)
	}
	resolvedModel, err := cfg.ResolveModel(selectedModel, a.Getenv)
	if err != nil {
		return a.fail(fmt.Errorf("launch Cursor CLI: %w", err))
	}
	cursorPath, err := cursorcli.Find(a.Getenv)
	if err != nil {
		return a.fail(err)
	}
	versionContext, cancelVersion := context.WithTimeout(a.Context, 3*time.Second)
	cursorVersion, versionError := cursorcli.ReadVersion(versionContext, cursorPath)
	cancelVersion()
	if versionError != nil {
		_, _ = fmt.Fprintf(a.Stderr, "cursor-cli-byok: warning: %v\n", versionError)
	} else if !cursorcli.IsVerifiedVersion(cursorVersion) {
		_, _ = fmt.Fprintf(a.Stderr, "cursor-cli-byok: warning: cursor-agent version %s is untested\n", cursorVersion)
	}
	executable, err := a.Executable()
	if err != nil {
		return a.fail(fmt.Errorf("launch Cursor CLI: locate cursor-cli-byok executable: %w", err))
	}
	manager := daemon.Manager{
		Store:    daemon.NewStateStore(daemon.StatePath(runtimePaths)),
		LockPath: daemon.LockPath(runtimePaths),
		Probe:    daemon.HTTPProbe{Timeout: time.Second},
		Starter: daemon.ExecStarter{
			Executable:            executable,
			ParentEnv:             a.Environ(),
			ProviderSecretEnvKeys: providerSecretEnvironmentKeys(cfg),
		},
		ExpectedVersion: buildinfo.Version,
		Shutdown: func(ctx context.Context, state daemon.State) error {
			return daemon.ShutdownService(ctx, state, 2*time.Second)
		},
		StartTimeout: 10 * time.Second,
		StopTimeout:  2 * time.Second,
		PollInterval: 50 * time.Millisecond,
	}
	state, err := manager.Ensure(a.Context)
	if err != nil {
		return a.fail(fmt.Errorf("launch Cursor CLI: %w", err))
	}
	providerValues := selectedProviderEnvironmentValues(cfg, selectedModel, resolvedModel.APIKey)
	syncContext, cancelSync := context.WithTimeout(a.Context, 2*time.Second)
	syncError := daemon.SyncProviderEnvironment(syncContext, state, providerValues, 2*time.Second)
	cancelSync()
	if syncError != nil {
		return a.fail(fmt.Errorf("launch Cursor CLI: %w", syncError))
	}
	spec, err := cursorcli.BuildLaunchSpec(cursorcli.LaunchOptions{
		CursorPath:            cursorPath,
		EndpointURL:           state.EndpointURL(),
		Model:                 selectedModel,
		CACertPath:            state.CACertPath,
		AuthToken:             state.AuthToken,
		ParentEnv:             a.Environ(),
		ProviderSecretEnvKeys: providerSecretEnvironmentKeys(cfg),
		UserArgs:              forwardedArguments,
	})
	if err != nil {
		return a.fail(err)
	}
	exitCode, err := cursorcli.Run(a.Context, spec, a.Stdin, a.Stdout, a.Stderr, a.Signals)
	if err != nil {
		return a.fail(err)
	}
	return exitCode
}

func providerSecretEnvironmentKeys(cfg config.Config) []string {
	keys := make([]string, 0, len(cfg.Models))
	seen := make(map[string]struct{}, len(cfg.Models))
	for _, model := range cfg.Models {
		if model.APIKeyEnv == "" {
			continue
		}
		if _, duplicate := seen[model.APIKeyEnv]; duplicate {
			continue
		}
		seen[model.APIKeyEnv] = struct{}{}
		keys = append(keys, model.APIKeyEnv)
	}
	return keys
}

func selectedProviderEnvironmentValues(cfg config.Config, selectedModel, resolvedSecret string) map[string]string {
	for _, model := range cfg.Models {
		if model.Name == selectedModel && model.APIKeyEnv != "" {
			return map[string]string{model.APIKeyEnv: resolvedSecret}
		}
	}
	return nil
}
