package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Store struct {
	path string
}

func NewStore(path string) Store {
	return Store{path: path}
}

func (s Store) Load() (Config, error) {
	if err := s.validatePath("load"); err != nil {
		return Config{}, err
	}
	if err := os.Chmod(filepath.Dir(s.path), 0o700); err != nil {
		return Config{}, fmt.Errorf("load config: secure directory permissions: %w", err)
	}
	info, err := os.Lstat(s.path)
	if err != nil {
		return Config{}, fmt.Errorf("load config: inspect file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return Config{}, errors.New("load config: config file must not be a symlink")
	}
	if !info.Mode().IsRegular() {
		return Config{}, errors.New("load config: config file must be regular")
	}
	if err := os.Chmod(s.path, 0o600); err != nil {
		return Config{}, fmt.Errorf("load config: secure file permissions: %w", err)
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, errors.New("load config: decode YAML")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Config{}, errors.New("load config: decode YAML")
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func (s Store) Save(cfg Config) error {
	if err := s.validatePath("save"); err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("save config: encode YAML: %w", err)
	}
	directory := filepath.Dir(s.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("save config: create directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("save config: secure directory permissions: %w", err)
	}

	temporary, err := os.CreateTemp(directory, "."+filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("save config: create temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("save config: secure temporary file permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("save config: write temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("save config: sync temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("save config: close temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		return fmt.Errorf("save config: replace file: %w", err)
	}
	removeTemporary = false
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("save config: secure file permissions: %w", err)
	}
	return nil
}

func (s Store) validatePath(action string) error {
	if s.path == "" || !filepath.IsAbs(s.path) {
		return fmt.Errorf("%s config: path must be absolute", action)
	}
	cleanPath := filepath.Clean(s.path)
	if filepath.Dir(cleanPath) == cleanPath {
		return fmt.Errorf("%s config: path must name a file", action)
	}
	return nil
}
