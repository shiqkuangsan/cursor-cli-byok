package daemon

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/certs"
	localserver "github.com/shiqkuangsan/cursor-cli-byok/internal/server"
)

func TestHTTPProbeAuthenticatesHealthyInstance(t *testing.T) {
	bundle, err := (certs.Manager{Directory: filepath.Join(t.TempDir(), "certs")}).Ensure()
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	instanceID := "0123456789abcdef0123456789abcdef"
	running, err := localserver.Start(ctx, localserver.Options{
		Certificate:   bundle.Certificate,
		InstanceID:    instanceID,
		AuthToken:     "test-local-auth-token",
		DaemonVersion: "dev",
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer func() {
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = running.Shutdown(shutdownContext)
	}()
	endpoint, err := url.Parse(running.EndpointURL())
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	state := validState()
	state.PID = os.Getpid()
	state.Port = portFromURL(t, endpoint)
	state.CACertPath = bundle.CACertPath
	state.InstanceID = instanceID
	state.DaemonVersion = "dev"
	probe := HTTPProbe{Timeout: time.Second}

	if err := probe.Check(context.Background(), state); err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	state.InstanceID = "abcdef0123456789abcdef0123456789"
	if err := probe.Check(context.Background(), state); err == nil || !strings.Contains(err.Error(), "instance") {
		t.Fatalf("Check(mismatch) error = %v, want instance mismatch", err)
	}
}

func portFromURL(t *testing.T, endpoint *url.URL) int {
	t.Helper()
	port, err := strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatalf("Atoi(port) error = %v", err)
	}
	return port
}
