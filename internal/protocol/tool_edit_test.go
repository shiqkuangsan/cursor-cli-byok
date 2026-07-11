package protocol

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestEditToolStartsWithHiddenReadAndVisibleEdit(t *testing.T) {
	request := ToolRequest{
		MessageID: 61,
		ExecID:    "exec-edit-read-1",
		CallID:    "call-edit-1",
		Name:      "Edit",
		Arguments: `{"path":"main.go","old_string":"before","new_string":"after","replace_all":false}`,
	}
	dispatch, err := EncodeEditReadDispatch(request)
	if err != nil {
		t.Fatalf("EncodeEditReadDispatch() error = %v", err)
	}
	readArgs := testNestedMessage(t, dispatch.Execute, []protowire.Number{2, 7})
	if got := string(testFieldBytes(t, readArgs, 1)); got != "main.go" {
		t.Fatalf("hidden read path = %q", got)
	}
	if got := string(testFieldBytes(t, readArgs, 2)); got != "call-edit-1" {
		t.Fatalf("hidden read call ID = %q", got)
	}
	editArgs := testNestedMessage(t, dispatch.Started, []protowire.Number{1, 2, 2, 12, 1})
	if got := string(testFieldBytes(t, editArgs, 1)); got != "main.go" {
		t.Fatalf("visible edit path = %q", got)
	}
}

func TestApplyEditArgumentsRequiresUnambiguousMatch(t *testing.T) {
	result, replacements, err := ApplyEditArguments(`{"path":"main.go","old_string":"before","new_string":"after"}`, "one before two")
	if err != nil || replacements != 1 || result != "one after two" {
		t.Fatalf("single edit = %q/%d/%v", result, replacements, err)
	}
	if _, _, err := ApplyEditArguments(`{"path":"main.go","old_string":"same","new_string":"new"}`, "same and same"); err == nil || !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("ambiguous edit error = %v", err)
	}
	result, replacements, err = ApplyEditArguments(`{"path":"main.go","old_string":"same","new_string":"new","replace_all":true}`, "same and same")
	if err != nil || replacements != 2 || result != "new and new" {
		t.Fatalf("replace-all edit = %q/%d/%v", result, replacements, err)
	}
	if _, _, err := ApplyEditArguments(`{"path":"main.go","old_string":"missing","new_string":"new"}`, "content"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing edit error = %v", err)
	}
}

func TestEncodeEditFailureCompleted(t *testing.T) {
	request := ToolRequest{MessageID: 62, ExecID: "exec-edit-read-2", CallID: "call-edit-2", Name: "Edit", Arguments: `{"path":"main.go","old_string":"missing","new_string":"new"}`}
	completed, err := EncodeEditFailureCompleted(request, "old_string was not found")
	if err != nil {
		t.Fatalf("EncodeEditFailureCompleted() error = %v", err)
	}
	errorPayload := testNestedMessage(t, completed, []protowire.Number{1, 3, 2, 12, 2, 7})
	if got := string(testFieldBytes(t, errorPayload, 1)); got != "main.go" {
		t.Fatalf("edit failure path = %q", got)
	}
	if got := string(testFieldBytes(t, errorPayload, 2)); got != "old_string was not found" {
		t.Fatalf("edit failure detail = %q", got)
	}
}
