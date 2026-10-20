package tunnelserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/policy"
	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

// ProxyHandler は HTTP リクエストをトンネルクライアントへ転送する。
type ProxyHandler struct {
	tunnel tunnelBackend
	logger *logging.Logger
	events *eventRecorder
}

// bodyLimitProvider is implemented by backends that carry server-wide request /
// response body ceilings (TunnelServer does). Backends without it use the defaults.
type bodyLimitProvider interface {
	BodyLimits() (maxRequestBytes, maxResponseBytes int64)
}

type tunnelBackend interface {
	BaseDomain() string
	LookupDomain(subdomain string) (config.DomainConfig, bool)
	PolicyEngine() *policy.Engine
	ForwardRequest(ctx context.Context, subdomain string, payload *protocol.RequestPayload) (*protocol.ResponsePayload, error)
}

// NewProxyHandler は ProxyHandler を生成する。
func NewProxyHandler(tunnel *TunnelServer) http.Handler {
	return &ProxyHandler{
		tunnel: tunnel,
		logger: tunnel.logger,
		events: tunnel.events,
	}
}

// NewProxyHandlerWithBackend constructs a ProxyHandler for an arbitrary tunnel backend.
// Primarily intended for tests or alternative backends that satisfy tunnelBackend.
func NewProxyHandlerWithBackend(backend tunnelBackend, logger *logging.Logger) http.Handler {
	if backend == nil {
		panic("tunnelserver: backend is required")
	}
	if logger == nil {
		logger = logging.New("tunnel-proxy")
	}
	return &ProxyHandler{
		tunnel: backend,
		logger: logger,
		events: newEventRecorder(logger),
	}
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	subdomain := ExtractSubdomain(r.Host, h.tunnel.BaseDomain())
	if subdomain == "" {
		http.Error(w, "invalid host header", http.StatusBadRequest)
		return
	}

	domain, ok := h.tunnel.LookupDomain(subdomain)
	if !ok {
		http.Error(w, "unknown subdomain", http.StatusServiceUnavailable)
		return
	}

	defer r.Body.Close()

	bodyBytes, err := h.readRequestBody(r, h.requestLimit(domain))
	if err != nil {
		var limitErr *requestTooLargeError
		if errors.As(err, &limitErr) {
			http.Error(w, limitErr.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, fmt.Sprintf("read body: %v", err), http.StatusBadRequest)
		return
	}

	requestCtx := policy.RequestContext{
		Method:     r.Method,
		Header:     r.Header.Clone(),
		BodySize:   int64(len(bodyBytes)),
		RemoteAddr: r.RemoteAddr,
		TLS:        r.TLS != nil,
	}

	defaultHeaders, err := h.tunnel.PolicyEngine().EvaluateRequest(domain.Policy, requestCtx)
	if err != nil {
		status := http.StatusInternalServerError
		message := "policy evaluation failed"
		var violation *policy.ViolationError
		if errors.As(err, &violation) {
			if violation.HTTPStatus > 0 {
				status = violation.HTTPStatus
			} else {
				status = http.StatusForbidden
			}
			if violation.Message != "" {
				message = violation.Message
			} else {
				message = "policy violation"
			}
			h.events.Warn("tunnel.policy_violation", "policy violation", err, logging.Fields{
				"subdomain": subdomain,
				"code":      violation.Code,
				"details":   violation.Details,
			})
		} else {
			h.events.Error("tunnel.policy_evaluation_failed", "policy evaluation error", err, logging.Fields{
				"subdomain": subdomain,
			})
		}
		http.Error(w, message, status)
		return
	}

	headerCopy := make(map[string][]string, len(r.Header))
	for key, values := range r.Header {
		headerCopy[key] = append([]string(nil), values...)
	}

	for key, value := range defaultHeaders {
		if key == "" || value == "" {
			continue
		}
		existing := headerCopy[key]
		alreadySet := false
		for _, v := range existing {
			if v == value {
				alreadySet = true
				break
			}
		}
		if alreadySet {
			continue
		}
		headerCopy[key] = append(headerCopy[key], value)
	}

	payload := &protocol.RequestPayload{
		Method: r.Method,
		Path:   r.URL.RequestURI(),
		Header: headerCopy,
		Body:   bodyBytes,
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	resp, err := h.tunnel.ForwardRequest(ctx, subdomain, payload)
	if err != nil {
		// Visitors only get a fixed message: the underlying error can name internal
		// details and is already in the server log (tunnel.forward_failed).
		status, message := http.StatusBadGateway, "tunnel error"
		switch {
		case errors.Is(err, ErrNoTunnelClient):
			status, message = http.StatusServiceUnavailable, "tunnel error: tunnel client not connected"
		case errors.Is(err, ErrResponseTooLarge):
			status, message = http.StatusBadGateway, "tunnel error: upstream response too large"
		}
		http.Error(w, message, status)
		return
	}

	for key, values := range resp.Header {
		if strings.EqualFold(key, "Transfer-Encoding") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}

	if _, ok := resp.Header["Content-Length"]; !ok {
		w.Header().Set("Content-Length", strconv.Itoa(len(resp.Body)))
	}

	statusCode := resp.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}

	w.WriteHeader(statusCode)
	if len(resp.Body) > 0 {
		// A failed write means the visitor went away; there is nothing to recover.
		_, _ = w.Write(resp.Body)
	}
}

// requestLimit returns the request body cap for a domain: the server-wide
// ceiling, lowered by the domain policy's max_request_bytes when set.
func (h *ProxyHandler) requestLimit(domain config.DomainConfig) int64 {
	limit := config.DefaultMaxBodyBytes
	if provider, ok := h.tunnel.(bodyLimitProvider); ok {
		limit, _ = provider.BodyLimits()
	}
	if p := domain.Policy.MaxRequestBytes; p > 0 && p < limit {
		limit = p
	}
	return limit
}

type requestTooLargeError struct {
	max int64
}

func (e *requestTooLargeError) Error() string {
	return fmt.Sprintf("request body exceeded %d bytes", e.max)
}

// readRequestBody reads the visitor's body, refusing anything above maxBytes
// (which is always positive: there is no unlimited mode).
func (h *ProxyHandler) readRequestBody(r *http.Request, maxBytes int64) ([]byte, error) {
	if r.ContentLength > maxBytes {
		return nil, &requestTooLargeError{max: maxBytes}
	}
	reader := io.LimitReader(r.Body, maxBytes+1)

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(reader); err != nil {
		return nil, err
	}

	if int64(buf.Len()) > maxBytes {
		return nil, &requestTooLargeError{max: maxBytes}
	}

	return buf.Bytes(), nil
}
