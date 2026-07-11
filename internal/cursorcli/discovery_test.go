package cursorcli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFindUsesFirstExecutableOnAbsolutePATH(t *testing.T) {
	first := filepath.Join(t.TempDir(), "bin")
	second := filepath.Join(t.TempDir(), "bin")
	want := writeFakeCursorAgent(t, first, "2026.07.08-aaaaaaa")
	writeFakeCursorAgent(t, second, "2026.07.08-bbbbbbb")

	got, err := Find(envLookup(map[string]string{
		"HOME": t.TempDir(),
		"PATH": strings.Join([]string{first, second}, string(os.PathListSeparator)),
	}))
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if got != want {
		t.Fatalf("Find() = %q, want %q", got, want)
	}
}

func TestFindFallsBackToLocalBin(t *testing.T) {
	home := t.TempDir()
	want := writeFakeCursorAgent(t, filepath.Join(home, ".local", "bin"), "2026.07.08-aaaaaaa")

	got, err := Find(envLookup(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if got != want {
		t.Fatalf("Find() = %q, want %q", got, want)
	}
}

func TestFindRejectsRelativePATHEntries(t *testing.T) {
	_, err := Find(envLookup(map[string]string{
		"HOME": t.TempDir(),
		"PATH": "relative-bin",
	}))
	if err == nil {
		t.Fatal("Find() error = nil, want missing cursor-agent error")
	}
	if !strings.Contains(err.Error(), "cursor-agent") || !strings.Contains(err.Error(), "cursor.com/install") {
		t.Fatalf("Find() error = %q, want binary and installation context", err)
	}
}

func TestReadVersionParsesOfficialDateVersion(t *testing.T) {
	path := writeFakeCursorAgent(t, filepath.Join(t.TempDir(), "bin"), "2026.07.08-0c04a8a")

	got, err := ReadVersion(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadVersion() error = %v", err)
	}
	if got.Raw != "2026.07.08-0c04a8a" || got.Year != 2026 || got.Month != 7 || got.Day != 8 || got.Revision != "0c04a8a" {
		t.Fatalf("ReadVersion() = %#v, want parsed official version", got)
	}
}

func TestIsVerifiedVersionMatchesExecutableAcceptanceMatrix(t *testing.T) {
	manifest, err := os.ReadFile("verified_versions.txt")
	if err != nil {
		t.Fatalf("ReadFile(verified_versions.txt) error = %v", err)
	}
	expected := strings.Fields(string(manifest))
	if len(expected) != len(verifiedVersions) {
		t.Fatalf("manifest/map version counts = %d/%d", len(expected), len(verifiedVersions))
	}
	for _, raw := range expected {
		version, err := ParseVersion(raw)
		if err != nil {
			t.Fatalf("ParseVersion(%q) error = %v", raw, err)
		}
		if !IsVerifiedVersion(version) {
			t.Fatalf("IsVerifiedVersion(%q) = false", raw)
		}
	}
	unknown, err := ParseVersion("2026.07.10-unknown")
	if err != nil {
		t.Fatalf("ParseVersion(unknown) error = %v", err)
	}
	if IsVerifiedVersion(unknown) {
		t.Fatal("IsVerifiedVersion(unknown) = true")
	}
}

func writeFakeCursorAgent(t *testing.T, directory, version string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixtures are required")
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	path := filepath.Join(directory, "cursor-agent")
	contents := "#!/bin/sh\nprintf '%s\\n' '" + version + "'\n"
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func envLookup(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}
