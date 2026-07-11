package agent

import (
	"context"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/protocol"
)

type EventKind uint8

const (
	EventUnknown EventKind = iota
	EventTextDelta
	EventReasoningDelta
	EventToolCall
	EventUsage
)

type Event struct {
	Kind   EventKind
	Text   string
	Usage  protocol.TokenUsage
	Tool   ToolCall
	Result chan<- ToolResult
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

type Executor interface {
	Execute(context.Context, protocol.RunRequest, func(Event) error) error
}

type ExecutorFunc func(context.Context, protocol.RunRequest, func(Event) error) error

func (function ExecutorFunc) Execute(ctx context.Context, request protocol.RunRequest, emit func(Event) error) error {
	return function(ctx, request, emit)
}
