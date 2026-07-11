package protocol

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestEncodeAvailableModelsPublishesAliasesAndDefault(t *testing.T) {
	payload, err := EncodeAvailableModels([]string{"relay-gpt", "relay-chat"}, "relay-chat")
	if err != nil {
		t.Fatalf("EncodeAvailableModels() error = %v", err)
	}
	if got := stringFields(t, payload, 1); !reflect.DeepEqual(got, []string{"relay-gpt", "relay-chat"}) {
		t.Fatalf("model_names = %#v, want configured aliases", got)
	}
	models := messageFields(t, payload, 2)
	if len(models) != 2 {
		t.Fatalf("models count = %d, want 2", len(models))
	}
	for index, want := range []string{"relay-gpt", "relay-chat"} {
		if got := stringFields(t, models[index], 1); !reflect.DeepEqual(got, []string{want}) {
			t.Fatalf("model[%d].name = %#v, want %q", index, got, want)
		}
		defaultOn := varintFields(t, models[index], 2)
		wantDefault := uint64(0)
		if want == "relay-chat" {
			wantDefault = 1
		}
		if len(defaultOn) > 1 || (len(defaultOn) == 1 && defaultOn[0] != wantDefault) || (wantDefault == 1 && len(defaultOn) != 1) {
			t.Fatalf("model[%d].default_on = %#v, want %d", index, defaultOn, wantDefault)
		}
	}
}

func TestEncodeUsableAndDefaultModelsUseAgentModelDetails(t *testing.T) {
	usable, err := EncodeUsableModels([]string{"relay-gpt", "relay-chat"})
	if err != nil {
		t.Fatalf("EncodeUsableModels() error = %v", err)
	}
	details := messageFields(t, usable, 1)
	if len(details) != 2 {
		t.Fatalf("usable models count = %d, want 2", len(details))
	}
	for index, alias := range []string{"relay-gpt", "relay-chat"} {
		for _, field := range []protowire.Number{1, 3, 4, 5} {
			if got := stringFields(t, details[index], field); !reflect.DeepEqual(got, []string{alias}) {
				t.Fatalf("model[%d] field %d = %#v, want %q", index, field, got, alias)
			}
		}
	}

	defaultPayload, err := EncodeDefaultModel("relay-chat")
	if err != nil {
		t.Fatalf("EncodeDefaultModel() error = %v", err)
	}
	defaultDetails := messageFields(t, defaultPayload, 1)
	if len(defaultDetails) != 1 || !reflect.DeepEqual(stringFields(t, defaultDetails[0], 1), []string{"relay-chat"}) {
		t.Fatalf("default model payload = %x, want relay-chat details", defaultPayload)
	}
}

func stringFields(t *testing.T, payload []byte, number protowire.Number) []string {
	t.Helper()
	messages := messageFields(t, payload, number)
	result := make([]string, len(messages))
	for index, message := range messages {
		result[index] = string(message)
	}
	return result
}

func messageFields(t *testing.T, payload []byte, number protowire.Number) [][]byte {
	t.Helper()
	var result [][]byte
	for len(payload) > 0 {
		fieldNumber, wireType, tagLength := protowire.ConsumeTag(payload)
		if tagLength < 0 {
			t.Fatalf("ConsumeTag() error = %d", tagLength)
		}
		payload = payload[tagLength:]
		if wireType == protowire.BytesType {
			value, valueLength := protowire.ConsumeBytes(payload)
			if valueLength < 0 {
				t.Fatalf("ConsumeBytes() error = %d", valueLength)
			}
			if fieldNumber == number {
				result = append(result, append([]byte(nil), value...))
			}
			payload = payload[valueLength:]
			continue
		}
		valueLength := protowire.ConsumeFieldValue(fieldNumber, wireType, payload)
		if valueLength < 0 {
			t.Fatalf("ConsumeFieldValue() error = %d", valueLength)
		}
		payload = payload[valueLength:]
	}
	return result
}

func varintFields(t *testing.T, payload []byte, number protowire.Number) []uint64 {
	t.Helper()
	var result []uint64
	for len(payload) > 0 {
		fieldNumber, wireType, tagLength := protowire.ConsumeTag(payload)
		if tagLength < 0 {
			t.Fatalf("ConsumeTag() error = %d", tagLength)
		}
		payload = payload[tagLength:]
		if wireType == protowire.VarintType {
			value, valueLength := protowire.ConsumeVarint(payload)
			if valueLength < 0 {
				t.Fatalf("ConsumeVarint() error = %d", valueLength)
			}
			if fieldNumber == number {
				result = append(result, value)
			}
			payload = payload[valueLength:]
			continue
		}
		valueLength := protowire.ConsumeFieldValue(fieldNumber, wireType, payload)
		if valueLength < 0 {
			t.Fatalf("ConsumeFieldValue() error = %d", valueLength)
		}
		payload = payload[valueLength:]
	}
	return result
}
