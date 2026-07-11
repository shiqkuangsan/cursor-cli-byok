package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/agent"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/protocol"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/provider"
)

const (
	bidiAppendProcedure      = "/aiserver.v1.BidiService/BidiAppend"
	directRunProcedure       = "/agent.v1.AgentService/Run"
	runSSEProcedure          = "/agent.v1.AgentService/RunSSE"
	maxAgentRequestBytes     = 4 * 1024 * 1024
	maxShellStreamBytes      = 256 * 1024
	defaultHeartbeatInterval = 5 * time.Second
	defaultAgentStartTimeout = 10 * time.Second
	directHalfCloseTimeout   = 2 * time.Second
	defaultSessionRetention  = 5 * time.Minute
	defaultMaxSessions       = 1024
	defaultMCPStateTimeout   = 10 * time.Second
)

type AgentEventKind = agent.EventKind
type AgentEvent = agent.Event
type AgentExecutor = agent.Executor
type AgentExecutorFunc = agent.ExecutorFunc

const (
	AgentEventUnknown        = agent.EventUnknown
	AgentEventTextDelta      = agent.EventTextDelta
	AgentEventReasoningDelta = agent.EventReasoningDelta
	AgentEventToolCall       = agent.EventToolCall
	AgentEventUsage          = agent.EventUsage
)

type AgentHandlerOptions struct {
	Context             context.Context
	HeartbeatInterval   time.Duration
	StartTimeout        time.Duration
	SessionRetention    time.Duration
	MaxRetainedSessions int
	MCPDiscoveryTimeout time.Duration
	Now                 func() time.Time
}

func NewAgentHandler(executor AgentExecutor, options AgentHandlerOptions) http.Handler {
	rootContext := options.Context
	if rootContext == nil {
		rootContext = context.Background()
	}
	heartbeatInterval := options.HeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = defaultHeartbeatInterval
	}
	startTimeout := options.StartTimeout
	if startTimeout <= 0 {
		startTimeout = defaultAgentStartTimeout
	}
	sessionRetention := options.SessionRetention
	if sessionRetention <= 0 {
		sessionRetention = defaultSessionRetention
	}
	maxRetainedSessions := options.MaxRetainedSessions
	if maxRetainedSessions <= 0 {
		maxRetainedSessions = defaultMaxSessions
	}
	mcpDiscoveryTimeout := options.MCPDiscoveryTimeout
	if mcpDiscoveryTimeout <= 0 {
		mcpDiscoveryTimeout = defaultMCPStateTimeout
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &agentHandler{
		executor:            executor,
		rootContext:         rootContext,
		heartbeatInterval:   heartbeatInterval,
		startTimeout:        startTimeout,
		sessionRetention:    sessionRetention,
		maxRetainedSessions: maxRetainedSessions,
		mcpDiscoveryTimeout: mcpDiscoveryTimeout,
		now:                 now,
		sessions:            make(map[string]*agentSession),
	}
}

func NewApplicationHandler(load ConfigLoader, executor AgentExecutor, options AgentHandlerOptions) http.Handler {
	agentHandler := NewAgentHandler(executor, options)
	compatibilityHandler := NewCompatibilityHandler(load)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case bidiAppendProcedure, directRunProcedure, runSSEProcedure:
			agentHandler.ServeHTTP(writer, request)
		default:
			compatibilityHandler.ServeHTTP(writer, request)
		}
	})
}

type agentHandler struct {
	executor            AgentExecutor
	rootContext         context.Context
	heartbeatInterval   time.Duration
	startTimeout        time.Duration
	sessionRetention    time.Duration
	maxRetainedSessions int
	mcpDiscoveryTimeout time.Duration
	now                 func() time.Time

	mu            sync.Mutex
	sessions      map[string]*agentSession
	nextMessageID atomic.Uint32
}

func (handler *agentHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case bidiAppendProcedure:
		handler.serveBidiAppend(writer, request)
	case directRunProcedure:
		handler.serveDirectRun(writer, request)
	case runSSEProcedure:
		handler.serveRunSSE(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (handler *agentHandler) serveDirectRun(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeAgentHTTPError(writer, http.StatusMethodNotAllowed, "invalid_argument", "method not allowed")
		return
	}
	if !hasMediaType(request, "application/connect+proto") {
		writeAgentHTTPError(writer, http.StatusUnsupportedMediaType, "invalid_argument", "unsupported content type")
		return
	}
	flag, payload, err := protocol.ReadConnectMessage(request.Body, maxAgentRequestBytes)
	if err != nil || flag != 0 {
		writeAgentHTTPError(writer, http.StatusBadRequest, "invalid_argument", "invalid Connect request")
		return
	}
	message, err := protocol.DecodeAgentClientMessage(payload)
	if err != nil {
		writeAgentHTTPError(writer, http.StatusBadRequest, "invalid_argument", "initial run message is invalid")
		return
	}
	switch message.Kind {
	case protocol.ClientMessageExec:
		if err := handler.applyDetachedToolResult(message); err != nil {
			writeAgentHTTPError(writer, http.StatusBadRequest, "invalid_argument", "tool result does not match an active run")
			return
		}
		writeDirectRunAcknowledgement(writer)
		return
	case protocol.ClientMessageExecControl, protocol.ClientMessageHeartbeat:
		writeDirectRunAcknowledgement(writer)
		return
	case protocol.ClientMessageRun:
		if message.Run == nil {
			writeAgentHTTPError(writer, http.StatusBadRequest, "invalid_argument", "initial run message is invalid")
			return
		}
	default:
		writeAgentHTTPError(writer, http.StatusBadRequest, "invalid_argument", "initial agent message is unsupported")
		return
	}
	if handler.executor == nil {
		writeAgentHTTPError(writer, http.StatusServiceUnavailable, "unavailable", "agent executor unavailable")
		return
	}
	requestID, err := directSessionID(*message.Run)
	if err != nil {
		writeAgentHTTPError(writer, http.StatusInternalServerError, "internal", "create local request ID")
		return
	}
	session := handler.session(requestID)
	started, err := session.start(handler.rootContext, *message.Run)
	if err != nil {
		writeAgentHTTPError(writer, http.StatusConflict, "already_exists", "run request already exists")
		return
	}
	if started {
		go handler.execute(session, *message.Run)
	}
	inputDone := make(chan struct{})
	go func() {
		defer close(inputDone)
		handler.consumeDirectInputs(request.Context(), request.Body, session)
	}()
	_ = http.NewResponseController(writer).EnableFullDuplex()
	handler.streamSession(writer, request, session)
	waitForDirectInputClose(request.Context(), handler.rootContext, inputDone)
}

func waitForDirectInputClose(requestContext, rootContext context.Context, inputDone <-chan struct{}) {
	timer := time.NewTimer(directHalfCloseTimeout)
	defer timer.Stop()
	select {
	case <-inputDone:
	case <-requestContext.Done():
	case <-rootContext.Done():
	case <-timer.C:
	}
}

func (handler *agentHandler) consumeDirectInputs(ctx context.Context, body io.Reader, session *agentSession) {
	for {
		flag, payload, err := protocol.ReadConnectMessage(body, maxAgentRequestBytes)
		if errors.Is(err, io.EOF) || ctx.Err() != nil {
			return
		}
		if err != nil {
			session.finish(nil, "invalid_argument", "invalid client stream frame")
			return
		}
		if flag == 0x02 {
			return
		}
		message, err := protocol.DecodeAgentClientMessage(payload)
		if err != nil {
			session.finish(nil, "invalid_argument", "invalid agent client message")
			return
		}
		switch message.Kind {
		case protocol.ClientMessageHeartbeat, protocol.ClientMessageExecControl:
			continue
		case protocol.ClientMessageExec:
			if err := session.applyToolResult(message); err != nil {
				session.finish(nil, "invalid_argument", "invalid tool result")
				return
			}
			continue
		}
		session.finish(nil, "unimplemented", "additional agent messages are not implemented")
		return
	}
}

func (handler *agentHandler) serveBidiAppend(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeAgentHTTPError(writer, http.StatusMethodNotAllowed, "invalid_argument", "method not allowed")
		return
	}
	if !hasMediaType(request, "application/proto") {
		writeAgentHTTPError(writer, http.StatusUnsupportedMediaType, "invalid_argument", "unsupported content type")
		return
	}
	body, err := readAgentBody(request.Body, maxAgentRequestBytes)
	if err != nil {
		writeAgentHTTPError(writer, statusForAgentReadError(err), "invalid_argument", err.Error())
		return
	}
	appendRequest, err := protocol.DecodeBidiAppendRequest(body)
	if err != nil {
		writeAgentHTTPError(writer, http.StatusBadRequest, "invalid_argument", "invalid bidi append request")
		return
	}
	if appendRequest.Message.Kind != protocol.ClientMessageRun || appendRequest.Message.Run == nil {
		writeAgentHTTPError(writer, http.StatusBadRequest, "invalid_argument", "unsupported agent client message")
		return
	}
	if handler.executor == nil {
		writeAgentHTTPError(writer, http.StatusServiceUnavailable, "unavailable", "agent executor unavailable")
		return
	}
	session := handler.session(appendRequest.RequestID)
	started, err := session.start(handler.rootContext, *appendRequest.Message.Run)
	if err != nil {
		writeAgentHTTPError(writer, http.StatusConflict, "already_exists", "request ID already has a different run")
		return
	}
	if started {
		go handler.execute(session, *appendRequest.Message.Run)
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Connect-Protocol-Version", "1")
	writer.Header().Set("Content-Type", "application/proto")
	writer.WriteHeader(http.StatusOK)
}

func (handler *agentHandler) execute(session *agentSession, request protocol.RunRequest) {
	enriched, err := session.enrichRunWithMCP(request)
	if err != nil {
		if errors.Is(err, context.Canceled) && session.context().Err() != nil {
			session.finish(nil, "canceled", "request canceled")
			return
		}
		session.finish(nil, "internal", "MCP discovery failed")
		return
	}
	request = enriched
	var usage protocol.TokenUsage
	err = handler.executor.Execute(session.context(), request, func(event AgentEvent) error {
		switch event.Kind {
		case AgentEventTextDelta:
			payload, err := protocol.EncodeTextDelta(event.Text)
			if err != nil {
				return err
			}
			return session.publish(payload)
		case AgentEventReasoningDelta:
			payload, err := protocol.EncodeThinkingDelta(event.Text)
			if err != nil {
				return err
			}
			return session.publish(payload)
		case AgentEventToolCall:
			return session.openTool(event)
		case AgentEventUsage:
			usage = event.Usage
			return nil
		default:
			return errors.New("unsupported agent event")
		}
	})
	if err != nil {
		if errors.Is(err, context.Canceled) && session.context().Err() != nil {
			session.finish(nil, "canceled", "request canceled")
			return
		}
		session.finish(nil, providerConnectErrorCode(err), "provider request failed")
		return
	}
	turnEnded, encodeError := protocol.EncodeTurnEnded(usage)
	if encodeError != nil {
		session.finish(nil, "internal", "provider response invalid")
		return
	}
	session.finish(turnEnded, "", "")
}

func providerConnectErrorCode(err error) string {
	var providerError *provider.Error
	if !errors.As(err, &providerError) {
		return "internal"
	}
	switch providerError.Code {
	case "canceled",
		"unknown",
		"invalid_argument",
		"deadline_exceeded",
		"not_found",
		"already_exists",
		"permission_denied",
		"resource_exhausted",
		"failed_precondition",
		"aborted",
		"out_of_range",
		"unimplemented",
		"internal",
		"unavailable",
		"data_loss",
		"unauthenticated":
		return providerError.Code
	default:
		return "internal"
	}
}

func (handler *agentHandler) serveRunSSE(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeAgentHTTPError(writer, http.StatusMethodNotAllowed, "invalid_argument", "method not allowed")
		return
	}
	if !hasMediaType(request, "application/connect+proto") {
		writeAgentHTTPError(writer, http.StatusUnsupportedMediaType, "invalid_argument", "unsupported content type")
		return
	}
	body, err := readAgentBody(request.Body, maxAgentRequestBytes+5)
	if err != nil {
		writeAgentHTTPError(writer, statusForAgentReadError(err), "invalid_argument", err.Error())
		return
	}
	requestMessage, err := protocol.DecodeConnectRequest(body, maxAgentRequestBytes)
	if err != nil {
		writeAgentHTTPError(writer, http.StatusBadRequest, "invalid_argument", "invalid Connect request")
		return
	}
	requestID, err := protocol.DecodeBidiRequestID(requestMessage)
	if err != nil {
		writeAgentHTTPError(writer, http.StatusBadRequest, "invalid_argument", "invalid request ID")
		return
	}

	session := handler.session(requestID)
	handler.streamSession(writer, request, session)
}

func (handler *agentHandler) streamSession(writer http.ResponseWriter, request *http.Request, session *agentSession) {
	session.subscribe()
	defer session.unsubscribe()
	writer.Header().Set("Cache-Control", "no-cache, no-store")
	writer.Header().Set("Connect-Protocol-Version", "1")
	writer.Header().Set("Content-Type", "application/connect+proto")
	writer.WriteHeader(http.StatusOK)
	flushResponse(writer)

	heartbeat := time.NewTicker(handler.heartbeatInterval)
	defer heartbeat.Stop()
	startTimer := time.NewTimer(handler.startTimeout)
	defer startTimer.Stop()
	startChannel := startTimer.C
	cursor := 0
	for {
		events, done, errorCode, errorMessage, started := session.snapshot(cursor)
		if started && startChannel != nil {
			if !startTimer.Stop() {
				select {
				case <-startTimer.C:
				default:
				}
			}
			startChannel = nil
		}
		for _, event := range events {
			if _, err := writer.Write(protocol.EncodeConnectMessage(event)); err != nil {
				return
			}
			cursor++
			flushResponse(writer)
		}
		if done {
			_, _ = writer.Write(protocol.EncodeConnectEnd(errorCode, errorMessage))
			flushResponse(writer)
			return
		}

		select {
		case <-request.Context().Done():
			return
		case <-handler.rootContext.Done():
			return
		case <-session.notification():
			continue
		case <-startChannel:
			session.finish(nil, "invalid_argument", "run request was not appended")
			startChannel = nil
		case <-heartbeat.C:
			payload, encodeError := protocol.EncodeHeartbeat()
			if encodeError != nil {
				return
			}
			if _, err := writer.Write(protocol.EncodeConnectMessage(payload)); err != nil {
				return
			}
			flushResponse(writer)
		}
	}
}

func newDirectRequestID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func directSessionID(request protocol.RunRequest) (string, error) {
	if request.UserMessageID == "" {
		return newDirectRequestID()
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(request.ConversationID))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(request.UserMessageID))
	return "direct-" + hex.EncodeToString(digest.Sum(nil)), nil
}

func (handler *agentHandler) session(requestID string) *agentSession {
	now := handler.now()
	handler.mu.Lock()
	defer handler.mu.Unlock()
	handler.pruneExpiredSessionsLocked(now)
	if existing := handler.sessions[requestID]; existing != nil {
		return existing
	}
	handler.makeRoomForSessionLocked()
	session := &agentSession{
		notify:              make(chan struct{}, 1),
		allocateMessageID:   handler.allocateMessageID,
		mcpDiscoveryTimeout: handler.mcpDiscoveryTimeout,
		now:                 handler.now,
	}
	handler.sessions[requestID] = session
	return session
}

func (handler *agentHandler) pruneExpiredSessionsLocked(now time.Time) {
	for requestID, session := range handler.sessions {
		done, terminalAt := session.terminalState()
		if done && !terminalAt.IsZero() && !now.Before(terminalAt.Add(handler.sessionRetention)) {
			delete(handler.sessions, requestID)
		}
	}
}

func (handler *agentHandler) makeRoomForSessionLocked() {
	for len(handler.sessions) >= handler.maxRetainedSessions {
		var oldestRequestID string
		var oldestTerminalAt time.Time
		for requestID, session := range handler.sessions {
			done, terminalAt := session.terminalState()
			if !done || terminalAt.IsZero() {
				continue
			}
			if oldestRequestID == "" || terminalAt.Before(oldestTerminalAt) {
				oldestRequestID = requestID
				oldestTerminalAt = terminalAt
			}
		}
		if oldestRequestID == "" {
			return
		}
		delete(handler.sessions, oldestRequestID)
	}
}

func (handler *agentHandler) allocateMessageID() uint32 {
	value := handler.nextMessageID.Add(1)
	if value == 0 {
		value = handler.nextMessageID.Add(1)
	}
	return value
}

func (handler *agentHandler) applyDetachedToolResult(message protocol.ClientMessage) error {
	identity, err := protocol.DecodeExecResultIdentity(message)
	if err != nil {
		return err
	}
	handler.mu.Lock()
	handler.pruneExpiredSessionsLocked(handler.now())
	sessions := make([]*agentSession, 0, len(handler.sessions))
	for _, session := range handler.sessions {
		sessions = append(sessions, session)
	}
	handler.mu.Unlock()
	var matched *agentSession
	for _, session := range sessions {
		if !session.matchesToolResult(identity) {
			continue
		}
		if matched != nil {
			return errors.New("tool result matches multiple sessions")
		}
		matched = session
	}
	if matched == nil {
		return errors.New("tool result does not match a pending session")
	}
	return matched.applyToolResult(message)
}

type agentSession struct {
	mu sync.Mutex

	run                 protocol.RunRequest
	started             bool
	done                bool
	events              [][]byte
	errorCode           string
	errorMessage        string
	terminalAt          time.Time
	notify              chan struct{}
	ctx                 context.Context
	cancel              context.CancelFunc
	now                 func() time.Time
	subscribers         int
	allocateMessageID   func() uint32
	pendingByMessage    map[uint32]*pendingTool
	pendingByExec       map[string]*pendingTool
	mcpDiscovery        *pendingMCPState
	discoveredMCPTools  []protocol.MCPToolDefinition
	mcpDiscoveryTimeout time.Duration
}

type pendingMCPState struct {
	messageID uint32
	execID    string
	result    chan protocol.MCPStateResult
}

type pendingTool struct {
	request     protocol.ToolRequest
	transport   protocol.ToolRequest
	readRequest *protocol.ReadToolRequest
	result      chan<- agent.ToolResult
	stdout      strings.Builder
	stderr      strings.Builder
	truncated   bool
	editPhase   editPhase
}

type editPhase uint8

const (
	editPhaseNone editPhase = iota
	editPhaseRead
	editPhaseWrite
)

func (session *agentSession) start(parent context.Context, request protocol.RunRequest) (bool, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.started {
		if !session.run.Equal(request) {
			return false, errors.New("request ID already started with a different run")
		}
		return false, nil
	}
	if session.done {
		return false, errors.New("request ID is already terminal")
	}
	session.started = true
	session.run = request
	session.ctx, session.cancel = context.WithCancel(parent)
	session.signalLocked()
	return true, nil
}

func (session *agentSession) context() context.Context {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.ctx == nil {
		return context.Background()
	}
	return session.ctx
}

func (session *agentSession) enrichRunWithMCP(request protocol.RunRequest) (protocol.RunRequest, error) {
	if !request.MCPToolsPresent || len(request.MCPTools) > 0 {
		return request, nil
	}
	execSuffix, err := newDirectRequestID()
	if err != nil {
		return protocol.RunRequest{}, errors.New("create MCP discovery exec ID")
	}
	session.mu.Lock()
	if session.done {
		session.mu.Unlock()
		return protocol.RunRequest{}, errors.New("agent session is already terminal")
	}
	if session.allocateMessageID == nil {
		session.mu.Unlock()
		return protocol.RunRequest{}, errors.New("message ID allocator is unavailable")
	}
	if session.mcpDiscovery != nil {
		session.mu.Unlock()
		return protocol.RunRequest{}, errors.New("MCP discovery is already pending")
	}
	pending := &pendingMCPState{
		messageID: session.allocateMessageID(),
		execID:    "exec-mcp-state-" + execSuffix,
		result:    make(chan protocol.MCPStateResult, 1),
	}
	payload, err := protocol.EncodeMCPStateRequest(pending.messageID, pending.execID)
	if err != nil {
		session.mu.Unlock()
		return protocol.RunRequest{}, err
	}
	session.mcpDiscovery = pending
	session.events = append(session.events, payload)
	session.signalLocked()
	ctx := session.ctx
	session.mu.Unlock()

	timer := time.NewTimer(session.mcpDiscoveryTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		session.clearMCPDiscovery(pending)
		return protocol.RunRequest{}, ctx.Err()
	case result := <-pending.result:
		return session.completeMCPDiscovery(request, result)
	case <-timer.C:
		if session.clearMCPDiscovery(pending) {
			return protocol.RunRequest{}, errors.New("MCP state request timed out")
		}
		select {
		case result := <-pending.result:
			return session.completeMCPDiscovery(request, result)
		case <-ctx.Done():
			return protocol.RunRequest{}, ctx.Err()
		}
	}
}

func (session *agentSession) clearMCPDiscovery(pending *pendingMCPState) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.mcpDiscovery != pending {
		return false
	}
	session.mcpDiscovery = nil
	return true
}

func (session *agentSession) completeMCPDiscovery(request protocol.RunRequest, result protocol.MCPStateResult) (protocol.RunRequest, error) {
	if result.IsError {
		return protocol.RunRequest{}, errors.New("MCP state is unavailable")
	}
	request.MCPTools = cloneMCPToolDefinitions(result.Tools)
	session.mu.Lock()
	session.discoveredMCPTools = cloneMCPToolDefinitions(result.Tools)
	session.mu.Unlock()
	return request, nil
}

func (session *agentSession) publish(payload []byte) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.done {
		return errors.New("agent session is already terminal")
	}
	if session.ctx != nil {
		if err := session.ctx.Err(); err != nil {
			return err
		}
	}
	session.events = append(session.events, append([]byte(nil), payload...))
	session.signalLocked()
	return nil
}

func (session *agentSession) openTool(event AgentEvent) error {
	if event.Result == nil {
		return errors.New("agent tool is unsupported")
	}
	execSuffix, err := newDirectRequestID()
	if err != nil {
		return errors.New("create tool exec ID")
	}
	session.mu.Lock()
	if session.done {
		session.mu.Unlock()
		return errors.New("agent session is already terminal")
	}
	if session.allocateMessageID == nil {
		session.mu.Unlock()
		return errors.New("message ID allocator is unavailable")
	}
	request := protocol.ToolRequest{
		MessageID: session.allocateMessageID(),
		ExecID:    "exec-" + strings.ToLower(event.Tool.Name) + "-" + execSuffix,
		CallID:    event.Tool.ID,
		Name:      event.Tool.Name,
		Arguments: event.Tool.Arguments,
	}
	var dispatch protocol.ToolDispatch
	var readRequest *protocol.ReadToolRequest
	phase := editPhaseNone
	transportRequest := request
	switch event.Tool.Name {
	case "Read":
		var arguments struct {
			Path   string  `json:"path"`
			Offset *int32  `json:"offset"`
			Limit  *uint32 `json:"limit"`
		}
		decoder := json.NewDecoder(strings.NewReader(event.Tool.Arguments))
		decoder.DisallowUnknownFields()
		if decodeError := decoder.Decode(&arguments); decodeError != nil || strings.TrimSpace(arguments.Path) == "" {
			session.mu.Unlock()
			return errors.New("Read tool arguments are invalid")
		}
		readRequest = &protocol.ReadToolRequest{
			MessageID: request.MessageID,
			ExecID:    request.ExecID,
			CallID:    request.CallID,
			Path:      arguments.Path,
			Offset:    arguments.Offset,
			Limit:     arguments.Limit,
		}
		dispatch, err = protocol.EncodeReadToolDispatch(*readRequest)
	case "Write", "Delete", "List", "Grep", "Glob", "Shell":
		dispatch, err = protocol.EncodeToolDispatch(request)
	case "Edit":
		arguments, decodeError := protocol.DecodeEditArguments(event.Tool.Arguments)
		if decodeError != nil {
			err = decodeError
			break
		}
		readRequest = &protocol.ReadToolRequest{
			MessageID: request.MessageID,
			ExecID:    request.ExecID,
			CallID:    request.CallID,
			Path:      arguments.Path,
		}
		dispatch, err = protocol.EncodeEditReadDispatch(request)
		phase = editPhaseRead
	default:
		definition, found := runMCPTool(session.effectiveMCPToolsLocked(), event.Tool.Name)
		if !found {
			err = errors.New("agent tool is unsupported")
			break
		}
		var dynamicArguments map[string]json.RawMessage
		if decodeError := json.Unmarshal([]byte(event.Tool.Arguments), &dynamicArguments); decodeError != nil || dynamicArguments == nil {
			err = errors.New("MCP tool arguments are invalid")
			break
		}
		wrappedArguments, encodeError := json.Marshal(struct {
			Name      string          `json:"name"`
			Server    string          `json:"server"`
			ToolName  string          `json:"tool_name"`
			Arguments json.RawMessage `json:"arguments"`
		}{
			Name: definition.Name, Server: definition.ProviderIdentifier, ToolName: definition.ToolName,
			Arguments: json.RawMessage(event.Tool.Arguments),
		})
		if encodeError != nil {
			err = errors.New("encode MCP tool arguments")
			break
		}
		transportRequest.Name = "CallMcpTool"
		transportRequest.Arguments = string(wrappedArguments)
		dispatch, err = protocol.EncodeToolDispatch(transportRequest)
	}
	if err != nil {
		session.mu.Unlock()
		return err
	}
	if session.pendingByMessage == nil {
		session.pendingByMessage = make(map[uint32]*pendingTool)
		session.pendingByExec = make(map[string]*pendingTool)
	}
	pending := &pendingTool{request: request, transport: transportRequest, readRequest: readRequest, result: event.Result, editPhase: phase}
	session.pendingByMessage[request.MessageID] = pending
	session.pendingByExec[request.ExecID] = pending
	session.events = append(session.events, dispatch.Execute, dispatch.Started)
	session.signalLocked()
	session.mu.Unlock()
	return nil
}

func (session *agentSession) matchesToolResult(identity protocol.ExecResultIdentity) bool {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.done {
		return false
	}
	if session.mcpDiscovery != nil && identityMatches(identity, session.mcpDiscovery.messageID, session.mcpDiscovery.execID) {
		return true
	}
	if identity.ExecID != "" {
		pending := session.pendingByExec[identity.ExecID]
		return pending != nil && (identity.MessageID == 0 || pending.transport.MessageID == identity.MessageID)
	}
	return identity.MessageID != 0 && session.pendingByMessage[identity.MessageID] != nil
}

func (session *agentSession) applyToolResult(message protocol.ClientMessage) error {
	identity, err := protocol.DecodeExecResultIdentity(message)
	if err != nil {
		return err
	}
	session.mu.Lock()
	if session.done {
		session.mu.Unlock()
		return errors.New("agent session is already terminal")
	}
	if session.mcpDiscovery != nil && identityMatches(identity, session.mcpDiscovery.messageID, session.mcpDiscovery.execID) {
		pending := session.mcpDiscovery
		result, decodeError := protocol.DecodeMCPStateResult(message, pending.messageID, pending.execID)
		if decodeError != nil {
			session.mu.Unlock()
			return decodeError
		}
		session.mcpDiscovery = nil
		ctx := session.ctx
		session.mu.Unlock()
		select {
		case pending.result <- result:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var pending *pendingTool
	if identity.ExecID != "" {
		pending = session.pendingByExec[identity.ExecID]
	}
	if pending == nil && identity.MessageID != 0 {
		pending = session.pendingByMessage[identity.MessageID]
	}
	if pending == nil {
		session.mu.Unlock()
		return errors.New("result does not match a pending tool")
	}
	var completed []byte
	var content string
	var isError bool
	switch pending.request.Name {
	case "Read":
		if pending.readRequest == nil {
			session.mu.Unlock()
			return errors.New("Read request metadata is unavailable")
		}
		result, decodeError := protocol.DecodeReadToolResult(message)
		if decodeError != nil {
			session.mu.Unlock()
			return decodeError
		}
		completed, err = protocol.EncodeReadToolCompleted(*pending.readRequest, result)
		content = result.Content
		isError = result.IsError
		if isError {
			content = "Read failed: " + result.Error
		}
	case "Edit":
		switch pending.editPhase {
		case editPhaseRead:
			readResult, decodeError := protocol.DecodeReadToolResult(message)
			if decodeError != nil {
				session.mu.Unlock()
				return decodeError
			}
			if readResult.IsError {
				detail := fallbackToolFailure(readResult.Error, "could not read the target file")
				completed, err = protocol.EncodeEditFailureCompleted(pending.request, detail)
				content = "Edit failed: " + detail
				isError = true
				break
			}
			updated, _, applyError := protocol.ApplyEditArguments(pending.request.Arguments, readResult.Content)
			if applyError != nil {
				detail := applyError.Error()
				completed, err = protocol.EncodeEditFailureCompleted(pending.request, detail)
				content = "Edit failed: " + detail
				isError = true
				break
			}
			arguments, decodeError := protocol.DecodeEditArguments(pending.request.Arguments)
			if decodeError != nil {
				session.mu.Unlock()
				return decodeError
			}
			writeArguments, encodeError := json.Marshal(struct {
				Path     string `json:"path"`
				Contents string `json:"contents"`
			}{Path: arguments.Path, Contents: updated})
			if encodeError != nil {
				session.mu.Unlock()
				return errors.New("encode Edit write arguments")
			}
			execSuffix, idError := newDirectRequestID()
			if idError != nil {
				session.mu.Unlock()
				return errors.New("create Edit write exec ID")
			}
			writeRequest := protocol.ToolRequest{
				MessageID: session.allocateMessageID(),
				ExecID:    "exec-edit-write-" + execSuffix,
				CallID:    pending.request.CallID,
				Name:      "Write",
				Arguments: string(writeArguments),
			}
			dispatch, dispatchError := protocol.EncodeToolDispatch(writeRequest)
			if dispatchError != nil {
				session.mu.Unlock()
				return dispatchError
			}
			delete(session.pendingByMessage, pending.transport.MessageID)
			delete(session.pendingByExec, pending.transport.ExecID)
			pending.transport = writeRequest
			pending.readRequest = nil
			pending.editPhase = editPhaseWrite
			session.pendingByMessage[writeRequest.MessageID] = pending
			session.pendingByExec[writeRequest.ExecID] = pending
			session.events = append(session.events, dispatch.Execute)
			session.signalLocked()
			session.mu.Unlock()
			return nil
		case editPhaseWrite:
			result, decodeError := protocol.DecodeToolResult(message, pending.transport)
			if decodeError != nil {
				session.mu.Unlock()
				return decodeError
			}
			completed, err = protocol.EncodeToolCompleted(pending.transport, result)
			content = result.Content
			isError = result.IsError
		default:
			session.mu.Unlock()
			return errors.New("Edit phase is invalid")
		}
	case "Write", "Delete", "List", "Grep", "Glob", "Shell":
		result, decodeError := protocol.DecodeToolResult(message, pending.transport)
		if decodeError != nil {
			session.mu.Unlock()
			return decodeError
		}
		if pending.request.Name == "Shell" {
			publishProgress := false
			switch result.ShellEvent {
			case protocol.ShellEventStdout:
				accepted, truncated := appendBoundedShellOutput(&pending.stdout, result.StdoutDelta)
				result.StdoutDelta = accepted
				pending.truncated = pending.truncated || truncated
				publishProgress = accepted != ""
			case protocol.ShellEventStderr:
				accepted, truncated := appendBoundedShellOutput(&pending.stderr, result.StderrDelta)
				result.StderrDelta = accepted
				pending.truncated = pending.truncated || truncated
				publishProgress = accepted != ""
			case protocol.ShellEventStart:
				publishProgress = true
			}
			if !result.Complete {
				if publishProgress {
					progress, progressError := protocol.EncodeToolProgress(result)
					if progressError != nil {
						session.mu.Unlock()
						return progressError
					}
					session.events = append(session.events, progress)
					session.signalLocked()
				}
				session.mu.Unlock()
				return nil
			}
			if result.ShellEvent == protocol.ShellEventExit {
				progress, progressError := protocol.EncodeToolProgress(result)
				if progressError != nil {
					session.mu.Unlock()
					return progressError
				}
				session.events = append(session.events, progress)
				session.signalLocked()
			}
			result, decodeError = protocol.FinalizeShellToolResult(result, pending.stdout.String(), pending.stderr.String(), pending.truncated)
			if decodeError != nil {
				session.mu.Unlock()
				return decodeError
			}
		}
		if !result.Complete {
			session.mu.Unlock()
			return nil
		}
		completed, err = protocol.EncodeToolCompleted(pending.transport, result)
		content = result.Content
		isError = result.IsError
	default:
		if pending.transport.Name != "CallMcpTool" {
			session.mu.Unlock()
			return errors.New("pending tool is unsupported")
		}
		result, decodeError := protocol.DecodeToolResult(message, pending.transport)
		if decodeError != nil {
			session.mu.Unlock()
			return decodeError
		}
		completed, err = protocol.EncodeToolCompleted(pending.transport, result)
		content = result.Content
		isError = result.IsError
	}
	if err != nil {
		session.mu.Unlock()
		return err
	}
	delete(session.pendingByMessage, pending.transport.MessageID)
	delete(session.pendingByExec, pending.transport.ExecID)
	session.events = append(session.events, completed)
	session.signalLocked()
	ctx := session.ctx
	session.mu.Unlock()
	toolResult := agent.ToolResult{CallID: pending.request.CallID, Content: content, IsError: isError}
	select {
	case pending.result <- toolResult:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func runMCPTool(definitions []protocol.MCPToolDefinition, name string) (protocol.MCPToolDefinition, bool) {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition, true
		}
	}
	return protocol.MCPToolDefinition{}, false
}

func (session *agentSession) effectiveMCPToolsLocked() []protocol.MCPToolDefinition {
	if len(session.discoveredMCPTools) == 0 {
		return session.run.MCPTools
	}
	result := make([]protocol.MCPToolDefinition, 0, len(session.run.MCPTools)+len(session.discoveredMCPTools))
	result = append(result, session.run.MCPTools...)
	result = append(result, session.discoveredMCPTools...)
	return result
}

func cloneMCPToolDefinitions(definitions []protocol.MCPToolDefinition) []protocol.MCPToolDefinition {
	result := make([]protocol.MCPToolDefinition, len(definitions))
	for index, definition := range definitions {
		result[index] = definition
		result[index].InputSchema = append([]byte(nil), definition.InputSchema...)
	}
	return result
}

func identityMatches(identity protocol.ExecResultIdentity, messageID uint32, execID string) bool {
	if identity.ExecID != "" {
		return identity.ExecID == execID && (identity.MessageID == 0 || identity.MessageID == messageID)
	}
	return identity.MessageID != 0 && identity.MessageID == messageID
}

func fallbackToolFailure(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func appendBoundedShellOutput(builder *strings.Builder, delta string) (string, bool) {
	delta = strings.ToValidUTF8(delta, "\uFFFD")
	if delta == "" {
		return "", false
	}
	remaining := maxShellStreamBytes - builder.Len()
	if remaining <= 0 {
		return "", true
	}
	accepted := delta
	truncated := false
	if len(accepted) > remaining {
		accepted = accepted[:remaining]
		for !utf8.ValidString(accepted) && len(accepted) > 0 {
			accepted = accepted[:len(accepted)-1]
		}
		truncated = true
	}
	_, _ = builder.WriteString(accepted)
	return accepted, truncated
}

func writeDirectRunAcknowledgement(writer http.ResponseWriter) {
	_ = http.NewResponseController(writer).EnableFullDuplex()
	writer.Header().Set("Cache-Control", "no-cache, no-store")
	writer.Header().Set("Connect-Protocol-Version", "1")
	writer.Header().Set("Content-Type", "application/connect+proto")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(protocol.EncodeConnectEnd("", ""))
	flushResponse(writer)
}

func (session *agentSession) finish(payload []byte, code, message string) {
	session.mu.Lock()
	if session.done {
		session.mu.Unlock()
		return
	}
	if len(payload) > 0 {
		session.events = append(session.events, append([]byte(nil), payload...))
	}
	session.done = true
	session.errorCode = code
	session.errorMessage = message
	session.pendingByMessage = nil
	session.pendingByExec = nil
	session.mcpDiscovery = nil
	if session.now == nil {
		session.terminalAt = time.Now()
	} else {
		session.terminalAt = session.now()
	}
	cancel := session.cancel
	session.signalLocked()
	session.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (session *agentSession) terminalState() (bool, time.Time) {
	session.mu.Lock()
	defer session.mu.Unlock()
	return session.done, session.terminalAt
}

func (session *agentSession) snapshot(cursor int) (events [][]byte, done bool, code, message string, started bool) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if cursor < 0 {
		cursor = 0
	}
	if cursor < len(session.events) {
		events = make([][]byte, len(session.events)-cursor)
		for index, event := range session.events[cursor:] {
			events[index] = append([]byte(nil), event...)
		}
	}
	return events, session.done, session.errorCode, session.errorMessage, session.started
}

func (session *agentSession) notification() <-chan struct{} {
	return session.notify
}

func (session *agentSession) subscribe() {
	session.mu.Lock()
	session.subscribers++
	session.mu.Unlock()
}

func (session *agentSession) unsubscribe() {
	var cancel context.CancelFunc
	session.mu.Lock()
	if session.subscribers > 0 {
		session.subscribers--
	}
	if session.subscribers == 0 && session.started && !session.done {
		cancel = session.cancel
	}
	session.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (session *agentSession) signalLocked() {
	select {
	case session.notify <- struct{}{}:
	default:
	}
}

func hasMediaType(request *http.Request, expected string) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	return err == nil && mediaType == expected
}

func readAgentBody(body io.Reader, maximum int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maximum+1))
	if err != nil {
		return nil, errors.New("read request body")
	}
	if int64(len(data)) > maximum {
		return nil, errors.New("request body is too large")
	}
	return data, nil
}

func statusForAgentReadError(err error) int {
	if err != nil && bytes.Contains([]byte(err.Error()), []byte("too large")) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}

func writeAgentHTTPError(writer http.ResponseWriter, status int, code, message string) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]string{"code": code, "message": message})
}

func flushResponse(writer http.ResponseWriter) {
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

var _ AgentExecutor = AgentExecutorFunc(nil)
