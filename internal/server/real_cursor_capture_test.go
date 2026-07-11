package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/agent"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/certs"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/protocol"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestCaptureRealCursorRunShape(t *testing.T) {
	if os.Getenv("CURSOR_CLI_BYOK_CAPTURE_REAL") != "1" {
		t.Skip("set CURSOR_CLI_BYOK_CAPTURE_REAL=1 for local protocol capture")
	}
	cursorPath, err := exec.LookPath("cursor-agent")
	if err != nil {
		t.Skip("cursor-agent is unavailable")
	}
	root := t.TempDir()
	bundle, err := (certs.Manager{Directory: filepath.Join(root, "certs")}).Ensure()
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	cfg := config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: "relay-gpt",
		Models: []config.Model{{
			Name: "relay-gpt", Protocol: config.ProtocolOpenAI, BaseURL: "http://127.0.0.1:1",
			Endpoint: config.EndpointResponses, APIKey: "capture-key", UpstreamModel: "capture-model",
		}},
	}
	compatibility := NewCompatibilityHandler(func() (config.Config, error) { return cfg, nil })
	captures := make(chan capturedRunShape, 4)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != directRunProcedure {
			compatibility.ServeHTTP(writer, request)
			return
		}
		var consumed bytes.Buffer
		flag, payload, readError := protocol.ReadConnectMessage(io.TeeReader(request.Body, &consumed), maxAgentRequestBytes)
		shape := capturedRunShape{
			ContentType:     request.Header.Get("Content-Type"),
			ConnectEncoding: request.Header.Get("Connect-Encoding"),
			ContentEncoding: request.Header.Get("Content-Encoding"),
			Flag:            flag,
			ReadError:       errorText(readError),
			ConsumedBytes:   consumed.Len(),
			PayloadBytes:    len(payload),
			TopFields:       protobufShape(payload),
		}
		if runPayload, ok := protobufBytesField(payload, 1); ok {
			shape.RunFields = protobufShape(runPayload)
			if modelPayload, ok := protobufBytesField(runPayload, 3); ok {
				shape.ModelDetailsFields = protobufShape(modelPayload)
			}
			if requestedPayload, ok := protobufBytesField(runPayload, 9); ok {
				shape.RequestedModelFields = protobufShape(requestedPayload)
			}
		}
		if clientMessage, decodeError := protocol.DecodeAgentClientMessage(payload); decodeError != nil {
			shape.DecodeError = decodeError.Error()
		} else {
			shape.ClientKind = clientMessage.Kind
			if clientMessage.Run != nil {
				shape.ConversationIDBytes = len(clientMessage.Run.ConversationID)
				shape.ModelIDBytes = len(clientMessage.Run.ModelID)
				shape.UserTextBytes = len(clientMessage.Run.UserText)
				shape.UserMessageIDBytes = len(clientMessage.Run.UserMessageID)
			}
		}
		select {
		case captures <- shape:
		default:
		}
		writeAgentHTTPError(writer, http.StatusBadRequest, "invalid_argument", "capture complete")
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	running, err := Start(ctx, Options{
		Certificate: bundle.Certificate, InstanceID: "0123456789abcdef0123456789abcdef",
		AuthToken: "capture-token", DaemonVersion: "capture", Handler: handler,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer shutdownServer(t, running)

	commandContext, cancelCommand := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancelCommand()
	command := exec.CommandContext(commandContext, cursorPath,
		"-e", running.EndpointURL(), "--model", "relay-gpt", "--trust", "--print", "--mode", "ask", "capture-shape",
	)
	command.Env = captureCursorEnvironment(root, running.EndpointURL(), bundle.CACertPath)
	_, _ = command.CombinedOutput()
	select {
	case capture := <-captures:
		t.Logf("real Cursor Run shape: %+v", capture)
		if capture.ReadError != "" {
			t.Fatalf("ReadConnectMessage() error = %s", capture.ReadError)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("real Cursor CLI did not send /agent.v1.AgentService/Run")
	}
}

func TestCaptureRealCursorReadRoundTrip(t *testing.T) {
	if os.Getenv("CURSOR_CLI_BYOK_CAPTURE_REAL") != "1" {
		t.Skip("set CURSOR_CLI_BYOK_CAPTURE_REAL=1 for local protocol capture")
	}
	cursorPath, err := exec.LookPath("cursor-agent")
	if err != nil {
		t.Skip("cursor-agent is unavailable")
	}
	root := t.TempDir()
	bundle, err := (certs.Manager{Directory: filepath.Join(root, "certs")}).Ensure()
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	cfg := config.Config{Version: config.CurrentVersion, DefaultModel: "relay-gpt", Models: []config.Model{{
		Name: "relay-gpt", Protocol: config.ProtocolOpenAI, BaseURL: "http://127.0.0.1:1",
		Endpoint: config.EndpointResponses, APIKey: "capture-key", UpstreamModel: "capture-model",
	}}}
	executor := AgentExecutorFunc(func(ctx context.Context, run protocol.RunRequest, emit func(AgentEvent) error) error {
		resultChannel := make(chan agent.ToolResult, 1)
		if err := emit(AgentEvent{
			Kind:   AgentEventToolCall,
			Tool:   agent.ToolCall{ID: "capture-read-1", Name: "Read", Arguments: `{"path":"README.md"}`},
			Result: resultChannel,
		}); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-resultChannel:
			return emit(AgentEvent{Kind: AgentEventTextDelta, Text: "CAPTURE_TOOL_OK"})
		}
	})
	application := NewApplicationHandler(func() (config.Config, error) { return cfg, nil }, executor, AgentHandlerOptions{HeartbeatInterval: time.Second})
	captures := make(chan capturedClientFrame, 32)
	captureHandler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != directRunProcedure {
			application.ServeHTTP(writer, request)
			return
		}
		flag, payload, readError := protocol.ReadConnectMessage(request.Body, maxAgentRequestBytes)
		capture := capturedClientFrame{Flag: flag, ReadError: errorText(readError), Shape: protobufShape(payload)}
		if readError == nil {
			message, decodeError := protocol.DecodeAgentClientMessage(payload)
			capture.DecodeError = errorText(decodeError)
			capture.Kind = message.Kind
			frame := protocol.EncodeConnectMessage(payload)
			frame[0] = flag
			request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(frame), request.Body))
		}
		select {
		case captures <- capture:
		default:
		}
		application.ServeHTTP(writer, request)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	running, err := Start(ctx, Options{
		Certificate: bundle.Certificate, InstanceID: "0123456789abcdef0123456789abcdef",
		AuthToken: "capture-token", DaemonVersion: "capture", Handler: captureHandler,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer shutdownServer(t, running)

	commandContext, cancelCommand := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancelCommand()
	command := exec.CommandContext(commandContext, cursorPath,
		"-e", running.EndpointURL(), "--model", "relay-gpt", "--trust", "--print", "--mode", "ask",
		"Read README.md and return the provider response.",
	)
	command.Dir = filepath.Clean(filepath.Join("..", ".."))
	command.Env = captureCursorEnvironment(root, running.EndpointURL(), bundle.CACertPath)
	output, commandError := command.CombinedOutput()
	t.Logf("real Cursor Read command error=%v output=%q", commandError, strings.TrimSpace(string(output)))
	var observed []capturedClientFrame
drain:
	for {
		select {
		case capture := <-captures:
			observed = append(observed, capture)
		default:
			break drain
		}
	}
	t.Logf("real Cursor Read client frames: %+v", observed)
	if len(observed) == 0 {
		t.Fatal("no /Run client frames captured")
	}
}

type capturedClientFrame struct {
	Flag        byte
	Kind        protocol.ClientMessageKind
	ReadError   string
	DecodeError string
	Shape       string
}

type capturedRunShape struct {
	ContentType          string
	ConnectEncoding      string
	ContentEncoding      string
	Flag                 byte
	ReadError            string
	DecodeError          string
	ConsumedBytes        int
	PayloadBytes         int
	TopFields            string
	RunFields            string
	ModelDetailsFields   string
	RequestedModelFields string
	ClientKind           protocol.ClientMessageKind
	ConversationIDBytes  int
	ModelIDBytes         int
	UserTextBytes        int
	UserMessageIDBytes   int
}

func captureCursorEnvironment(root, endpoint, caPath string) []string {
	values := map[string]string{
		"HOME":                       root,
		"XDG_CONFIG_HOME":            filepath.Join(root, "config"),
		"XDG_DATA_HOME":              filepath.Join(root, "data"),
		"XDG_STATE_HOME":             filepath.Join(root, "state"),
		"PATH":                       os.Getenv("PATH"),
		"AGENT_CLI_CREDENTIAL_STORE": "file",
		"CURSOR_AUTH_TOKEN":          "capture-token",
		"CURSOR_API_ENDPOINT":        endpoint,
		"CURSOR_API_BASE_URL":        endpoint,
		"NODE_EXTRA_CA_CERTS":        caPath,
		"NO_OPEN_BROWSER":            "1",
		"npm_config_offline":         "true",
		"npm_config_update_notifier": "false",
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func protobufShape(payload []byte) string {
	var fields []string
	for len(payload) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(payload)
		if tagLength < 0 {
			fields = append(fields, "malformed-tag")
			break
		}
		payload = payload[tagLength:]
		switch wireType {
		case protowire.BytesType:
			value, valueLength := protowire.ConsumeBytes(payload)
			if valueLength < 0 {
				fields = append(fields, fmt.Sprintf("%d:malformed-bytes", number))
				return strings.Join(fields, ",")
			}
			fields = append(fields, fmt.Sprintf("%d:bytes(%d)", number, len(value)))
			payload = payload[valueLength:]
		case protowire.VarintType:
			_, valueLength := protowire.ConsumeVarint(payload)
			if valueLength < 0 {
				fields = append(fields, fmt.Sprintf("%d:malformed-varint", number))
				return strings.Join(fields, ",")
			}
			fields = append(fields, fmt.Sprintf("%d:varint", number))
			payload = payload[valueLength:]
		default:
			valueLength := protowire.ConsumeFieldValue(number, wireType, payload)
			if valueLength < 0 {
				fields = append(fields, fmt.Sprintf("%d:malformed", number))
				return strings.Join(fields, ",")
			}
			fields = append(fields, fmt.Sprintf("%d:type(%d)", number, wireType))
			payload = payload[valueLength:]
		}
	}
	return strings.Join(fields, ",")
}

func protobufBytesField(payload []byte, wanted protowire.Number) ([]byte, bool) {
	var found []byte
	for len(payload) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(payload)
		if tagLength < 0 {
			return nil, false
		}
		payload = payload[tagLength:]
		if wireType == protowire.BytesType {
			value, valueLength := protowire.ConsumeBytes(payload)
			if valueLength < 0 {
				return nil, false
			}
			if number == wanted {
				found = append(found[:0], value...)
			}
			payload = payload[valueLength:]
			continue
		}
		valueLength := protowire.ConsumeFieldValue(number, wireType, payload)
		if valueLength < 0 {
			return nil, false
		}
		payload = payload[valueLength:]
	}
	return found, found != nil
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
