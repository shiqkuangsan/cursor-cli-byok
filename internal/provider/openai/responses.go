package openai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/provider"
)

var errResponsesDone = errors.New("responses stream done")

type responsesRequest struct {
	Model  string          `json:"model"`
	Input  []any           `json:"input"`
	Tools  []responsesTool `json:"tools,omitempty"`
	Stream bool            `json:"stream"`
}

type responsesMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responsesFunctionCall struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type responsesFunctionOutput struct {
	Type   string `json:"type"`
	CallID string `json:"call_id"`
	Output string `json:"output"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

func (client *Client) streamResponses(ctx context.Context, request provider.Request, emit func(provider.Event) error) error {
	response, err := client.doStreamRequest(ctx, buildResponsesRequest(request))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	terminal := false
	err = readSSE(ctx, response.Body, client.maxEventBytes, func(event sseEvent) error {
		if strings.TrimSpace(event.Data) == "[DONE]" {
			return errResponsesDone
		}
		var envelope responsesEvent
		if err := json.Unmarshal([]byte(event.Data), &envelope); err != nil {
			return errors.New("decode Responses SSE event")
		}
		eventType := envelope.Type
		if eventType == "" {
			eventType = event.Name
		}
		switch eventType {
		case "response.output_text.delta":
			return emitValidated(emit, provider.Event{Kind: provider.EventTextDelta, Text: envelope.Delta})
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			return emitValidated(emit, provider.Event{Kind: provider.EventReasoningDelta, Text: envelope.Delta})
		case "response.output_item.added":
			if envelope.Item.Type != "function_call" {
				return nil
			}
			callID := envelope.Item.CallID
			if callID == "" {
				callID = envelope.Item.ID
			}
			return emitValidated(emit, provider.Event{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{
				Index: envelope.OutputIndex, ID: callID, Name: envelope.Item.Name,
			}})
		case "response.function_call_arguments.delta":
			return emitValidated(emit, provider.Event{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{
				Index: envelope.OutputIndex, ArgumentsDelta: envelope.Delta,
			}})
		case "response.function_call_arguments.done", "response.output_item.done":
			if eventType == "response.output_item.done" && envelope.Item.Type != "function_call" {
				return nil
			}
			return emitValidated(emit, provider.Event{Kind: provider.EventToolCallDelta, ToolCall: provider.ToolCallDelta{
				Index: envelope.OutputIndex, Done: true,
			}})
		case "response.completed":
			terminal = true
			return emitValidated(emit, provider.Event{Kind: provider.EventUsage, Usage: provider.Usage{
				InputTokens:     envelope.Response.Usage.InputTokens,
				OutputTokens:    envelope.Response.Usage.OutputTokens,
				CacheReadTokens: envelope.Response.Usage.InputTokenDetails.CachedTokens,
			}})
		case "error", "response.failed", "response.incomplete":
			return provider.NewError("provider_failed", response.StatusCode, false, nil)
		default:
			return nil
		}
	})
	if errors.Is(err, errResponsesDone) {
		err = nil
	}
	if err != nil {
		return err
	}
	if !terminal {
		return errors.New("decode Responses stream: terminal event is missing")
	}
	return nil
}

func buildResponsesRequest(request provider.Request) responsesRequest {
	result := responsesRequest{Model: request.Model, Stream: true}
	for _, message := range request.Messages {
		switch message.Role {
		case provider.RoleSystem, provider.RoleUser:
			result.Input = append(result.Input, responsesMessage{Role: string(message.Role), Content: message.Content})
		case provider.RoleAssistant:
			if message.Content != "" {
				result.Input = append(result.Input, responsesMessage{Role: string(message.Role), Content: message.Content})
			}
			for _, call := range message.ToolCalls {
				result.Input = append(result.Input, responsesFunctionCall{Type: "function_call", CallID: call.ID, Name: call.Name, Arguments: call.Arguments})
			}
		case provider.RoleTool:
			result.Input = append(result.Input, responsesFunctionOutput{Type: "function_call_output", CallID: message.ToolCallID, Output: message.Content})
		}
	}
	for _, tool := range request.Tools {
		result.Tools = append(result.Tools, responsesTool{Type: "function", Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters})
	}
	return result
}

type responsesEvent struct {
	Type        string `json:"type"`
	Delta       string `json:"delta"`
	OutputIndex int    `json:"output_index"`
	Item        struct {
		ID     string `json:"id"`
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Name   string `json:"name"`
	} `json:"item"`
	Response struct {
		Usage struct {
			InputTokens       int64 `json:"input_tokens"`
			OutputTokens      int64 `json:"output_tokens"`
			InputTokenDetails struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
}
