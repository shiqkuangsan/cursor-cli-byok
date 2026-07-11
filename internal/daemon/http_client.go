package daemon

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

const maxDaemonCACertificateBytes = 1024 * 1024

func newDaemonHTTPClient(state State, timeout time.Duration) (*http.Client, func(), error) {
	if err := state.Validate(); err != nil {
		return nil, nil, fmt.Errorf("validate daemon state: %w", err)
	}
	info, err := os.Lstat(state.CACertPath)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect CA certificate: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxDaemonCACertificateBytes {
		return nil, nil, errors.New("CA certificate file is invalid")
	}
	caPEM, err := os.ReadFile(state.CACertPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read CA certificate: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, nil, errors.New("parse CA certificate")
	}
	transport := &http.Transport{
		ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
		},
	}
	if timeout <= 0 {
		timeout = time.Second
	}
	return &http.Client{Transport: transport, Timeout: timeout}, transport.CloseIdleConnections, nil
}
