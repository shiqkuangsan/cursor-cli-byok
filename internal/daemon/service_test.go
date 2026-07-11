package daemon

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/paths"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/protocol"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestRunServicePublishesHealthyStateAndRemovesItAfterIdleShutdown(t *testing.T) {
	root := t.TempDir()
	runtimePaths := paths.Paths{
		ConfigDir:  filepath.Join(root, "config"),
		ConfigFile: filepath.Join(root, "config", "config.yaml"),
		DataDir:    filepath.Join(root, "data"),
		StateDir:   filepath.Join(root, "state"),
	}
	if err := config.NewStore(runtimePaths.ConfigFile).Save(config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: "relay-gpt",
		Models: []config.Model{{
			Name:          "relay-gpt",
			Protocol:      config.ProtocolOpenAI,
			BaseURL:       "https://api.example.com",
			Endpoint:      config.EndpointResponses,
			APIKeyEnv:     "RELAY_API_KEY",
			UpstreamModel: "gpt-5.4",
		}},
	}); err != nil {
		t.Fatalf("Save(config) error = %v", err)
	}
	lock, err := TryAcquireLock(LockPath(runtimePaths))
	if err != nil {
		t.Fatalf("TryAcquireLock() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- RunService(ctx, ServiceOptions{
			Paths:       runtimePaths,
			Version:     "dev",
			Lock:        lock,
			IdleTimeout: 120 * time.Millisecond,
			Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				_, _ = io.WriteString(writer, "ok")
			}),
		})
	}()

	store := NewStateStore(StatePath(runtimePaths))
	var state State
	deadline := time.Now().Add(3 * time.Second)
	for {
		state, err = store.Load()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("state was not published: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if state.PID != os.Getpid() || state.DaemonVersion != "dev" {
		t.Fatalf("state = %#v, want current service", state)
	}
	if err := (HTTPProbe{Timeout: time.Second}).Check(context.Background(), state); err != nil {
		t.Fatalf("health probe error = %v", err)
	}

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("RunService() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunService() did not exit after idle timeout")
	}
	if _, err := store.Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(after shutdown) error = %v, want state removal", err)
	}
	reacquired, err := TryAcquireLock(LockPath(runtimePaths))
	if err != nil {
		t.Fatalf("TryAcquireLock(after shutdown) error = %v", err)
	}
	_ = reacquired.Close()
}

func TestRunServiceStopsThroughAuthenticatedControl(t *testing.T) {
	root := t.TempDir()
	runtimePaths := paths.Paths{
		ConfigDir:  filepath.Join(root, "config"),
		ConfigFile: filepath.Join(root, "config", "config.yaml"),
		DataDir:    filepath.Join(root, "data"),
		StateDir:   filepath.Join(root, "state"),
	}
	if err := config.NewStore(runtimePaths.ConfigFile).Save(config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: "relay-gpt",
		Models: []config.Model{{
			Name: "relay-gpt", Protocol: config.ProtocolOpenAI, BaseURL: "https://api.example.com",
			Endpoint: config.EndpointResponses, APIKey: "provider-key", UpstreamModel: "upstream",
		}},
	}); err != nil {
		t.Fatalf("Save(config) error = %v", err)
	}
	lock, err := TryAcquireLock(LockPath(runtimePaths))
	if err != nil {
		t.Fatalf("TryAcquireLock() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- RunService(ctx, ServiceOptions{
			Paths: runtimePaths, Version: "dev", Lock: lock, IdleTimeout: time.Minute,
			Handler: http.NotFoundHandler(),
		})
	}()
	state := waitForServiceState(t, runtimePaths)
	if err := ShutdownService(context.Background(), state, time.Second); err != nil {
		t.Fatalf("ShutdownService() error = %v", err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("RunService() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunService() did not stop through authenticated control")
	}
	if _, err := NewStateStore(StatePath(runtimePaths)).Load(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load(after control shutdown) error = %v, want state removal", err)
	}
}

func TestRunServiceDefaultHandlerStreamsConfiguredOpenAIEndpoints(t *testing.T) {
	tests := []struct {
		name         string
		endpoint     string
		providerBody string
	}{
		{
			name:     "responses",
			endpoint: config.EndpointResponses,
			providerBody: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello from responses\"}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":4,\"output_tokens\":3}}}\n\n" +
				"data: [DONE]\n\n",
		},
		{
			name:     "chat completions",
			endpoint: config.EndpointChatCompletions,
			providerBody: "data: {\"choices\":[{\"delta\":{\"content\":\"hello from chat\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\n" +
				"data: [DONE]\n\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var providerFailure string
			providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != test.endpoint || request.Header.Get("Authorization") != "Bearer provider-key" {
					providerFailure = request.URL.Path + " " + request.Header.Get("Authorization")
				}
				if request.Header.Get("User-Agent") != "cursor-cli-byok-acceptance" {
					providerFailure = "configured provider header was not forwarded"
				}
				var body struct {
					Model  string            `json:"model"`
					Stream bool              `json:"stream"`
					Tools  []json.RawMessage `json:"tools"`
				}
				if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Model != "upstream-model" || !body.Stream {
					providerFailure = "invalid provider request body"
				}
				var toolPayloadBuilder strings.Builder
				for _, tool := range body.Tools {
					toolPayloadBuilder.Write(tool)
				}
				toolPayload := toolPayloadBuilder.String()
				if len(body.Tools) != 8 {
					providerFailure = "provider request did not offer the eight implemented tools"
				}
				for _, name := range []string{"Read", "Write", "Edit", "Delete", "List", "Glob", "Grep", "Shell"} {
					if !strings.Contains(toolPayload, `"`+name+`"`) {
						providerFailure = "provider request omitted implemented tool " + name
					}
				}
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, test.providerBody)
			}))
			defer providerServer.Close()

			root := t.TempDir()
			runtimePaths := paths.Paths{
				ConfigDir:  filepath.Join(root, "config"),
				ConfigFile: filepath.Join(root, "config", "config.yaml"),
				DataDir:    filepath.Join(root, "data"),
				StateDir:   filepath.Join(root, "state"),
			}
			if err := config.NewStore(runtimePaths.ConfigFile).Save(config.Config{
				Version:      config.CurrentVersion,
				DefaultModel: "relay-gpt",
				Models: []config.Model{{
					Name: "relay-gpt", Protocol: config.ProtocolOpenAI, BaseURL: providerServer.URL,
					Endpoint: test.endpoint, APIKey: "provider-key", Headers: map[string]string{"User-Agent": "cursor-cli-byok-acceptance"}, UpstreamModel: "upstream-model",
				}},
			}); err != nil {
				t.Fatalf("Save(config) error = %v", err)
			}
			lock, err := TryAcquireLock(LockPath(runtimePaths))
			if err != nil {
				t.Fatalf("TryAcquireLock() error = %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			serviceStopped := false
			go func() {
				result <- RunService(ctx, ServiceOptions{Paths: runtimePaths, Version: "dev", Lock: lock, IdleTimeout: time.Minute})
			}()
			t.Cleanup(func() {
				cancel()
				if serviceStopped {
					return
				}
				select {
				case <-result:
				case <-time.After(2 * time.Second):
				}
			})

			state := waitForServiceState(t, runtimePaths)
			client := trustedServiceClient(t, state.CACertPath)
			appendResponse := servicePost(t, client, state, "/aiserver.v1.BidiService/BidiAppend", "application/proto", serviceAppendPayload("request-1", "conversation-1", "relay-gpt", "hello"))
			if appendResponse.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(appendResponse.Body)
				_ = appendResponse.Body.Close()
				t.Fatalf("BidiAppend status/body = %d/%q", appendResponse.StatusCode, body)
			}
			_ = appendResponse.Body.Close()

			runBody := protocol.EncodeConnectMessage(serviceString(nil, 1, "request-1"))
			runResponse := servicePost(t, client, state, "/agent.v1.AgentService/RunSSE", "application/connect+proto", runBody)
			streamBody, err := io.ReadAll(runResponse.Body)
			_ = runResponse.Body.Close()
			if err != nil {
				t.Fatalf("ReadAll(RunSSE) error = %v", err)
			}
			if runResponse.StatusCode != http.StatusOK || runResponse.ProtoMajor != 2 {
				t.Fatalf("RunSSE status/protocol = %d/%s body=%q", runResponse.StatusCode, runResponse.Proto, streamBody)
			}
			frames := serviceFrames(t, streamBody)
			wantText, _ := protocol.EncodeTextDelta("hello from " + map[bool]string{true: "responses", false: "chat"}[test.endpoint == config.EndpointResponses])
			if len(frames) < 3 || frames[0].flag != 0 || !bytes.Equal(frames[0].payload, wantText) || frames[len(frames)-1].flag != 0x02 {
				t.Fatalf("RunSSE frames = %#v", frames)
			}
			if providerFailure != "" {
				t.Fatal(providerFailure)
			}
			cancel()
			select {
			case err := <-result:
				serviceStopped = true
				if err != nil {
					t.Fatalf("RunService() error = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("RunService() did not stop")
			}
		})
	}
}

func TestRunServiceReusesDaemonWithRotatedProviderEnvironment(t *testing.T) {
	t.Setenv("RELAY_API_KEY", "old-provider-key")
	var authorizationMu sync.Mutex
	var authorizations []string
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorizationMu.Lock()
		authorizations = append(authorizations, request.Header.Get("Authorization"))
		authorizationMu.Unlock()
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer,
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n"+
				"data: {\"type\":\"response.completed\",\"response\":{}}\n\n"+
				"data: [DONE]\n\n",
		)
	}))
	defer providerServer.Close()

	root := t.TempDir()
	runtimePaths := paths.Paths{
		ConfigDir:  filepath.Join(root, "config"),
		ConfigFile: filepath.Join(root, "config", "config.yaml"),
		DataDir:    filepath.Join(root, "data"),
		StateDir:   filepath.Join(root, "state"),
	}
	if err := config.NewStore(runtimePaths.ConfigFile).Save(config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: "relay-gpt",
		Models: []config.Model{{
			Name: "relay-gpt", Protocol: config.ProtocolOpenAI, BaseURL: providerServer.URL,
			Endpoint: config.EndpointResponses, APIKeyEnv: "RELAY_API_KEY", UpstreamModel: "upstream-model",
		}},
	}); err != nil {
		t.Fatalf("Save(config) error = %v", err)
	}
	lock, err := TryAcquireLock(LockPath(runtimePaths))
	if err != nil {
		t.Fatalf("TryAcquireLock() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	serviceStopped := false
	go func() {
		result <- RunService(ctx, ServiceOptions{Paths: runtimePaths, Version: "dev", Lock: lock, IdleTimeout: time.Minute})
	}()
	t.Cleanup(func() {
		cancel()
		if serviceStopped {
			return
		}
		select {
		case <-result:
		case <-time.After(2 * time.Second):
		}
	})

	state := waitForServiceState(t, runtimePaths)
	client := trustedServiceClient(t, state.CACertPath)
	runServiceTextRequest(t, client, state, "request-old", "conversation-old")
	if err := SyncProviderEnvironment(
		context.Background(),
		state,
		map[string]string{"RELAY_API_KEY": "rotated-provider-key"},
		time.Second,
	); err != nil {
		t.Fatalf("SyncProviderEnvironment() error = %v", err)
	}
	runServiceTextRequest(t, client, state, "request-new", "conversation-new")

	authorizationMu.Lock()
	gotAuthorizations := append([]string(nil), authorizations...)
	authorizationMu.Unlock()
	wantAuthorizations := []string{"Bearer old-provider-key", "Bearer rotated-provider-key"}
	if len(gotAuthorizations) != len(wantAuthorizations) {
		t.Fatalf("provider authorizations = %#v, want %#v", gotAuthorizations, wantAuthorizations)
	}
	for index := range wantAuthorizations {
		if gotAuthorizations[index] != wantAuthorizations[index] {
			t.Fatalf("provider authorizations = %#v, want %#v", gotAuthorizations, wantAuthorizations)
		}
	}

	cancel()
	select {
	case err := <-result:
		serviceStopped = true
		if err != nil {
			t.Fatalf("RunService() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunService() did not stop")
	}
}

func waitForServiceState(t *testing.T, runtimePaths paths.Paths) State {
	t.Helper()
	store := NewStateStore(StatePath(runtimePaths))
	deadline := time.Now().Add(3 * time.Second)
	for {
		state, err := store.Load()
		if err == nil {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("state was not published: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func trustedServiceClient(t *testing.T, caPath string) *http.Client {
	t.Helper()
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("ReadFile(CA) error = %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("AppendCertsFromPEM() = false")
	}
	return &http.Client{Transport: &http.Transport{ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}}}
}

func servicePost(t *testing.T, client *http.Client, state State, path, contentType string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, state.EndpointURL()+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+state.AuthToken)
	request.Header.Set("Content-Type", contentType)
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("Do(%s) error = %v", path, err)
	}
	return response
}

func runServiceTextRequest(t *testing.T, client *http.Client, state State, requestID, conversationID string) {
	t.Helper()
	appendResponse := servicePost(
		t,
		client,
		state,
		"/aiserver.v1.BidiService/BidiAppend",
		"application/proto",
		serviceAppendPayload(requestID, conversationID, "relay-gpt", "hello"),
	)
	if appendResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(appendResponse.Body)
		_ = appendResponse.Body.Close()
		t.Fatalf("BidiAppend status/body = %d/%q", appendResponse.StatusCode, body)
	}
	_ = appendResponse.Body.Close()

	runBody := protocol.EncodeConnectMessage(serviceString(nil, 1, requestID))
	runResponse := servicePost(t, client, state, "/agent.v1.AgentService/RunSSE", "application/connect+proto", runBody)
	body, err := io.ReadAll(runResponse.Body)
	_ = runResponse.Body.Close()
	if err != nil {
		t.Fatalf("ReadAll(RunSSE) error = %v", err)
	}
	if runResponse.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("ok")) {
		t.Fatalf("RunSSE status/body = %d/%q", runResponse.StatusCode, body)
	}
}

func serviceAppendPayload(requestID, conversationID, modelID, text string) []byte {
	user := serviceString(nil, 1, text)
	user = serviceString(user, 2, "message-1")
	userAction := serviceMessage(nil, 1, user)
	action := serviceMessage(nil, 1, userAction)
	model := serviceString(nil, 1, modelID)
	run := serviceMessage(nil, 2, action)
	run = serviceMessage(run, 3, model)
	run = serviceString(run, 5, conversationID)
	client := serviceMessage(nil, 1, run)
	requestIDMessage := serviceString(nil, 1, requestID)
	appendRequest := serviceMessage(nil, 2, requestIDMessage)
	appendRequest = serviceVarint(appendRequest, 3, 0)
	return serviceMessage(appendRequest, 4, client)
}

func serviceString(payload []byte, number protowire.Number, value string) []byte {
	payload = protowire.AppendTag(payload, number, protowire.BytesType)
	return protowire.AppendString(payload, value)
}

func serviceMessage(payload []byte, number protowire.Number, value []byte) []byte {
	payload = protowire.AppendTag(payload, number, protowire.BytesType)
	return protowire.AppendBytes(payload, value)
}

func serviceVarint(payload []byte, number protowire.Number, value uint64) []byte {
	payload = protowire.AppendTag(payload, number, protowire.VarintType)
	return protowire.AppendVarint(payload, value)
}

type serviceFrame struct {
	flag    byte
	payload []byte
}

func serviceFrames(t *testing.T, body []byte) []serviceFrame {
	t.Helper()
	var frames []serviceFrame
	for len(body) > 0 {
		if len(body) < 5 {
			t.Fatalf("truncated frame: %x", body)
		}
		length := int(binary.BigEndian.Uint32(body[1:5]))
		if len(body) < 5+length {
			t.Fatalf("truncated frame body: %x", body)
		}
		frames = append(frames, serviceFrame{flag: body[0], payload: append([]byte(nil), body[5:5+length]...)})
		body = body[5+length:]
	}
	return frames
}
