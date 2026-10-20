package config

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestConfigValidateDefaultsToHTTP(t *testing.T) {
	cfg := validConfig()
	cfg.Domains[0].Type = ""

	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if cfg.Domains[0].Type != DomainTypeHTTP {
		t.Fatalf("expected domain type to default to %q, got %q", DomainTypeHTTP, cfg.Domains[0].Type)
	}
}

func TestConfigValidateHTTPRejectsTCPFields(t *testing.T) {
	cfg := validConfig()
	cfg.Domains[0].LocalHost = "127.0.0.1"
	cfg.Domains[0].LocalPort = 9000

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for HTTP domain with TCP fields")
	}
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected ValidationError, got %T", err)
	}
}

func TestConfigValidateTCPRequirements(t *testing.T) {
	cfg := validConfig()
	cfg.Domains[0].Type = DomainTypeTCP
	cfg.Domains[0].LocalURL = ""
	cfg.Domains[0].LocalHost = "127.0.0.1"
	cfg.Domains[0].LocalPort = 5432

	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
	if cfg.Domains[0].LocalURL != "" {
		t.Fatalf("expected LocalURL to be cleared for TCP domains, got %q", cfg.Domains[0].LocalURL)
	}
}

func TestConfigValidateTCPMissingFields(t *testing.T) {
	cfg := validConfig()
	cfg.Domains[0].Type = DomainTypeTCP
	cfg.Domains[0].LocalURL = ""
	cfg.Domains[0].LocalHost = ""
	cfg.Domains[0].LocalPort = 0

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for TCP domain without host/port")
	}
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected ValidationError, got %T", err)
	}
}

func TestConfigValidateBaseDomain(t *testing.T) {
	cfg := validConfig()
	cfg.BaseDomain = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error when base_domain is missing")
	}

	cfg = validConfig()
	cfg.BaseDomain = "https://example.com"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for base_domain containing a scheme")
	}

	cfg = validConfig()
	cfg.BaseDomain = " *.Tunnel.Example.com. "
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.BaseDomain != "tunnel.example.com" {
		t.Fatalf("base_domain not normalised: %q", cfg.BaseDomain)
	}
	if cfg.Admin.Host != "admin.tunnel.example.com" {
		t.Fatalf("admin host should derive from base_domain, got %q", cfg.Admin.Host)
	}

	cfg = validConfig()
	cfg.Admin.Host = "ops.example.org"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Admin.Host != "ops.example.org" {
		t.Fatalf("explicit admin host must be kept, got %q", cfg.Admin.Host)
	}
}

func TestConfigValidateDomainsOptional(t *testing.T) {
	cfg := validConfig()
	cfg.Domains = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("domains must be optional (Directus-only operation): %v", err)
	}
}

func TestConfigValidateTLSOptional(t *testing.T) {
	// No TLS at all: plain TCP tunnel listener.
	cfg := validConfig()
	cfg.TLS = TLSConfig{}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("plain mode must be valid: %v", err)
	}

	// TLS without client_ca: server-only TLS (no mTLS).
	cfg = validConfig()
	cfg.TLS.ClientCA = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("server-only TLS must be valid: %v", err)
	}

	// cert without key.
	cfg = validConfig()
	cfg.TLS = TLSConfig{CertPath: "cert.pem"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for cert without key")
	}

	// client_ca without server cert/key.
	cfg = validConfig()
	cfg.TLS = TLSConfig{ClientCA: "ca.pem"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for client_ca without server cert/key")
	}
}

func TestConfigValidateTimezone(t *testing.T) {
	cfg := validConfig()
	cfg.Admin.Timezone = "Not/AZone"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for invalid timezone")
	}
	cfg = validConfig()
	cfg.Admin.Timezone = "Asia/Tokyo"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAdminTokenIssue(t *testing.T) {
	bad := []string{"", "   ", "short", "01234567890123456789012", "change-me", "Change-Me-0123456789012345678901", "local-dev-0123456789012345678901", "replace-with-a-long-random-token"}
	for _, token := range bad {
		if AdminTokenIssue(token) == "" {
			t.Errorf("token %q should be rejected", token)
		}
	}
	for _, token := range []string{"0123456789012345678901234", "k7Qm2x9Zp4Lw8Rt1Vb6Ny3HcJd5Fg0Sa"} {
		if why := AdminTokenIssue(token); why != "" {
			t.Errorf("token %q should be accepted, got %q", token, why)
		}
	}
	if strings.Contains(AdminTokenIssue("change-me"), "change-me-") {
		t.Error("the reason must not repeat the token")
	}
}

func TestParseTrustedProxies(t *testing.T) {
	nets, err := ParseTrustedProxies([]string{"10.0.0.0/8", " 192.168.1.5 ", "", "2001:db8::/32", "::1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nets) != 4 {
		t.Fatalf("expected 4 networks, got %d", len(nets))
	}
	contains := func(ip string) bool {
		for _, n := range nets {
			if n.Contains(net.ParseIP(ip)) {
				return true
			}
		}
		return false
	}
	for ip, want := range map[string]bool{"10.9.9.9": true, "192.168.1.5": true, "192.168.1.6": false, "2001:db8::1": true, "::1": true, "8.8.8.8": false} {
		if contains(ip) != want {
			t.Errorf("contains(%s) = %v, want %v", ip, !want, want)
		}
	}
	for _, bad := range []string{"10.0.0.0/33", "not-an-ip", "10.0.0"} {
		if _, err := ParseTrustedProxies([]string{bad}); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestValidateRejectsBadLimitsAndProxies(t *testing.T) {
	mutations := map[string]func(*Config){
		"negative frame":    func(c *Config) { c.Tunnel.MaxFrameBytes = -1 },
		"negative timeout":  func(c *Config) { c.Tunnel.HandshakeTimeout = -1 },
		"negative req":      func(c *Config) { c.HTTP.MaxRequestBodyBytes = -1 },
		"negative resp":     func(c *Config) { c.HTTP.MaxResponseBodyBytes = -1 },
		"bad trusted proxy": func(c *Config) { c.HTTP.TrustedProxies = []string{"nope"} },
	}
	for name, mutate := range mutations {
		cfg := validConfig()
		mutate(cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults must be valid: %v", err)
	}
	if cfg.Tunnel.FrameLimit() != DefaultMaxFrameBytes || cfg.Tunnel.HandshakeDeadline() != 10*time.Second ||
		cfg.HTTP.RequestBodyLimit() != DefaultMaxBodyBytes || cfg.HTTP.ResponseBodyLimit() != DefaultMaxBodyBytes {
		t.Fatal("zero values must select the documented defaults")
	}
}

func TestEnvOverridesForLimitsAndProxies(t *testing.T) {
	env := map[string]string{
		"GRIDBASE_TUNNEL_MAX_FRAME_BYTES":       "1048576",
		"GRIDBASE_TUNNEL_HANDSHAKE_TIMEOUT":     "3",
		"GRIDBASE_HTTP_MAX_REQUEST_BODY_BYTES":  "2048",
		"GRIDBASE_HTTP_MAX_RESPONSE_BODY_BYTES": "4096",
		"GRIDBASE_HTTP_TRUSTED_PROXIES":         "10.0.0.0/8, 172.16.0.0/12 ,",
	}
	cfg := &Config{}
	if err := applyEnvOverrides(cfg, func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if cfg.Tunnel.MaxFrameBytes != 1048576 || cfg.Tunnel.HandshakeTimeout != 3 ||
		cfg.HTTP.MaxRequestBodyBytes != 2048 || cfg.HTTP.MaxResponseBodyBytes != 4096 {
		t.Fatalf("unexpected limits: %+v %+v", cfg.Tunnel, cfg.HTTP)
	}
	if len(cfg.HTTP.TrustedProxies) != 2 || cfg.HTTP.TrustedProxies[1] != "172.16.0.0/12" {
		t.Fatalf("unexpected trusted proxies %v", cfg.HTTP.TrustedProxies)
	}

	clone := cfg.Clone()
	clone.HTTP.TrustedProxies[0] = "changed"
	if cfg.HTTP.TrustedProxies[0] == "changed" {
		t.Fatal("Clone must deep-copy trusted_proxies")
	}
}
