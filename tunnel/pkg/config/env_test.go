package config

import "testing"

func TestApplyEnvOverrides_BaseDomainAndDirectus(t *testing.T) {
	cfg := &Config{}
	env := map[string]string{
		"GRIDBASE_BASE_DOMAIN":       "Tunnel.Example.com",
		"GRIDBASE_DIRECTUS_URL":      "http://directus:8055",
		"GRIDBASE_DIRECTUS_TOKEN":    "static-token",
		"GRIDBASE_ADMIN_CORS_ORIGIN": "https://app.example.com",
		"GRIDBASE_ADMIN_TIMEZONE":    "UTC",
	}
	lookup := func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}

	if err := applyEnvOverrides(cfg, lookup); err != nil {
		t.Fatalf("applyEnvOverrides returned error: %v", err)
	}
	if cfg.BaseDomain != "Tunnel.Example.com" {
		t.Fatalf("unexpected base domain %q", cfg.BaseDomain)
	}
	if cfg.Directus.URL != "http://directus:8055" || cfg.Directus.Token != "static-token" {
		t.Fatalf("unexpected directus config %+v", cfg.Directus)
	}
	if cfg.Admin.CORSOrigin != "https://app.example.com" || cfg.Admin.Timezone != "UTC" {
		t.Fatalf("unexpected admin config %+v", cfg.Admin)
	}
}
