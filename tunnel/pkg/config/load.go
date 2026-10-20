package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// ErrDecode indicates the configuration file could not be decoded.
var ErrDecode = errors.New("failed to decode configuration")

// LoadFile reads a configuration file from disk using os.LookupEnv for overrides.
func LoadFile(path string) (*Config, error) {
	return LoadWithEnv(path, os.LookupEnv)
}

// LoadWithEnv reads a configuration file from disk, applies environment overrides, and validates it.
func LoadWithEnv(path string, env EnvProvider) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg, err := decodeConfig(data)
	if err != nil {
		return nil, err
	}

	if err := applyEnvOverrides(cfg, env); err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func decodeConfig(data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecode, err)
	}
	return &cfg, nil
}
