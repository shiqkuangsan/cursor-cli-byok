package server

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

const automaticShutdownTimeout = 5 * time.Second

var instanceIDPattern = regexp.MustCompile(`^[0-9a-f]{32,64}$`)

type Options struct {
	ListenAddress string
	Certificate   tls.Certificate
	InstanceID    string
	AuthToken     string
	DaemonVersion string
	Handler       http.Handler
}

type Server struct {
	httpServer  *http.Server
	endpointURL string
	finished    chan struct{}

	mu       sync.Mutex
	serveErr error
}

func Start(ctx context.Context, options Options) (*Server, error) {
	if ctx == nil {
		return nil, errors.New("start server: context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("start server: %w", err)
	}
	if len(options.Certificate.Certificate) == 0 || options.Certificate.PrivateKey == nil {
		return nil, errors.New("start server: TLS certificate is required")
	}
	if !instanceIDPattern.MatchString(options.InstanceID) {
		return nil, errors.New("start server: instance ID is invalid")
	}
	if options.AuthToken == "" || strings.IndexFunc(options.AuthToken, unicode.IsControl) >= 0 {
		return nil, errors.New("start server: auth token is invalid")
	}
	if options.DaemonVersion == "" || options.DaemonVersion != strings.TrimSpace(options.DaemonVersion) || strings.IndexFunc(options.DaemonVersion, unicode.IsControl) >= 0 {
		return nil, errors.New("start server: daemon version is invalid")
	}
	address := options.ListenAddress
	if address == "" {
		address = "127.0.0.1:0"
	}
	if err := validateListenAddress(address); err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("start server: listen: %w", err)
	}

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{options.Certificate},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	}
	applicationHandler := options.Handler
	if applicationHandler == nil {
		applicationHandler = http.NotFoundHandler()
	}
	health := healthHandler(options.InstanceID, options.DaemonVersion)
	expectedAuthorization := []byte("Bearer " + options.AuthToken)
	rootHandler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthz" {
			health.ServeHTTP(writer, request)
			return
		}
		providedAuthorization := []byte(request.Header.Get("Authorization"))
		if len(providedAuthorization) != len(expectedAuthorization) || subtle.ConstantTimeCompare(providedAuthorization, expectedAuthorization) != 1 {
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		applicationHandler.ServeHTTP(writer, request)
	})
	httpServer := &http.Server{
		Handler:           rootHandler,
		TLSConfig:         tlsConfig,
		Protocols:         protocols,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	tcpAddress, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		_ = listener.Close()
		return nil, errors.New("start server: listener did not return a TCP address")
	}
	server := &Server{
		httpServer:  httpServer,
		endpointURL: "https://" + net.JoinHostPort(tcpAddress.IP.String(), strconv.Itoa(tcpAddress.Port)),
		finished:    make(chan struct{}),
	}
	tlsListener := tls.NewListener(listener, tlsConfig)
	go func() {
		err := httpServer.Serve(tlsListener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		server.mu.Lock()
		server.serveErr = err
		server.mu.Unlock()
		close(server.finished)
	}()
	go func() {
		select {
		case <-ctx.Done():
			shutdownContext, cancel := context.WithTimeout(context.Background(), automaticShutdownTimeout)
			defer cancel()
			if err := server.Shutdown(shutdownContext); err != nil {
				_ = httpServer.Close()
			}
		case <-server.finished:
		}
	}()
	return server, nil
}

func (s *Server) EndpointURL() string {
	if s == nil {
		return ""
	}
	return s.endpointURL
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil || s.httpServer == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("shutdown server: context is required")
	}
	if err := s.httpServer.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown server: %w", err)
	}
	return nil
}

func (s *Server) Wait() error {
	if s == nil {
		return errors.New("wait for server: server is required")
	}
	<-s.finished
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serveErr
}

func validateListenAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("start server: listen address must be a loopback host and port")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("start server: listen address must use a literal loopback IP")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 0 || port > 65535 {
		return errors.New("start server: listen address must contain a valid loopback port")
	}
	return nil
}
