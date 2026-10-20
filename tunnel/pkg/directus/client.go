package directus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
)

const (
	defaultSubdomainsPath = "/items/subdomains?fields=id,userId,subdomain,mode,local_server_url,local_host,local_port,status,description&filter[status][_eq]=active&limit=-1"
	defaultUsersPath      = "/items/users?fields=userId,api_key,settings,status&filter[status][_eq]=active&limit=-1"
)

// httpClient abstracts http.Client for testing.
type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// State exposes Directus client runtime health.
type State struct {
	Degraded            bool
	Reason              string
	LastSync            time.Time
	LastError           time.Time
	ConsecutiveFailures int
}

// TunnelRecord represents a hydrated tunnel domain entry joined with user metadata.
type TunnelRecord struct {
	APIKey      string          `json:"api_key"`
	UserID      string          `json:"user_id"`
	Subdomain   string          `json:"subdomain"`
	Mode        string          `json:"mode"`
	LocalURL    string          `json:"local_url"`
	LocalHost   string          `json:"local_host"`
	LocalPort   int             `json:"local_port"`
	Description string          `json:"description,omitempty"`
	SettingsRaw json.RawMessage `json:"settings"`
}

type userRecord struct {
	UserID   string          `json:"userId"`
	APIKey   string          `json:"api_key"`
	Status   string          `json:"status"`
	Settings json.RawMessage `json:"settings"`
}

type subdomainRecord struct {
	ID             string `json:"id"`
	UserID         string `json:"userId"`
	Subdomain      string `json:"subdomain"`
	Mode           string `json:"mode"`
	LocalServerURL string `json:"local_server_url"`
	LocalHost      string `json:"local_host"`
	LocalPort      *int   `json:"local_port"`
	Status         string `json:"status"`
	Description    string `json:"description"`
}

// Option configures the client.
type Option func(*Client)

// Client reads users and subdomains from Directus with retry/backoff.
type Client struct {
	baseURL    string
	token      string
	httpClient httpClient
	logger     *logging.Logger

	pollInterval time.Duration
	backoff      backoffConfig
	maxRetries   int

	tunnelsPath string
	usersPath   string

	mu    sync.RWMutex
	state State
}

type backoffConfig struct {
	initial    time.Duration
	max        time.Duration
	multiplier float64
}

// NewClient constructs a Directus client from configuration.
func NewClient(cfg config.DirectusConfig, opts ...Option) (*Client, error) {
	cfg.URL = strings.TrimSpace(cfg.URL)
	token := strings.TrimSpace(cfg.Token)

	if cfg.URL == "" {
		return nil, errors.New("directus: url is required")
	}
	if token == "" {
		return nil, errors.New("directus: token is required")
	}

	client := &Client{
		baseURL:      cfg.URL,
		token:        token,
		httpClient:   &http.Client{Timeout: 15 * time.Second},
		logger:       logging.New("directus"),
		pollInterval: 30 * time.Second,
		backoff: backoffConfig{
			initial:    2 * time.Second,
			max:        1 * time.Minute,
			multiplier: 2,
		},
		maxRetries:  5,
		tunnelsPath: defaultSubdomainsPath,
		usersPath:   defaultUsersPath,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(client)
		}
	}

	return client, nil
}

// WithHTTPClient overrides the HTTP client used for outbound requests.
func WithHTTPClient(h httpClient) Option {
	return func(c *Client) {
		if h != nil {
			c.httpClient = h
		}
	}
}

// WithLogger supplies a custom logger.
func WithLogger(l *logging.Logger) Option {
	return func(c *Client) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithPollInterval changes the watch polling cadence.
func WithPollInterval(interval time.Duration) Option {
	return func(c *Client) {
		if interval > 0 {
			c.pollInterval = interval
		}
	}
}

// WithBackoff adjusts retry behaviour.
func WithBackoff(initial, max time.Duration, multiplier float64) Option {
	return func(c *Client) {
		if initial > 0 {
			c.backoff.initial = initial
		}
		if max >= initial {
			c.backoff.max = max
		}
		if multiplier > 1 {
			c.backoff.multiplier = multiplier
		}
	}
}

// WithMaxRetries sets the number of consecutive failures before degraded mode.
func WithMaxRetries(limit int) Option {
	return func(c *Client) {
		if limit > 0 {
			c.maxRetries = limit
		}
	}
}

// WithTunnelsPath overrides the tunnels REST resource path.
func WithTunnelsPath(path string) Option {
	return func(c *Client) {
		if path != "" {
			c.tunnelsPath = path
		}
	}
}

// WithUsersPath overrides the users REST resource path.
func WithUsersPath(path string) Option {
	return func(c *Client) {
		if path != "" {
			c.usersPath = path
		}
	}
}

// FetchActiveTunnels retrieves current tunnel definitions from Directus.
func (c *Client) FetchActiveTunnels(ctx context.Context) ([]TunnelRecord, error) {
	users, err := c.fetchUsers(ctx)
	if err != nil {
		return nil, err
	}
	subdomains, err := c.fetchSubdomains(ctx)
	if err != nil {
		return nil, err
	}

	activeUsers := make(map[string]userRecord, len(users))
	for _, user := range users {
		userID := strings.TrimSpace(user.UserID)
		apiKey := strings.TrimSpace(user.APIKey)
		if userID == "" || apiKey == "" {
			c.logger.Warn("Directus user skipped due to missing identifiers", logging.Fields{
				"event":  "directus.user_skipped",
				"userId": user.UserID,
			})
			continue
		}
		status := strings.ToLower(strings.TrimSpace(user.Status))
		if status != "" && status != "active" {
			continue
		}
		activeUsers[userID] = user
	}

	records := make([]TunnelRecord, 0, len(subdomains))
	for _, domain := range subdomains {
		userID := strings.TrimSpace(domain.UserID)
		user, ok := activeUsers[userID]
		if !ok {
			c.logger.Warn("Directus subdomain skipped because user not active or missing", logging.Fields{
				"event":     "directus.subdomain_skipped_no_user",
				"subdomain": domain.Subdomain,
				"userId":    domain.UserID,
			})
			continue
		}

		apiKey := strings.TrimSpace(user.APIKey)
		subdomain := strings.TrimSpace(domain.Subdomain)
		mode := strings.ToLower(strings.TrimSpace(domain.Mode))
		if mode == "" {
			mode = config.DomainTypeHTTP
		}

		localURL := strings.TrimSpace(domain.LocalServerURL)
		localHost := strings.TrimSpace(domain.LocalHost)
		localPort := 0
		if domain.LocalPort != nil {
			localPort = *domain.LocalPort
		}

		if apiKey == "" || subdomain == "" {
			c.logger.Warn("Directus subdomain skipped due to missing identifiers", logging.Fields{
				"event":     "directus.subdomain_skipped_missing_fields",
				"userId":    userID,
				"subdomain": domain.Subdomain,
				"mode":      mode,
			})
			continue
		}

		records = append(records, TunnelRecord{
			APIKey:      apiKey,
			UserID:      userID,
			Subdomain:   subdomain,
			Mode:        mode,
			LocalURL:    localURL,
			LocalHost:   localHost,
			LocalPort:   localPort,
			Description: strings.TrimSpace(domain.Description),
			SettingsRaw: user.Settings,
		})
	}

	sort.Slice(records, func(i, j int) bool {
		if records[i].APIKey != records[j].APIKey {
			return records[i].APIKey < records[j].APIKey
		}
		if records[i].Subdomain != records[j].Subdomain {
			return records[i].Subdomain < records[j].Subdomain
		}
		if records[i].LocalURL != records[j].LocalURL {
			return records[i].LocalURL < records[j].LocalURL
		}
		if records[i].LocalHost != records[j].LocalHost {
			return records[i].LocalHost < records[j].LocalHost
		}
		return records[i].LocalPort < records[j].LocalPort
	})

	return records, nil
}

func (c *Client) fetchUsers(ctx context.Context) ([]userRecord, error) {
	var users []userRecord
	if err := c.fetch(ctx, c.usersPath, &users); err != nil {
		return nil, fmt.Errorf("directus fetch users: %w", err)
	}
	return users, nil
}

func (c *Client) fetchSubdomains(ctx context.Context) ([]subdomainRecord, error) {
	var subdomains []subdomainRecord
	if err := c.fetch(ctx, c.tunnelsPath, &subdomains); err != nil {
		return nil, fmt.Errorf("directus fetch subdomains: %w", err)
	}
	return subdomains, nil
}

func (c *Client) fetch(ctx context.Context, path string, dest any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.buildURL(path), nil)
	if err != nil {
		return err
	}
	c.addHeaders(req.Header)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("directus fetch request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("directus fetch unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	// Directus wraps list responses as {"data": [...]}.
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("directus fetch decode envelope: %w", err)
	}
	if err := json.Unmarshal(envelope.Data, dest); err != nil {
		return fmt.Errorf("directus fetch decode: %w", err)
	}

	return nil
}

// Watch polls Directus for tunnel changes and invokes onChange when snapshots differ.
func (c *Client) Watch(ctx context.Context, onChange func([]TunnelRecord)) {
	if onChange == nil {
		onChange = func([]TunnelRecord) {}
	}

	var (
		consecutiveFailures int
		delay               = c.backoff.initial
		lastHash            string
	)

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("Directus watch loop stopped", logging.Fields{
				"event":  "directus.watch_stopped",
				"reason": errorString(ctx.Err()),
			})
			return
		default:
		}

		records, err := c.FetchActiveTunnels(ctx)
		if err != nil {
			consecutiveFailures++
			if consecutiveFailures >= c.maxRetries {
				c.setFailure(err, true)
			} else {
				c.setFailure(err, false)
			}
			c.logger.Warn("Directus watch fetch failed", logging.Fields{
				"event":       "directus.watch_retry",
				"attempt":     consecutiveFailures,
				"max_attempt": c.maxRetries,
				"error":       errorString(err),
			})
			sleep := delay
			delay = c.nextDelay(delay)

			select {
			case <-time.After(sleep):
				continue
			case <-ctx.Done():
				c.logger.Warn("Directus watch loop stopped during backoff", logging.Fields{
					"event":  "directus.watch_stopped",
					"reason": errorString(ctx.Err()),
				})
				return
			}
		}

		consecutiveFailures = 0
		delay = c.backoff.initial
		c.setSuccess()

		hash := hashRecords(records)
		if hash != lastHash {
			lastHash = hash
			c.logger.Info("Directus watch detected new snapshot", logging.Fields{
				"event":        "directus.watch_snapshot",
				"record_count": len(records),
			})
			out := make([]TunnelRecord, len(records))
			copy(out, records)
			onChange(out)
		}

		select {
		case <-time.After(c.pollInterval):
		case <-ctx.Done():
			c.logger.Info("Directus watch loop stopped after poll", logging.Fields{
				"event":  "directus.watch_stopped",
				"reason": errorString(ctx.Err()),
			})
			return
		}
	}
}

// State returns the current runtime state.
func (c *Client) State() State {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *Client) setSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.Degraded = false
	c.state.Reason = ""
	c.state.ConsecutiveFailures = 0
	c.state.LastSync = time.Now()
}

func (c *Client) setFailure(err error, degraded bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.ConsecutiveFailures++
	c.state.LastError = time.Now()
	if err != nil {
		c.state.Reason = err.Error()
	}
	if degraded {
		c.state.Degraded = true
	}
}

func (c *Client) addHeaders(h http.Header) {
	h.Set("Authorization", "Bearer "+c.token)
	h.Set("X-Client-Info", "gridbase-go-tunnel")
}

func (c *Client) buildURL(path string) string {
	base := strings.TrimSuffix(c.baseURL, "/")
	suffix := strings.TrimPrefix(path, "/")
	if suffix == "" {
		return base
	}
	return base + "/" + suffix
}

func (c *Client) nextDelay(current time.Duration) time.Duration {
	next := time.Duration(float64(current) * c.backoff.multiplier)
	if next > c.backoff.max {
		return c.backoff.max
	}
	return next
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func hashRecords(records []TunnelRecord) string {
	if len(records) == 0 {
		return ""
	}
	copied := make([]TunnelRecord, len(records))
	copy(copied, records)
	sort.Slice(copied, func(i, j int) bool {
		if copied[i].APIKey != copied[j].APIKey {
			return copied[i].APIKey < copied[j].APIKey
		}
		if copied[i].Subdomain != copied[j].Subdomain {
			return copied[i].Subdomain < copied[j].Subdomain
		}
		if copied[i].LocalURL != copied[j].LocalURL {
			return copied[i].LocalURL < copied[j].LocalURL
		}
		if copied[i].LocalHost != copied[j].LocalHost {
			return copied[i].LocalHost < copied[j].LocalHost
		}
		if copied[i].LocalPort != copied[j].LocalPort {
			return copied[i].LocalPort < copied[j].LocalPort
		}
		return copied[i].Description < copied[j].Description
	})
	for i := range copied {
		copied[i].SettingsRaw = canonicalizeJSON(copied[i].SettingsRaw)
	}
	data, _ := json.Marshal(copied)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func canonicalizeJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return data
}
