package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

const maxFakeProviderBodyBytes = 4 * 1024 * 1024

type fakeProviderOptions struct {
	APIKey    string
	Workspace string
	LogPath   string
}

type fakeProvider struct {
	options fakeProviderOptions
	logger  fakeProviderLogger
	nextID  atomic.Uint64
}

type fakeProviderLogger struct {
	path string
	mu   sync.Mutex
}

type fakeProviderRequest struct {
	Endpoint   string
	Scenario   string
	ToolNames  []string
	ToolOutput []string
}

type fakeProviderLogEntry struct {
	Event      string `json:"event"`
	Endpoint   string `json:"endpoint,omitempty"`
	Scenario   string `json:"scenario,omitempty"`
	RequestID  uint64 `json:"request_id,omitempty"`
	StatusCode int    `json:"status_code,omitempty"`
}

func newFakeProvider(options fakeProviderOptions) http.Handler {
	return &fakeProvider{
		options: options,
		logger:  fakeProviderLogger{path: options.LogPath},
	}
}

func (p *fakeProvider) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	endpoint := fakeProviderEndpoint(request.URL.Path)
	if endpoint == "" {
		http.NotFound(writer, request)
		return
	}
	if !p.authorized(request.Header.Get("Authorization")) {
		p.log(fakeProviderLogEntry{Event: "rejected", Endpoint: endpoint, StatusCode: http.StatusUnauthorized})
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"error":{"message":"unauthorized"}}`)
		return
	}
	if request.Method == http.MethodHead {
		writer.WriteHeader(http.StatusOK)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", "HEAD, POST")
		writer.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	decoded, err := decodeFakeProviderRequest(writer, request, endpoint)
	if err != nil {
		p.log(fakeProviderLogEntry{Event: "rejected", Endpoint: endpoint, StatusCode: http.StatusBadRequest})
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":{"message":"invalid request"}}`)
		return
	}
	requestID := p.nextID.Add(1)
	p.log(fakeProviderLogEntry{
		Event:     "request",
		Endpoint:  endpoint,
		Scenario:  decoded.Scenario,
		RequestID: requestID,
	})

	switch {
	case decoded.Scenario == "E2E_FAIL":
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(writer, `{"error":{"message":"deterministic E2E failure"}}`)
		p.log(fakeProviderLogEntry{Event: "response", Endpoint: endpoint, Scenario: decoded.Scenario, RequestID: requestID, StatusCode: http.StatusServiceUnavailable})
		return
	case decoded.Scenario == "E2E_SHELL_FAIL" && len(decoded.ToolOutput) != 0:
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(writer, `{"error":{"message":"deterministic post-tool E2E failure"}}`)
		p.log(fakeProviderLogEntry{Event: "response", Endpoint: endpoint, Scenario: decoded.Scenario, RequestID: requestID, StatusCode: http.StatusServiceUnavailable})
		return
	case decoded.Scenario == "E2E_CANCEL":
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("Cache-Control", "no-cache")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, ": waiting for cancellation\n\n")
		flush(writer)
		<-request.Context().Done()
		p.log(fakeProviderLogEntry{Event: "canceled", Endpoint: endpoint, Scenario: decoded.Scenario, RequestID: requestID})
		return
	}

	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.WriteHeader(http.StatusOK)
	if endpoint == "responses" {
		p.streamResponses(writer, decoded)
	} else {
		p.streamChat(writer, decoded)
	}
	p.log(fakeProviderLogEntry{Event: "response", Endpoint: endpoint, Scenario: decoded.Scenario, RequestID: requestID, StatusCode: http.StatusOK})
}

func (p *fakeProvider) streamResponses(writer http.ResponseWriter, request fakeProviderRequest) {
	if name, arguments, callID, ok := p.nextToolCall(request); ok {
		writeSSE(writer, map[string]any{
			"type":         "response.output_item.added",
			"output_index": 0,
			"item": map[string]any{
				"type":    "function_call",
				"call_id": callID,
				"name":    name,
			},
		})
		writeSSE(writer, map[string]any{
			"type":         "response.function_call_arguments.delta",
			"output_index": 0,
			"delta":        arguments,
		})
		writeSSE(writer, map[string]any{
			"type":         "response.function_call_arguments.done",
			"output_index": 0,
		})
		writeResponsesCompleted(writer)
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
		flush(writer)
		return
	}
	text := p.finalText(request, "responses")
	writeResponsesText(writer, text)
}

func (p *fakeProvider) streamChat(writer http.ResponseWriter, request fakeProviderRequest) {
	if name, arguments, callID, ok := p.nextToolCall(request); ok {
		writeSSE(writer, map[string]any{
			"choices": []any{map[string]any{
				"delta": map[string]any{
					"tool_calls": []any{map[string]any{
						"index": 0,
						"id":    callID,
						"type":  "function",
						"function": map[string]any{
							"name":      name,
							"arguments": arguments,
						},
					}},
				},
				"finish_reason": "tool_calls",
			}},
			"usage": fakeUsage(),
		})
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
		flush(writer)
		return
	}
	text := p.finalText(request, "chat")
	writeSSE(writer, map[string]any{
		"choices": []any{map[string]any{
			"delta":         map[string]any{"content": text},
			"finish_reason": "stop",
		}},
		"usage": fakeUsage(),
	})
	_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	flush(writer)
}

func (p *fakeProvider) nextToolCall(request fakeProviderRequest) (name, arguments, callID string, ok bool) {
	if len(request.ToolOutput) != 0 {
		return "", "", "", false
	}
	switch request.Scenario {
	case "E2E_READ":
		if hasTool(request.ToolNames, "Read") {
			return "Read", marshalArguments(map[string]any{"path": filepath.Join(p.options.Workspace, "read.txt")}), "e2e-read-1", true
		}
	case "E2E_WRITE":
		if hasTool(request.ToolNames, "Write") {
			return "Write", marshalArguments(map[string]any{
				"path":     filepath.Join(p.options.Workspace, "written.txt"),
				"contents": "WRITE_REAL_OK\n",
			}), "e2e-write-1", true
		}
	case "E2E_SHELL":
		if hasTool(request.ToolNames, "Shell") {
			return "Shell", marshalArguments(map[string]any{
				"command":           "printf 'once\\n' >> shell-count.txt; printf 'SHELL_STDOUT'; printf 'SHELL_STDERR' >&2; if [ \"${CURSOR_CLI_BYOK_E2E_PROVIDER_KEY+x}\" = x ]; then printf 'SHELL_SECRET_LEAK'; else printf 'SHELL_SECRET_ABSENT'; fi",
				"description":       "Run deterministic E2E shell command",
				"working_directory": p.options.Workspace,
				"block_until_ms":    30000,
			}), "e2e-shell-1", true
		}
	case "E2E_SHELL_FAIL":
		if hasTool(request.ToolNames, "Shell") {
			return "Shell", marshalArguments(map[string]any{
				"command":           "printf 'once\\n' >> shell-fail-count.txt; printf 'SHELL_FAIL_STDOUT'",
				"description":       "Run post-tool failure E2E shell command",
				"working_directory": p.options.Workspace,
				"block_until_ms":    30000,
			}), "e2e-shell-fail-1", true
		}
	case "E2E_MCP":
		for _, toolName := range request.ToolNames {
			if strings.HasSuffix(toolName, "-weather_lookup") {
				return toolName, marshalArguments(map[string]any{"city": "Taipei"}), "e2e-mcp-1", true
			}
		}
	}
	return "", "", "", false
}

func (p *fakeProvider) finalText(request fakeProviderRequest, endpoint string) string {
	switch request.Scenario {
	case "E2E_TEXT":
		if endpoint == "chat" {
			return "E2E_CHAT_OK"
		}
		return "E2E_RESPONSES_OK"
	case "E2E_JSON":
		return "E2E_JSON_OK"
	case "E2E_READ":
		if containsToolOutput(request.ToolOutput, "READ_FIXTURE_OK") {
			return "E2E_READ_OK"
		}
		return "E2E_READ_RESULT_MISSING"
	case "E2E_WRITE":
		data, err := os.ReadFile(filepath.Join(p.options.Workspace, "written.txt"))
		if err == nil && string(data) == "WRITE_REAL_OK\n" && len(request.ToolOutput) != 0 {
			return "E2E_WRITE_OK"
		}
		return "E2E_WRITE_RESULT_MISSING"
	case "E2E_SHELL":
		data, err := os.ReadFile(filepath.Join(p.options.Workspace, "shell-count.txt"))
		if err == nil && string(data) == "once\n" && containsToolOutput(request.ToolOutput, "SHELL_STDOUT") && containsToolOutput(request.ToolOutput, "SHELL_STDERR") && containsToolOutput(request.ToolOutput, "SHELL_SECRET_ABSENT") && !containsToolOutput(request.ToolOutput, "SHELL_SECRET_LEAK") {
			return "E2E_SHELL_OK"
		}
		return "E2E_SHELL_RESULT_MISSING"
	case "E2E_MCP":
		if containsToolOutput(request.ToolOutput, "MCP_REAL_OK: Taipei") {
			return "E2E_MCP_OK"
		}
		return "E2E_MCP_RESULT_MISSING"
	case "E2E_CONCURRENT_A":
		return "E2E_CONCURRENT_A_OK"
	case "E2E_CONCURRENT_B":
		return "E2E_CONCURRENT_B_OK"
	default:
		return "E2E_UNKNOWN_SCENARIO"
	}
}

func decodeFakeProviderRequest(writer http.ResponseWriter, request *http.Request, endpoint string) (fakeProviderRequest, error) {
	request.Body = http.MaxBytesReader(writer, request.Body, maxFakeProviderBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return fakeProviderRequest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fakeProviderRequest{}, errors.New("trailing JSON data")
	}
	result := fakeProviderRequest{Endpoint: endpoint}
	if endpoint == "responses" {
		decodeResponsesShape(raw, &result)
	} else {
		decodeChatShape(raw, &result)
	}
	if result.Scenario == "" {
		return fakeProviderRequest{}, errors.New("scenario is missing")
	}
	return result, nil
}

func decodeResponsesShape(raw map[string]any, result *fakeProviderRequest) {
	for _, item := range anySlice(raw["input"]) {
		entry, _ := item.(map[string]any)
		if entry["role"] == "user" {
			result.Scenario = findScenario(anyString(entry["content"]), result.Scenario)
		}
		if entry["type"] == "function_call_output" {
			result.ToolOutput = append(result.ToolOutput, anyString(entry["output"]))
		}
	}
	for _, item := range anySlice(raw["tools"]) {
		entry, _ := item.(map[string]any)
		if name := anyString(entry["name"]); name != "" {
			result.ToolNames = append(result.ToolNames, name)
		}
	}
}

func decodeChatShape(raw map[string]any, result *fakeProviderRequest) {
	for _, item := range anySlice(raw["messages"]) {
		entry, _ := item.(map[string]any)
		if entry["role"] == "user" {
			result.Scenario = findScenario(anyString(entry["content"]), result.Scenario)
		}
		if entry["role"] == "tool" {
			result.ToolOutput = append(result.ToolOutput, anyString(entry["content"]))
		}
	}
	for _, item := range anySlice(raw["tools"]) {
		entry, _ := item.(map[string]any)
		function, _ := entry["function"].(map[string]any)
		if name := anyString(function["name"]); name != "" {
			result.ToolNames = append(result.ToolNames, name)
		}
	}
}

func findScenario(content, current string) string {
	for _, scenario := range []string{
		"E2E_CONCURRENT_A",
		"E2E_CONCURRENT_B",
		"E2E_JSON",
		"E2E_TEXT",
		"E2E_READ",
		"E2E_WRITE",
		"E2E_SHELL_FAIL",
		"E2E_SHELL",
		"E2E_MCP",
		"E2E_CANCEL",
		"E2E_FAIL",
	} {
		if strings.Contains(content, scenario) {
			return scenario
		}
	}
	return current
}

func writeResponsesText(writer http.ResponseWriter, text string) {
	writeSSE(writer, map[string]any{"type": "response.output_text.delta", "delta": text})
	writeResponsesCompleted(writer)
	_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	flush(writer)
}

func writeResponsesCompleted(writer http.ResponseWriter) {
	writeSSE(writer, map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"usage": map[string]any{
				"input_tokens":  8,
				"output_tokens": 4,
				"input_tokens_details": map[string]any{
					"cached_tokens": 0,
				},
			},
		},
	})
}

func writeSSE(writer io.Writer, value any) {
	data, err := json.Marshal(value)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(writer, "data: %s\n\n", data)
}

func fakeUsage() map[string]any {
	return map[string]any{
		"prompt_tokens":     8,
		"completion_tokens": 4,
		"prompt_tokens_details": map[string]any{
			"cached_tokens": 0,
		},
	}
}

func fakeProviderEndpoint(path string) string {
	switch path {
	case "/v1/responses":
		return "responses"
	case "/v1/chat/completions":
		return "chat"
	default:
		return ""
	}
}

func (p *fakeProvider) authorized(header string) bool {
	want := "Bearer " + p.options.APIKey
	return len(header) == len(want) && subtle.ConstantTimeCompare([]byte(header), []byte(want)) == 1
}

func (p *fakeProvider) log(entry fakeProviderLogEntry) {
	_ = p.logger.append(entry)
}

func (l *fakeProviderLogger) append(entry fakeProviderLogEntry) error {
	if l.path == "" {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func hasTool(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func containsToolOutput(outputs []string, fragment string) bool {
	for _, output := range outputs {
		if strings.Contains(output, fragment) {
			return true
		}
	}
	return false
}

func marshalArguments(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func anySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func anyString(value any) string {
	text, _ := value.(string)
	return text
}

func flush(writer http.ResponseWriter) {
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}
