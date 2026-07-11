package protocol

import (
	"encoding/json"
	"math"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestDecodeRunRequestPreservesMCPToolDefinitionAndSchema(t *testing.T) {
	schema := testProtoValueStruct([]testProtoMapEntry{
		{key: "type", value: testProtoValueString("object")},
		{key: "properties", value: testProtoValueStruct([]testProtoMapEntry{
			{key: "city", value: testProtoValueStruct([]testProtoMapEntry{{key: "type", value: testProtoValueString("string")}})},
		})},
		{key: "required", value: testProtoValueList([][]byte{testProtoValueString("city")})},
		{key: "additionalProperties", value: testProtoValueBool(false)},
		{key: "minimum", value: testProtoValueNumber(1.5)},
	})
	definition := testString(nil, 1, "weather_lookup")
	definition = testString(definition, 2, "Look up weather")
	definition = testMessageInto(definition, 3, schema)
	definition = testString(definition, 4, "weather-server")
	definition = testString(definition, 5, "lookup")
	mcpTools := testMessage(1, definition)

	userMessage := testString(nil, 1, "weather")
	userMessage = testString(userMessage, 2, "message-1")
	action := testMessage(1, testMessage(1, userMessage))
	model := testString(nil, 1, "relay-gpt")
	run := testMessage(2, action)
	run = testMessageInto(run, 4, mcpTools)
	run = testString(run, 5, "conversation-1")
	run = testMessageInto(run, 9, model)
	message, err := DecodeAgentClientMessage(testMessage(1, run))
	if err != nil {
		t.Fatalf("DecodeAgentClientMessage() error = %v", err)
	}
	if message.Run == nil || len(message.Run.MCPTools) != 1 {
		t.Fatalf("run = %#v", message.Run)
	}
	tool := message.Run.MCPTools[0]
	if tool.Name != "weather_lookup" || tool.Description != "Look up weather" || tool.ProviderIdentifier != "weather-server" || tool.ToolName != "lookup" {
		t.Fatalf("MCP tool = %#v", tool)
	}
	var decoded map[string]any
	if err := json.Unmarshal(tool.InputSchema, &decoded); err != nil {
		t.Fatalf("schema JSON error = %v, schema=%s", err, tool.InputSchema)
	}
	properties := decoded["properties"].(map[string]any)
	if decoded["type"] != "object" || properties["city"].(map[string]any)["type"] != "string" || decoded["minimum"] != 1.5 || decoded["additionalProperties"] != false {
		t.Fatalf("decoded schema = %#v", decoded)
	}
}

func TestDecodeRunRequestReadsMCPFileSystemDescriptors(t *testing.T) {
	schema := testProtoValueStruct([]testProtoMapEntry{
		{key: "type", value: testProtoValueString("object")},
		{key: "properties", value: testProtoValueStruct([]testProtoMapEntry{
			{key: "city", value: testProtoValueStruct([]testProtoMapEntry{{key: "type", value: testProtoValueString("string")}})},
		})},
	})
	toolDescriptor := testString(nil, 1, "weather_lookup")
	toolDescriptor = testString(toolDescriptor, 3, "Look up weather")
	toolDescriptor = testMessageInto(toolDescriptor, 4, schema)
	descriptor := testString(nil, 1, "Weather")
	descriptor = testString(descriptor, 2, "weather-server")
	descriptor = testMessageInto(descriptor, 5, toolDescriptor)
	fileSystemOptions := testVarint(nil, 1, 1)
	fileSystemOptions = testString(fileSystemOptions, 2, "/repo")
	fileSystemOptions = testMessageInto(fileSystemOptions, 3, descriptor)

	userMessage := testString(nil, 1, "weather")
	action := testMessage(1, testMessage(1, userMessage))
	model := testString(nil, 1, "relay-gpt")
	run := testMessage(2, action)
	run = testString(run, 5, "conversation-1")
	run = testMessageInto(run, 6, fileSystemOptions)
	run = testMessageInto(run, 9, model)
	message, err := DecodeAgentClientMessage(testMessage(1, run))
	if err != nil {
		t.Fatalf("DecodeAgentClientMessage() error = %v", err)
	}
	if message.Run == nil || len(message.Run.MCPTools) != 1 {
		t.Fatalf("run = %#v", message.Run)
	}
	tool := message.Run.MCPTools[0]
	if tool.Name != "weather-server-weather_lookup" || tool.ProviderIdentifier != "weather-server" || tool.ToolName != "weather_lookup" || tool.Description != "Look up weather" {
		t.Fatalf("descriptor tool = %#v", tool)
	}
}

func TestMCPStateDiscoveryRequestAndResult(t *testing.T) {
	requestPayload, err := EncodeMCPStateRequest(81, "exec-mcp-state-1")
	if err != nil {
		t.Fatalf("EncodeMCPStateRequest() error = %v", err)
	}
	exec := testNestedMessage(t, requestPayload, []protowire.Number{2})
	if got := testFieldVarint(t, exec, 1); got != 81 {
		t.Fatalf("MCP state message ID = %d", got)
	}
	if got := string(testFieldBytes(t, exec, 15)); got != "exec-mcp-state-1" {
		t.Fatalf("MCP state exec ID = %q", got)
	}
	if payload := testFieldBytes(t, exec, 36); len(payload) != 0 {
		t.Fatalf("MCP state args = %x", payload)
	}

	schema := testProtoValueStruct([]testProtoMapEntry{{key: "type", value: testProtoValueString("object")}})
	definition := testString(nil, 1, "weather-server-weather_lookup")
	definition = testString(definition, 2, "Look up weather")
	definition = testMessageInto(definition, 3, schema)
	definition = testString(definition, 4, "weather-server")
	definition = testString(definition, 5, "weather_lookup")
	server := testString(nil, 1, "Weather")
	server = testString(server, 2, "weather-server")
	server = testMessageInto(server, 5, definition)
	stateSuccess := testMessage(1, server)
	stateResult := testMessage(1, stateSuccess)
	message := testExecClientMessage(t, 81, "exec-mcp-state-1", 36, stateResult)
	discovered, err := DecodeMCPStateResult(message, 81, "exec-mcp-state-1")
	if err != nil {
		t.Fatalf("DecodeMCPStateResult() error = %v", err)
	}
	if discovered.IsError || len(discovered.Tools) != 1 || discovered.Tools[0].Name != "weather-server-weather_lookup" || discovered.Tools[0].ProviderIdentifier != "weather-server" {
		t.Fatalf("MCP state result = %#v", discovered)
	}
}

func TestRunRequestMarksPresentEmptyMCPTools(t *testing.T) {
	userMessage := testString(nil, 1, "hello")
	action := testMessage(1, testMessage(1, userMessage))
	model := testString(nil, 1, "relay-gpt")
	run := testMessage(2, action)
	run = testMessageInto(run, 4, nil)
	run = testString(run, 5, "conversation-1")
	run = testMessageInto(run, 9, model)
	message, err := DecodeAgentClientMessage(testMessage(1, run))
	if err != nil {
		t.Fatalf("DecodeAgentClientMessage() error = %v", err)
	}
	if message.Run == nil || !message.Run.MCPToolsPresent || len(message.Run.MCPTools) != 0 {
		t.Fatalf("run = %#v", message.Run)
	}
}

type testProtoMapEntry struct {
	key   string
	value []byte
}

func testProtoValueString(value string) []byte {
	return testString(nil, 3, value)
}

func testProtoValueBool(value bool) []byte {
	var encoded uint64
	if value {
		encoded = 1
	}
	return testVarint(nil, 4, encoded)
}

func testProtoValueNumber(value float64) []byte {
	payload := protowire.AppendTag(nil, 2, protowire.Fixed64Type)
	return protowire.AppendFixed64(payload, math.Float64bits(value))
}

func testProtoValueStruct(entries []testProtoMapEntry) []byte {
	var structure []byte
	for _, item := range entries {
		entry := testString(nil, 1, item.key)
		entry = testMessageInto(entry, 2, item.value)
		structure = testMessageInto(structure, 1, entry)
	}
	return testMessage(5, structure)
}

func testProtoValueList(values [][]byte) []byte {
	var list []byte
	for _, value := range values {
		list = testMessageInto(list, 1, value)
	}
	return testMessage(6, list)
}
