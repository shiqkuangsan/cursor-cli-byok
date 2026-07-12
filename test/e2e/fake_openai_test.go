package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2EHelperProviderLifecycleUsesEnvironmentSecretAndPrivateFiles(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	readyPath := filepath.Join(root, "provider.url")
	logPath := filepath.Join(root, "provider.jsonl")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runE2EHelper(
			ctx,
			[]string{"provider", "--api-key-env", "E2E_PROVIDER_KEY", "--workspace", workspace, "--log-file", logPath, "--ready-file", readyPath},
			strings.NewReader(""),
			&stdout,
			&stderr,
			func(name string) string {
				if name == "E2E_PROVIDER_KEY" {
					return "provider-key-from-env"
				}
				return ""
			},
		)
	}()
	waitForFile(t, readyPath)
	endpointData, err := os.ReadFile(readyPath)
	if err != nil {
		t.Fatalf("ReadFile(ready) error = %v", err)
	}
	endpoint := strings.TrimSpace(string(endpointData))
	host, _, err := net.SplitHostPort(strings.TrimPrefix(endpoint, "http://"))
	if err != nil || host != "127.0.0.1" {
		t.Fatalf("provider endpoint = %q, split error = %v", endpoint, err)
	}
	request, err := http.NewRequest(http.MethodHead, endpoint+"/v1/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer provider-key-from-env")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d", response.StatusCode)
	}
	for _, path := range []string{readyPath, logPath} {
		if path == logPath {
			postProviderRequest(t, endpoint+"/v1/responses", "provider-key-from-env", `{"input":[{"role":"user","content":"E2E_TEXT"}]}`)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(%s) error = %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("mode(%s) = %#o, want 0600", path, got)
		}
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("runE2EHelper() code = %d, stderr = %q", code, stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("provider helper did not stop after cancellation")
	}
	if strings.Contains(stdout.String()+stderr.String(), "provider-key-from-env") {
		t.Fatal("provider helper output leaked the API key")
	}
}

func TestE2EHelperMCPDispatchAndInvalidProviderOptions(t *testing.T) {
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"
	var output bytes.Buffer
	var stderr bytes.Buffer
	code := runE2EHelper(context.Background(), []string{"mcp"}, strings.NewReader(initialize), &output, &stderr, os.Getenv)
	if code != 0 || !strings.Contains(output.String(), `"name":"cursor-cli-byok-e2e"`) {
		t.Fatalf("MCP code = %d, output = %q, stderr = %q", code, output.String(), stderr.String())
	}

	stderr.Reset()
	code = runE2EHelper(context.Background(), []string{"provider", "--api-key-env", "MISSING"}, strings.NewReader(""), io.Discard, &stderr, func(string) string { return "" })
	if code == 0 || !strings.Contains(stderr.String(), "provider") || strings.Contains(stderr.String(), "provider-key-from-env") {
		t.Fatalf("invalid provider code = %d, stderr = %q", code, stderr.String())
	}
}

func TestFakeProviderStreamsResponsesTextAndReadContinuation(t *testing.T) {
	workspace := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "provider.jsonl")
	server := httptest.NewServer(newFakeProvider(fakeProviderOptions{
		APIKey:    "e2e-secret",
		Workspace: workspace,
		LogPath:   logPath,
	}))
	defer server.Close()

	text := postProviderRequest(t, server.URL+"/v1/responses", "e2e-secret", `{
		"model":"e2e-model",
		"input":[{"role":"user","content":"E2E_TEXT"}],
		"stream":true
	}`)
	if !strings.Contains(text, "E2E_RESPONSES_OK") || !strings.Contains(text, "response.completed") {
		t.Fatalf("text response = %q", text)
	}
	jsonText := postProviderRequest(t, server.URL+"/v1/responses", "e2e-secret", `{
		"model":"e2e-model",
		"input":[{"role":"user","content":"E2E_JSON"}],
		"stream":true
	}`)
	if !strings.Contains(jsonText, "E2E_JSON_OK") || !strings.Contains(jsonText, "response.completed") {
		t.Fatalf("JSON response = %q", jsonText)
	}

	first := postProviderRequest(t, server.URL+"/v1/responses", "e2e-secret", `{
		"model":"e2e-model",
		"input":[{"role":"user","content":"E2E_READ"}],
		"tools":[{"type":"function","name":"Read","parameters":{"type":"object"}}],
		"stream":true
	}`)
	if !strings.Contains(first, `"name":"Read"`) || !strings.Contains(first, `"call_id":"e2e-read-1"`) {
		t.Fatalf("first read response = %q", first)
	}

	second := postProviderRequest(t, server.URL+"/v1/responses", "e2e-secret", `{
		"model":"e2e-model",
		"input":[
			{"role":"user","content":"E2E_READ"},
			{"type":"function_call","call_id":"e2e-read-1","name":"Read","arguments":"{}"},
			{"type":"function_call_output","call_id":"e2e-read-1","output":"READ_FIXTURE_OK"}
		],
		"stream":true
	}`)
	if !strings.Contains(second, "E2E_READ_OK") {
		t.Fatalf("read continuation = %q", second)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(log) error = %v", err)
	}
	if strings.Contains(string(logData), "e2e-secret") || strings.Contains(string(logData), "READ_FIXTURE_OK") {
		t.Fatalf("provider log contains a secret or raw tool output: %s", logData)
	}
}

func TestFakeProviderStreamsChatTextAndDynamicMCPContinuation(t *testing.T) {
	server := httptest.NewServer(newFakeProvider(fakeProviderOptions{
		APIKey:    "chat-secret",
		Workspace: t.TempDir(),
		LogPath:   filepath.Join(t.TempDir(), "provider.jsonl"),
	}))
	defer server.Close()

	text := postProviderRequest(t, server.URL+"/v1/chat/completions", "chat-secret", `{
		"model":"chat-model",
		"messages":[{"role":"user","content":"E2E_TEXT"}],
		"stream":true
	}`)
	if !strings.Contains(text, "E2E_CHAT_OK") || !strings.Contains(text, "[DONE]") {
		t.Fatalf("chat text response = %q", text)
	}

	first := postProviderRequest(t, server.URL+"/v1/chat/completions", "chat-secret", `{
		"model":"chat-model",
		"messages":[{"role":"user","content":"E2E_MCP"}],
		"tools":[{"type":"function","function":{"name":"weather-server-weather_lookup","parameters":{"type":"object"}}}],
		"stream":true
	}`)
	if !strings.Contains(first, `"name":"weather-server-weather_lookup"`) || !strings.Contains(first, `"id":"e2e-mcp-1"`) {
		t.Fatalf("first MCP response = %q", first)
	}

	second := postProviderRequest(t, server.URL+"/v1/chat/completions", "chat-secret", `{
		"model":"chat-model",
		"messages":[
			{"role":"user","content":"E2E_MCP"},
			{"role":"assistant","tool_calls":[{"id":"e2e-mcp-1","type":"function","function":{"name":"weather-server-weather_lookup","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"e2e-mcp-1","content":"MCP_REAL_OK: Taipei"}
		],
		"stream":true
	}`)
	if !strings.Contains(second, "E2E_MCP_OK") {
		t.Fatalf("MCP continuation = %q", second)
	}
}

func TestFakeProviderValidatesWriteAndShellSideEffects(t *testing.T) {
	workspace := t.TempDir()
	server := httptest.NewServer(newFakeProvider(fakeProviderOptions{
		APIKey:    "side-effect-secret",
		Workspace: workspace,
		LogPath:   filepath.Join(t.TempDir(), "provider.jsonl"),
	}))
	defer server.Close()

	writeFirst := postProviderRequest(t, server.URL+"/v1/responses", "side-effect-secret", `{
		"model":"e2e-model",
		"input":[{"role":"user","content":"E2E_WRITE"}],
		"tools":[{"type":"function","name":"Write","parameters":{"type":"object"}}],
		"stream":true
	}`)
	if !strings.Contains(writeFirst, `"name":"Write"`) {
		t.Fatalf("first write response = %q", writeFirst)
	}
	if err := os.WriteFile(filepath.Join(workspace, "written.txt"), []byte("WRITE_REAL_OK\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	writeSecond := postProviderRequest(t, server.URL+"/v1/responses", "side-effect-secret", `{
		"model":"e2e-model",
		"input":[
			{"role":"user","content":"E2E_WRITE"},
			{"type":"function_call_output","call_id":"e2e-write-1","output":"write completed"}
		],
		"stream":true
	}`)
	if !strings.Contains(writeSecond, "E2E_WRITE_OK") {
		t.Fatalf("write continuation = %q", writeSecond)
	}

	shellFirst := postProviderRequest(t, server.URL+"/v1/chat/completions", "side-effect-secret", `{
		"model":"chat-model",
		"messages":[{"role":"user","content":"E2E_SHELL"}],
		"tools":[{"type":"function","function":{"name":"Shell","parameters":{"type":"object"}}}],
		"stream":true
	}`)
	if !strings.Contains(shellFirst, `"name":"Shell"`) {
		t.Fatalf("first shell response = %q", shellFirst)
	}
	if err := os.WriteFile(filepath.Join(workspace, "shell-count.txt"), []byte("once\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	shellSecond := postProviderRequest(t, server.URL+"/v1/chat/completions", "side-effect-secret", `{
		"model":"chat-model",
		"messages":[
			{"role":"user","content":"E2E_SHELL"},
			{"role":"tool","tool_call_id":"e2e-shell-1","content":"SHELL_STDOUT SHELL_STDERR SHELL_SECRET_ABSENT exit code 0"}
		],
		"stream":true
	}`)
	if !strings.Contains(shellSecond, "E2E_SHELL_OK") {
		t.Fatalf("shell continuation = %q", shellSecond)
	}
}

func TestFakeProviderFailsAfterShellContinuation(t *testing.T) {
	workspace := t.TempDir()
	server := httptest.NewServer(newFakeProvider(fakeProviderOptions{
		APIKey:    "failure-secret",
		Workspace: workspace,
		LogPath:   filepath.Join(t.TempDir(), "provider.jsonl"),
	}))
	defer server.Close()

	first := postProviderRequest(t, server.URL+"/v1/responses", "failure-secret", `{
		"model":"model",
		"input":[{"role":"user","content":"E2E_SHELL_FAIL"}],
		"tools":[{"type":"function","name":"Shell","parameters":{"type":"object"}}],
		"stream":true
	}`)
	if !strings.Contains(first, `"name":"Shell"`) || !strings.Contains(first, `"call_id":"e2e-shell-fail-1"`) {
		t.Fatalf("first shell failure response = %q", first)
	}
	if err := os.WriteFile(filepath.Join(workspace, "shell-fail-count.txt"), []byte("once\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{
		"model":"model",
		"input":[
			{"role":"user","content":"E2E_SHELL_FAIL"},
			{"type":"function_call_output","call_id":"e2e-shell-fail-1","output":"SHELL_FAIL_STDOUT exit code 0"}
		],
		"stream":true
	}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer failure-secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("continuation status = %d, want 503", response.StatusCode)
	}
}

func TestFakeProviderRejectsBadAuthFailsClosedAndObservesCancellation(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "provider.jsonl")
	server := httptest.NewServer(newFakeProvider(fakeProviderOptions{
		APIKey:    "auth-secret",
		Workspace: t.TempDir(),
		LogPath:   logPath,
	}))
	defer server.Close()

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"input":[{"role":"user","content":"E2E_TEXT"}]}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer wrong")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad auth status = %d", response.StatusCode)
	}

	failureRequest, err := http.NewRequest(http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"input":[{"role":"user","content":"E2E_FAIL"}]}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	failureRequest.Header.Set("Authorization", "Bearer auth-secret")
	failureResponse, err := http.DefaultClient.Do(failureRequest)
	if err != nil {
		t.Fatalf("Do(failure) error = %v", err)
	}
	_ = failureResponse.Body.Close()
	if failureResponse.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("failure status = %d", failureResponse.StatusCode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/responses", strings.NewReader(`{"input":[{"role":"user","content":"E2E_CANCEL"}]}`))
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	cancelRequest.Header.Set("Authorization", "Bearer auth-secret")
	done := make(chan error, 1)
	go func() {
		response, err := http.DefaultClient.Do(cancelRequest)
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		done <- err
	}()
	waitForLog(t, logPath, `"scenario":"E2E_CANCEL"`)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled provider request did not return")
	}
	waitForLog(t, logPath, `"event":"canceled"`)
}

func postProviderRequest(t *testing.T, url, apiKey, body string) string {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+apiKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.StatusCode, data)
	}
	return string(data)
}

func waitForLog(t *testing.T, path, fragment string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), fragment) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("log %s did not contain %q", path, fragment)
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() && info.Size() != 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("file %s was not created", path)
}
