package agent

import "testing"

func TestConversationRegistryEvictsLeastRecentlyUsedInactiveConversation(t *testing.T) {
	registry, err := NewConversationRegistry(2)
	if err != nil {
		t.Fatalf("NewConversationRegistry() error = %v", err)
	}
	registry.maxConversations = 2
	for _, conversationID := range []string{"conversation-a", "conversation-b", "conversation-a", "conversation-c"} {
		_, release := registry.acquireConversation(conversationID)
		release()
	}

	registry.mu.Lock()
	_, hasA := registry.conversations["conversation-a"]
	_, hasB := registry.conversations["conversation-b"]
	_, hasC := registry.conversations["conversation-c"]
	count := len(registry.conversations)
	registry.mu.Unlock()
	if count != 2 || !hasA || hasB || !hasC {
		t.Fatalf("retained conversations = count:%d a:%t b:%t c:%t", count, hasA, hasB, hasC)
	}
}

func TestConversationRegistryNeverEvictsActiveConversation(t *testing.T) {
	registry, err := NewConversationRegistry(2)
	if err != nil {
		t.Fatalf("NewConversationRegistry() error = %v", err)
	}
	registry.maxConversations = 1
	activeA, releaseA := registry.acquireConversation("conversation-a")
	_, releaseB := registry.acquireConversation("conversation-b")

	registry.mu.Lock()
	countWhileActive := len(registry.conversations)
	registry.mu.Unlock()
	if countWhileActive != 2 {
		releaseB()
		releaseA()
		t.Fatalf("active conversation count = %d, want temporary overflow of 2", countWhileActive)
	}
	releaseB()

	registry.mu.Lock()
	retainedA := registry.conversations["conversation-a"]
	countAfterRelease := len(registry.conversations)
	registry.mu.Unlock()
	releaseA()
	if countAfterRelease != 1 || retainedA != activeA {
		t.Fatalf("active conversation was evicted: count=%d retained=%p active=%p", countAfterRelease, retainedA, activeA)
	}
}
