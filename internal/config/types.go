package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

const (
	CurrentVersion          = 1
	ProtocolOpenAI          = "openai"
	EndpointResponses       = "/v1/responses"
	EndpointChatCompletions = "/v1/chat/completions"
	reservedDaemonLockEnv   = "CURSOR_CLI_BYOK_LOCK_FD"
)

var shellEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

type Config struct {
	Version      int     `yaml:"version"`
	DefaultModel string  `yaml:"default_model"`
	Models       []Model `yaml:"models"`
}

type Model struct {
	Name          string `yaml:"name"`
	Protocol      string `yaml:"protocol"`
	BaseURL       string `yaml:"base_url"`
	Endpoint      string `yaml:"endpoint"`
	APIKey        string `yaml:"api_key,omitempty" json:"-"`
	APIKeyEnv     string `yaml:"api_key_env,omitempty"`
	UpstreamModel string `yaml:"upstream_model"`
}

func (m Model) String() string {
	apiKey := ""
	if m.APIKey != "" {
		apiKey = "[REDACTED]"
	}
	return fmt.Sprintf(
		"Model{Name:%q Protocol:%q BaseURL:%q Endpoint:%q APIKey:%q APIKeyEnv:%q UpstreamModel:%q}",
		m.Name,
		m.Protocol,
		m.BaseURL,
		m.Endpoint,
		apiKey,
		m.APIKeyEnv,
		m.UpstreamModel,
	)
}

func (m Model) GoString() string {
	return m.String()
}

func (c Config) String() string {
	models := make([]string, len(c.Models))
	for index, model := range c.Models {
		models[index] = model.String()
	}
	return fmt.Sprintf(
		"Config{Version:%d DefaultModel:%q Models:[%s]}",
		c.Version,
		c.DefaultModel,
		strings.Join(models, ", "),
	)
}

func (c Config) GoString() string {
	return c.String()
}

type ResolvedModel struct {
	Name          string
	Protocol      string
	BaseURL       string
	Endpoint      string
	APIKey        string `json:"-" yaml:"-"`
	UpstreamModel string
}

func (m ResolvedModel) String() string {
	return fmt.Sprintf(
		"ResolvedModel{Name:%q Protocol:%q BaseURL:%q Endpoint:%q APIKey:%q UpstreamModel:%q}",
		m.Name,
		m.Protocol,
		m.BaseURL,
		m.Endpoint,
		"[REDACTED]",
		m.UpstreamModel,
	)
}

func (m ResolvedModel) GoString() string {
	return m.String()
}

func (c Config) ResolveModel(name string, getenv func(string) string) (ResolvedModel, error) {
	if err := c.Validate(); err != nil {
		return ResolvedModel{}, err
	}

	selectedName := name
	if selectedName == "" {
		selectedName = c.DefaultModel
	}

	for _, model := range c.Models {
		if model.Name != selectedName {
			continue
		}

		secret := model.APIKey
		if model.APIKeyEnv != "" {
			if getenv == nil {
				return ResolvedModel{}, errors.New("resolve model: environment lookup is required")
			}
			secret = getenv(model.APIKeyEnv)
			if secret == "" {
				return ResolvedModel{}, fmt.Errorf("resolve model %q: environment variable %s is unset or empty", model.Name, model.APIKeyEnv)
			}
		}

		return ResolvedModel{
			Name:          model.Name,
			Protocol:      model.Protocol,
			BaseURL:       model.BaseURL,
			Endpoint:      model.Endpoint,
			APIKey:        secret,
			UpstreamModel: model.UpstreamModel,
		}, nil
	}

	return ResolvedModel{}, fmt.Errorf("resolve model: model %q does not exist", selectedName)
}

func (c Config) Validate() error {
	if c.Version != CurrentVersion {
		return errors.New("validate config: unsupported version")
	}
	if c.DefaultModel == "" {
		return errors.New("validate config: default_model is required")
	}
	if hasPadding(c.DefaultModel) {
		return errors.New("validate config: default_model must not contain surrounding whitespace")
	}
	if len(c.Models) == 0 {
		return errors.New("validate config: models is required")
	}
	modelNames := make(map[string]struct{}, len(c.Models))
	for _, model := range c.Models {
		if model.Name == "" {
			return errors.New("validate config: model name is required")
		}
		if hasPadding(model.Name) {
			return errors.New("validate config: model name must not contain surrounding whitespace")
		}
		if hasControlCharacters(model.Name) {
			return errors.New("validate config: model name must not contain control characters")
		}
		if _, exists := modelNames[model.Name]; exists {
			return errors.New("validate config: duplicate model name")
		}
		modelNames[model.Name] = struct{}{}
		if model.Protocol == "" {
			return errors.New("validate config: model protocol is required")
		}
		if hasPadding(model.Protocol) {
			return errors.New("validate config: model protocol must not contain surrounding whitespace")
		}
		if model.Protocol != ProtocolOpenAI {
			return errors.New("validate config: model protocol is unsupported")
		}
		if model.BaseURL == "" {
			return errors.New("validate config: model base_url is required")
		}
		if hasPadding(model.BaseURL) {
			return errors.New("validate config: model base_url must not contain surrounding whitespace")
		}
		baseURL, err := url.Parse(model.BaseURL)
		if err != nil || !baseURL.IsAbs() || baseURL.Host == "" || (baseURL.Scheme != "http" && baseURL.Scheme != "https") {
			return errors.New("validate config: model base_url must be an absolute HTTP or HTTPS URL with a host")
		}
		if baseURL.User != nil || baseURL.RawQuery != "" || baseURL.ForceQuery || baseURL.Fragment != "" {
			return errors.New("validate config: model base_url must not contain userinfo, query, or fragment components")
		}
		if baseURL.Scheme == "http" && !isLoopbackProviderHost(baseURL.Hostname()) {
			return errors.New("validate config: model base_url must use HTTPS unless the host is loopback")
		}
		if model.Endpoint == "" {
			return errors.New("validate config: model endpoint is required")
		}
		if hasPadding(model.Endpoint) {
			return errors.New("validate config: model endpoint must not contain surrounding whitespace")
		}
		if model.Endpoint != EndpointResponses && model.Endpoint != EndpointChatCompletions {
			return errors.New("validate config: model endpoint is unsupported")
		}
		if model.UpstreamModel == "" {
			return errors.New("validate config: model upstream_model is required")
		}
		if hasPadding(model.UpstreamModel) {
			return errors.New("validate config: model upstream_model must not contain surrounding whitespace")
		}
		if hasControlCharacters(model.UpstreamModel) {
			return errors.New("validate config: model upstream_model must not contain control characters")
		}
		if model.APIKeyEnv != "" && hasPadding(model.APIKeyEnv) {
			return errors.New("validate config: model api_key_env must not contain surrounding whitespace")
		}
		inlineKeySet := strings.TrimSpace(model.APIKey) != ""
		environmentKeySet := model.APIKeyEnv != ""
		if inlineKeySet == environmentKeySet {
			return errors.New("validate config: exactly one of api_key or api_key_env is required")
		}
		if model.APIKeyEnv != "" && !shellEnvironmentName.MatchString(model.APIKeyEnv) {
			return errors.New("validate config: model api_key_env is not a valid environment variable name")
		}
		if model.APIKeyEnv == reservedDaemonLockEnv {
			return errors.New("validate config: model api_key_env conflicts with an internal runtime variable")
		}
	}
	if _, exists := modelNames[c.DefaultModel]; !exists {
		return errors.New("validate config: default_model does not exist")
	}
	return nil
}

func isLoopbackProviderHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func hasPadding(value string) bool {
	return value != strings.TrimSpace(value)
}

func hasControlCharacters(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}
