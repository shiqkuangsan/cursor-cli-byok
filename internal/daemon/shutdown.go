package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	ShutdownPath             = "/cursor-cli-byok.v1.Control/Shutdown"
	maxShutdownResponseBytes = 8 * 1024
)

func NewShutdownHandler(shutdown func()) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if shutdown == nil {
			http.Error(writer, "shutdown unavailable", http.StatusServiceUnavailable)
			return
		}
		request.Body = http.MaxBytesReader(writer, request.Body, 1)
		body, err := io.ReadAll(request.Body)
		if err != nil || len(body) != 0 {
			http.Error(writer, "invalid shutdown request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(http.StatusNoContent)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		go shutdown()
	})
}

func ShutdownService(ctx context.Context, state State, timeout time.Duration) error {
	if ctx == nil {
		return errors.New("shutdown daemon service: context is required")
	}
	client, closeClient, err := newDaemonHTTPClient(state, timeout)
	if err != nil {
		return fmt.Errorf("shutdown daemon service: %w", err)
	}
	defer closeClient()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, state.EndpointURL()+ShutdownPath, nil)
	if err != nil {
		return fmt.Errorf("shutdown daemon service: create request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+state.AuthToken)
	request.Header.Set("Cache-Control", "no-store")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("shutdown daemon service: request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxShutdownResponseBytes+1))
	if err != nil {
		return errors.New("shutdown daemon service: read response")
	}
	if len(body) > maxShutdownResponseBytes {
		return errors.New("shutdown daemon service: response is too large")
	}
	if response.StatusCode != http.StatusNoContent {
		return fmt.Errorf("shutdown daemon service: unexpected status %d", response.StatusCode)
	}
	return nil
}
