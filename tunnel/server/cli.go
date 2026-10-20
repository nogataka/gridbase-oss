package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/admin"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/buildinfo"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/directus"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/tunnelserver"
)

const (
	defaultConfigPath       = "config/server.json"
	statePollInterval       = 5 * time.Second
	tcpShutdownGraceTimeout = 10 * time.Second
	// healthPath is an unauthenticated liveness endpoint served on every host.
	healthPath = "/_gridbase/health"
)

// Command manages CLI parsing and server lifecycle.
type Command struct {
	stdout io.Writer
	stderr io.Writer
}

// NewCommand constructs a server command.
func NewCommand() *Command {
	return &Command{
		stdout: os.Stdout,
		stderr: os.Stderr,
	}
}

type options struct {
	configPath       string
	baseDomain       string
	directusURL      string
	directusToken    string
	adminBearerToken string
	healthcheck      bool
}

// Run executes the command using the provided context and CLI arguments.
func (c *Command) Run(ctx context.Context, args []string) error {
	opts, err := c.parseArgs(args)
	if err != nil {
		return err
	}
	if opts.healthcheck {
		return c.probeHealth(ctx, opts)
	}
	return c.run(ctx, opts)
}

// probeHealth performs a local GET against the liveness endpoint. The port comes
// from the configuration file (including GRIDBASE_HTTP_PORT) and falls back to 9080.
func (c *Command) probeHealth(ctx context.Context, opts *options) error {
	port := 9080
	if absConfig, err := filepath.Abs(opts.configPath); err == nil {
		if cfg, err := config.LoadFile(absConfig); err == nil && cfg.HTTP.Port > 0 {
			port = cfg.HTTP.Port
		}
	}

	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d%s", port, healthPath), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func (c *Command) parseArgs(args []string) (*options, error) {
	fs := flag.NewFlagSet("gridbase-server", flag.ContinueOnError)
	fs.SetOutput(c.stderr)

	configPath := fs.String("config", defaultConfigPath, "path to server configuration file (JSON)")
	baseDomain := fs.String("base-domain", "", "override base domain (e.g. gridbase.localhost)")
	directusURL := fs.String("directus-url", "", "override Directus base URL")
	directusToken := fs.String("directus-token", "", "override Directus static token (prefer GRIDBASE_DIRECTUS_TOKEN)")
	adminToken := fs.String("admin-bearer-token", "", "override admin API bearer token")
	healthcheck := fs.Bool("healthcheck", false, "probe the local HTTP listener (/_gridbase/health) and exit 0 when healthy; for container health checks")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	opts := &options{
		configPath:       strings.TrimSpace(*configPath),
		baseDomain:       strings.TrimSpace(*baseDomain),
		directusURL:      strings.TrimSpace(*directusURL),
		directusToken:    strings.TrimSpace(*directusToken),
		adminBearerToken: strings.TrimSpace(*adminToken),
		healthcheck:      *healthcheck,
	}
	if opts.configPath == "" {
		opts.configPath = defaultConfigPath
	}

	return opts, nil
}

func (c *Command) run(ctx context.Context, opts *options) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	serverLogger := logging.New("server", logging.WithWriter(c.stdout))
	info := buildinfo.Current()
	serverLogger.Info("starting gridbase tunnel server", logging.Fields{
		"event":       "server.start",
		"config_path": opts.configPath,
	})
	serverLogger.Info("build metadata", logging.Fields{
		"event":      "server.build_info",
		"version":    info.Version,
		"commit_sha": info.CommitSHA,
		"build_time": info.BuildTime,
	})

	absConfig, err := filepath.Abs(opts.configPath)
	if err != nil {
		return fmt.Errorf("resolve config path: %w", err)
	}

	cfg, err := config.LoadFile(absConfig)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	applyCLIOverrides(cfg, opts)

	manager := config.NewManager(cfg, config.WithFileSource(absConfig))
	tunnelLogger := logging.New("tunnel-server", logging.WithWriter(c.stdout))

	tunnel, err := tunnelserver.NewTunnelServer(manager.Current(), tunnelLogger)
	if err != nil {
		return fmt.Errorf("init tunnel server: %w", err)
	}

	unsubscribe := manager.Subscribe(func(updated *config.Config) {
		tunnel.Reload(updated)
		tunnelLogger.Info("configuration applied", logging.Fields{
			"event":        "tunnel.config_applied",
			"domain_count": len(updated.Domains),
		})
	})
	defer unsubscribe()

	adminLogger := logging.New("admin", logging.WithWriter(c.stdout))
	adminHandler := admin.NewRouter(tunnel, manager, adminLogger)
	proxyHandler := tunnelserver.NewProxyHandler(tunnel)

	// Admin endpoints are served only on the admin host; every other request
	// (including /api/* on a tunnel host) goes to the tunnel proxy.
	mux := http.NewServeMux()
	mux.HandleFunc(healthPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.Handle("/", admin.WithFallback(adminHandler, proxyHandler))

	httpLogger := logging.New("http", logging.WithWriter(c.stdout))
	httpServer := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTP.Port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if cfg.HTTP.ReadTimeout > 0 {
		httpServer.ReadTimeout = time.Duration(cfg.HTTP.ReadTimeout) * time.Second
	}
	if cfg.HTTP.WriteTimeout > 0 {
		httpServer.WriteTimeout = time.Duration(cfg.HTTP.WriteTimeout) * time.Second
	}
	if cfg.HTTP.IdleTimeout > 0 {
		httpServer.IdleTimeout = time.Duration(cfg.HTTP.IdleTimeout) * time.Second
	}

	errCh := make(chan error, 4)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := tunnel.AcceptLoop(runCtx); err != nil {
			errCh <- fmt.Errorf("accept loop: %w", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		httpLogger.Info("HTTP proxy listening", logging.Fields{
			"event": "http.listen",
			"addr":  httpServer.Addr,
		})
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http server: %w", err)
		}
	}()

	tracker := newDegradeTracker(manager)
	directusLogger := logging.New("directus", logging.WithWriter(c.stdout))

	if err := c.startDirectusLoops(runCtx, cfg.Directus, manager, tracker, directusLogger, &wg); err != nil {
		return err
	}

	var runErr error
	select {
	case <-ctx.Done():
		serverLogger.Info("shutdown requested", logging.Fields{
			"event":  "server.shutdown_request",
			"reason": errorString(ctx.Err()),
		})
	case err := <-errCh:
		runErr = err
		serverLogger.Error("fatal error received", err, logging.Fields{
			"event": "server.fatal_error",
		})
	}

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), tcpShutdownGraceTimeout)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
		httpLogger.Warn("HTTP shutdown warning", logging.Fields{
			"event": "http.shutdown_warning",
			"error": err.Error(),
		})
	}

	if err := tunnel.Close(); err != nil {
		tunnelLogger.Warn("tunnel server close warning", logging.Fields{
			"event": "tunnel.close_warning",
			"error": err.Error(),
		})
	}

	tracker.clear("directus-watch")
	tracker.clear("directus-snapshot")

	wg.Wait()
	serverLogger.Info("server stopped", logging.Fields{
		"event": "server.stopped",
	})
	return runErr
}

func applyCLIOverrides(cfg *config.Config, opts *options) {
	if opts.adminBearerToken != "" {
		cfg.Admin.BearerToken = opts.adminBearerToken
	}
	if opts.baseDomain != "" {
		cfg.BaseDomain = config.NormalizeBaseDomain(opts.baseDomain)
		// An explicit admin.host in the file/env stays; the derived default follows base_domain.
	}
	if opts.directusURL != "" {
		cfg.Directus.URL = opts.directusURL
	}
	if opts.directusToken != "" {
		cfg.Directus.Token = opts.directusToken
	}
}

func (c *Command) startDirectusLoops(
	ctx context.Context,
	cfg config.DirectusConfig,
	manager *config.Manager,
	tracker *degradeTracker,
	logger *logging.Logger,
	wg *sync.WaitGroup,
) error {
	if strings.TrimSpace(cfg.URL) == "" || strings.TrimSpace(cfg.Token) == "" {
		logger.Info("Directus client disabled: missing url or token; serving only domains from the config file", logging.Fields{
			"event": "directus.disabled",
		})
		return nil
	}

	client, err := directus.NewClient(cfg, directus.WithLogger(logger))
	if err != nil {
		return fmt.Errorf("init directus client: %w", err)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		client.Watch(ctx, func(records []directus.TunnelRecord) {
			warnings, err := applyDirectusSnapshot(manager, records)
			if err != nil {
				tracker.set("directus-watch", fmt.Sprintf("directus snapshot: %v", err))
				logger.Error("Directus snapshot application failed", err, logging.Fields{
					"event": "directus.snapshot_failed",
				})
				return
			}
			if len(warnings) > 0 {
				reason := strings.Join(warnings, "; ")
				tracker.set("directus-snapshot", reason)
				logger.Warn("Directus snapshot applied with warnings", logging.Fields{
					"event":    "directus.snapshot_warning",
					"warnings": warnings,
				})
			} else {
				tracker.clear("directus-snapshot")
			}
			tracker.clear("directus-watch")
			logger.Info("Directus snapshot applied", logging.Fields{
				"event":        "directus.snapshot_applied",
				"record_count": len(records),
			})
		})
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		monitorDirectusState(ctx, client, tracker, logger)
	}()

	return nil
}

// applyDirectusSnapshot converts Directus records to domains and installs them
// on top of the file-declared domains. The manager remembers the Directus set so
// that a later file reload (POST /api/config) re-merges it instead of dropping it.
func applyDirectusSnapshot(manager *config.Manager, records []directus.TunnelRecord) ([]string, error) {
	domains, warnings := convertRecordsToDomains(records)
	_, err := manager.ApplyExternalDomains(domains)
	return warnings, err
}

// convertRecordsToDomains turns Directus records into domain configs. One bad
// record must not stop the others from being applied (it would freeze every
// tenant on the previous snapshot, including users that were just disabled), so a
// record that is incomplete, duplicated, unsupported or carries unreadable
// settings is skipped and reported as a warning. API keys never appear in the
// warnings; records are identified by index, subdomain and key fingerprint.
func convertRecordsToDomains(records []directus.TunnelRecord) ([]config.DomainConfig, []string) {
	domains := make([]config.DomainConfig, 0, len(records))
	seen := make(map[string]struct{})
	subdomainOwner := make(map[string]string)
	var warnings []string

	for i, record := range records {
		apiKey := strings.TrimSpace(record.APIKey)
		subdomain := strings.TrimSpace(record.Subdomain)
		mode := strings.TrimSpace(record.Mode)
		if mode == "" {
			mode = config.DomainTypeHTTP
		}

		if apiKey == "" || subdomain == "" {
			warnings = append(warnings, fmt.Sprintf("record %d skipped: missing api key or subdomain (subdomain=%q)", i, subdomain))
			continue
		}
		keyID := tunnelserver.APIKeyFingerprint(apiKey)

		localURL := strings.TrimSpace(record.LocalURL)
		localHost := strings.TrimSpace(record.LocalHost)
		localPort := record.LocalPort

		switch mode {
		case config.DomainTypeHTTP:
			if localURL == "" {
				warnings = append(warnings, fmt.Sprintf("record %d skipped: http requires local_url (subdomain=%s)", i, subdomain))
				continue
			}
			localHost = ""
			localPort = 0
		case config.DomainTypeTCP:
			if localHost == "" || localPort <= 0 || localPort > 65535 {
				warnings = append(warnings, fmt.Sprintf("record %d skipped: tcp requires local_host and valid local_port (subdomain=%s)", i, subdomain))
				continue
			}
			localURL = ""
		default:
			warnings = append(warnings, fmt.Sprintf("record %d skipped: unsupported mode %q (subdomain=%s)", i, mode, subdomain))
			continue
		}

		key := apiKey + "::" + subdomain
		if _, dup := seen[key]; dup {
			warnings = append(warnings, fmt.Sprintf("record %d skipped: duplicate record for subdomain %q (key_id=%s)", i, subdomain, keyID))
			continue
		}
		// A subdomain routes to exactly one tunnel; first record (in sorted order) wins.
		if _, taken := subdomainOwner[subdomain]; taken {
			warnings = append(warnings, fmt.Sprintf("record %d skipped: subdomain %q is already assigned to another user", i, subdomain))
			continue
		}

		policy, err := policyFromSettings(record.SettingsRaw)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("record %d skipped: settings are not a valid JSON object (subdomain=%s, key_id=%s): %v", i, subdomain, keyID, err))
			continue
		}

		seen[key] = struct{}{}
		subdomainOwner[subdomain] = apiKey

		domains = append(domains, config.DomainConfig{
			APIKey:    apiKey,
			Subdomain: subdomain,
			Type:      mode,
			LocalURL:  localURL,
			LocalHost: localHost,
			LocalPort: localPort,
			Policy:    policy,
		})
	}

	sort.Slice(domains, func(i, j int) bool {
		if domains[i].APIKey != domains[j].APIKey {
			return domains[i].APIKey < domains[j].APIKey
		}
		return domains[i].Subdomain < domains[j].Subdomain
	})

	return domains, warnings
}

func policyFromSettings(raw json.RawMessage) (config.DomainPolicy, error) {
	var policy config.DomainPolicy

	if len(raw) == 0 {
		return policy, nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return policy, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return policy, err
	}

	if allowed, ok := payload["allowed_protocols"]; ok {
		policy.AllowedProtocols = toStringSlice(allowed)
	}

	enableIPFiltering := false
	ipFilterFlagPresent := false
	if value, ok := payload["enable_ip_filtering"]; ok {
		ipFilterFlagPresent = true
		enableIPFiltering = boolValue(value)
	}
	if enableIPFiltering || !ipFilterFlagPresent {
		if allow := toStringSlice(payload["ip_whitelist"]); len(allow) > 0 {
			policy.IPAllowList = allow
		}
	}

	if headers, ok := payload["default_headers"]; ok {
		policy.DefaultHeaders = toStringMap(headers)
	}

	if v, ok := payload["max_request_bytes"]; ok {
		if n, ok := toInt64(v); ok {
			policy.MaxRequestBytes = n
		}
	}
	if v, ok := payload["max_response_bytes"]; ok {
		if n, ok := toInt64(v); ok {
			policy.MaxResponseBytes = n
		}
	}

	return policy, nil
}

func toStringSlice(value any) []string {
	switch v := value.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, elem := range v {
			if s, ok := elem.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out = append(out, s)
				}
			}
		}
		return out
	case []string:
		out := make([]string, 0, len(v))
		for _, elem := range v {
			elem = strings.TrimSpace(elem)
			if elem != "" {
				out = append(out, elem)
			}
		}
		return out
	default:
		return nil
	}
}

func toStringMap(value any) map[string]string {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]string, len(v))
		for key, elem := range v {
			if s, ok := elem.(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					out[key] = s
				}
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(v))
		for key, elem := range v {
			elem = strings.TrimSpace(elem)
			if elem != "" {
				out[key] = elem
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	default:
		return nil
	}
}

func toInt64(value any) (int64, bool) {
	switch v := value.(type) {
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n, true
		}
	case float64:
		return int64(v), true
	case float32:
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return 0, false
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err == nil {
			return n, true
		}
	}
	return 0, false
}

func boolValue(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n != 0
		}
	case float64, float32:
		return toFloat64(v) != 0
	case int, int64:
		return toFloat64(v) != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "y", "on":
			return true
		}
	}
	return false
}

func toFloat64(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		if f, err := v.Float64(); err == nil {
			return f
		}
	}
	return 0
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func monitorDirectusState(ctx context.Context, client *directus.Client, tracker *degradeTracker, logger *logging.Logger) {
	ticker := time.NewTicker(statePollInterval)
	defer ticker.Stop()

	var last directus.State
	for {
		select {
		case <-ctx.Done():
			logger.Info("Directus state monitor stopped", logging.Fields{
				"event":  "directus.state_monitor_stop",
				"reason": errorString(ctx.Err()),
			})
			return
		case <-ticker.C:
		}

		state := client.State()
		if state.Degraded {
			reason := state.Reason
			if reason == "" {
				reason = "directus degraded"
			}
			tracker.set("directus-watch", reason)
			if !last.Degraded || last.Reason != state.Reason {
				logger.Warn("Directus degraded", logging.Fields{
					"event":                "directus.degraded",
					"reason":               reason,
					"consecutive_failures": state.ConsecutiveFailures,
				})
			}
		} else {
			if last.Degraded {
				logger.Info("Directus recovered", logging.Fields{
					"event":                "directus.recovered",
					"consecutive_failures": last.ConsecutiveFailures,
				})
			}
			tracker.clear("directus-watch")
		}
		last = state
	}
}

type degradeTracker struct {
	mu      sync.Mutex
	manager *config.Manager
	reasons map[string]string
}

func newDegradeTracker(manager *config.Manager) *degradeTracker {
	return &degradeTracker{
		manager: manager,
		reasons: make(map[string]string),
	}
}

func (t *degradeTracker) set(key, reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if reason == "" {
		reason = key
	}
	if existing, ok := t.reasons[key]; ok && existing == reason {
		return
	}

	t.reasons[key] = reason
	t.manager.MarkDegraded(errors.New(t.combine()))
}

func (t *degradeTracker) clear(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, ok := t.reasons[key]; !ok {
		return
	}
	delete(t.reasons, key)

	if len(t.reasons) == 0 {
		t.manager.ClearDegraded()
		return
	}

	t.manager.MarkDegraded(errors.New(t.combine()))
}

func (t *degradeTracker) combine() string {
	if len(t.reasons) == 0 {
		return ""
	}
	keys := make([]string, 0, len(t.reasons))
	for key := range t.reasons {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, t.reasons[key])
	}
	return strings.Join(values, "; ")
}
