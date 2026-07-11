package protocol

import (
	"errors"
	"strings"
	"unicode"

	"google.golang.org/protobuf/encoding/protowire"
)

func EncodeAvailableModels(modelNames []string, defaultModel string) ([]byte, error) {
	if err := validateModels(modelNames, defaultModel); err != nil {
		return nil, err
	}
	var payload []byte
	for _, name := range modelNames {
		payload = appendString(payload, 1, name)
		model := appendString(nil, 1, name)
		if name == defaultModel {
			model = appendBool(model, 2, true)
		}
		model = appendBool(model, 5, true)
		model = appendString(model, 17, name)
		model = appendString(model, 18, name)
		model = appendBool(model, 19, true)
		model = appendBool(model, 22, true)
		model = appendBool(model, 23, true)
		model = appendString(model, 24, name)
		model = appendBool(model, 25, true)
		payload = appendMessage(payload, 2, model)
	}
	for _, field := range []protowire.Number{4, 5, 6, 7, 8, 9, 10} {
		config := appendString(nil, 1, defaultModel)
		for _, name := range modelNames {
			config = appendString(config, 2, name)
			if field == 4 || field == 6 {
				config = appendString(config, 3, name)
			}
		}
		payload = appendMessage(payload, field, config)
	}
	return payload, nil
}

func EncodeUsableModels(modelNames []string) ([]byte, error) {
	if len(modelNames) == 0 {
		return nil, errors.New("encode models: at least one model is required")
	}
	if err := validateModels(modelNames, modelNames[0]); err != nil {
		return nil, err
	}
	var payload []byte
	for _, name := range modelNames {
		payload = appendMessage(payload, 1, encodeAgentModelDetails(name))
	}
	return payload, nil
}

func EncodeDefaultModel(modelName string) ([]byte, error) {
	if err := validateModelName(modelName); err != nil {
		return nil, err
	}
	return appendMessage(nil, 1, encodeAgentModelDetails(modelName)), nil
}

func EncodeLocalAccount() []byte {
	payload := appendString(nil, 1, "cursor-cli-byok")
	payload = appendVarint(payload, 2, 1)
	payload = appendString(payload, 3, "byok@localhost")
	payload = appendString(payload, 4, "Cursor")
	payload = appendString(payload, 5, "BYOK")
	payload = appendString(payload, 8, "2026-01-01T00:00:00Z")
	payload = appendString(payload, 11, "personal")
	payload = appendString(payload, 12, "local")
	return payload
}

func EncodeNoStoragePrivacyMode() []byte {
	payload := appendVarint(nil, 1, 1)
	return appendBool(payload, 6, true)
}

func EncodeServerConfig() []byte {
	payload := appendString(nil, 6, "cursor_cli_byok_v1")
	return appendBool(payload, 28, true)
}

func encodeAgentModelDetails(name string) []byte {
	details := appendString(nil, 1, name)
	details = appendString(details, 3, name)
	details = appendString(details, 4, name)
	details = appendString(details, 5, name)
	details = appendString(details, 6, name)
	return details
}

func validateModels(modelNames []string, defaultModel string) error {
	if len(modelNames) == 0 {
		return errors.New("encode models: at least one model is required")
	}
	seen := make(map[string]struct{}, len(modelNames))
	defaultFound := false
	for _, name := range modelNames {
		if err := validateModelName(name); err != nil {
			return err
		}
		if _, exists := seen[name]; exists {
			return errors.New("encode models: model names must be unique")
		}
		seen[name] = struct{}{}
		defaultFound = defaultFound || name == defaultModel
	}
	if !defaultFound {
		return errors.New("encode models: default model does not exist")
	}
	return nil
}

func validateModelName(name string) error {
	if name == "" || len(name) > 256 || name != strings.TrimSpace(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New("encode models: model name is invalid")
	}
	return nil
}

func appendString(payload []byte, number protowire.Number, value string) []byte {
	payload = protowire.AppendTag(payload, number, protowire.BytesType)
	return protowire.AppendString(payload, value)
}

func appendMessage(payload []byte, number protowire.Number, message []byte) []byte {
	payload = protowire.AppendTag(payload, number, protowire.BytesType)
	return protowire.AppendBytes(payload, message)
}

func appendBool(payload []byte, number protowire.Number, value bool) []byte {
	payload = protowire.AppendTag(payload, number, protowire.VarintType)
	if value {
		return protowire.AppendVarint(payload, 1)
	}
	return protowire.AppendVarint(payload, 0)
}

func appendVarint(payload []byte, number protowire.Number, value uint64) []byte {
	payload = protowire.AppendTag(payload, number, protowire.VarintType)
	return protowire.AppendVarint(payload, value)
}
