package protocol

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestMCPToolDispatchPreservesJSONArgumentsAndMetadata(t *testing.T) {
	request := ToolRequest{
		MessageID: 71,
		ExecID:    "exec-mcp-1",
		CallID:    "call-mcp-1",
		Name:      "CallMcpTool",
		Arguments: `{"name":"weather_lookup","server":"weather-server","tool_name":"lookup","arguments":{"city":"Taipei","days":2,"units":["c"],"verbose":true}}`,
	}
	dispatch, err := EncodeToolDispatch(request)
	if err != nil {
		t.Fatalf("EncodeToolDispatch(MCP) error = %v", err)
	}
	mcpArgs := testNestedMessage(t, dispatch.Execute, []protowire.Number{2, 11})
	if got := string(testFieldBytes(t, mcpArgs, 1)); got != "weather_lookup" {
		t.Fatalf("MCP name = %q", got)
	}
	if got := string(testFieldBytes(t, mcpArgs, 3)); got != "call-mcp-1" {
		t.Fatalf("MCP call ID = %q", got)
	}
	if got := string(testFieldBytes(t, mcpArgs, 4)); got != "weather-server" {
		t.Fatalf("MCP provider = %q", got)
	}
	if got := string(testFieldBytes(t, mcpArgs, 5)); got != "lookup" {
		t.Fatalf("MCP tool name = %q", got)
	}
	decodedArguments := map[string]any{}
	for _, entryPayload := range repeatedBytesField(mustDecodeWireFields(t, mcpArgs), 2) {
		entryFields := mustDecodeWireFields(t, entryPayload)
		key := string(testFieldBytes(t, entryPayload, 1))
		valuePayload, ok := lastBytesField(entryFields, 2)
		if !ok {
			t.Fatalf("MCP argument %q has no value", key)
		}
		value, err := decodeProtoValue(valuePayload, 0, &protoJSONBudget{})
		if err != nil {
			t.Fatalf("decode argument %q error = %v", key, err)
		}
		decodedArguments[key] = value
	}
	if decodedArguments["city"] != "Taipei" || decodedArguments["days"] != float64(2) || decodedArguments["verbose"] != true || decodedArguments["units"].([]any)[0] != "c" {
		t.Fatalf("decoded MCP arguments = %#v", decodedArguments)
	}
	if tool := testNestedMessage(t, dispatch.Started, []protowire.Number{1, 2, 2}); testFirstWireField(t, tool) != 15 {
		t.Fatalf("MCP started tool = %x", tool)
	}
}

func TestMCPToolResultConvertsTextAndStructuredContent(t *testing.T) {
	request := ToolRequest{MessageID: 72, ExecID: "exec-mcp-2", CallID: "call-mcp-2", Name: "CallMcpTool", Arguments: `{"name":"weather_lookup","server":"weather-server","tool_name":"lookup","arguments":{"city":"Taipei"}}`}
	textContent := testString(nil, 1, "sunny")
	contentItem := testMessage(1, textContent)
	structured := testProtoStructMessage([]testProtoMapEntry{{key: "temperature", value: testProtoValueNumber(24)}})
	success := testMessage(1, contentItem)
	success = testMessageInto(success, 3, structured)
	mcpResult := testMessage(1, success)
	message := testExecClientMessage(t, 72, "exec-mcp-2", 11, mcpResult)
	result, err := DecodeToolResult(message, request)
	if err != nil {
		t.Fatalf("DecodeToolResult(MCP) error = %v", err)
	}
	if result.IsError || !result.Complete || !strings.Contains(result.Content, "sunny") || !strings.Contains(result.Content, `"temperature":24`) {
		t.Fatalf("MCP result = %#v", result)
	}
	completed, err := EncodeToolCompleted(request, result)
	if err != nil {
		t.Fatalf("EncodeToolCompleted(MCP) error = %v", err)
	}
	mcpToolResult := testNestedMessage(t, completed, []protowire.Number{1, 3, 2, 15, 2})
	if successPayload := testFieldBytes(t, mcpToolResult, 1); len(successPayload) == 0 {
		t.Fatal("completed MCP success is empty")
	}
}

func TestMCPToolErrorBecomesProviderAndCursorError(t *testing.T) {
	request := ToolRequest{MessageID: 73, ExecID: "exec-mcp-3", CallID: "call-mcp-3", Name: "CallMcpTool", Arguments: `{"name":"weather_lookup","server":"weather-server","tool_name":"lookup","arguments":{}}`}
	mcpResult := testMessage(2, testString(nil, 1, "server failed"))
	result, err := DecodeToolResult(testExecClientMessage(t, 73, "exec-mcp-3", 11, mcpResult), request)
	if err != nil || !result.IsError || !strings.Contains(result.Content, "server failed") {
		t.Fatalf("MCP error result/error = %#v/%v", result, err)
	}
	completed, err := EncodeToolCompleted(request, result)
	if err != nil {
		t.Fatalf("EncodeToolCompleted(MCP error) error = %v", err)
	}
	mcpToolError := testNestedMessage(t, completed, []protowire.Number{1, 3, 2, 15, 2, 2})
	if got := string(testFieldBytes(t, mcpToolError, 1)); got != "server failed" {
		t.Fatalf("completed MCP error = %q", got)
	}
}

func testProtoStructMessage(entries []testProtoMapEntry) []byte {
	var structure []byte
	for _, item := range entries {
		entry := testString(nil, 1, item.key)
		entry = testMessageInto(entry, 2, item.value)
		structure = testMessageInto(structure, 1, entry)
	}
	return structure
}

func mustDecodeWireFields(t *testing.T, payload []byte) []wireField {
	t.Helper()
	fields, err := decodeWireFields(payload)
	if err != nil {
		t.Fatalf("decodeWireFields() error = %v", err)
	}
	return fields
}
