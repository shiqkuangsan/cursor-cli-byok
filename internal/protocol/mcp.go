package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

const (
	maxMCPTools       = 256
	maxProtoJSONDepth = 64
	maxProtoJSONNodes = 10000
)

func (request RunRequest) Equal(other RunRequest) bool {
	if request.ConversationID != other.ConversationID || request.ModelID != other.ModelID || request.UserText != other.UserText || request.UserMessageID != other.UserMessageID || request.MCPToolsPresent != other.MCPToolsPresent || len(request.MCPTools) != len(other.MCPTools) {
		return false
	}
	for index, tool := range request.MCPTools {
		candidate := other.MCPTools[index]
		if tool.Name != candidate.Name || tool.Description != candidate.Description || tool.ProviderIdentifier != candidate.ProviderIdentifier || tool.ToolName != candidate.ToolName || !bytes.Equal(tool.InputSchema, candidate.InputSchema) {
			return false
		}
	}
	return true
}

type MCPStateResult struct {
	MessageID uint32
	ExecID    string
	Tools     []MCPToolDefinition
	IsError   bool
	Error     string
}

func EncodeMCPStateRequest(messageID uint32, execID string) ([]byte, error) {
	if messageID == 0 || !validIdentifier(strings.TrimSpace(execID)) {
		return nil, errors.New("encode MCP state request: message ID and exec ID are required")
	}
	exec := appendVarint(nil, 1, uint64(messageID))
	exec = appendString(exec, 15, execID)
	exec = appendMessage(exec, 36, nil)
	return appendMessage(nil, 2, exec), nil
}

func DecodeMCPStateResult(message ClientMessage, expectedMessageID uint32, expectedExecID string) (MCPStateResult, error) {
	if message.Kind != ClientMessageExec {
		return MCPStateResult{}, errors.New("decode MCP state result: client message is not an exec result")
	}
	identity, err := DecodeExecResultIdentity(message)
	if err != nil {
		return MCPStateResult{}, err
	}
	if (identity.MessageID != 0 && identity.MessageID != expectedMessageID) || (identity.ExecID != "" && identity.ExecID != expectedExecID) {
		return MCPStateResult{}, errors.New("decode MCP state result: identity does not match request")
	}
	execPayload, err := execClientPayload(message.Raw)
	if err != nil {
		return MCPStateResult{}, err
	}
	execFields, err := decodeWireFields(execPayload)
	if err != nil {
		return MCPStateResult{}, errors.New("decode MCP state result: malformed exec message")
	}
	payload, ok := lastBytesField(execFields, 36)
	if !ok {
		return MCPStateResult{}, errors.New("decode MCP state result: result is required")
	}
	fields, err := decodeWireFields(payload)
	if err != nil {
		return MCPStateResult{}, errors.New("decode MCP state result: malformed result")
	}
	result := MCPStateResult{MessageID: expectedMessageID, ExecID: expectedExecID}
	if successPayload, found := lastBytesField(fields, 1); found {
		tools, decodeError := decodeMCPStateSuccess(successPayload)
		if decodeError != nil {
			return MCPStateResult{}, decodeError
		}
		result.Tools = tools
		return result, nil
	}
	for _, candidate := range []struct {
		field    protowire.Number
		fallback string
	}{
		{2, "MCP state unavailable"}, {3, "MCP state request rejected"},
	} {
		failurePayload, found := lastBytesField(fields, candidate.field)
		if !found {
			continue
		}
		failureFields, parseError := decodeWireFields(failurePayload)
		if parseError != nil {
			return MCPStateResult{}, errors.New("decode MCP state result: malformed failure")
		}
		detail, _ := lastBytesField(failureFields, 1)
		result.IsError = true
		result.Error = fallbackString(string(detail), candidate.fallback)
		return result, nil
	}
	return MCPStateResult{}, errors.New("decode MCP state result: variant is unsupported")
}

func decodeMCPStateSuccess(payload []byte) ([]MCPToolDefinition, error) {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return nil, errors.New("decode MCP state result: malformed success")
	}
	result := make([]MCPToolDefinition, 0)
	seen := make(map[string]struct{})
	for _, serverPayload := range repeatedBytesField(fields, 1) {
		serverFields, parseError := decodeWireFields(serverPayload)
		if parseError != nil {
			return nil, errors.New("decode MCP state result: malformed server")
		}
		serverIdentifierBytes, _ := lastBytesField(serverFields, 2)
		serverIdentifier := strings.TrimSpace(string(serverIdentifierBytes))
		if !validIdentifier(serverIdentifier) {
			return nil, errors.New("decode MCP state result: server identifier is invalid")
		}
		for _, definitionPayload := range repeatedBytesField(serverFields, 5) {
			definition, decodeError := decodeMCPStateToolDefinition(definitionPayload, serverIdentifier)
			if decodeError != nil {
				return nil, decodeError
			}
			if len(result) >= maxMCPTools {
				return nil, errors.New("decode MCP state result: too many tools")
			}
			if _, duplicate := seen[definition.Name]; duplicate {
				return nil, errors.New("decode MCP state result: duplicate tool names")
			}
			seen[definition.Name] = struct{}{}
			result = append(result, definition)
		}
	}
	return result, nil
}

func decodeMCPStateToolDefinition(payload []byte, fallbackProvider string) (MCPToolDefinition, error) {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return MCPToolDefinition{}, errors.New("decode MCP state result: malformed tool")
	}
	nameBytes, _ := lastBytesField(fields, 1)
	descriptionBytes, _ := lastBytesField(fields, 2)
	schemaPayload, hasSchema := lastBytesField(fields, 3)
	providerBytes, _ := lastBytesField(fields, 4)
	toolNameBytes, _ := lastBytesField(fields, 5)
	providerIdentifier := strings.TrimSpace(string(providerBytes))
	if providerIdentifier == "" {
		providerIdentifier = fallbackProvider
	}
	toolName := strings.TrimSpace(string(toolNameBytes))
	name := strings.TrimSpace(string(nameBytes))
	if name == "" && providerIdentifier != "" && toolName != "" {
		name = providerIdentifier + "-" + toolName
	}
	if !validIdentifier(name) || !validIdentifier(providerIdentifier) || !validIdentifier(toolName) || !utf8.Valid(descriptionBytes) || !hasSchema {
		return MCPToolDefinition{}, errors.New("decode MCP state result: tool metadata is invalid")
	}
	inputSchema, err := decodeMCPInputSchema(schemaPayload)
	if err != nil {
		return MCPToolDefinition{}, err
	}
	return MCPToolDefinition{Name: name, Description: string(descriptionBytes), ProviderIdentifier: providerIdentifier, ToolName: toolName, InputSchema: inputSchema}, nil
}

func decodeMCPTools(runFields []wireField) ([]MCPToolDefinition, error) {
	result := make([]MCPToolDefinition, 0)
	seen := make(map[string]struct{})
	appendDefinition := func(definition MCPToolDefinition) error {
		if len(result) >= maxMCPTools {
			return errors.New("run request has too many MCP tools")
		}
		if _, duplicate := seen[definition.Name]; duplicate {
			return errors.New("run request has duplicate MCP tool names")
		}
		seen[definition.Name] = struct{}{}
		result = append(result, definition)
		return nil
	}
	if payload, ok := lastBytesField(runFields, 4); ok {
		fields, err := decodeWireFields(payload)
		if err != nil {
			return nil, errors.New("run request MCP tools are malformed")
		}
		for _, definitionPayload := range repeatedBytesField(fields, 1) {
			definition, decodeError := decodeMCPToolDefinition(definitionPayload)
			if decodeError != nil {
				return nil, decodeError
			}
			if err := appendDefinition(definition); err != nil {
				return nil, err
			}
		}
	}
	if payload, ok := lastBytesField(runFields, 6); ok {
		fields, err := decodeWireFields(payload)
		if err != nil {
			return nil, errors.New("run request MCP filesystem options are malformed")
		}
		enabled, _ := lastVarintField(fields, 1)
		if enabled != 0 {
			for _, descriptorPayload := range repeatedBytesField(fields, 3) {
				definitions, decodeError := decodeMCPDescriptor(descriptorPayload)
				if decodeError != nil {
					return nil, decodeError
				}
				for _, definition := range definitions {
					if err := appendDefinition(definition); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return result, nil
}

func decodeMCPToolDefinition(payload []byte) (MCPToolDefinition, error) {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return MCPToolDefinition{}, errors.New("run request MCP tool is malformed")
	}
	name, _ := lastBytesField(fields, 1)
	description, _ := lastBytesField(fields, 2)
	schemaPayload, hasSchema := lastBytesField(fields, 3)
	providerIdentifier, _ := lastBytesField(fields, 4)
	toolName, _ := lastBytesField(fields, 5)
	definition := MCPToolDefinition{
		Name:               strings.TrimSpace(string(name)),
		Description:        string(description),
		ProviderIdentifier: strings.TrimSpace(string(providerIdentifier)),
		ToolName:           strings.TrimSpace(string(toolName)),
	}
	if !validIdentifier(definition.Name) || !validIdentifier(definition.ProviderIdentifier) || !validIdentifier(definition.ToolName) || !utf8.ValidString(definition.Description) {
		return MCPToolDefinition{}, errors.New("run request MCP tool metadata is invalid")
	}
	if !hasSchema {
		return MCPToolDefinition{}, errors.New("run request MCP tool schema is required")
	}
	encoded, err := decodeMCPInputSchema(schemaPayload)
	if err != nil {
		return MCPToolDefinition{}, err
	}
	definition.InputSchema = encoded
	return definition, nil
}

func decodeMCPDescriptor(payload []byte) ([]MCPToolDefinition, error) {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return nil, errors.New("run request MCP descriptor is malformed")
	}
	providerIdentifierBytes, _ := lastBytesField(fields, 2)
	providerIdentifier := strings.TrimSpace(string(providerIdentifierBytes))
	if !validIdentifier(providerIdentifier) {
		return nil, errors.New("run request MCP descriptor identifier is invalid")
	}
	toolPayloads := repeatedBytesField(fields, 5)
	result := make([]MCPToolDefinition, 0, len(toolPayloads))
	for _, toolPayload := range toolPayloads {
		toolFields, parseError := decodeWireFields(toolPayload)
		if parseError != nil {
			return nil, errors.New("run request MCP tool descriptor is malformed")
		}
		toolNameBytes, _ := lastBytesField(toolFields, 1)
		descriptionBytes, _ := lastBytesField(toolFields, 3)
		schemaPayload, hasSchema := lastBytesField(toolFields, 4)
		toolName := strings.TrimSpace(string(toolNameBytes))
		if !validIdentifier(toolName) || !utf8.Valid(descriptionBytes) || !hasSchema {
			return nil, errors.New("run request MCP tool descriptor is invalid")
		}
		inputSchema, decodeError := decodeMCPInputSchema(schemaPayload)
		if decodeError != nil {
			return nil, decodeError
		}
		result = append(result, MCPToolDefinition{
			Name:               providerIdentifier + "-" + toolName,
			Description:        string(descriptionBytes),
			ProviderIdentifier: providerIdentifier,
			ToolName:           toolName,
			InputSchema:        inputSchema,
		})
	}
	return result, nil
}

func decodeMCPInputSchema(schemaPayload []byte) ([]byte, error) {
	budget := protoJSONBudget{}
	value, err := decodeProtoValue(schemaPayload, 0, &budget)
	if err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, errors.New("run request MCP tool schema must be an object")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("run request MCP tool schema cannot be encoded")
	}
	return encoded, nil
}

type protoJSONBudget struct {
	nodes int
}

func decodeProtoValue(payload []byte, depth int, budget *protoJSONBudget) (any, error) {
	if depth > maxProtoJSONDepth {
		return nil, errors.New("run request MCP schema exceeds nesting limit")
	}
	budget.nodes++
	if budget.nodes > maxProtoJSONNodes {
		return nil, errors.New("run request MCP schema exceeds node limit")
	}
	fields, err := decodeWireFields(payload)
	if err != nil {
		return nil, errors.New("run request MCP schema value is malformed")
	}
	kinds := 0
	var value any
	if raw, found := lastVarintField(fields, 1); found {
		kinds++
		if raw != 0 {
			return nil, errors.New("run request MCP schema null value is invalid")
		}
		value = nil
	}
	if raw, found := lastFixed64Field(fields, 2); found {
		kinds++
		number := math.Float64frombits(raw)
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, errors.New("run request MCP schema number is invalid")
		}
		value = number
	}
	if raw, found := lastBytesField(fields, 3); found {
		kinds++
		if !utf8.Valid(raw) {
			return nil, errors.New("run request MCP schema string is invalid")
		}
		value = string(raw)
	}
	if raw, found := lastVarintField(fields, 4); found {
		kinds++
		if raw > 1 {
			return nil, errors.New("run request MCP schema boolean is invalid")
		}
		value = raw == 1
	}
	if raw, found := lastBytesField(fields, 5); found {
		kinds++
		value, err = decodeProtoStruct(raw, depth+1, budget)
		if err != nil {
			return nil, err
		}
	}
	if raw, found := lastBytesField(fields, 6); found {
		kinds++
		value, err = decodeProtoList(raw, depth+1, budget)
		if err != nil {
			return nil, err
		}
	}
	if kinds != 1 {
		return nil, errors.New("run request MCP schema value kind is invalid")
	}
	return value, nil
}

func decodeProtoStruct(payload []byte, depth int, budget *protoJSONBudget) (map[string]any, error) {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return nil, errors.New("run request MCP schema object is malformed")
	}
	result := make(map[string]any)
	for _, entryPayload := range repeatedBytesField(fields, 1) {
		entryFields, parseError := decodeWireFields(entryPayload)
		if parseError != nil {
			return nil, errors.New("run request MCP schema object entry is malformed")
		}
		keyBytes, hasKey := lastBytesField(entryFields, 1)
		valuePayload, hasValue := lastBytesField(entryFields, 2)
		if !hasKey || !hasValue || !utf8.Valid(keyBytes) {
			return nil, errors.New("run request MCP schema object entry is invalid")
		}
		key := string(keyBytes)
		if _, duplicate := result[key]; duplicate {
			return nil, errors.New("run request MCP schema object has duplicate keys")
		}
		value, decodeError := decodeProtoValue(valuePayload, depth, budget)
		if decodeError != nil {
			return nil, decodeError
		}
		result[key] = value
	}
	return result, nil
}

func decodeProtoList(payload []byte, depth int, budget *protoJSONBudget) ([]any, error) {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return nil, errors.New("run request MCP schema list is malformed")
	}
	values := repeatedBytesField(fields, 1)
	result := make([]any, 0, len(values))
	for _, valuePayload := range values {
		value, decodeError := decodeProtoValue(valuePayload, depth, budget)
		if decodeError != nil {
			return nil, decodeError
		}
		result = append(result, value)
	}
	return result, nil
}

func lastFixed64Field(fields []wireField, number protowire.Number) (uint64, bool) {
	for index := len(fields) - 1; index >= 0; index-- {
		if fields[index].number == number && fields[index].typeID == protowire.Fixed64Type {
			return fields[index].fixed64, true
		}
	}
	return 0, false
}
