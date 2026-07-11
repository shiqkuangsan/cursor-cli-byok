package certs

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const minimumRemainingValidity = 24 * time.Hour

type Manager struct {
	Directory string
	Now       func() time.Time
}

type Bundle struct {
	CACertPath     string
	CAKeyPath      string
	ServerCertPath string
	ServerKeyPath  string
	Certificate    tls.Certificate
}

func (m Manager) Ensure() (Bundle, error) {
	if m.Directory == "" || !filepath.IsAbs(m.Directory) {
		return Bundle{}, errors.New("ensure certificates: directory must be absolute")
	}
	if err := os.MkdirAll(m.Directory, 0o700); err != nil {
		return Bundle{}, fmt.Errorf("ensure certificates: create directory: %w", err)
	}
	if err := os.Chmod(m.Directory, 0o700); err != nil {
		return Bundle{}, fmt.Errorf("ensure certificates: secure directory permissions: %w", err)
	}
	bundle := Bundle{
		CACertPath:     filepath.Join(m.Directory, "ca.pem"),
		CAKeyPath:      filepath.Join(m.Directory, "ca-key.pem"),
		ServerCertPath: filepath.Join(m.Directory, "server.pem"),
		ServerKeyPath:  filepath.Join(m.Directory, "server-key.pem"),
	}
	now := time.Now().UTC()
	if m.Now != nil {
		now = m.Now().UTC()
	}
	if certificate, err := loadAndValidate(bundle, now); err == nil {
		bundle.Certificate = certificate
		return bundle, nil
	}
	if err := generate(bundle, now); err != nil {
		return Bundle{}, err
	}
	certificate, err := loadAndValidate(bundle, now)
	if err != nil {
		return Bundle{}, fmt.Errorf("ensure certificates: validate generated material: %w", err)
	}
	bundle.Certificate = certificate
	return bundle, nil
}

func generate(bundle Bundle, now time.Time) error {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("ensure certificates: generate CA key: %w", err)
	}
	caSerial, err := randomSerial()
	if err != nil {
		return fmt.Errorf("ensure certificates: generate CA serial: %w", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: caSerial,
		Subject: pkix.Name{
			CommonName:   "cursor-cli-byok local CA",
			Organization: []string{"cursor-cli-byok"},
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("ensure certificates: create CA certificate: %w", err)
	}

	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("ensure certificates: generate server key: %w", err)
	}
	serverSerial, err := randomSerial()
	if err != nil {
		return fmt.Errorf("ensure certificates: generate server serial: %w", err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: serverSerial,
		Subject: pkix.Name{
			CommonName:   "localhost",
			Organization: []string{"cursor-cli-byok"},
		},
		NotBefore:   now.Add(-5 * time.Minute),
		NotAfter:    now.AddDate(1, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		return fmt.Errorf("ensure certificates: create server certificate: %w", err)
	}
	caKeyDER, err := x509.MarshalPKCS8PrivateKey(caKey)
	if err != nil {
		return fmt.Errorf("ensure certificates: encode CA key: %w", err)
	}
	serverKeyDER, err := x509.MarshalPKCS8PrivateKey(serverKey)
	if err != nil {
		return fmt.Errorf("ensure certificates: encode server key: %w", err)
	}

	material := []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{bundle.CACertPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o644},
		{bundle.CAKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: caKeyDER}), 0o600},
		{bundle.ServerCertPath, append(
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}),
			pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...,
		), 0o644},
		{bundle.ServerKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: serverKeyDER}), 0o600},
	}
	for _, file := range material {
		if err := writeAtomic(file.path, file.data, file.mode); err != nil {
			return err
		}
	}
	directoryHandle, err := os.Open(filepath.Dir(bundle.CACertPath))
	if err != nil {
		return fmt.Errorf("ensure certificates: open directory for sync: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("ensure certificates: sync directory: %w", err)
	}
	return nil
}

func loadAndValidate(bundle Bundle, now time.Time) (tls.Certificate, error) {
	for _, file := range []struct {
		path string
		mode os.FileMode
	}{
		{bundle.CACertPath, 0o644},
		{bundle.CAKeyPath, 0o600},
		{bundle.ServerCertPath, 0o644},
		{bundle.ServerKeyPath, 0o600},
	} {
		info, err := os.Lstat(file.path)
		if err != nil {
			return tls.Certificate{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return tls.Certificate{}, errors.New("certificate material must be regular files")
		}
		if err := os.Chmod(file.path, file.mode); err != nil {
			return tls.Certificate{}, err
		}
	}

	caCert, err := readFirstCertificate(bundle.CACertPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	if !caCert.IsCA || now.Before(caCert.NotBefore) || caCert.NotAfter.Sub(now) < minimumRemainingValidity {
		return tls.Certificate{}, errors.New("CA certificate is invalid or expiring")
	}
	caKey, err := readECDSAKey(bundle.CAKeyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	if !publicKeysEqual(caCert.PublicKey, &caKey.PublicKey) {
		return tls.Certificate{}, errors.New("CA certificate and key do not match")
	}

	certificate, err := tls.LoadX509KeyPair(bundle.ServerCertPath, bundle.ServerKeyPath)
	if err != nil {
		return tls.Certificate{}, err
	}
	if len(certificate.Certificate) == 0 {
		return tls.Certificate{}, errors.New("server certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return tls.Certificate{}, err
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	for _, hostname := range []string{"localhost", "127.0.0.1", "::1"} {
		if _, err := leaf.Verify(x509.VerifyOptions{
			Roots:       roots,
			DNSName:     hostname,
			CurrentTime: now,
			KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			return tls.Certificate{}, err
		}
	}
	if leaf.NotAfter.Sub(now) < minimumRemainingValidity {
		return tls.Certificate{}, errors.New("server certificate is expiring")
	}
	certificate.Leaf = leaf
	return certificate, nil
}

func readFirstCertificate(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("certificate PEM is invalid")
	}
	return x509.ParseCertificate(block.Bytes)
}

func readECDSAKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, errors.New("private key PEM is invalid")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ecdsaKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not ECDSA")
	}
	return ecdsaKey, nil
}

func publicKeysEqual(left, right any) bool {
	leftDER, leftError := x509.MarshalPKIXPublicKey(left)
	rightDER, rightError := x509.MarshalPKIXPublicKey(right)
	return leftError == nil && rightError == nil && bytes.Equal(leftDER, rightDER)
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, err
	}
	if serial.Sign() == 0 {
		return big.NewInt(1), nil
	}
	return serial, nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("ensure certificates: create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("ensure certificates: secure temporary file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("ensure certificates: write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("ensure certificates: sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("ensure certificates: close temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("ensure certificates: replace file: %w", err)
	}
	removeTemporary = false
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("ensure certificates: secure file permissions: %w", err)
	}
	return nil
}
