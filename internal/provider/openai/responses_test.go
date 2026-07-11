package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/provider"
)

func TestResponsesStreamSendsContractAndEmitsCanonicalEvents(t *testing.T) {
	var requestFailure string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/gateway/v1/responses" || request.URL.RawQuery != "" {
			requestFailure = fmt.Sprintf("request target = %s %s", request.Method, request.URL.String())
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-api-key" {
			requestFailure = "authorization header mismatch"
		}
		if got := request.Header.Get("Accept"); got != "text/event-stream" {
			requestFailure = "accept header mismatch"
		}
		var body struct {
			Model  string            `json:"model"`
			Input  []json.RawMessage `json:"input"`
			Tools  []json.RawMessage `json:"tools"`
			Stream bool              `json:"stream"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			requestFailure = "request JSON was invalid"
		}
		if body.Model != "gpt-upstream" || !body.Stream || len(body.Input) != 2 || len(body.Tools) != 1 {
			requestFailure = fmt.Sprintf("request body = %#v", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello \"}\n\n")
		_, _ = io.WriteString(writer, "event: response.reasoning_summary_text.delta\n")
		_, _ = io.WriteString(writer, "data: {\"delta\":\"thinking\"}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_item.added\",\"output_index\":1,\"item\":{\"type\":\"function_call\",\"call_id\":\"call-1\",\"name\":\"read_file\"}}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"delta\":\"{\\\"path\\\":\"}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":1,\"delta\":\"\\\"README.md\\\"}\"}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.function_call_arguments.done\",\"output_index\":1}\n\n")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":12,\"output_tokens\":5,\"input_tokens_details\":{\"cached_tokens\":3}}}}\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := newTestClient(t, Options{
		BaseURL:       server.URL + "/gateway/",
		Endpoint:      config.EndpointResponses,
		APIKey:        "test-api-key",
		HTTPClient:    server.Client(),
		MaxEventBytes: 64 * 1024,
	})
	request := provider.Request{
		Model: "gpt-upstream",
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "Be concise."},
			{Role: provider.RoleUser, Content: "Read README."},
		},
		Tools: []provider.Tool{{Name: "read_file", Description: "Read a file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)}},
	}
	var events []provider.Event
	if err := client.Stream(context.Background(), request, func(event provider.Event) error {
		events = append(events, event)
		return nil
	}); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if requestFailure != "" {
		t.Fatal(requestFailure)
	}
	want := []provider.Event{
		{Kind: provider.EventTextDelta, Text: "hello "},
		{Kind: provider.EventReasoningDelta, Text: "thinking"},
		{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{Index: 1, ID: "call-1", Name: "read_file"}},
		{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{Index: 1, ArgumentsDelta: `{"path":`}},
		{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{Index: 1, ArgumentsDelta: `"README.md"}`}},
		{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{Index: 1, Done: true}},
		{Kind: provider.EventUsage, Usage: provider.Usage{InputTokens: 12, OutputTokens: 5, CacheReadTokens: 3}},
	}
	if !equalProviderEvents(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestResponsesRequestConvertsToolHistory(t *testing.T) {
	var input []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Input []map[string]any `json:"input"`
		}
		_ = json.NewDecoder(request.Body).Decode(&body)
		input = body.Input
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{}}}\n\n")
	}))
	defer server.Close()
	client := newTestClient(t, Options{BaseURL: server.URL, Endpoint: config.EndpointResponses, APIKey: "key", HTTPClient: server.Client()})
	request := provider.Request{
		Model: "model",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "read"},
			{Role: provider.RoleAssistant, Content: "checking", ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "read_file", Arguments: `{"path":"README.md"}`}}},
			{Role: provider.RoleTool, ToolCallID: "call-1", Content: "contents"},
		},
	}
	if err := client.Stream(context.Background(), request, func(provider.Event) error { return nil }); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if len(input) != 4 {
		t.Fatalf("input = %#v, want four items", input)
	}
	if input[2]["type"] != "function_call" || input[2]["call_id"] != "call-1" || input[3]["type"] != "function_call_output" || input[3]["call_id"] != "call-1" {
		t.Fatalf("tool history = %#v", input)
	}
}

func TestResponsesStreamReturnsRedactedTypedHTTPError(t *testing.T) {
	for _, test := range []struct {
		status    int
		code      string
		retryable bool
	}{
		{http.StatusBadRequest, "invalid_argument", false},
		{http.StatusUnauthorized, "unauthenticated", false},
		{http.StatusNotFound, "not_found", false},
		{http.StatusTooManyRequests, "resource_exhausted", true},
		{http.StatusInternalServerError, "unavailable", true},
	} {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, `{"error":{"message":"upstream leaked sk-secret"}}`)
			}))
			defer server.Close()
			client := newTestClient(t, Options{BaseURL: server.URL, Endpoint: config.EndpointResponses, APIKey: "sk-secret", HTTPClient: server.Client()})
			err := client.Stream(context.Background(), provider.Request{Model: "model", Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}}, func(provider.Event) error { return nil })
			var providerError *provider.Error
			if !errors.As(err, &providerError) {
				t.Fatalf("Stream() error = %T %v, want *provider.Error", err, err)
			}
			if providerError.StatusCode != test.status || providerError.Code != test.code || providerError.Retryable != test.retryable {
				t.Fatalf("provider error = %#v", providerError)
			}
			if strings.Contains(err.Error(), "sk-secret") || strings.Contains(err.Error(), "upstream leaked") {
				t.Fatalf("error leaked provider response: %v", err)
			}
		})
	}
}

func TestResponsesStreamRejectsMalformedAndOversizedSSE(t *testing.T) {
	tests := []struct {
		name string
		body string
		max  int
		want string
	}{
		{name: "malformed JSON", body: "data: {\n\n", max: 1024, want: "decode"},
		{name: "oversized", body: "data: " + strings.Repeat("x", 128) + "\n\n", max: 32, want: "too large"},
		{name: "missing terminal", body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n", max: 1024, want: "terminal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			client := newTestClient(t, Options{BaseURL: server.URL, Endpoint: config.EndpointResponses, APIKey: "key", HTTPClient: server.Client(), MaxEventBytes: test.max})
			err := client.Stream(context.Background(), provider.Request{Model: "model", Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}}, func(provider.Event) error { return nil })
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Stream() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestResponsesStreamPropagatesCancellationAndEmitterError(t *testing.T) {
	requestCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		<-request.Context().Done()
		close(requestCanceled)
	}))
	defer server.Close()
	client := newTestClient(t, Options{BaseURL: server.URL, Endpoint: config.EndpointResponses, APIKey: "key", HTTPClient: server.Client()})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- client.Stream(ctx, provider.Request{Model: "model", Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}}, func(provider.Event) error { return nil })
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Stream() error = %v, want context canceled", err)
	}
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("provider HTTP request was not canceled")
	}

	emitError := errors.New("consumer stopped")
	emitterServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
	}))
	defer emitterServer.Close()
	emitterClient := newTestClient(t, Options{BaseURL: emitterServer.URL, Endpoint: config.EndpointResponses, APIKey: "key", HTTPClient: emitterServer.Client()})
	err := emitterClient.Stream(context.Background(), provider.Request{Model: "model", Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}}, func(provider.Event) error { return emitError })
	if !errors.Is(err, emitError) {
		t.Fatalf("Stream() error = %v, want emitter error", err)
	}
}

func TestNewClientRejectsUnsafeOrIncompleteOptionsWithoutLeakingKey(t *testing.T) {
	for _, options := range []Options{
		{},
		{BaseURL: "not-a-url", Endpoint: config.EndpointResponses, APIKey: "secret"},
		{BaseURL: "https://example.com", Endpoint: "/other", APIKey: "secret"},
		{BaseURL: "https://example.com", Endpoint: config.EndpointResponses},
	} {
		_, err := NewClient(options)
		if err == nil {
			t.Fatalf("NewClient(%#v) accepted invalid options", options)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("NewClient() error leaked key: %v", err)
		}
	}
}

func newTestClient(t *testing.T, options Options) *Client {
	t.Helper()
	client, err := NewClient(options)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client
}

func equalProviderEvents(left, right []provider.Event) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
