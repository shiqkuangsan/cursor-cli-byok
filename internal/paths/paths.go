package paths

import (
	"errors"
	"fmt"
	"path/filepath"
)

const appName = "cursor-cli-byok"

type Paths struct {
	ConfigDir  string
	ConfigFile string
	DataDir    string
	StateDir   string
}

func Resolve(getenv func(string) string) (Paths, error) {
	if getenv == nil {
		return Paths{}, errors.New("resolve paths: environment lookup is required")
	}
	home := getenv("HOME")
	if home == "" {
		return Paths{}, errors.New("resolve paths: HOME is required")
	}
	if !filepath.IsAbs(home) {
		return Paths{}, errors.New("resolve paths: HOME must be absolute")
	}
	configHome, err := xdgRoot(getenv, "XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if err != nil {
		return Paths{}, err
	}
	dataHome, err := xdgRoot(getenv, "XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	if err != nil {
		return Paths{}, err
	}
	stateHome, err := xdgRoot(getenv, "XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	if err != nil {
		return Paths{}, err
	}
	configDir := filepath.Join(configHome, appName)

	return Paths{
		ConfigDir:  configDir,
		ConfigFile: filepath.Join(configDir, "config.yaml"),
		DataDir:    filepath.Join(dataHome, appName),
		StateDir:   filepath.Join(stateHome, appName),
	}, nil
}

func xdgRoot(getenv func(string) string, variable, fallback string) (string, error) {
	root := getenv(variable)
	if root == "" {
		return fallback, nil
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("resolve paths: %s must be absolute", variable)
	}
	return root, nil
}
