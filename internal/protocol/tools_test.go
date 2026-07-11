package protocol

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestEncodeWriteToolDispatchAndCompletion(t *testing.T) {
	request := ToolRequest{
		MessageID: 9,
		ExecID:    "exec-write-1",
		CallID:    "call-write-1",
		Name:      "Write",
		Arguments: `{"path":"notes.txt","contents":"one\ntwo\n"}`,
	}
	dispatch, err := EncodeToolDispatch(request)
	if err != nil {
		t.Fatalf("EncodeToolDispatch() error = %v", err)
	}
	execMessage := testNestedMessage(t, dispatch.Execute, []protowire.Number{2})
	writeArgs := testFieldBytes(t, execMessage, 3)
	if got := string(testFieldBytes(t, writeArgs, 1)); got != "notes.txt" {
		t.Fatalf("write path = %q", got)
	}
	if got := string(testFieldBytes(t, writeArgs, 2)); got != "one\ntwo\n" {
		t.Fatalf("write contents = %q", got)
	}
	if got := string(testFieldBytes(t, writeArgs, 3)); got != "call-write-1" {
		t.Fatalf("write call ID = %q", got)
	}
	if got := testFieldVarint(t, writeArgs, 4); got != 1 {
		t.Fatalf("return content = %d", got)
	}
	startedEditArgs := testNestedMessage(t, dispatch.Started, []protowire.Number{1, 2, 2, 12, 1})
	if got := string(testFieldBytes(t, startedEditArgs, 1)); got != "notes.txt" {
		t.Fatalf("started edit path = %q", got)
	}
	if got := string(testFieldBytes(t, startedEditArgs, 6)); got != "one\ntwo\n" {
		t.Fatalf("started edit content = %q", got)
	}

	success := testString(nil, 1, "notes.txt")
	success = testVarint(success, 2, 2)
	success = testVarint(success, 3, 8)
	success = testString(success, 4, "one\ntwo\n")
	writeResult := testMessage(1, success)
	exec := testVarint(nil, 1, 9)
	exec = testString(exec, 15, "exec-write-1")
	exec = testMessageInto(exec, 3, writeResult)
	message, err := DecodeAgentClientMessage(testMessage(2, exec))
	if err != nil {
		t.Fatalf("DecodeAgentClientMessage() error = %v", err)
	}
	result, err := DecodeToolResult(message, request)
	if err != nil {
		t.Fatalf("DecodeToolResult() error = %v", err)
	}
	if result.MessageID != 9 || result.ExecID != "exec-write-1" || result.IsError || !result.Complete {
		t.Fatalf("write result = %#v", result)
	}
	for _, want := range []string{`"path":"notes.txt"`, `"lines_created":2`, `"file_size":8`} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("write result content = %q, missing %q", result.Content, want)
		}
	}

	completed, err := EncodeToolCompleted(request, result)
	if err != nil {
		t.Fatalf("EncodeToolCompleted() error = %v", err)
	}
	completedEdit := testNestedMessage(t, completed, []protowire.Number{1, 3, 2, 12})
	editSuccess := testNestedMessage(t, completedEdit, []protowire.Number{2, 1})
	if got := string(testFieldBytes(t, editSuccess, 1)); got != "notes.txt" {
		t.Fatalf("completed edit path = %q", got)
	}
	if got := string(testFieldBytes(t, editSuccess, 7)); got != "one\ntwo\n" {
		t.Fatalf("completed edit content = %q", got)
	}
}

func TestDecodeWriteToolResultPreservesSanitizedFailure(t *testing.T) {
	request := ToolRequest{MessageID: 11, ExecID: "exec-write-2", CallID: "call-write-2", Name: "Write", Arguments: `{"path":"locked.txt","contents":"data"}`}
	failure := testString(nil, 1, "locked.txt")
	failure = testString(failure, 4, "permission denied")
	failure = testVarint(failure, 5, 1)
	writeResult := testMessage(3, failure)
	exec := testVarint(nil, 1, 11)
	exec = testString(exec, 15, "exec-write-2")
	exec = testMessageInto(exec, 3, writeResult)
	message, err := DecodeAgentClientMessage(testMessage(2, exec))
	if err != nil {
		t.Fatalf("DecodeAgentClientMessage() error = %v", err)
	}
	result, err := DecodeToolResult(message, request)
	if err != nil {
		t.Fatalf("DecodeToolResult() error = %v", err)
	}
	if !result.IsError || !result.Complete || !strings.Contains(result.Content, "permission denied") {
		t.Fatalf("write failure = %#v", result)
	}
	completed, err := EncodeToolCompleted(request, result)
	if err != nil {
		t.Fatalf("EncodeToolCompleted() error = %v", err)
	}
	editError := testNestedMessage(t, completed, []protowire.Number{1, 3, 2, 12, 2, 4})
	if got := string(testFieldBytes(t, editError, 2)); got != "permission denied" {
		t.Fatalf("completed permission error = %q", got)
	}
}

func TestEncodeReadToolDispatchAndCompletion(t *testing.T) {
	offset := int32(2)
	limit := uint32(20)
	request := ReadToolRequest{MessageID: 7, ExecID: "exec-read-1", CallID: "call-1", Path: "README.md", Offset: &offset, Limit: &limit}
	dispatch, err := EncodeReadToolDispatch(request)
	if err != nil {
		t.Fatalf("EncodeReadToolDispatch() error = %v", err)
	}
	execMessage := testNestedMessage(t, dispatch.Execute, []protowire.Number{2})
	if got := testFieldVarint(t, execMessage, 1); got != 7 {
		t.Fatalf("exec message ID = %d", got)
	}
	if got := string(testFieldBytes(t, execMessage, 15)); got != "exec-read-1" {
		t.Fatalf("exec ID = %q", got)
	}
	readArgs := testFieldBytes(t, execMessage, 7)
	if got := string(testFieldBytes(t, readArgs, 1)); got != "README.md" {
		t.Fatalf("read path = %q", got)
	}
	if got := string(testFieldBytes(t, readArgs, 2)); got != "call-1" {
		t.Fatalf("read call ID = %q", got)
	}
	started := testNestedMessage(t, dispatch.Started, []protowire.Number{1, 2})
	if got := string(testFieldBytes(t, started, 1)); got != "call-1" {
		t.Fatalf("started call ID = %q", got)
	}
	readToolArgs := testNestedMessage(t, started, []protowire.Number{2, 8, 1})
	if got := string(testFieldBytes(t, readToolArgs, 1)); got != "README.md" {
		t.Fatalf("tool read path = %q", got)
	}

	completed, err := EncodeReadToolCompleted(request, ReadToolResult{Path: "README.md", Content: "contents"})
	if err != nil {
		t.Fatalf("EncodeReadToolCompleted() error = %v", err)
	}
	completedUpdate := testNestedMessage(t, completed, []protowire.Number{1, 3})
	if got := string(testFieldBytes(t, completedUpdate, 1)); got != "call-1" {
		t.Fatalf("completed call ID = %q", got)
	}
	readSuccess := testNestedMessage(t, completedUpdate, []protowire.Number{2, 8, 2, 1})
	if got := string(testFieldBytes(t, readSuccess, 1)); got != "contents" {
		t.Fatalf("completed content = %q", got)
	}
}

func TestDecodeReadToolResultReadsSuccessAndErrors(t *testing.T) {
	success := testString(nil, 1, "README.md")
	success = testString(success, 2, "contents")
	readResult := testMessage(1, success)
	exec := testVarint(nil, 1, 7)
	exec = testString(exec, 15, "exec-read-1")
	exec = testMessageInto(exec, 7, readResult)
	message, err := DecodeAgentClientMessage(testMessage(2, exec))
	if err != nil {
		t.Fatalf("DecodeAgentClientMessage() error = %v", err)
	}
	result, err := DecodeReadToolResult(message)
	if err != nil {
		t.Fatalf("DecodeReadToolResult() error = %v", err)
	}
	if result.MessageID != 7 || result.ExecID != "exec-read-1" || result.Path != "README.md" || result.Content != "contents" || result.IsError {
		t.Fatalf("result = %#v", result)
	}

	readError := testString(nil, 1, "README.md")
	readError = testString(readError, 2, "permission denied")
	errorExec := testVarint(nil, 1, 8)
	errorExec = testMessageInto(errorExec, 7, testMessage(2, readError))
	errorMessage, err := DecodeAgentClientMessage(testMessage(2, errorExec))
	if err != nil {
		t.Fatalf("DecodeAgentClientMessage(error) error = %v", err)
	}
	result, err = DecodeReadToolResult(errorMessage)
	if err != nil {
		t.Fatalf("DecodeReadToolResult(error) error = %v", err)
	}
	if !result.IsError || result.Error != "permission denied" {
		t.Fatalf("error result = %#v", result)
	}
}
