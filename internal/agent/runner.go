package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/protocol"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/provider"
)

type ModelResolver func(string) (config.ResolvedModel, error)
type StreamerFactory func(config.ResolvedModel) (provider.Streamer, error)

type RunnerOptions struct {
	Registry     *ConversationRegistry
	Tools        []provider.Tool
	ResolveModel ModelResolver
	NewStreamer  StreamerFactory
}

type Runner struct {
	registry     *ConversationRegistry
	resolveModel ModelResolver
	newStreamer  StreamerFactory
	tools        []provider.Tool
}

func NewRunner(options RunnerOptions) (*Runner, error) {
	if options.ResolveModel == nil {
		return nil, errors.New("create agent runner: model resolver is required")
	}
	if options.NewStreamer == nil {
		return nil, errors.New("create agent runner: streamer factory is required")
	}
	registry := options.Registry
	if registry == nil {
		registry = newDefaultConversationRegistry()
	}
	tools := cloneProviderTools(options.Tools)
	if len(tools) > 0 {
		validation := provider.Request{Model: "validation", Messages: []provider.Message{{Role: provider.RoleUser, Content: "validation"}}, Tools: tools}
		if err := validation.Validate(); err != nil {
			return nil, fmt.Errorf("create agent runner: %w", err)
		}
	}
	return &Runner{registry: registry, resolveModel: options.ResolveModel, newStreamer: options.NewStreamer, tools: tools}, nil
}

func (runner *Runner) Execute(ctx context.Context, run protocol.RunRequest, emit func(Event) error) error {
	if runner == nil {
		return errors.New("execute agent turn: runner is required")
	}
	if ctx == nil {
		return errors.New("execute agent turn: context is required")
	}
	if emit == nil {
		return errors.New("execute agent turn: event callback is required")
	}
	if err := validateRunRequest(run); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	conversation, releaseConversation := runner.registry.acquireConversation(run.ConversationID)
	defer releaseConversation()
	if err := ctx.Err(); err != nil {
		return err
	}
	turnTools, err := mergeRunTools(runner.tools, run.MCPTools)
	if err != nil {
		return fmt.Errorf("execute agent turn: %w", err)
	}
	resolved, err := runner.resolveModel(run.ModelID)
	if err != nil {
		return fmt.Errorf("execute agent turn: resolve model: %w", err)
	}
	if !validRunIdentifier(resolved.UpstreamModel) {
		return errors.New("execute agent turn: resolved upstream model is invalid")
	}
	streamer, err := runner.newStreamer(resolved)
	if err != nil {
		return fmt.Errorf("execute agent turn: create provider client: %w", err)
	}
	if streamer == nil {
		return errors.New("execute agent turn: provider client is unavailable")
	}
	baseMessages := conversation.messagesLocked()
	turnMessages := []provider.Message{{Role: provider.RoleUser, Content: run.UserText}}
	deliveredCallIDs := make(map[string]struct{})
	allowedTools := make(map[string]struct{}, len(turnTools))
	for _, tool := range turnTools {
		allowedTools[tool.Name] = struct{}{}
	}
	var totalUsage protocol.TokenUsage
	for providerPass := 0; providerPass < 16; providerPass++ {
		messages := append(cloneProviderMessages(baseMessages), cloneProviderMessages(turnMessages)...)
		providerRequest := provider.Request{Model: resolved.UpstreamModel, Messages: messages, Tools: cloneProviderTools(turnTools)}
		if err := providerRequest.Validate(); err != nil {
			return fmt.Errorf("execute agent turn: %w", err)
		}
		pass, err := runner.runProviderPass(ctx, streamer, providerRequest, emit)
		if err != nil {
			return err
		}
		totalUsage.InputTokens += pass.usage.InputTokens
		totalUsage.OutputTokens += pass.usage.OutputTokens
		totalUsage.CacheReadTokens += pass.usage.CacheReadTokens
		totalUsage.CacheWriteTokens += pass.usage.CacheWriteTokens
		if len(pass.toolCalls) == 0 {
			if pass.text == "" {
				return errors.New("execute agent turn: provider returned no assistant text")
			}
			turnMessages = append(turnMessages, provider.Message{Role: provider.RoleAssistant, Content: pass.text})
			runner.registry.appendTurnLocked(conversation, turnMessages)
			return emit(Event{Kind: EventUsage, Usage: totalUsage})
		}
		assistant := provider.Message{Role: provider.RoleAssistant, Content: pass.text, ToolCalls: pass.toolCalls}
		turnMessages = append(turnMessages, assistant)
		for _, call := range pass.toolCalls {
			if _, ok := allowedTools[call.Name]; !ok {
				return errors.New("execute agent turn: provider requested a tool that was not offered")
			}
			if _, duplicate := deliveredCallIDs[call.ID]; duplicate {
				return errors.New("execute agent turn: provider repeated a tool call ID")
			}
			deliveredCallIDs[call.ID] = struct{}{}
			resultChannel := make(chan ToolResult, 1)
			if err := emit(Event{
				Kind:   EventToolCall,
				Tool:   ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Arguments},
				Result: resultChannel,
			}); err != nil {
				return err
			}
			var result ToolResult
			select {
			case <-ctx.Done():
				return ctx.Err()
			case result = <-resultChannel:
			}
			if result.CallID != call.ID {
				return errors.New("execute agent turn: tool result call ID does not match")
			}
			turnMessages = append(turnMessages, provider.Message{Role: provider.RoleTool, ToolCallID: call.ID, Content: result.Content})
		}
	}
	return errors.New("execute agent turn: provider exceeded the tool continuation limit")
}

func mergeRunTools(static []provider.Tool, dynamic []protocol.MCPToolDefinition) ([]provider.Tool, error) {
	result := cloneProviderTools(static)
	seen := make(map[string]struct{}, len(static)+len(dynamic))
	for _, tool := range static {
		seen[tool.Name] = struct{}{}
	}
	for _, definition := range dynamic {
		if _, duplicate := seen[definition.Name]; duplicate {
			return nil, errors.New("duplicate tool name between built-in and MCP tools")
		}
		seen[definition.Name] = struct{}{}
		result = append(result, provider.Tool{
			Name:        definition.Name,
			Description: definition.Description,
			Parameters:  append([]byte(nil), definition.InputSchema...),
		})
	}
	if len(result) == 0 {
		return result, nil
	}
	validation := provider.Request{Model: "validation", Messages: []provider.Message{{Role: provider.RoleUser, Content: "validation"}}, Tools: result}
	if err := validation.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

type providerPassResult struct {
	text      string
	toolCalls []provider.ToolCall
	usage     provider.Usage
}

type providerToolAccumulator struct {
	id        string
	name      string
	arguments strings.Builder
	done      bool
}

func (runner *Runner) runProviderPass(ctx context.Context, streamer provider.Streamer, request provider.Request, emit func(Event) error) (providerPassResult, error) {
	var text strings.Builder
	toolCalls := make(map[int]*providerToolAccumulator)
	var usage provider.Usage
	var callbackError error
	err := streamer.Stream(ctx, request, func(event provider.Event) error {
		if callbackError != nil {
			return callbackError
		}
		if err := event.Validate(); err != nil {
			callbackError = fmt.Errorf("execute agent turn: %w", err)
			return callbackError
		}
		switch event.Kind {
		case provider.EventTextDelta:
			text.WriteString(event.Text)
			callbackError = emit(Event{Kind: EventTextDelta, Text: event.Text})
		case provider.EventReasoningDelta:
			callbackError = emit(Event{Kind: EventReasoningDelta, Text: event.Text})
		case provider.EventUsage:
			usage = event.Usage
		case provider.EventToolCallDelta:
			accumulator := toolCalls[event.ToolCall.Index]
			if accumulator == nil {
				accumulator = &providerToolAccumulator{}
				toolCalls[event.ToolCall.Index] = accumulator
			}
			if event.ToolCall.ID != "" {
				accumulator.id = event.ToolCall.ID
			}
			if event.ToolCall.Name != "" {
				accumulator.name = event.ToolCall.Name
			}
			accumulator.arguments.WriteString(event.ToolCall.ArgumentsDelta)
			accumulator.done = accumulator.done || event.ToolCall.Done
		default:
			callbackError = errors.New("execute agent turn: provider event is unsupported")
		}
		return callbackError
	})
	if callbackError != nil {
		return providerPassResult{}, callbackError
	}
	if err != nil {
		if ctx.Err() != nil {
			return providerPassResult{}, ctx.Err()
		}
		return providerPassResult{}, fmt.Errorf("execute agent turn: provider stream: %w", err)
	}
	indexes := make([]int, 0, len(toolCalls))
	for index := range toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	result := providerPassResult{text: text.String(), usage: usage}
	for _, index := range indexes {
		call := toolCalls[index]
		candidate := provider.ToolCall{ID: strings.TrimSpace(call.id), Name: strings.TrimSpace(call.name), Arguments: call.arguments.String()}
		if !call.done || candidate.ID == "" || candidate.Name == "" {
			return providerPassResult{}, errors.New("execute agent turn: provider tool call is incomplete")
		}
		validation := provider.Request{Model: request.Model, Messages: []provider.Message{{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{candidate}}}}
		if err := validation.Validate(); err != nil {
			return providerPassResult{}, fmt.Errorf("execute agent turn: %w", err)
		}
		result.toolCalls = append(result.toolCalls, candidate)
	}
	return result, nil
}

func cloneProviderMessages(messages []provider.Message) []provider.Message {
	result := make([]provider.Message, len(messages))
	for index, message := range messages {
		result[index] = cloneProviderMessage(message)
	}
	return result
}

func cloneProviderTools(tools []provider.Tool) []provider.Tool {
	result := make([]provider.Tool, len(tools))
	for index, tool := range tools {
		result[index] = tool
		result[index].Parameters = append([]byte(nil), tool.Parameters...)
	}
	return result
}

func validateRunRequest(run protocol.RunRequest) error {
	if !validRunIdentifier(run.ConversationID) {
		return errors.New("execute agent turn: conversation ID is invalid")
	}
	if !validRunIdentifier(run.ModelID) {
		return errors.New("execute agent turn: model ID is invalid")
	}
	if strings.TrimSpace(run.UserText) == "" {
		return errors.New("execute agent turn: user message is required")
	}
	return nil
}

func validRunIdentifier(value string) bool {
	return value != "" && len(value) <= 1024 && value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

var _ Executor = (*Runner)(nil)
