package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/provider"
)

const (
	defaultMaxEventBytes = 1024 * 1024
	maximumMaxEventBytes = 16 * 1024 * 1024
)

type Options struct {
	BaseURL       string
	Endpoint      string
	APIKey        string
	Headers       map[string]string
	HTTPClient    *http.Client
	MaxEventBytes int
}

type Client struct {
	endpointURL   string
	endpoint      string
	apiKey        string
	headers       http.Header
	httpClient    *http.Client
	maxEventBytes int
}

func NewClient(options Options) (*Client, error) {
	endpointURL, err := buildEndpointURL(options.BaseURL, options.Endpoint)
	if err != nil {
		return nil, err
	}
	if options.APIKey == "" || options.APIKey != strings.TrimSpace(options.APIKey) || strings.IndexFunc(options.APIKey, unicode.IsControl) >= 0 {
		return nil, errors.New("create OpenAI client: API key is required and must be valid")
	}
	if err := config.ValidateProviderHeaders(options.Headers); err != nil {
		return nil, errors.New("create OpenAI client: provider headers are invalid")
	}
	maximum := options.MaxEventBytes
	if maximum == 0 {
		maximum = defaultMaxEventBytes
	}
	if maximum < 1 || maximum > maximumMaxEventBytes {
		return nil, errors.New("create OpenAI client: max event bytes is invalid")
	}
	httpClient := options.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Transport: http.DefaultTransport,
			Timeout:   0,
		}
	}
	clientCopy := *httpClient
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return errors.New("provider redirects are disabled")
	}
	headers := make(http.Header, len(options.Headers))
	for name, value := range options.Headers {
		headers.Set(name, value)
	}
	return &Client{
		endpointURL:   endpointURL,
		endpoint:      options.Endpoint,
		apiKey:        options.APIKey,
		headers:       headers,
		httpClient:    &clientCopy,
		maxEventBytes: maximum,
	}, nil
}

func (client *Client) Stream(ctx context.Context, request provider.Request, emit func(provider.Event) error) error {
	if client == nil {
		return errors.New("stream OpenAI response: client is required")
	}
	if ctx == nil {
		return errors.New("stream OpenAI response: context is required")
	}
	if emit == nil {
		return errors.New("stream OpenAI response: event callback is required")
	}
	if err := request.Validate(); err != nil {
		return err
	}
	switch client.endpoint {
	case config.EndpointResponses:
		return client.streamResponses(ctx, request, emit)
	case config.EndpointChatCompletions:
		return client.streamChat(ctx, request, emit)
	default:
		return errors.New("stream OpenAI response: endpoint is unsupported")
	}
}

func (client *Client) doStreamRequest(ctx context.Context, body any) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, errors.New("encode provider request")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpointURL, strings.NewReader(string(payload)))
	if err != nil {
		return nil, errors.New("create provider request")
	}
	for name, values := range client.headers {
		for _, value := range values {
			httpRequest.Header.Add(name, value)
		}
	}
	httpRequest.Header.Set("Accept", "text/event-stream")
	httpRequest.Header.Set("Authorization", "Bearer "+client.apiKey)
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := client.httpClient.Do(httpRequest)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, provider.NewError("unavailable", 0, true, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = response.Body.Close()
		return nil, providerErrorForStatus(response.StatusCode)
	}
	mediaType, _, parseError := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseError != nil || mediaType != "text/event-stream" {
		_ = response.Body.Close()
		return nil, provider.NewError("invalid_response", response.StatusCode, false, nil)
	}
	return response, nil
}

func buildEndpointURL(baseURL, endpoint string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("create OpenAI client: base URL must be an absolute HTTP or HTTPS URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("create OpenAI client: base URL must not contain userinfo, query, or fragment")
	}
	if endpoint != config.EndpointResponses && endpoint != config.EndpointChatCompletions {
		return "", errors.New("create OpenAI client: endpoint is unsupported")
	}
	joined, err := url.JoinPath(parsed.String(), strings.TrimPrefix(endpoint, "/"))
	if err != nil {
		return "", errors.New("create OpenAI client: endpoint URL is invalid")
	}
	return joined, nil
}

func providerErrorForStatus(status int) *provider.Error {
	code := "unknown"
	retryable := false
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		code = "invalid_argument"
	case http.StatusUnauthorized:
		code = "unauthenticated"
	case http.StatusForbidden:
		code = "permission_denied"
	case http.StatusNotFound:
		code = "not_found"
	case http.StatusConflict:
		code = "aborted"
	case http.StatusRequestTimeout:
		code = "deadline_exceeded"
		retryable = true
	case http.StatusTooManyRequests:
		code = "resource_exhausted"
		retryable = true
	default:
		if status >= 500 {
			code = "unavailable"
			retryable = true
		}
	}
	return provider.NewError(code, status, retryable, nil)
}

func emitValidated(emit func(provider.Event) error, event provider.Event) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("decode provider stream: %w", err)
	}
	return emit(event)
}

var _ provider.Streamer = (*Client)(nil)
