package provider

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type Request struct {
	Model    string
	Messages []Message
	Tools    []Tool
}

func (request Request) Validate() error {
	if !validBoundedText(request.Model, 1024) {
		return errors.New("validate provider request: model is required and must be valid")
	}
	if len(request.Messages) == 0 {
		return errors.New("validate provider request: at least one message is required")
	}
	for _, message := range request.Messages {
		if err := message.validate(); err != nil {
			return err
		}
	}
	seenTools := make(map[string]struct{}, len(request.Tools))
	for _, tool := range request.Tools {
		if !toolNamePattern.MatchString(tool.Name) {
			return errors.New("validate provider request: tool name is invalid")
		}
		if _, exists := seenTools[tool.Name]; exists {
			return errors.New("validate provider request: duplicate tool name")
		}
		seenTools[tool.Name] = struct{}{}
		if !validJSONObject(tool.Parameters) {
			return errors.New("validate provider request: tool parameters must be a JSON object")
		}
	}
	return nil
}

func (message Message) validate() error {
	switch message.Role {
	case RoleSystem, RoleUser:
		if message.Content == "" {
			return errors.New("validate provider request: message content is required")
		}
		if len(message.ToolCalls) > 0 || message.ToolCallID != "" {
			return errors.New("validate provider request: message role has unsupported tool fields")
		}
	case RoleAssistant:
		if message.Content == "" && len(message.ToolCalls) == 0 {
			return errors.New("validate provider request: assistant message must contain content or tool calls")
		}
		if message.ToolCallID != "" {
			return errors.New("validate provider request: assistant message must not contain tool_call_id")
		}
		for _, call := range message.ToolCalls {
			if !validBoundedText(call.ID, 1024) {
				return errors.New("validate provider request: tool call ID is invalid")
			}
			if !toolNamePattern.MatchString(call.Name) {
				return errors.New("validate provider request: tool call name is invalid")
			}
			if !validJSONObject([]byte(call.Arguments)) {
				return errors.New("validate provider request: tool call arguments must be a JSON object")
			}
		}
	case RoleTool:
		if !validBoundedText(message.ToolCallID, 1024) {
			return errors.New("validate provider request: tool_call_id is required")
		}
		if len(message.ToolCalls) > 0 {
			return errors.New("validate provider request: tool result must not contain tool calls")
		}
	default:
		return errors.New("validate provider request: message role is unsupported")
	}
	return nil
}

type EventKind uint8

const (
	EventUnknown EventKind = iota
	EventTextDelta
	EventReasoningDelta
	EventToolCallDelta
	EventUsage
)

type Event struct {
	Kind     EventKind
	Text     string
	ToolCall ToolCallDelta
	Usage    Usage
}

type ToolCallDelta struct {
	Index          int
	ID             string
	Name           string
	ArgumentsDelta string
	Done           bool
}

type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

func (event Event) Validate() error {
	switch event.Kind {
	case EventTextDelta, EventReasoningDelta:
		if event.Text == "" {
			return errors.New("validate provider event: text delta is empty")
		}
	case EventToolCallDelta:
		if event.ToolCall.Index < 0 {
			return errors.New("validate provider event: tool call index is negative")
		}
		if event.ToolCall.ID == "" && event.ToolCall.Name == "" && event.ToolCall.ArgumentsDelta == "" && !event.ToolCall.Done {
			return errors.New("validate provider event: tool call delta is empty")
		}
	case EventUsage:
		if event.Usage.InputTokens < 0 || event.Usage.OutputTokens < 0 || event.Usage.CacheReadTokens < 0 || event.Usage.CacheWriteTokens < 0 {
			return errors.New("validate provider event: token usage is negative")
		}
	default:
		return errors.New("validate provider event: event kind is unsupported")
	}
	return nil
}

type Streamer interface {
	Stream(context.Context, Request, func(Event) error) error
}

type StreamFunc func(context.Context, Request, func(Event) error) error

func (function StreamFunc) Stream(ctx context.Context, request Request, emit func(Event) error) error {
	return function(ctx, request, emit)
}

func validBoundedText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validJSONObject(value []byte) bool {
	if !json.Valid(value) {
		return false
	}
	var object map[string]json.RawMessage
	return json.Unmarshal(value, &object) == nil && object != nil
}

var _ Streamer = StreamFunc(nil)
