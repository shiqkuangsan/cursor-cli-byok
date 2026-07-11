package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	maxMCPResultItems = 64
	maxMCPResultBytes = 256 * 1024
)

type mcpToolArguments struct {
	Name      string          `json:"name"`
	Server    string          `json:"server"`
	ToolName  string          `json:"tool_name"`
	Arguments json.RawMessage `json:"arguments"`
}

func encodeMCPToolDispatch(request ToolRequest) (ToolDispatch, error) {
	arguments, err := decodeMCPToolArguments(request.Arguments)
	if err != nil {
		return ToolDispatch{}, err
	}
	mcpArgs, err := encodeMCPArgs(arguments, request.CallID)
	if err != nil {
		return ToolDispatch{}, err
	}
	execMessage := appendVarint(nil, 1, uint64(request.MessageID))
	execMessage = appendString(execMessage, 15, request.ExecID)
	execMessage = appendMessage(execMessage, 11, mcpArgs)
	mcpToolCall := appendMessage(nil, 1, mcpArgs)
	toolCall := appendMessage(nil, 15, mcpToolCall)
	started := appendString(nil, 1, request.CallID)
	started = appendMessage(started, 2, toolCall)
	interaction := appendMessage(nil, 2, started)
	return ToolDispatch{Execute: appendMessage(nil, 2, execMessage), Started: appendMessage(nil, 1, interaction)}, nil
}

func decodeMCPToolResult(execFields []wireField, result ToolResult) (ToolResult, error) {
	payload, ok := lastBytesField(execFields, 11)
	if !ok {
		return ToolResult{}, errors.New("decode tool result: MCP result is required")
	}
	content, isError, err := summarizeMCPResult(payload)
	if err != nil {
		return ToolResult{}, err
	}
	result.Complete = true
	result.IsError = isError
	result.Content = content
	result.ResultPayload = append([]byte(nil), payload...)
	return result, nil
}

func encodeMCPDisplayToolCall(request ToolRequest, result []byte) ([]byte, error) {
	arguments, err := decodeMCPToolArguments(request.Arguments)
	if err != nil {
		return nil, err
	}
	mcpArgs, err := encodeMCPArgs(arguments, request.CallID)
	if err != nil {
		return nil, err
	}
	mcpToolCall := appendMessage(nil, 1, mcpArgs)
	if result != nil {
		toolResult, convertError := convertMCPResultForDisplay(result)
		if convertError != nil {
			return nil, convertError
		}
		mcpToolCall = appendMessage(mcpToolCall, 2, toolResult)
	}
	return appendMessage(nil, 15, mcpToolCall), nil
}

func encodeMCPArgs(arguments mcpToolArguments, callID string) ([]byte, error) {
	values, err := decodeMCPArgumentObject(arguments.Arguments)
	if err != nil {
		return nil, err
	}
	payload := appendString(nil, 1, arguments.Name)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	budget := protoJSONBudget{}
	for _, key := range keys {
		value, encodeError := encodeProtoValue(values[key], 0, &budget)
		if encodeError != nil {
			return nil, encodeError
		}
		entry := appendString(nil, 1, key)
		entry = appendMessage(entry, 2, value)
		payload = appendMessage(payload, 2, entry)
	}
	payload = appendString(payload, 3, callID)
	payload = appendString(payload, 4, arguments.Server)
	payload = appendString(payload, 5, arguments.ToolName)
	return payload, nil
}

func decodeMCPToolArguments(raw string) (mcpToolArguments, error) {
	var arguments mcpToolArguments
	if err := decodeStrictToolArguments(raw, &arguments); err != nil {
		return mcpToolArguments{}, errors.New("encode MCP tool: arguments are invalid")
	}
	arguments.Name = strings.TrimSpace(arguments.Name)
	arguments.Server = strings.TrimSpace(arguments.Server)
	arguments.ToolName = strings.TrimSpace(arguments.ToolName)
	if !validIdentifier(arguments.Name) || !validIdentifier(arguments.Server) || !validIdentifier(arguments.ToolName) || len(arguments.Arguments) == 0 {
		return mcpToolArguments{}, errors.New("encode MCP tool: arguments are invalid")
	}
	if _, err := decodeMCPArgumentObject(arguments.Arguments); err != nil {
		return mcpToolArguments{}, err
	}
	return arguments, nil
}

func decodeMCPArgumentObject(raw []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var result map[string]any
	if err := decoder.Decode(&result); err != nil || result == nil {
		return nil, errors.New("encode MCP tool: arguments must be a JSON object")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, errors.New("encode MCP tool: arguments have trailing JSON")
	}
	return result, nil
}

func encodeProtoValue(value any, depth int, budget *protoJSONBudget) ([]byte, error) {
	if depth > maxProtoJSONDepth {
		return nil, errors.New("encode MCP tool: arguments exceed nesting limit")
	}
	budget.nodes++
	if budget.nodes > maxProtoJSONNodes {
		return nil, errors.New("encode MCP tool: arguments exceed node limit")
	}
	switch typed := value.(type) {
	case nil:
		return appendVarint(nil, 1, 0), nil
	case bool:
		if typed {
			return appendVarint(nil, 4, 1), nil
		}
		return appendVarint(nil, 4, 0), nil
	case string:
		if !utf8.ValidString(typed) {
			return nil, errors.New("encode MCP tool: argument string is invalid")
		}
		return appendString(nil, 3, typed), nil
	case json.Number:
		number, err := typed.Float64()
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, errors.New("encode MCP tool: argument number is invalid")
		}
		payload := protowire.AppendTag(nil, 2, protowire.Fixed64Type)
		return protowire.AppendFixed64(payload, math.Float64bits(number)), nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil, errors.New("encode MCP tool: argument number is invalid")
		}
		payload := protowire.AppendTag(nil, 2, protowire.Fixed64Type)
		return protowire.AppendFixed64(payload, math.Float64bits(typed)), nil
	case []any:
		var list []byte
		for _, item := range typed {
			encoded, err := encodeProtoValue(item, depth+1, budget)
			if err != nil {
				return nil, err
			}
			list = appendMessage(list, 1, encoded)
		}
		return appendMessage(nil, 6, list), nil
	case map[string]any:
		structure, err := encodeProtoStruct(typed, depth+1, budget)
		if err != nil {
			return nil, err
		}
		return appendMessage(nil, 5, structure), nil
	default:
		return nil, errors.New("encode MCP tool: argument type is unsupported")
	}
}

func encodeProtoStruct(value map[string]any, depth int, budget *protoJSONBudget) ([]byte, error) {
	keys := make([]string, 0, len(value))
	for key := range value {
		if !utf8.ValidString(key) {
			return nil, errors.New("encode MCP tool: argument key is invalid")
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var structure []byte
	for _, key := range keys {
		encoded, err := encodeProtoValue(value[key], depth, budget)
		if err != nil {
			return nil, err
		}
		entry := appendString(nil, 1, key)
		entry = appendMessage(entry, 2, encoded)
		structure = appendMessage(structure, 1, entry)
	}
	return structure, nil
}

func summarizeMCPResult(payload []byte) (string, bool, error) {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return "", false, errors.New("decode tool result: malformed MCP result")
	}
	if successPayload, found := lastBytesField(fields, 1); found {
		return summarizeMCPSuccess(successPayload)
	}
	for _, candidate := range []struct {
		field       protowire.Number
		prefix      string
		detailField protowire.Number
	}{
		{2, "MCP error", 1}, {3, "MCP rejected", 1}, {4, "MCP permission denied", 1},
		{5, "MCP tool not found", 1}, {6, "MCP server not found", 1}, {7, "MCP tool was approved but not executed", 0},
	} {
		failurePayload, found := lastBytesField(fields, candidate.field)
		if !found {
			continue
		}
		detail := candidate.prefix
		if candidate.detailField != 0 {
			failureFields, parseError := decodeWireFields(failurePayload)
			if parseError != nil {
				return "", false, errors.New("decode tool result: malformed MCP failure")
			}
			value, _ := lastBytesField(failureFields, candidate.detailField)
			if strings.TrimSpace(string(value)) != "" {
				detail += ": " + strings.TrimSpace(string(value))
			}
		}
		return detail, true, nil
	}
	return "", false, errors.New("decode tool result: MCP result variant is unsupported")
}

func summarizeMCPSuccess(payload []byte) (string, bool, error) {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return "", false, errors.New("decode tool result: malformed MCP success")
	}
	sections := make([]string, 0)
	totalBytes := 0
	truncated := false
	items := repeatedBytesField(fields, 1)
	if len(items) > maxMCPResultItems {
		items = items[:maxMCPResultItems]
		truncated = true
	}
	for _, itemPayload := range items {
		itemFields, parseError := decodeWireFields(itemPayload)
		if parseError != nil {
			return "", false, errors.New("decode tool result: malformed MCP content item")
		}
		section := ""
		if textPayload, found := lastBytesField(itemFields, 1); found {
			textFields, textError := decodeWireFields(textPayload)
			if textError != nil {
				return "", false, errors.New("decode tool result: malformed MCP text content")
			}
			text, _ := lastBytesField(textFields, 1)
			section = string(text)
		} else if imagePayload, found := lastBytesField(itemFields, 2); found {
			imageFields, imageError := decodeWireFields(imagePayload)
			if imageError != nil {
				return "", false, errors.New("decode tool result: malformed MCP image content")
			}
			data, _ := lastBytesField(imageFields, 1)
			mimeType, _ := lastBytesField(imageFields, 2)
			section = fmt.Sprintf("[image: %s, %d bytes]", fallbackString(string(mimeType), "application/octet-stream"), len(data))
		}
		section, didTruncate := boundedMCPSection(section, maxMCPResultBytes-totalBytes)
		truncated = truncated || didTruncate
		if section != "" {
			sections = append(sections, section)
			totalBytes += len(section)
		}
		if totalBytes >= maxMCPResultBytes {
			truncated = true
			break
		}
	}
	if structuredPayload, found := lastBytesField(fields, 3); found && totalBytes < maxMCPResultBytes {
		value, decodeError := decodeProtoStruct(structuredPayload, 0, &protoJSONBudget{})
		if decodeError != nil {
			return "", false, decodeError
		}
		encoded, encodeError := json.Marshal(value)
		if encodeError != nil {
			return "", false, errors.New("decode tool result: encode MCP structured content")
		}
		section, didTruncate := boundedMCPSection(string(encoded), maxMCPResultBytes-totalBytes)
		truncated = truncated || didTruncate
		if section != "" {
			sections = append(sections, section)
		}
	}
	if truncated {
		sections = append(sections, "[MCP result truncated by cursor-cli-byok]")
	}
	if len(sections) == 0 {
		sections = append(sections, "MCP tool completed without content")
	}
	isErrorValue, _ := lastVarintField(fields, 2)
	return strings.Join(sections, "\n\n"), isErrorValue != 0, nil
}

func convertMCPResultForDisplay(payload []byte) ([]byte, error) {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return nil, errors.New("encode tool completed: malformed MCP result")
	}
	if success, found := lastBytesField(fields, 1); found {
		return appendMessage(nil, 1, success), nil
	}
	if failure, found := lastBytesField(fields, 2); found {
		failureFields, parseError := decodeWireFields(failure)
		if parseError != nil {
			return nil, errors.New("encode tool completed: malformed MCP error")
		}
		detail, _ := lastBytesField(failureFields, 1)
		return appendMessage(nil, 2, appendString(nil, 1, fallbackString(string(detail), "MCP error"))), nil
	}
	if rejected, found := lastBytesField(fields, 3); found {
		return appendMessage(nil, 3, rejected), nil
	}
	if denied, found := lastBytesField(fields, 4); found {
		return appendMessage(nil, 4, denied), nil
	}
	content, _, summarizeError := summarizeMCPResult(payload)
	if summarizeError != nil {
		return nil, summarizeError
	}
	return appendMessage(nil, 2, appendString(nil, 1, content)), nil
}

func boundedMCPSection(value string, limit int) (string, bool) {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if limit <= 0 {
		return "", value != ""
	}
	if len(value) <= limit {
		return value, false
	}
	value = value[:limit]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value, true
}
