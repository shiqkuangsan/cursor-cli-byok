package protocol

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestShellToolDispatchProgressAndCompletion(t *testing.T) {
	request := ToolRequest{
		MessageID: 51,
		ExecID:    "exec-shell-1",
		CallID:    "call-shell-1",
		Name:      "Shell",
		Arguments: `{"command":"printf hello","description":"print text","working_directory":"/repo","block_until_ms":2500}`,
	}
	dispatch, err := EncodeToolDispatch(request)
	if err != nil {
		t.Fatalf("EncodeToolDispatch(Shell) error = %v", err)
	}
	shellArgs := testNestedMessage(t, dispatch.Execute, []protowire.Number{2, 14})
	if got := string(testFieldBytes(t, shellArgs, 1)); got != "printf hello" {
		t.Fatalf("shell command = %q", got)
	}
	if got := testFieldVarint(t, shellArgs, 3); got != 2500 {
		t.Fatalf("shell timeout = %d", got)
	}
	if got := string(testFieldBytes(t, shellArgs, 4)); got != "call-shell-1" {
		t.Fatalf("shell call ID = %q", got)
	}
	if tool := testNestedMessage(t, dispatch.Started, []protowire.Number{1, 2, 2}); testFirstWireField(t, tool) != 1 {
		t.Fatalf("Shell started tool = %x", tool)
	}

	startStream := testMessage(4, nil)
	startResult, err := DecodeToolResult(testExecClientMessage(t, 51, "exec-shell-1", 14, startStream), request)
	if err != nil || startResult.Complete || startResult.ShellEvent != ShellEventStart {
		t.Fatalf("start result/error = %#v/%v", startResult, err)
	}
	startProgress, err := EncodeToolProgress(startResult)
	if err != nil {
		t.Fatalf("EncodeToolProgress(start) error = %v", err)
	}
	if event := testNestedMessage(t, startProgress, []protowire.Number{1, 12}); testFirstWireField(t, event) != 4 {
		t.Fatalf("start progress = %x", event)
	}

	stdoutStream := testMessage(1, testString(nil, 1, "hello\n"))
	stdoutResult, err := DecodeToolResult(testExecClientMessage(t, 51, "exec-shell-1", 14, stdoutStream), request)
	if err != nil || stdoutResult.Complete || stdoutResult.ShellEvent != ShellEventStdout || stdoutResult.StdoutDelta != "hello\n" {
		t.Fatalf("stdout result/error = %#v/%v", stdoutResult, err)
	}
	stdoutProgress, err := EncodeToolProgress(stdoutResult)
	if err != nil {
		t.Fatalf("EncodeToolProgress(stdout) error = %v", err)
	}
	if got := testNestedString(t, stdoutProgress, []protowire.Number{1, 12, 1, 1}); got != "hello\n" {
		t.Fatalf("stdout progress = %q", got)
	}

	stderrStream := testMessage(2, testString(nil, 1, "warning\n"))
	stderrResult, err := DecodeToolResult(testExecClientMessage(t, 51, "exec-shell-1", 14, stderrStream), request)
	if err != nil || stderrResult.ShellEvent != ShellEventStderr || stderrResult.StderrDelta != "warning\n" {
		t.Fatalf("stderr result/error = %#v/%v", stderrResult, err)
	}

	exit := testVarint(nil, 1, 0)
	exit = testString(exit, 2, "/repo")
	exitStream := testMessage(3, exit)
	exitResult, err := DecodeToolResult(testExecClientMessage(t, 51, "exec-shell-1", 14, exitStream), request)
	if err != nil || !exitResult.Complete || exitResult.ShellEvent != ShellEventExit || exitResult.ExitCode != 0 || exitResult.IsError {
		t.Fatalf("exit result/error = %#v/%v", exitResult, err)
	}
	exitProgress, err := EncodeToolProgress(exitResult)
	if err != nil {
		t.Fatalf("EncodeToolProgress(exit) error = %v", err)
	}
	if event := testNestedMessage(t, exitProgress, []protowire.Number{1, 12}); testFirstWireField(t, event) != 3 {
		t.Fatalf("exit progress = %x", event)
	}
	finalResult, err := FinalizeShellToolResult(exitResult, "hello\n", "warning\n", false)
	if err != nil {
		t.Fatalf("FinalizeShellToolResult() error = %v", err)
	}
	for _, want := range []string{"hello", "<stderr>", "warning"} {
		if !strings.Contains(finalResult.Content, want) {
			t.Fatalf("shell content = %q, missing %q", finalResult.Content, want)
		}
	}
	completed, err := EncodeToolCompleted(request, finalResult)
	if err != nil {
		t.Fatalf("EncodeToolCompleted(Shell) error = %v", err)
	}
	shellResult := testNestedMessage(t, completed, []protowire.Number{1, 3, 2, 1, 2})
	success := testFieldBytes(t, shellResult, 1)
	if got := string(testFieldBytes(t, success, 5)); got != "hello\n" {
		t.Fatalf("completed stdout = %q", got)
	}
	if got := string(testFieldBytes(t, success, 6)); got != "warning\n" {
		t.Fatalf("completed stderr = %q", got)
	}
}

func TestShellToolNonzeroExitAndRejectionAreErrors(t *testing.T) {
	request := ToolRequest{MessageID: 52, ExecID: "exec-shell-2", CallID: "call-shell-2", Name: "Shell", Arguments: `{"command":"false"}`}
	exit := testVarint(nil, 1, 7)
	exitStream := testMessage(3, exit)
	result, err := DecodeToolResult(testExecClientMessage(t, 52, "exec-shell-2", 14, exitStream), request)
	if err != nil || !result.Complete || !result.IsError || result.ExitCode != 7 {
		t.Fatalf("nonzero result/error = %#v/%v", result, err)
	}
	result, err = FinalizeShellToolResult(result, "", "failed", false)
	if err != nil {
		t.Fatalf("FinalizeShellToolResult(nonzero) error = %v", err)
	}
	completed, err := EncodeToolCompleted(request, result)
	if err != nil {
		t.Fatalf("EncodeToolCompleted(nonzero) error = %v", err)
	}
	shellResult := testNestedMessage(t, completed, []protowire.Number{1, 3, 2, 1, 2})
	if failure := testFieldBytes(t, shellResult, 2); testFieldVarint(t, failure, 3) != 7 {
		t.Fatalf("completed failure = %x", failure)
	}

	rejected := testString(nil, 1, "rm file")
	rejected = testString(rejected, 3, "approval required")
	rejectedStream := testMessage(5, rejected)
	rejectedResult, err := DecodeToolResult(testExecClientMessage(t, 52, "exec-shell-2", 14, rejectedStream), request)
	if err != nil || !rejectedResult.Complete || !rejectedResult.IsError || !strings.Contains(rejectedResult.Content, "approval required") {
		t.Fatalf("rejected result/error = %#v/%v", rejectedResult, err)
	}
}

func TestShellToolPreservesExplicitZeroBlockUntil(t *testing.T) {
	request := ToolRequest{MessageID: 53, ExecID: "exec-shell-3", CallID: "call-shell-3", Name: "Shell", Arguments: `{"command":"sleep 1","block_until_ms":0}`}
	dispatch, err := EncodeToolDispatch(request)
	if err != nil {
		t.Fatalf("EncodeToolDispatch() error = %v", err)
	}
	args := testNestedMessage(t, dispatch.Execute, []protowire.Number{2, 14})
	if got := testFieldVarint(t, args, 3); got != 0 {
		t.Fatalf("explicit zero timeout = %d, want 0", got)
	}
}
