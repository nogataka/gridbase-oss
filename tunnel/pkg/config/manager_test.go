package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadWithEnvJSONAndOverrides(t *testing.T) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "server.json")
	writeConfig(t, path, `{
		"base_domain": "gridbase.test",
		"tls": {"cert": "/certs/server.crt", "key": "/certs/server.key", "client_ca": "/certs/ca.crt"},
		"tunnel": {"port": 9443},
		"http": {"port": 9080},
		"domains": [{"api_key": "alpha", "subdomain": " dev ", "local_url": "http://localhost:9000"}]
	}`)

	cfg, err := LoadWithEnv(path, nil)
	if err != nil {
		t.Fatalf("LoadWithEnv returned error: %v", err)
	}
	if cfg.Tunnel.Port != 9443 {
		t.Fatalf("expected tunnel port 9443, got %d", cfg.Tunnel.Port)
	}
	if cfg.Domains[0].Subdomain != "dev" {
		t.Fatalf("expected trimmed subdomain 'dev', got %q", cfg.Domains[0].Subdomain)
	}

	overrides := map[string]string{
		"GRIDBASE_TUNNEL_PORT":        "9555",
		"GRIDBASE_HTTP_PORT":          "9180",
		"GRIDBASE_TLS_CERT":           "/tmp/cert.pem",
		"GRIDBASE_TLS_KEY":            "/tmp/key.pem",
		"GRIDBASE_CLIENT_CA":          "/tmp/ca.pem",
		"GRIDBASE_ADMIN_BEARER_TOKEN": "override-token",
	}

	cfg, err = LoadWithEnv(path, func(key string) (string, bool) {
		val, ok := overrides[key]
		return val, ok
	})
	if err != nil {
		t.Fatalf("LoadWithEnv with overrides returned error: %v", err)
	}
	if cfg.Tunnel.Port != 9555 {
		t.Fatalf("expected overridden tunnel port 9555, got %d", cfg.Tunnel.Port)
	}
	if cfg.HTTP.Port != 9180 {
		t.Fatalf("expected overridden http port 9180, got %d", cfg.HTTP.Port)
	}
	if cfg.TLS.CertPath != "/tmp/cert.pem" {
		t.Fatalf("expected overridden cert path, got %q", cfg.TLS.CertPath)
	}
	if cfg.Admin.BearerToken != "override-token" {
		t.Fatalf("expected overridden bearer token, got %q", cfg.Admin.BearerToken)
	}
}

func TestManagerReloadSuccessNotifiesWatchers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeConfig(t, path, `{
		"base_domain": "gridbase.test",
		"tls": {"cert": "/certs/server.crt", "key": "/certs/server.key", "client_ca": "/certs/ca.crt"},
		"tunnel": {"port": 9443},
		"http": {"port": 9080},
		"domains": [{"api_key": "alpha", "subdomain": "dev", "local_url": "http://localhost:9000"}]
	}`)

	initial, err := LoadWithEnv(path, nil)
	if err != nil {
		t.Fatalf("LoadWithEnv: %v", err)
	}

	manager := NewManager(initial, WithFileSource(path))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	updates := make(chan *Config, 2)
	go func() {
		if err := manager.Watch(ctx, func(cfg *Config) {
			select {
			case updates <- cfg:
			default:
			}
		}); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Watch returned error: %v", err)
		}
	}()

	select {
	case <-updates:
	case <-time.After(2 * time.Second):
		t.Fatal("expected initial configuration callback")
	}

	writeConfig(t, path, `{
		"base_domain": "gridbase.test",
		"tls": {"cert": "/certs/server.crt", "key": "/certs/server.key", "client_ca": "/certs/ca.crt"},
		"tunnel": {"port": 9666},
		"http": {"port": 9080},
		"domains": [{"api_key": "alpha", "subdomain": "dev", "local_url": "http://localhost:9000"}]
	}`)

	if _, err := manager.Reload(context.Background()); err != nil {
		t.Fatalf("Reload returned error: %v", err)
	}

	select {
	case cfg := <-updates:
		if cfg.Tunnel.Port != 9666 {
			t.Fatalf("expected updated tunnel port 9666, got %d", cfg.Tunnel.Port)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected configuration update callback")
	}
}

func TestManagerReloadFailureKeepsCurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	writeConfig(t, path, `{
		"base_domain": "gridbase.test",
		"tls": {"cert": "/certs/server.crt", "key": "/certs/server.key", "client_ca": "/certs/ca.crt"},
		"tunnel": {"port": 9443},
		"http": {"port": 9080},
		"domains": [{"api_key": "alpha", "subdomain": "dev", "local_url": "http://localhost:9000"}]
	}`)

	initial, err := LoadWithEnv(path, nil)
	if err != nil {
		t.Fatalf("LoadWithEnv: %v", err)
	}

	manager := NewManager(initial, WithFileSource(path))
	original := manager.Current()

	writeConfig(t, path, `{}`)

	if _, err := manager.Reload(context.Background()); err == nil {
		t.Fatal("expected reload to fail for invalid configuration")
	}

	after := manager.Current()
	if after.TLS.CertPath != original.TLS.CertPath {
		t.Fatal("expected current configuration to remain unchanged after failed reload")
	}
}

func TestManagerDegradedState(t *testing.T) {
	cfg := &Config{
		BaseDomain: "gridbase.test",
		TLS:        TLSConfig{CertPath: "cert.pem", KeyPath: "key.pem", ClientCA: "ca.pem"},
		Tunnel: TunnelConfig{
			Port: 9000,
		},
		HTTP: HTTPConfig{
			Port: 8000,
		},
		Domains: []DomainConfig{
			{APIKey: "key", Subdomain: "dev", LocalURL: "http://localhost:9000"},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	manager := NewManager(cfg)

	state := manager.State()
	if state.Degraded {
		t.Fatal("expected non-degraded initial state")
	}

	manager.MarkDegraded(errors.New("directus unavailable"))
	state = manager.State()
	if !state.Degraded {
		t.Fatal("expected degraded state after MarkDegraded")
	}
	if state.Reason == "" {
		t.Fatal("expected degraded reason to be set")
	}

	manager.ClearDegraded()
	state = manager.State()
	if state.Degraded {
		t.Fatal("expected degraded flag cleared")
	}
	if state.Reason != "" {
		t.Fatal("expected reason to be cleared")
	}
}

func TestManagerApplySnapshotNotifiesWatchers(t *testing.T) {
	base := validConfig()
	overrides := map[string]string{
		"GRIDBASE_HTTP_PORT": "9100",
	}

	manager := NewManager(base, WithEnvProvider(func(key string) (string, bool) {
		val, ok := overrides[key]
		return val, ok
	}))

	updateCh := make(chan *Config, 1)
	unsubscribe := manager.Subscribe(func(cfg *Config) {
		updateCh <- cfg
	})
	defer unsubscribe()

	snapshot := base.Clone()
	snapshot.HTTP.Port = 0
	snapshot.Domains[0].APIKey = "beta"
	snapshot.Domains[0].Subdomain = "prod"

	updated, err := manager.ApplySnapshot(snapshot)
	if err != nil {
		t.Fatalf("ApplySnapshot returned error: %v", err)
	}
	if updated.HTTP.Port != 9100 {
		t.Fatalf("expected HTTP port overridden to 9100, got %d", updated.HTTP.Port)
	}
	if updated.Domains[0].APIKey != "beta" {
		t.Fatalf("expected api key updated to beta, got %s", updated.Domains[0].APIKey)
	}

	select {
	case cfg := <-updateCh:
		if cfg.HTTP.Port != 9100 {
			t.Fatalf("expected watcher to observe overridden HTTP port, got %d", cfg.HTTP.Port)
		}
	case <-time.After(time.Second):
		t.Fatal("expected watcher to be notified after ApplySnapshot")
	}

	current := manager.Current()
	if current.Domains[0].APIKey != "beta" {
		t.Fatalf("expected current configuration to use new domain, got %s", current.Domains[0].APIKey)
	}
	snapshot.Domains[0].APIKey = "mutated"
	if manager.Current().Domains[0].APIKey != "beta" {
		t.Fatal("expected manager state to remain isolated from snapshot mutations")
	}
}

func TestManagerApplySnapshotRejectsInvalidSnapshot(t *testing.T) {
	base := validConfig()
	manager := NewManager(base)

	before := manager.Current()
	if _, err := manager.ApplySnapshot(&Config{}); err == nil {
		t.Fatal("expected ApplySnapshot to fail for invalid configuration")
	}

	after := manager.Current()
	if after.Domains[0].APIKey != before.Domains[0].APIKey {
		t.Fatal("expected current configuration to remain unchanged after failed snapshot")
	}
}

func TestManagerFallbackReturnsClone(t *testing.T) {
	base := validConfig()
	manager := NewManager(base)

	fallback := manager.Fallback()
	fallback.Domains[0].APIKey = "mutated"
	if manager.Fallback().Domains[0].APIKey == "mutated" {
		t.Fatal("expected fallback to return a clone independent of callers")
	}
}

func writeConfig(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write config fixture: %v", err)
	}
}

func validConfig() *Config {
	return &Config{
		BaseDomain: "gridbase.test",
		TLS: TLSConfig{
			CertPath: "cert.pem",
			KeyPath:  "key.pem",
			ClientCA: "ca.pem",
		},
		Tunnel: TunnelConfig{Port: 9443},
		HTTP:   HTTPConfig{Port: 9080},
		Domains: []DomainConfig{
			{
				APIKey:    "alpha",
				Subdomain: "dev",
				LocalURL:  "http://localhost:9000",
			},
		},
		Admin: AdminConfig{
			BearerToken: "token",
		},
	}
}

func TestManagerReloadRemergesExternalDomains(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfig(t, path, `{
		"base_domain": "gridbase.test",
		"tunnel": {"port": 9443},
		"http": {"port": 9080},
		"domains": [{"api_key": "static", "subdomain": "web", "local_url": "http://localhost:9000"}]
	}`)

	initial, err := LoadWithEnv(path, nil)
	if err != nil {
		t.Fatalf("LoadWithEnv: %v", err)
	}
	manager := NewManager(initial, WithFileSource(path), WithEnvProvider(func(string) (string, bool) { return "", false }))

	external := []DomainConfig{
		{APIKey: "dx", Subdomain: "app", Type: DomainTypeHTTP, LocalURL: "http://localhost:3000"},
		// Same subdomain as the file entry: the external one wins.
		{APIKey: "dx", Subdomain: "web", Type: DomainTypeHTTP, LocalURL: "http://localhost:4000"},
	}
	cfg, err := manager.ApplyExternalDomains(external)
	if err != nil {
		t.Fatalf("ApplyExternalDomains: %v", err)
	}
	assertSubdomains(t, cfg.Domains, map[string]string{"app": "http://localhost:3000", "web": "http://localhost:4000"})

	// A file reload (POST /api/config) must not drop Directus-sourced domains.
	cfg, err = manager.Reload(context.Background())
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	assertSubdomains(t, cfg.Domains, map[string]string{"app": "http://localhost:3000", "web": "http://localhost:4000"})
	assertSubdomains(t, manager.Current().Domains, map[string]string{"app": "http://localhost:3000", "web": "http://localhost:4000"})

	// File edits are picked up on reload alongside the external set.
	writeConfig(t, path, `{
		"base_domain": "gridbase.test",
		"tunnel": {"port": 9443},
		"http": {"port": 9080},
		"domains": [{"api_key": "static", "subdomain": "docs", "local_url": "http://localhost:9100"}]
	}`)
	cfg, err = manager.Reload(context.Background())
	if err != nil {
		t.Fatalf("Reload after edit: %v", err)
	}
	assertSubdomains(t, cfg.Domains, map[string]string{"docs": "http://localhost:9100", "app": "http://localhost:3000", "web": "http://localhost:4000"})

	// Clearing the external set (Directus now empty) leaves only file domains.
	cfg, err = manager.ApplyExternalDomains(nil)
	if err != nil {
		t.Fatalf("ApplyExternalDomains(nil): %v", err)
	}
	assertSubdomains(t, cfg.Domains, map[string]string{"docs": "http://localhost:9100"})
}

func TestManagerExternalDomainsOnlyWithEmptyFileDomains(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfig(t, path, `{"base_domain": "gridbase.test"}`)

	initial, err := LoadWithEnv(path, nil)
	if err != nil {
		t.Fatalf("config without domains must be valid: %v", err)
	}
	manager := NewManager(initial, WithFileSource(path))
	cfg, err := manager.ApplyExternalDomains([]DomainConfig{{APIKey: "k", Subdomain: "a", LocalURL: "http://x"}})
	if err != nil {
		t.Fatalf("ApplyExternalDomains: %v", err)
	}
	if len(cfg.Domains) != 1 {
		t.Fatalf("expected 1 domain, got %d", len(cfg.Domains))
	}
	cfg, err = manager.Reload(context.Background())
	if err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if len(cfg.Domains) != 1 || cfg.Domains[0].Subdomain != "a" {
		t.Fatalf("external domain lost after reload: %+v", cfg.Domains)
	}
}

func assertSubdomains(t *testing.T, domains []DomainConfig, want map[string]string) {
	t.Helper()
	if len(domains) != len(want) {
		t.Fatalf("expected %d domains, got %d: %+v", len(want), len(domains), domains)
	}
	for _, d := range domains {
		url, ok := want[d.Subdomain]
		if !ok || d.LocalURL != url {
			t.Fatalf("unexpected domain %+v (want %v)", d, want)
		}
	}
}
