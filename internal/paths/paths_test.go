package paths

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveUsesXDGDefaults(t *testing.T) {
	home := t.TempDir()

	got, err := Resolve(envLookup(map[string]string{"HOME": home}))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	want := Paths{
		ConfigDir:  filepath.Join(home, ".config", appName),
		ConfigFile: filepath.Join(home, ".config", appName, "config.yaml"),
		DataDir:    filepath.Join(home, ".local", "share", appName),
		StateDir:   filepath.Join(home, ".local", "state", appName),
	}
	if got != want {
		t.Fatalf("Resolve() = %#v, want %#v", got, want)
	}
}

func TestResolveRejectsNilEnvironmentLookup(t *testing.T) {
	_, err := Resolve(nil)
	if err == nil {
		t.Fatal("Resolve() error = nil, want missing environment lookup error")
	}
	if !strings.Contains(err.Error(), "environment") {
		t.Fatalf("Resolve() error = %q, want environment context", err)
	}
}

func TestResolveUsesAbsoluteXDGOverrides(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(t.TempDir(), "config")
	dataHome := filepath.Join(t.TempDir(), "data")
	stateHome := filepath.Join(t.TempDir(), "state")

	got, err := Resolve(envLookup(map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": configHome,
		"XDG_DATA_HOME":   dataHome,
		"XDG_STATE_HOME":  stateHome,
	}))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	want := Paths{
		ConfigDir:  filepath.Join(configHome, appName),
		ConfigFile: filepath.Join(configHome, appName, "config.yaml"),
		DataDir:    filepath.Join(dataHome, appName),
		StateDir:   filepath.Join(stateHome, appName),
	}
	if got != want {
		t.Fatalf("Resolve() = %#v, want %#v", got, want)
	}
}

func TestResolveRejectsMissingHome(t *testing.T) {
	_, err := Resolve(envLookup(nil))
	if err == nil {
		t.Fatal("Resolve() error = nil, want missing HOME error")
	}
	if !strings.Contains(err.Error(), "HOME") {
		t.Fatalf("Resolve() error = %q, want HOME context", err)
	}
}

func TestResolveRejectsRelativeHome(t *testing.T) {
	_, err := Resolve(envLookup(map[string]string{"HOME": filepath.Join("relative", "home")}))
	if err == nil {
		t.Fatal("Resolve() error = nil, want relative HOME error")
	}
	if !strings.Contains(err.Error(), "HOME") {
		t.Fatalf("Resolve() error = %q, want HOME context", err)
	}
}

func TestResolveRejectsRelativeXDGRoots(t *testing.T) {
	for _, variable := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		t.Run(variable, func(t *testing.T) {
			values := map[string]string{
				"HOME":   t.TempDir(),
				variable: filepath.Join("relative", "root"),
			}

			_, err := Resolve(envLookup(values))
			if err == nil {
				t.Fatalf("Resolve() error = nil, want relative %s error", variable)
			}
			if !strings.Contains(err.Error(), variable) {
				t.Fatalf("Resolve() error = %q, want %s context", err, variable)
			}
		})
	}
}

func envLookup(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}
