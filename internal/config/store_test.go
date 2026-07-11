package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestConfigValidateAcceptsValidConfiguration(t *testing.T) {
	cfg := validConfig()

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestConfigValidateRejectsDaemonLockEnvironmentAsAPIKeySource(t *testing.T) {
	cfg := validConfig()
	cfg.Models[0].APIKey = ""
	cfg.Models[0].APIKeyEnv = "CURSOR_CLI_BYOK_LOCK_FD"
	requireValidationErrorContains(t, cfg, "api_key_env")
}

func TestConfigValidateRejectsUnsupportedVersion(t *testing.T) {
	for _, version := range []int{0, 2} {
		t.Run(string(rune('0'+version)), func(t *testing.T) {
			cfg := validConfig()
			cfg.Version = version

			requireValidationErrorContains(t, cfg, "version")
		})
	}
}

func TestConfigValidateRejectsMissingRequiredFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{name: "default model", mutate: func(cfg *Config) { cfg.DefaultModel = "" }, field: "default_model"},
		{name: "models", mutate: func(cfg *Config) { cfg.Models = nil }, field: "models"},
		{name: "model name", mutate: func(cfg *Config) { cfg.Models[0].Name = "" }, field: "name"},
		{name: "protocol", mutate: func(cfg *Config) { cfg.Models[0].Protocol = "" }, field: "protocol"},
		{name: "base URL", mutate: func(cfg *Config) { cfg.Models[0].BaseURL = "" }, field: "base_url"},
		{name: "endpoint", mutate: func(cfg *Config) { cfg.Models[0].Endpoint = "" }, field: "endpoint"},
		{name: "upstream model", mutate: func(cfg *Config) { cfg.Models[0].UpstreamModel = "" }, field: "upstream_model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)

			requireValidationErrorContains(t, cfg, tt.field)
		})
	}
}

func TestConfigValidateTreatsWhitespaceOnlyRequiredFieldsAsMissing(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{name: "default model", mutate: func(cfg *Config) { cfg.DefaultModel = "   " }, field: "default_model"},
		{name: "model name", mutate: func(cfg *Config) { cfg.Models[0].Name = "   " }, field: "name"},
		{name: "protocol", mutate: func(cfg *Config) { cfg.Models[0].Protocol = "   " }, field: "protocol"},
		{name: "base URL", mutate: func(cfg *Config) { cfg.Models[0].BaseURL = "   " }, field: "base_url"},
		{name: "endpoint", mutate: func(cfg *Config) { cfg.Models[0].Endpoint = "   " }, field: "endpoint"},
		{name: "upstream model", mutate: func(cfg *Config) { cfg.Models[0].UpstreamModel = "   " }, field: "upstream_model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			tt.mutate(&cfg)

			requireValidationErrorContains(t, cfg, tt.field)
		})
	}
}

func TestConfigValidateRejectsPaddedStrictFields(t *testing.T) {
	const secret = " sk-padded-field-secret "
	tests := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{name: "default model", mutate: func(cfg *Config) { cfg.DefaultModel = " relay-gpt " }, field: "default_model"},
		{name: "model name", mutate: func(cfg *Config) { cfg.Models[0].Name = " relay-gpt " }, field: "name"},
		{name: "protocol", mutate: func(cfg *Config) { cfg.Models[0].Protocol = " openai " }, field: "protocol"},
		{name: "base URL", mutate: func(cfg *Config) { cfg.Models[0].BaseURL = " https://api.example.com " }, field: "base_url"},
		{name: "endpoint", mutate: func(cfg *Config) { cfg.Models[0].Endpoint = " /v1/responses " }, field: "endpoint"},
		{
			name: "API key environment variable",
			mutate: func(cfg *Config) {
				cfg.Models[0].APIKey = ""
				cfg.Models[0].APIKeyEnv = " RELAY_API_KEY "
			},
			field: "api_key_env",
		},
		{name: "upstream model", mutate: func(cfg *Config) { cfg.Models[0].UpstreamModel = " gpt-5.4 " }, field: "upstream_model"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Models[0].APIKey = secret
			cfg.Models[0].APIKeyEnv = ""
			tt.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() error = nil, want %s padding error", tt.field)
			}
			if !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("Validate() error = %q, want %s context", err, tt.field)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("Validate() error leaked inline API key")
			}
		})
	}
}

func TestConfigValidateRejectsUnsupportedProtocol(t *testing.T) {
	cfg := validConfig()
	cfg.Models[0].Protocol = "anthropic"

	requireValidationErrorContains(t, cfg, "protocol")
}

func TestConfigValidateSupportsOnlyKnownEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		valid    bool
	}{
		{name: "responses", endpoint: "/v1/responses", valid: true},
		{name: "chat completions", endpoint: "/v1/chat/completions", valid: true},
		{name: "messages", endpoint: "/v1/messages"},
		{name: "trailing slash", endpoint: "/v1/responses/"},
		{name: "relative", endpoint: "v1/responses"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Models[0].Endpoint = tt.endpoint

			err := cfg.Validate()
			if tt.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !tt.valid && (err == nil || !strings.Contains(err.Error(), "endpoint")) {
				t.Fatalf("Validate() error = %v, want endpoint error", err)
			}
		})
	}
}

func TestConfigValidateRequiresHTTPSOrLoopbackHTTPBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		valid   bool
	}{
		{name: "https", baseURL: "https://api.example.com", valid: true},
		{name: "IPv4 loopback HTTP", baseURL: "http://127.0.0.1:8080/v1", valid: true},
		{name: "IPv6 loopback HTTP", baseURL: "http://[::1]:8080/v1", valid: true},
		{name: "localhost HTTP", baseURL: "http://localhost:8080/v1", valid: true},
		{name: "remote HTTP", baseURL: "http://api.example.com/v1"},
		{name: "private network HTTP", baseURL: "http://192.168.1.20:8080/v1"},
		{name: "lookalike localhost HTTP", baseURL: "http://localhost.example.com/v1"},
		{name: "missing scheme", baseURL: "api.example.com"},
		{name: "relative path", baseURL: "/v1"},
		{name: "unsupported scheme", baseURL: "ftp://api.example.com"},
		{name: "missing host", baseURL: "https:///v1"},
		{name: "invalid URL", baseURL: "https://%"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Models[0].BaseURL = tt.baseURL

			err := cfg.Validate()
			if tt.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !tt.valid && (err == nil || !strings.Contains(err.Error(), "base_url")) {
				t.Fatalf("Validate() error = %v, want base_url error", err)
			}
		})
	}
}

func TestConfigValidateRejectsSensitiveBaseURLComponents(t *testing.T) {
	tests := []string{
		"https://user:password@api.example.com",
		"https://api.example.com?api_key=secret",
		"https://api.example.com#secret",
	}
	for _, baseURL := range tests {
		t.Run(baseURL, func(t *testing.T) {
			cfg := validConfig()
			cfg.Models[0].BaseURL = baseURL
			requireValidationErrorContains(t, cfg, "base_url")
		})
	}
}

func TestConfigValidateRejectsControlCharactersInDisplayedFields(t *testing.T) {
	t.Run("model name", func(t *testing.T) {
		cfg := validConfig()
		cfg.DefaultModel = "relay\x1b-gpt"
		cfg.Models[0].Name = "relay\x1b-gpt"
		requireValidationErrorContains(t, cfg, "name")
	})

	t.Run("upstream model", func(t *testing.T) {
		cfg := validConfig()
		cfg.Models[0].UpstreamModel = "gpt\t5.4"
		requireValidationErrorContains(t, cfg, "upstream_model")
	})
}

func TestConfigValidateRejectsDuplicateModelNames(t *testing.T) {
	cfg := validConfig()
	cfg.Models = append(cfg.Models, cfg.Models[0])
	cfg.Models[1].UpstreamModel = "gpt-5-mini"

	requireValidationErrorContains(t, cfg, "duplicate")
}

func TestConfigValidateRequiresExistingDefaultModel(t *testing.T) {
	cfg := validConfig()
	cfg.DefaultModel = "missing-model"

	requireValidationErrorContains(t, cfg, "default_model")
}

func TestConfigValidateRequiresExactlyOneKeySource(t *testing.T) {
	const secret = "sk-super-secret"
	tests := []struct {
		name      string
		apiKey    string
		apiKeyEnv string
		valid     bool
	}{
		{name: "environment key", apiKeyEnv: "RELAY_API_KEY", valid: true},
		{name: "inline key", apiKey: secret, valid: true},
		{name: "no key"},
		{name: "blank inline key", apiKey: "   "},
		{name: "both keys", apiKey: secret, apiKeyEnv: "RELAY_API_KEY"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Models[0].APIKey = tt.apiKey
			cfg.Models[0].APIKeyEnv = tt.apiKeyEnv

			err := cfg.Validate()
			if tt.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !tt.valid && (err == nil || !strings.Contains(err.Error(), "api_key")) {
				t.Fatalf("Validate() error = %v, want api_key error", err)
			}
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Fatalf("Validate() error leaked API key: %q", err)
			}
		})
	}
}

func TestConfigValidateRequiresShellEnvironmentVariableName(t *testing.T) {
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "uppercase", value: "RELAY_API_KEY", valid: true},
		{name: "underscore prefix", value: "_private_key_2", valid: true},
		{name: "numeric prefix", value: "2_API_KEY"},
		{name: "hyphen", value: "API-KEY"},
		{name: "dollar prefix", value: "$API_KEY"},
		{name: "assignment", value: "API_KEY=value"},
		{name: "space", value: "API KEY"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.Models[0].APIKeyEnv = tt.value

			err := cfg.Validate()
			if tt.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !tt.valid && (err == nil || !strings.Contains(err.Error(), "api_key_env")) {
				t.Fatalf("Validate() error = %v, want api_key_env error", err)
			}
		})
	}
}

func TestConfigResolveModelResolvesDefaultAndNamedSecrets(t *testing.T) {
	cfg := validConfig()
	cfg.Models = append(cfg.Models, Model{
		Name:          "relay-chat",
		Protocol:      "openai",
		BaseURL:       "https://chat.example.com",
		Endpoint:      "/v1/chat/completions",
		APIKey:        " inline-secret ",
		UpstreamModel: "gpt-5-mini",
	})

	tests := []struct {
		name        string
		modelName   string
		environment map[string]string
		wantName    string
		wantSecret  string
	}{
		{
			name:        "default model from environment",
			environment: map[string]string{"RELAY_API_KEY": "environment-secret"},
			wantName:    "relay-gpt",
			wantSecret:  "environment-secret",
		},
		{
			name:       "named model with inline key",
			modelName:  "relay-chat",
			wantName:   "relay-chat",
			wantSecret: " inline-secret ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cfg.ResolveModel(tt.modelName, envLookup(tt.environment))
			if err != nil {
				t.Fatalf("ResolveModel() error = %v", err)
			}
			if got.Name != tt.wantName || got.APIKey != tt.wantSecret {
				t.Fatalf("ResolveModel() name = %q, secret matched = %t", got.Name, got.APIKey == tt.wantSecret)
			}
		})
	}
}

func TestConfigResolveModelRejectsPaddedName(t *testing.T) {
	const secret = "environment-secret"

	_, err := validConfig().ResolveModel(" relay-gpt ", envLookup(map[string]string{
		"RELAY_API_KEY": secret,
	}))
	if err == nil {
		t.Fatal("ResolveModel() error = nil, want padded model name error")
	}
	if !strings.Contains(err.Error(), "model") {
		t.Fatalf("ResolveModel() error = %q, want model context", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("ResolveModel() error leaked API key")
	}
}

func TestResolvedModelStringOutputRedactsAPIKey(t *testing.T) {
	const secret = "sk-string-output-secret"
	resolved := ResolvedModel{
		Name:          "relay-gpt",
		Protocol:      "openai",
		BaseURL:       "https://api.example.com",
		Endpoint:      "/v1/responses",
		APIKey:        secret,
		UpstreamModel: "gpt-5.4",
	}

	output := fmt.Sprintf("%v\n%+v\n%#v", resolved, resolved, resolved)
	if strings.Contains(output, secret) {
		t.Fatalf("formatted ResolvedModel leaked API key: %q", output)
	}
	if !strings.Contains(output, "REDACTED") {
		t.Fatalf("formatted ResolvedModel = %q, want redaction marker", output)
	}
}

func TestConfigAndModelStringOutputRedactsAPIKey(t *testing.T) {
	const secret = "sk-config-string-secret"
	cfg := validConfig()
	cfg.Models[0].APIKeyEnv = ""
	cfg.Models[0].APIKey = secret

	output := fmt.Sprintf("%v\n%+v\n%#v\n%v\n%+v\n%#v", cfg, cfg, cfg, cfg.Models[0], cfg.Models[0], cfg.Models[0])
	if strings.Contains(output, secret) {
		t.Fatalf("formatted configuration leaked API key: %q", output)
	}
	if !strings.Contains(output, "REDACTED") {
		t.Fatalf("formatted configuration = %q, want redaction marker", output)
	}
}

func TestModelJSONOutputOmitsInlineAPIKey(t *testing.T) {
	const secret = "sk-json-secret"
	model := validConfig().Models[0]
	model.APIKeyEnv = ""
	model.APIKey = secret

	data, err := json.Marshal(model)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatalf("json.Marshal() leaked inline API key: %s", data)
	}
}

func TestStoreSaveAndLoadPreservesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	store := NewStore(path)
	cfg := validConfig()
	cfg.Models[0].APIKey = " inline-secret "
	cfg.Models[0].APIKeyEnv = ""

	if err := store.Save(cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Fatalf("Load() = %v, want %v", got, cfg)
	}
}

func TestStoreRejectsPaddedStrictFields(t *testing.T) {
	const secret = "sk-store-padding-secret"

	t.Run("save", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config", "config.yaml")
		cfg := validConfig()
		cfg.Models[0].Protocol = " openai "
		cfg.Models[0].APIKey = secret
		cfg.Models[0].APIKeyEnv = ""

		err := NewStore(path).Save(cfg)
		if err == nil {
			t.Fatal("Save() error = nil, want padded protocol error")
		}
		if !strings.Contains(err.Error(), "protocol") {
			t.Fatalf("Save() error = %q, want protocol context", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatal("Save() error leaked inline API key")
		}
	})

	t.Run("load", func(t *testing.T) {
		directory := filepath.Join(t.TempDir(), "config")
		path := filepath.Join(directory, "config.yaml")
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		data := []byte(`version: 1
default_model: relay-gpt
models:
  - name: relay-gpt
    protocol: " openai "
    base_url: https://api.example.com
    endpoint: /v1/responses
    api_key: ` + secret + `
    upstream_model: gpt-5.4
`)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		_, err := NewStore(path).Load()
		if err == nil {
			t.Fatal("Load() error = nil, want padded protocol error")
		}
		if !strings.Contains(err.Error(), "protocol") {
			t.Fatalf("Load() error = %q, want protocol context", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatal("Load() error leaked inline API key")
		}
	})
}

func TestStoreSaveCorrectsBroadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are required")
	}

	directory := filepath.Join(t.TempDir(), "config")
	path := filepath.Join(directory, "config.yaml")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := NewStore(path).Save(validConfig()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	assertMode(t, directory, 0o700)
	assertMode(t, path, 0o600)
}

func TestStoreLoadCorrectsBroadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are required")
	}

	directory := filepath.Join(t.TempDir(), "config")
	path := filepath.Join(directory, "config.yaml")
	store := NewStore(path)
	if err := store.Save(validConfig()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatalf("Chmod(directory) error = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod(file) error = %v", err)
	}

	if _, err := store.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	assertMode(t, directory, 0o700)
	assertMode(t, path, 0o600)
}

func TestStoreRejectsRelativePathBeforeFilesystemAccess(t *testing.T) {
	relativePath := filepath.Join("missing-relative-directory", "config.yaml")

	_, err := NewStore(relativePath).Load()
	if err == nil {
		t.Fatal("Load() error = nil, want relative path error")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("Load() error = %q, want absolute path context", err)
	}
}

func TestStoreLoadRejectsSymlinkWithoutChangingTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink and Unix permission behavior is verified on Unix")
	}

	directory := filepath.Join(t.TempDir(), "config")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	target := filepath.Join(directory, "target.yaml")
	if err := os.WriteFile(target, []byte("not a config"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	path := filepath.Join(directory, "config.yaml")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	_, err := NewStore(path).Load()
	if err == nil {
		t.Fatal("Load() error = nil, want symlink rejection")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("Load() error = %q, want symlink context", err)
	}
	assertMode(t, target, 0o644)
}

func TestStoreLoadRejectsUnknownYAMLFields(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "config")
	path := filepath.Join(directory, "config.yaml")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	data := []byte(`version: 1
default_model: relay-gpt
unexpected: true
models:
  - name: relay-gpt
    protocol: openai
    base_url: https://api.example.com
    endpoint: /v1/responses
    api_key_env: RELAY_API_KEY
    upstream_model: gpt-5.4
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := NewStore(path).Load()
	if err == nil {
		t.Fatal("Load() error = nil, want unknown YAML field error")
	}
	if !strings.Contains(err.Error(), "decode YAML") {
		t.Fatalf("Load() error = %q, want decode context", err)
	}
}

func TestStoreSaveReplacesExistingFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("same-directory replacement semantics are verified on Unix")
	}

	directory := filepath.Join(t.TempDir(), "config")
	path := filepath.Join(directory, "config.yaml")
	store := NewStore(path)
	if err := store.Save(validConfig()); err != nil {
		t.Fatalf("first Save() error = %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(before) error = %v", err)
	}

	updated := validConfig()
	updated.Models[0].UpstreamModel = "gpt-5-mini"
	if err := store.Save(updated); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(after) error = %v", err)
	}
	if os.SameFile(before, after) {
		t.Fatal("Save() updated the existing config file in place, want atomic replacement")
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.yaml" {
		t.Fatalf("directory entries = %v, want only config.yaml", entries)
	}
}

func TestStoreLoadErrorsDoNotLeakInlineAPIKeys(t *testing.T) {
	const secret = "sk-load-error-secret"
	directory := filepath.Join(t.TempDir(), "config")
	path := filepath.Join(directory, "config.yaml")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	data := []byte(`version: 1
default_model: relay-gpt
models:
  - name: relay-gpt
    protocol: openai
    base_url: https://api.example.com
    endpoint: /v1/responses
    api_key: !!int ` + secret + `
    upstream_model: gpt-5.4
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := NewStore(path).Load()
	if err == nil {
		t.Fatal("Load() error = nil, want invalid YAML error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("Load() error leaked inline API key")
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

func requireValidationErrorContains(t *testing.T, cfg Config, want string) {
	t.Helper()
	err := cfg.Validate()
	if err == nil {
		t.Fatalf("Validate() error = nil, want error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Validate() error = %q, want substring %q", err, want)
	}
}

func envLookup(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

func validConfig() Config {
	return Config{
		Version:      1,
		DefaultModel: "relay-gpt",
		Models: []Model{
			{
				Name:          "relay-gpt",
				Protocol:      "openai",
				BaseURL:       "https://api.example.com",
				Endpoint:      "/v1/responses",
				APIKeyEnv:     "RELAY_API_KEY",
				UpstreamModel: "gpt-5.4",
			},
		},
	}
}
