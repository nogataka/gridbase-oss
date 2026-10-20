package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/tunnelserver"
)

// testAdminHost is the default admin host derived from BaseDomain "gridbase.test".
const testAdminHost = "admin.gridbase.test"

// testAdminToken satisfies config.AdminTokenIssue (>= 24 characters, no placeholder prefix).
const testAdminToken = "test-admin-bearer-token-0123456789"

type stubTunnel struct {
	stats       tunnelserver.ServerStats
	tunnelsResp tunnelserver.TunnelsAPIResponse
	metricsResp tunnelserver.MetricsAPIResponse
	reloadedCfg *config.Config
}

func (s *stubTunnel) Stats() tunnelserver.ServerStats {
	return s.stats
}

func (s *stubTunnel) BuildTunnelsResponse() tunnelserver.TunnelsAPIResponse {
	return s.tunnelsResp
}

func (s *stubTunnel) BuildMetricsResponse() tunnelserver.MetricsAPIResponse {
	return s.metricsResp
}

func (s *stubTunnel) Reload(cfg *config.Config) {
	s.reloadedCfg = cfg
}

type stubManager struct {
	cfg         *config.Config
	state       config.RuntimeState
	reloadCount int
	reloadErr   error
}

func (m *stubManager) Current() *config.Config {
	if m.cfg == nil {
		return &config.Config{}
	}
	return m.cfg.Clone()
}

func (m *stubManager) Reload(ctx context.Context) (*config.Config, error) {
	m.reloadCount++
	if m.reloadErr != nil {
		return nil, m.reloadErr
	}
	return m.cfg.Clone(), nil
}

func (m *stubManager) State() config.RuntimeState {
	return m.state
}

func newTestRouter(t *testing.T, tunnel *stubTunnel, manager *stubManager) http.Handler {
	t.Helper()
	return NewRouter(tunnel, manager, nil)
}

func bearerRequest(method, path, token string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, path, body)
	req.Host = testAdminHost
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestRouterUnauthorizedWithoutBearer(t *testing.T) {
	tunnel := &stubTunnel{}
	manager := &stubManager{
		cfg: &config.Config{BaseDomain: "gridbase.test", Admin: config.AdminConfig{BearerToken: testAdminToken}},
	}
	handler := newTestRouter(t, tunnel, manager)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Host = testAdminHost
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRouterHealthzSuccess(t *testing.T) {
	now := time.Now().UTC()
	tunnel := &stubTunnel{
		stats: tunnelserver.ServerStats{
			ActiveConnections: 3,
			CertExpiry:        now.Add(12 * time.Hour),
			LastReload:        now.Add(-1 * time.Hour),
			ErrorCounts:       map[string]int{"forward_send": 1},
			Domains: map[string]tunnelserver.DomainRuntimeStats{
				"key1": {Subdomains: []string{"alpha"}},
			},
		},
	}
	manager := &stubManager{
		cfg:   &config.Config{BaseDomain: "gridbase.test", Admin: config.AdminConfig{BearerToken: testAdminToken}},
		state: config.RuntimeState{Degraded: false},
	}

	handler := newTestRouter(t, tunnel, manager)
	rec := httptest.NewRecorder()
	req := bearerRequest(http.MethodGet, "/api/health", testAdminToken, nil)

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp struct {
		Status         string `json:"status"`
		ActiveTunnels  int    `json:"active_tunnels"`
		ConfigDegraded bool   `json:"config_degraded"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("unexpected status %q", resp.Status)
	}
	if resp.ActiveTunnels != 3 {
		t.Fatalf("expected active_tunnels=3, got %d", resp.ActiveTunnels)
	}
	if resp.ConfigDegraded {
		t.Fatalf("expected config_degraded=false")
	}
}

func TestRouterReloadInvokesManagerAndTunnel(t *testing.T) {
	tunnel := &stubTunnel{
		stats: tunnelserver.ServerStats{
			ActiveConnections: 1,
			ActiveByMode:      map[string]int{config.DomainTypeHTTP: 1},
			ErrorCounts:       map[string]int{"handshake_error": 2},
			ErrorCountsByMode: map[string]map[string]int{
				config.DomainTypeHTTP: {"handshake_error": 2},
			},
		},
	}
	manager := &stubManager{
		cfg: &config.Config{
			BaseDomain: "gridbase.test",
			Admin:      config.AdminConfig{BearerToken: testAdminToken},
			Domains: []config.DomainConfig{
				{APIKey: "k1", Subdomain: "alpha"},
			},
		},
	}
	handler := newTestRouter(t, tunnel, manager)

	rec := httptest.NewRecorder()
	req := bearerRequest(http.MethodPost, "/api/config", testAdminToken, nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if manager.reloadCount != 1 {
		t.Fatalf("expected reload invoked once, got %d", manager.reloadCount)
	}
	if tunnel.reloadedCfg == nil {
		t.Fatalf("expected tunnel reload to be called")
	}

	var resp struct {
		ConfigSummary struct {
			DomainsByMode map[string]int `json:"domains_by_mode"`
		} `json:"config_summary"`
		AppliedSummary struct {
			ActiveConnections int            `json:"active_connections"`
			ActiveByMode      map[string]int `json:"active_by_mode"`
		} `json:"applied_summary"`
		RuntimeErrorCodes []string `json:"runtime_error_codes"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ConfigSummary.DomainsByMode[config.DomainTypeHTTP] != 1 {
		t.Fatalf("expected http domain count 1, got %d", resp.ConfigSummary.DomainsByMode[config.DomainTypeHTTP])
	}
	if resp.AppliedSummary.ActiveConnections != 1 {
		t.Fatalf("expected active connections 1, got %d", resp.AppliedSummary.ActiveConnections)
	}
	if resp.AppliedSummary.ActiveByMode[config.DomainTypeHTTP] != 1 {
		t.Fatalf("expected active http connections 1")
	}
	if len(resp.RuntimeErrorCodes) != 1 || resp.RuntimeErrorCodes[0] != "handshake_error" {
		t.Fatalf("expected runtime error code handshake_error, got %v", resp.RuntimeErrorCodes)
	}
}

func TestRouterTunnelsReturnsSnapshot(t *testing.T) {
	tunnel := &stubTunnel{
		tunnelsResp: tunnelserver.TunnelsAPIResponse{
			Tunnels: []tunnelserver.TunnelDTO{
				{
					KeyID:        "k1",
					Mode:         config.DomainTypeHTTP,
					Subdomains:   []string{"alpha"},
					RemoteAddr:   "10.0.0.1:1234",
					ConnectedAt:  time.Unix(10, 0),
					LastActivity: time.Unix(20, 0),
					BytesIn:      64,
					BytesOut:     32,
				},
			},
		},
	}
	manager := &stubManager{
		cfg: &config.Config{BaseDomain: "gridbase.test", Admin: config.AdminConfig{BearerToken: testAdminToken}},
	}

	handler := newTestRouter(t, tunnel, manager)
	rec := httptest.NewRecorder()
	req := bearerRequest(http.MethodGet, "/api/tunnels", testAdminToken, nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if origin := rec.Header().Get("Access-Control-Allow-Origin"); origin != "" {
		t.Fatalf("expected no CORS header, got %q", origin)
	}

	var resp tunnelserver.TunnelsAPIResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Tunnels) != 1 {
		t.Fatalf("expected 1 tunnel, got %d", len(resp.Tunnels))
	}
	if resp.Tunnels[0].Mode != config.DomainTypeHTTP {
		t.Fatalf("expected tunnel mode http, got %s", resp.Tunnels[0].Mode)
	}
	if resp.Tunnels[0].BytesIn != 64 || resp.Tunnels[0].BytesOut != 32 {
		t.Fatalf("unexpected byte counters %+v", resp.Tunnels[0])
	}
}

func TestRouterOptionsSetsCORSHeadersWhenConfigured(t *testing.T) {
	tunnel := &stubTunnel{}
	manager := &stubManager{
		cfg: &config.Config{
			BaseDomain: "gridbase.test",
			Admin:      config.AdminConfig{BearerToken: testAdminToken, CORSOrigin: "https://app.example.com"},
		},
	}

	handler := newTestRouter(t, tunnel, manager)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/config", nil)
	req.Host = testAdminHost
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}
	if origin := rec.Header().Get("Access-Control-Allow-Origin"); origin != "https://app.example.com" {
		t.Fatalf("unexpected origin header %q", origin)
	}
	if rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatalf("expected Access-Control-Allow-Methods header")
	}

	// Actual (non-preflight) responses carry the allow-origin header too.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, bearerRequest(http.MethodGet, "/api/tunnels", testAdminToken, nil))
	if origin := rec.Header().Get("Access-Control-Allow-Origin"); origin != "https://app.example.com" {
		t.Fatalf("expected allow-origin on GET response, got %q", origin)
	}
}

func TestRouterEmitsNoCORSHeadersWhenOriginUnset(t *testing.T) {
	manager := &stubManager{
		cfg: &config.Config{BaseDomain: "gridbase.test", Admin: config.AdminConfig{BearerToken: testAdminToken}},
	}
	handler := newTestRouter(t, &stubTunnel{}, manager)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodOptions, "/api/config", nil)
	req.Host = testAdminHost
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for preflight without cors_origin, got %d", rec.Code)
	}
	for name := range rec.Header() {
		if len(name) >= 14 && name[:14] == "Access-Control" {
			t.Fatalf("unexpected CORS header %q", name)
		}
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, bearerRequest(http.MethodGet, "/api/tunnels", testAdminToken, nil))
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("unexpected allow-origin on GET response")
	}
}

func TestRouterAdminHostFollowsBaseDomainAndOverride(t *testing.T) {
	manager := &stubManager{
		cfg: &config.Config{BaseDomain: "tunnel.example.com", Admin: config.AdminConfig{BearerToken: testAdminToken}},
	}
	handler := newTestRouter(t, &stubTunnel{}, manager)

	req := bearerRequest(http.MethodGet, "/api/tunnels", testAdminToken, nil)
	req.Host = "admin.tunnel.example.com:8080"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on derived admin host, got %d", rec.Code)
	}

	manager.cfg.Admin.Host = "ops.example.org"
	req = bearerRequest(http.MethodGet, "/api/tunnels", testAdminToken, nil)
	req.Host = "admin.tunnel.example.com"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 once admin.host is overridden, got %d", rec.Code)
	}
}

func TestWithFallbackKeepsTunnelledAPIPathsReachable(t *testing.T) {
	manager := &stubManager{
		cfg: &config.Config{BaseDomain: "gridbase.test", Admin: config.AdminConfig{BearerToken: testAdminToken}},
	}
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	handler := WithFallback(newTestRouter(t, &stubTunnel{}, manager), fallback)

	// Same path on a tunnel host goes to the proxy, not to the admin 404.
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Host = "foo.gridbase.test"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("expected fallback for tunnel host, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, bearerRequest(http.MethodGet, "/api/health", testAdminToken, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected admin handler on admin host, got %d", rec.Code)
	}

	// Non-admin paths on the admin host also fall through.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = testAdminHost
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("expected fallback for non-admin path, got %d", rec.Code)
	}
}

func TestRouterReloadTimestampUsesConfiguredTimezone(t *testing.T) {
	manager := &stubManager{
		cfg: &config.Config{BaseDomain: "gridbase.test", Admin: config.AdminConfig{BearerToken: testAdminToken, Timezone: "Asia/Tokyo"}},
	}
	handler := newTestRouter(t, &stubTunnel{}, manager)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, bearerRequest(http.MethodPost, "/api/config", testAdminToken, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ts, _ := resp["timestamp"].(string)
	if len(ts) < 6 || ts[len(ts)-6:] != "+09:00" {
		t.Fatalf("expected +09:00 offset, got %q", ts)
	}

	manager.cfg.Admin.Timezone = ""
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, bearerRequest(http.MethodPost, "/api/config", testAdminToken, nil))
	resp = nil
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ts, _ = resp["timestamp"].(string)
	if len(ts) < 1 || ts[len(ts)-1:] != "Z" {
		t.Fatalf("expected UTC (Z) timestamp by default, got %q", ts)
	}
}

func TestRouterMetricsReturnsJSON(t *testing.T) {
	now := time.Now().UTC()
	tunnel := &stubTunnel{
		metricsResp: tunnelserver.MetricsAPIResponse{
			GeneratedAt:       now,
			Active:            2,
			ActiveByMode:      map[string]int{config.DomainTypeHTTP: 1, config.DomainTypeTCP: 1},
			BytesIn:           map[string]int64{"k1": 10},
			BytesOut:          map[string]int64{"k1": 4},
			BytesInByMode:     map[string]map[string]int64{config.DomainTypeHTTP: {"k1": 10}},
			BytesOutByMode:    map[string]map[string]int64{config.DomainTypeHTTP: {"k1": 4}},
			ErrorCounts:       map[string]int{"forward_send": 1},
			ErrorCountsByMode: map[string]map[string]int{config.DomainTypeHTTP: {"forward_send": 1}},
			CertExpiry:        now.Add(24 * time.Hour),
			LastReload:        now.Add(-time.Hour),
			Domains: map[string]tunnelserver.Domain{
				"k1": {
					Mode:        config.DomainTypeHTTP,
					Subdomains:  []string{"alpha"},
					BytesIn:     10,
					BytesOut:    4,
					ErrorCounts: map[string]int{"forward_send": 1},
				},
			},
		},
	}
	manager := &stubManager{
		cfg: &config.Config{BaseDomain: "gridbase.test", Admin: config.AdminConfig{BearerToken: testAdminToken}},
	}

	handler := newTestRouter(t, tunnel, manager)
	rec := httptest.NewRecorder()
	req := bearerRequest(http.MethodGet, "/api/metrics", testAdminToken, nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != jsonContentType {
		t.Fatalf("unexpected Content-Type %q", ct)
	}

	var resp tunnelserver.MetricsAPIResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Active != 2 {
		t.Fatalf("expected active connections to equal 2, got %d", resp.Active)
	}
	if resp.ActiveByMode[config.DomainTypeHTTP] != 1 {
		t.Fatalf("expected http active connections 1, got %d", resp.ActiveByMode[config.DomainTypeHTTP])
	}
	if resp.BytesInByMode[config.DomainTypeHTTP]["k1"] != 10 {
		t.Fatalf("expected bytes_in_by_mode to include http/k1 value 10")
	}
	domain := resp.Domains["k1"]
	if len(domain.Subdomains) != 1 || domain.Subdomains[0] != "alpha" {
		t.Fatalf("unexpected subdomains %v", domain.Subdomains)
	}
	if domain.Mode != config.DomainTypeHTTP {
		t.Fatalf("expected domain mode http, got %s", domain.Mode)
	}
	if domain.BytesIn != 10 || domain.BytesOut != 4 {
		t.Fatalf("unexpected domain byte counters")
	}
}

func TestRouterRejectsNonAdminHost(t *testing.T) {
	tunnel := &stubTunnel{}
	manager := &stubManager{
		cfg: &config.Config{BaseDomain: "gridbase.test", Admin: config.AdminConfig{BearerToken: testAdminToken}},
	}

	handler := newTestRouter(t, tunnel, manager)
	rec := httptest.NewRecorder()
	req := bearerRequest(http.MethodGet, "/api/health", testAdminToken, nil)
	req.Host = "example.com"

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for non-admin host, got %d", rec.Code)
	}
}

// A missing, short or placeholder bearer token disables the admin API
// entirely, even for a caller that presents exactly that token.
func TestRouterDisabledForWeakBearerTokens(t *testing.T) {
	weak := []string{
		"",
		"secret",
		"short-token-23-chars-xx", // 23 characters
		"change-me-change-me-change-me",
		"CHANGE-ME-uppercase-prefix-0000",
		"local-dev-admin-token-0123456789",
		"replace-with-a-long-random-token",
	}
	for _, token := range weak {
		var logBuf bytes.Buffer
		logger := logging.New("admin-test", logging.WithWriter(&logBuf))
		manager := &stubManager{
			cfg: &config.Config{BaseDomain: "gridbase.test", Admin: config.AdminConfig{BearerToken: token}},
		}
		handler := NewRouter(&stubTunnel{}, manager, logger)

		if !strings.Contains(logBuf.String(), "admin.api_disabled") {
			t.Errorf("token %q: expected a startup warning, log=%q", token, logBuf.String())
		}
		if token != "" && strings.Contains(logBuf.String(), token) {
			t.Errorf("token %q: the warning must not echo the token: %q", token, logBuf.String())
		}

		for _, path := range []string{"/api/health", "/api/tunnels", "/api/metrics"} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, bearerRequest(http.MethodGet, path, token, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("token %q %s: expected 401 (API disabled), got %d", token, path, rec.Code)
			}
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, bearerRequest(http.MethodPost, "/api/config", token, nil))
		if rec.Code != http.StatusUnauthorized || manager.reloadCount != 0 {
			t.Errorf("token %q: reload must stay unreachable (status %d, reloads %d)", token, rec.Code, manager.reloadCount)
		}
	}
}

func TestRouterAcceptsStrongTokenAndRejectsWrongOne(t *testing.T) {
	var logBuf bytes.Buffer
	manager := &stubManager{
		cfg: &config.Config{BaseDomain: "gridbase.test", Admin: config.AdminConfig{BearerToken: testAdminToken}},
	}
	handler := NewRouter(&stubTunnel{}, manager, logging.New("admin-test", logging.WithWriter(&logBuf)))

	if strings.Contains(logBuf.String(), "admin.api_disabled") {
		t.Fatalf("a strong token must not trigger the warning: %s", logBuf.String())
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, bearerRequest(http.MethodGet, "/api/tunnels", testAdminToken, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with the right token, got %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, bearerRequest(http.MethodGet, "/api/tunnels", testAdminToken+"x", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with a wrong token, got %d", rec.Code)
	}
}

// A reload that swaps in a weak token disables the API and warns once.
func TestRouterWarnsOnceWhenTokenBecomesWeak(t *testing.T) {
	var logBuf bytes.Buffer
	manager := &stubManager{
		cfg: &config.Config{BaseDomain: "gridbase.test", Admin: config.AdminConfig{BearerToken: testAdminToken}},
	}
	handler := NewRouter(&stubTunnel{}, manager, logging.New("admin-test", logging.WithWriter(&logBuf)))

	manager.cfg = &config.Config{BaseDomain: "gridbase.test", Admin: config.AdminConfig{BearerToken: "change-me"}}
	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, bearerRequest(http.MethodGet, "/api/tunnels", "change-me", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 after the token became weak, got %d", rec.Code)
		}
	}
	if n := strings.Count(logBuf.String(), "admin.api_disabled"); n != 1 {
		t.Fatalf("expected exactly one warning, got %d:\n%s", n, logBuf.String())
	}
}

func TestCORSAllowHeadersAreMinimal(t *testing.T) {
	if corsAllowHeaders != "Content-Type, Authorization" {
		t.Fatalf("unexpected CORS allow headers %q", corsAllowHeaders)
	}
}
