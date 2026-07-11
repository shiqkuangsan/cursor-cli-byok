package protocol

import (
	"encoding/hex"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestDecodeBidiAppendRequestReadsBinaryRunMessage(t *testing.T) {
	agentClient := testRunClientMessage("conversation-1", "relay-gpt", "hello", "message-1")
	requestID := testMessage(1, []byte("request-1"))
	payload := testMessage(2, requestID)
	payload = testVarint(payload, 3, 7)
	payload = testMessageInto(payload, 4, agentClient)
	payload = testString(payload, 99, "ignored")

	appendRequest, err := DecodeBidiAppendRequest(payload)
	if err != nil {
		t.Fatalf("DecodeBidiAppendRequest() error = %v", err)
	}
	if appendRequest.RequestID != "request-1" || appendRequest.AppendSequence != 7 {
		t.Fatalf("append request = %#v", appendRequest)
	}
	if appendRequest.Message.Kind != ClientMessageRun || appendRequest.Message.Run == nil {
		t.Fatalf("client message = %#v, want run", appendRequest.Message)
	}
	want := RunRequest{
		ConversationID: "conversation-1",
		ModelID:        "relay-gpt",
		UserText:       "hello",
		UserMessageID:  "message-1",
	}
	if !appendRequest.Message.Run.Equal(want) {
		t.Fatalf("run request = %#v, want %#v", *appendRequest.Message.Run, want)
	}
}

func TestDecodeBidiAppendRequestSupportsLegacyHexAndPrefersBinary(t *testing.T) {
	legacy := testRunClientMessage("legacy-conversation", "legacy-model", "legacy", "legacy-message")
	binary := testRunClientMessage("binary-conversation", "binary-model", "binary", "binary-message")
	requestID := testMessage(1, []byte("request-2"))
	payload := testString(nil, 1, hex.EncodeToString(legacy))
	payload = testMessageInto(payload, 2, requestID)
	payload = testMessageInto(payload, 4, binary)

	appendRequest, err := DecodeBidiAppendRequest(payload)
	if err != nil {
		t.Fatalf("DecodeBidiAppendRequest() error = %v", err)
	}
	if appendRequest.Message.Run == nil || appendRequest.Message.Run.UserText != "binary" {
		t.Fatalf("run request = %#v, want binary payload", appendRequest.Message.Run)
	}

	legacyPayload := testString(nil, 1, hex.EncodeToString(legacy))
	legacyPayload = testMessageInto(legacyPayload, 2, requestID)
	legacyRequest, err := DecodeBidiAppendRequest(legacyPayload)
	if err != nil {
		t.Fatalf("DecodeBidiAppendRequest(legacy) error = %v", err)
	}
	if legacyRequest.Message.Run == nil || legacyRequest.Message.Run.UserText != "legacy" {
		t.Fatalf("legacy run request = %#v", legacyRequest.Message.Run)
	}
}

func TestDecodeAgentClientMessageReadsRequestedModel(t *testing.T) {
	userMessage := testString(nil, 1, "hello")
	userMessage = testString(userMessage, 2, "message-1")
	userAction := testMessage(1, userMessage)
	action := testMessage(1, userAction)
	requestedModel := testString(nil, 1, "relay-gpt")
	run := testMessage(2, action)
	run = testString(run, 5, "conversation-1")
	run = testMessageInto(run, 9, requestedModel)

	message, err := DecodeAgentClientMessage(testMessage(1, run))
	if err != nil {
		t.Fatalf("DecodeAgentClientMessage() error = %v", err)
	}
	if message.Run == nil || message.Run.ModelID != "relay-gpt" || message.Run.UserText != "hello" {
		t.Fatalf("run = %#v", message.Run)
	}
}

func TestDecodeAgentClientMessageReadsBackgroundTaskCompletionRun(t *testing.T) {
	completion := testString(nil, 1, "task-1")
	completion = testVarint(completion, 2, 1)
	completion = testVarint(completion, 3, 1)
	completion = testString(completion, 4, "Append once to shell-count.txt")
	completion = testVarint(completion, 8, 1)
	completion = testString(completion, 10, "call-shell-1")
	completionAction := testMessage(1, completion)
	action := testMessage(12, completionAction)
	model := testString(nil, 1, "relay-gpt")
	run := testMessage(1, nil)
	run = testMessageInto(run, 2, action)
	run = testMessageInto(run, 3, model)
	run = testMessageInto(run, 4, nil)
	run = testString(run, 5, "conversation-1")

	message, err := DecodeAgentClientMessage(testMessage(1, run))
	if err != nil {
		t.Fatalf("DecodeAgentClientMessage() error = %v", err)
	}
	if message.Kind != ClientMessageRun || message.Run == nil {
		t.Fatalf("client message = %#v, want metadata Run", message)
	}
	if message.Run.ConversationID != "conversation-1" || message.Run.ModelID != "relay-gpt" || message.Run.UserText != "" || !message.Run.MetadataOnly {
		t.Fatalf("metadata run = %#v", *message.Run)
	}
}

func TestDecodeBidiAppendRequestRejectsMalformedOrIncompleteMessages(t *testing.T) {
	validRun := testRunClientMessage("conversation-1", "relay-gpt", "hello", "message-1")
	validID := testMessage(1, []byte("request-1"))
	tests := []struct {
		name    string
		payload []byte
		want    string
	}{
		{name: "malformed protobuf", payload: []byte{0x0a, 0xff}, want: "malformed"},
		{name: "missing request id", payload: testMessage(4, validRun), want: "request_id"},
		{name: "missing client data", payload: testMessage(2, validID), want: "data"},
		{name: "invalid hex", payload: testMessageInto(testString(nil, 1, "not-hex"), 2, validID), want: "hex"},
		{name: "empty conversation", payload: testMessageInto(testMessage(2, validID), 4, testRunClientMessage("", "relay-gpt", "hello", "message-1")), want: "conversation_id"},
		{name: "empty model", payload: testMessageInto(testMessage(2, validID), 4, testRunClientMessage("conversation-1", "", "hello", "message-1")), want: "model"},
		{name: "empty text", payload: testMessageInto(testMessage(2, validID), 4, testRunClientMessage("conversation-1", "relay-gpt", "", "message-1")), want: "user message"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeBidiAppendRequest(test.payload)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeBidiAppendRequest() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestDecodeBidiRequestIDIgnoresUnknownFields(t *testing.T) {
	payload := testString(nil, 9, "ignored")
	payload = testString(payload, 1, "request-3")
	requestID, err := DecodeBidiRequestID(payload)
	if err != nil {
		t.Fatalf("DecodeBidiRequestID() error = %v", err)
	}
	if requestID != "request-3" {
		t.Fatalf("request ID = %q, want request-3", requestID)
	}
}

func TestEncodeAgentServerEvents(t *testing.T) {
	textPayload, err := EncodeTextDelta("hello")
	if err != nil {
		t.Fatalf("EncodeTextDelta() error = %v", err)
	}
	if got := testNestedString(t, textPayload, []protowire.Number{1, 1, 1}); got != "hello" {
		t.Fatalf("text delta = %q, want hello", got)
	}
	reasoningPayload, err := EncodeThinkingDelta("thinking")
	if err != nil {
		t.Fatalf("EncodeThinkingDelta() error = %v", err)
	}
	if got := testNestedString(t, reasoningPayload, []protowire.Number{1, 4, 1}); got != "thinking" {
		t.Fatalf("thinking delta = %q, want thinking", got)
	}

	turnPayload, err := EncodeTurnEnded(TokenUsage{InputTokens: 11, OutputTokens: 7, CacheReadTokens: 3, CacheWriteTokens: 2})
	if err != nil {
		t.Fatalf("EncodeTurnEnded() error = %v", err)
	}
	turnEnded := testNestedMessage(t, turnPayload, []protowire.Number{1, 14})
	for field, want := range map[protowire.Number]uint64{1: 11, 2: 7, 3: 3, 4: 2} {
		if got := testFieldVarint(t, turnEnded, field); got != want {
			t.Fatalf("turn-ended field %d = %d, want %d", field, got, want)
		}
	}

	heartbeat, err := EncodeHeartbeat()
	if err != nil {
		t.Fatalf("EncodeHeartbeat() error = %v", err)
	}
	if nested := testNestedMessage(t, heartbeat, []protowire.Number{1, 13}); len(nested) != 0 {
		t.Fatalf("heartbeat payload = %x, want empty message", nested)
	}
}

func testRunClientMessage(conversationID, modelID, text, messageID string) []byte {
	userMessage := testString(nil, 1, text)
	userMessage = testString(userMessage, 2, messageID)
	userMessageAction := testMessage(1, userMessage)
	conversationAction := testMessage(1, userMessageAction)
	model := testString(nil, 1, modelID)
	run := testMessage(2, conversationAction)
	run = testMessageInto(run, 3, model)
	run = testString(run, 5, conversationID)
	return testMessage(1, run)
}

func testMessage(number protowire.Number, value []byte) []byte {
	return testMessageInto(nil, number, value)
}

func testMessageInto(payload []byte, number protowire.Number, value []byte) []byte {
	payload = protowire.AppendTag(payload, number, protowire.BytesType)
	return protowire.AppendBytes(payload, value)
}

func testString(payload []byte, number protowire.Number, value string) []byte {
	payload = protowire.AppendTag(payload, number, protowire.BytesType)
	return protowire.AppendString(payload, value)
}

func testVarint(payload []byte, number protowire.Number, value uint64) []byte {
	payload = protowire.AppendTag(payload, number, protowire.VarintType)
	return protowire.AppendVarint(payload, value)
}

func testNestedString(t *testing.T, payload []byte, path []protowire.Number) string {
	t.Helper()
	message := testNestedMessage(t, payload, path[:len(path)-1])
	return string(testFieldBytes(t, message, path[len(path)-1]))
}

func testNestedMessage(t *testing.T, payload []byte, path []protowire.Number) []byte {
	t.Helper()
	message := payload
	for _, field := range path {
		message = testFieldBytes(t, message, field)
	}
	return message
}

func testFieldBytes(t *testing.T, payload []byte, wanted protowire.Number) []byte {
	t.Helper()
	for len(payload) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(payload)
		if tagLength < 0 {
			t.Fatalf("ConsumeTag() error = %d", tagLength)
		}
		payload = payload[tagLength:]
		if wireType == protowire.BytesType {
			value, valueLength := protowire.ConsumeBytes(payload)
			if valueLength < 0 {
				t.Fatalf("ConsumeBytes() error = %d", valueLength)
			}
			if number == wanted {
				return value
			}
			payload = payload[valueLength:]
			continue
		}
		length := protowire.ConsumeFieldValue(number, wireType, payload)
		if length < 0 {
			t.Fatalf("ConsumeFieldValue() error = %d", length)
		}
		payload = payload[length:]
	}
	t.Fatalf("field %d not found", wanted)
	return nil
}

func testFieldVarint(t *testing.T, payload []byte, wanted protowire.Number) uint64 {
	t.Helper()
	for len(payload) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(payload)
		if tagLength < 0 {
			t.Fatalf("ConsumeTag() error = %d", tagLength)
		}
		payload = payload[tagLength:]
		if wireType == protowire.VarintType {
			value, valueLength := protowire.ConsumeVarint(payload)
			if valueLength < 0 {
				t.Fatalf("ConsumeVarint() error = %d", valueLength)
			}
			if number == wanted {
				return value
			}
			payload = payload[valueLength:]
			continue
		}
		length := protowire.ConsumeFieldValue(number, wireType, payload)
		if length < 0 {
			t.Fatalf("ConsumeFieldValue() error = %d", length)
		}
		payload = payload[length:]
	}
	t.Fatalf("field %d not found", wanted)
	return 0
}
