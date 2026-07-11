package protocol

import (
	"errors"
	"strings"
)

type EditArguments struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all,omitempty"`
}

func EncodeEditReadDispatch(request ToolRequest) (ToolDispatch, error) {
	if err := validateToolRequest(request); err != nil {
		return ToolDispatch{}, err
	}
	if request.Name != "Edit" {
		return ToolDispatch{}, errors.New("encode Edit tool: tool name is invalid")
	}
	arguments, err := DecodeEditArguments(request.Arguments)
	if err != nil {
		return ToolDispatch{}, err
	}
	readArgs := appendString(nil, 1, arguments.Path)
	readArgs = appendString(readArgs, 2, request.CallID)
	execMessage := appendVarint(nil, 1, uint64(request.MessageID))
	execMessage = appendString(execMessage, 15, request.ExecID)
	execMessage = appendMessage(execMessage, 7, readArgs)
	editArgs := appendString(nil, 1, arguments.Path)
	editToolCall := appendMessage(nil, 1, editArgs)
	toolCall := appendMessage(nil, 12, editToolCall)
	started := appendString(nil, 1, request.CallID)
	started = appendMessage(started, 2, toolCall)
	interaction := appendMessage(nil, 2, started)
	return ToolDispatch{Execute: appendMessage(nil, 2, execMessage), Started: appendMessage(nil, 1, interaction)}, nil
}

func ApplyEditArguments(raw, content string) (string, int, error) {
	arguments, err := DecodeEditArguments(raw)
	if err != nil {
		return "", 0, err
	}
	matches := strings.Count(content, arguments.OldString)
	switch {
	case matches == 0:
		return "", 0, errors.New("apply Edit tool: old_string was not found")
	case matches > 1 && !arguments.ReplaceAll:
		return "", 0, errors.New("apply Edit tool: old_string has multiple matches; set replace_all to true")
	}
	maximum := 1
	if arguments.ReplaceAll {
		maximum = -1
	}
	return strings.Replace(content, arguments.OldString, arguments.NewString, maximum), matchesForReplacement(matches, arguments.ReplaceAll), nil
}

func EncodeEditFailureCompleted(request ToolRequest, detail string) ([]byte, error) {
	if err := validateToolRequest(request); err != nil {
		return nil, err
	}
	if request.Name != "Edit" {
		return nil, errors.New("encode Edit tool: tool name is invalid")
	}
	arguments, err := DecodeEditArguments(request.Arguments)
	if err != nil {
		return nil, err
	}
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return nil, errors.New("encode Edit tool: failure detail is required")
	}
	editArgs := appendString(nil, 1, arguments.Path)
	editError := appendString(nil, 1, arguments.Path)
	editError = appendString(editError, 2, detail)
	editError = appendString(editError, 5, detail)
	editResult := appendMessage(nil, 7, editError)
	editToolCall := appendMessage(nil, 1, editArgs)
	editToolCall = appendMessage(editToolCall, 2, editResult)
	toolCall := appendMessage(nil, 12, editToolCall)
	completed := appendString(nil, 1, request.CallID)
	completed = appendMessage(completed, 2, toolCall)
	interaction := appendMessage(nil, 3, completed)
	return appendMessage(nil, 1, interaction), nil
}

func DecodeEditArguments(raw string) (EditArguments, error) {
	var arguments EditArguments
	if err := decodeStrictToolArguments(raw, &arguments); err != nil {
		return EditArguments{}, errors.New("encode Edit tool: arguments are invalid")
	}
	if strings.TrimSpace(arguments.Path) == "" || strings.IndexByte(arguments.Path, 0) >= 0 || arguments.OldString == "" {
		return EditArguments{}, errors.New("encode Edit tool: arguments are invalid")
	}
	return arguments, nil
}

func matchesForReplacement(matches int, replaceAll bool) int {
	if replaceAll {
		return matches
	}
	return 1
}
