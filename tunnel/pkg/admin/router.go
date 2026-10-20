package admin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/tunnelserver"
)

const (
	corsMethods      = "GET, POST, OPTIONS"
	corsAllowHeaders = "Content-Type, Authorization"
	corsMaxAge       = "86400"
	reloadTimeout    = 5 * time.Second
	authHeaderName   = "Authorization"
	authScheme       = "Bearer "
	jsonContentType  = "application/json"
)

// adminPaths are the request paths served by the admin API (on the admin host only).
var adminPaths = map[string]struct{}{
	"/api/health":  {},
	"/api/config":  {},
	"/api/tunnels": {},
	"/api/metrics": {},
}

type tunnelMetricsProvider interface {
	Stats() tunnelserver.ServerStats
	BuildTunnelsResponse() tunnelserver.TunnelsAPIResponse
	BuildMetricsResponse() tunnelserver.MetricsAPIResponse
	Reload(cfg *config.Config)
}

type configManager interface {
	Current() *config.Config
	Reload(ctx context.Context) (*config.Config, error)
	State() config.RuntimeState
}

// Router exposes management HTTP endpoints for health checks, configuration reloads, and observability APIs.
type Router struct {
	tunnel  tunnelMetricsProvider
	manager configManager
	logger  *logging.Logger

	warnMu    sync.Mutex
	warnedWhy string // token problem last reported, so each distinct problem is warned about once
}

// NewRouter constructs an admin router for the supplied tunnel server and configuration manager.
func NewRouter(tunnel tunnelMetricsProvider, manager configManager, logger *logging.Logger) http.Handler {
	if tunnel == nil {
		panic("admin.NewRouter requires a tunnel metrics provider")
	}
	if manager == nil {
		panic("admin.NewRouter requires a configuration manager")
	}
	if logger == nil {
		logger = logging.New("admin")
	}

	r := &Router{
		tunnel:  tunnel,
		manager: manager,
		logger:  logger,
	}
	// Report an unusable token at startup rather than on the first request.
	r.tokenIssue()
	return r
}

// WithFallback dispatches requests for the admin API (admin host + known admin
// path) to router and everything else to fallback. This keeps tunnelled
// applications reachable on paths such as /api/health.
func WithFallback(router http.Handler, fallback http.Handler) http.Handler {
	r, ok := router.(*Router)
	if !ok {
		return router
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if _, isAdminPath := adminPaths[req.URL.Path]; isAdminPath && r.isAdminHost(req) {
			r.ServeHTTP(w, req)
			return
		}
		fallback.ServeHTTP(w, req)
	})
}

func (r *Router) corsOrigin() string {
	cfg := r.manager.Current()
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Admin.CORSOrigin)
}

func (r *Router) location() *time.Location {
	cfg := r.manager.Current()
	if cfg == nil || cfg.Admin.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(cfg.Admin.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// ServeHTTP routes incoming management requests.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if !r.isAdminHost(req) {
		http.NotFound(w, req)
		return
	}

	// CORS is opt-in: headers are only emitted when admin.cors_origin is configured.
	if origin := r.corsOrigin(); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
	}

	switch req.Method {
	case http.MethodOptions:
		r.handleOptions(w, req)
		return
	case http.MethodGet, http.MethodPost:
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !r.authorize(req) {
		w.Header().Set("WWW-Authenticate", "Bearer realm=\"admin\"")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	switch req.URL.Path {
	case "/api/health":
		r.handleHealthz(w, req)
	case "/api/config":
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.handleReload(w, req)
	case "/api/tunnels":
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.handleTunnels(w, req)
	case "/api/metrics":
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		r.handleMetrics(w, req)
	default:
		http.NotFound(w, req)
	}
}

func (r *Router) handleOptions(w http.ResponseWriter, req *http.Request) {
	if r.corsOrigin() == "" {
		http.NotFound(w, req)
		return
	}
	switch req.URL.Path {
	case "/api/config", "/api/tunnels":
		w.Header().Set("Access-Control-Allow-Methods", corsMethods)
		w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
		w.Header().Set("Access-Control-Max-Age", corsMaxAge)
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, req)
	}
}

func (r *Router) handleHealthz(w http.ResponseWriter, req *http.Request) {
	cfgState := r.manager.State()
	stats := r.tunnel.Stats()

	response := map[string]any{
		"status":            "ok",
		"generated_at":      time.Now().UTC(),
		"active_tunnels":    stats.ActiveConnections,
		"cert_expiry":       stats.CertExpiry,
		"last_reload":       stats.LastReload,
		"config_degraded":   cfgState.Degraded,
		"error_counts":      stats.ErrorCounts,
		"active_subdomains": len(stats.Domains),
	}
	if cfgState.Reason != "" {
		response["config_degraded_reason"] = cfgState.Reason
	}

	r.logger.Info("admin health check", logging.Fields{
		"event":              "admin.healthz",
		"active_connections": stats.ActiveConnections,
		"config_degraded":    cfgState.Degraded,
	})
	r.writeJSON(w, response, http.StatusOK)
}

func (r *Router) handleReload(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithTimeout(req.Context(), reloadTimeout)
	defer cancel()

	cfg, err := r.manager.Reload(ctx)
	if err != nil {
		r.logger.Error("configuration reload failed", err, logging.Fields{
			"event": "admin.config_reload",
		})
		http.Error(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	r.tunnel.Reload(cfg)

	state := r.manager.State()
	totalConfigs, totalDomains := summarizeDomains(cfg.Domains)
	domainsByMode := summarizeDomainsByMode(cfg.Domains)
	stats := r.tunnel.Stats()

	snapshotTime := time.Now().In(r.location())
	response := map[string]any{
		"status":    "success",
		"timestamp": snapshotTime.Format(time.RFC3339),
		"config_summary": map[string]any{
			"total_configs":   totalConfigs,
			"total_domains":   totalDomains,
			"domains_by_mode": domainsByMode,
		},
		"applied_summary": map[string]any{
			"active_connections": stats.ActiveConnections,
			"active_by_mode":     copyStringIntMap(stats.ActiveByMode),
		},
	}
	if state.Degraded {
		response["config_degraded"] = true
		if state.Reason != "" {
			response["config_degraded_reason"] = state.Reason
			if warnings := splitWarnings(state.Reason); len(warnings) > 0 {
				response["config_warnings"] = warnings
			}
		}
	}
	if codes, countsByMode := collectErrorSummaries(stats.ErrorCounts, stats.ErrorCountsByMode); len(codes) > 0 {
		response["runtime_error_codes"] = codes
		if len(countsByMode) > 0 {
			response["runtime_error_counts_by_mode"] = countsByMode
		}
	}

	logFields := logging.Fields{
		"event":           "admin.config_reload",
		"domain_count":    totalDomains,
		"config_degraded": state.Degraded,
	}
	if state.Reason != "" {
		logFields["degraded_reason"] = state.Reason
	}
	r.logger.Info("configuration reloaded", logFields)
	r.writeJSON(w, response, http.StatusOK)
}

func (r *Router) handleTunnels(w http.ResponseWriter, req *http.Request) {
	resp := r.tunnel.BuildTunnelsResponse()
	r.logger.Info("tunnels snapshot returned", logging.Fields{
		"event":        "admin.api_tunnels",
		"tunnel_count": len(resp.Tunnels),
	})
	r.writeJSON(w, resp, http.StatusOK)
}

func (r *Router) handleMetrics(w http.ResponseWriter, req *http.Request) {
	resp := r.tunnel.BuildMetricsResponse()
	r.logger.Info("metrics snapshot returned", logging.Fields{
		"event":              "admin.api_metrics",
		"active_connections": resp.Active,
	})
	r.writeJSON(w, resp, http.StatusOK)
}

func (r *Router) isAdminHost(req *http.Request) bool {
	host := canonicalHost(req.Host)
	if host == "" {
		return false
	}

	allowed := canonicalHost(r.currentAdminHost())
	if allowed == "" {
		return false
	}

	return strings.EqualFold(host, allowed)
}

// currentAdminHost returns the configured admin host, or "admin.<base_domain>"
// when none is set. An empty result disables the admin API.
func (r *Router) currentAdminHost() string {
	if r.manager == nil {
		return ""
	}
	cfg := r.manager.Current()
	if cfg == nil {
		return ""
	}
	if host := strings.TrimSpace(cfg.Admin.Host); host != "" {
		return host
	}
	if cfg.BaseDomain != "" {
		return config.DefaultAdminSubdomain + "." + cfg.BaseDomain
	}
	return ""
}

func canonicalHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}

	if strings.HasPrefix(host, "[") {
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			return parsed
		}
		return strings.Trim(host, "[]")
	}

	if strings.Contains(host, ":") {
		if parsed, _, err := net.SplitHostPort(host); err == nil {
			return parsed
		}
		if idx := strings.Index(host, ":"); idx > 0 {
			return host[:idx]
		}
	}

	return host
}

// tokenIssue reports why the configured bearer token cannot protect the admin
// API ("" when it is fine) and logs a warning once per distinct problem. While a
// problem exists every admin request is rejected: the API stays disabled instead
// of being reachable with a value copied from the documentation.
func (r *Router) tokenIssue() string {
	cfg := r.manager.Current()
	if cfg == nil {
		return "no configuration loaded"
	}
	why := config.AdminTokenIssue(cfg.Admin.BearerToken)

	r.warnMu.Lock()
	defer r.warnMu.Unlock()
	if why != "" && r.warnedWhy != why {
		r.logger.Warn("admin API disabled: "+why+" (set a random token of at least 24 characters)", logging.Fields{
			"event": "admin.api_disabled",
			"cause": why,
		})
	}
	r.warnedWhy = why
	return why
}

func (r *Router) authorize(req *http.Request) bool {
	if r.tokenIssue() != "" {
		return false
	}
	token := strings.TrimSpace(r.manager.Current().Admin.BearerToken)

	header := req.Header.Get(authHeaderName)
	if !strings.HasPrefix(header, authScheme) {
		return false
	}

	value := strings.TrimSpace(strings.TrimPrefix(header, authScheme))
	return subtle.ConstantTimeCompare([]byte(value), []byte(token)) == 1
}

func (r *Router) writeJSON(w http.ResponseWriter, payload any, status int) {
	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(status)

	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		r.logger.Warn("failed to encode JSON response", logging.Fields{
			"event": "admin.json_encode_failed",
			"error": err.Error(),
		})
	}
}

func summarizeDomains(domains []config.DomainConfig) (int, int) {
	if len(domains) == 0 {
		return 0, 0
	}

	totalDomains := len(domains)
	uniqueKeys := make(map[string]struct{}, len(domains))

	for _, domain := range domains {
		key := strings.TrimSpace(domain.APIKey)
		if key == "" {
			continue
		}
		uniqueKeys[key] = struct{}{}
	}

	return len(uniqueKeys), totalDomains
}

func summarizeDomainsByMode(domains []config.DomainConfig) map[string]int {
	counts := map[string]int{
		config.DomainTypeHTTP: 0,
		config.DomainTypeTCP:  0,
	}

	for _, domain := range domains {
		mode := strings.ToLower(strings.TrimSpace(domain.Type))
		if mode == "" {
			mode = config.DomainTypeHTTP
		}
		counts[mode]++
	}
	return counts
}

func copyStringIntMap(src map[string]int) map[string]int {
	if len(src) == 0 {
		return map[string]int{}
	}
	dst := make(map[string]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func splitWarnings(reason string) []string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil
	}
	parts := strings.Split(reason, ";")
	results := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			results = append(results, trimmed)
		}
	}
	return results
}

func collectErrorSummaries(total map[string]int, byMode map[string]map[string]int) ([]string, map[string]map[string]int) {
	if len(total) == 0 {
		return nil, nil
	}

	errorCodes := make([]string, 0, len(total))
	for code, count := range total {
		if count > 0 {
			errorCodes = append(errorCodes, code)
		}
	}
	sort.Strings(errorCodes)

	modeCounts := make(map[string]map[string]int, len(byMode))
	for mode, counts := range byMode {
		if len(counts) == 0 {
			continue
		}
		modeCopy := make(map[string]int, len(counts))
		for code, count := range counts {
			if count > 0 {
				modeCopy[code] = count
			}
		}
		if len(modeCopy) > 0 {
			modeCounts[mode] = modeCopy
		}
	}

	return errorCodes, modeCounts
}
