package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"gopkg.in/yaml.v3"
)

// EnvProvider allows injecting a custom environment lookup (primarily for tests).
type EnvProvider func(string) (string, bool)

// Duration is a helper wrapper that supports JSON/YAML unmarshalling from either
// a duration string (e.g. "5s") or a numeric value interpreted as seconds.
type Duration struct {
	time.Duration
}

// NewDuration constructs a Duration from a time.Duration value.
func NewDuration(d time.Duration) Duration {
	return Duration{Duration: d}
}

// UnmarshalJSON parses JSON duration values.
func (d *Duration) UnmarshalJSON(data []byte) error {
	data = bytesTrim(data)
	if len(data) == 0 || string(data) == "null" {
		return nil
	}

	// Try string first.
	if data[0] == '"' && data[len(data)-1] == '"' {
		value, err := strconv.Unquote(string(data))
		if err != nil {
			return fmt.Errorf("decode duration string: %w", err)
		}
		parsed, err := parseDurationString(value)
		if err != nil {
			return err
		}
		d.Duration = parsed
		return nil
	}

	// Fallback to numeric seconds.
	seconds, err := strconv.ParseFloat(string(data), 64)
	if err != nil {
		return fmt.Errorf("decode duration seconds: %w", err)
	}
	d.Duration = time.Duration(seconds * float64(time.Second))
	return nil
}

// UnmarshalYAML parses YAML duration values.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}

	switch value.Kind {
	case yaml.ScalarNode:
		switch value.Tag {
		case "!!str":
			parsed, err := parseDurationString(value.Value)
			if err != nil {
				return err
			}
			d.Duration = parsed
			return nil
		case "!!int", "!!float":
			seconds, err := strconv.ParseFloat(value.Value, 64)
			if err != nil {
				return fmt.Errorf("decode duration seconds: %w", err)
			}
			d.Duration = time.Duration(seconds * float64(time.Second))
			return nil
		}
	}

	return fmt.Errorf("unsupported YAML duration node: %s", value.Tag)
}

// MarshalJSON encodes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration.String())
}

// parseDurationString accepts Go duration strings or decimal seconds.
func parseDurationString(raw string) (time.Duration, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, errors.New("duration string is empty")
	}

	if strings.ContainsAny(value, "hmsun") {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return 0, fmt.Errorf("parse duration %q: %w", value, err)
		}
		return parsed, nil
	}

	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration seconds %q: %w", value, err)
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// ClientConfig captures runtime configuration for the tunnel client.
type ClientConfig struct {
	ServerAddr string `json:"server_addr" yaml:"server_addr"`
	ServerName string `json:"server_name" yaml:"server_name"`
	APIKey     string `json:"api_key" yaml:"api_key"`
	Mode       string `json:"mode" yaml:"mode"`
	TunnelID   string `json:"tunnel_id,omitempty" yaml:"tunnel_id,omitempty"`

	TLS TLSFiles `json:"tls" yaml:"tls"`

	HTTP struct {
		Timeout     Duration `json:"timeout" yaml:"timeout"`
		OverrideURL string   `json:"override_url,omitempty" yaml:"override_url,omitempty"`
		// MaxRequestBytes caps a request body received from the tunnel server and
		// MaxResponseBytes caps the response body read from the local application
		// (defaults 32 MiB each).
		MaxRequestBytes  int64 `json:"max_request_bytes,omitempty" yaml:"max_request_bytes,omitempty"`
		MaxResponseBytes int64 `json:"max_response_bytes,omitempty" yaml:"max_response_bytes,omitempty"`
	} `json:"http" yaml:"http"`
	TCP TCPConfig `json:"tcp" yaml:"tcp"`

	Reconnect ReconnectConfig `json:"reconnect" yaml:"reconnect"`
	Heartbeat HeartbeatConfig `json:"heartbeat" yaml:"heartbeat"`
}

// TCPConfig captures local endpoint details for TCP tunnels.
type TCPConfig struct {
	LocalHost string `json:"local_host" yaml:"local_host"`
	LocalPort int    `json:"local_port" yaml:"local_port"`
}

// TLSFiles controls how the client connects to the tunnel port.
//
// TLS is opt-in: with nothing set the client speaks plain TCP (local docker use).
// Setting Enabled, CAPath, CertPath/KeyPath or Insecure turns TLS on.
//   - CAPath trusts a private CA instead of the system roots.
//   - CertPath/KeyPath present a client certificate (only needed when the server
//     enforces mTLS via tls.client_ca).
//   - Insecure skips server certificate verification (development only).
type TLSFiles struct {
	Enabled  bool   `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Insecure bool   `json:"insecure,omitempty" yaml:"insecure,omitempty"`
	CertPath string `json:"cert,omitempty" yaml:"cert,omitempty"`
	KeyPath  string `json:"key,omitempty" yaml:"key,omitempty"`
	CAPath   string `json:"ca,omitempty" yaml:"ca,omitempty"`
}

// UseTLS reports whether the connection to the tunnel server is wrapped in TLS.
func (t TLSFiles) UseTLS() bool {
	return t.Enabled || t.Insecure || t.CAPath != "" || t.CertPath != "" || t.KeyPath != ""
}

// ReconnectConfig defines backoff behaviour for reconnect attempts.
type ReconnectConfig struct {
	Initial    Duration `json:"initial" yaml:"initial"`
	Max        Duration `json:"max" yaml:"max"`
	Multiplier float64  `json:"multiplier" yaml:"multiplier"`
	Jitter     float64  `json:"jitter_percent" yaml:"jitter_percent"`
}

// HeartbeatConfig governs inactivity detection for the tunnel stream.
type HeartbeatConfig struct {
	Interval Duration `json:"interval" yaml:"interval"`
	Timeout  Duration `json:"timeout" yaml:"timeout"`
}

// defaultClientConfig returns a configuration seeded with sensible defaults.
func defaultClientConfig() *ClientConfig {
	cfg := &ClientConfig{
		APIKey: "",
		Mode:   config.DomainTypeHTTP,
		TLS:    TLSFiles{},
		Reconnect: ReconnectConfig{
			Initial:    NewDuration(2 * time.Second),
			Max:        NewDuration(30 * time.Second),
			Multiplier: 2.0,
			Jitter:     0.2,
		},
		Heartbeat: HeartbeatConfig{
			Interval: NewDuration(15 * time.Second),
			Timeout:  NewDuration(45 * time.Second),
		},
	}
	cfg.HTTP.Timeout = NewDuration(30 * time.Second)
	cfg.HTTP.MaxRequestBytes = config.DefaultMaxBodyBytes
	cfg.HTTP.MaxResponseBytes = config.DefaultMaxBodyBytes
	return cfg
}

// CLIInputs captures flag-derived values and which flags were explicitly set.
type CLIInputs struct {
	ConfigPath      string
	ServerAddr      string
	ServerName      string
	APIKey          string
	Mode            string
	TunnelID        string
	HTTPOverrideURL string
	TCPLocalHost    string
	TCPLocalPort    int
	CertPath        string
	KeyPath         string
	CAPath          string
	TLS             bool
	Insecure        bool
	TimeoutSeconds  int
	FlagsSet        map[string]bool
}

// FlagIsSet reports whether a flag was explicitly provided by the caller.
func (i CLIInputs) FlagIsSet(name string) bool {
	if i.FlagsSet == nil {
		return false
	}
	return i.FlagsSet[name]
}

const (
	envConfigPath          = "GRIDBASE_CLIENT_CONFIG"
	envServerAddr          = "GRIDBASE_CLIENT_SERVER_ADDR"
	envServerName          = "GRIDBASE_CLIENT_SERVER_NAME"
	envAPIKey              = "GRIDBASE_CLIENT_API_KEY"
	envMode                = "GRIDBASE_CLIENT_MODE"
	envTunnelID            = "GRIDBASE_CLIENT_TUNNEL_ID"
	envHTTPOverrideURL     = "GRIDBASE_CLIENT_HTTP_OVERRIDE_URL"
	envTCPLocalHost        = "GRIDBASE_CLIENT_TCP_LOCAL_HOST"
	envTCPLocalPort        = "GRIDBASE_CLIENT_TCP_LOCAL_PORT"
	envTLS                 = "GRIDBASE_CLIENT_TLS"
	envTLSInsecure         = "GRIDBASE_CLIENT_TLS_INSECURE"
	envTLSCert             = "GRIDBASE_CLIENT_TLS_CERT"
	envTLSKey              = "GRIDBASE_CLIENT_TLS_KEY"
	envTLSCA               = "GRIDBASE_CLIENT_TLS_CA"
	envHTTPTimeout         = "GRIDBASE_CLIENT_HTTP_TIMEOUT"
	envHTTPMaxRequest      = "GRIDBASE_CLIENT_HTTP_MAX_REQUEST_BYTES"
	envHTTPMaxResponse     = "GRIDBASE_CLIENT_HTTP_MAX_RESPONSE_BYTES"
	envReconnectInitial    = "GRIDBASE_CLIENT_RECONNECT_INITIAL"
	envReconnectMax        = "GRIDBASE_CLIENT_RECONNECT_MAX"
	envReconnectMultiplier = "GRIDBASE_CLIENT_RECONNECT_MULTIPLIER"
	envReconnectJitter     = "GRIDBASE_CLIENT_RECONNECT_JITTER"
	envHeartbeatInterval   = "GRIDBASE_CLIENT_HEARTBEAT_INTERVAL"
	envHeartbeatTimeout    = "GRIDBASE_CLIENT_HEARTBEAT_TIMEOUT"
)

// loadClientConfig merges configuration from disk, environment variables, and CLI flags.
func loadClientConfig(inputs CLIInputs, lookup EnvProvider) (*ClientConfig, error) {
	cfg := defaultClientConfig()

	path := strings.TrimSpace(inputs.ConfigPath)
	if path == "" && lookup != nil {
		if candidate, ok := lookup(envConfigPath); ok {
			path = strings.TrimSpace(candidate)
		}
	}

	if path != "" {
		loaded, err := loadConfigFile(path)
		if err != nil {
			return nil, err
		}
		cfg = loaded
	}

	if err := applyEnv(cfg, lookup); err != nil {
		return nil, err
	}
	applyFlagOverrides(cfg, inputs)
	cfg.normalize()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func loadConfigFile(path string) (*ClientConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read client config %s: %w", path, err)
	}

	cfg := defaultClientConfig()
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("decode YAML config %s: %w", path, err)
		}
	default:
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("decode JSON config %s: %w", path, err)
		}
	}
	return cfg, nil
}

var envSetters = map[string]func(*ClientConfig, string) error{
	envServerAddr: func(cfg *ClientConfig, value string) error {
		cfg.ServerAddr = value
		return nil
	},
	envServerName: func(cfg *ClientConfig, value string) error {
		cfg.ServerName = value
		return nil
	},
	envAPIKey: func(cfg *ClientConfig, value string) error {
		cfg.APIKey = value
		return nil
	},
	envMode: func(cfg *ClientConfig, value string) error {
		cfg.Mode = value
		return nil
	},
	envTunnelID: func(cfg *ClientConfig, value string) error {
		cfg.TunnelID = value
		return nil
	},
	envHTTPOverrideURL: func(cfg *ClientConfig, value string) error {
		cfg.HTTP.OverrideURL = value
		return nil
	},
	envHTTPMaxRequest: func(cfg *ClientConfig, value string) error {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid max request bytes %q: %w", value, err)
		}
		cfg.HTTP.MaxRequestBytes = n
		return nil
	},
	envHTTPMaxResponse: func(cfg *ClientConfig, value string) error {
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid max response bytes %q: %w", value, err)
		}
		cfg.HTTP.MaxResponseBytes = n
		return nil
	},
	envTCPLocalHost: func(cfg *ClientConfig, value string) error {
		cfg.TCP.LocalHost = value
		return nil
	},
	envTCPLocalPort: func(cfg *ClientConfig, value string) error {
		port, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid tcp local port %q: %w", value, err)
		}
		cfg.TCP.LocalPort = port
		return nil
	},
	envTLS: func(cfg *ClientConfig, value string) error {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid boolean %q", value)
		}
		cfg.TLS.Enabled = enabled
		return nil
	},
	envTLSInsecure: func(cfg *ClientConfig, value string) error {
		insecure, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("invalid boolean %q", value)
		}
		cfg.TLS.Insecure = insecure
		return nil
	},
	envTLSCert: func(cfg *ClientConfig, value string) error {
		cfg.TLS.CertPath = value
		return nil
	},
	envTLSKey: func(cfg *ClientConfig, value string) error {
		cfg.TLS.KeyPath = value
		return nil
	},
	envTLSCA: func(cfg *ClientConfig, value string) error {
		cfg.TLS.CAPath = value
		return nil
	},
	envHTTPTimeout: func(cfg *ClientConfig, value string) error {
		parsed, err := parseDurationString(value)
		if err != nil {
			return err
		}
		cfg.HTTP.Timeout = NewDuration(parsed)
		return nil
	},
	envReconnectInitial: func(cfg *ClientConfig, value string) error {
		parsed, err := parseDurationString(value)
		if err != nil {
			return err
		}
		cfg.Reconnect.Initial = NewDuration(parsed)
		return nil
	},
	envReconnectMax: func(cfg *ClientConfig, value string) error {
		parsed, err := parseDurationString(value)
		if err != nil {
			return err
		}
		cfg.Reconnect.Max = NewDuration(parsed)
		return nil
	},
	envReconnectMultiplier: func(cfg *ClientConfig, value string) error {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid reconnect multiplier %q: %w", value, err)
		}
		cfg.Reconnect.Multiplier = parsed
		return nil
	},
	envReconnectJitter: func(cfg *ClientConfig, value string) error {
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid reconnect jitter %q: %w", value, err)
		}
		cfg.Reconnect.Jitter = parsed
		return nil
	},
	envHeartbeatInterval: func(cfg *ClientConfig, value string) error {
		parsed, err := parseDurationString(value)
		if err != nil {
			return err
		}
		cfg.Heartbeat.Interval = NewDuration(parsed)
		return nil
	},
	envHeartbeatTimeout: func(cfg *ClientConfig, value string) error {
		parsed, err := parseDurationString(value)
		if err != nil {
			return err
		}
		cfg.Heartbeat.Timeout = NewDuration(parsed)
		return nil
	},
}

func applyEnv(cfg *ClientConfig, lookup EnvProvider) error {
	if lookup == nil {
		return nil
	}

	var issues []string
	for key, setter := range envSetters {
		raw, ok := lookup(key)
		if !ok {
			continue
		}
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if err := setter(cfg, value); err != nil {
			issues = append(issues, fmt.Sprintf("%s: %v", key, err))
		}
	}

	if len(issues) > 0 {
		return &ConfigError{Issues: issues}
	}
	return nil
}

func applyFlagOverrides(cfg *ClientConfig, inputs CLIInputs) {
	if inputs.FlagIsSet("server") || inputs.FlagIsSet("server_addr") {
		cfg.ServerAddr = inputs.ServerAddr
	}
	if inputs.FlagIsSet("server_name") {
		cfg.ServerName = inputs.ServerName
	}
	if inputs.FlagIsSet("api_key") {
		cfg.APIKey = inputs.APIKey
	}
	if inputs.FlagIsSet("mode") {
		cfg.Mode = inputs.Mode
	}
	if inputs.FlagIsSet("tunnel_id") {
		cfg.TunnelID = inputs.TunnelID
	}
	if inputs.FlagIsSet("local_server_url") {
		cfg.HTTP.OverrideURL = inputs.HTTPOverrideURL
	}
	if inputs.FlagIsSet("tcp_host") {
		cfg.TCP.LocalHost = inputs.TCPLocalHost
	}
	if inputs.FlagIsSet("tcp_port") {
		cfg.TCP.LocalPort = inputs.TCPLocalPort
	}
	if inputs.FlagIsSet("cert") {
		cfg.TLS.CertPath = inputs.CertPath
	}
	if inputs.FlagIsSet("key") {
		cfg.TLS.KeyPath = inputs.KeyPath
	}
	if inputs.FlagIsSet("ca") {
		cfg.TLS.CAPath = inputs.CAPath
	}
	if inputs.FlagIsSet("tls") {
		cfg.TLS.Enabled = inputs.TLS
	}
	if inputs.FlagIsSet("insecure") {
		cfg.TLS.Insecure = inputs.Insecure
	}
	if inputs.FlagIsSet("timeout") {
		cfg.HTTP.Timeout = NewDuration(time.Duration(inputs.TimeoutSeconds) * time.Second)
	}
}

func (cfg *ClientConfig) normalize() {
	cfg.ServerAddr = strings.TrimSpace(cfg.ServerAddr)
	cfg.ServerName = strings.TrimSpace(cfg.ServerName)
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.Mode = strings.ToLower(strings.TrimSpace(cfg.Mode))
	if cfg.Mode == "" {
		cfg.Mode = config.DomainTypeHTTP
	}
	cfg.TunnelID = strings.TrimSpace(cfg.TunnelID)
	cfg.HTTP.OverrideURL = strings.TrimSpace(cfg.HTTP.OverrideURL)
	cfg.TLS.CertPath = strings.TrimSpace(cfg.TLS.CertPath)
	cfg.TLS.KeyPath = strings.TrimSpace(cfg.TLS.KeyPath)
	cfg.TLS.CAPath = strings.TrimSpace(cfg.TLS.CAPath)
	cfg.TCP.LocalHost = strings.TrimSpace(cfg.TCP.LocalHost)

	if cfg.ServerName == "" {
		if host, _, err := net.SplitHostPort(cfg.ServerAddr); err == nil && host != "" {
			cfg.ServerName = host
		}
	}
}

// ConfigError aggregates validation failures.
type ConfigError struct {
	Issues []string
}

// Error implements error.
func (e *ConfigError) Error() string {
	return "invalid client configuration: " + strings.Join(e.Issues, "; ")
}

// Validate ensures configuration invariants hold.
func (cfg *ClientConfig) Validate() error {
	var issues []string

	if cfg.ServerAddr == "" {
		issues = append(issues, "server address is required (--server host:port, server_addr in the config file or GRIDBASE_CLIENT_SERVER_ADDR)")
	} else if _, _, err := net.SplitHostPort(cfg.ServerAddr); err != nil {
		issues = append(issues, fmt.Sprintf("server_addr must be host:port (%v)", err))
	}

	if cfg.ServerName == "" {
		issues = append(issues, "server_name is required")
	}

	if strings.TrimSpace(cfg.APIKey) == "" {
		issues = append(issues, "api_key is required")
	}

	mode := cfg.Mode
	switch mode {
	case config.DomainTypeHTTP:
		if cfg.TunnelID != "" {
			issues = append(issues, "tunnel_id must be empty when mode=http")
		}
		if cfg.HTTP.OverrideURL != "" {
			if err := validateHTTPOverrideURL(cfg.HTTP.OverrideURL); err != nil {
				issues = append(issues, fmt.Sprintf("http.override_url: %v", err))
			}
		}
		if cfg.TCP.LocalHost != "" {
			issues = append(issues, "tcp.local_host must be empty when mode=http")
		}
		if cfg.TCP.LocalPort != 0 {
			issues = append(issues, "tcp.local_port must be zero when mode=http")
		}
	case config.DomainTypeTCP:
		if cfg.TunnelID == "" {
			issues = append(issues, "tunnel_id is required when mode=tcp")
		}
		if cfg.HTTP.OverrideURL != "" {
			issues = append(issues, "http.override_url cannot be set when mode=tcp")
		}
		if cfg.TCP.LocalHost == "" {
			issues = append(issues, "tcp.local_host is required when mode=tcp")
		}
		if cfg.TCP.LocalPort <= 0 || cfg.TCP.LocalPort > 65535 {
			issues = append(issues, "tcp.local_port must be between 1 and 65535 when mode=tcp")
		}
	default:
		issues = append(issues, fmt.Sprintf("mode must be one of [%s,%s]", config.DomainTypeHTTP, config.DomainTypeTCP))
	}

	if (cfg.TLS.CertPath == "") != (cfg.TLS.KeyPath == "") {
		issues = append(issues, "tls.cert and tls.key must be set together")
	}
	if cfg.TLS.Insecure && cfg.TLS.CAPath != "" {
		issues = append(issues, "tls.insecure and tls.ca are mutually exclusive")
	}
	if cfg.TLS.CertPath != "" {
		if err := ensureReadableFile(cfg.TLS.CertPath); err != nil {
			issues = append(issues, fmt.Sprintf("tls.cert: %v", err))
		}
	}
	if cfg.TLS.KeyPath != "" {
		if err := ensureReadableFile(cfg.TLS.KeyPath); err != nil {
			issues = append(issues, fmt.Sprintf("tls.key: %v", err))
		}
	}
	if cfg.TLS.CAPath != "" {
		if err := ensureReadableFile(cfg.TLS.CAPath); err != nil {
			issues = append(issues, fmt.Sprintf("tls.ca: %v", err))
		}
	}

	if cfg.HTTP.Timeout.Duration <= 0 {
		issues = append(issues, "http.timeout must be greater than 0s")
	}
	if cfg.HTTP.MaxRequestBytes <= 0 {
		issues = append(issues, "http.max_request_bytes must be greater than 0")
	}
	if cfg.HTTP.MaxResponseBytes <= 0 {
		issues = append(issues, "http.max_response_bytes must be greater than 0")
	}

	if cfg.Reconnect.Initial.Duration <= 0 {
		issues = append(issues, "reconnect.initial must be greater than 0s")
	}
	if cfg.Reconnect.Max.Duration < cfg.Reconnect.Initial.Duration {
		issues = append(issues, "reconnect.max must be >= reconnect.initial")
	}
	if cfg.Reconnect.Multiplier < 1.0 {
		issues = append(issues, "reconnect.multiplier must be >= 1.0")
	}
	if cfg.Reconnect.Jitter < 0 || cfg.Reconnect.Jitter >= 1 {
		issues = append(issues, "reconnect.jitter_percent must be within [0.0, 1.0)")
	}

	if cfg.Heartbeat.Interval.Duration <= 0 {
		issues = append(issues, "heartbeat.interval must be greater than 0s")
	}
	if cfg.Heartbeat.Timeout.Duration <= cfg.Heartbeat.Interval.Duration {
		issues = append(issues, "heartbeat.timeout must be greater than heartbeat.interval")
	}

	if len(issues) > 0 {
		return &ConfigError{Issues: issues}
	}
	return nil
}

func ensureReadableFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("path must not be empty")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("path points to a directory")
	}
	return nil
}

func validateHTTPOverrideURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https")
	}
	if parsed.Host == "" {
		return errors.New("host must be specified")
	}
	return nil
}

func bytesTrim(data []byte) []byte {
	start := 0
	for start < len(data) && (data[start] == ' ' || data[start] == '\n' || data[start] == '\t' || data[start] == '\r') {
		start++
	}
	end := len(data)
	for end > start && (data[end-1] == ' ' || data[end-1] == '\n' || data[end-1] == '\t' || data[end-1] == '\r') {
		end--
	}
	return data[start:end]
}
