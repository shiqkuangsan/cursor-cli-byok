package protocol

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

type ShellEventKind uint8

const (
	ShellEventUnknown ShellEventKind = iota
	ShellEventStdout
	ShellEventStderr
	ShellEventExit
	ShellEventStart
	ShellEventRejected
	ShellEventPermissionDenied
	ShellEventBackgrounded
)

type shellToolArguments struct {
	Command          string   `json:"command"`
	Description      string   `json:"description,omitempty"`
	WorkingDirectory string   `json:"working_directory,omitempty"`
	BlockUntilMS     *float64 `json:"block_until_ms,omitempty"`
}

func encodeShellToolDispatch(request ToolRequest) (ToolDispatch, error) {
	arguments, err := decodeShellToolArguments(request.Arguments)
	if err != nil {
		return ToolDispatch{}, err
	}
	args := encodeShellArgs(arguments, request.CallID, true)
	execMessage := appendVarint(nil, 1, uint64(request.MessageID))
	execMessage = appendString(execMessage, 15, request.ExecID)
	execMessage = appendMessage(execMessage, 14, args)
	toolCallPayload := appendMessage(nil, 1, encodeShellArgs(arguments, request.CallID, false))
	if arguments.Description != "" {
		toolCallPayload = appendString(toolCallPayload, 3, arguments.Description)
	}
	toolCall := appendMessage(nil, 1, toolCallPayload)
	started := appendString(nil, 1, request.CallID)
	started = appendMessage(started, 2, toolCall)
	interaction := appendMessage(nil, 2, started)
	return ToolDispatch{Execute: appendMessage(nil, 2, execMessage), Started: appendMessage(nil, 1, interaction)}, nil
}

func decodeShellToolResult(execFields []wireField, result ToolResult) (ToolResult, error) {
	payload, ok := lastBytesField(execFields, 14)
	if !ok {
		return ToolResult{}, errors.New("decode tool result: shell stream result is required")
	}
	fields, err := decodeWireFields(payload)
	if err != nil {
		return ToolResult{}, errors.New("decode tool result: malformed shell stream")
	}
	result.ResultPayload = append([]byte(nil), payload...)
	matched := 0
	if eventPayload, found := lastBytesField(fields, 1); found {
		matched++
		eventFields, parseError := decodeWireFields(eventPayload)
		if parseError != nil {
			return ToolResult{}, errors.New("decode tool result: malformed shell stdout")
		}
		data, _ := lastBytesField(eventFields, 1)
		result.ShellEvent = ShellEventStdout
		result.StdoutDelta = string(data)
		result.EventPayload = append([]byte(nil), eventPayload...)
	}
	if eventPayload, found := lastBytesField(fields, 2); found {
		matched++
		eventFields, parseError := decodeWireFields(eventPayload)
		if parseError != nil {
			return ToolResult{}, errors.New("decode tool result: malformed shell stderr")
		}
		data, _ := lastBytesField(eventFields, 1)
		result.ShellEvent = ShellEventStderr
		result.StderrDelta = string(data)
		result.EventPayload = append([]byte(nil), eventPayload...)
	}
	if eventPayload, found := lastBytesField(fields, 3); found {
		matched++
		eventFields, parseError := decodeWireFields(eventPayload)
		if parseError != nil {
			return ToolResult{}, errors.New("decode tool result: malformed shell exit")
		}
		code, _ := lastVarintField(eventFields, 1)
		if code > math.MaxInt32 {
			return ToolResult{}, errors.New("decode tool result: shell exit code is invalid")
		}
		cwd, _ := lastBytesField(eventFields, 2)
		result.ShellEvent = ShellEventExit
		result.ExitCode = int32(code)
		result.CWD = string(cwd)
		result.Complete = true
		result.IsError = code != 0
		result.EventPayload = append([]byte(nil), eventPayload...)
	}
	if eventPayload, found := lastBytesField(fields, 4); found {
		matched++
		result.ShellEvent = ShellEventStart
		result.EventPayload = append([]byte(nil), eventPayload...)
	}
	if eventPayload, found := lastBytesField(fields, 5); found {
		matched++
		eventFields, parseError := decodeWireFields(eventPayload)
		if parseError != nil {
			return ToolResult{}, errors.New("decode tool result: malformed shell rejection")
		}
		detail, _ := lastBytesField(eventFields, 3)
		result.ShellEvent = ShellEventRejected
		result.Complete = true
		result.IsError = true
		result.Content = "shell rejected: " + fallbackString(string(detail), "approval required")
		result.EventPayload = append([]byte(nil), eventPayload...)
	}
	if eventPayload, found := lastBytesField(fields, 6); found {
		matched++
		eventFields, parseError := decodeWireFields(eventPayload)
		if parseError != nil {
			return ToolResult{}, errors.New("decode tool result: malformed shell permission failure")
		}
		detail, _ := lastBytesField(eventFields, 3)
		result.ShellEvent = ShellEventPermissionDenied
		result.Complete = true
		result.IsError = true
		result.Content = "shell permission denied: " + fallbackString(string(detail), "permission denied")
		result.EventPayload = append([]byte(nil), eventPayload...)
	}
	if eventPayload, found := lastBytesField(fields, 7); found {
		matched++
		eventFields, parseError := decodeWireFields(eventPayload)
		if parseError != nil {
			return ToolResult{}, errors.New("decode tool result: malformed shell background result")
		}
		shellID, _ := lastVarintField(eventFields, 1)
		cwd, _ := lastBytesField(eventFields, 3)
		pid, _ := lastVarintField(eventFields, 4)
		result.ShellEvent = ShellEventBackgrounded
		result.ShellID = uint32(shellID)
		result.PID = uint32(pid)
		result.CWD = string(cwd)
		result.Complete = true
		result.Content = fmt.Sprintf("shell backgrounded: shell_id=%d pid=%d", result.ShellID, result.PID)
		result.EventPayload = append([]byte(nil), eventPayload...)
	}
	if matched != 1 {
		return ToolResult{}, errors.New("decode tool result: shell stream event is unsupported")
	}
	return result, nil
}

func FinalizeShellToolResult(result ToolResult, stdout, stderr string, truncated bool) (ToolResult, error) {
	if result.Name != "Shell" || !result.Complete {
		return ToolResult{}, errors.New("finalize shell tool result: terminal Shell result is required")
	}
	result.Stdout = stdout
	result.Stderr = stderr
	result.Truncated = truncated
	if result.ShellEvent != ShellEventExit {
		return result, nil
	}
	trimmedStdout := strings.TrimSpace(stdout)
	trimmedStderr := strings.TrimSpace(stderr)
	sections := make([]string, 0, 3)
	if trimmedStdout != "" {
		sections = append(sections, trimmedStdout)
	}
	if trimmedStderr != "" {
		if trimmedStdout != "" {
			sections = append(sections, "<stderr>\n"+trimmedStderr+"\n</stderr>")
		} else {
			sections = append(sections, trimmedStderr)
		}
	}
	if truncated {
		sections = append(sections, "[output truncated by cursor-cli-byok]")
	}
	if len(sections) == 0 {
		result.Content = fmt.Sprintf("shell exited with code=%d cwd=%s", result.ExitCode, strings.TrimSpace(result.CWD))
	} else {
		result.Content = strings.Join(sections, "\n\n")
	}
	return result, nil
}

func EncodeToolProgress(result ToolResult) ([]byte, error) {
	if result.Name != "Shell" || (result.Complete && result.ShellEvent != ShellEventExit) {
		return nil, errors.New("encode tool progress: Shell progress result is required")
	}
	var progress []byte
	switch result.ShellEvent {
	case ShellEventStdout:
		progress = appendMessage(nil, 1, appendString(nil, 1, result.StdoutDelta))
	case ShellEventStderr:
		progress = appendMessage(nil, 2, appendString(nil, 1, result.StderrDelta))
	case ShellEventStart:
		progress = appendMessage(nil, 4, result.EventPayload)
	case ShellEventExit:
		progress = appendMessage(nil, 3, result.EventPayload)
	default:
		return nil, errors.New("encode tool progress: shell event is unsupported")
	}
	interaction := appendMessage(nil, 12, progress)
	return appendMessage(nil, 1, interaction), nil
}

func encodeShellDisplayToolCall(request ToolRequest, result ToolResult) ([]byte, error) {
	arguments, err := decodeShellToolArguments(request.Arguments)
	if err != nil {
		return nil, err
	}
	toolCallPayload := appendMessage(nil, 1, encodeShellArgs(arguments, request.CallID, false))
	if result.Complete {
		shellResult, encodeError := encodeShellResult(arguments, result)
		if encodeError != nil {
			return nil, encodeError
		}
		toolCallPayload = appendMessage(toolCallPayload, 2, shellResult)
	}
	if arguments.Description != "" {
		toolCallPayload = appendString(toolCallPayload, 3, arguments.Description)
	}
	return appendMessage(nil, 1, toolCallPayload), nil
}

func encodeShellResult(arguments shellToolArguments, result ToolResult) ([]byte, error) {
	switch result.ShellEvent {
	case ShellEventExit:
		var terminal []byte
		if result.ExitCode == 0 {
			success := appendString(nil, 1, arguments.Command)
			success = appendString(success, 2, fallbackString(result.CWD, arguments.WorkingDirectory))
			success = appendVarint(success, 3, uint64(result.ExitCode))
			success = appendString(success, 5, result.Stdout)
			success = appendString(success, 6, result.Stderr)
			if interleaved := interleaveShellOutput(result.Stdout, result.Stderr); interleaved != "" {
				success = appendString(success, 10, interleaved)
			}
			terminal = appendMessage(nil, 1, success)
		} else {
			failure := appendString(nil, 1, arguments.Command)
			failure = appendString(failure, 2, fallbackString(result.CWD, arguments.WorkingDirectory))
			failure = appendVarint(failure, 3, uint64(result.ExitCode))
			failure = appendString(failure, 5, result.Stdout)
			failure = appendString(failure, 6, result.Stderr)
			if interleaved := interleaveShellOutput(result.Stdout, result.Stderr); interleaved != "" {
				failure = appendString(failure, 9, interleaved)
			}
			terminal = appendMessage(nil, 2, failure)
		}
		return terminal, nil
	case ShellEventRejected:
		return appendMessage(nil, 4, result.EventPayload), nil
	case ShellEventPermissionDenied:
		return appendMessage(nil, 7, result.EventPayload), nil
	case ShellEventBackgrounded:
		success := appendString(nil, 1, arguments.Command)
		success = appendString(success, 2, fallbackString(result.CWD, arguments.WorkingDirectory))
		success = appendVarint(success, 9, uint64(result.ShellID))
		if result.PID != 0 {
			success = appendVarint(success, 11, uint64(result.PID))
		}
		shellResult := appendMessage(nil, 1, success)
		shellResult = appendVarint(shellResult, 102, 1)
		if result.PID != 0 {
			shellResult = appendVarint(shellResult, 104, uint64(result.PID))
		}
		return shellResult, nil
	default:
		return nil, errors.New("encode tool completed: shell terminal event is unsupported")
	}
}

func encodeShellArgs(arguments shellToolArguments, callID string, includeExecutionMetadata bool) []byte {
	args := appendString(nil, 1, arguments.Command)
	args = appendString(args, 2, arguments.WorkingDirectory)
	args = appendVarint(args, 3, uint64(shellTimeout(arguments)))
	args = appendString(args, 4, callID)
	if includeExecutionMetadata {
		args = appendString(args, 5, arguments.Command)
		args = appendMessage(args, 8, encodeShellParsingResult(arguments.Command))
		args = appendVarint(args, 10, 40000)
		args = appendVarint(args, 13, 2)
		args = appendVarint(args, 14, 86400000)
	}
	if arguments.Description != "" {
		args = appendString(args, 15, arguments.Description)
	}
	return args
}

func encodeShellParsingResult(command string) []byte {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}
	executable := appendString(nil, 1, parts[0])
	for _, value := range parts[1:] {
		argument := appendString(nil, 1, "word")
		argument = appendString(argument, 2, value)
		executable = appendMessage(executable, 2, argument)
	}
	executable = appendString(executable, 3, strings.TrimSpace(command))
	return appendMessage(nil, 2, executable)
}

func decodeShellToolArguments(raw string) (shellToolArguments, error) {
	var arguments shellToolArguments
	if err := decodeStrictToolArguments(raw, &arguments); err != nil {
		return shellToolArguments{}, errors.New("encode Shell tool: arguments are invalid")
	}
	arguments.Command = strings.TrimSpace(arguments.Command)
	arguments.Description = strings.TrimSpace(arguments.Description)
	arguments.WorkingDirectory = strings.TrimSpace(arguments.WorkingDirectory)
	if arguments.Command == "" || strings.IndexByte(arguments.WorkingDirectory, 0) >= 0 || (arguments.BlockUntilMS != nil && (*arguments.BlockUntilMS < 0 || *arguments.BlockUntilMS > math.MaxInt32)) {
		return shellToolArguments{}, errors.New("encode Shell tool: arguments are invalid")
	}
	return arguments, nil
}

func shellTimeout(arguments shellToolArguments) int32 {
	if arguments.BlockUntilMS == nil {
		return 30000
	}
	return int32(*arguments.BlockUntilMS)
}

func interleaveShellOutput(stdout, stderr string) string {
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	case strings.HasSuffix(stdout, "\n"):
		return stdout + stderr
	default:
		return stdout + "\n" + stderr
	}
}
