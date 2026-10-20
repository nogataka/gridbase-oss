package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
)

func TestLoadClientConfigJSONOverrides(t *testing.T) {
	dir := t.TempDir()

	certPath := filepath.Join(dir, "client.crt")
	keyPath := filepath.Join(dir, "client.key")
	caPath := filepath.Join(dir, "ca.crt")
	for _, path := range []string{certPath, keyPath, caPath} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatalf("write cert fixture %s: %v", path, err)
		}
	}

	configPath := filepath.Join(dir, "config.json")
	configBody := fmt.Sprintf(`{
		"server_addr": "file-host:1234",
		"api_key": "file-key",
		"tls": {
			"cert": %q,
			"key": %q,
			"ca": %q
		},
		"http": { "timeout": "45s" },
		"reconnect": {
			"initial": "1s",
			"max": "8s",
			"multiplier": 2,
			"jitter_percent": 0.2
		},
		"heartbeat": {
			"interval": "10s",
			"timeout": "30s"
		}
	}`, certPath, keyPath, caPath)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	env := map[string]string{
		envServerAddr:        "env-host:9443",
		envServerName:        "env.example.local",
		envHTTPTimeout:       "60s",
		envReconnectJitter:   "0.4",
		envHeartbeatInterval: "12s",
	}

	inputs := CLIInputs{
		ConfigPath:     configPath,
		APIKey:         "flag-key",
		TimeoutSeconds: 20,
		FlagsSet: map[string]bool{
			"api_key": true,
			"timeout": true,
		},
	}

	cfg, err := loadClientConfig(inputs, func(key string) (string, bool) {
		val, ok := env[key]
		return val, ok
	})
	if err != nil {
		t.Fatalf("loadClientConfig returned error: %v", err)
	}

	if cfg.ServerAddr != "env-host:9443" {
		t.Fatalf("expected server addr env override, got %s", cfg.ServerAddr)
	}
	if cfg.ServerName != "env.example.local" {
		t.Fatalf("expected server name env override, got %s", cfg.ServerName)
	}
	if cfg.APIKey != "flag-key" {
		t.Fatalf("expected API key from flag, got %s", cfg.APIKey)
	}
	if want := 20 * time.Second; cfg.HTTP.Timeout.Duration != want {
		t.Fatalf("expected timeout %s, got %s", want, cfg.HTTP.Timeout.Duration)
	}
	if want := time.Second; cfg.Reconnect.Initial.Duration != want {
		t.Fatalf("expected reconnect initial %s, got %s", want, cfg.Reconnect.Initial.Duration)
	}
	if want := 8 * time.Second; cfg.Reconnect.Max.Duration != want {
		t.Fatalf("expected reconnect max %s, got %s", want, cfg.Reconnect.Max.Duration)
	}
	if cfg.Reconnect.Multiplier != 2 {
		t.Fatalf("expected multiplier 2, got %f", cfg.Reconnect.Multiplier)
	}
	if cfg.Reconnect.Jitter != 0.4 {
		t.Fatalf("expected jitter 0.4, got %f", cfg.Reconnect.Jitter)
	}
	if want := 12 * time.Second; cfg.Heartbeat.Interval.Duration != want {
		t.Fatalf("expected heartbeat interval %s, got %s", want, cfg.Heartbeat.Interval.Duration)
	}
	if want := 30 * time.Second; cfg.Heartbeat.Timeout.Duration != want {
		t.Fatalf("expected heartbeat timeout %s, got %s", want, cfg.Heartbeat.Timeout.Duration)
	}
	if cfg.Mode != config.DomainTypeHTTP {
		t.Fatalf("expected default mode http, got %s", cfg.Mode)
	}
	if cfg.TunnelID != "" {
		t.Fatalf("expected tunnel_id empty in http mode, got %s", cfg.TunnelID)
	}
	if cfg.HTTP.OverrideURL != "" {
		t.Fatalf("expected override url empty when not specified, got %s", cfg.HTTP.OverrideURL)
	}
	if cfg.TCP.LocalHost != "" || cfg.TCP.LocalPort != 0 {
		t.Fatalf("expected tcp settings empty in http mode, got host=%s port=%d", cfg.TCP.LocalHost, cfg.TCP.LocalPort)
	}
}

func TestLoadClientConfigYAML(t *testing.T) {
	dir := t.TempDir()

	certPath := filepath.Join(dir, "client.crt")
	keyPath := filepath.Join(dir, "client.key")
	caPath := filepath.Join(dir, "ca.crt")
	for _, path := range []string{certPath, keyPath, caPath} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatalf("write cert fixture %s: %v", path, err)
		}
	}

	configPath := filepath.Join(dir, "config.yaml")
	configBody := fmt.Sprintf(`
server_addr: yaml-host:9443
server_name: yaml-host
api_key: yaml-key
tls:
  cert: %s
  key: %s
  ca: %s
http:
  timeout: 40
reconnect:
  initial: 3
  max: 9
  multiplier: 1.5
  jitter_percent: 0.1
heartbeat:
  interval: 5
  timeout: 12
`, quoteForYAML(certPath), quoteForYAML(keyPath), quoteForYAML(caPath))
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write yaml config: %v", err)
	}

	cfg, err := loadClientConfig(CLIInputs{ConfigPath: configPath}, nil)
	if err != nil {
		t.Fatalf("loadClientConfig returned error: %v", err)
	}

	if cfg.ServerAddr != "yaml-host:9443" {
		t.Fatalf("unexpected server addr %s", cfg.ServerAddr)
	}
	if cfg.ServerName != "yaml-host" {
		t.Fatalf("expected server name yaml-host, got %s", cfg.ServerName)
	}
	if cfg.APIKey != "yaml-key" {
		t.Fatalf("unexpected api key %s", cfg.APIKey)
	}
	if want := 40 * time.Second; cfg.HTTP.Timeout.Duration != want {
		t.Fatalf("expected timeout %s, got %s", want, cfg.HTTP.Timeout.Duration)
	}
	if want := 3 * time.Second; cfg.Reconnect.Initial.Duration != want {
		t.Fatalf("expected reconnect initial %s, got %s", want, cfg.Reconnect.Initial.Duration)
	}
	if want := 9 * time.Second; cfg.Reconnect.Max.Duration != want {
		t.Fatalf("expected reconnect max %s, got %s", want, cfg.Reconnect.Max.Duration)
	}
	if cfg.Reconnect.Multiplier != 1.5 {
		t.Fatalf("expected multiplier 1.5, got %f", cfg.Reconnect.Multiplier)
	}
	if cfg.Reconnect.Jitter != 0.1 {
		t.Fatalf("expected jitter 0.1, got %f", cfg.Reconnect.Jitter)
	}
	if want := 5 * time.Second; cfg.Heartbeat.Interval.Duration != want {
		t.Fatalf("expected heartbeat interval %s, got %s", want, cfg.Heartbeat.Interval.Duration)
	}
	if want := 12 * time.Second; cfg.Heartbeat.Timeout.Duration != want {
		t.Fatalf("expected heartbeat timeout %s, got %s", want, cfg.Heartbeat.Timeout.Duration)
	}
}

func TestLoadClientConfigHTTPOverridePrecedence(t *testing.T) {
	dir := t.TempDir()

	certPath := filepath.Join(dir, "client.crt")
	keyPath := filepath.Join(dir, "client.key")
	caPath := filepath.Join(dir, "ca.crt")
	for _, path := range []string{certPath, keyPath, caPath} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatalf("write cert fixture %s: %v", path, err)
		}
	}

	configPath := filepath.Join(dir, "config.json")
	configBody := fmt.Sprintf(`{
		"server_addr": "file-host:1234",
		"server_name": "file-host",
		"api_key": "file-key",
		"tls": {
			"cert": %q,
			"key": %q,
			"ca": %q
		},
		"http": {
			"timeout": "45s",
			"override_url": "http://file-backend.internal"
		},
		"heartbeat": { "interval": "10s", "timeout": "30s" }
	}`, certPath, keyPath, caPath)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	env := map[string]string{
		envHTTPOverrideURL: "https://env-backend.example",
	}

	inputs := CLIInputs{
		ConfigPath:      configPath,
		HTTPOverrideURL: "https://flag-backend.internal",
		FlagsSet:        map[string]bool{"local_server_url": true},
		TimeoutSeconds:  20,
		ServerAddr:      "ignored:9443",
		ServerName:      "ignored",
		APIKey:          "flag-key",
		Mode:            config.DomainTypeHTTP,
		CertPath:        certPath,
		KeyPath:         keyPath,
		CAPath:          caPath,
	}

	cfg, err := loadClientConfig(inputs, func(key string) (string, bool) {
		val, ok := env[key]
		return val, ok
	})
	if err != nil {
		t.Fatalf("loadClientConfig returned error: %v", err)
	}

	if got := cfg.HTTP.OverrideURL; got != "https://flag-backend.internal" {
		t.Fatalf("expected flag override url, got %s", got)
	}
}

func TestClientConfigValidateHTTPOverride(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "client.crt")
	keyPath := filepath.Join(dir, "client.key")
	caPath := filepath.Join(dir, "ca.crt")
	for _, path := range []string{certPath, keyPath, caPath} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatalf("write cert fixture %s: %v", path, err)
		}
	}

	cfg := defaultClientConfig()
	cfg.ServerAddr = "example.com:9443"
	cfg.ServerName = "example.com"
	cfg.APIKey = "unit-test-key"
	cfg.TLS = TLSFiles{
		CertPath: certPath,
		KeyPath:  keyPath,
		CAPath:   caPath,
	}

	cfg.HTTP.OverrideURL = "ftp://invalid.example"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "scheme must be http or https") {
		t.Fatalf("expected invalid scheme error, got %v", err)
	}

	cfg.HTTP.OverrideURL = "http://valid.local"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error validating override url: %v", err)
	}

	cfg.Mode = config.DomainTypeTCP
	cfg.TunnelID = "tcp-123"
	cfg.TCP.LocalHost = "127.0.0.1"
	cfg.TCP.LocalPort = 9000
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "http.override_url cannot be set") {
		t.Fatalf("expected override disallowed in tcp mode, got %v", err)
	}
}

func TestLoadClientConfigTCPMode(t *testing.T) {
	dir := t.TempDir()

	certPath := filepath.Join(dir, "client.crt")
	keyPath := filepath.Join(dir, "client.key")
	caPath := filepath.Join(dir, "ca.crt")
	for _, path := range []string{certPath, keyPath, caPath} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatalf("write cert fixture %s: %v", path, err)
		}
	}

	configPath := filepath.Join(dir, "config.json")
	configBody := fmt.Sprintf(`{
		"server_addr": "tcp-host:9000",
		"server_name": "tcp-host",
		"api_key": "tcp-key",
		"mode": "tcp",
		"tunnel_id": "tcp-service",
		"tls": {
			"cert": %q,
			"key": %q,
			"ca": %q
		},
		"tcp": {
			"local_host": "127.0.0.1",
			"local_port": 6000
		},
		"http": { "timeout": "30s" },
		"reconnect": {
			"initial": "1s",
			"max": "10s",
			"multiplier": 2,
			"jitter_percent": 0.1
		},
		"heartbeat": {
			"interval": "5s",
			"timeout": "15s"
		}
	}`, certPath, keyPath, caPath)
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write tcp config: %v", err)
	}

	cfg, err := loadClientConfig(CLIInputs{ConfigPath: configPath}, nil)
	if err != nil {
		t.Fatalf("loadClientConfig returned error: %v", err)
	}

	if cfg.Mode != config.DomainTypeTCP {
		t.Fatalf("expected tcp mode, got %s", cfg.Mode)
	}
	if cfg.TunnelID != "tcp-service" {
		t.Fatalf("expected tunnel id tcp-service, got %s", cfg.TunnelID)
	}
	if cfg.TCP.LocalHost != "127.0.0.1" {
		t.Fatalf("expected tcp.local_host 127.0.0.1, got %s", cfg.TCP.LocalHost)
	}
	if cfg.TCP.LocalPort != 6000 {
		t.Fatalf("expected tcp.local_port 6000, got %d", cfg.TCP.LocalPort)
	}
}

func TestClientConfigValidateErrors(t *testing.T) {
	cfg := &ClientConfig{}
	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}
	confErr, ok := err.(*ConfigError)
	if !ok {
		t.Fatalf("expected ConfigError, got %T", err)
	}
	if len(confErr.Issues) == 0 {
		t.Fatalf("expected at least one validation issue")
	}
}

func quoteForYAML(path string) string {
	return fmt.Sprintf("%q", path)
}
