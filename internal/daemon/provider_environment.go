package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
)

const (
	ProviderEnvironmentPath         = "/cursor-cli-byok.v1.Control/ProviderEnvironment"
	maxProviderEnvironmentBodyBytes = 64 * 1024
	maxProviderSecretBytes          = 16 * 1024
	maxProviderEnvironmentResponse  = 8 * 1024
)

type ProviderEnvironment struct {
	mu        sync.RWMutex
	fallback  func(string) string
	overrides map[string]string
}

type providerEnvironmentRequest struct {
	Values map[string]string `json:"values"`
}

func NewProviderEnvironment(fallback func(string) string) *ProviderEnvironment {
	if fallback == nil {
		fallback = func(string) string { return "" }
	}
	return &ProviderEnvironment{fallback: fallback, overrides: make(map[string]string)}
}

func (environment *ProviderEnvironment) Getenv(name string) string {
	if environment == nil {
		return ""
	}
	environment.mu.RLock()
	value, found := environment.overrides[name]
	environment.mu.RUnlock()
	if found {
		return value
	}
	return environment.fallback(name)
}

func (environment *ProviderEnvironment) Update(values map[string]string) {
	if environment == nil || len(values) == 0 {
		return
	}
	environment.mu.Lock()
	defer environment.mu.Unlock()
	for name, value := range values {
		environment.overrides[name] = value
	}
}

func (environment *ProviderEnvironment) String() string {
	if environment == nil {
		return "ProviderEnvironment{overrides:0}"
	}
	environment.mu.RLock()
	count := len(environment.overrides)
	environment.mu.RUnlock()
	return fmt.Sprintf("ProviderEnvironment{overrides:%d}", count)
}

func (environment *ProviderEnvironment) GoString() string {
	return environment.String()
}

func NewProviderEnvironmentHandler(load func() (config.Config, error), update func(map[string]string)) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			http.Error(writer, "invalid provider environment request", http.StatusBadRequest)
			return
		}
		if load == nil || update == nil {
			http.Error(writer, "provider environment unavailable", http.StatusServiceUnavailable)
			return
		}
		request.Body = http.MaxBytesReader(writer, request.Body, maxProviderEnvironmentBodyBytes)
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var payload providerEnvironmentRequest
		if err := decoder.Decode(&payload); err != nil {
			http.Error(writer, "invalid provider environment request", http.StatusBadRequest)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			http.Error(writer, "invalid provider environment request", http.StatusBadRequest)
			return
		}
		cfg, err := load()
		if err != nil || cfg.Validate() != nil {
			http.Error(writer, "provider environment unavailable", http.StatusServiceUnavailable)
			return
		}
		allowed := make(map[string]struct{}, len(cfg.Models))
		for _, model := range cfg.Models {
			if model.APIKeyEnv != "" {
				allowed[model.APIKeyEnv] = struct{}{}
			}
		}
		if len(payload.Values) == 0 {
			http.Error(writer, "invalid provider environment request", http.StatusBadRequest)
			return
		}
		values := make(map[string]string, len(payload.Values))
		for name, value := range payload.Values {
			if _, ok := allowed[name]; !ok || !validProviderSecret(value) {
				http.Error(writer, "invalid provider environment request", http.StatusBadRequest)
				return
			}
			values[name] = value
		}
		update(values)
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(http.StatusNoContent)
	})
}

func validProviderSecret(value string) bool {
	return value != "" && len(value) <= maxProviderSecretBytes && value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func encodeProviderEnvironment(values map[string]string) ([]byte, error) {
	data, err := json.Marshal(providerEnvironmentRequest{Values: values})
	if err != nil {
		return nil, errors.New("encode provider environment")
	}
	if len(data) > maxProviderEnvironmentBodyBytes {
		return nil, errors.New("encode provider environment: request is too large")
	}
	return bytes.Clone(data), nil
}

func SyncProviderEnvironment(ctx context.Context, state State, values map[string]string, timeout time.Duration) error {
	if ctx == nil {
		return errors.New("sync provider environment: context is required")
	}
	if len(values) == 0 {
		return nil
	}
	data, err := encodeProviderEnvironment(values)
	if err != nil {
		return fmt.Errorf("sync provider environment: %w", err)
	}
	client, closeClient, err := newDaemonHTTPClient(state, timeout)
	if err != nil {
		return fmt.Errorf("sync provider environment: %w", err)
	}
	defer closeClient()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, state.EndpointURL()+ProviderEnvironmentPath, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("sync provider environment: create request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+state.AuthToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Cache-Control", "no-store")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("sync provider environment: request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxProviderEnvironmentResponse+1))
	if err != nil {
		return errors.New("sync provider environment: read response")
	}
	if len(body) > maxProviderEnvironmentResponse {
		return errors.New("sync provider environment: response is too large")
	}
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("sync provider environment: unexpected status %d", response.StatusCode)
	}
	return nil
}
