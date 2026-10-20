package policy

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
)

// ViolationCode identifies the reason for a policy rejection.
type ViolationCode string

const (
	// ViolationProtocol indicates the request protocol is not permitted.
	ViolationProtocol ViolationCode = "protocol_not_allowed"
	// ViolationIP indicates the client IP is not in the allow list.
	ViolationIP ViolationCode = "ip_not_allowed"
	// ViolationRequestSize indicates the request body exceeded policy thresholds.
	ViolationRequestSize ViolationCode = "request_size_exceeded"
)

// ViolationError is returned when a policy rule is violated.
type ViolationError struct {
	Code       ViolationCode
	Message    string
	HTTPStatus int
	Details    map[string]string
}

func (e *ViolationError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("policy violation (%s): %s", e.Code, e.Message)
}

// Engine evaluates HTTP requests against domain policies.
//
// By default the client address is the TCP peer address (RemoteAddr) and the
// protocol is derived from the connection (TLS or not); X-Forwarded-For,
// X-Real-IP and X-Forwarded-Proto are ignored because any visitor can forge them.
// They are honoured only for requests that arrive from a trusted proxy (see
// SetTrustedProxies).
type Engine struct {
	mu      sync.RWMutex
	trusted []*net.IPNet
}

// NewEngine creates a policy engine instance that trusts no proxy.
func NewEngine() *Engine {
	return &Engine{}
}

// SetTrustedProxies replaces the set of proxies (IPs or CIDRs) whose forwarding
// headers are believed. Entries that do not parse are ignored; callers validate
// them beforehand (config.Config.Validate does).
func (e *Engine) SetTrustedProxies(entries []string) {
	nets, _ := config.ParseTrustedProxies(entries)
	e.mu.Lock()
	e.trusted = nets
	e.mu.Unlock()
}

func (e *Engine) trustedProxies() []*net.IPNet {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.trusted
}

// StripUntrustedForwarding removes forwarding headers (X-Forwarded-*, X-Real-IP,
// Forwarded) from header unless remoteAddr is a trusted proxy. Call it before
// relaying a request to the tunnel client so that a value forged by a visitor
// who reached the HTTP port directly is not presented to the backend as if a
// proxy had written it. Requests from trusted proxies are left untouched.
func (e *Engine) StripUntrustedForwarding(header http.Header, remoteAddr string) {
	if len(header) == 0 || ipInNets(peerAddress(remoteAddr), e.trustedProxies()) {
		return
	}
	for key := range header {
		canonical := http.CanonicalHeaderKey(key)
		if strings.HasPrefix(canonical, "X-Forwarded-") || canonical == "X-Real-Ip" || canonical == "Forwarded" {
			delete(header, key)
		}
	}
}

// RequestContext captures metadata required for policy evaluation.
type RequestContext struct {
	Method     string
	Header     http.Header
	BodySize   int64
	RemoteAddr string
	TLS        bool
}

// TCPContext captures metadata for TCP stream evaluation.
type TCPContext struct {
	RemoteAddr string
}

// EvaluateRequest validates the incoming request using the supplied domain policy and returns
// normalized default headers to append when forwarding on success.
func (e *Engine) EvaluateRequest(policy config.DomainPolicy, ctx RequestContext) (map[string]string, error) {
	if err := e.checkProtocol(policy, ctx); err != nil {
		return nil, err
	}
	if err := e.checkClientIP(policy, ctx); err != nil {
		return nil, err
	}
	if err := e.checkRequestSize(policy, ctx); err != nil {
		return nil, err
	}

	return copyHeaders(policy.DefaultHeaders), nil
}

// EvaluateTCP validates a TCP stream against the supplied domain policy.
// 現状は IP 制御のみを提供し、追加要件が発生した場合に備えて拡張用のフックとして定義する。
func (e *Engine) EvaluateTCP(policy config.DomainPolicy, ctx TCPContext) error {
	if err := e.checkClientIP(policy, RequestContext{RemoteAddr: ctx.RemoteAddr}); err != nil {
		return err
	}
	return nil
}

func (e *Engine) checkProtocol(policy config.DomainPolicy, ctx RequestContext) error {
	if len(policy.AllowedProtocols) == 0 {
		return nil
	}

	allowed := make(map[string]struct{}, len(policy.AllowedProtocols))
	for _, p := range policy.AllowedProtocols {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			allowed[p] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return nil
	}

	protocol := detectProtocol(ctx, e.trustedProxies())
	if _, ok := allowed[protocol]; ok {
		return nil
	}

	return &ViolationError{
		Code:       ViolationProtocol,
		Message:    fmt.Sprintf("protocol %q is not permitted", protocol),
		HTTPStatus: http.StatusForbidden,
		Details: map[string]string{
			"protocol": protocol,
		},
	}
}

func (e *Engine) checkClientIP(policy config.DomainPolicy, ctx RequestContext) error {
	if len(policy.IPAllowList) == 0 {
		return nil
	}

	clientIP := extractClientIP(ctx, e.trustedProxies())
	if clientIP == "" {
		return &ViolationError{
			Code:       ViolationIP,
			Message:    "client IP could not be determined",
			HTTPStatus: http.StatusForbidden,
			Details:    map[string]string{},
		}
	}

	ip := net.ParseIP(clientIP)
	if ip == nil {
		return &ViolationError{
			Code:       ViolationIP,
			Message:    fmt.Sprintf("client IP %q is invalid", clientIP),
			HTTPStatus: http.StatusForbidden,
			Details: map[string]string{
				"client_ip": clientIP,
			},
		}
	}

	for _, entry := range policy.IPAllowList {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		if strings.Contains(entry, "/") {
			if _, network, err := net.ParseCIDR(entry); err == nil {
				if network.Contains(ip) {
					return nil
				}
			}
			continue
		}

		if allowedIP := net.ParseIP(entry); allowedIP != nil && allowedIP.Equal(ip) {
			return nil
		}
	}

	return &ViolationError{
		Code:       ViolationIP,
		Message:    fmt.Sprintf("client IP %q is not permitted", clientIP),
		HTTPStatus: http.StatusForbidden,
		Details: map[string]string{
			"client_ip": clientIP,
		},
	}
}

func (e *Engine) checkRequestSize(policy config.DomainPolicy, ctx RequestContext) error {
	if policy.MaxRequestBytes <= 0 {
		return nil
	}

	if ctx.BodySize <= policy.MaxRequestBytes {
		return nil
	}

	return &ViolationError{
		Code:       ViolationRequestSize,
		Message:    fmt.Sprintf("request size %d exceeds max %d", ctx.BodySize, policy.MaxRequestBytes),
		HTTPStatus: http.StatusRequestEntityTooLarge,
		Details: map[string]string{
			"body_size":        fmt.Sprintf("%d", ctx.BodySize),
			"max_request_size": fmt.Sprintf("%d", policy.MaxRequestBytes),
		},
	}
}

// detectProtocol returns "https" or "http" for the visitor's connection.
//
// X-Forwarded-Proto is consulted only when the TCP peer is a trusted proxy;
// from any other peer the header is forgeable, so the protocol is taken from
// the connection itself (ctx.TLS). Like X-Forwarded-For it is read from the
// right, i.e. the value written by the proxy nearest to this server, so a value
// the visitor prepended to a header the proxy appends to is never used.
func detectProtocol(ctx RequestContext, trusted []*net.IPNet) string {
	if ctx.Header != nil && peerIsTrusted(ctx, trusted) {
		var last string
		for _, line := range ctx.Header.Values("X-Forwarded-Proto") {
			for _, part := range strings.Split(line, ",") {
				if candidate := strings.ToLower(strings.TrimSpace(part)); candidate != "" {
					last = candidate
				}
			}
		}
		if last != "" {
			return last
		}
	}
	if ctx.TLS {
		return "https"
	}
	return "http"
}

// peerIsTrusted reports whether the TCP peer of ctx is one of the trusted
// proxies. It is the single trust decision shared by every forwarding header.
func peerIsTrusted(ctx RequestContext, trusted []*net.IPNet) bool {
	return len(trusted) > 0 && ipInNets(peerAddress(ctx.RemoteAddr), trusted)
}

// extractClientIP returns the address the allow list is checked against.
//
// Forwarding headers are consulted only when the TCP peer is itself a trusted
// proxy. X-Forwarded-For is then read from the right: entries appended by
// trusted proxies are skipped and the first remaining entry is the client, so
// values a visitor prepended to the header are never used. With no trusted
// proxy configured (the default) the header is ignored entirely.
func extractClientIP(ctx RequestContext, trusted []*net.IPNet) string {
	peer := peerAddress(ctx.RemoteAddr)
	if ctx.Header == nil || !peerIsTrusted(ctx, trusted) {
		return peer
	}

	var chain []string
	for _, line := range ctx.Header.Values("X-Forwarded-For") {
		for _, part := range strings.Split(line, ",") {
			if candidate := strings.TrimSpace(part); candidate != "" {
				chain = append(chain, candidate)
			}
		}
	}
	if len(chain) > 0 {
		for i := len(chain) - 1; i >= 0; i-- {
			if !ipInNets(chain[i], trusted) {
				return chain[i]
			}
		}
		// Every hop is a trusted proxy: the leftmost entry is the origin.
		return chain[0]
	}

	if realIP := strings.TrimSpace(ctx.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	return peer
}

func peerAddress(remoteAddr string) string {
	remoteAddr = strings.TrimSpace(remoteAddr)
	if remoteAddr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

func ipInNets(value string, nets []*net.IPNet) bool {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return false
	}
	for _, network := range nets {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func copyHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	clone := make(map[string]string, len(headers))
	for k, v := range headers {
		clone[k] = v
	}
	return clone
}
