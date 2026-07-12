package command

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
)

func TestConfigInitNonInteractiveCreatesConfiguration(t *testing.T) {
	home := t.TempDir()
	const secret = "sk-init-secret"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := App{
		Stdin:  strings.NewReader(""),
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: commandEnv(map[string]string{"HOME": home}),
	}

	exitCode := app.Run([]string{
		"config", "init", "--non-interactive",
		"--name", "relay-gpt",
		"--base-url", "https://api.example.com",
		"--endpoint", config.EndpointResponses,
		"--upstream-model", "gpt-5.4",
		"--api-key", secret,
		"--header", "User-Agent: codex_cli_rs/0.144.1 (Linux; aarch64)",
		"--header", "OpenAI-Beta: responses=experimental",
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	path := filepath.Join(home, ".config", "cursor-cli-byok", "config.yaml")
	got, err := config.NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: "relay-gpt",
		Models: []config.Model{{
			Name:     "relay-gpt",
			Protocol: config.ProtocolOpenAI,
			BaseURL:  "https://api.example.com",
			Endpoint: config.EndpointResponses,
			APIKey:   secret,
			Headers: map[string]string{
				"OpenAI-Beta": "responses=experimental",
				"User-Agent":  "codex_cli_rs/0.144.1 (Linux; aarch64)",
			},
			UpstreamModel: "gpt-5.4",
		}},
	}
	if got.String() != want.String() || got.Models[0].APIKey != secret {
		t.Fatalf("saved config = %v, want %v", got, want)
	}
	if strings.Contains(stdout.String()+stderr.String(), secret) {
		t.Fatal("command output leaked inline API key")
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatalf("Stat() error = %v", err)
	} else if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("config mode = %04o, want 0600", gotMode)
	}
}

func TestConfigInitNonInteractiveAppliesReleaseDefaults(t *testing.T) {
	home := t.TempDir()
	const secret = "sk-standard-environment-secret"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := App{
		Stdin:  strings.NewReader(""),
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: commandEnv(map[string]string{
			"HOME":           home,
			"OPENAI_API_KEY": secret,
		}),
	}

	exitCode := app.Run([]string{
		"config", "init", "--non-interactive",
		"--base-url", "https://api.example.com",
		"--upstream-model", "gpt-5.4",
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	path := filepath.Join(home, ".config", "cursor-cli-byok", "config.yaml")
	got, err := config.NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.DefaultModel != "gpt-5.4" || len(got.Models) != 1 {
		t.Fatalf("saved config = %v, want one default gpt-5.4 model", got)
	}
	model := got.Models[0]
	if model.Name != "gpt-5.4" || model.Endpoint != config.EndpointResponses || model.APIKeyEnv != "OPENAI_API_KEY" || model.APIKey != "" {
		t.Fatalf("saved model = %v, want release defaults", model)
	}
	if strings.Contains(stdout.String()+stderr.String(), secret) {
		t.Fatal("command output leaked standard environment API key")
	}
}

func TestConfigInitNonInteractiveExplicitValuesOverrideReleaseDefaults(t *testing.T) {
	home := t.TempDir()
	var stderr bytes.Buffer
	app := App{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Getenv: commandEnv(map[string]string{
			"HOME":           home,
			"OPENAI_API_KEY": "standard-secret",
		}),
	}

	exitCode := app.Run([]string{
		"config", "init", "--non-interactive",
		"--name", "custom-chat",
		"--base-url", "https://chat.example.com",
		"--endpoint", config.EndpointChatCompletions,
		"--upstream-model", "gpt-5-mini",
		"--api-key-env", "CUSTOM_API_KEY",
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	path := filepath.Join(home, ".config", "cursor-cli-byok", "config.yaml")
	got, err := config.NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	model := got.Models[0]
	if model.Name != "custom-chat" || model.Endpoint != config.EndpointChatCompletions || model.APIKeyEnv != "CUSTOM_API_KEY" {
		t.Fatalf("saved model = %v, want explicit values", model)
	}
}

func TestConfigInitNonInteractiveRequiresKeySourceWithoutStandardEnvironment(t *testing.T) {
	home := t.TempDir()
	var stderr bytes.Buffer
	app := App{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Getenv: commandEnv(map[string]string{"HOME": home}),
	}

	exitCode := app.Run([]string{
		"config", "init", "--non-interactive",
		"--base-url", "https://api.example.com",
		"--upstream-model", "gpt-5.4",
	})

	if exitCode == 0 {
		t.Fatal("Run() exit code = 0, want missing key source failure")
	}
	if !strings.Contains(stderr.String(), "api_key") {
		t.Fatalf("stderr = %q, want API key source context", stderr.String())
	}
	path := filepath.Join(home, ".config", "cursor-cli-byok", "config.yaml")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Stat(config) error = %v, want no configuration", err)
	}
}

func TestConfigHelpListsSubcommandsAndAutomationFlags(t *testing.T) {
	var stdout bytes.Buffer
	app := App{Stdout: &stdout}

	exitCode := app.Run([]string{"config", "--help"})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0", exitCode)
	}
	for _, want := range []string{
		"config init",
		"config add",
		"config list",
		"config use",
		"config remove",
		"--non-interactive",
		"--api-key-env",
		"--header",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestConfigInitInteractivePromptsDeterministically(t *testing.T) {
	home := t.TempDir()
	input := strings.Join([]string{
		"https://interactive.example.com",
		"gpt-5-mini",
		"",
		"env",
		"INTERACTIVE_API_KEY",
	}, "\n") + "\n"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := App{
		Stdin:  strings.NewReader(input),
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: commandEnv(map[string]string{"HOME": home}),
	}

	exitCode := app.Run([]string{"config", "init", "--endpoint", config.EndpointChatCompletions})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	for _, prompt := range []string{
		"Base URL: ",
		"Upstream model: ",
		"Model name [gpt-5-mini]: ",
		"API key source (env/inline): ",
		"API key environment variable: ",
	} {
		if !strings.Contains(stdout.String(), prompt) {
			t.Fatalf("stdout = %q, want prompt %q", stdout.String(), prompt)
		}
	}
	path := filepath.Join(home, ".config", "cursor-cli-byok", "config.yaml")
	got, err := config.NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.DefaultModel != "gpt-5-mini" || got.Models[0].Endpoint != config.EndpointChatCompletions || got.Models[0].APIKeyEnv != "INTERACTIVE_API_KEY" {
		t.Fatalf("saved config = %v, want interactive Chat Completions model", got)
	}
}

func TestConfigInitChecksForExistingConfigurationBeforePrompting(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "cursor-cli-byok", "config.yaml")
	if err := config.NewStore(path).Save(twoModelConfig()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	stdin := &trackingReader{}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := App{
		Stdin:  stdin,
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: commandEnv(map[string]string{"HOME": home}),
	}

	exitCode := app.Run([]string{"config", "init"})

	if exitCode == 0 {
		t.Fatal("Run() exit code = 0, want existing configuration failure")
	}
	if stdin.reads != 0 {
		t.Fatalf("stdin reads = %d, want 0", stdin.reads)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no prompts", stdout.String())
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Fatalf("stderr = %q, want existing configuration context", stderr.String())
	}
}

func TestConfigAddNonInteractivePreservesDefaultModel(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "cursor-cli-byok", "config.yaml")
	initial := config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: "relay-gpt",
		Models: []config.Model{{
			Name:          "relay-gpt",
			Protocol:      config.ProtocolOpenAI,
			BaseURL:       "https://responses.example.com",
			Endpoint:      config.EndpointResponses,
			APIKeyEnv:     "RESPONSES_API_KEY",
			UpstreamModel: "gpt-5.4",
		}},
	}
	if err := config.NewStore(path).Save(initial); err != nil {
		t.Fatalf("Save(initial) error = %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := App{
		Stdin:  strings.NewReader(""),
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: commandEnv(map[string]string{"HOME": home}),
	}
	exitCode := app.Run([]string{
		"config", "add", "--non-interactive",
		"--name", "relay-chat",
		"--base-url", "https://chat.example.com",
		"--endpoint", config.EndpointChatCompletions,
		"--upstream-model", "gpt-5-mini",
		"--api-key-env", "CHAT_API_KEY",
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	got, err := config.NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.DefaultModel != initial.DefaultModel {
		t.Fatalf("default model = %q, want %q", got.DefaultModel, initial.DefaultModel)
	}
	if len(got.Models) != 2 {
		t.Fatalf("models count = %d, want 2", len(got.Models))
	}
	if added := got.Models[1]; added.Name != "relay-chat" || added.Endpoint != config.EndpointChatCompletions || added.APIKeyEnv != "CHAT_API_KEY" {
		t.Fatalf("added model = %v, want relay-chat Chat Completions model", added)
	}
}

func TestConfigAddNonInteractiveAppliesReleaseDefaults(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "cursor-cli-byok", "config.yaml")
	initial := config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: "existing",
		Models: []config.Model{{
			Name:          "existing",
			Protocol:      config.ProtocolOpenAI,
			BaseURL:       "https://existing.example.com",
			Endpoint:      config.EndpointResponses,
			APIKeyEnv:     "EXISTING_API_KEY",
			UpstreamModel: "existing-model",
		}},
	}
	if err := config.NewStore(path).Save(initial); err != nil {
		t.Fatalf("Save(initial) error = %v", err)
	}

	var stderr bytes.Buffer
	app := App{
		Stdin:  strings.NewReader(""),
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		Getenv: commandEnv(map[string]string{
			"HOME":           home,
			"OPENAI_API_KEY": "standard-secret",
		}),
	}
	exitCode := app.Run([]string{
		"config", "add", "--non-interactive",
		"--base-url", "https://new.example.com",
		"--upstream-model", "gpt-5.4",
	})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	got, err := config.NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.DefaultModel != initial.DefaultModel || len(got.Models) != 2 {
		t.Fatalf("saved config = %v, want existing default plus one model", got)
	}
	added := got.Models[1]
	if added.Name != "gpt-5.4" || added.Endpoint != config.EndpointResponses || added.APIKeyEnv != "OPENAI_API_KEY" {
		t.Fatalf("added model = %v, want release defaults", added)
	}
}

func TestConfigAddChecksForConfigurationBeforePrompting(t *testing.T) {
	home := t.TempDir()
	stdin := &trackingReader{}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := App{
		Stdin:  stdin,
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: commandEnv(map[string]string{"HOME": home}),
	}

	exitCode := app.Run([]string{"config", "add"})

	if exitCode == 0 {
		t.Fatal("Run() exit code = 0, want missing configuration failure")
	}
	if stdin.reads != 0 {
		t.Fatalf("stdin reads = %d, want 0", stdin.reads)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no prompts", stdout.String())
	}
	if !strings.Contains(stderr.String(), "config add") {
		t.Fatalf("stderr = %q, want config add context", stderr.String())
	}
}

func TestConfigListRedactsKeysAndReportsEnvironmentStatus(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "cursor-cli-byok", "config.yaml")
	const inlineSecret = "sk-list-inline-secret"
	const environmentSecret = "sk-list-environment-secret"
	cfg := config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: "responses",
		Models: []config.Model{
			{
				Name:          "responses",
				Protocol:      config.ProtocolOpenAI,
				BaseURL:       "https://responses.example.com",
				Endpoint:      config.EndpointResponses,
				APIKeyEnv:     "RESPONSES_API_KEY",
				UpstreamModel: "gpt-5.4",
			},
			{
				Name:          "chat-inline",
				Protocol:      config.ProtocolOpenAI,
				BaseURL:       "https://chat.example.com",
				Endpoint:      config.EndpointChatCompletions,
				APIKey:        inlineSecret,
				UpstreamModel: "gpt-5-mini",
			},
			{
				Name:          "chat-unset",
				Protocol:      config.ProtocolOpenAI,
				BaseURL:       "https://unset.example.com",
				Endpoint:      config.EndpointChatCompletions,
				APIKeyEnv:     "UNSET_API_KEY",
				UpstreamModel: "gpt-5-nano",
			},
		},
	}
	if err := config.NewStore(path).Save(cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := App{
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: commandEnv(map[string]string{
			"HOME":              home,
			"RESPONSES_API_KEY": environmentSecret,
		}),
	}
	exitCode := app.Run([]string{"config", "list"})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	output := stdout.String() + stderr.String()
	for _, want := range []string{
		"* responses",
		"chat-inline",
		"chat-unset",
		"env:RESPONSES_API_KEY (set)",
		"env:UNSET_API_KEY (unset)",
		"inline:[REDACTED]",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output = %q, want substring %q", output, want)
		}
	}
	for _, secret := range []string{inlineSecret, environmentSecret} {
		if strings.Contains(output, secret) {
			t.Fatalf("output leaked API key %q", secret)
		}
	}
}

func TestConfigUseChangesDefaultModel(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "cursor-cli-byok", "config.yaml")
	cfg := twoModelConfig()
	if err := config.NewStore(path).Save(cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := App{
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: commandEnv(map[string]string{"HOME": home}),
	}
	exitCode := app.Run([]string{"config", "use", "relay-chat"})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	got, err := config.NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.DefaultModel != "relay-chat" {
		t.Fatalf("default model = %q, want relay-chat", got.DefaultModel)
	}
	if !strings.Contains(stdout.String(), "relay-chat") {
		t.Fatalf("stdout = %q, want selected model", stdout.String())
	}
}

func TestConfigRemoveDeletesOnlyNonDefaultModel(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "cursor-cli-byok", "config.yaml")
	if err := config.NewStore(path).Save(twoModelConfig()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	app := App{
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: commandEnv(map[string]string{"HOME": home}),
	}
	exitCode := app.Run([]string{"config", "remove", "relay-chat"})

	if exitCode != 0 {
		t.Fatalf("Run() exit code = %d, want 0; stderr = %q", exitCode, stderr.String())
	}
	got, err := config.NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got.Models) != 1 || got.Models[0].Name != "relay-gpt" {
		t.Fatalf("models = %v, want only relay-gpt", got.Models)
	}
	if !strings.Contains(stdout.String(), "relay-chat") {
		t.Fatalf("stdout = %q, want removed model", stdout.String())
	}
}

func TestConfigRemoveRefusesDefaultModel(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".config", "cursor-cli-byok", "config.yaml")
	want := twoModelConfig()
	if err := config.NewStore(path).Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	var stderr bytes.Buffer
	app := App{
		Stderr: &stderr,
		Getenv: commandEnv(map[string]string{"HOME": home}),
	}
	exitCode := app.Run([]string{"config", "remove", "relay-gpt"})

	if exitCode == 0 {
		t.Fatal("Run() exit code = 0, want failure for default model")
	}
	if !strings.Contains(stderr.String(), "default") || !strings.Contains(stderr.String(), "config use") {
		t.Fatalf("stderr = %q, want guidance to change default first", stderr.String())
	}
	got, err := config.NewStore(path).Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.String() != want.String() {
		t.Fatalf("config changed after rejected removal: got %v, want %v", got, want)
	}
}

func commandEnv(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}

type trackingReader struct {
	reads int
}

func (r *trackingReader) Read([]byte) (int, error) {
	r.reads++
	return 0, errors.New("unexpected stdin read")
}

func twoModelConfig() config.Config {
	return config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: "relay-gpt",
		Models: []config.Model{
			{
				Name:          "relay-gpt",
				Protocol:      config.ProtocolOpenAI,
				BaseURL:       "https://responses.example.com",
				Endpoint:      config.EndpointResponses,
				APIKeyEnv:     "RESPONSES_API_KEY",
				UpstreamModel: "gpt-5.4",
			},
			{
				Name:          "relay-chat",
				Protocol:      config.ProtocolOpenAI,
				BaseURL:       "https://chat.example.com",
				Endpoint:      config.EndpointChatCompletions,
				APIKeyEnv:     "CHAT_API_KEY",
				UpstreamModel: "gpt-5-mini",
			},
		},
	}
}
