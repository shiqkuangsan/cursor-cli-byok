package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const maxHealthResponseBytes = 8 * 1024

type HTTPProbe struct {
	Timeout time.Duration
}

func (p HTTPProbe) Check(ctx context.Context, state State) error {
	if ctx == nil {
		return errors.New("probe daemon health: context is required")
	}
	client, closeClient, err := newDaemonHTTPClient(state, p.Timeout)
	if err != nil {
		return fmt.Errorf("probe daemon health: %w", err)
	}
	defer closeClient()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, state.EndpointURL()+"/healthz", nil)
	if err != nil {
		return fmt.Errorf("probe daemon health: create request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("probe daemon health: request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("probe daemon health: unexpected status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxHealthResponseBytes+1))
	if err != nil {
		return fmt.Errorf("probe daemon health: read response: %w", err)
	}
	if len(body) > maxHealthResponseBytes {
		return errors.New("probe daemon health: response is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var health struct {
		Status        string `json:"status"`
		InstanceID    string `json:"instance_id"`
		DaemonVersion string `json:"daemon_version"`
	}
	if err := decoder.Decode(&health); err != nil {
		return errors.New("probe daemon health: decode response")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("probe daemon health: decode response")
	}
	if health.Status != "ok" {
		return errors.New("probe daemon health: daemon is not healthy")
	}
	if health.InstanceID != state.InstanceID {
		return errors.New("probe daemon health: instance ID mismatch")
	}
	if health.DaemonVersion != state.DaemonVersion {
		return errors.New("probe daemon health: daemon version mismatch")
	}
	return nil
}
