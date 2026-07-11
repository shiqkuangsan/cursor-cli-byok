package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/protocol"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/provider"
)

func TestRunnerReloadsModelMapsEventsAndCommitsConversation(t *testing.T) {
	registry, err := NewConversationRegistry(10)
	if err != nil {
		t.Fatalf("NewConversationRegistry() error = %v", err)
	}
	resolveCalls := 0
	streamCalls := 0
	runner, err := NewRunner(RunnerOptions{
		Registry: registry,
		ResolveModel: func(alias string) (config.ResolvedModel, error) {
			resolveCalls++
			if alias != "relay-gpt" {
				t.Fatalf("alias = %q", alias)
			}
			return config.ResolvedModel{Name: alias, Endpoint: config.EndpointResponses, UpstreamModel: fmt.Sprintf("upstream-%d", resolveCalls), APIKey: "secret"}, nil
		},
		NewStreamer: func(model config.ResolvedModel) (provider.Streamer, error) {
			return provider.StreamFunc(func(ctx context.Context, request provider.Request, emit func(provider.Event) error) error {
				streamCalls++
				if request.Model != fmt.Sprintf("upstream-%d", streamCalls) {
					t.Fatalf("provider model = %q", request.Model)
				}
				if streamCalls == 1 {
					want := []provider.Message{{Role: provider.RoleUser, Content: "first"}}
					if !equalMessages(request.Messages, want) {
						t.Fatalf("first messages = %#v", request.Messages)
					}
					if err := emit(provider.Event{Kind: provider.EventTextDelta, Text: "answer one"}); err != nil {
						return err
					}
					if err := emit(provider.Event{Kind: provider.EventReasoningDelta, Text: "thought"}); err != nil {
						return err
					}
					return emit(provider.Event{Kind: provider.EventUsage, Usage: provider.Usage{InputTokens: 7, OutputTokens: 3, CacheReadTokens: 2}})
				}
				want := []provider.Message{
					{Role: provider.RoleUser, Content: "first"},
					{Role: provider.RoleAssistant, Content: "answer one"},
					{Role: provider.RoleUser, Content: "second"},
				}
				if !equalMessages(request.Messages, want) {
					t.Fatalf("second messages = %#v, want %#v", request.Messages, want)
				}
				if err := emit(provider.Event{Kind: provider.EventTextDelta, Text: "answer two"}); err != nil {
					return err
				}
				return emit(provider.Event{Kind: provider.EventUsage, Usage: provider.Usage{InputTokens: 12, OutputTokens: 2}})
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	var firstEvents []Event
	if err := runner.Execute(context.Background(), protocol.RunRequest{ConversationID: "conversation-1", ModelID: "relay-gpt", UserText: "first"}, func(event Event) error {
		firstEvents = append(firstEvents, event)
		return nil
	}); err != nil {
		t.Fatalf("Execute(first) error = %v", err)
	}
	wantFirstEvents := []Event{
		{Kind: EventTextDelta, Text: "answer one"},
		{Kind: EventReasoningDelta, Text: "thought"},
		{Kind: EventUsage, Usage: protocol.TokenUsage{InputTokens: 7, OutputTokens: 3, CacheReadTokens: 2}},
	}
	if !equalAgentEvents(firstEvents, wantFirstEvents) {
		t.Fatalf("first events = %#v, want %#v", firstEvents, wantFirstEvents)
	}
	if err := runner.Execute(context.Background(), protocol.RunRequest{ConversationID: "conversation-1", ModelID: "relay-gpt", UserText: "second"}, func(Event) error { return nil }); err != nil {
		t.Fatalf("Execute(second) error = %v", err)
	}
	if resolveCalls != 2 || streamCalls != 2 {
		t.Fatalf("calls = resolve %d stream %d", resolveCalls, streamCalls)
	}
	wantSnapshot := []provider.Message{
		{Role: provider.RoleUser, Content: "first"},
		{Role: provider.RoleAssistant, Content: "answer one"},
		{Role: provider.RoleUser, Content: "second"},
		{Role: provider.RoleAssistant, Content: "answer two"},
	}
	if snapshot := registry.Snapshot("conversation-1"); !equalMessages(snapshot, wantSnapshot) {
		t.Fatalf("snapshot = %#v, want %#v", snapshot, wantSnapshot)
	}
}

func TestRunnerRollsBackFailedOrCanceledTurns(t *testing.T) {
	registry, _ := NewConversationRegistry(10)
	call := 0
	runner, err := NewRunner(RunnerOptions{
		Registry: registry,
		ResolveModel: func(alias string) (config.ResolvedModel, error) {
			return config.ResolvedModel{Name: alias, UpstreamModel: "upstream", Endpoint: config.EndpointResponses, APIKey: "secret"}, nil
		},
		NewStreamer: func(config.ResolvedModel) (provider.Streamer, error) {
			call++
			current := call
			return provider.StreamFunc(func(ctx context.Context, request provider.Request, emit func(provider.Event) error) error {
				if current == 1 {
					_ = emit(provider.Event{Kind: provider.EventTextDelta, Text: "partial"})
					return errors.New("failed")
				}
				if len(request.Messages) != 1 || request.Messages[0].Content != "third" {
					t.Fatalf("messages after rollback = %#v", request.Messages)
				}
				return emit(provider.Event{Kind: provider.EventTextDelta, Text: "success"})
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	if err := runner.Execute(context.Background(), protocol.RunRequest{ConversationID: "conversation-1", ModelID: "relay", UserText: "first"}, func(Event) error { return nil }); err == nil {
		t.Fatal("failed turn returned nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runner.Execute(ctx, protocol.RunRequest{ConversationID: "conversation-1", ModelID: "relay", UserText: "second"}, func(Event) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled turn error = %v", err)
	}
	if err := runner.Execute(context.Background(), protocol.RunRequest{ConversationID: "conversation-1", ModelID: "relay", UserText: "third"}, func(Event) error { return nil }); err != nil {
		t.Fatalf("successful turn error = %v", err)
	}
	if snapshot := registry.Snapshot("conversation-1"); len(snapshot) != 2 || snapshot[0].Content != "third" || snapshot[1].Content != "success" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestRunnerAllowsDifferentConversationsConcurrently(t *testing.T) {
	registry, _ := NewConversationRegistry(10)
	entered := make(chan string, 2)
	release := make(chan struct{})
	runner := testRunner(t, registry, provider.StreamFunc(func(ctx context.Context, request provider.Request, emit func(provider.Event) error) error {
		userText := request.Messages[len(request.Messages)-1].Content
		entered <- userText
		select {
		case <-release:
			return emit(provider.Event{Kind: provider.EventTextDelta, Text: "answer " + userText})
		case <-ctx.Done():
			return ctx.Err()
		}
	}))
	var wait sync.WaitGroup
	for _, item := range []struct{ conversation, text string }{{"one", "first"}, {"two", "second"}} {
		wait.Add(1)
		go func(conversation, text string) {
			defer wait.Done()
			if err := runner.Execute(context.Background(), protocol.RunRequest{ConversationID: conversation, ModelID: "relay", UserText: text}, func(Event) error { return nil }); err != nil {
				t.Errorf("Execute(%s) error = %v", conversation, err)
			}
		}(item.conversation, item.text)
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case text := <-entered:
			seen[text] = true
		case <-time.After(time.Second):
			t.Fatal("different conversations were serialized globally")
		}
	}
	close(release)
	wait.Wait()
	if !seen["first"] || !seen["second"] {
		t.Fatalf("entered = %#v", seen)
	}
}

func TestRunnerSerializesTurnsWithinConversation(t *testing.T) {
	registry, _ := NewConversationRegistry(10)
	firstEntered := make(chan struct{})
	secondEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	runner := testRunner(t, registry, provider.StreamFunc(func(ctx context.Context, request provider.Request, emit func(provider.Event) error) error {
		call := calls.Add(1)
		if call == 1 {
			close(firstEntered)
			<-releaseFirst
		} else {
			close(secondEntered)
		}
		return emit(provider.Event{Kind: provider.EventTextDelta, Text: fmt.Sprintf("answer-%d", call)})
	}))
	done := make(chan error, 2)
	go func() {
		done <- runner.Execute(context.Background(), protocol.RunRequest{ConversationID: "same", ModelID: "relay", UserText: "first"}, func(Event) error { return nil })
	}()
	<-firstEntered
	go func() {
		done <- runner.Execute(context.Background(), protocol.RunRequest{ConversationID: "same", ModelID: "relay", UserText: "second"}, func(Event) error { return nil })
	}()
	select {
	case <-secondEntered:
		t.Fatal("second turn entered provider before first completed")
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	}
	select {
	case <-secondEntered:
	case <-time.After(time.Second):
		t.Fatal("second turn never entered provider")
	}
}

func TestConversationRegistryBoundsCompletedTurns(t *testing.T) {
	registry, err := NewConversationRegistry(2)
	if err != nil {
		t.Fatalf("NewConversationRegistry() error = %v", err)
	}
	runner := testRunner(t, registry, provider.StreamFunc(func(ctx context.Context, request provider.Request, emit func(provider.Event) error) error {
		return emit(provider.Event{Kind: provider.EventTextDelta, Text: "answer " + request.Messages[len(request.Messages)-1].Content})
	}))
	for _, text := range []string{"one", "two", "three"} {
		if err := runner.Execute(context.Background(), protocol.RunRequest{ConversationID: "bounded", ModelID: "relay", UserText: text}, func(Event) error { return nil }); err != nil {
			t.Fatalf("Execute(%s) error = %v", text, err)
		}
	}
	snapshot := registry.Snapshot("bounded")
	if len(snapshot) != 4 || snapshot[0].Content != "two" || snapshot[2].Content != "three" {
		t.Fatalf("bounded snapshot = %#v", snapshot)
	}
}

func TestRunnerExecutesToolAndContinuesProviderTurn(t *testing.T) {
	registry, _ := NewConversationRegistry(10)
	providerPass := 0
	readTool := provider.Tool{Name: "Read", Description: "Read a file", Parameters: []byte(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}
	runner, err := NewRunner(RunnerOptions{
		Registry: registry,
		Tools:    []provider.Tool{readTool},
		ResolveModel: func(alias string) (config.ResolvedModel, error) {
			return config.ResolvedModel{Name: alias, UpstreamModel: "upstream", Endpoint: config.EndpointResponses, APIKey: "secret"}, nil
		},
		NewStreamer: func(config.ResolvedModel) (provider.Streamer, error) {
			return provider.StreamFunc(func(ctx context.Context, request provider.Request, emit func(provider.Event) error) error {
				providerPass++
				if len(request.Tools) != 1 || request.Tools[0].Name != "Read" {
					t.Fatalf("tools = %#v", request.Tools)
				}
				if providerPass == 1 {
					if err := emit(provider.Event{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{Index: 0, ID: "call-1", Name: "Read", ArgumentsDelta: `{"path":`}}); err != nil {
						return err
					}
					if err := emit(provider.Event{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{Index: 0, ArgumentsDelta: `"README.md"}`}}); err != nil {
						return err
					}
					if err := emit(provider.Event{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{Index: 0, Done: true}}); err != nil {
						return err
					}
					return emit(provider.Event{Kind: provider.EventUsage, Usage: provider.Usage{InputTokens: 5, OutputTokens: 2}})
				}
				want := []provider.Message{
					{Role: provider.RoleUser, Content: "read it"},
					{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "Read", Arguments: `{"path":"README.md"}`}}},
					{Role: provider.RoleTool, ToolCallID: "call-1", Content: "README contents"},
				}
				if !equalMessagesWithTools(request.Messages, want) {
					t.Fatalf("continuation messages = %#v, want %#v", request.Messages, want)
				}
				if err := emit(provider.Event{Kind: provider.EventTextDelta, Text: "finished"}); err != nil {
					return err
				}
				return emit(provider.Event{Kind: provider.EventUsage, Usage: provider.Usage{InputTokens: 8, OutputTokens: 3, CacheReadTokens: 1}})
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	toolCalls := 0
	var events []Event
	err = runner.Execute(context.Background(), protocol.RunRequest{ConversationID: "tool-conversation", ModelID: "relay", UserText: "read it"}, func(event Event) error {
		events = append(events, event)
		if event.Kind == EventToolCall {
			toolCalls++
			if event.Tool.ID != "call-1" || event.Tool.Name != "Read" || event.Tool.Arguments != `{"path":"README.md"}` {
				t.Fatalf("tool call = %#v", event.Tool)
			}
			event.Result <- ToolResult{CallID: event.Tool.ID, Content: "README contents"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if providerPass != 2 || toolCalls != 1 {
		t.Fatalf("passes/tools = %d/%d", providerPass, toolCalls)
	}
	last := events[len(events)-1]
	if last.Kind != EventUsage || last.Usage != (protocol.TokenUsage{InputTokens: 13, OutputTokens: 5, CacheReadTokens: 1}) {
		t.Fatalf("aggregate usage = %#v", last)
	}
	wantSnapshot := []provider.Message{
		{Role: provider.RoleUser, Content: "read it"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "Read", Arguments: `{"path":"README.md"}`}}},
		{Role: provider.RoleTool, ToolCallID: "call-1", Content: "README contents"},
		{Role: provider.RoleAssistant, Content: "finished"},
	}
	if snapshot := registry.Snapshot("tool-conversation"); !equalMessagesWithTools(snapshot, wantSnapshot) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestRunnerMergesRunScopedMCPToolsWithoutRenamingSchema(t *testing.T) {
	staticTool := provider.Tool{Name: "Read", Parameters: []byte(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}
	schema := []byte(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`)
	pass := 0
	runner, err := NewRunner(RunnerOptions{
		Tools: []provider.Tool{staticTool},
		ResolveModel: func(alias string) (config.ResolvedModel, error) {
			return config.ResolvedModel{Name: alias, UpstreamModel: "upstream", Endpoint: config.EndpointResponses, APIKey: "secret"}, nil
		},
		NewStreamer: func(config.ResolvedModel) (provider.Streamer, error) {
			return provider.StreamFunc(func(ctx context.Context, request provider.Request, emit func(provider.Event) error) error {
				pass++
				if len(request.Tools) != 2 || request.Tools[0].Name != "Read" || request.Tools[1].Name != "weather_lookup" || request.Tools[1].Description != "Look up weather" || !bytes.Equal(request.Tools[1].Parameters, schema) {
					t.Fatalf("merged tools = %#v", request.Tools)
				}
				if pass == 1 {
					if err := emit(provider.Event{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{Index: 0, ID: "mcp-call-1", Name: "weather_lookup", ArgumentsDelta: `{"city":"Taipei"}`}}); err != nil {
						return err
					}
					return emit(provider.Event{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{Index: 0, Done: true}})
				}
				if len(request.Messages) != 3 || len(request.Messages[1].ToolCalls) != 1 || request.Messages[1].ToolCalls[0].Name != "weather_lookup" || request.Messages[2].Content != "sunny" {
					t.Fatalf("MCP continuation messages = %#v", request.Messages)
				}
				return emit(provider.Event{Kind: provider.EventTextDelta, Text: "finished"})
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	run := protocol.RunRequest{
		ConversationID: "mcp-conversation", ModelID: "relay", UserText: "weather",
		MCPTools: []protocol.MCPToolDefinition{{
			Name: "weather_lookup", Description: "Look up weather", ProviderIdentifier: "weather-server", ToolName: "lookup", InputSchema: schema,
		}},
	}
	toolCalls := 0
	err = runner.Execute(context.Background(), run, func(event Event) error {
		if event.Kind == EventToolCall {
			toolCalls++
			if event.Tool.Name != "weather_lookup" || event.Tool.Arguments != `{"city":"Taipei"}` {
				t.Fatalf("MCP tool event = %#v", event.Tool)
			}
			event.Result <- ToolResult{CallID: event.Tool.ID, Content: "sunny"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if pass != 2 || toolCalls != 1 {
		t.Fatalf("passes/tool calls = %d/%d", pass, toolCalls)
	}
}

func TestRunnerRejectsMCPToolNameCollisionsBeforeProviderDispatch(t *testing.T) {
	called := false
	runner, err := NewRunner(RunnerOptions{
		Tools: []provider.Tool{{Name: "Read", Parameters: []byte(`{"type":"object"}`)}},
		ResolveModel: func(alias string) (config.ResolvedModel, error) {
			return config.ResolvedModel{Name: alias, UpstreamModel: "upstream", Endpoint: config.EndpointResponses, APIKey: "secret"}, nil
		},
		NewStreamer: func(config.ResolvedModel) (provider.Streamer, error) {
			called = true
			return provider.StreamFunc(func(context.Context, provider.Request, func(provider.Event) error) error { return nil }), nil
		},
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	err = runner.Execute(context.Background(), protocol.RunRequest{
		ConversationID: "collision", ModelID: "relay", UserText: "hello",
		MCPTools: []protocol.MCPToolDefinition{{Name: "Read", ProviderIdentifier: "server", ToolName: "read", InputSchema: []byte(`{"type":"object"}`)}},
	}, func(Event) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("collision error = %v", err)
	}
	if called {
		t.Fatal("provider client was created for colliding MCP tools")
	}
}

func testRunner(t *testing.T, registry *ConversationRegistry, streamer provider.Streamer) *Runner {
	t.Helper()
	runner, err := NewRunner(RunnerOptions{
		Registry: registry,
		ResolveModel: func(alias string) (config.ResolvedModel, error) {
			return config.ResolvedModel{Name: alias, UpstreamModel: "upstream", Endpoint: config.EndpointResponses, APIKey: "secret"}, nil
		},
		NewStreamer: func(config.ResolvedModel) (provider.Streamer, error) { return streamer, nil },
	})
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}
	return runner
}

func equalMessages(left, right []provider.Message) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Role != right[index].Role || left[index].Content != right[index].Content || left[index].ToolCallID != right[index].ToolCallID {
			return false
		}
	}
	return true
}

func equalMessagesWithTools(left, right []provider.Message) bool {
	if !equalMessages(left, right) {
		return false
	}
	for index := range left {
		if len(left[index].ToolCalls) != len(right[index].ToolCalls) {
			return false
		}
		for callIndex := range left[index].ToolCalls {
			if left[index].ToolCalls[callIndex] != right[index].ToolCalls[callIndex] {
				return false
			}
		}
	}
	return true
}

func equalAgentEvents(left, right []Event) bool {
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
