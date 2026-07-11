package openai

import (
	"context"
	"strings"
	"testing"
)

func TestReadSSEParsesCommentsCRLFMultilineAndFinalEvent(t *testing.T) {
	input := ": keepalive\r\nevent: first\r\ndata: one\r\ndata: two\r\n\r\nevent: final\ndata: three"
	var events []sseEvent
	err := readSSE(context.Background(), strings.NewReader(input), 1024, func(event sseEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("readSSE() error = %v", err)
	}
	want := []sseEvent{{Name: "first", Data: "one\ntwo"}, {Name: "final", Data: "three"}}
	if len(events) != len(want) {
		t.Fatalf("events = %#v", events)
	}
	for index := range want {
		if events[index] != want[index] {
			t.Fatalf("event %d = %#v, want %#v", index, events[index], want[index])
		}
	}
}
