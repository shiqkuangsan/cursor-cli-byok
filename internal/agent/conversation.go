package agent

import (
	"errors"
	"sync"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/provider"
)

const (
	defaultConversationTurns = 50
	defaultMaxConversations  = 256
)

type ConversationRegistry struct {
	mu               sync.Mutex
	maxTurns         int
	maxConversations int
	sequence         uint64
	conversations    map[string]*conversationState
}

type conversationState struct {
	mu       sync.Mutex
	turns    []conversationTurn
	active   int
	lastUsed uint64
}

type conversationTurn struct {
	messages []provider.Message
}

func NewConversationRegistry(maxTurns int) (*ConversationRegistry, error) {
	if maxTurns <= 0 {
		return nil, errors.New("create conversation registry: max turns must be positive")
	}
	return &ConversationRegistry{
		maxTurns:         maxTurns,
		maxConversations: defaultMaxConversations,
		conversations:    make(map[string]*conversationState),
	}, nil
}

func newDefaultConversationRegistry() *ConversationRegistry {
	registry, _ := NewConversationRegistry(defaultConversationTurns)
	return registry
}

func (registry *ConversationRegistry) acquireConversation(id string) (*conversationState, func()) {
	return registry.acquireConversationState(id, true)
}

func (registry *ConversationRegistry) acquireConversationState(id string, create bool) (*conversationState, func()) {
	registry.mu.Lock()
	conversation := registry.conversations[id]
	if conversation == nil && create {
		conversation = &conversationState{}
		registry.conversations[id] = conversation
	}
	if conversation == nil {
		registry.mu.Unlock()
		return nil, nil
	}
	conversation.active++
	registry.touchLocked(conversation)
	registry.evictInactiveLocked()
	registry.mu.Unlock()

	conversation.mu.Lock()
	return conversation, func() {
		conversation.mu.Unlock()
		registry.mu.Lock()
		conversation.active--
		registry.touchLocked(conversation)
		registry.evictInactiveLocked()
		registry.mu.Unlock()
	}
}

func (registry *ConversationRegistry) touchLocked(conversation *conversationState) {
	registry.sequence++
	conversation.lastUsed = registry.sequence
}

func (registry *ConversationRegistry) evictInactiveLocked() {
	for len(registry.conversations) > registry.maxConversations {
		var oldestID string
		var oldestSequence uint64
		for id, conversation := range registry.conversations {
			if conversation.active != 0 {
				continue
			}
			if oldestID == "" || conversation.lastUsed < oldestSequence {
				oldestID = id
				oldestSequence = conversation.lastUsed
			}
		}
		if oldestID == "" {
			return
		}
		delete(registry.conversations, oldestID)
	}
}

func (registry *ConversationRegistry) Snapshot(id string) []provider.Message {
	if registry == nil {
		return nil
	}
	conversation, release := registry.acquireConversationState(id, false)
	if conversation == nil {
		return nil
	}
	defer release()
	return conversation.messagesLocked()
}

func (conversation *conversationState) messagesLocked() []provider.Message {
	var messageCount int
	for _, turn := range conversation.turns {
		messageCount += len(turn.messages)
	}
	messages := make([]provider.Message, 0, messageCount)
	for _, turn := range conversation.turns {
		for _, message := range turn.messages {
			messages = append(messages, cloneProviderMessage(message))
		}
	}
	return messages
}

func (registry *ConversationRegistry) appendTurnLocked(conversation *conversationState, messages []provider.Message) {
	cloned := make([]provider.Message, len(messages))
	for index, message := range messages {
		cloned[index] = cloneProviderMessage(message)
	}
	conversation.turns = append(conversation.turns, conversationTurn{messages: cloned})
	if extra := len(conversation.turns) - registry.maxTurns; extra > 0 {
		copy(conversation.turns, conversation.turns[extra:])
		conversation.turns = conversation.turns[:registry.maxTurns]
	}
}

func cloneProviderMessage(message provider.Message) provider.Message {
	cloned := message
	cloned.ToolCalls = append([]provider.ToolCall(nil), message.ToolCalls...)
	return cloned
}
