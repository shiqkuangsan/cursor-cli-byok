package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/agent"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/certs"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/paths"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/provider"
	openaiadapter "github.com/shiqkuangsan/cursor-cli-byok/internal/provider/openai"
	localserver "github.com/shiqkuangsan/cursor-cli-byok/internal/server"
	localtools "github.com/shiqkuangsan/cursor-cli-byok/internal/tools"
)

type ServiceOptions struct {
	Paths         paths.Paths
	Version       string
	Lock          *FileLock
	ListenAddress string
	IdleTimeout   time.Duration
	Handler       http.Handler
}

func StatePath(runtimePaths paths.Paths) string {
	return filepath.Join(runtimePaths.StateDir, "daemon.json")
}

func LockPath(runtimePaths paths.Paths) string {
	return filepath.Join(runtimePaths.StateDir, "daemon.lock")
}

func RunService(ctx context.Context, options ServiceOptions) (runError error) {
	if ctx == nil {
		return errors.New("run daemon service: context is required")
	}
	if err := validateServiceOptions(options); err != nil {
		return err
	}
	stateStore := NewStateStore(StatePath(options.Paths))
	defer func() {
		removeError := stateStore.Remove()
		lockError := options.Lock.Close()
		runError = errors.Join(runError, removeError, lockError)
	}()
	configStore := config.NewStore(options.Paths.ConfigFile)
	if _, err := configStore.Load(); err != nil {
		return fmt.Errorf("run daemon service: %w", err)
	}
	bundle, err := (certs.Manager{Directory: filepath.Join(options.Paths.DataDir, "certs")}).Ensure()
	if err != nil {
		return fmt.Errorf("run daemon service: %w", err)
	}
	instanceID, err := newInstanceID()
	if err != nil {
		return fmt.Errorf("run daemon service: create instance ID: %w", err)
	}
	authToken, err := newAuthToken()
	if err != nil {
		return fmt.Errorf("run daemon service: create auth token: %w", err)
	}
	idleTimeout := options.IdleTimeout
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	idleMonitor, err := NewIdleMonitor(idleTimeout)
	if err != nil {
		return fmt.Errorf("run daemon service: %w", err)
	}
	serviceContext, cancelService := context.WithCancel(ctx)
	defer cancelService()
	providerEnvironment := NewProviderEnvironment(os.Getenv)
	applicationHandler := options.Handler
	if applicationHandler == nil {
		toolCatalog, err := localtools.Select("Read", "Write", "Edit", "Delete", "List", "Glob", "Grep", "Shell")
		if err != nil {
			return fmt.Errorf("run daemon service: create tool catalog: %w", err)
		}
		runner, err := agent.NewRunner(agent.RunnerOptions{
			Tools: toolCatalog,
			ResolveModel: func(alias string) (config.ResolvedModel, error) {
				cfg, err := configStore.Load()
				if err != nil {
					return config.ResolvedModel{}, err
				}
				return cfg.ResolveModel(alias, providerEnvironment.Getenv)
			},
			NewStreamer: func(model config.ResolvedModel) (provider.Streamer, error) {
				return openaiadapter.NewClient(openaiadapter.Options{
					BaseURL:  model.BaseURL,
					Endpoint: model.Endpoint,
					APIKey:   model.APIKey,
					Headers:  model.Headers,
				})
			},
		})
		if err != nil {
			return fmt.Errorf("run daemon service: create agent runner: %w", err)
		}
		applicationHandler = localserver.NewApplicationHandler(configStore.Load, runner, localserver.AgentHandlerOptions{Context: serviceContext})
	}
	providerEnvironmentHandler := NewProviderEnvironmentHandler(configStore.Load, providerEnvironment.Update)
	shutdownHandler := NewShutdownHandler(cancelService)
	coreHandler := applicationHandler
	applicationHandler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case ProviderEnvironmentPath:
			providerEnvironmentHandler.ServeHTTP(writer, request)
		case ShutdownPath:
			shutdownHandler.ServeHTTP(writer, request)
		default:
			coreHandler.ServeHTTP(writer, request)
		}
	})
	trackedHandler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		endActivity := idleMonitor.Begin()
		defer endActivity()
		applicationHandler.ServeHTTP(writer, request)
	})
	runningServer, err := localserver.Start(serviceContext, localserver.Options{
		ListenAddress: options.ListenAddress,
		Certificate:   bundle.Certificate,
		InstanceID:    instanceID,
		AuthToken:     authToken,
		DaemonVersion: options.Version,
		Handler:       trackedHandler,
	})
	if err != nil {
		return fmt.Errorf("run daemon service: %w", err)
	}
	port, err := endpointPort(runningServer.EndpointURL())
	if err != nil {
		_ = shutdownServiceServer(runningServer)
		return fmt.Errorf("run daemon service: %w", err)
	}
	state := State{
		Version:       CurrentStateVersion,
		PID:           os.Getpid(),
		Port:          port,
		CACertPath:    bundle.CACertPath,
		DaemonVersion: options.Version,
		InstanceID:    instanceID,
		AuthToken:     authToken,
		StartedAt:     time.Now().UTC(),
	}
	if err := stateStore.Save(state); err != nil {
		_ = shutdownServiceServer(runningServer)
		return fmt.Errorf("run daemon service: publish state: %w", err)
	}

	serverResult := make(chan error, 1)
	idleResult := make(chan error, 1)
	go func() { serverResult <- runningServer.Wait() }()
	go func() { idleResult <- idleMonitor.Wait(serviceContext) }()
	select {
	case err := <-serverResult:
		cancelService()
		if err != nil {
			return fmt.Errorf("run daemon service: server stopped: %w", err)
		}
		return nil
	case err := <-idleResult:
		if err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("run daemon service: idle monitor: %w", err)
		}
		cancelService()
		if err := shutdownServiceServer(runningServer); err != nil {
			return err
		}
		return <-serverResult
	case <-ctx.Done():
		cancelService()
		if err := shutdownServiceServer(runningServer); err != nil {
			return err
		}
		serverError := <-serverResult
		if serverError != nil {
			return serverError
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil
		}
		return ctx.Err()
	}
}

func validateServiceOptions(options ServiceOptions) error {
	for name, path := range map[string]string{
		"config file": options.Paths.ConfigFile,
		"data dir":    options.Paths.DataDir,
		"state dir":   options.Paths.StateDir,
	} {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("run daemon service: %s must be absolute", name)
		}
	}
	if options.Version == "" {
		return errors.New("run daemon service: version is required")
	}
	if options.Lock == nil || options.Lock.File() == nil {
		return errors.New("run daemon service: owned lock is required")
	}
	return nil
}

func newInstanceID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func newAuthToken() (string, error) {
	signature := make([]byte, 32)
	if _, err := rand.Read(signature); err != nil {
		return "", err
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"cursor-cli-byok","email":"byok@localhost","type":"session","iss":"cursor-cli-byok","exp":4102444800}`))
	return header + "." + payload + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func endpointPort(endpoint string) (int, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return 0, errors.New("server returned an invalid endpoint")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return 0, errors.New("server returned an invalid endpoint port")
	}
	return port, nil
}

func shutdownServiceServer(server *localserver.Server) error {
	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		return fmt.Errorf("run daemon service: shutdown server: %w", err)
	}
	return nil
}
