package main

import (
	"bytes"
	"context"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

func TestBackoffStrategyNoJitter(t *testing.T) {
	cfg := ReconnectConfig{
		Initial:    NewDuration(2 * time.Second),
		Max:        NewDuration(10 * time.Second),
		Multiplier: 2,
		Jitter:     0,
	}

	strategy := newBackoffStrategy(cfg)
	strategy.source = rand.New(rand.NewSource(1))

	delays := []time.Duration{
		strategy.NextDelay(),
		strategy.NextDelay(),
		strategy.NextDelay(),
		strategy.NextDelay(),
	}
	expected := []time.Duration{
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		10 * time.Second,
	}

	for i, want := range expected {
		if delays[i] != want {
			t.Fatalf("delay[%d] expected %s, got %s", i, want, delays[i])
		}
	}

	strategy.Reset()
	if delay := strategy.NextDelay(); delay != 2*time.Second {
		t.Fatalf("after reset expected initial delay 2s, got %s", delay)
	}
}

func TestBackoffStrategyWithJitter(t *testing.T) {
	cfg := ReconnectConfig{
		Initial:    NewDuration(5 * time.Second),
		Max:        NewDuration(30 * time.Second),
		Multiplier: 2,
		Jitter:     0.5,
	}

	strategy := newBackoffStrategy(cfg)
	strategy.source = rand.New(rand.NewSource(42))

	delay := strategy.NextDelay()
	min := 5 * time.Second / 2
	max := 5*time.Second + 5*time.Second/2
	if delay < min || delay > max {
		t.Fatalf("delay with jitter expected between %s and %s, got %s", min, max, delay)
	}
}

func TestWaitWithContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitWithContext(ctx, 5*time.Second); err == nil {
		t.Fatalf("expected context cancellation error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHandleRequestUsesOverrideURL(t *testing.T) {
	cfg := defaultClientConfig()
	cfg.HTTP.OverrideURL = "http://override.example/internal"
	runner := NewRunner(nil, nil)

	var requestedURL string
	httpClient := &http.Client{
		Timeout: 2 * time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestedURL = req.URL.String()
			resp := &http.Response{
				StatusCode: http.StatusCreated,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString("override used")),
			}
			resp.Header.Set("X-Test", "override")
			return resp, nil
		}),
	}

	payload := &protocol.RequestPayload{
		Method: http.MethodGet,
		Path:   "/api/test?query=1",
		Target: "http://original.example/should-not-be-used",
		Header: map[string][]string{
			"X-Forwarded-For": {"1.1.1.1"},
		},
	}

	resp := runner.handleRequest(cfg, httpClient, payload)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected status 201, got %d", resp.StatusCode)
	}
	if string(resp.Body) != "override used" {
		t.Fatalf("unexpected response body %q", string(resp.Body))
	}
	if requestedURL != "http://override.example/internal/api/test?query=1" {
		t.Fatalf("expected override target to be used, got %s", requestedURL)
	}
}

func TestHandleRequestFallsBackToPayloadTarget(t *testing.T) {
	cfg := defaultClientConfig()
	cfg.HTTP.OverrideURL = ""
	runner := NewRunner(nil, nil)

	var requestedURL string
	httpClient := &http.Client{
		Timeout: 2 * time.Second,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestedURL = req.URL.String()
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(bytes.NewBufferString("fallback used")),
			}, nil
		}),
	}

	payload := &protocol.RequestPayload{
		Method: http.MethodPost,
		Path:   "/should-not-be-used",
		Target: "http://original.example/from-target",
		Body:   []byte("ping"),
	}

	resp := runner.handleRequest(cfg, httpClient, payload)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}
	if string(resp.Body) != "fallback used" {
		t.Fatalf("unexpected response body %q", string(resp.Body))
	}
	if requestedURL != "http://original.example/from-target" {
		t.Fatalf("expected original target to be used, got %s", requestedURL)
	}
}

func TestHandleRequestMissingTarget(t *testing.T) {
	cfg := defaultClientConfig()
	cfg.HTTP.OverrideURL = ""
	httpClient := &http.Client{Timeout: 2 * time.Second}
	runner := NewRunner(nil, nil)

	resp := runner.handleRequest(cfg, httpClient, &protocol.RequestPayload{})
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 for missing target, got %d", resp.StatusCode)
	}
	if resp.Error == "" || !strings.Contains(resp.Error, "missing target url") {
		t.Fatalf("expected missing target error, got %q", resp.Error)
	}
}
