package protocol

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

type ReadToolRequest struct {
	MessageID uint32
	ExecID    string
	CallID    string
	Path      string
	Offset    *int32
	Limit     *uint32
}

type ToolDispatch struct {
	Execute []byte
	Started []byte
}

type ReadToolResult struct {
	MessageID uint32
	ExecID    string
	Path      string
	Content   string
	IsError   bool
	Error     string
}

func EncodeReadToolDispatch(request ReadToolRequest) (ToolDispatch, error) {
	if err := validateReadToolRequest(request); err != nil {
		return ToolDispatch{}, err
	}
	execArgs := appendString(nil, 1, request.Path)
	execArgs = appendString(execArgs, 2, request.CallID)
	if request.Offset != nil {
		execArgs = appendVarint(execArgs, 4, uint64(*request.Offset))
	}
	if request.Limit != nil {
		execArgs = appendVarint(execArgs, 5, uint64(*request.Limit))
	}
	execMessage := appendVarint(nil, 1, uint64(request.MessageID))
	execMessage = appendString(execMessage, 15, request.ExecID)
	execMessage = appendMessage(execMessage, 7, execArgs)

	toolCall := encodeReadToolCall(request, nil)
	started := appendString(nil, 1, request.CallID)
	started = appendMessage(started, 2, toolCall)
	interaction := appendMessage(nil, 2, started)
	return ToolDispatch{
		Execute: appendMessage(nil, 2, execMessage),
		Started: appendMessage(nil, 1, interaction),
	}, nil
}

func EncodeReadToolCompleted(request ReadToolRequest, result ReadToolResult) ([]byte, error) {
	if err := validateReadToolRequest(request); err != nil {
		return nil, err
	}
	toolCall := encodeReadToolCall(request, &result)
	completed := appendString(nil, 1, request.CallID)
	completed = appendMessage(completed, 2, toolCall)
	interaction := appendMessage(nil, 3, completed)
	return appendMessage(nil, 1, interaction), nil
}

func DecodeReadToolResult(message ClientMessage) (ReadToolResult, error) {
	if message.Kind != ClientMessageExec {
		return ReadToolResult{}, errors.New("decode read result: client message is not an exec result")
	}
	topFields, err := decodeWireFields(message.Raw)
	if err != nil {
		return ReadToolResult{}, fmt.Errorf("decode read result: %w", err)
	}
	execPayload, ok := lastBytesField(topFields, 2)
	if !ok {
		return ReadToolResult{}, errors.New("decode read result: exec message is required")
	}
	execFields, err := decodeWireFields(execPayload)
	if err != nil {
		return ReadToolResult{}, fmt.Errorf("decode read result: malformed exec message: %w", err)
	}
	messageID, _ := lastVarintField(execFields, 1)
	execIDBytes, _ := lastBytesField(execFields, 15)
	readPayload, ok := lastBytesField(execFields, 7)
	if !ok {
		return ReadToolResult{}, errors.New("decode read result: read result is required")
	}
	readFields, err := decodeWireFields(readPayload)
	if err != nil {
		return ReadToolResult{}, fmt.Errorf("decode read result: malformed read result: %w", err)
	}
	result := ReadToolResult{MessageID: uint32(messageID), ExecID: strings.TrimSpace(string(execIDBytes))}
	if successPayload, found := lastBytesField(readFields, 1); found {
		successFields, err := decodeWireFields(successPayload)
		if err != nil {
			return ReadToolResult{}, errors.New("decode read result: malformed success payload")
		}
		path, _ := lastBytesField(successFields, 1)
		content, hasContent := lastBytesField(successFields, 2)
		if !hasContent {
			content, _ = lastBytesField(successFields, 5)
		}
		result.Path = string(path)
		result.Content = string(content)
		return result, nil
	}
	for _, candidate := range []struct {
		field   protowire.Number
		message string
	}{
		{2, "read failed"}, {3, "read rejected"}, {4, "file not found"},
		{5, "permission denied"}, {6, "invalid file"},
	} {
		failurePayload, found := lastBytesField(readFields, candidate.field)
		if !found {
			continue
		}
		failureFields, parseError := decodeWireFields(failurePayload)
		if parseError != nil {
			return ReadToolResult{}, errors.New("decode read result: malformed failure payload")
		}
		path, _ := lastBytesField(failureFields, 1)
		detail, _ := lastBytesField(failureFields, 2)
		result.Path = string(path)
		result.IsError = true
		result.Error = strings.TrimSpace(string(detail))
		if result.Error == "" {
			result.Error = candidate.message
		}
		return result, nil
	}
	return ReadToolResult{}, errors.New("decode read result: result variant is unsupported")
}

func validateReadToolRequest(request ReadToolRequest) error {
	if request.MessageID == 0 {
		return errors.New("encode read tool: message ID is required")
	}
	if !validIdentifier(strings.TrimSpace(request.ExecID)) || !validIdentifier(strings.TrimSpace(request.CallID)) {
		return errors.New("encode read tool: exec ID and call ID are required")
	}
	if request.Path == "" || strings.IndexByte(request.Path, 0) >= 0 {
		return errors.New("encode read tool: path is required")
	}
	if request.Offset != nil && *request.Offset < 0 {
		return errors.New("encode read tool: offset must not be negative")
	}
	if request.Limit != nil && *request.Limit == 0 {
		return errors.New("encode read tool: limit must be positive")
	}
	return nil
}

func encodeReadToolCall(request ReadToolRequest, result *ReadToolResult) []byte {
	readArgs := appendString(nil, 1, request.Path)
	if request.Offset != nil {
		readArgs = appendVarint(readArgs, 2, uint64(*request.Offset))
	}
	if request.Limit != nil {
		readArgs = appendVarint(readArgs, 3, uint64(*request.Limit))
	}
	readToolCall := appendMessage(nil, 1, readArgs)
	if result != nil {
		var readResult []byte
		if result.IsError {
			failure := appendString(nil, 1, result.Error)
			readResult = appendMessage(nil, 2, failure)
		} else {
			success := appendString(nil, 1, result.Content)
			path := result.Path
			if path == "" {
				path = request.Path
			}
			success = appendString(success, 7, path)
			readResult = appendMessage(nil, 1, success)
		}
		readToolCall = appendMessage(readToolCall, 2, readResult)
	}
	return appendMessage(nil, 8, readToolCall)
}
