package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

type ToolRequest struct {
	MessageID uint32
	ExecID    string
	CallID    string
	Name      string
	Arguments string
}

type ToolResult struct {
	MessageID     uint32
	ExecID        string
	Name          string
	Content       string
	IsError       bool
	Complete      bool
	ResultPayload []byte
	ShellEvent    ShellEventKind
	StdoutDelta   string
	StderrDelta   string
	Stdout        string
	Stderr        string
	ExitCode      int32
	CWD           string
	ShellID       uint32
	PID           uint32
	Truncated     bool
	EventPayload  []byte
}

type ExecResultIdentity struct {
	MessageID uint32
	ExecID    string
}

type writeToolArguments struct {
	Path     string `json:"path"`
	Contents string `json:"contents"`
}

type writeSuccessContent struct {
	Path         string `json:"path"`
	LinesCreated int64  `json:"lines_created"`
	FileSize     int64  `json:"file_size"`
}

type writeErrorContent struct {
	Path     string `json:"path,omitempty"`
	Error    string `json:"error"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

func EncodeToolDispatch(request ToolRequest) (ToolDispatch, error) {
	if err := validateToolRequest(request); err != nil {
		return ToolDispatch{}, err
	}
	switch request.Name {
	case "Write":
		return encodeWriteToolDispatch(request)
	case "Delete":
		return encodeDeleteToolDispatch(request)
	case "List":
		return encodeListToolDispatch(request)
	case "Grep":
		return encodeGrepToolDispatch(request)
	case "Glob":
		return encodeGlobToolDispatch(request)
	case "Shell":
		return encodeShellToolDispatch(request)
	case "CallMcpTool":
		return encodeMCPToolDispatch(request)
	default:
		return ToolDispatch{}, errors.New("encode tool dispatch: tool is unsupported")
	}
}

func DecodeToolResult(message ClientMessage, request ToolRequest) (ToolResult, error) {
	if err := validateToolRequest(request); err != nil {
		return ToolResult{}, err
	}
	if message.Kind != ClientMessageExec {
		return ToolResult{}, errors.New("decode tool result: client message is not an exec result")
	}
	execPayload, err := execClientPayload(message.Raw)
	if err != nil {
		return ToolResult{}, err
	}
	execFields, err := decodeWireFields(execPayload)
	if err != nil {
		return ToolResult{}, errors.New("decode tool result: malformed exec message")
	}
	messageID, _ := lastVarintField(execFields, 1)
	execIDBytes, _ := lastBytesField(execFields, 15)
	result := ToolResult{
		MessageID: uint32(messageID),
		ExecID:    strings.TrimSpace(string(execIDBytes)),
		Name:      request.Name,
	}
	if (result.MessageID != 0 && result.MessageID != request.MessageID) || (result.ExecID != "" && result.ExecID != request.ExecID) || (result.MessageID == 0 && result.ExecID == "") {
		return ToolResult{}, errors.New("decode tool result: result identity does not match request")
	}
	result.MessageID = request.MessageID
	result.ExecID = request.ExecID
	switch request.Name {
	case "Write":
		return decodeWriteToolResult(execFields, result)
	case "Delete":
		return decodeDeleteToolResult(execFields, result)
	case "List":
		return decodeListToolResult(execFields, request, result)
	case "Grep":
		return decodeGrepToolResult(execFields, result)
	case "Glob":
		return decodeGlobToolResult(execFields, request, result)
	case "Shell":
		return decodeShellToolResult(execFields, result)
	case "CallMcpTool":
		return decodeMCPToolResult(execFields, result)
	default:
		return ToolResult{}, errors.New("decode tool result: tool is unsupported")
	}
}

func DecodeExecResultIdentity(message ClientMessage) (ExecResultIdentity, error) {
	if message.Kind != ClientMessageExec {
		return ExecResultIdentity{}, errors.New("decode exec result identity: client message is not an exec result")
	}
	execPayload, err := execClientPayload(message.Raw)
	if err != nil {
		return ExecResultIdentity{}, err
	}
	fields, err := decodeWireFields(execPayload)
	if err != nil {
		return ExecResultIdentity{}, errors.New("decode exec result identity: malformed exec message")
	}
	messageID, _ := lastVarintField(fields, 1)
	execID, _ := lastBytesField(fields, 15)
	identity := ExecResultIdentity{MessageID: uint32(messageID), ExecID: strings.TrimSpace(string(execID))}
	if identity.MessageID == 0 && identity.ExecID == "" {
		return ExecResultIdentity{}, errors.New("decode exec result identity: message ID or exec ID is required")
	}
	return identity, nil
}

func EncodeToolCompleted(request ToolRequest, result ToolResult) ([]byte, error) {
	if err := validateToolRequest(request); err != nil {
		return nil, err
	}
	if result.MessageID != request.MessageID || result.ExecID != request.ExecID || result.Name != request.Name || !result.Complete {
		return nil, errors.New("encode tool completed: result does not match request")
	}
	var toolCall []byte
	var err error
	switch request.Name {
	case "Write":
		toolCall, err = encodeWriteDisplayToolCall(request, result.ResultPayload)
	case "Delete":
		toolCall, err = encodeDeleteDisplayToolCall(request, result.ResultPayload)
	case "List":
		toolCall, err = encodeListDisplayToolCall(request, result.ResultPayload)
	case "Grep":
		toolCall, err = encodeGrepDisplayToolCall(request, result.ResultPayload)
	case "Glob":
		toolCall, err = encodeGlobDisplayToolCall(request, result.ResultPayload)
	case "Shell":
		toolCall, err = encodeShellDisplayToolCall(request, result)
	case "CallMcpTool":
		toolCall, err = encodeMCPDisplayToolCall(request, result.ResultPayload)
	default:
		return nil, errors.New("encode tool completed: tool is unsupported")
	}
	if err != nil {
		return nil, err
	}
	completed := appendString(nil, 1, request.CallID)
	completed = appendMessage(completed, 2, toolCall)
	interaction := appendMessage(nil, 3, completed)
	return appendMessage(nil, 1, interaction), nil
}

func encodeWriteToolDispatch(request ToolRequest) (ToolDispatch, error) {
	arguments, err := decodeWriteToolArguments(request.Arguments)
	if err != nil {
		return ToolDispatch{}, err
	}
	execArgs := appendString(nil, 1, arguments.Path)
	execArgs = appendString(execArgs, 2, arguments.Contents)
	execArgs = appendString(execArgs, 3, request.CallID)
	execArgs = appendVarint(execArgs, 4, 1)
	execArgs = appendString(execArgs, 6, "utf-8")
	execMessage := appendVarint(nil, 1, uint64(request.MessageID))
	execMessage = appendString(execMessage, 15, request.ExecID)
	execMessage = appendMessage(execMessage, 3, execArgs)
	toolCall, err := encodeWriteDisplayToolCall(request, nil)
	if err != nil {
		return ToolDispatch{}, err
	}
	started := appendString(nil, 1, request.CallID)
	started = appendMessage(started, 2, toolCall)
	interaction := appendMessage(nil, 2, started)
	return ToolDispatch{
		Execute: appendMessage(nil, 2, execMessage),
		Started: appendMessage(nil, 1, interaction),
	}, nil
}

func decodeWriteToolResult(execFields []wireField, result ToolResult) (ToolResult, error) {
	payload, ok := lastBytesField(execFields, 3)
	if !ok {
		return ToolResult{}, errors.New("decode tool result: write result is required")
	}
	fields, err := decodeWireFields(payload)
	if err != nil {
		return ToolResult{}, errors.New("decode tool result: malformed write result")
	}
	result.Complete = true
	result.ResultPayload = append([]byte(nil), payload...)
	if successPayload, found := lastBytesField(fields, 1); found {
		successFields, parseError := decodeWireFields(successPayload)
		if parseError != nil {
			return ToolResult{}, errors.New("decode tool result: malformed write success")
		}
		path, _ := lastBytesField(successFields, 1)
		lines, _ := lastVarintField(successFields, 2)
		size, _ := lastVarintField(successFields, 3)
		content, marshalError := json.Marshal(writeSuccessContent{Path: string(path), LinesCreated: int64(lines), FileSize: int64(size)})
		if marshalError != nil {
			return ToolResult{}, errors.New("decode tool result: encode write success")
		}
		result.Content = string(content)
		return result, nil
	}
	for _, candidate := range []struct {
		field         int
		fallback      string
		errorField    int
		readOnlyField int
	}{
		{3, "permission denied", 4, 5},
		{4, "no space left on device", 0, 0},
		{5, "write failed", 2, 0},
		{6, "write rejected", 2, 0},
	} {
		failurePayload, found := lastBytesField(fields, protowireNumber(candidate.field))
		if !found {
			continue
		}
		failureFields, parseError := decodeWireFields(failurePayload)
		if parseError != nil {
			return ToolResult{}, errors.New("decode tool result: malformed write failure")
		}
		path, _ := lastBytesField(failureFields, 1)
		detail := candidate.fallback
		if candidate.errorField != 0 {
			if value, present := lastBytesField(failureFields, protowireNumber(candidate.errorField)); present && strings.TrimSpace(string(value)) != "" {
				detail = strings.TrimSpace(string(value))
			}
		}
		readOnly := false
		if candidate.readOnlyField != 0 {
			value, _ := lastVarintField(failureFields, protowireNumber(candidate.readOnlyField))
			readOnly = value != 0
		}
		content, marshalError := json.Marshal(writeErrorContent{Path: string(path), Error: detail, ReadOnly: readOnly})
		if marshalError != nil {
			return ToolResult{}, errors.New("decode tool result: encode write failure")
		}
		result.Content = string(content)
		result.IsError = true
		return result, nil
	}
	return ToolResult{}, errors.New("decode tool result: write result variant is unsupported")
}

func encodeWriteDisplayToolCall(request ToolRequest, writeResult []byte) ([]byte, error) {
	arguments, err := decodeWriteToolArguments(request.Arguments)
	if err != nil {
		return nil, err
	}
	editArgs := appendString(nil, 1, arguments.Path)
	editArgs = appendString(editArgs, 6, arguments.Contents)
	editToolCall := appendMessage(nil, 1, editArgs)
	if writeResult != nil {
		editResult, convertError := convertWriteResultToEditResult(writeResult, arguments.Path)
		if convertError != nil {
			return nil, convertError
		}
		editToolCall = appendMessage(editToolCall, 2, editResult)
	}
	return appendMessage(nil, 12, editToolCall), nil
}

func convertWriteResultToEditResult(writeResult []byte, fallbackPath string) ([]byte, error) {
	fields, err := decodeWireFields(writeResult)
	if err != nil {
		return nil, errors.New("encode tool completed: malformed write result")
	}
	if successPayload, found := lastBytesField(fields, 1); found {
		successFields, parseError := decodeWireFields(successPayload)
		if parseError != nil {
			return nil, errors.New("encode tool completed: malformed write success")
		}
		path, _ := lastBytesField(successFields, 1)
		if len(path) == 0 {
			path = []byte(fallbackPath)
		}
		lines, hasLines := lastVarintField(successFields, 2)
		content, _ := lastBytesField(successFields, 4)
		editSuccess := appendString(nil, 1, string(path))
		if hasLines {
			editSuccess = appendVarint(editSuccess, 3, lines)
		}
		editSuccess = appendString(editSuccess, 7, string(content))
		return appendMessage(nil, 1, editSuccess), nil
	}
	if permissionPayload, found := lastBytesField(fields, 3); found {
		permissionFields, parseError := decodeWireFields(permissionPayload)
		if parseError != nil {
			return nil, errors.New("encode tool completed: malformed write permission failure")
		}
		path, _ := lastBytesField(permissionFields, 1)
		if len(path) == 0 {
			path = []byte(fallbackPath)
		}
		detail, _ := lastBytesField(permissionFields, 4)
		readOnly, _ := lastVarintField(permissionFields, 5)
		failure := appendString(nil, 1, string(path))
		failure = appendString(failure, 2, fallbackString(string(detail), "permission denied"))
		failure = appendVarint(failure, 3, readOnly)
		return appendMessage(nil, 4, failure), nil
	}
	for _, candidate := range []struct {
		field       int
		editField   int
		fallback    string
		detailField int
	}{
		{4, 7, "no space left on device", 0},
		{5, 7, "write failed", 2},
		{6, 6, "write rejected", 2},
	} {
		payload, found := lastBytesField(fields, protowireNumber(candidate.field))
		if !found {
			continue
		}
		failureFields, parseError := decodeWireFields(payload)
		if parseError != nil {
			return nil, errors.New("encode tool completed: malformed write failure")
		}
		path, _ := lastBytesField(failureFields, 1)
		if len(path) == 0 {
			path = []byte(fallbackPath)
		}
		detail := candidate.fallback
		if candidate.detailField != 0 {
			value, _ := lastBytesField(failureFields, protowireNumber(candidate.detailField))
			detail = fallbackString(string(value), detail)
		}
		failure := appendString(nil, 1, string(path))
		failure = appendString(failure, 2, detail)
		return appendMessage(nil, protowireNumber(candidate.editField), failure), nil
	}
	return nil, errors.New("encode tool completed: write result variant is unsupported")
}

func decodeWriteToolArguments(raw string) (writeToolArguments, error) {
	var arguments writeToolArguments
	if err := decodeStrictToolArguments(raw, &arguments); err != nil {
		return writeToolArguments{}, errors.New("encode Write tool: arguments are invalid")
	}
	if strings.TrimSpace(arguments.Path) == "" || strings.IndexByte(arguments.Path, 0) >= 0 {
		return writeToolArguments{}, errors.New("encode Write tool: path is required")
	}
	return arguments, nil
}

func decodeStrictToolArguments(raw string, target any) error {
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func validateToolRequest(request ToolRequest) error {
	if request.MessageID == 0 {
		return errors.New("encode tool dispatch: message ID is required")
	}
	if !validIdentifier(strings.TrimSpace(request.ExecID)) || !validIdentifier(strings.TrimSpace(request.CallID)) {
		return errors.New("encode tool dispatch: exec ID and call ID are required")
	}
	if request.Name == "" || request.Name != strings.TrimSpace(request.Name) {
		return errors.New("encode tool dispatch: tool name is required")
	}
	if request.Arguments == "" {
		return errors.New("encode tool dispatch: arguments are required")
	}
	return nil
}

func execClientPayload(raw []byte) ([]byte, error) {
	topFields, err := decodeWireFields(raw)
	if err != nil {
		return nil, fmt.Errorf("decode tool result: malformed protobuf: %w", err)
	}
	payload, ok := lastBytesField(topFields, 2)
	if !ok {
		return nil, errors.New("decode tool result: exec message is required")
	}
	return payload, nil
}

func fallbackString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

// protowireNumber keeps field-number conversions local without exposing wire
// details through the public tool bridge types.
func protowireNumber(value int) protowire.Number {
	return protowire.Number(value)
}
