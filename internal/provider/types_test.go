package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRequestValidateAcceptsConversationAndTools(t *testing.T) {
	request := Request{
		Model: "gpt-5.4",
		Messages: []Message{
			{Role: RoleSystem, Content: "Be concise."},
			{Role: RoleUser, Content: "Read the file."},
			{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "read_file", Arguments: `{"path":"README.md"}`}}},
			{Role: RoleTool, ToolCallID: "call-1", Content: "contents"},
		},
		Tools: []Tool{{Name: "read_file", Description: "Read a file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}},
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRequestValidateRejectsInvalidProviderInput(t *testing.T) {
	valid := Request{Model: "gpt-5.4", Messages: []Message{{Role: RoleUser, Content: "hello"}}}
	tests := []struct {
		name   string
		mutate func(*Request)
		want   string
	}{
		{name: "model", mutate: func(request *Request) { request.Model = "" }, want: "model"},
		{name: "messages", mutate: func(request *Request) { request.Messages = nil }, want: "message"},
		{name: "role", mutate: func(request *Request) { request.Messages[0].Role = "owner" }, want: "role"},
		{name: "empty user", mutate: func(request *Request) { request.Messages[0].Content = "" }, want: "content"},
		{name: "tool call id", mutate: func(request *Request) { request.Messages = []Message{{Role: RoleTool, Content: "result"}} }, want: "tool_call_id"},
		{name: "assistant empty", mutate: func(request *Request) { request.Messages = []Message{{Role: RoleAssistant}} }, want: "assistant"},
		{name: "duplicate tools", mutate: func(request *Request) {
			request.Tools = []Tool{{Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}, {Name: "read", Parameters: json.RawMessage(`{"type":"object"}`)}}
		}, want: "duplicate"},
		{name: "tool name", mutate: func(request *Request) {
			request.Tools = []Tool{{Name: "bad name", Parameters: json.RawMessage(`{"type":"object"}`)}}
		}, want: "name"},
		{name: "tool schema", mutate: func(request *Request) { request.Tools = []Tool{{Name: "read", Parameters: json.RawMessage(`[]`)}} }, want: "parameters"},
		{name: "arguments", mutate: func(request *Request) {
			request.Messages = []Message{{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "read", Arguments: `{`}}}}
		}, want: "arguments"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			request.Messages = append([]Message(nil), valid.Messages...)
			test.mutate(&request)
			err := request.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestEventValidateDistinguishesStreamEvents(t *testing.T) {
	tests := []Event{
		{Kind: EventTextDelta, Text: "hello"},
		{Kind: EventReasoningDelta, Text: "thinking"},
		{Kind: EventToolCallDelta, ToolCall: ToolCallDelta{Index: 0, ID: "call-1", Name: "read", ArgumentsDelta: `{"path":`}},
		{Kind: EventUsage, Usage: Usage{InputTokens: 10, OutputTokens: 4, CacheReadTokens: 2}},
	}
	for _, event := range tests {
		if err := event.Validate(); err != nil {
			t.Fatalf("Validate(%#v) error = %v", event, err)
		}
	}
	if err := (Event{Kind: EventTextDelta}).Validate(); err == nil {
		t.Fatal("empty text delta accepted")
	}
	if err := (Event{Kind: EventUsage, Usage: Usage{InputTokens: -1}}).Validate(); err == nil {
		t.Fatal("negative usage accepted")
	}
}

func TestStreamFuncImplementsStreamer(t *testing.T) {
	called := false
	streamer := StreamFunc(func(context.Context, Request, func(Event) error) error {
		called = true
		return nil
	})
	if err := streamer.Stream(context.Background(), Request{}, func(Event) error { return nil }); err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if !called {
		t.Fatal("stream function was not called")
	}
}
