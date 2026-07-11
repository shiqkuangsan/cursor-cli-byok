package protocol

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestEncodeDeleteAndListToolDispatch(t *testing.T) {
	deleteRequest := ToolRequest{MessageID: 21, ExecID: "exec-delete-1", CallID: "call-delete-1", Name: "Delete", Arguments: `{"path":"obsolete.txt"}`}
	deleteDispatch, err := EncodeToolDispatch(deleteRequest)
	if err != nil {
		t.Fatalf("EncodeToolDispatch(Delete) error = %v", err)
	}
	deleteExec := testNestedMessage(t, deleteDispatch.Execute, []protowire.Number{2})
	deleteArgs := testFieldBytes(t, deleteExec, 4)
	if got := string(testFieldBytes(t, deleteArgs, 1)); got != "obsolete.txt" {
		t.Fatalf("delete path = %q", got)
	}
	if got := string(testFieldBytes(t, deleteArgs, 2)); got != "call-delete-1" {
		t.Fatalf("delete call ID = %q", got)
	}
	if tool := testNestedMessage(t, deleteDispatch.Started, []protowire.Number{1, 2, 2}); testFirstWireField(t, tool) != 3 {
		t.Fatalf("Delete started tool = %x", tool)
	}

	listRequest := ToolRequest{MessageID: 22, ExecID: "exec-list-1", CallID: "call-list-1", Name: "List", Arguments: `{"path":".","depth":3}`}
	listDispatch, err := EncodeToolDispatch(listRequest)
	if err != nil {
		t.Fatalf("EncodeToolDispatch(List) error = %v", err)
	}
	listExec := testNestedMessage(t, listDispatch.Execute, []protowire.Number{2})
	listArgs := testFieldBytes(t, listExec, 8)
	if got := string(testFieldBytes(t, listArgs, 1)); got != "." {
		t.Fatalf("list path = %q", got)
	}
	if got := string(testFieldBytes(t, listArgs, 3)); got != "call-list-1" {
		t.Fatalf("list call ID = %q", got)
	}
	if tool := testNestedMessage(t, listDispatch.Started, []protowire.Number{1, 2, 2}); testFirstWireField(t, tool) != 13 {
		t.Fatalf("List started tool = %x", tool)
	}
}

func TestDecodeDeleteAndListToolResults(t *testing.T) {
	deleteRequest := ToolRequest{MessageID: 31, ExecID: "exec-delete-2", CallID: "call-delete-2", Name: "Delete", Arguments: `{"path":"obsolete.txt"}`}
	deleteSuccess := testString(nil, 1, "obsolete.txt")
	deleteSuccess = testVarint(deleteSuccess, 3, 42)
	deleteResult := testMessage(1, deleteSuccess)
	deleteMessage := testExecClientMessage(t, 31, "exec-delete-2", 4, deleteResult)
	decodedDelete, err := DecodeToolResult(deleteMessage, deleteRequest)
	if err != nil {
		t.Fatalf("DecodeToolResult(Delete) error = %v", err)
	}
	if decodedDelete.IsError || !decodedDelete.Complete || !strings.Contains(decodedDelete.Content, `"path":"obsolete.txt"`) || !strings.Contains(decodedDelete.Content, `"file_size":42`) {
		t.Fatalf("delete result = %#v", decodedDelete)
	}
	deleteCompleted, err := EncodeToolCompleted(deleteRequest, decodedDelete)
	if err != nil {
		t.Fatalf("EncodeToolCompleted(Delete) error = %v", err)
	}
	if got := string(testNestedMessage(t, deleteCompleted, []protowire.Number{1, 3, 2, 3, 2, 1, 1})); got != "obsolete.txt" {
		t.Fatalf("completed delete path = %q", got)
	}

	listRequest := ToolRequest{MessageID: 32, ExecID: "exec-list-2", CallID: "call-list-2", Name: "List", Arguments: `{"path":"/repo","depth":2}`}
	root := testString(nil, 1, "/repo")
	root = testMessageInto(root, 3, testString(nil, 1, "README.md"))
	child := testString(nil, 1, "/repo/internal")
	child = testMessageInto(child, 3, testString(nil, 1, "main.go"))
	root = testMessageInto(root, 2, child)
	listSuccess := testMessage(1, root)
	listResult := testMessage(1, listSuccess)
	listMessage := testExecClientMessage(t, 32, "exec-list-2", 8, listResult)
	decodedList, err := DecodeToolResult(listMessage, listRequest)
	if err != nil {
		t.Fatalf("DecodeToolResult(List) error = %v", err)
	}
	for _, want := range []string{"/repo/README.md", "/repo/internal", "/repo/internal/main.go"} {
		if decodedList.IsError || !strings.Contains(decodedList.Content, want) {
			t.Fatalf("list result = %#v, missing %q", decodedList, want)
		}
	}
	listCompleted, err := EncodeToolCompleted(listRequest, decodedList)
	if err != nil {
		t.Fatalf("EncodeToolCompleted(List) error = %v", err)
	}
	if tool := testNestedMessage(t, listCompleted, []protowire.Number{1, 3, 2}); testFirstWireField(t, tool) != 13 {
		t.Fatalf("completed List tool = %x", tool)
	}
}

func TestEncodeAndDecodeGrepAndGlobTools(t *testing.T) {
	grepRequest := ToolRequest{MessageID: 41, ExecID: "exec-grep-1", CallID: "call-grep-1", Name: "Grep", Arguments: `{"pattern":"TODO","path":".","glob":"*.go","output_mode":"content"}`}
	grepDispatch, err := EncodeToolDispatch(grepRequest)
	if err != nil {
		t.Fatalf("EncodeToolDispatch(Grep) error = %v", err)
	}
	grepExecArgs := testNestedMessage(t, grepDispatch.Execute, []protowire.Number{2, 5})
	if got := string(testFieldBytes(t, grepExecArgs, 1)); got != "TODO" {
		t.Fatalf("grep pattern = %q", got)
	}
	if tool := testNestedMessage(t, grepDispatch.Started, []protowire.Number{1, 2, 2}); testFirstWireField(t, tool) != 5 {
		t.Fatalf("Grep started tool = %x", tool)
	}
	contentMatch := testVarint(nil, 1, 12)
	contentMatch = testString(contentMatch, 2, "TODO: fix")
	fileMatch := testString(nil, 1, "main.go")
	fileMatch = testMessageInto(fileMatch, 2, contentMatch)
	contentResult := testMessage(1, fileMatch)
	union := testMessage(3, contentResult)
	workspaceEntry := testString(nil, 1, ".")
	workspaceEntry = testMessageInto(workspaceEntry, 2, union)
	grepSuccess := testString(nil, 1, "TODO")
	grepSuccess = testString(grepSuccess, 2, ".")
	grepSuccess = testString(grepSuccess, 3, "content")
	grepSuccess = testMessageInto(grepSuccess, 4, workspaceEntry)
	grepResult := testMessage(1, grepSuccess)
	grepMessage := testExecClientMessage(t, 41, "exec-grep-1", 5, grepResult)
	decodedGrep, err := DecodeToolResult(grepMessage, grepRequest)
	if err != nil {
		t.Fatalf("DecodeToolResult(Grep) error = %v", err)
	}
	for _, want := range []string{"main.go", "TODO: fix", `"line_number":12`} {
		if decodedGrep.IsError || !strings.Contains(decodedGrep.Content, want) {
			t.Fatalf("grep result = %#v, missing %q", decodedGrep, want)
		}
	}
	grepCompleted, err := EncodeToolCompleted(grepRequest, decodedGrep)
	if err != nil {
		t.Fatalf("EncodeToolCompleted(Grep) error = %v", err)
	}
	if tool := testNestedMessage(t, grepCompleted, []protowire.Number{1, 3, 2}); testFirstWireField(t, tool) != 5 {
		t.Fatalf("completed Grep tool = %x", tool)
	}

	globRequest := ToolRequest{MessageID: 42, ExecID: "exec-glob-1", CallID: "call-glob-1", Name: "Glob", Arguments: `{"glob_pattern":"*.go","target_directory":"/repo"}`}
	globDispatch, err := EncodeToolDispatch(globRequest)
	if err != nil {
		t.Fatalf("EncodeToolDispatch(Glob) error = %v", err)
	}
	globExecArgs := testNestedMessage(t, globDispatch.Execute, []protowire.Number{2, 5})
	if got := string(testFieldBytes(t, globExecArgs, 3)); got != "*.go" {
		t.Fatalf("glob transport pattern = %q", got)
	}
	if got := string(testFieldBytes(t, globExecArgs, 4)); got != "files_with_matches" {
		t.Fatalf("glob output mode = %q", got)
	}
	if tool := testNestedMessage(t, globDispatch.Started, []protowire.Number{1, 2, 2}); testFirstWireField(t, tool) != 4 {
		t.Fatalf("Glob started tool = %x", tool)
	}
	filesResult := testString(nil, 1, "/repo/a.go")
	filesResult = testString(filesResult, 1, "/repo/b.go")
	filesResult = testVarint(filesResult, 2, 2)
	globUnion := testMessage(2, filesResult)
	globEntry := testString(nil, 1, "/repo")
	globEntry = testMessageInto(globEntry, 2, globUnion)
	globSuccess := testString(nil, 1, "")
	globSuccess = testString(globSuccess, 2, "/repo")
	globSuccess = testString(globSuccess, 3, "files_with_matches")
	globSuccess = testMessageInto(globSuccess, 4, globEntry)
	globTransportResult := testMessage(1, globSuccess)
	globMessage := testExecClientMessage(t, 42, "exec-glob-1", 5, globTransportResult)
	decodedGlob, err := DecodeToolResult(globMessage, globRequest)
	if err != nil {
		t.Fatalf("DecodeToolResult(Glob) error = %v", err)
	}
	for _, want := range []string{"/repo/a.go", "/repo/b.go"} {
		if decodedGlob.IsError || !strings.Contains(decodedGlob.Content, want) {
			t.Fatalf("glob result = %#v, missing %q", decodedGlob, want)
		}
	}
	globCompleted, err := EncodeToolCompleted(globRequest, decodedGlob)
	if err != nil {
		t.Fatalf("EncodeToolCompleted(Glob) error = %v", err)
	}
	globToolSuccess := testNestedMessage(t, globCompleted, []protowire.Number{1, 3, 2, 4, 2, 1})
	if got := string(testFieldBytes(t, globToolSuccess, 3)); got != "/repo/a.go" {
		t.Fatalf("completed Glob first file = %q", got)
	}
}

func testExecClientMessage(t *testing.T, messageID uint64, execID string, resultField protowire.Number, result []byte) ClientMessage {
	t.Helper()
	exec := testVarint(nil, 1, messageID)
	exec = testString(exec, 15, execID)
	exec = testMessageInto(exec, resultField, result)
	message, err := DecodeAgentClientMessage(testMessage(2, exec))
	if err != nil {
		t.Fatalf("DecodeAgentClientMessage() error = %v", err)
	}
	return message
}

func testFirstWireField(t *testing.T, payload []byte) protowire.Number {
	t.Helper()
	number, _, length := protowire.ConsumeTag(payload)
	if length < 0 {
		t.Fatalf("ConsumeTag() error = %d", length)
	}
	return number
}
