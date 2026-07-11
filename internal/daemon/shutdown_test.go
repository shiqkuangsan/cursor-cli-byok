package daemon

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestShutdownServiceAuthenticatesOverPinnedTLSBeforeCanceling(t *testing.T) {
	shutdownCalled := make(chan struct{}, 1)
	handler := NewShutdownHandler(func() {
		shutdownCalled <- struct{}{}
	})
	state := startDaemonTestServer(t, handler)

	if err := ShutdownService(context.Background(), state, time.Second); err != nil {
		t.Fatalf("ShutdownService() error = %v", err)
	}
	select {
	case <-shutdownCalled:
	case <-time.After(time.Second):
		t.Fatal("authenticated shutdown did not cancel the service")
	}

	request, err := http.NewRequest(http.MethodPost, state.EndpointURL()+ShutdownPath, nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	client, closeClient, err := newDaemonHTTPClient(state, time.Second)
	if err != nil {
		t.Fatalf("newDaemonHTTPClient() error = %v", err)
	}
	defer closeClient()
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("unauthenticated request error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated shutdown status = %d, want 401", response.StatusCode)
	}
}
