package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

// S2: the client must hand a redirect back to the visitor, not follow it.
func TestLocalHTTPClientDoesNotFollowRedirects(t *testing.T) {
	var internalHits atomic.Int32
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		internalHits.Add(1)
		_, _ = w.Write([]byte("internal secret"))
	}))
	defer internal.Close()

	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1"})
		http.Redirect(w, r, internal.URL+"/metadata", http.StatusFound)
	}))
	defer app.Close()

	cfg := defaultClientConfig()
	runner := NewRunner(nil, nil)
	resp := runner.handleRequest(cfg, newLocalHTTPClient(2*time.Second), &protocol.RequestPayload{
		Method: http.MethodGet,
		Target: app.URL + "/go",
	})

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected the 302 to be passed through, got %d", resp.StatusCode)
	}
	if loc := resp.Header["Location"]; len(loc) != 1 || !strings.HasPrefix(loc[0], internal.URL) {
		t.Fatalf("Location header must reach the visitor unchanged, got %v", resp.Header)
	}
	if len(resp.Header["Set-Cookie"]) == 0 {
		t.Fatalf("Set-Cookie on the redirect was lost: %v", resp.Header)
	}
	if n := internalHits.Load(); n != 0 {
		t.Fatalf("the client followed the redirect and called the internal service %d time(s)", n)
	}
}

// S3: an oversized local response becomes a 502 instead of being buffered.
func TestHandleRequestCapsResponseBody(t *testing.T) {
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 1000)))
	}))
	defer app.Close()

	cfg := defaultClientConfig()
	cfg.HTTP.MaxResponseBytes = 999
	runner := NewRunner(nil, nil)

	resp := runner.handleRequest(cfg, newLocalHTTPClient(2*time.Second), &protocol.RequestPayload{Method: http.MethodGet, Target: app.URL})
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(resp.Error, "exceeds") {
		t.Fatalf("expected 502 for an oversized response, got %d %q", resp.StatusCode, resp.Error)
	}
	if len(resp.Body) > 200 {
		t.Fatalf("oversized body must not be forwarded, got %d bytes", len(resp.Body))
	}

	cfg.HTTP.MaxResponseBytes = 1000
	resp = runner.handleRequest(cfg, newLocalHTTPClient(2*time.Second), &protocol.RequestPayload{Method: http.MethodGet, Target: app.URL})
	if resp.StatusCode != http.StatusOK || len(resp.Body) != 1000 {
		t.Fatalf("a response exactly at the limit should pass, got %d (%d bytes)", resp.StatusCode, len(resp.Body))
	}
}

// N3: connection errors name the local URL; visitors must not see it.
func TestHandleRequestHidesLocalURLFromVisitor(t *testing.T) {
	cfg := defaultClientConfig()
	runner := NewRunner(nil, nil)
	resp := runner.handleRequest(cfg, newLocalHTTPClient(time.Second), &protocol.RequestPayload{
		Method: http.MethodGet,
		Target: "http://127.0.0.1:1/private-path",
	})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}
	if strings.Contains(string(resp.Body), "127.0.0.1") || strings.Contains(resp.Error, "private-path") {
		t.Fatalf("local URL leaked to the visitor: body=%q error=%q", resp.Body, resp.Error)
	}
}

func TestClientConfigRejectsNonPositiveBodyLimits(t *testing.T) {
	cfg := defaultClientConfig()
	cfg.ServerAddr, cfg.ServerName, cfg.APIKey = "tunnel.example.com:9443", "tunnel.example.com", "k"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
	cfg.HTTP.MaxResponseBytes = 0
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "max_response_bytes") {
		t.Fatalf("expected max_response_bytes error, got %v", err)
	}
}

// S3: a request body streamed from the server is bounded on the client as well.
func TestReadRequestBodyEnforcesLimit(t *testing.T) {
	serverEnd, clientEnd := net.Pipe()
	defer serverEnd.Close()
	defer clientEnd.Close()

	cfg := defaultClientConfig()
	cfg.HTTP.MaxRequestBytes = 8
	runner := NewRunner(nil, nil)

	go func() {
		_ = protocol.WriteStreamFrame(serverEnd, protocol.FrameRequestBody, "r1", protocol.BodyChunkPayload{Data: []byte("12345")})
		_ = protocol.WriteStreamFrame(serverEnd, protocol.FrameRequestBody, "r1", protocol.BodyChunkPayload{Data: []byte("67890")})
	}()

	_, err := runner.readRequestBody(context.Background(), clientEnd, cfg, "r1")
	if err == nil || !strings.Contains(err.Error(), "exceeds 8 bytes") {
		t.Fatalf("expected a body limit error, got %v", err)
	}
}
