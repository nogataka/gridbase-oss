package tunnelserver

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/policy"
	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

type stubTunnel struct {
	domain   config.DomainConfig
	response *protocol.ResponsePayload
	err      error
	payload  *protocol.RequestPayload
}

func newTestProxyHandler(tunnel tunnelBackend) *ProxyHandler {
	logger := logging.New("test", logging.WithWriter(io.Discard))
	return &ProxyHandler{
		tunnel: tunnel,
		logger: logger,
		events: newEventRecorder(logger),
	}
}

func (s *stubTunnel) BaseDomain() string {
	return "example.com"
}

func (s *stubTunnel) LookupDomain(subdomain string) (config.DomainConfig, bool) {
	return s.domain, true
}

func (s *stubTunnel) PolicyEngine() *policy.Engine {
	return policy.NewEngine()
}

func (s *stubTunnel) ForwardRequest(_ context.Context, _ string, payload *protocol.RequestPayload) (*protocol.ResponsePayload, error) {
	s.payload = payload
	if s.payload != nil {
		s.payload.Target = strings.TrimRight(s.domain.LocalURL, "/") + payload.Path
		s.payload.Subdomain = s.domain.Subdomain
	}
	if s.response == nil {
		return &protocol.ResponsePayload{
			StatusCode: http.StatusOK,
			Header:     map[string][]string{"Content-Type": {"text/plain"}},
			Body:       []byte("ok"),
		}, nil
	}
	return s.response, s.err
}

func TestProxyHandlerRejectsOversizedBody(t *testing.T) {
	tunnel := &stubTunnel{
		domain: config.DomainConfig{
			Subdomain: "sub",
			Policy: config.DomainPolicy{
				MaxRequestBytes: 5,
			},
		},
	}
	handler := newTestProxyHandler(tunnel)

	req := httptest.NewRequest(http.MethodPost, "http://sub.example.com/data", strings.NewReader("0123456789"))
	req.Host = "sub.example.com"

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected status 413, got %d", rec.Code)
	}
	if tunnel.payload != nil {
		t.Fatalf("expected forward request not to be called")
	}
	if !strings.Contains(rec.Body.String(), "exceeded") {
		t.Fatalf("expected error message to mention exceeded, got %q", rec.Body.String())
	}
}

func TestProxyHandlerInjectsDefaultHeaders(t *testing.T) {
	tunnel := &stubTunnel{
		domain: config.DomainConfig{
			Subdomain: "sub",
			LocalURL:  "http://backend",
			Policy: config.DomainPolicy{
				DefaultHeaders: map[string]string{
					"X-Injected": "value",
				},
				MaxRequestBytes: 1024,
			},
		},
		response: &protocol.ResponsePayload{
			StatusCode: http.StatusAccepted,
			Header:     map[string][]string{"Content-Length": {"0"}},
			Body:       []byte{},
		},
	}

	handler := newTestProxyHandler(tunnel)

	req := httptest.NewRequest(http.MethodPost, "http://sub.example.com/api", bytes.NewBufferString("ping"))
	req.Host = "sub.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d", rec.Code)
	}

	if tunnel.payload == nil {
		t.Fatalf("expected payload to be forwarded")
	}

	values := tunnel.payload.Header["X-Injected"]
	if len(values) != 1 || values[0] != "value" {
		t.Fatalf("expected injected header, got %#v", values)
	}

	if tunnel.payload.Target != "http://backend/api" {
		t.Fatalf("unexpected target: %s", tunnel.payload.Target)
	}
}

func TestProxyHandlerRejectsHostOutsideBaseDomain(t *testing.T) {
	tunnel := &stubTunnel{domain: config.DomainConfig{Subdomain: "sub"}}
	handler := newTestProxyHandler(tunnel)

	for _, host := range []string{"sub.other.org", "example.com", "evilexample.com", ""} {
		req := httptest.NewRequest(http.MethodGet, "http://placeholder/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("host %q: expected 400, got %d", host, rec.Code)
		}
	}
}

func TestProxyHandlerAcceptsHostWithPortAndCase(t *testing.T) {
	tunnel := &stubTunnel{domain: config.DomainConfig{Subdomain: "sub", LocalURL: "http://localhost:1"}}
	handler := newTestProxyHandler(tunnel)

	req := httptest.NewRequest(http.MethodGet, "http://placeholder/x", nil)
	req.Host = "SUB.Example.com:8080"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}
