package certs

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestManagerGeneratesVerifiableLoopbackCertificateAndReusesIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics are required")
	}
	directory := filepath.Join(t.TempDir(), "certs")
	manager := Manager{Directory: directory}

	first, err := manager.Ensure()
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if first.CACertPath != filepath.Join(directory, "ca.pem") {
		t.Fatalf("CA path = %q, want stable path", first.CACertPath)
	}
	assertMode(t, directory, 0o700)
	assertMode(t, first.CAKeyPath, 0o600)
	assertMode(t, first.ServerKeyPath, 0o600)

	ca := readCertificate(t, first.CACertPath)
	server := readCertificate(t, first.ServerCertPath)
	if !ca.IsCA {
		t.Fatal("CA certificate IsCA = false")
	}
	if err := server.CheckSignatureFrom(ca); err != nil {
		t.Fatalf("server signature verification error = %v", err)
	}
	if err := server.VerifyHostname("localhost"); err != nil {
		t.Fatalf("VerifyHostname(localhost) error = %v", err)
	}
	for _, address := range []string{"127.0.0.1", "::1"} {
		if err := server.VerifyHostname(address); err != nil {
			t.Fatalf("VerifyHostname(%s) error = %v", address, err)
		}
	}
	if len(first.Certificate.Certificate) == 0 || first.Certificate.PrivateKey == nil {
		t.Fatal("TLS certificate was not loaded")
	}

	before := readFiles(t, first)
	second, err := manager.Ensure()
	if err != nil {
		t.Fatalf("Ensure(reuse) error = %v", err)
	}
	after := readFiles(t, second)
	for path, want := range before {
		if got := after[path]; !bytes.Equal(got, want) {
			t.Fatalf("certificate material %s changed during reuse", path)
		}
	}
}

func TestManagerCorrectsPermissionsAndRegeneratesCorruptMaterial(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics are required")
	}
	directory := filepath.Join(t.TempDir(), "certs")
	manager := Manager{Directory: directory}
	bundle, err := manager.Ensure()
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	oldServerCert, err := os.ReadFile(bundle.ServerCertPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatalf("Chmod(directory) error = %v", err)
	}
	if err := os.Chmod(bundle.CAKeyPath, 0o644); err != nil {
		t.Fatalf("Chmod(CA key) error = %v", err)
	}
	if err := os.WriteFile(bundle.ServerCertPath, []byte("corrupt"), 0o644); err != nil {
		t.Fatalf("WriteFile(corrupt) error = %v", err)
	}

	repaired, err := manager.Ensure()
	if err != nil {
		t.Fatalf("Ensure(repair) error = %v", err)
	}
	assertMode(t, directory, 0o700)
	assertMode(t, repaired.CAKeyPath, 0o600)
	newServerCert, err := os.ReadFile(repaired.ServerCertPath)
	if err != nil {
		t.Fatalf("ReadFile(repaired) error = %v", err)
	}
	if bytes.Equal(oldServerCert, newServerCert) || bytes.Equal(newServerCert, []byte("corrupt")) {
		t.Fatal("corrupt server certificate was not regenerated")
	}
	server := readCertificate(t, repaired.ServerCertPath)
	foundLoopback := false
	for _, address := range server.IPAddresses {
		foundLoopback = foundLoopback || address.Equal(net.ParseIP("127.0.0.1"))
	}
	if !foundLoopback {
		t.Fatal("repaired server certificate has no loopback IP SAN")
	}
}

func TestManagerRejectsRelativeDirectory(t *testing.T) {
	_, err := (Manager{Directory: "relative/certs"}).Ensure()
	if err == nil {
		t.Fatal("Ensure() error = nil, want relative directory rejection")
	}
}

func readCertificate(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("PEM %s does not contain a certificate", path)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(%s) error = %v", path, err)
	}
	return certificate
}

func readFiles(t *testing.T, bundle Bundle) map[string][]byte {
	t.Helper()
	result := make(map[string][]byte)
	for _, path := range []string{bundle.CACertPath, bundle.CAKeyPath, bundle.ServerCertPath, bundle.ServerKeyPath} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", path, err)
		}
		result[path] = data
	}
	return result
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode for %s = %04o, want %04o", path, got, want)
	}
}
