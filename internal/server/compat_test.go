package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestCompatibilityHandlerServesAndReloadsConfiguredModels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "config.yaml")
	store := config.NewStore(path)
	if err := store.Save(compatConfig("relay-gpt", "relay-chat")); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	handler := NewCompatibilityHandler(store.Load)

	available := performCompatibilityRequest(t, handler, "/aiserver.v1.AiService/AvailableModels", "application/proto", []byte{0x98, 0x06, 0x01})
	if available.Code != http.StatusOK {
		t.Fatalf("AvailableModels status = %d, want 200; body = %q", available.Code, available.Body.String())
	}
	if got := protobufStrings(t, available.Body.Bytes(), 1); !equalStrings(got, []string{"relay-gpt", "relay-chat"}) {
		t.Fatalf("AvailableModels names = %#v, want configured aliases", got)
	}

	updated := compatConfig("new-default", "relay-chat")
	if err := store.Save(updated); err != nil {
		t.Fatalf("Save(updated) error = %v", err)
	}
	defaultModel := performCompatibilityRequest(t, handler, "/aiserver.v1.AiService/GetDefaultModelForCli", "application/proto", nil)
	details := protobufMessages(t, defaultModel.Body.Bytes(), 1)
	if len(details) != 1 || !equalStrings(protobufStrings(t, details[0], 1), []string{"new-default"}) {
		t.Fatalf("default model payload = %x, want reloaded default", defaultModel.Body.Bytes())
	}
	usable := performCompatibilityRequest(t, handler, "/aiserver.v1.AiService/GetUsableModels", "application/proto", nil)
	if models := protobufMessages(t, usable.Body.Bytes(), 1); len(models) != 2 {
		t.Fatalf("usable model count = %d, want 2", len(models))
	}
}

func TestCompatibilityHandlerServesAccountPrivacyAndSafeEmptyProcedures(t *testing.T) {
	store := config.NewStore(filepath.Join(t.TempDir(), "config", "config.yaml"))
	if err := store.Save(compatConfig("relay-gpt")); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	handler := NewCompatibilityHandler(store.Load)

	me := performCompatibilityRequest(t, handler, "/aiserver.v1.DashboardService/GetMe", "application/proto", nil)
	if me.Code != http.StatusOK || len(protobufStrings(t, me.Body.Bytes(), 3)) != 1 {
		t.Fatalf("GetMe status/body = %d/%x, want local account", me.Code, me.Body.Bytes())
	}
	privacy := performCompatibilityRequest(t, handler, "/aiserver.v1.DashboardService/GetUserPrivacyMode", "application/proto", nil)
	if got := protobufVarints(t, privacy.Body.Bytes(), 1); len(got) != 1 || got[0] != 1 {
		t.Fatalf("privacy mode = %#v, want no-storage enum 1", got)
	}
	serverConfig := performCompatibilityRequest(t, handler, "/aiserver.v1.ServerConfigService/GetServerConfig", "application/proto", nil)
	if got := protobufStrings(t, serverConfig.Body.Bytes(), 6); len(got) != 1 || got[0] == "" {
		t.Fatalf("server config version = %#v, want nonempty", got)
	}

	for _, procedure := range []string{
		"/aiserver.v1.DashboardService/ListMarketplaces",
		"/aiserver.v1.AnalyticsService/TrackEvents",
		"/aiserver.v1.DashboardService/GetManagedSkills",
		"/aiserver.v1.DashboardService/GetEffectiveUserPlugins",
		"/aiserver.v1.DashboardService/RegisterMarketplaceAndPlugins",
		"/aiserver.v1.AnalyticsService/SubmitLogs",
		"/aiserver.v1.DashboardService/GetGlobalCommands",
		"/v1/traces",
	} {
		response := performCompatibilityRequest(t, handler, procedure, "application/proto", nil)
		if response.Code != http.StatusOK || response.Body.Len() != 0 {
			t.Fatalf("%s status/body = %d/%x, want empty protobuf success", procedure, response.Code, response.Body.Bytes())
		}
	}
}

func TestCompatibilityHandlerSupportsJSONAndRejectsUnknownOrOversizedRequests(t *testing.T) {
	store := config.NewStore(filepath.Join(t.TempDir(), "config", "config.yaml"))
	if err := store.Save(compatConfig("relay-gpt")); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	handler := NewCompatibilityHandler(store.Load)

	jsonResponse := performCompatibilityRequest(t, handler, "/aiserver.v1.AiService/GetUsableModels", "application/json", []byte(`{"unknown":"ignored"}`))
	if jsonResponse.Code != http.StatusOK || !strings.Contains(jsonResponse.Body.String(), `"modelId":"relay-gpt"`) {
		t.Fatalf("JSON response status/body = %d/%q", jsonResponse.Code, jsonResponse.Body.String())
	}
	unknown := performCompatibilityRequest(t, handler, "/unknown.Service/Procedure", "application/proto", nil)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d, want 404", unknown.Code)
	}
	oversized := performCompatibilityRequest(t, handler, "/aiserver.v1.AnalyticsService/TrackEvents", "application/proto", bytes.Repeat([]byte{'x'}, maxCompatibilityRequestBytes+1))
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status = %d, want 413", oversized.Code)
	}
}

func performCompatibilityRequest(t *testing.T, handler http.Handler, path, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	request.Header.Set("Content-Type", contentType)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func compatConfig(names ...string) config.Config {
	models := make([]config.Model, len(names))
	for index, name := range names {
		models[index] = config.Model{
			Name:          name,
			Protocol:      config.ProtocolOpenAI,
			BaseURL:       "https://api.example.com",
			Endpoint:      config.EndpointResponses,
			APIKeyEnv:     "RELAY_API_KEY",
			UpstreamModel: "gpt-5.4",
		}
	}
	return config.Config{Version: config.CurrentVersion, DefaultModel: names[0], Models: models}
}

func protobufStrings(t *testing.T, payload []byte, number protowire.Number) []string {
	t.Helper()
	messages := protobufMessages(t, payload, number)
	result := make([]string, len(messages))
	for index, message := range messages {
		result[index] = string(message)
	}
	return result
}

func protobufMessages(t *testing.T, payload []byte, number protowire.Number) [][]byte {
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

func protobufVarints(t *testing.T, payload []byte, number protowire.Number) []uint64 {
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

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
