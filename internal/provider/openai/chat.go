package openai

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/provider"
)

var errChatDone = errors.New("chat stream done")

type chatRequest struct {
	Model         string            `json:"model"`
	Messages      []chatMessage     `json:"messages"`
	Tools         []chatTool        `json:"tools,omitempty"`
	Stream        bool              `json:"stream"`
	StreamOptions chatStreamOptions `json:"stream_options"`
}

type chatStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string             `json:"type"`
	Function chatToolDefinition `json:"function"`
}

type chatToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

func (client *Client) streamChat(ctx context.Context, request provider.Request, emit func(provider.Event) error) error {
	response, err := client.doStreamRequest(ctx, buildChatRequest(request))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	terminal := false
	seenTools := make(map[int]struct{})
	doneTools := make(map[int]struct{})
	err = readSSE(ctx, response.Body, client.maxEventBytes, func(event sseEvent) error {
		if strings.TrimSpace(event.Data) == "[DONE]" {
			terminal = true
			return errChatDone
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(event.Data), &chunk); err != nil {
			return errors.New("decode Chat Completions SSE event")
		}
		if chunk.Error != nil {
			return provider.NewError("provider_failed", response.StatusCode, false, nil)
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				if err := emitValidated(emit, provider.Event{Kind: provider.EventTextDelta, Text: choice.Delta.Content}); err != nil {
					return err
				}
			}
			reasoning := choice.Delta.ReasoningContent
			if reasoning == "" {
				reasoning = choice.Delta.Reasoning
			}
			if reasoning != "" {
				if err := emitValidated(emit, provider.Event{Kind: provider.EventReasoningDelta, Text: reasoning}); err != nil {
					return err
				}
			}
			for _, call := range choice.Delta.ToolCalls {
				if call.Index < 0 {
					return errors.New("decode Chat Completions stream: tool call index is invalid")
				}
				seenTools[call.Index] = struct{}{}
				if err := emitValidated(emit, provider.Event{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{
					Index: call.Index, ID: call.ID, Name: call.Function.Name, ArgumentsDelta: call.Function.Arguments,
				}}); err != nil {
					return err
				}
			}
			if choice.FinishReason == "tool_calls" {
				indexes := make([]int, 0, len(seenTools))
				for index := range seenTools {
					indexes = append(indexes, index)
				}
				sort.Ints(indexes)
				for _, index := range indexes {
					if _, alreadyDone := doneTools[index]; alreadyDone {
						continue
					}
					if err := emitValidated(emit, provider.Event{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{Index: index, Done: true}}); err != nil {
						return err
					}
					doneTools[index] = struct{}{}
				}
			}
		}
		if chunk.Usage != nil {
			return emitValidated(emit, provider.Event{Kind: provider.EventUsage, Usage: provider.Usage{
				InputTokens:     chunk.Usage.PromptTokens,
				OutputTokens:    chunk.Usage.CompletionTokens,
				CacheReadTokens: chunk.Usage.PromptTokenDetails.CachedTokens,
			}})
		}
		return nil
	})
	if errors.Is(err, errChatDone) {
		err = nil
	}
	if err != nil {
		return err
	}
	if !terminal {
		return errors.New("decode Chat Completions stream: terminal event is missing")
	}
	return nil
}

func buildChatRequest(request provider.Request) chatRequest {
	result := chatRequest{
		Model:         request.Model,
		Stream:        true,
		StreamOptions: chatStreamOptions{IncludeUsage: true},
	}
	for _, message := range request.Messages {
		converted := chatMessage{Role: string(message.Role), Content: message.Content, ToolCallID: message.ToolCallID}
		for _, call := range message.ToolCalls {
			converted.ToolCalls = append(converted.ToolCalls, chatToolCall{
				ID: call.ID, Type: "function", Function: chatFunction{Name: call.Name, Arguments: call.Arguments},
			})
		}
		result.Messages = append(result.Messages, converted)
	}
	for _, tool := range request.Tools {
		result.Tools = append(result.Tools, chatTool{
			Type:     "function",
			Function: chatToolDefinition{Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters},
		})
	}
	return result
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
			Reasoning        string `json:"reasoning"`
			ToolCalls        []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens       int64 `json:"prompt_tokens"`
		CompletionTokens   int64 `json:"completion_tokens"`
		PromptTokenDetails struct {
			CachedTokens int64 `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
	Error *json.RawMessage `json:"error"`
}
