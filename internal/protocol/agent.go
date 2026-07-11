package protocol

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"google.golang.org/protobuf/encoding/protowire"
)

type ClientMessageKind uint8

const (
	ClientMessageUnknown ClientMessageKind = iota
	ClientMessageRun
	ClientMessageExec
	ClientMessageKV
	ClientMessageConversationAction
	ClientMessageExecControl
	ClientMessageInteractionResponse
	ClientMessageHeartbeat
	ClientMessagePrewarm
)

type BidiAppendRequest struct {
	RequestID      string
	AppendSequence uint64
	Message        ClientMessage
}

type ClientMessage struct {
	Kind ClientMessageKind
	Run  *RunRequest
	Raw  []byte
}

type RunRequest struct {
	ConversationID  string
	ModelID         string
	UserText        string
	UserMessageID   string
	MetadataOnly    bool
	MCPToolsPresent bool
	MCPTools        []MCPToolDefinition
}

type MCPToolDefinition struct {
	Name               string
	Description        string
	ProviderIdentifier string
	ToolName           string
	InputSchema        []byte
}

type TokenUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
}

func DecodeBidiAppendRequest(payload []byte) (BidiAppendRequest, error) {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return BidiAppendRequest{}, fmt.Errorf("decode bidi append: malformed protobuf: %w", err)
	}
	requestIDMessage, ok := lastBytesField(fields, 2)
	if !ok {
		return BidiAppendRequest{}, errors.New("decode bidi append: request_id is required")
	}
	requestID, err := DecodeBidiRequestID(requestIDMessage)
	if err != nil {
		return BidiAppendRequest{}, fmt.Errorf("decode bidi append: %w", err)
	}

	clientPayload, hasBinary := lastBytesField(fields, 4)
	if !hasBinary {
		legacyHex, hasLegacy := lastBytesField(fields, 1)
		if !hasLegacy || len(legacyHex) == 0 {
			return BidiAppendRequest{}, errors.New("decode bidi append: client data is required")
		}
		clientPayload, err = hex.DecodeString(string(legacyHex))
		if err != nil {
			return BidiAppendRequest{}, errors.New("decode bidi append: client data is not valid hex")
		}
	}
	if len(clientPayload) == 0 {
		return BidiAppendRequest{}, errors.New("decode bidi append: client data is required")
	}
	message, err := decodeClientMessage(clientPayload)
	if err != nil {
		return BidiAppendRequest{}, fmt.Errorf("decode bidi append: %w", err)
	}
	sequence, _ := lastVarintField(fields, 3)
	return BidiAppendRequest{
		RequestID:      requestID,
		AppendSequence: sequence,
		Message:        message,
	}, nil
}

func DecodeAgentClientMessage(payload []byte) (ClientMessage, error) {
	return decodeClientMessage(payload)
}

func DecodeBidiRequestID(payload []byte) (string, error) {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return "", fmt.Errorf("request_id is malformed: %w", err)
	}
	value, ok := lastBytesField(fields, 1)
	if !ok {
		return "", errors.New("request_id is required")
	}
	requestID := strings.TrimSpace(string(value))
	if !validIdentifier(requestID) {
		return "", errors.New("request_id is invalid")
	}
	return requestID, nil
}

func EncodeTextDelta(text string) ([]byte, error) {
	if text == "" {
		return nil, errors.New("encode text delta: text is required")
	}
	update := appendString(nil, 1, text)
	interaction := appendMessage(nil, 1, update)
	return appendMessage(nil, 1, interaction), nil
}

func EncodeThinkingDelta(text string) ([]byte, error) {
	if text == "" {
		return nil, errors.New("encode thinking delta: text is required")
	}
	update := appendString(nil, 1, text)
	interaction := appendMessage(nil, 4, update)
	return appendMessage(nil, 1, interaction), nil
}

func EncodeHeartbeat() ([]byte, error) {
	interaction := appendMessage(nil, 13, nil)
	return appendMessage(nil, 1, interaction), nil
}

func EncodeTurnEnded(usage TokenUsage) ([]byte, error) {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CacheReadTokens < 0 || usage.CacheWriteTokens < 0 {
		return nil, errors.New("encode turn ended: token counts must not be negative")
	}
	ended := appendVarint(nil, 1, uint64(usage.InputTokens))
	ended = appendVarint(ended, 2, uint64(usage.OutputTokens))
	ended = appendVarint(ended, 3, uint64(usage.CacheReadTokens))
	ended = appendVarint(ended, 4, uint64(usage.CacheWriteTokens))
	interaction := appendMessage(nil, 14, ended)
	return appendMessage(nil, 1, interaction), nil
}

func decodeClientMessage(payload []byte) (ClientMessage, error) {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return ClientMessage{}, fmt.Errorf("client message is malformed: %w", err)
	}
	message := ClientMessage{Raw: append([]byte(nil), payload...)}
	for _, candidate := range []struct {
		field protowire.Number
		kind  ClientMessageKind
	}{
		{1, ClientMessageRun},
		{2, ClientMessageExec},
		{3, ClientMessageKV},
		{4, ClientMessageConversationAction},
		{5, ClientMessageExecControl},
		{6, ClientMessageInteractionResponse},
		{7, ClientMessageHeartbeat},
		{8, ClientMessagePrewarm},
	} {
		value, ok := lastBytesField(fields, candidate.field)
		if !ok {
			continue
		}
		message.Kind = candidate.kind
		if candidate.kind == ClientMessageRun {
			run, err := decodeRunRequest(value)
			if err != nil {
				return ClientMessage{}, err
			}
			message.Run = &run
		}
		return message, nil
	}
	return ClientMessage{}, errors.New("client message type is unsupported")
}

func decodeRunRequest(payload []byte) (RunRequest, error) {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return RunRequest{}, fmt.Errorf("run request is malformed: %w", err)
	}
	conversationIDBytes, ok := lastBytesField(fields, 5)
	if !ok || !validIdentifier(strings.TrimSpace(string(conversationIDBytes))) {
		return RunRequest{}, errors.New("run request conversation_id is required")
	}
	conversationID := strings.TrimSpace(string(conversationIDBytes))

	modelMessage, ok := lastBytesField(fields, 9)
	if !ok {
		modelMessage, ok = lastBytesField(fields, 3)
	}
	if !ok {
		return RunRequest{}, errors.New("run request model is required")
	}
	modelFields, err := decodeWireFields(modelMessage)
	if err != nil {
		return RunRequest{}, fmt.Errorf("run request model is malformed: %w", err)
	}
	modelBytes, ok := lastBytesField(modelFields, 1)
	modelID := strings.TrimSpace(string(modelBytes))
	if !ok || !validIdentifier(modelID) {
		return RunRequest{}, errors.New("run request model is required")
	}

	actionMessage, ok := lastBytesField(fields, 2)
	if !ok {
		return RunRequest{}, errors.New("run request user message is required")
	}
	actionFields, err := decodeWireFields(actionMessage)
	if err != nil {
		return RunRequest{}, fmt.Errorf("run request action is malformed: %w", err)
	}
	userActionMessage, hasUserAction := lastBytesField(actionFields, 1)
	backgroundCompletion, hasBackgroundCompletion := lastBytesField(actionFields, 12)
	if hasUserAction && hasBackgroundCompletion {
		return RunRequest{}, errors.New("run request action is ambiguous")
	}
	if hasBackgroundCompletion {
		if err := validateBackgroundTaskCompletionAction(backgroundCompletion); err != nil {
			return RunRequest{}, err
		}
		mcpTools, err := decodeMCPTools(fields)
		if err != nil {
			return RunRequest{}, err
		}
		return RunRequest{
			ConversationID:  conversationID,
			ModelID:         modelID,
			MetadataOnly:    true,
			MCPToolsPresent: hasBytesField(fields, 4),
			MCPTools:        mcpTools,
		}, nil
	}
	if !hasUserAction {
		return RunRequest{}, errors.New("run request user message is required")
	}
	userActionFields, err := decodeWireFields(userActionMessage)
	if err != nil {
		return RunRequest{}, fmt.Errorf("run request user action is malformed: %w", err)
	}
	userMessage, ok := lastBytesField(userActionFields, 1)
	if !ok {
		return RunRequest{}, errors.New("run request user message is required")
	}
	userFields, err := decodeWireFields(userMessage)
	if err != nil {
		return RunRequest{}, fmt.Errorf("run request user message is malformed: %w", err)
	}
	textBytes, ok := lastBytesField(userFields, 1)
	if !ok || strings.TrimSpace(string(textBytes)) == "" {
		return RunRequest{}, errors.New("run request user message text is required")
	}
	messageIDBytes, _ := lastBytesField(userFields, 2)
	mcpTools, err := decodeMCPTools(fields)
	if err != nil {
		return RunRequest{}, err
	}
	return RunRequest{
		ConversationID:  conversationID,
		ModelID:         modelID,
		UserText:        string(textBytes),
		UserMessageID:   strings.TrimSpace(string(messageIDBytes)),
		MCPToolsPresent: hasBytesField(fields, 4),
		MCPTools:        mcpTools,
	}, nil
}

func validateBackgroundTaskCompletionAction(payload []byte) error {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return fmt.Errorf("run request background completion is malformed: %w", err)
	}
	completion, ok := lastBytesField(fields, 1)
	if !ok {
		return errors.New("run request background completion is required")
	}
	completionFields, err := decodeWireFields(completion)
	if err != nil {
		return fmt.Errorf("run request background completion is malformed: %w", err)
	}
	taskID, ok := lastBytesField(completionFields, 1)
	if !ok || !validIdentifier(strings.TrimSpace(string(taskID))) {
		return errors.New("run request background completion task ID is required")
	}
	return nil
}

type wireField struct {
	number  protowire.Number
	typeID  protowire.Type
	bytes   []byte
	varint  uint64
	fixed64 uint64
	fixed32 uint32
}

func decodeWireFields(payload []byte) ([]wireField, error) {
	fields := make([]wireField, 0, 8)
	for len(payload) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(payload)
		if tagLength < 0 {
			return nil, protowire.ParseError(tagLength)
		}
		payload = payload[tagLength:]
		field := wireField{number: number, typeID: wireType}
		switch wireType {
		case protowire.BytesType:
			value, valueLength := protowire.ConsumeBytes(payload)
			if valueLength < 0 {
				return nil, protowire.ParseError(valueLength)
			}
			field.bytes = value
			payload = payload[valueLength:]
		case protowire.VarintType:
			value, valueLength := protowire.ConsumeVarint(payload)
			if valueLength < 0 {
				return nil, protowire.ParseError(valueLength)
			}
			field.varint = value
			payload = payload[valueLength:]
		case protowire.Fixed64Type:
			value, valueLength := protowire.ConsumeFixed64(payload)
			if valueLength < 0 {
				return nil, protowire.ParseError(valueLength)
			}
			field.fixed64 = value
			payload = payload[valueLength:]
		case protowire.Fixed32Type:
			value, valueLength := protowire.ConsumeFixed32(payload)
			if valueLength < 0 {
				return nil, protowire.ParseError(valueLength)
			}
			field.fixed32 = value
			payload = payload[valueLength:]
		default:
			valueLength := protowire.ConsumeFieldValue(number, wireType, payload)
			if valueLength < 0 {
				return nil, protowire.ParseError(valueLength)
			}
			payload = payload[valueLength:]
		}
		fields = append(fields, field)
	}
	return fields, nil
}

func lastBytesField(fields []wireField, number protowire.Number) ([]byte, bool) {
	for index := len(fields) - 1; index >= 0; index-- {
		if fields[index].number == number && fields[index].typeID == protowire.BytesType {
			return fields[index].bytes, true
		}
	}
	return nil, false
}

func lastVarintField(fields []wireField, number protowire.Number) (uint64, bool) {
	for index := len(fields) - 1; index >= 0; index-- {
		if fields[index].number == number && fields[index].typeID == protowire.VarintType {
			return fields[index].varint, true
		}
	}
	return 0, false
}

func hasBytesField(fields []wireField, number protowire.Number) bool {
	_, ok := lastBytesField(fields, number)
	return ok
}

func validIdentifier(value string) bool {
	return value != "" && len(value) <= 1024 && strings.IndexFunc(value, unicode.IsControl) < 0
}
