package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/provider"
)

func TestChatStreamSendsContractAndEmitsCanonicalEvents(t *testing.T) {
	var requestFailure string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
			requestFailure = fmt.Sprintf("request target = %s %s", request.Method, request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer chat-key" {
			requestFailure = "authorization header mismatch"
		}
		var body struct {
			Model         string            `json:"model"`
			Messages      []json.RawMessage `json:"messages"`
			Tools         []json.RawMessage `json:"tools"`
			Stream        bool              `json:"stream"`
			StreamOptions struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			requestFailure = "request JSON was invalid"
		}
		if body.Model != "chat-upstream" || !body.Stream || !body.StreamOptions.IncludeUsage || len(body.Messages) != 4 || len(body.Tools) != 1 {
			requestFailure = fmt.Sprintf("request body = %#v", body)
		}
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"hello \",\"reasoning_content\":\"thinking\",\"tool_calls\":[{\"index\":0,\"id\":\"call-1\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\"}}]}}]}\n\n")
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"\\\"README.md\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4,\"prompt_tokens_details\":{\"cached_tokens\":2}}}\n\n")
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := newTestClient(t, Options{BaseURL: server.URL, Endpoint: config.EndpointChatCompletions, APIKey: "chat-key", HTTPClient: server.Client()})
	request := provider.Request{
		Model: "chat-upstream",
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "Be concise."},
			{Role: provider.RoleUser, Content: "Read README."},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "old-call", Name: "read_file", Arguments: `{"path":"OLD.md"}`}}},
			{Role: provider.RoleTool, ToolCallID: "old-call", Content: "old contents"},
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
		{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{Index: 0, ID: "call-1", Name: "read_file", ArgumentsDelta: `{"path":`}},
		{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{Index: 0, ArgumentsDelta: `"README.md"}`}},
		{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{Index: 0, Done: true}},
		{Kind: provider.EventUsage, Usage: provider.Usage{InputTokens: 9, OutputTokens: 4, CacheReadTokens: 2}},
	}
	if !equalProviderEvents(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestChatStreamSupportsReasoningAliasAndRequiresDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"reasoning\":\"alias\"},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer server.Close()
	client := newTestClient(t, Options{BaseURL: server.URL, Endpoint: config.EndpointChatCompletions, APIKey: "key", HTTPClient: server.Client()})
	var events []provider.Event
	err := client.Stream(context.Background(), provider.Request{Model: "model", Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}}, func(event provider.Event) error {
		events = append(events, event)
		return nil
	})
	if err == nil || err.Error() != "decode Chat Completions stream: terminal event is missing" {
		t.Fatalf("Stream() error = %v", err)
	}
	if len(events) != 1 || events[0] != (provider.Event{Kind: provider.EventReasoningDelta, Text: "alias"}) {
		t.Fatalf("events = %#v", events)
	}
}

func TestChatStreamRejectsMalformedChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":-1,\"function\":{\"name\":\"read\"}}]}}]}\n\n")
	}))
	defer server.Close()
	client := newTestClient(t, Options{BaseURL: server.URL, Endpoint: config.EndpointChatCompletions, APIKey: "key", HTTPClient: server.Client()})
	err := client.Stream(context.Background(), provider.Request{Model: "model", Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}}, func(provider.Event) error { return nil })
	if err == nil {
		t.Fatal("malformed tool call chunk was accepted")
	}
}
