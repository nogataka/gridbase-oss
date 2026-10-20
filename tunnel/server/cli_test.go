package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/directus"
)

func TestConvertRecordsToDomainsWarnings(t *testing.T) {
	records := []directus.TunnelRecord{
		{
			APIKey:    "key-http",
			Subdomain: "alpha",
			Mode:      config.DomainTypeHTTP,
			LocalURL:  "http://localhost:9000",
		},
		{
			APIKey:    "key-tcp",
			Subdomain: "beta",
			Mode:      config.DomainTypeTCP,
			LocalHost: "127.0.0.1",
			LocalPort: 5432,
		},
		{
			APIKey:    "key-tcp-missing",
			Subdomain: "gamma",
			Mode:      config.DomainTypeTCP,
		},
		{
			APIKey:    "key-http-missing",
			Subdomain: "delta",
			Mode:      config.DomainTypeHTTP,
		},
		{
			APIKey:    "key-invalid-mode",
			Subdomain: "epsilon",
			Mode:      "udp",
			LocalURL:  "http://localhost",
		},
	}

	domains, warnings := convertRecordsToDomains(records)

	if len(domains) != 2 {
		t.Fatalf("expected 2 valid domains, got %d", len(domains))
	}
	if domains[0].Type != config.DomainTypeHTTP || domains[0].LocalURL == "" {
		t.Fatalf("unexpected http domain config: %#v", domains[0])
	}
	if domains[1].Type != config.DomainTypeTCP || domains[1].LocalHost == "" || domains[1].LocalPort != 5432 {
		t.Fatalf("unexpected tcp domain config: %#v", domains[1])
	}

	if len(warnings) != 3 {
		t.Fatalf("expected 3 warnings, got %d (%v)", len(warnings), warnings)
	}
}

// A duplicated record is skipped with a warning; it must not fail the whole
// snapshot, and the other records are still applied.
func TestConvertRecordsToDomainsDuplicateIsSkipped(t *testing.T) {
	records := []directus.TunnelRecord{
		{APIKey: "dup-secret-key", Subdomain: "alpha", LocalURL: "http://localhost"},
		{APIKey: "dup-secret-key", Subdomain: "alpha", LocalURL: "http://localhost:8080"},
		{APIKey: "other-key", Subdomain: "beta", LocalURL: "http://localhost:9090"},
	}

	domains, warnings := convertRecordsToDomains(records)
	if len(domains) != 2 {
		t.Fatalf("expected the first alpha and beta to be applied, got %d: %#v", len(domains), domains)
	}
	for _, d := range domains {
		if d.Subdomain == "alpha" && d.LocalURL != "http://localhost" {
			t.Fatalf("the first duplicate must win, got %q", d.LocalURL)
		}
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "duplicate") {
		t.Fatalf("expected one duplicate warning, got %v", warnings)
	}
	if strings.Contains(strings.Join(warnings, " "), "dup-secret-key") {
		t.Fatalf("warnings must not contain the API key: %v", warnings)
	}
}

// Unreadable users.settings on one record skips only that record.
func TestConvertRecordsToDomainsSettingsDecodeFailureIsSkipped(t *testing.T) {
	records := []directus.TunnelRecord{
		{APIKey: "bad-settings-key", Subdomain: "broken", LocalURL: "http://localhost:1", SettingsRaw: json.RawMessage(`["not","an","object"]`)},
		{APIKey: "good-key", Subdomain: "fine", LocalURL: "http://localhost:2", SettingsRaw: json.RawMessage(`{"max_request_bytes": 10}`)},
		{APIKey: "late-key", Subdomain: "broken", LocalURL: "http://localhost:3"},
	}

	domains, warnings := convertRecordsToDomains(records)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "settings") {
		t.Fatalf("expected one settings warning, got %v", warnings)
	}
	if strings.Contains(warnings[0], "bad-settings-key") {
		t.Fatalf("warning must not contain the API key: %q", warnings[0])
	}
	got := map[string]config.DomainConfig{}
	for _, d := range domains {
		got[d.Subdomain] = d
	}
	if len(got) != 2 {
		t.Fatalf("expected the other records to be applied, got %#v", domains)
	}
	if got["fine"].Policy.MaxRequestBytes != 10 {
		t.Fatalf("settings of a healthy record must still be applied: %#v", got["fine"])
	}
	// The skipped record never claimed the subdomain, so a later record can use it.
	if got["broken"].APIKey != "late-key" {
		t.Fatalf("subdomain of a skipped record should stay available, got %#v", got["broken"])
	}
}

func TestConvertRecordsToDomainsMissingFieldsAreSkipped(t *testing.T) {
	records := []directus.TunnelRecord{
		{APIKey: "", Subdomain: "nokey", LocalURL: "http://localhost:1"},
		{APIKey: "k", Subdomain: "", LocalURL: "http://localhost:2"},
		{APIKey: "ok", Subdomain: "ok", LocalURL: "http://localhost:3"},
	}
	domains, warnings := convertRecordsToDomains(records)
	if len(domains) != 1 || domains[0].Subdomain != "ok" {
		t.Fatalf("expected only the complete record, got %#v", domains)
	}
	if len(warnings) != 2 {
		t.Fatalf("expected 2 warnings, got %v", warnings)
	}
}

func TestConvertRecordsToDomainsSkipsSubdomainOwnedByAnotherUser(t *testing.T) {
	records := []directus.TunnelRecord{
		{APIKey: "key-a", Subdomain: "shared", LocalURL: "http://localhost:1"},
		{APIKey: "key-b", Subdomain: "shared", LocalURL: "http://localhost:2"},
		{APIKey: "key-b", Subdomain: "own", LocalURL: "http://localhost:3"},
	}
	domains, warnings := convertRecordsToDomains(records)
	if len(domains) != 2 {
		t.Fatalf("expected 2 domains, got %d (%#v)", len(domains), domains)
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning, got %v", warnings)
	}
	for _, d := range domains {
		if d.Subdomain == "shared" && d.APIKey != "key-a" {
			t.Fatalf("shared subdomain must stay with the first owner, got %q", d.APIKey)
		}
	}
}

func TestApplyDirectusSnapshotKeepsFileDomainsAndSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.json")
	content := `{
		"base_domain": "gridbase.test",
		"domains": [{"api_key": "file-key", "subdomain": "static", "local_url": "http://localhost:9000"}]
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadWithEnv(path, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	manager := config.NewManager(cfg, config.WithFileSource(path), config.WithEnvProvider(func(string) (string, bool) { return "", false }))

	warnings, err := applyDirectusSnapshot(manager, []directus.TunnelRecord{
		{APIKey: "dx", Subdomain: "dyn", Mode: config.DomainTypeHTTP, LocalURL: "http://localhost:3000"},
	})
	if err != nil || len(warnings) != 0 {
		t.Fatalf("apply: err=%v warnings=%v", err, warnings)
	}

	has := func(c *config.Config, sub string) bool {
		for _, d := range c.Domains {
			if d.Subdomain == sub {
				return true
			}
		}
		return false
	}
	if !has(manager.Current(), "static") || !has(manager.Current(), "dyn") {
		t.Fatalf("expected both file and directus domains, got %+v", manager.Current().Domains)
	}

	// POST /api/config path: reload from file must keep the Directus domain.
	reloaded, err := manager.Reload(context.Background())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !has(reloaded, "static") || !has(reloaded, "dyn") {
		t.Fatalf("directus domain lost after reload: %+v", reloaded.Domains)
	}
}
