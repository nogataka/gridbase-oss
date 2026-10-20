package directus_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/directus"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
)

func discardLogger() *logging.Logger {
	return logging.New("directus-test", logging.WithWriter(io.Discard))
}

func TestFetchActiveTunnelsSuccess(t *testing.T) {
	const (
		usersResponse = `{"data":[
			{"userId":"user-1","api_key":"alpha","status":"active","settings":{"default_headers":{"X-Test":"true"}}},
			{"userId":"user-2","api_key":"beta","status":"active","settings":{}}
		]}`
		subdomainsResponse = `{"data":[
			{"id":"1","userId":"user-1","subdomain":"alpha","local_server_url":"http://localhost:9000","status":"active"},
			{"id":"2","userId":"user-2","subdomain":"beta","local_server_url":"http://localhost:9001","status":"active"}
		]}`
	)

	headersByPath := make(map[string]http.Header)
	hits := make(map[string]int)

	httpClient := &stubHTTPClient{
		fn: func(req *http.Request) (*http.Response, error) {
			hits[req.URL.Path]++
			headersByPath[req.URL.Path] = req.Header.Clone()
			switch req.URL.Path {
			case "/items/users":
				return jsonResponse(http.StatusOK, usersResponse), nil
			case "/items/subdomains":
				return jsonResponse(http.StatusOK, subdomainsResponse), nil
			default:
				t.Fatalf("unexpected path %q", req.URL.Path)
				return nil, nil
			}
		},
	}

	cfg := config.DirectusConfig{
		URL:   "https://directus.example.com",
		Token: "test-key",
	}

	client, err := directus.NewClient(
		cfg,
		directus.WithLogger(discardLogger()),
		directus.WithHTTPClient(httpClient),
	)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	records, err := client.FetchActiveTunnels(ctx)
	if err != nil {
		t.Fatalf("FetchActiveTunnels error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0].APIKey != "alpha" || records[1].APIKey != "beta" {
		t.Fatalf("unexpected API keys: %#v", records)
	}
	if records[0].Mode != config.DomainTypeHTTP || records[1].Mode != config.DomainTypeHTTP {
		t.Fatalf("expected default http mode, got %#v", records)
	}
	if records[0].LocalHost != "" || records[0].LocalPort != 0 {
		t.Fatalf("unexpected tcp fields for http record: host=%q port=%d", records[0].LocalHost, records[0].LocalPort)
	}
	if headersByPath["/items/users"].Get("Authorization") != "Bearer test-key" {
		t.Fatalf("unexpected Authorization header %q", headersByPath["/items/users"].Get("Authorization"))
	}
	if len(records[0].SettingsRaw) == 0 {
		t.Fatal("expected settings payload for first record")
	}
	if hits["/items/users"] == 0 || hits["/items/subdomains"] == 0 {
		t.Fatalf("expected users and subdomains endpoints to be called, hits=%v", hits)
	}
}

func TestFetchActiveTunnelsError(t *testing.T) {
	httpClient := &stubHTTPClient{
		fn: func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       io.NopCloser(strings.NewReader("bad gateway")),
			}, nil
		},
	}

	cfg := config.DirectusConfig{
		URL:   "https://directus.example.com",
		Token: "test-key",
	}

	client, err := directus.NewClient(
		cfg,
		directus.WithLogger(discardLogger()),
		directus.WithHTTPClient(httpClient),
	)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	if _, err := client.FetchActiveTunnels(ctx); err == nil {
		t.Fatal("expected fetch error, got nil")
	}
}

func TestFetchActiveTunnelsTCPMode(t *testing.T) {
	const (
		usersResponse = `{"data":[
			{"userId":"user-1","api_key":"alpha","status":"active","settings":{}}
		]}`
		subdomainsResponse = `{"data":[
			{"id":"1","userId":"user-1","subdomain":"alpha-tcp","mode":"tcp","local_host":"127.0.0.1","local_port":5432,"status":"active"}
		]}`
	)

	httpClient := &stubHTTPClient{
		fn: func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/items/users":
				return jsonResponse(http.StatusOK, usersResponse), nil
			case "/items/subdomains":
				return jsonResponse(http.StatusOK, subdomainsResponse), nil
			default:
				t.Fatalf("unexpected path %q", req.URL.Path)
				return nil, nil
			}
		},
	}

	cfg := config.DirectusConfig{
		URL:   "https://directus.example.com",
		Token: "test-key",
	}

	client, err := directus.NewClient(
		cfg,
		directus.WithLogger(discardLogger()),
		directus.WithHTTPClient(httpClient),
	)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	records, err := client.FetchActiveTunnels(ctx)
	if err != nil {
		t.Fatalf("FetchActiveTunnels error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	rec := records[0]
	if rec.Mode != config.DomainTypeTCP {
		t.Fatalf("expected tcp mode, got %q", rec.Mode)
	}
	if rec.LocalHost != "127.0.0.1" || rec.LocalPort != 5432 {
		t.Fatalf("unexpected tcp location: host=%q port=%d", rec.LocalHost, rec.LocalPort)
	}
	if rec.LocalURL != "" {
		t.Fatalf("expected empty local_url for tcp, got %q", rec.LocalURL)
	}
}

func TestWatchHandlesFailuresAndRecovery(t *testing.T) {
	var (
		mu                sync.Mutex
		usersPayload      = `{"data":[{"userId":"user-1","api_key":"alpha","status":"active","settings":{}}]}`
		subdomainsPayload = `{"data":[{"id":"1","userId":"user-1","subdomain":"alpha","local_server_url":"http://localhost:9000","status":"active"}]}`
		fail              bool
	)

	httpClient := &stubHTTPClient{
		fn: func(req *http.Request) (*http.Response, error) {
			mu.Lock()
			defer mu.Unlock()
			if fail {
				return jsonResponse(http.StatusServiceUnavailable, "unavailable"), nil
			}
			switch req.URL.Path {
			case "/items/users":
				return jsonResponse(http.StatusOK, usersPayload), nil
			case "/items/subdomains":
				return jsonResponse(http.StatusOK, subdomainsPayload), nil
			default:
				return nil, fmt.Errorf("unexpected path %q", req.URL.Path)
			}
		},
	}

	cfg := config.DirectusConfig{
		URL:   "https://directus.example.com",
		Token: "watch-key",
	}

	client, err := directus.NewClient(
		cfg,
		directus.WithLogger(discardLogger()),
		directus.WithHTTPClient(httpClient),
		directus.WithPollInterval(20*time.Millisecond),
		directus.WithBackoff(10*time.Millisecond, 40*time.Millisecond, 2),
		directus.WithMaxRetries(1),
	)
	if err != nil {
		t.Fatalf("NewClient error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	changes := make(chan []directus.TunnelRecord, 4)
	done := make(chan struct{})

	go func() {
		client.Watch(ctx, func(records []directus.TunnelRecord) {
			select {
			case changes <- records:
			default:
			}
		})
		close(done)
	}()

	select {
	case <-time.After(200 * time.Millisecond):
		t.Fatal("timeout waiting for initial snapshot")
	case recs := <-changes:
		if len(recs) != 1 || recs[0].APIKey != "alpha" {
			t.Fatalf("unexpected initial records: %#v", recs)
		}
	}

	if client.State().Degraded {
		t.Fatalf("expected healthy state after initial fetch")
	}

	mu.Lock()
	fail = true
	mu.Unlock()

	degradedDeadline := time.After(500 * time.Millisecond)
	for {
		if client.State().Degraded {
			break
		}
		select {
		case <-degradedDeadline:
			t.Fatal("expected degraded state after failures")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	mu.Lock()
	fail = false
	subdomainsPayload = `{"data":[{"id":"1","userId":"user-1","subdomain":"alpha","local_server_url":"http://localhost:9001","status":"active"}]}`
	mu.Unlock()

	select {
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for recovery change")
	case recs := <-changes:
		if recs[0].LocalURL != "http://localhost:9001" {
			t.Fatalf("expected updated local_url, got %#v", recs[0])
		}
	}

	recoveryDeadline := time.After(300 * time.Millisecond)
	for {
		if !client.State().Degraded {
			break
		}
		select {
		case <-recoveryDeadline:
			t.Fatal("expected healthy state after recovery")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watch loop did not exit")
	}
}

type stubHTTPClient struct {
	fn func(*http.Request) (*http.Response, error)
}

func (s *stubHTTPClient) Do(req *http.Request) (*http.Response, error) {
	if s.fn == nil {
		return nil, errors.New("no handler defined")
	}
	return s.fn(req)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
