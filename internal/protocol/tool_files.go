package protocol

import (
	"encoding/json"
	"errors"
	"path"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

const maxStructuredToolItems = 4096

type deleteToolArguments struct {
	Path string `json:"path"`
}

type listToolArguments struct {
	Path  string `json:"path"`
	Depth int    `json:"depth,omitempty"`
}

type grepToolArguments struct {
	Pattern    string `json:"pattern"`
	Path       string `json:"path,omitempty"`
	Glob       string `json:"glob,omitempty"`
	OutputMode string `json:"output_mode,omitempty"`
}

type globToolArguments struct {
	GlobPattern     string `json:"glob_pattern"`
	TargetDirectory string `json:"target_directory,omitempty"`
}

type deleteSuccessContent struct {
	Path     string `json:"path"`
	FileSize int64  `json:"file_size"`
}

type listResultContent struct {
	Path      string   `json:"path"`
	Entries   []string `json:"entries"`
	Truncated bool     `json:"truncated,omitempty"`
}

type grepMatchContent struct {
	File          string `json:"file"`
	LineNumber    int64  `json:"line_number"`
	Content       string `json:"content"`
	IsContextLine bool   `json:"is_context_line,omitempty"`
}

type grepCountContent struct {
	File  string `json:"file"`
	Count int64  `json:"count"`
}

type grepResultContent struct {
	Pattern    string             `json:"pattern,omitempty"`
	Path       string             `json:"path,omitempty"`
	OutputMode string             `json:"output_mode,omitempty"`
	Files      []string           `json:"files,omitempty"`
	Matches    []grepMatchContent `json:"matches,omitempty"`
	Counts     []grepCountContent `json:"counts,omitempty"`
	Truncated  bool               `json:"truncated,omitempty"`
}

type toolErrorContent struct {
	Path  string `json:"path,omitempty"`
	Error string `json:"error"`
}

func encodeDeleteToolDispatch(request ToolRequest) (ToolDispatch, error) {
	arguments, err := decodeDeleteToolArguments(request.Arguments)
	if err != nil {
		return ToolDispatch{}, err
	}
	args := appendString(nil, 1, arguments.Path)
	args = appendString(args, 2, request.CallID)
	return encodeFileToolDispatch(request, 4, args, 3, args), nil
}

func encodeListToolDispatch(request ToolRequest) (ToolDispatch, error) {
	arguments, err := decodeListToolArguments(request.Arguments)
	if err != nil {
		return ToolDispatch{}, err
	}
	args := appendString(nil, 1, arguments.Path)
	args = appendString(args, 3, request.CallID)
	return encodeFileToolDispatch(request, 8, args, 13, args), nil
}

func encodeGrepToolDispatch(request ToolRequest) (ToolDispatch, error) {
	arguments, err := decodeGrepToolArguments(request.Arguments)
	if err != nil {
		return ToolDispatch{}, err
	}
	args := encodeGrepExecArgs(arguments, request.CallID)
	return encodeFileToolDispatch(request, 5, args, 5, args), nil
}

func encodeGlobToolDispatch(request ToolRequest) (ToolDispatch, error) {
	arguments, err := decodeGlobToolArguments(request.Arguments)
	if err != nil {
		return ToolDispatch{}, err
	}
	grepArgs := grepToolArguments{Path: arguments.TargetDirectory, Glob: arguments.GlobPattern, OutputMode: "files_with_matches"}
	execArgs := encodeGrepExecArgs(grepArgs, request.CallID)
	displayArgs := make([]byte, 0)
	if arguments.TargetDirectory != "" {
		displayArgs = appendString(displayArgs, 1, arguments.TargetDirectory)
	}
	displayArgs = appendString(displayArgs, 2, arguments.GlobPattern)
	return encodeFileToolDispatch(request, 5, execArgs, 4, displayArgs), nil
}

func encodeFileToolDispatch(request ToolRequest, execField protowire.Number, execArgs []byte, displayField protowire.Number, displayArgs []byte) ToolDispatch {
	execMessage := appendVarint(nil, 1, uint64(request.MessageID))
	execMessage = appendString(execMessage, 15, request.ExecID)
	execMessage = appendMessage(execMessage, execField, execArgs)
	display := appendMessage(nil, 1, displayArgs)
	toolCall := appendMessage(nil, displayField, display)
	started := appendString(nil, 1, request.CallID)
	started = appendMessage(started, 2, toolCall)
	interaction := appendMessage(nil, 2, started)
	return ToolDispatch{Execute: appendMessage(nil, 2, execMessage), Started: appendMessage(nil, 1, interaction)}
}

func decodeDeleteToolResult(execFields []wireField, result ToolResult) (ToolResult, error) {
	payload, ok := lastBytesField(execFields, 4)
	if !ok {
		return ToolResult{}, errors.New("decode tool result: delete result is required")
	}
	fields, err := decodeWireFields(payload)
	if err != nil {
		return ToolResult{}, errors.New("decode tool result: malformed delete result")
	}
	result.Complete = true
	result.ResultPayload = append([]byte(nil), payload...)
	if successPayload, found := lastBytesField(fields, 1); found {
		successFields, parseError := decodeWireFields(successPayload)
		if parseError != nil {
			return ToolResult{}, errors.New("decode tool result: malformed delete success")
		}
		pathValue, _ := lastBytesField(successFields, 1)
		size, _ := lastVarintField(successFields, 3)
		return withJSONContent(result, deleteSuccessContent{Path: string(pathValue), FileSize: int64(size)})
	}
	for _, candidate := range []struct {
		field       protowire.Number
		fallback    string
		detailField protowire.Number
	}{
		{2, "file not found", 0}, {3, "path is not a file", 2}, {4, "permission denied", 2},
		{5, "file is busy", 0}, {6, "delete rejected", 2}, {7, "delete failed", 2},
	} {
		failurePayload, found := lastBytesField(fields, candidate.field)
		if !found {
			continue
		}
		failureFields, parseError := decodeWireFields(failurePayload)
		if parseError != nil {
			return ToolResult{}, errors.New("decode tool result: malformed delete failure")
		}
		pathValue, _ := lastBytesField(failureFields, 1)
		detail := candidate.fallback
		if candidate.detailField != 0 {
			value, _ := lastBytesField(failureFields, candidate.detailField)
			detail = fallbackString(string(value), detail)
		}
		result.IsError = true
		return withJSONContent(result, toolErrorContent{Path: string(pathValue), Error: detail})
	}
	return ToolResult{}, errors.New("decode tool result: delete result variant is unsupported")
}

func decodeListToolResult(execFields []wireField, request ToolRequest, result ToolResult) (ToolResult, error) {
	payload, ok := lastBytesField(execFields, 8)
	if !ok {
		return ToolResult{}, errors.New("decode tool result: list result is required")
	}
	fields, err := decodeWireFields(payload)
	if err != nil {
		return ToolResult{}, errors.New("decode tool result: malformed list result")
	}
	result.Complete = true
	result.ResultPayload = append([]byte(nil), payload...)
	arguments, err := decodeListToolArguments(request.Arguments)
	if err != nil {
		return ToolResult{}, err
	}
	if successPayload, found := lastBytesField(fields, 1); found {
		successFields, parseError := decodeWireFields(successPayload)
		if parseError != nil {
			return ToolResult{}, errors.New("decode tool result: malformed list success")
		}
		tree, ok := lastBytesField(successFields, 1)
		if !ok {
			return ToolResult{}, errors.New("decode tool result: list tree is required")
		}
		entries := make([]string, 0)
		truncated, parseError := flattenListTree(tree, false, 0, arguments.Depth, &entries)
		if parseError != nil {
			return ToolResult{}, parseError
		}
		return withJSONContent(result, listResultContent{Path: arguments.Path, Entries: entries, Truncated: truncated})
	}
	for _, candidate := range []struct {
		field       protowire.Number
		fallback    string
		detailField protowire.Number
	}{
		{2, "list failed", 2}, {3, "list rejected", 2}, {4, "list timed out", 0},
	} {
		failurePayload, found := lastBytesField(fields, candidate.field)
		if !found {
			continue
		}
		failureFields, parseError := decodeWireFields(failurePayload)
		if parseError != nil {
			return ToolResult{}, errors.New("decode tool result: malformed list failure")
		}
		pathValue, _ := lastBytesField(failureFields, 1)
		detail := candidate.fallback
		if candidate.detailField != 0 {
			value, _ := lastBytesField(failureFields, candidate.detailField)
			detail = fallbackString(string(value), detail)
		}
		result.IsError = true
		return withJSONContent(result, toolErrorContent{Path: string(pathValue), Error: detail})
	}
	return ToolResult{}, errors.New("decode tool result: list result variant is unsupported")
}

func decodeGrepToolResult(execFields []wireField, result ToolResult) (ToolResult, error) {
	payload, ok := lastBytesField(execFields, 5)
	if !ok {
		return ToolResult{}, errors.New("decode tool result: grep result is required")
	}
	content, isError, err := parseGrepResult(payload)
	if err != nil {
		return ToolResult{}, err
	}
	result.Complete = true
	result.ResultPayload = append([]byte(nil), payload...)
	result.IsError = isError
	return withJSONContent(result, content)
}

func decodeGlobToolResult(execFields []wireField, request ToolRequest, result ToolResult) (ToolResult, error) {
	payload, ok := lastBytesField(execFields, 5)
	if !ok {
		return ToolResult{}, errors.New("decode tool result: glob transport result is required")
	}
	content, isError, err := parseGrepResult(payload)
	if err != nil {
		return ToolResult{}, err
	}
	arguments, err := decodeGlobToolArguments(request.Arguments)
	if err != nil {
		return ToolResult{}, err
	}
	content.Pattern = arguments.GlobPattern
	content.Path = arguments.TargetDirectory
	content.OutputMode = "files_with_matches"
	result.Complete = true
	result.ResultPayload = append([]byte(nil), payload...)
	result.IsError = isError
	return withJSONContent(result, content)
}

func encodeDeleteDisplayToolCall(request ToolRequest, result []byte) ([]byte, error) {
	arguments, err := decodeDeleteToolArguments(request.Arguments)
	if err != nil {
		return nil, err
	}
	args := appendString(nil, 1, arguments.Path)
	args = appendString(args, 2, request.CallID)
	return encodeDirectResultDisplay(3, args, result), nil
}

func encodeListDisplayToolCall(request ToolRequest, result []byte) ([]byte, error) {
	arguments, err := decodeListToolArguments(request.Arguments)
	if err != nil {
		return nil, err
	}
	args := appendString(nil, 1, arguments.Path)
	args = appendString(args, 3, request.CallID)
	return encodeDirectResultDisplay(13, args, result), nil
}

func encodeGrepDisplayToolCall(request ToolRequest, result []byte) ([]byte, error) {
	arguments, err := decodeGrepToolArguments(request.Arguments)
	if err != nil {
		return nil, err
	}
	args := encodeGrepExecArgs(arguments, request.CallID)
	return encodeDirectResultDisplay(5, args, result), nil
}

func encodeGlobDisplayToolCall(request ToolRequest, grepResult []byte) ([]byte, error) {
	arguments, err := decodeGlobToolArguments(request.Arguments)
	if err != nil {
		return nil, err
	}
	args := make([]byte, 0)
	if arguments.TargetDirectory != "" {
		args = appendString(args, 1, arguments.TargetDirectory)
	}
	args = appendString(args, 2, arguments.GlobPattern)
	toolCall := appendMessage(nil, 1, args)
	if grepResult != nil {
		content, isError, parseError := parseGrepResult(grepResult)
		if parseError != nil {
			return nil, parseError
		}
		var globResult []byte
		if isError {
			errorPayload := appendString(nil, 1, firstGrepError(content))
			globResult = appendMessage(nil, 2, errorPayload)
		} else {
			success := appendString(nil, 1, arguments.GlobPattern)
			success = appendString(success, 2, arguments.TargetDirectory)
			for _, file := range content.Files {
				success = appendString(success, 3, file)
			}
			success = appendVarint(success, 4, uint64(len(content.Files)))
			if content.Truncated {
				success = appendVarint(success, 5, 1)
			}
			globResult = appendMessage(nil, 1, success)
		}
		toolCall = appendMessage(toolCall, 2, globResult)
	}
	return appendMessage(nil, 4, toolCall), nil
}

func encodeDirectResultDisplay(toolField protowire.Number, args, result []byte) []byte {
	toolCall := appendMessage(nil, 1, args)
	if result != nil {
		toolCall = appendMessage(toolCall, 2, result)
	}
	return appendMessage(nil, toolField, toolCall)
}

func encodeGrepExecArgs(arguments grepToolArguments, callID string) []byte {
	var args []byte
	if arguments.Pattern != "" {
		args = appendString(args, 1, arguments.Pattern)
	}
	if arguments.Path != "" {
		args = appendString(args, 2, arguments.Path)
	}
	if arguments.Glob != "" {
		args = appendString(args, 3, arguments.Glob)
	}
	if arguments.OutputMode != "" {
		args = appendString(args, 4, arguments.OutputMode)
	}
	return appendString(args, 14, callID)
}

func decodeDeleteToolArguments(raw string) (deleteToolArguments, error) {
	var arguments deleteToolArguments
	if err := decodeStrictToolArguments(raw, &arguments); err != nil || strings.TrimSpace(arguments.Path) == "" || strings.IndexByte(arguments.Path, 0) >= 0 {
		return deleteToolArguments{}, errors.New("encode Delete tool: arguments are invalid")
	}
	return arguments, nil
}

func decodeListToolArguments(raw string) (listToolArguments, error) {
	var arguments listToolArguments
	if err := decodeStrictToolArguments(raw, &arguments); err != nil || strings.TrimSpace(arguments.Path) == "" || strings.IndexByte(arguments.Path, 0) >= 0 || arguments.Depth < 0 {
		return listToolArguments{}, errors.New("encode List tool: arguments are invalid")
	}
	if arguments.Depth == 0 {
		arguments.Depth = 64
	}
	return arguments, nil
}

func decodeGrepToolArguments(raw string) (grepToolArguments, error) {
	var arguments grepToolArguments
	if err := decodeStrictToolArguments(raw, &arguments); err != nil || strings.TrimSpace(arguments.Pattern) == "" {
		return grepToolArguments{}, errors.New("encode Grep tool: arguments are invalid")
	}
	switch arguments.OutputMode {
	case "", "content", "files_with_matches", "count":
	default:
		return grepToolArguments{}, errors.New("encode Grep tool: output mode is invalid")
	}
	return arguments, nil
}

func decodeGlobToolArguments(raw string) (globToolArguments, error) {
	var arguments globToolArguments
	if err := decodeStrictToolArguments(raw, &arguments); err != nil || strings.TrimSpace(arguments.GlobPattern) == "" {
		return globToolArguments{}, errors.New("encode Glob tool: arguments are invalid")
	}
	return arguments, nil
}

func flattenListTree(payload []byte, includeDirectory bool, currentDepth, maximumDepth int, entries *[]string) (bool, error) {
	if len(*entries) >= maxStructuredToolItems || currentDepth > maximumDepth {
		return true, nil
	}
	fields, err := decodeWireFields(payload)
	if err != nil {
		return false, errors.New("decode tool result: malformed list tree")
	}
	directoryBytes, _ := lastBytesField(fields, 1)
	directory := string(directoryBytes)
	if includeDirectory && directory != "" {
		*entries = append(*entries, directory)
	}
	if currentDepth < maximumDepth {
		for _, filePayload := range repeatedBytesField(fields, 3) {
			if len(*entries) >= maxStructuredToolItems {
				return true, nil
			}
			fileFields, parseError := decodeWireFields(filePayload)
			if parseError != nil {
				return false, errors.New("decode tool result: malformed list file")
			}
			name, _ := lastBytesField(fileFields, 1)
			if len(name) > 0 {
				*entries = append(*entries, path.Join(directory, string(name)))
			}
		}
	}
	truncated := false
	for _, child := range repeatedBytesField(fields, 2) {
		childTruncated, parseError := flattenListTree(child, true, currentDepth+1, maximumDepth, entries)
		if parseError != nil {
			return false, parseError
		}
		truncated = truncated || childTruncated
		if len(*entries) >= maxStructuredToolItems {
			return true, nil
		}
	}
	return truncated, nil
}

func parseGrepResult(payload []byte) (grepResultContent, bool, error) {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return grepResultContent{}, false, errors.New("decode tool result: malformed grep result")
	}
	if errorPayload, found := lastBytesField(fields, 2); found {
		errorFields, parseError := decodeWireFields(errorPayload)
		if parseError != nil {
			return grepResultContent{}, false, errors.New("decode tool result: malformed grep failure")
		}
		detail, _ := lastBytesField(errorFields, 1)
		return grepResultContent{Matches: []grepMatchContent{{Content: fallbackString(string(detail), "grep failed")}}}, true, nil
	}
	successPayload, found := lastBytesField(fields, 1)
	if !found {
		return grepResultContent{}, false, errors.New("decode tool result: grep result variant is unsupported")
	}
	successFields, err := decodeWireFields(successPayload)
	if err != nil {
		return grepResultContent{}, false, errors.New("decode tool result: malformed grep success")
	}
	patternValue, _ := lastBytesField(successFields, 1)
	pathValue, _ := lastBytesField(successFields, 2)
	modeValue, _ := lastBytesField(successFields, 3)
	result := grepResultContent{Pattern: string(patternValue), Path: string(pathValue), OutputMode: string(modeValue)}
	for _, entryPayload := range repeatedBytesField(successFields, 4) {
		entryFields, parseError := decodeWireFields(entryPayload)
		if parseError != nil {
			return grepResultContent{}, false, errors.New("decode tool result: malformed grep workspace result")
		}
		unionPayload, ok := lastBytesField(entryFields, 2)
		if !ok {
			continue
		}
		if err := appendGrepUnion(unionPayload, &result); err != nil {
			return grepResultContent{}, false, err
		}
	}
	if active, ok := lastBytesField(successFields, 5); ok {
		if err := appendGrepUnion(active, &result); err != nil {
			return grepResultContent{}, false, err
		}
	}
	return result, false, nil
}

func appendGrepUnion(payload []byte, result *grepResultContent) error {
	fields, err := decodeWireFields(payload)
	if err != nil {
		return errors.New("decode tool result: malformed grep union")
	}
	if countPayload, found := lastBytesField(fields, 1); found {
		countFields, parseError := decodeWireFields(countPayload)
		if parseError != nil {
			return errors.New("decode tool result: malformed grep count result")
		}
		for _, item := range repeatedBytesField(countFields, 1) {
			itemFields, itemError := decodeWireFields(item)
			if itemError != nil {
				return errors.New("decode tool result: malformed grep count")
			}
			file, _ := lastBytesField(itemFields, 1)
			count, _ := lastVarintField(itemFields, 2)
			if len(result.Counts) < maxStructuredToolItems {
				result.Counts = append(result.Counts, grepCountContent{File: string(file), Count: int64(count)})
			} else {
				result.Truncated = true
			}
		}
		return nil
	}
	if filesPayload, found := lastBytesField(fields, 2); found {
		fileFields, parseError := decodeWireFields(filesPayload)
		if parseError != nil {
			return errors.New("decode tool result: malformed grep files result")
		}
		for _, file := range repeatedBytesField(fileFields, 1) {
			if len(result.Files) < maxStructuredToolItems {
				result.Files = append(result.Files, string(file))
			} else {
				result.Truncated = true
			}
		}
		return nil
	}
	if contentPayload, found := lastBytesField(fields, 3); found {
		contentFields, parseError := decodeWireFields(contentPayload)
		if parseError != nil {
			return errors.New("decode tool result: malformed grep content result")
		}
		for _, filePayload := range repeatedBytesField(contentFields, 1) {
			fileFields, fileError := decodeWireFields(filePayload)
			if fileError != nil {
				return errors.New("decode tool result: malformed grep file match")
			}
			file, _ := lastBytesField(fileFields, 1)
			for _, matchPayload := range repeatedBytesField(fileFields, 2) {
				matchFields, matchError := decodeWireFields(matchPayload)
				if matchError != nil {
					return errors.New("decode tool result: malformed grep content match")
				}
				line, _ := lastVarintField(matchFields, 1)
				content, _ := lastBytesField(matchFields, 2)
				contextLine, _ := lastVarintField(matchFields, 4)
				if len(result.Matches) < maxStructuredToolItems {
					result.Matches = append(result.Matches, grepMatchContent{File: string(file), LineNumber: int64(line), Content: string(content), IsContextLine: contextLine != 0})
				} else {
					result.Truncated = true
				}
			}
		}
		return nil
	}
	return errors.New("decode tool result: grep union variant is unsupported")
}

func repeatedBytesField(fields []wireField, number protowire.Number) [][]byte {
	values := make([][]byte, 0)
	for _, field := range fields {
		if field.number == number && field.typeID == protowire.BytesType {
			values = append(values, field.bytes)
		}
	}
	return values
}

func withJSONContent(result ToolResult, value any) (ToolResult, error) {
	content, err := json.Marshal(value)
	if err != nil {
		return ToolResult{}, errors.New("decode tool result: encode structured output")
	}
	result.Content = string(content)
	return result, nil
}

func firstGrepError(content grepResultContent) string {
	if len(content.Matches) > 0 && strings.TrimSpace(content.Matches[0].Content) != "" {
		return content.Matches[0].Content
	}
	return "glob failed"
}
