package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/agent"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/protocol"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/provider"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestAgentHandlerStreamsRunStartedBeforeBidiAppend(t *testing.T) {
	executor := AgentExecutorFunc(func(ctx context.Context, run protocol.RunRequest, emit func(AgentEvent) error) error {
		if run.ConversationID != "conversation-1" || run.ModelID != "relay-gpt" || run.UserText != "hello" {
			t.Fatalf("run = %#v", run)
		}
		if err := emit(AgentEvent{Kind: AgentEventTextDelta, Text: "hello "}); err != nil {
			return err
		}
		if err := emit(AgentEvent{Kind: AgentEventTextDelta, Text: "world"}); err != nil {
			return err
		}
		if err := emit(AgentEvent{Kind: AgentEventReasoningDelta, Text: "thinking"}); err != nil {
			return err
		}
		return emit(AgentEvent{Kind: AgentEventUsage, Usage: protocol.TokenUsage{InputTokens: 4, OutputTokens: 2}})
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})

	streamResult := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		streamResult <- performRunSSE(handler, "request-1")
	}()
	time.Sleep(10 * time.Millisecond)

	appendResponse := performBidiAppend(handler, agentTestAppendPayload("request-1", 0, "conversation-1", "relay-gpt", "hello"))
	if appendResponse.Code != http.StatusOK || appendResponse.Body.Len() != 0 {
		t.Fatalf("BidiAppend status/body = %d/%q", appendResponse.Code, appendResponse.Body.String())
	}

	select {
	case stream := <-streamResult:
		if stream.Code != http.StatusOK {
			t.Fatalf("RunSSE status/body = %d/%q", stream.Code, stream.Body.String())
		}
		if contentType := stream.Header().Get("Content-Type"); contentType != "application/connect+proto" {
			t.Fatalf("Content-Type = %q", contentType)
		}
		frames := agentTestFrames(t, stream.Body.Bytes())
		if len(frames) != 5 {
			t.Fatalf("frame count = %d, want 5", len(frames))
		}
		textOne, _ := protocol.EncodeTextDelta("hello ")
		textTwo, _ := protocol.EncodeTextDelta("world")
		reasoning, _ := protocol.EncodeThinkingDelta("thinking")
		turnEnded, _ := protocol.EncodeTurnEnded(protocol.TokenUsage{InputTokens: 4, OutputTokens: 2})
		for index, want := range [][]byte{textOne, textTwo, reasoning, turnEnded} {
			if frames[index].flag != 0 || !bytes.Equal(frames[index].payload, want) {
				t.Fatalf("frame %d = flag %d payload %x, want %x", index, frames[index].flag, frames[index].payload, want)
			}
		}
		if frames[4].flag != 0x02 {
			t.Fatalf("terminal flag = %d, want 2", frames[4].flag)
		}
		var terminal struct {
			Metadata map[string][]string `json:"metadata"`
		}
		if err := json.Unmarshal(frames[4].payload, &terminal); err != nil || terminal.Metadata == nil {
			t.Fatalf("terminal payload = %q, error = %v", frames[4].payload, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunSSE did not finish")
	}
}

func TestAgentHandlerStreamsDirectBidiRun(t *testing.T) {
	executor := AgentExecutorFunc(func(ctx context.Context, run protocol.RunRequest, emit func(AgentEvent) error) error {
		if run.ConversationID != "direct-conversation" || run.ModelID != "relay-gpt" || run.UserText != "hello direct" {
			return errors.New("unexpected direct run")
		}
		if err := emit(AgentEvent{Kind: AgentEventTextDelta, Text: "direct response"}); err != nil {
			return err
		}
		return emit(AgentEvent{Kind: AgentEventUsage, Usage: protocol.TokenUsage{InputTokens: 2, OutputTokens: 2}})
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})
	body := protocol.EncodeConnectMessage(agentTestClientPayload("direct-conversation", "relay-gpt", "hello direct"))
	request := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/connect+proto")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/connect+proto" {
		t.Fatalf("Run status/content type = %d/%q body=%q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	frames := agentTestFrames(t, response.Body.Bytes())
	wantText, _ := protocol.EncodeTextDelta("direct response")
	wantEnded, _ := protocol.EncodeTurnEnded(protocol.TokenUsage{InputTokens: 2, OutputTokens: 2})
	if len(frames) != 3 || frames[0].flag != 0 || !bytes.Equal(frames[0].payload, wantText) || !bytes.Equal(frames[1].payload, wantEnded) || frames[2].flag != 0x02 {
		t.Fatalf("Run frames = %#v", frames)
	}
}

func TestAgentHandlerAcknowledgesBackgroundTaskCompletionWithoutExecuting(t *testing.T) {
	var calls atomic.Int32
	executor := AgentExecutorFunc(func(context.Context, protocol.RunRequest, func(AgentEvent) error) error {
		calls.Add(1)
		return nil
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{})

	response := performDirectRunPayload(handler, agentTestBackgroundTaskCompletionPayload("conversation-1", "relay-gpt"))

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/connect+proto" {
		t.Fatalf("Run status/content type = %d/%q body=%q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("executor calls = %d, want 0", calls.Load())
	}
	frames := agentTestFrames(t, response.Body.Bytes())
	wantEnded, err := protocol.EncodeTurnEnded(protocol.TokenUsage{})
	if err != nil {
		t.Fatalf("EncodeTurnEnded() error = %v", err)
	}
	if len(frames) != 2 || frames[0].flag != 0 || !bytes.Equal(frames[0].payload, wantEnded) || frames[1].flag != 0x02 {
		t.Fatalf("metadata Run frames = %#v", frames)
	}
}

func TestDirectRunWaitsForClientHalfCloseAfterServerEnd(t *testing.T) {
	executor := AgentExecutorFunc(func(ctx context.Context, run protocol.RunRequest, emit func(AgentEvent) error) error {
		return emit(AgentEvent{Kind: AgentEventTextDelta, Text: "done"})
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})
	reader, writer := io.Pipe()
	request := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", reader)
	request.Header.Set("Content-Type", "application/connect+proto")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	if _, err := writer.Write(protocol.EncodeConnectMessage(agentTestClientPayload("half-close-conversation", "relay-gpt", "hello"))); err != nil {
		t.Fatalf("Write(initial) error = %v", err)
	}
	select {
	case <-done:
		t.Fatal("direct Run returned before the client half-closed its upload stream")
	case <-time.After(50 * time.Millisecond):
	}
	if _, err := writer.Write(protocol.EncodeConnectEnd("", "")); err != nil {
		t.Fatalf("Write(end stream) error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close(upload) error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("direct Run did not return after the client half-closed")
	}
}

func TestAgentHandlerDispatchesReadAndFeedsResult(t *testing.T) {
	toolDispatched := make(chan struct{})
	executor := AgentExecutorFunc(func(ctx context.Context, run protocol.RunRequest, emit func(AgentEvent) error) error {
		resultChannel := make(chan agent.ToolResult, 1)
		if err := emit(AgentEvent{
			Kind:   AgentEventToolCall,
			Tool:   agent.ToolCall{ID: "call-1", Name: "Read", Arguments: `{"path":"README.md"}`},
			Result: resultChannel,
		}); err != nil {
			return err
		}
		close(toolDispatched)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-resultChannel:
			if result.CallID != "call-1" || result.Content != "file contents" || result.IsError {
				return errors.New("unexpected read result")
			}
		}
		return emit(AgentEvent{Kind: AgentEventTextDelta, Text: "read complete"})
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})
	initialBody := protocol.EncodeConnectMessage(agentTestClientPayload("tool-conversation", "relay-gpt", "read it"))
	request := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(initialBody))
	request.Header.Set("Content-Type", "application/connect+proto")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-toolDispatched:
	case <-time.After(time.Second):
		t.Fatal("Read tool was not dispatched")
	}
	resultRequest := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(protocol.EncodeConnectMessage(agentTestReadResultPayload(1, "file contents"))))
	resultRequest.Header.Set("Content-Type", "application/connect+proto")
	resultResponse := httptest.NewRecorder()
	handler.ServeHTTP(resultResponse, resultRequest)
	if resultResponse.Code != http.StatusOK {
		t.Fatalf("detached result status/body = %d/%q", resultResponse.Code, resultResponse.Body.String())
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("direct Run did not finish after Read result")
	}
	frames := agentTestFrames(t, response.Body.Bytes())
	if len(frames) < 6 {
		t.Fatalf("frames = %#v", frames)
	}
	if field := agentTestFirstField(t, frames[0].payload); field != 2 {
		t.Fatalf("first message field = %d, want ExecServerMessage(2)", field)
	}
	if field := agentTestInteractionField(t, frames[1].payload); field != 2 {
		t.Fatalf("second interaction field = %d, want ToolCallStarted(2)", field)
	}
	if field := agentTestInteractionField(t, frames[2].payload); field != 3 {
		t.Fatalf("third interaction field = %d, want ToolCallCompleted(3)", field)
	}
}

func TestAgentHandlerDispatchesWriteExactlyOnceAndFeedsResult(t *testing.T) {
	toolDispatched := make(chan struct{})
	executions := 0
	executor := AgentExecutorFunc(func(ctx context.Context, run protocol.RunRequest, emit func(AgentEvent) error) error {
		resultChannel := make(chan agent.ToolResult, 1)
		executions++
		if err := emit(AgentEvent{
			Kind:   AgentEventToolCall,
			Tool:   agent.ToolCall{ID: "call-write-1", Name: "Write", Arguments: `{"path":"notes.txt","contents":"hello\n"}`},
			Result: resultChannel,
		}); err != nil {
			return err
		}
		close(toolDispatched)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-resultChannel:
			if result.CallID != "call-write-1" || result.IsError || !strings.Contains(result.Content, `"path":"notes.txt"`) {
				return errors.New("unexpected write result")
			}
		}
		return emit(AgentEvent{Kind: AgentEventTextDelta, Text: "write complete"})
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})
	initialBody := protocol.EncodeConnectMessage(agentTestClientPayload("write-conversation", "relay-gpt", "write it"))
	request := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(initialBody))
	request.Header.Set("Content-Type", "application/connect+proto")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-toolDispatched:
	case <-time.After(time.Second):
		t.Fatal("Write tool was not dispatched")
	}
	resultRequest := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(protocol.EncodeConnectMessage(agentTestWriteResultPayload(1, "notes.txt", "hello\n"))))
	resultRequest.Header.Set("Content-Type", "application/connect+proto")
	resultResponse := httptest.NewRecorder()
	handler.ServeHTTP(resultResponse, resultRequest)
	if resultResponse.Code != http.StatusOK {
		t.Fatalf("detached result status/body = %d/%q", resultResponse.Code, resultResponse.Body.String())
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("direct Run did not finish after Write result")
	}
	if executions != 1 {
		t.Fatalf("Write execution count = %d, want 1", executions)
	}
	frames := agentTestFrames(t, response.Body.Bytes())
	if len(frames) < 6 {
		t.Fatalf("frames = %#v", frames)
	}
	execMessage := agentTestNestedField(t, frames[0].payload, 2)
	if field := agentTestFirstFieldAfterIdentity(t, execMessage); field != 3 {
		t.Fatalf("exec tool field = %d, want WriteArgs(3)", field)
	}
	startedTool := agentTestNestedField(t, frames[1].payload, 1, 2, 2)
	if field := agentTestFirstField(t, startedTool); field != 12 {
		t.Fatalf("started tool field = %d, want EditToolCall(12)", field)
	}
	if field := agentTestInteractionField(t, frames[2].payload); field != 3 {
		t.Fatalf("third interaction field = %d, want ToolCallCompleted(3)", field)
	}
}

func TestAgentHandlerRoutesGenericFileToolResults(t *testing.T) {
	tests := []struct {
		name          string
		arguments     string
		resultPayload func(uint64) []byte
		wantContent   string
	}{
		{name: "Delete", arguments: `{"path":"obsolete.txt"}`, resultPayload: agentTestDeleteResultPayload, wantContent: "obsolete.txt"},
		{name: "List", arguments: `{"path":"/repo","depth":2}`, resultPayload: agentTestListResultPayload, wantContent: "/repo/README.md"},
		{name: "Grep", arguments: `{"pattern":"TODO","path":".","output_mode":"content"}`, resultPayload: agentTestGrepResultPayload, wantContent: "TODO: fix"},
		{name: "Glob", arguments: `{"glob_pattern":"*.go","target_directory":"/repo"}`, resultPayload: agentTestGrepResultPayload, wantContent: "/repo/main.go"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatched := make(chan struct{})
			executor := AgentExecutorFunc(func(ctx context.Context, run protocol.RunRequest, emit func(AgentEvent) error) error {
				resultChannel := make(chan agent.ToolResult, 1)
				if err := emit(AgentEvent{Kind: AgentEventToolCall, Tool: agent.ToolCall{ID: "call-1", Name: test.name, Arguments: test.arguments}, Result: resultChannel}); err != nil {
					return err
				}
				close(dispatched)
				select {
				case <-ctx.Done():
					return ctx.Err()
				case result := <-resultChannel:
					if result.CallID != "call-1" || result.IsError || !strings.Contains(result.Content, test.wantContent) {
						return errors.New("unexpected generic tool result")
					}
				}
				return emit(AgentEvent{Kind: AgentEventTextDelta, Text: "done"})
			})
			handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})
			initialBody := protocol.EncodeConnectMessage(agentTestClientPayload("generic-conversation", "relay-gpt", "run tool"))
			request := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(initialBody))
			request.Header.Set("Content-Type", "application/connect+proto")
			response := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				handler.ServeHTTP(response, request)
				close(done)
			}()
			select {
			case <-dispatched:
			case <-time.After(time.Second):
				t.Fatalf("%s tool was not dispatched", test.name)
			}
			resultRequest := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(protocol.EncodeConnectMessage(test.resultPayload(1))))
			resultRequest.Header.Set("Content-Type", "application/connect+proto")
			resultResponse := httptest.NewRecorder()
			handler.ServeHTTP(resultResponse, resultRequest)
			if resultResponse.Code != http.StatusOK {
				t.Fatalf("detached result status/body = %d/%q", resultResponse.Code, resultResponse.Body.String())
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatalf("direct Run did not finish after %s result", test.name)
			}
			frames := agentTestFrames(t, response.Body.Bytes())
			if len(frames) < 6 || agentTestInteractionField(t, frames[2].payload) != 3 {
				t.Fatalf("%s frames = %#v", test.name, frames)
			}
		})
	}
}

func TestAgentHandlerAccumulatesShellStreamUntilExit(t *testing.T) {
	dispatched := make(chan struct{})
	executor := AgentExecutorFunc(func(ctx context.Context, run protocol.RunRequest, emit func(AgentEvent) error) error {
		resultChannel := make(chan agent.ToolResult, 1)
		if err := emit(AgentEvent{
			Kind:   AgentEventToolCall,
			Tool:   agent.ToolCall{ID: "call-shell-1", Name: "Shell", Arguments: `{"command":"printf hello","working_directory":"/repo"}`},
			Result: resultChannel,
		}); err != nil {
			return err
		}
		close(dispatched)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-resultChannel:
			if result.CallID != "call-shell-1" || result.IsError || !strings.Contains(result.Content, "hello") || !strings.Contains(result.Content, "warning") {
				return errors.New("unexpected Shell result")
			}
		}
		return emit(AgentEvent{Kind: AgentEventTextDelta, Text: "shell complete"})
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})
	initialBody := protocol.EncodeConnectMessage(agentTestClientPayload("shell-conversation", "relay-gpt", "run shell"))
	request := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(initialBody))
	request.Header.Set("Content-Type", "application/connect+proto")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("Shell tool was not dispatched")
	}

	for _, payload := range [][]byte{
		agentTestShellResultPayload(1, 4, nil),
		agentTestShellResultPayload(1, 1, agentTestString(nil, 1, "hello\n")),
		agentTestShellResultPayload(1, 2, agentTestString(nil, 1, "warning\n")),
	} {
		resultRequest := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(protocol.EncodeConnectMessage(payload)))
		resultRequest.Header.Set("Content-Type", "application/connect+proto")
		resultResponse := httptest.NewRecorder()
		handler.ServeHTTP(resultResponse, resultRequest)
		if resultResponse.Code != http.StatusOK {
			t.Fatalf("shell progress status/body = %d/%q", resultResponse.Code, resultResponse.Body.String())
		}
		select {
		case <-done:
			t.Fatal("Shell completed before exit event")
		default:
		}
	}
	exit := agentTestVarint(nil, 1, 0)
	exit = agentTestString(exit, 2, "/repo")
	exitRequest := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(protocol.EncodeConnectMessage(agentTestShellResultPayload(1, 3, exit))))
	exitRequest.Header.Set("Content-Type", "application/connect+proto")
	exitResponse := httptest.NewRecorder()
	handler.ServeHTTP(exitResponse, exitRequest)
	if exitResponse.Code != http.StatusOK {
		t.Fatalf("shell exit status/body = %d/%q", exitResponse.Code, exitResponse.Body.String())
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("direct Run did not finish after Shell exit")
	}
	frames := agentTestFrames(t, response.Body.Bytes())
	if len(frames) < 10 {
		t.Fatalf("shell frames = %#v", frames)
	}
	for index, wantEvent := range []protowire.Number{4, 1, 2, 3} {
		progress := agentTestNestedField(t, frames[2+index].payload, 1, 12)
		if got := agentTestFirstField(t, progress); got != wantEvent {
			t.Fatalf("shell progress %d = %d, want %d", index, got, wantEvent)
		}
	}
	if got := agentTestInteractionField(t, frames[6].payload); got != 3 {
		t.Fatalf("shell completed interaction = %d, want 3", got)
	}
}

func TestAgentHandlerExecutesEditAsHiddenReadThenWrite(t *testing.T) {
	dispatched := make(chan struct{})
	executor := AgentExecutorFunc(func(ctx context.Context, run protocol.RunRequest, emit func(AgentEvent) error) error {
		resultChannel := make(chan agent.ToolResult, 1)
		if err := emit(AgentEvent{
			Kind:   AgentEventToolCall,
			Tool:   agent.ToolCall{ID: "call-edit-1", Name: "Edit", Arguments: `{"path":"main.go","old_string":"before","new_string":"after"}`},
			Result: resultChannel,
		}); err != nil {
			return err
		}
		close(dispatched)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-resultChannel:
			if result.CallID != "call-edit-1" || result.IsError || !strings.Contains(result.Content, `"path":"main.go"`) {
				return errors.New("unexpected Edit result")
			}
		}
		return emit(AgentEvent{Kind: AgentEventTextDelta, Text: "edit complete"})
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})
	initialBody := protocol.EncodeConnectMessage(agentTestClientPayload("edit-conversation", "relay-gpt", "edit file"))
	request := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(initialBody))
	request.Header.Set("Content-Type", "application/connect+proto")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("Edit tool was not dispatched")
	}

	readRequest := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(protocol.EncodeConnectMessage(agentTestReadResultPayloadForPath(1, "main.go", "before value\n"))))
	readRequest.Header.Set("Content-Type", "application/connect+proto")
	readResponse := httptest.NewRecorder()
	handler.ServeHTTP(readResponse, readRequest)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("Edit hidden read status/body = %d/%q", readResponse.Code, readResponse.Body.String())
	}
	select {
	case <-done:
		t.Fatal("Edit completed before hidden Write")
	default:
	}

	writeRequest := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(protocol.EncodeConnectMessage(agentTestWriteResultPayload(2, "main.go", "after value\n"))))
	writeRequest.Header.Set("Content-Type", "application/connect+proto")
	writeResponse := httptest.NewRecorder()
	handler.ServeHTTP(writeResponse, writeRequest)
	if writeResponse.Code != http.StatusOK {
		t.Fatalf("Edit hidden write status/body = %d/%q", writeResponse.Code, writeResponse.Body.String())
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("direct Run did not finish after Edit write")
	}
	frames := agentTestFrames(t, response.Body.Bytes())
	if len(frames) < 7 {
		t.Fatalf("Edit frames = %#v", frames)
	}
	if got := agentTestFirstFieldAfterIdentity(t, agentTestNestedField(t, frames[0].payload, 2)); got != 7 {
		t.Fatalf("Edit first exec = %d, want hidden Read(7)", got)
	}
	if got := agentTestFirstField(t, agentTestNestedField(t, frames[1].payload, 1, 2, 2)); got != 12 {
		t.Fatalf("Edit started tool = %d, want Edit(12)", got)
	}
	writeArgs := agentTestNestedField(t, frames[2].payload, 2, 3)
	if got := string(agentTestNestedField(t, writeArgs, 2)); got != "after value\n" {
		t.Fatalf("Edit hidden Write contents = %q", got)
	}
	if got := agentTestInteractionField(t, frames[3].payload); got != 3 {
		t.Fatalf("Edit completed interaction = %d, want 3", got)
	}
}

func TestAgentHandlerRoutesRunScopedMCPToolWithoutRenamingProviderCall(t *testing.T) {
	dispatched := make(chan struct{})
	executor := AgentExecutorFunc(func(ctx context.Context, run protocol.RunRequest, emit func(AgentEvent) error) error {
		if len(run.MCPTools) != 1 || run.MCPTools[0].Name != "weather_lookup" || run.MCPTools[0].ProviderIdentifier != "weather-server" || run.MCPTools[0].ToolName != "lookup" {
			return errors.New("missing MCP metadata")
		}
		resultChannel := make(chan agent.ToolResult, 1)
		if err := emit(AgentEvent{
			Kind:   AgentEventToolCall,
			Tool:   agent.ToolCall{ID: "call-mcp-1", Name: "weather_lookup", Arguments: `{"city":"Taipei"}`},
			Result: resultChannel,
		}); err != nil {
			return err
		}
		close(dispatched)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-resultChannel:
			if result.CallID != "call-mcp-1" || result.IsError || !strings.Contains(result.Content, "sunny") {
				return errors.New("unexpected MCP result")
			}
		}
		return emit(AgentEvent{Kind: AgentEventTextDelta, Text: "mcp complete"})
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})
	initialBody := protocol.EncodeConnectMessage(agentTestClientPayloadWithMCP("mcp-conversation", "relay-gpt", "weather"))
	request := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(initialBody))
	request.Header.Set("Content-Type", "application/connect+proto")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-dispatched:
	case <-time.After(time.Second):
		t.Fatal("run-scoped MCP tool was not dispatched")
	}

	mcpText := agentTestString(nil, 1, "sunny")
	mcpContentItem := agentTestMessage(nil, 1, mcpText)
	mcpSuccess := agentTestMessage(nil, 1, mcpContentItem)
	mcpResult := agentTestMessage(nil, 1, mcpSuccess)
	resultPayload := agentTestExecResultPayload(1, 11, mcpResult)
	resultRequest := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(protocol.EncodeConnectMessage(resultPayload)))
	resultRequest.Header.Set("Content-Type", "application/connect+proto")
	resultResponse := httptest.NewRecorder()
	handler.ServeHTTP(resultResponse, resultRequest)
	if resultResponse.Code != http.StatusOK {
		t.Fatalf("MCP result status/body = %d/%q", resultResponse.Code, resultResponse.Body.String())
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("direct Run did not finish after MCP result")
	}
	frames := agentTestFrames(t, response.Body.Bytes())
	if len(frames) < 6 {
		t.Fatalf("MCP frames = %#v", frames)
	}
	mcpArgs := agentTestNestedField(t, frames[0].payload, 2, 11)
	for field, want := range map[protowire.Number]string{1: "weather_lookup", 4: "weather-server", 5: "lookup"} {
		if got := string(agentTestNestedField(t, mcpArgs, field)); got != want {
			t.Fatalf("MCP field %d = %q, want %q", field, got, want)
		}
	}
	if tool := agentTestNestedField(t, frames[1].payload, 1, 2, 2); agentTestFirstField(t, tool) != 15 {
		t.Fatalf("MCP started tool = %x", tool)
	}
	if got := agentTestInteractionField(t, frames[2].payload); got != 3 {
		t.Fatalf("MCP completed interaction = %d, want 3", got)
	}
}

func TestAgentHandlerDiscoversMCPStateBeforeStartingProvider(t *testing.T) {
	executorStarted := make(chan struct{})
	executor := AgentExecutorFunc(func(ctx context.Context, run protocol.RunRequest, emit func(AgentEvent) error) error {
		if len(run.MCPTools) != 1 || run.MCPTools[0].Name != "weather-server-weather_lookup" {
			return errors.New("MCP state was not injected into run")
		}
		close(executorStarted)
		return emit(AgentEvent{Kind: AgentEventTextDelta, Text: "discovered"})
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})
	initialBody := protocol.EncodeConnectMessage(agentTestClientPayloadWithEmptyMCP("mcp-discovery", "relay-gpt", "weather"))
	request := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(initialBody))
	request.Header.Set("Content-Type", "application/connect+proto")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-executorStarted:
		t.Fatal("provider executor started before MCP state result")
	case <-time.After(25 * time.Millisecond):
	}

	resultRequest := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(protocol.EncodeConnectMessage(agentTestMCPStateResultPayload(1))))
	resultRequest.Header.Set("Content-Type", "application/connect+proto")
	resultResponse := httptest.NewRecorder()
	handler.ServeHTTP(resultResponse, resultRequest)
	if resultResponse.Code != http.StatusOK {
		t.Fatalf("MCP state result status/body = %d/%q", resultResponse.Code, resultResponse.Body.String())
	}
	select {
	case <-executorStarted:
	case <-time.After(time.Second):
		t.Fatal("provider executor did not start after MCP state result")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("direct Run did not finish after MCP discovery")
	}
	frames := agentTestFrames(t, response.Body.Bytes())
	if len(frames) < 4 {
		t.Fatalf("MCP discovery frames = %#v", frames)
	}
	exec := agentTestNestedField(t, frames[0].payload, 2)
	if got := agentTestFirstFieldAfterIdentity(t, exec); got != 36 {
		t.Fatalf("MCP discovery exec field = %d, want 36", got)
	}
}

func TestAgentHandlerTimesOutMCPDiscoveryAndRejectsLateResult(t *testing.T) {
	var executorCalls atomic.Int32
	executor := AgentExecutorFunc(func(context.Context, protocol.RunRequest, func(AgentEvent) error) error {
		executorCalls.Add(1)
		return nil
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{
		HeartbeatInterval:   time.Second,
		StartTimeout:        time.Second,
		MCPDiscoveryTimeout: 20 * time.Millisecond,
	})
	initialBody := protocol.EncodeConnectMessage(agentTestClientPayloadWithEmptyMCP("mcp-timeout", "relay-gpt", "weather"))
	request := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(initialBody))
	request.Header.Set("Content-Type", "application/connect+proto")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("direct Run did not finish after MCP discovery timeout")
	}
	if got := executorCalls.Load(); got != 0 {
		t.Fatalf("provider executor calls = %d, want 0", got)
	}
	frames := agentTestFrames(t, response.Body.Bytes())
	if len(frames) < 2 || frames[len(frames)-1].flag != 0x02 || !bytes.Contains(frames[len(frames)-1].payload, []byte(`"code":"internal"`)) {
		t.Fatalf("MCP timeout frames = %#v", frames)
	}

	lateRequest := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(protocol.EncodeConnectMessage(agentTestMCPStateResultPayload(1))))
	lateRequest.Header.Set("Content-Type", "application/connect+proto")
	lateResponse := httptest.NewRecorder()
	handler.ServeHTTP(lateResponse, lateRequest)
	if lateResponse.Code != http.StatusBadRequest {
		t.Fatalf("late MCP state result status/body = %d/%q, want 400", lateResponse.Code, lateResponse.Body.String())
	}
}

func TestAgentSessionRejectsToolAfterTerminalStateWithoutPanicking(t *testing.T) {
	session := &agentSession{done: true}
	resultChannel := make(chan agent.ToolResult, 1)
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("openTool() panicked: %v", recovered)
		}
	}()
	if err := session.openTool(AgentEvent{Kind: AgentEventToolCall, Tool: agent.ToolCall{ID: "call-1", Name: "Read", Arguments: `{"path":"README.md"}`}, Result: resultChannel}); err == nil {
		t.Fatal("openTool() accepted a terminal session")
	}
}

func TestAgentSessionRejectsLateEditResultAfterTerminalState(t *testing.T) {
	nextMessageID := uint32(0)
	session := &agentSession{
		notify: make(chan struct{}, 1),
		allocateMessageID: func() uint32 {
			nextMessageID++
			return nextMessageID
		},
		now: time.Now,
	}
	started, err := session.start(context.Background(), protocol.RunRequest{ConversationID: "conversation-1", UserText: "edit"})
	if err != nil || !started {
		t.Fatalf("start() = %t, %v", started, err)
	}
	resultChannel := make(chan agent.ToolResult, 1)
	if err := session.openTool(AgentEvent{
		Kind: AgentEventToolCall,
		Tool: agent.ToolCall{
			ID:        "call-edit-late",
			Name:      "Edit",
			Arguments: `{"path":"main.go","old_string":"before","new_string":"after"}`,
		},
		Result: resultChannel,
	}); err != nil {
		t.Fatalf("openTool() error = %v", err)
	}

	session.finish(nil, "canceled", "request canceled")
	message, err := protocol.DecodeAgentClientMessage(agentTestReadResultPayloadForPath(1, "main.go", "before value\n"))
	if err != nil {
		t.Fatalf("DecodeAgentClientMessage() error = %v", err)
	}
	if err := session.applyToolResult(message); err == nil {
		t.Fatal("applyToolResult() accepted a terminal session")
	}

	session.mu.Lock()
	defer session.mu.Unlock()
	if len(session.events) != 2 {
		t.Fatalf("terminal session events = %d, want original 2", len(session.events))
	}
	if len(session.pendingByMessage) != 0 || len(session.pendingByExec) != 0 {
		t.Fatalf("terminal pending tools = %d/%d, want 0/0", len(session.pendingByMessage), len(session.pendingByExec))
	}
}

func TestAgentSessionFinishCancelsExecutionContext(t *testing.T) {
	session := &agentSession{notify: make(chan struct{}, 1), now: time.Now}
	started, err := session.start(context.Background(), protocol.RunRequest{ConversationID: "conversation-1", UserText: "hello"})
	if err != nil || !started {
		t.Fatalf("start() = %t, %v", started, err)
	}
	executionContext := session.context()
	session.finish(nil, "invalid_argument", "terminal protocol failure")
	select {
	case <-executionContext.Done():
	case <-time.After(time.Second):
		t.Fatal("terminal session did not cancel execution context")
	}
}

func TestAgentHandlerStreamsHeartbeatWhileExecutorIsRunning(t *testing.T) {
	release := make(chan struct{})
	executor := AgentExecutorFunc(func(ctx context.Context, run protocol.RunRequest, emit func(AgentEvent) error) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: 10 * time.Millisecond, StartTimeout: time.Second})
	if response := performBidiAppend(handler, agentTestAppendPayload("request-heartbeat", 0, "conversation-1", "relay-gpt", "hello")); response.Code != http.StatusOK {
		t.Fatalf("BidiAppend status = %d", response.Code)
	}

	streamResult := make(chan *httptest.ResponseRecorder, 1)
	go func() { streamResult <- performRunSSE(handler, "request-heartbeat") }()
	time.Sleep(35 * time.Millisecond)
	close(release)
	stream := <-streamResult
	frames := agentTestFrames(t, stream.Body.Bytes())
	heartbeat, _ := protocol.EncodeHeartbeat()
	found := false
	for _, frame := range frames {
		found = found || frame.flag == 0 && bytes.Equal(frame.payload, heartbeat)
	}
	if !found {
		t.Fatalf("frames = %#v, want heartbeat", frames)
	}
}

func TestAgentHandlerCancelsExecutorWhenRunStreamIsCanceled(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	executor := AgentExecutorFunc(func(ctx context.Context, run protocol.RunRequest, emit func(AgentEvent) error) error {
		close(started)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})
	if response := performBidiAppend(handler, agentTestAppendPayload("request-cancel", 0, "conversation-1", "relay-gpt", "hello")); response.Code != http.StatusOK {
		t.Fatalf("BidiAppend status = %d", response.Code)
	}
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/RunSSE", bytes.NewReader(protocol.EncodeConnectMessage(agentTestString(nil, 1, "request-cancel")))).WithContext(ctx)
	request.Header.Set("Content-Type", "application/connect+proto")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("executor context was not canceled")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("RunSSE handler did not stop after cancellation")
	}
}

func TestAgentHandlerReturnsConnectErrorForExecutorFailure(t *testing.T) {
	executor := AgentExecutorFunc(func(context.Context, protocol.RunRequest, func(AgentEvent) error) error {
		return errors.New("provider secret detail")
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})
	if response := performBidiAppend(handler, agentTestAppendPayload("request-error", 0, "conversation-1", "relay-gpt", "hello")); response.Code != http.StatusOK {
		t.Fatalf("BidiAppend status = %d", response.Code)
	}
	stream := performRunSSE(handler, "request-error")
	frames := agentTestFrames(t, stream.Body.Bytes())
	last := frames[len(frames)-1]
	if last.flag != 0x02 {
		t.Fatalf("terminal flag = %d, want 2", last.flag)
	}
	var terminal struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(last.payload, &terminal); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if terminal.Error.Code != "internal" || terminal.Error.Message != "provider request failed" {
		t.Fatalf("terminal error = %#v", terminal.Error)
	}
	if strings.Contains(stream.Body.String(), "secret detail") {
		t.Fatal("provider error detail leaked to client")
	}
}

func TestAgentHandlerPreservesSanitizedProviderConnectErrorCode(t *testing.T) {
	executor := AgentExecutorFunc(func(context.Context, protocol.RunRequest, func(AgentEvent) error) error {
		return errors.Join(errors.New("provider secret detail"), provider.NewError("unavailable", http.StatusServiceUnavailable, true, nil))
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})
	if response := performBidiAppend(handler, agentTestAppendPayload("request-provider-error", 0, "conversation-1", "relay-gpt", "hello")); response.Code != http.StatusOK {
		t.Fatalf("BidiAppend status = %d", response.Code)
	}
	stream := performRunSSE(handler, "request-provider-error")
	frames := agentTestFrames(t, stream.Body.Bytes())
	last := frames[len(frames)-1]
	var terminal struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(last.payload, &terminal); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if terminal.Error.Code != "unavailable" || terminal.Error.Message != "provider request failed" {
		t.Fatalf("terminal error = %#v", terminal.Error)
	}
	if strings.Contains(stream.Body.String(), "secret detail") {
		t.Fatal("provider error detail leaked to client")
	}
}

func TestAgentHandlerReusesDirectSessionForSameUserMessageID(t *testing.T) {
	var calls atomic.Int32
	executor := AgentExecutorFunc(func(context.Context, protocol.RunRequest, func(AgentEvent) error) error {
		calls.Add(1)
		return provider.NewError("unavailable", http.StatusServiceUnavailable, true, nil)
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})
	payload := agentTestClientPayloadWithMessageID("conversation-1", "relay-gpt", "same prompt", "stable-message-1")
	first := performDirectRunPayload(handler, payload)
	second := performDirectRunPayload(handler, payload)
	if first.Code != http.StatusOK || second.Code != http.StatusOK {
		t.Fatalf("direct statuses = %d/%d", first.Code, second.Code)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("executor calls = %d, want 1 for a retried user message", got)
	}
	for index, response := range []*httptest.ResponseRecorder{first, second} {
		frames := agentTestFrames(t, response.Body.Bytes())
		if len(frames) == 0 || frames[len(frames)-1].flag != 0x02 || !bytes.Contains(frames[len(frames)-1].payload, []byte(`"code":"unavailable"`)) {
			t.Fatalf("response %d terminal frames = %#v", index, frames)
		}
	}
}

func TestAgentHandlerExpiresTerminalDirectSessionAfterReplayWindow(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	var calls atomic.Int32
	executor := AgentExecutorFunc(func(context.Context, protocol.RunRequest, func(AgentEvent) error) error {
		calls.Add(1)
		return provider.NewError("unavailable", http.StatusServiceUnavailable, true, nil)
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{
		HeartbeatInterval: time.Second,
		StartTimeout:      time.Second,
		SessionRetention:  time.Minute,
		Now:               func() time.Time { return now },
	})
	payload := agentTestClientPayloadWithMessageID("conversation-1", "relay-gpt", "same prompt", "expiring-message")
	performDirectRunPayload(handler, payload)
	performDirectRunPayload(handler, payload)
	if got := calls.Load(); got != 1 {
		t.Fatalf("executor calls within replay window = %d, want 1", got)
	}

	now = now.Add(2 * time.Minute)
	performDirectRunPayload(handler, payload)
	if got := calls.Load(); got != 2 {
		t.Fatalf("executor calls after replay window = %d, want 2", got)
	}
}

func TestAgentHandlerBoundsRetainedTerminalSessions(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	handler := NewAgentHandler(
		AgentExecutorFunc(func(context.Context, protocol.RunRequest, func(AgentEvent) error) error { return nil }),
		AgentHandlerOptions{
			HeartbeatInterval:   time.Second,
			StartTimeout:        time.Second,
			SessionRetention:    time.Hour,
			MaxRetainedSessions: 2,
			Now:                 func() time.Time { return now },
		},
	)
	for index, messageID := range []string{"bounded-message-1", "bounded-message-2", "bounded-message-3"} {
		payload := agentTestClientPayloadWithMessageID("conversation-1", "relay-gpt", "prompt", messageID)
		if response := performDirectRunPayload(handler, payload); response.Code != http.StatusOK {
			t.Fatalf("direct status %d = %d", index, response.Code)
		}
		now = now.Add(time.Second)
	}
	concrete := handler.(*agentHandler)
	concrete.mu.Lock()
	retained := len(concrete.sessions)
	concrete.mu.Unlock()
	if retained != 2 {
		t.Fatalf("retained sessions = %d, want 2", retained)
	}
}

func TestAgentHandlerDoesNotMergeDifferentUserMessageIDs(t *testing.T) {
	var calls atomic.Int32
	executor := AgentExecutorFunc(func(context.Context, protocol.RunRequest, func(AgentEvent) error) error {
		calls.Add(1)
		return provider.NewError("unavailable", http.StatusServiceUnavailable, true, nil)
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{HeartbeatInterval: time.Second, StartTimeout: time.Second})
	for _, messageID := range []string{"message-1", "message-2"} {
		payload := agentTestClientPayloadWithMessageID("conversation-1", "relay-gpt", "same prompt", messageID)
		if response := performDirectRunPayload(handler, payload); response.Code != http.StatusOK {
			t.Fatalf("direct status for %s = %d", messageID, response.Code)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("executor calls = %d, want 2 for distinct user messages", got)
	}
}

func TestAgentHandlerRejectsMalformedFramesAndUnsupportedMedia(t *testing.T) {
	handler := NewAgentHandler(AgentExecutorFunc(func(context.Context, protocol.RunRequest, func(AgentEvent) error) error { return nil }), AgentHandlerOptions{})
	tests := []struct {
		name        string
		path        string
		contentType string
		body        []byte
		status      int
	}{
		{name: "bad bidi protobuf", path: "/aiserver.v1.BidiService/BidiAppend", contentType: "application/proto", body: []byte{0x0a, 0xff}, status: http.StatusBadRequest},
		{name: "bad bidi media", path: "/aiserver.v1.BidiService/BidiAppend", contentType: "application/json", body: nil, status: http.StatusUnsupportedMediaType},
		{name: "bad stream frame", path: "/agent.v1.AgentService/RunSSE", contentType: "application/connect+proto", body: []byte{0}, status: http.StatusBadRequest},
		{name: "bad stream media", path: "/agent.v1.AgentService/RunSSE", contentType: "application/proto", body: nil, status: http.StatusUnsupportedMediaType},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, bytes.NewReader(test.body))
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status/body = %d/%q, want %d", response.Code, response.Body.String(), test.status)
			}
		})
	}
}

func TestAgentHandlerExecutesDuplicateAppendSequenceAtMostOnce(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	executor := AgentExecutorFunc(func(context.Context, protocol.RunRequest, func(AgentEvent) error) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil
	})
	handler := NewAgentHandler(executor, AgentHandlerOptions{})
	payload := agentTestAppendPayload("request-duplicate", 0, "conversation-1", "relay-gpt", "hello")
	for range 2 {
		if response := performBidiAppend(handler, payload); response.Code != http.StatusOK {
			t.Fatalf("BidiAppend status = %d", response.Code)
		}
	}
	_ = performRunSSE(handler, "request-duplicate")
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("executor calls = %d, want 1", calls)
	}
}

func performBidiAppend(handler http.Handler, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/aiserver.v1.BidiService/BidiAppend", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/proto")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func performRunSSE(handler http.Handler, requestID string) *httptest.ResponseRecorder {
	requestBody := protocol.EncodeConnectMessage(agentTestString(nil, 1, requestID))
	request := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/RunSSE", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/connect+proto")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func performDirectRunPayload(handler http.Handler, payload []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/agent.v1.AgentService/Run", bytes.NewReader(protocol.EncodeConnectMessage(payload)))
	request.Header.Set("Content-Type", "application/connect+proto")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func agentTestAppendPayload(requestID string, sequence uint64, conversationID, modelID, text string) []byte {
	client := agentTestClientPayload(conversationID, modelID, text)
	requestIDMessage := agentTestString(nil, 1, requestID)
	appendRequest := agentTestMessage(nil, 2, requestIDMessage)
	appendRequest = agentTestVarint(appendRequest, 3, sequence)
	return agentTestMessage(appendRequest, 4, client)
}

func agentTestClientPayload(conversationID, modelID, text string) []byte {
	return agentTestClientPayloadWithMessageID(conversationID, modelID, text, "message-1")
}

func agentTestClientPayloadWithMessageID(conversationID, modelID, text, messageID string) []byte {
	userMessage := agentTestString(nil, 1, text)
	if messageID != "" {
		userMessage = agentTestString(userMessage, 2, messageID)
	}
	userAction := agentTestMessage(nil, 1, userMessage)
	action := agentTestMessage(nil, 1, userAction)
	model := agentTestString(nil, 1, modelID)
	run := agentTestMessage(nil, 2, action)
	run = agentTestMessage(run, 3, model)
	run = agentTestString(run, 5, conversationID)
	return agentTestMessage(nil, 1, run)
}

func agentTestBackgroundTaskCompletionPayload(conversationID, modelID string) []byte {
	completion := agentTestString(nil, 1, "task-1")
	completion = agentTestVarint(completion, 2, 1)
	completion = agentTestVarint(completion, 3, 1)
	completion = agentTestString(completion, 4, "Append once to shell-count.txt")
	completion = agentTestVarint(completion, 8, 1)
	completion = agentTestString(completion, 10, "call-shell-1")
	completionAction := agentTestMessage(nil, 1, completion)
	action := agentTestMessage(nil, 12, completionAction)
	model := agentTestString(nil, 1, modelID)
	run := agentTestMessage(nil, 1, nil)
	run = agentTestMessage(run, 2, action)
	run = agentTestMessage(run, 3, model)
	run = agentTestMessage(run, 4, nil)
	run = agentTestString(run, 5, conversationID)
	return agentTestMessage(nil, 1, run)
}

func agentTestClientPayloadWithMCP(conversationID, modelID, text string) []byte {
	client := agentTestClientPayload(conversationID, modelID, text)
	run := agentTestNestedFieldForFixture(client, 1)
	schemaEntry := agentTestString(nil, 1, "type")
	schemaEntry = agentTestMessage(schemaEntry, 2, agentTestString(nil, 3, "object"))
	schemaStruct := agentTestMessage(nil, 1, schemaEntry)
	schemaValue := agentTestMessage(nil, 5, schemaStruct)
	definition := agentTestString(nil, 1, "weather_lookup")
	definition = agentTestString(definition, 2, "Look up weather")
	definition = agentTestMessage(definition, 3, schemaValue)
	definition = agentTestString(definition, 4, "weather-server")
	definition = agentTestString(definition, 5, "lookup")
	mcpTools := agentTestMessage(nil, 1, definition)
	run = agentTestMessage(run, 4, mcpTools)
	return agentTestMessage(nil, 1, run)
}

func agentTestClientPayloadWithEmptyMCP(conversationID, modelID, text string) []byte {
	client := agentTestClientPayload(conversationID, modelID, text)
	run := agentTestNestedFieldForFixture(client, 1)
	run = agentTestMessage(run, 4, nil)
	return agentTestMessage(nil, 1, run)
}

func agentTestNestedFieldForFixture(payload []byte, wanted protowire.Number) []byte {
	for len(payload) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(payload)
		if tagLength < 0 {
			return nil
		}
		payload = payload[tagLength:]
		if wireType == protowire.BytesType {
			value, valueLength := protowire.ConsumeBytes(payload)
			if valueLength < 0 {
				return nil
			}
			if number == wanted {
				return append([]byte(nil), value...)
			}
			payload = payload[valueLength:]
			continue
		}
		valueLength := protowire.ConsumeFieldValue(number, wireType, payload)
		if valueLength < 0 {
			return nil
		}
		payload = payload[valueLength:]
	}
	return nil
}

func agentTestReadResultPayload(messageID uint64, content string) []byte {
	return agentTestReadResultPayloadForPath(messageID, "README.md", content)
}

func agentTestReadResultPayloadForPath(messageID uint64, path, content string) []byte {
	success := agentTestString(nil, 1, path)
	success = agentTestString(success, 2, content)
	readResult := agentTestMessage(nil, 1, success)
	exec := agentTestVarint(nil, 1, messageID)
	exec = agentTestMessage(exec, 7, readResult)
	return agentTestMessage(nil, 2, exec)
}

func agentTestWriteResultPayload(messageID uint64, path, content string) []byte {
	success := agentTestString(nil, 1, path)
	success = agentTestVarint(success, 2, uint64(strings.Count(content, "\n")))
	success = agentTestVarint(success, 3, uint64(len(content)))
	success = agentTestString(success, 4, content)
	writeResult := agentTestMessage(nil, 1, success)
	exec := agentTestVarint(nil, 1, messageID)
	exec = agentTestMessage(exec, 3, writeResult)
	return agentTestMessage(nil, 2, exec)
}

func agentTestDeleteResultPayload(messageID uint64) []byte {
	success := agentTestString(nil, 1, "obsolete.txt")
	success = agentTestVarint(success, 3, 5)
	result := agentTestMessage(nil, 1, success)
	exec := agentTestVarint(nil, 1, messageID)
	exec = agentTestMessage(exec, 4, result)
	return agentTestMessage(nil, 2, exec)
}

func agentTestListResultPayload(messageID uint64) []byte {
	root := agentTestString(nil, 1, "/repo")
	root = agentTestMessage(root, 3, agentTestString(nil, 1, "README.md"))
	success := agentTestMessage(nil, 1, root)
	result := agentTestMessage(nil, 1, success)
	exec := agentTestVarint(nil, 1, messageID)
	exec = agentTestMessage(exec, 8, result)
	return agentTestMessage(nil, 2, exec)
}

func agentTestGrepResultPayload(messageID uint64) []byte {
	match := agentTestVarint(nil, 1, 7)
	match = agentTestString(match, 2, "TODO: fix")
	fileMatch := agentTestString(nil, 1, "/repo/main.go")
	fileMatch = agentTestMessage(fileMatch, 2, match)
	contentResult := agentTestMessage(nil, 1, fileMatch)
	union := agentTestMessage(nil, 3, contentResult)
	entry := agentTestString(nil, 1, "/repo")
	entry = agentTestMessage(entry, 2, union)
	success := agentTestString(nil, 1, "TODO")
	success = agentTestString(success, 2, "/repo")
	success = agentTestString(success, 3, "content")
	success = agentTestMessage(success, 4, entry)
	result := agentTestMessage(nil, 1, success)
	exec := agentTestVarint(nil, 1, messageID)
	exec = agentTestMessage(exec, 5, result)
	return agentTestMessage(nil, 2, exec)
}

func agentTestShellResultPayload(messageID uint64, eventField protowire.Number, eventPayload []byte) []byte {
	stream := agentTestMessage(nil, eventField, eventPayload)
	exec := agentTestVarint(nil, 1, messageID)
	exec = agentTestMessage(exec, 14, stream)
	return agentTestMessage(nil, 2, exec)
}

func agentTestExecResultPayload(messageID uint64, resultField protowire.Number, result []byte) []byte {
	exec := agentTestVarint(nil, 1, messageID)
	exec = agentTestMessage(exec, resultField, result)
	return agentTestMessage(nil, 2, exec)
}

func agentTestMCPStateResultPayload(messageID uint64) []byte {
	schemaEntry := agentTestString(nil, 1, "type")
	schemaEntry = agentTestMessage(schemaEntry, 2, agentTestString(nil, 3, "object"))
	schemaStruct := agentTestMessage(nil, 1, schemaEntry)
	schemaValue := agentTestMessage(nil, 5, schemaStruct)
	definition := agentTestString(nil, 1, "weather-server-weather_lookup")
	definition = agentTestString(definition, 2, "Look up weather")
	definition = agentTestMessage(definition, 3, schemaValue)
	definition = agentTestString(definition, 4, "weather-server")
	definition = agentTestString(definition, 5, "weather_lookup")
	server := agentTestString(nil, 1, "Weather")
	server = agentTestString(server, 2, "weather-server")
	server = agentTestMessage(server, 5, definition)
	success := agentTestMessage(nil, 1, server)
	result := agentTestMessage(nil, 1, success)
	return agentTestExecResultPayload(messageID, 36, result)
}

func agentTestString(payload []byte, number protowire.Number, value string) []byte {
	payload = protowire.AppendTag(payload, number, protowire.BytesType)
	return protowire.AppendString(payload, value)
}

func agentTestMessage(payload []byte, number protowire.Number, value []byte) []byte {
	payload = protowire.AppendTag(payload, number, protowire.BytesType)
	return protowire.AppendBytes(payload, value)
}

func agentTestVarint(payload []byte, number protowire.Number, value uint64) []byte {
	payload = protowire.AppendTag(payload, number, protowire.VarintType)
	return protowire.AppendVarint(payload, value)
}

type agentTestFrame struct {
	flag    byte
	payload []byte
}

func agentTestFrames(t *testing.T, body []byte) []agentTestFrame {
	t.Helper()
	var frames []agentTestFrame
	for len(body) > 0 {
		if len(body) < 5 {
			t.Fatalf("truncated frame header: %x", body)
		}
		length := int(binary.BigEndian.Uint32(body[1:5]))
		if len(body) < 5+length {
			t.Fatalf("truncated frame body: %x", body)
		}
		frames = append(frames, agentTestFrame{flag: body[0], payload: append([]byte(nil), body[5:5+length]...)})
		body = body[5+length:]
	}
	return frames
}

func agentTestFirstField(t *testing.T, payload []byte) protowire.Number {
	t.Helper()
	number, _, length := protowire.ConsumeTag(payload)
	if length < 0 {
		t.Fatalf("ConsumeTag() error = %d", length)
	}
	return number
}

func agentTestFirstFieldAfterIdentity(t *testing.T, payload []byte) protowire.Number {
	t.Helper()
	for len(payload) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(payload)
		if tagLength < 0 {
			t.Fatalf("ConsumeTag() error = %d", tagLength)
		}
		payload = payload[tagLength:]
		valueLength := protowire.ConsumeFieldValue(number, wireType, payload)
		if valueLength < 0 {
			t.Fatalf("ConsumeFieldValue() error = %d", valueLength)
		}
		if number != 1 && number != 15 && number != 19 {
			return number
		}
		payload = payload[valueLength:]
	}
	t.Fatal("exec tool field not found")
	return 0
}

func agentTestNestedField(t *testing.T, payload []byte, path ...protowire.Number) []byte {
	t.Helper()
	message := payload
	for _, wanted := range path {
		found := false
		for len(message) > 0 {
			number, wireType, tagLength := protowire.ConsumeTag(message)
			if tagLength < 0 {
				t.Fatalf("ConsumeTag() error = %d", tagLength)
			}
			message = message[tagLength:]
			if wireType == protowire.BytesType {
				value, valueLength := protowire.ConsumeBytes(message)
				if valueLength < 0 {
					t.Fatalf("ConsumeBytes() error = %d", valueLength)
				}
				if number == wanted {
					message = value
					found = true
					break
				}
				message = message[valueLength:]
				continue
			}
			valueLength := protowire.ConsumeFieldValue(number, wireType, message)
			if valueLength < 0 {
				t.Fatalf("ConsumeFieldValue() error = %d", valueLength)
			}
			message = message[valueLength:]
		}
		if !found {
			t.Fatalf("field %d not found", wanted)
		}
	}
	return message
}

func agentTestInteractionField(t *testing.T, payload []byte) protowire.Number {
	t.Helper()
	_, _, tagLength := protowire.ConsumeTag(payload)
	payload = payload[tagLength:]
	interaction, length := protowire.ConsumeBytes(payload)
	if length < 0 {
		t.Fatalf("ConsumeBytes() error = %d", length)
	}
	return agentTestFirstField(t, interaction)
}
