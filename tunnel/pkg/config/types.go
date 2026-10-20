package config

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"strings"
	"time"
)

// NormalizeBaseDomain lower-cases the value and strips surrounding whitespace,
// a leading "*." / "." and any trailing dot.
func NormalizeBaseDomain(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "*.")
	value = strings.Trim(value, ".")
	return value
}

// DefaultAdminSubdomain is the label prepended to base_domain to form the default admin host.
const DefaultAdminSubdomain = "admin"

const (
	DomainTypeHTTP = "http"
	DomainTypeTCP  = "tcp"
)

// DomainPolicy captures request/response policy constraints for a single subdomain.
type DomainPolicy struct {
	AllowedProtocols []string          `json:"allowed_protocols,omitempty"`
	IPAllowList      []string          `json:"ip_allow_list,omitempty"`
	MaxRequestBytes  int64             `json:"max_request_bytes,omitempty"`
	MaxResponseBytes int64             `json:"max_response_bytes,omitempty"`
	DefaultHeaders   map[string]string `json:"default_headers,omitempty"`
}

// DomainConfig maps an API key and subdomain to a local upstream.
type DomainConfig struct {
	APIKey    string       `json:"api_key"`
	Subdomain string       `json:"subdomain"`
	Type      string       `json:"type,omitempty"`
	LocalURL  string       `json:"local_url,omitempty"`
	LocalHost string       `json:"local_host,omitempty"`
	LocalPort int          `json:"local_port,omitempty"`
	Policy    DomainPolicy `json:"policy,omitempty"`
}

// TLSConfig describes TLS certificate, key, and client CA paths.
//
// All three fields are optional. With cert and key unset the tunnel listener
// accepts plain TCP (intended for local/docker use or behind a TLS terminator).
// With cert and key set the listener serves TLS. Setting client_ca additionally
// requires and verifies client certificates (mTLS); leaving it empty disables
// client certificate checks and authentication relies on the API key alone.
type TLSConfig struct {
	CertPath string `json:"cert"`
	KeyPath  string `json:"key"`
	ClientCA string `json:"client_ca"`
}

// Defaults for the limits below. A zero value in the configuration selects the default.
const (
	// DefaultMaxFrameBytes caps one tunnel protocol frame (see protocol.ReadMessageLimit).
	DefaultMaxFrameBytes int64 = 16 << 20
	// DefaultHandshakeTimeoutSeconds is how long a new tunnel connection may take
	// to complete the TLS and hello exchange before it is dropped.
	DefaultHandshakeTimeoutSeconds = 10
	// DefaultMaxBodyBytes caps a visitor request body and a client response body.
	DefaultMaxBodyBytes int64 = 32 << 20
)

// TunnelConfig contains tunnel listener parameters.
//
// MaxFrameBytes bounds a single protocol frame read from a tunnel connection
// (default 16 MiB). HandshakeTimeout is the number of seconds a new connection
// has to finish the TLS handshake and send its hello (default 10).
type TunnelConfig struct {
	Port             int   `json:"port"`
	MaxFrameBytes    int64 `json:"max_frame_bytes,omitempty"`
	HandshakeTimeout int   `json:"handshake_timeout,omitempty"`
}

// FrameLimit returns the effective per-frame size limit.
func (t TunnelConfig) FrameLimit() int64 {
	if t.MaxFrameBytes > 0 {
		return t.MaxFrameBytes
	}
	return DefaultMaxFrameBytes
}

// HandshakeDeadline returns the effective handshake time budget.
func (t TunnelConfig) HandshakeDeadline() time.Duration {
	if t.HandshakeTimeout > 0 {
		return time.Duration(t.HandshakeTimeout) * time.Second
	}
	return DefaultHandshakeTimeoutSeconds * time.Second
}

// HTTPConfig describes the front-side HTTP proxy listener.
//
// MaxRequestBodyBytes / MaxResponseBodyBytes are server-wide ceilings (default
// 32 MiB each) for a visitor request body and for the response body a tunnel
// client sends back; a per-subdomain policy can only lower them.
//
// TrustedProxies lists the reverse proxies (IPs or CIDRs) whose X-Forwarded-For /
// X-Real-IP / X-Forwarded-Proto headers are believed when evaluating
// ip_allow_list and allowed_protocols. The default (empty) trusts nothing: the
// client address is the TCP peer address and the protocol comes from the
// connection (TLS or not).
type HTTPConfig struct {
	Port                 int      `json:"port"`
	ReadTimeout          int      `json:"read_timeout,omitempty"`
	WriteTimeout         int      `json:"write_timeout,omitempty"`
	IdleTimeout          int      `json:"idle_timeout,omitempty"`
	MaxRequestBodyBytes  int64    `json:"max_request_body_bytes,omitempty"`
	MaxResponseBodyBytes int64    `json:"max_response_body_bytes,omitempty"`
	TrustedProxies       []string `json:"trusted_proxies,omitempty"`
}

// RequestBodyLimit returns the effective ceiling for visitor request bodies.
func (h HTTPConfig) RequestBodyLimit() int64 {
	if h.MaxRequestBodyBytes > 0 {
		return h.MaxRequestBodyBytes
	}
	return DefaultMaxBodyBytes
}

// ResponseBodyLimit returns the effective ceiling for tunnel response bodies.
func (h HTTPConfig) ResponseBodyLimit() int64 {
	if h.MaxResponseBodyBytes > 0 {
		return h.MaxResponseBodyBytes
	}
	return DefaultMaxBodyBytes
}

// ParseTrustedProxies converts IP / CIDR strings into networks. A bare IP
// becomes a single-address network.
func ParseTrustedProxies(entries []string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			_, network, err := net.ParseCIDR(entry)
			if err != nil {
				return nil, fmt.Errorf("invalid CIDR %q", entry)
			}
			nets = append(nets, network)
			continue
		}
		ip := net.ParseIP(entry)
		if ip == nil {
			return nil, fmt.Errorf("invalid IP address %q", entry)
		}
		bits := 8 * net.IPv6len
		if v4 := ip.To4(); v4 != nil {
			ip, bits = v4, 8*net.IPv4len
		}
		nets = append(nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
	}
	return nets, nil
}

// Admin bearer-token strength requirements. A token that is empty, shorter than
// MinAdminTokenLength or still carries a placeholder prefix leaves the admin API
// disabled: it could otherwise be reached with a value copied from the docs.
const MinAdminTokenLength = 24

var weakAdminTokenPrefixes = []string{"change-me", "local-dev-", "replace-with-"}

// AdminTokenIssue explains why token cannot protect the admin API. It returns
// "" when the token is acceptable.
func AdminTokenIssue(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return "admin.bearer_token is not set"
	}
	lower := strings.ToLower(token)
	for _, prefix := range weakAdminTokenPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return fmt.Sprintf("admin.bearer_token still starts with the placeholder %q", prefix)
		}
	}
	if len(token) < MinAdminTokenLength {
		return fmt.Sprintf("admin.bearer_token is shorter than %d characters", MinAdminTokenLength)
	}
	return ""
}

// AdminConfig contains bearer token and listener configuration for management APIs.
// Host defaults to "admin.<base_domain>". CORSOrigin is the single origin allowed
// to call the admin API from a browser; when empty no CORS headers are emitted.
// Timezone is an IANA name used for timestamps in admin responses (default UTC).
type AdminConfig struct {
	BearerToken string `json:"bearer_token,omitempty"`
	Host        string `json:"host,omitempty"`
	CORSOrigin  string `json:"cors_origin,omitempty"`
	Timezone    string `json:"timezone,omitempty"`
}

// DirectusConfig holds connection details for the Directus integration that
// supplies users and subdomains. Use the GRIDBASE_DIRECTUS_TOKEN environment
// variable rather than writing the token to the configuration file.
type DirectusConfig struct {
	URL   string `json:"url,omitempty"`
	Token string `json:"token,omitempty"`
}

// Config represents the full server configuration.
type Config struct {
	// BaseDomain is the apex under which tunnel subdomains are served, e.g.
	// "gridbase.localhost" or "tunnel.example.com". A request for
	// "foo.<base_domain>" is routed to the subdomain "foo".
	BaseDomain string         `json:"base_domain"`
	TLS        TLSConfig      `json:"tls,omitempty"`
	Tunnel     TunnelConfig   `json:"tunnel"`
	HTTP       HTTPConfig     `json:"http"`
	Domains    []DomainConfig `json:"domains,omitempty"`
	Admin      AdminConfig    `json:"admin,omitempty"`
	Directus   DirectusConfig `json:"directus,omitempty"`
}

// ValidationError contains accumulated validation issues.
type ValidationError struct {
	Issues []string
}

// Error implements the error interface.
func (e *ValidationError) Error() string {
	if len(e.Issues) == 0 {
		return "invalid configuration"
	}
	return fmt.Sprintf("invalid configuration: %s", strings.Join(e.Issues, "; "))
}

// Is allows errors.Is comparisons.
func (e *ValidationError) Is(target error) bool {
	_, ok := target.(*ValidationError)
	return ok
}

// Validate checks configuration invariants and returns a ValidationError detailing issues.
func (c *Config) Validate() error {
	var errs []string

	if c.Tunnel.Port == 0 {
		c.Tunnel.Port = 9443
	}
	if c.HTTP.Port == 0 {
		c.HTTP.Port = 9080
	}

	c.BaseDomain = NormalizeBaseDomain(c.BaseDomain)
	if c.BaseDomain == "" {
		errs = append(errs, "base_domain is required (e.g. gridbase.localhost)")
	} else if strings.ContainsAny(c.BaseDomain, "/: \t") {
		errs = append(errs, "base_domain must be a bare host name without scheme, port or path")
	}

	if c.Tunnel.MaxFrameBytes < 0 {
		errs = append(errs, "tunnel.max_frame_bytes must not be negative")
	}
	if c.Tunnel.HandshakeTimeout < 0 {
		errs = append(errs, "tunnel.handshake_timeout must not be negative")
	}
	if c.HTTP.MaxRequestBodyBytes < 0 {
		errs = append(errs, "http.max_request_body_bytes must not be negative")
	}
	if c.HTTP.MaxResponseBodyBytes < 0 {
		errs = append(errs, "http.max_response_body_bytes must not be negative")
	}
	if _, err := ParseTrustedProxies(c.HTTP.TrustedProxies); err != nil {
		errs = append(errs, fmt.Sprintf("http.trusted_proxies: %v", err))
	}

	c.TLS.CertPath = strings.TrimSpace(c.TLS.CertPath)
	c.TLS.KeyPath = strings.TrimSpace(c.TLS.KeyPath)
	c.TLS.ClientCA = strings.TrimSpace(c.TLS.ClientCA)
	if (c.TLS.CertPath == "") != (c.TLS.KeyPath == "") {
		errs = append(errs, "tls.cert and tls.key must be set together (leave both empty for a plain TCP tunnel listener)")
	}
	if c.TLS.ClientCA != "" && c.TLS.CertPath == "" {
		errs = append(errs, "tls.client_ca requires tls.cert and tls.key")
	}

	c.Admin.Host = strings.ToLower(strings.TrimSpace(c.Admin.Host))
	if c.Admin.Host == "" && c.BaseDomain != "" {
		c.Admin.Host = DefaultAdminSubdomain + "." + c.BaseDomain
	}
	c.Admin.CORSOrigin = strings.TrimSpace(c.Admin.CORSOrigin)
	c.Admin.Timezone = strings.TrimSpace(c.Admin.Timezone)
	if c.Admin.Timezone != "" {
		if _, err := time.LoadLocation(c.Admin.Timezone); err != nil {
			errs = append(errs, fmt.Sprintf("admin.timezone %q is not a valid IANA time zone", c.Admin.Timezone))
		}
	}

	for i := range c.Domains {
		d := &c.Domains[i]
		d.Subdomain = strings.TrimSpace(d.Subdomain)
		d.APIKey = strings.TrimSpace(d.APIKey)
		d.LocalURL = strings.TrimSpace(d.LocalURL)
		d.LocalHost = strings.TrimSpace(d.LocalHost)
		d.Type = strings.ToLower(strings.TrimSpace(d.Type))

		if d.Subdomain == "" {
			errs = append(errs, fmt.Sprintf("domains[%d].subdomain must be set", i))
		}
		if d.APIKey == "" {
			errs = append(errs, fmt.Sprintf("domains[%d].api_key must be set", i))
		}

		if d.Type == "" {
			d.Type = DomainTypeHTTP
		}

		switch d.Type {
		case DomainTypeHTTP:
			if d.LocalURL == "" {
				errs = append(errs, fmt.Sprintf("domains[%d].local_url must be set for type http", i))
			}
			if d.LocalHost != "" {
				errs = append(errs, fmt.Sprintf("domains[%d].local_host must be empty for type http", i))
			}
			if d.LocalPort != 0 {
				errs = append(errs, fmt.Sprintf("domains[%d].local_port must be zero for type http", i))
			}
			d.LocalHost = ""
			d.LocalPort = 0
		case DomainTypeTCP:
			if d.LocalHost == "" {
				errs = append(errs, fmt.Sprintf("domains[%d].local_host must be set for type tcp", i))
			}
			if d.LocalPort <= 0 || d.LocalPort > 65535 {
				errs = append(errs, fmt.Sprintf("domains[%d].local_port must be between 1 and 65535 for type tcp", i))
			}
			if d.LocalURL != "" {
				errs = append(errs, fmt.Sprintf("domains[%d].local_url must be empty for type tcp", i))
			}
			d.LocalURL = ""
		default:
			errs = append(errs, fmt.Sprintf("domains[%d].type must be one of [%s,%s]", i, DomainTypeHTTP, DomainTypeTCP))
		}
	}

	if len(errs) != 0 {
		return &ValidationError{Issues: errs}
	}
	return nil
}

// ErrNotFound indicates configuration file was not found or accessible.
var ErrNotFound = errors.New("configuration not found")

// Clone performs a deep copy of the configuration.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}

	clone := *c
	clone.HTTP.TrustedProxies = append([]string(nil), c.HTTP.TrustedProxies...)
	clone.Domains = make([]DomainConfig, len(c.Domains))
	for i, d := range c.Domains {
		clone.Domains[i] = cloneDomainConfig(d)
	}
	return &clone
}

func cloneDomainConfig(src DomainConfig) DomainConfig {
	dst := src
	dst.Policy = cloneDomainPolicy(src.Policy)
	return dst
}

func cloneDomainPolicy(src DomainPolicy) DomainPolicy {
	dst := src
	if len(src.AllowedProtocols) > 0 {
		dst.AllowedProtocols = append([]string(nil), src.AllowedProtocols...)
	}
	if len(src.IPAllowList) > 0 {
		dst.IPAllowList = append([]string(nil), src.IPAllowList...)
	}
	if len(src.DefaultHeaders) > 0 {
		dst.DefaultHeaders = maps.Clone(src.DefaultHeaders)
	}
	return dst
}
