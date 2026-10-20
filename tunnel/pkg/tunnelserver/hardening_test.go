package tunnelserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/policy"
	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

func totalAlloc() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.TotalAlloc
}

func waitDone(t *testing.T, done <-chan struct{}, within time.Duration, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(within):
		t.Fatalf("%s did not finish within %s", what, within)
	}
}

// M3: a forged 0x7fffffff length header in the (unauthenticated) hello must be
// refused without allocating the announced size.
func TestHandshakeRejectsHugeLengthWithoutAllocating(t *testing.T) {
	ts := newTestServer()
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	done := make(chan struct{})
	go func() {
		ts.handleConnection(serverConn)
		close(done)
	}()

	before := totalAlloc()
	if _, err := clientConn.Write([]byte{0x7f, 0xff, 0xff, 0xff}); err != nil {
		t.Fatalf("write forged header: %v", err)
	}
	waitDone(t, done, 5*time.Second, "handleConnection")
	delta := totalAlloc() - before

	if delta > 4<<20 {
		t.Fatalf("server allocated %d bytes for a forged 2 GiB length header", delta)
	}
	ts.metrics.mu.RLock()
	count := ts.metrics.errorCounts["handshake_read"]
	ts.metrics.mu.RUnlock()
	if count != 1 {
		t.Fatalf("expected handshake_read error to be recorded once, got %d", count)
	}
	// The server closed its end.
	_ = clientConn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := clientConn.Read(make([]byte, 1)); err == nil {
		t.Fatal("expected the connection to be closed")
	}
}

// M3: a peer that connects and says nothing is dropped after handshake_timeout.
func TestHandshakeTimesOutOnSilentPeer(t *testing.T) {
	ts := newTestServer()
	ts.cfg = &config.Config{Tunnel: config.TunnelConfig{HandshakeTimeout: 1}}

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	done := make(chan struct{})
	start := time.Now()
	go func() {
		ts.handleConnection(serverConn)
		close(done)
	}()

	waitDone(t, done, 5*time.Second, "handleConnection")
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("handshake ended after %s, before the 1s deadline", elapsed)
	}
	ts.metrics.mu.RLock()
	count := ts.metrics.errorCounts["handshake_read"]
	ts.metrics.mu.RUnlock()
	if count != 1 {
		t.Fatalf("expected handshake_read error, got %d", count)
	}
}

// M3: the deadline also covers the TLS handshake, which is where a plain TCP
// connect-and-wait attacker would otherwise sit forever.
func TestTLSListenerDropsSilentPeerAfterHandshakeTimeout(t *testing.T) {
	pki := newTestPKI(t)
	cfg := &config.Config{
		BaseDomain: "gridbase.test",
		TLS:        config.TLSConfig{CertPath: pki.serverCertPath, KeyPath: pki.serverKeyPath},
		Tunnel:     config.TunnelConfig{HandshakeTimeout: 1},
	}
	ts, err := NewTunnelServer(cfg, logging.New("tls-timeout", logging.WithWriter(io.Discard)))
	if err != nil {
		t.Fatalf("NewTunnelServer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	go func() {
		_ = ts.AcceptLoop(ctx)
		close(loopDone)
	}()
	defer func() {
		cancel()
		_ = ts.Close()
		<-loopDone
	}()

	conn, err := net.Dial("tcp", ts.ListenerAddr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, rerr := conn.Read(make([]byte, 1)) // no bytes are ever sent; the server must hang up
	if rerr == nil {
		t.Fatal("expected the server to close the silent connection")
	}
	var nerr net.Error
	if errors.As(rerr, &nerr) && nerr.Timeout() {
		t.Fatalf("server kept the silent TLS connection open for %s", time.Since(start))
	}
}

// M3: frames from an authenticated client are bounded too.
func TestSendRejectsOversizedFrameFromClient(t *testing.T) {
	serverEnd, clientEnd := net.Pipe()
	defer clientEnd.Close()
	tc := &tunnelConn{apiKey: "k", mode: config.DomainTypeHTTP, conn: serverEnd, frameLimit: 1024, done: make(chan struct{})}

	go func() {
		for {
			frame, err := protocol.ReadStreamFrame(clientEnd, nil)
			if err != nil {
				return
			}
			if frame.Type == protocol.FrameRequestEnd {
				_, _ = clientEnd.Write([]byte{0x7f, 0xff, 0xff, 0xff})
				return
			}
		}
	}()

	before := totalAlloc()
	_, err := tc.send(context.Background(), &protocol.RequestPayload{Method: "GET", Path: "/"}, 0)
	delta := totalAlloc() - before

	if !errors.Is(err, protocol.ErrMessageTooLarge) {
		t.Fatalf("expected ErrMessageTooLarge, got %v", err)
	}
	if delta > 4<<20 {
		t.Fatalf("allocated %d bytes for a forged frame length", delta)
	}
}

// S3: policy.max_response_bytes is enforced and the desynchronised connection is dropped.
func TestForwardRequestEnforcesResponseLimit(t *testing.T) {
	ts := newTestServer()
	domain := config.DomainConfig{
		APIKey: "resp-key", Subdomain: "big", Type: config.DomainTypeHTTP, LocalURL: "http://localhost:1",
		Policy: config.DomainPolicy{MaxResponseBytes: 10},
	}
	ts.domainsBySubdomain["big"] = domain
	ts.domainsByAPIKey["resp-key"] = []config.DomainConfig{domain}

	serverEnd, clientEnd := net.Pipe()
	defer clientEnd.Close()
	tc := &tunnelConn{apiKey: "resp-key", mode: config.DomainTypeHTTP, conn: serverEnd, done: make(chan struct{})}
	ts.connections[buildConnectionKey("resp-key", config.DomainTypeHTTP, "")] = tc

	go func() {
		var id string
		for {
			frame, err := protocol.ReadStreamFrame(clientEnd, nil)
			if err != nil {
				return
			}
			id = frame.RequestID
			if frame.Type == protocol.FrameRequestEnd {
				break
			}
		}
		_ = protocol.WriteStreamFrame(clientEnd, protocol.FrameResponseStart, id, protocol.ResponseStartPayload{StatusCode: 200})
		_ = protocol.WriteStreamFrame(clientEnd, protocol.FrameResponseBody, id, protocol.BodyChunkPayload{Data: []byte("0123456789")})
		_ = protocol.WriteStreamFrame(clientEnd, protocol.FrameResponseBody, id, protocol.BodyChunkPayload{Data: []byte("X")})
	}()

	_, err := ts.ForwardRequest(context.Background(), "big", &protocol.RequestPayload{Method: "GET", Path: "/"})
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("expected ErrResponseTooLarge, got %v", err)
	}
	if _, ok := ts.getConnection("resp-key", config.DomainTypeHTTP, ""); ok {
		t.Fatal("connection must be dropped after an over-limit response")
	}
}

func TestResponseLimitDefaultsAndCeiling(t *testing.T) {
	ts := newTestServer() // no config: defaults
	if _, resp := ts.BodyLimits(); resp != config.DefaultMaxBodyBytes {
		t.Fatalf("default response limit = %d", resp)
	}
	if got := ts.responseLimit(config.DomainConfig{Policy: config.DomainPolicy{MaxResponseBytes: 5}}); got != 5 {
		t.Fatalf("policy should lower the limit, got %d", got)
	}
	ts.cfg = &config.Config{HTTP: config.HTTPConfig{MaxResponseBodyBytes: 100}}
	if got := ts.responseLimit(config.DomainConfig{Policy: config.DomainPolicy{MaxResponseBytes: 1 << 30}}); got != 100 {
		t.Fatalf("server ceiling must win over a larger policy value, got %d", got)
	}
}

// M2: the API key never shows up in logs, admin payloads, request ids or errors.
func TestAPIKeyNeverLeaksIntoOutputs(t *testing.T) {
	const secret = "SECRET-api-key-must-never-appear-0123"
	logger, logBuf := newCapturingLogger()
	ts := newTestServerWithLogger(logger)
	domain := config.DomainConfig{APIKey: secret, Subdomain: "alpha", Type: config.DomainTypeHTTP, LocalURL: "http://localhost:8080"}
	ts.domainsBySubdomain["alpha"] = domain
	ts.domainsByAPIKey[secret] = []config.DomainConfig{domain}

	// A rejected handshake with the secret as an unknown key (e.g. a typo of a real one).
	rejConn, rejClient := net.Pipe()
	rejDone := make(chan struct{})
	go func() { ts.handleConnection(rejConn); close(rejDone) }()
	_ = protocol.WriteMessage(rejClient, protocol.ClientHello{APIKey: secret + "-typo"})
	var rej protocol.ServerHello
	_ = protocol.ReadMessage(rejClient, &rej)
	rejClient.Close()
	waitDone(t, rejDone, 5*time.Second, "rejected handshake")

	// A successful handshake.
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	go ts.handleConnection(serverConn)
	if err := protocol.WriteMessage(clientConn, protocol.ClientHello{APIKey: secret}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	var ack protocol.ServerHello
	if err := protocol.ReadMessage(clientConn, &ack); err != nil || !ack.OK {
		t.Fatalf("handshake failed: %v %+v", err, ack)
	}

	// Serve one good request, then answer the next with a wrong request id.
	idRe := regexp.MustCompile(`^[0-9a-f]{32}$`)
	var seenIDs []string
	clientErr := make(chan error, 1)
	go func() {
		for round := 0; round < 2; round++ {
			var id string
			for {
				frame, err := protocol.ReadStreamFrame(clientConn, nil)
				if err != nil {
					clientErr <- err
					return
				}
				if frame.Type == protocol.FramePing {
					continue
				}
				id = frame.RequestID
				if frame.Type == protocol.FrameRequestEnd {
					break
				}
			}
			seenIDs = append(seenIDs, id)
			if round == 0 {
				_ = protocol.WriteStreamFrame(clientConn, protocol.FrameResponseStart, id, protocol.ResponseStartPayload{StatusCode: 200})
				_ = protocol.WriteStreamFrame(clientConn, protocol.FrameResponseEnd, id, nil)
			} else {
				_ = protocol.WriteStreamFrame(clientConn, protocol.FrameResponseStart, "someone-else", protocol.ResponseStartPayload{StatusCode: 200})
			}
		}
		clientErr <- nil
	}()

	if _, err := ts.ForwardRequest(context.Background(), "alpha", &protocol.RequestPayload{Method: "GET", Path: "/"}); err != nil {
		t.Fatalf("first forward: %v", err)
	}

	// Snapshot the admin payloads while the tunnel is connected.
	tunnelsJSON, _ := json.Marshal(ts.BuildTunnelsResponse())
	metricsJSON, _ := json.Marshal(ts.BuildMetricsResponse())
	if !strings.Contains(string(tunnelsJSON), APIKeyFingerprint(secret)) {
		t.Fatalf("/api/tunnels should identify the tunnel by fingerprint: %s", tunnelsJSON)
	}
	if !strings.Contains(string(metricsJSON), `"active_connections":1`) {
		t.Fatalf("expected the tunnel to be connected when snapshotting: %s", metricsJSON)
	}

	_, mismatchErr := ts.ForwardRequest(context.Background(), "alpha", &protocol.RequestPayload{Method: "GET", Path: "/"})
	if mismatchErr == nil {
		t.Fatal("expected a mismatched request id error")
	}
	if err := <-clientErr; err != nil {
		t.Fatalf("fake client: %v", err)
	}

	for _, id := range seenIDs {
		if !idRe.MatchString(id) {
			t.Fatalf("request id %q is not a 128-bit random hex string", id)
		}
		if strings.Contains(id, secret) {
			t.Fatalf("request id contains the API key: %q", id)
		}
	}
	if len(seenIDs) == 2 && seenIDs[0] == seenIDs[1] {
		t.Fatal("request ids must be unique")
	}

	outputs := map[string]string{
		"logs":           logBuf.String(),
		"/api/tunnels":   string(tunnelsJSON),
		"/api/metrics":   string(metricsJSON),
		"mismatch error": mismatchErr.Error(),
	}
	for name, out := range outputs {
		if out == "" {
			t.Fatalf("%s: empty output, test is not exercising anything", name)
		}
		if strings.Contains(out, secret) {
			t.Fatalf("%s contains the plaintext API key:\n%s", name, out)
		}
	}
	if !strings.Contains(logBuf.String(), APIKeyFingerprint(secret)) {
		t.Fatalf("logs should carry the key fingerprint:\n%s", logBuf.String())
	}
}

func TestAPIKeyFingerprint(t *testing.T) {
	a, b := APIKeyFingerprint("key-a"), APIKeyFingerprint("key-b")
	if a == b || len(a) != fingerprintHexLen || a != APIKeyFingerprint(" key-a ") {
		t.Fatalf("unexpected fingerprints a=%q b=%q", a, b)
	}
	if strings.Contains(a, "key-a") {
		t.Fatal("fingerprint must not contain the key")
	}
	if APIKeyFingerprint("") != "" {
		t.Fatal("empty key has no fingerprint")
	}
}

// limitedStub exposes BodyLimits like TunnelServer does.
type limitedStub struct {
	stubTunnel
	maxReq, maxResp int64
}

func (l *limitedStub) BodyLimits() (int64, int64) { return l.maxReq, l.maxResp }

// S3: without any policy the visitor body is still capped by the server-wide limit.
func TestProxyHandlerAppliesServerWideRequestLimit(t *testing.T) {
	stub := &limitedStub{stubTunnel: stubTunnel{domain: config.DomainConfig{Subdomain: "sub", LocalURL: "http://x"}}, maxReq: 8, maxResp: 1 << 20}
	handler := newTestProxyHandler(stub)

	over := httptest.NewRequest(http.MethodPost, "http://sub.example.com/", strings.NewReader("0123456789"))
	over.Host = "sub.example.com"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, over)
	if rec.Code != http.StatusRequestEntityTooLarge || stub.payload != nil {
		t.Fatalf("expected 413 and no forwarding, got %d (forwarded=%v)", rec.Code, stub.payload != nil)
	}

	// Unknown Content-Length (chunked): still bounded while reading.
	chunked := httptest.NewRequest(http.MethodPost, "http://sub.example.com/", io.NopCloser(strings.NewReader("0123456789")))
	chunked.ContentLength = -1
	chunked.Host = "sub.example.com"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, chunked)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("chunked body over the limit: expected 413, got %d", rec.Code)
	}

	ok := httptest.NewRequest(http.MethodPost, "http://sub.example.com/", strings.NewReader("12345678"))
	ok.Host = "sub.example.com"
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, ok)
	if rec.Code != http.StatusOK {
		t.Fatalf("body at the limit should pass, got %d", rec.Code)
	}
}

func TestProxyHandlerDefaultRequestLimitWithoutProvider(t *testing.T) {
	h := newTestProxyHandler(&stubTunnel{})
	if got := h.requestLimit(config.DomainConfig{}); got != config.DefaultMaxBodyBytes {
		t.Fatalf("default request limit = %d", got)
	}
	if got := h.requestLimit(config.DomainConfig{Policy: config.DomainPolicy{MaxRequestBytes: 7}}); got != 7 {
		t.Fatalf("policy should lower the limit, got %d", got)
	}
}

// Visitors get fixed messages: neither internal errors nor key material.
func TestProxyHandlerErrorBodiesAreFixed(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{"no client", ErrNoTunnelClient, http.StatusServiceUnavailable, "tunnel error: tunnel client not connected"},
		{"wrapped no client", fmt.Errorf("wrap: %w", ErrNoTunnelClient), http.StatusServiceUnavailable, "tunnel error: tunnel client not connected"},
		{"response too large", fmt.Errorf("%w (limit 10 bytes)", ErrResponseTooLarge), http.StatusBadGateway, "tunnel error: upstream response too large"},
		{"internal", errors.New("mismatched request id SECRET-KEY-abc at 10.0.0.5"), http.StatusBadGateway, "tunnel error"},
	}
	for _, tc := range cases {
		stub := &stubTunnel{domain: config.DomainConfig{Subdomain: "sub", LocalURL: "http://x"}, response: &protocol.ResponsePayload{}, err: tc.err}
		handler := newTestProxyHandler(stub)
		req := httptest.NewRequest(http.MethodGet, "http://sub.example.com/", nil)
		req.Host = "sub.example.com"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != tc.wantStatus {
			t.Errorf("%s: status %d, want %d", tc.name, rec.Code, tc.wantStatus)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != tc.wantBody {
			t.Errorf("%s: body %q, want %q", tc.name, got, tc.wantBody)
		}
	}
}

// S9: Reload carries trusted_proxies over to the policy engine.
func TestReloadUpdatesTrustedProxies(t *testing.T) {
	ts := newTestServer()
	policyCfg := config.DomainPolicy{IPAllowList: []string{"198.51.100.7"}}
	ctx := func() (map[string]string, error) {
		return ts.PolicyEngine().EvaluateRequest(policyCfg, requestContext("10.0.0.2:1", "198.51.100.7"))
	}
	if _, err := ctx(); err == nil {
		t.Fatal("X-Forwarded-For must be ignored before any proxy is trusted")
	}
	ts.Reload(&config.Config{HTTP: config.HTTPConfig{TrustedProxies: []string{"10.0.0.0/8"}}})
	if _, err := ctx(); err != nil {
		t.Fatalf("after trusting the proxy the forwarded address should be used: %v", err)
	}
	ts.Reload(&config.Config{})
	if _, err := ctx(); err == nil {
		t.Fatal("removing trusted_proxies on reload must stop trusting the header")
	}
}

func requestContext(remote, xff string) policy.RequestContext {
	return policy.RequestContext{RemoteAddr: remote, Header: http.Header{"X-Forwarded-For": []string{xff}}}
}

// sendHello runs one handshake through handleConnection over an in-memory pipe and
// returns the server's reply (when it sends one).
func sendHello(t *testing.T, ts *TunnelServer, hello protocol.ClientHello) protocol.ServerHello {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() { ts.handleConnection(serverConn); close(done) }()
	if err := protocol.WriteMessage(clientConn, hello); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	var reply protocol.ServerHello
	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_ = protocol.ReadMessageLimit(clientConn, &reply, protocol.MaxHandshakeBytes)
	clientConn.Close()
	waitDone(t, done, 5*time.Second, "handleConnection")
	return reply
}

// M3-R: the mode in an unauthenticated hello must never become a metrics key. However many
// distinct (huge) modes are sent, the per-mode error map stays within a fixed set of buckets.
func TestUnauthenticatedModeCannotGrowMetrics(t *testing.T) {
	logger, logBuf := newCapturingLogger()
	ts := newTestServerWithLogger(logger)
	const n = 300
	var heapBefore, heapAfter runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&heapBefore)
	for i := 0; i < n; i++ {
		mode := fmt.Sprintf("%06d-", i) + strings.Repeat("m", 48<<10)
		// Alternate between the empty-api-key rejection and the invalid-mode rejection.
		key := ""
		if i%2 == 1 {
			key = "some-unknown-key"
		}
		reply := sendHello(t, ts, protocol.ClientHello{APIKey: key, Mode: mode})
		if reply.OK {
			t.Fatalf("hello %d unexpectedly accepted", i)
		}
	}
	runtime.GC()
	runtime.ReadMemStats(&heapAfter)

	stats := ts.Stats()
	allowed := map[string]bool{config.DomainTypeHTTP: true, config.DomainTypeTCP: true, errorModeInvalid: true}
	for mode := range stats.ErrorCountsByMode {
		if !allowed[mode] {
			t.Fatalf("attacker-controlled mode became a metrics bucket (%d bytes): %.40q", len(mode), mode)
		}
	}
	if len(stats.ErrorCountsByMode) > len(allowed) {
		t.Fatalf("error mode buckets grew to %d", len(stats.ErrorCountsByMode))
	}
	// n/2 hellos have no API key (mode is not even looked at: it is bucketed as invalid because it is not http/tcp),
	// n/2 have an invalid mode.
	if got := stats.ErrorCountsByMode[errorModeInvalid]["handshake_invalid_mode"]; got != n/2 {
		t.Fatalf("handshake_invalid_mode in the invalid bucket = %d, want %d", got, n/2)
	}
	if got := stats.ErrorCountsByMode[errorModeInvalid]["handshake_empty_api_key"]; got != n/2 {
		t.Fatalf("handshake_empty_api_key in the invalid bucket = %d, want %d", got, n/2)
	}
	// Nothing proportional to the 300 x 48 KiB (about 14 MiB) that was sent may stay behind.
	if retained := int64(heapAfter.HeapAlloc) - int64(heapBefore.HeapAlloc); retained > 4<<20 {
		t.Fatalf("heap retained %d bytes after %d bogus hellos", retained, n)
	}
	// And no log line carries the attacker's string at full length.
	assertLogLinesShort(t, logBuf.String(), 1024)
}

// M3-R: mode and tunnel_id are cut to a short length wherever they reach a log line,
// including the rejections that happen after the API key is known (TCP tunnel id).
func TestHelloFieldsAreTruncatedInLogs(t *testing.T) {
	logger, logBuf := newCapturingLogger()
	ts := newTestServerWithLogger(logger)
	const key = "tcp-key-for-truncation-test"
	d := config.DomainConfig{APIKey: key, Subdomain: "tcp1", Type: config.DomainTypeTCP}
	ts.domainsBySubdomain["tcp1"] = d
	ts.domainsByAPIKey[key] = []config.DomainConfig{d}

	huge := strings.Repeat("Z", 30<<10) // two of these still fit in the 64 KiB hello limit
	for _, hello := range []protocol.ClientHello{
		{APIKey: key, Mode: config.DomainTypeTCP, TunnelID: huge},
		{APIKey: key, Mode: config.DomainTypeHTTP + huge},
		{APIKey: "unknown", Mode: config.DomainTypeTCP, TunnelID: huge},
		{APIKey: "unknown", Mode: huge, TunnelID: huge},
	} {
		if reply := sendHello(t, ts, hello); reply.OK {
			t.Fatalf("hello accepted: %.40q", hello.TunnelID)
		}
	}
	out := logBuf.String()
	if !strings.Contains(out, `"tunnel_id"`) || !strings.Contains(out, "truncated") {
		t.Fatalf("expected truncated tunnel_id/mode fields in the log:\n%.600s", out)
	}
	assertLogLinesShort(t, out, 1024)
	if strings.Contains(out, strings.Repeat("Z", 200)) {
		t.Fatal("log contains the untruncated hello field")
	}
	if got := len(ts.Stats().ErrorCountsByMode); got > 3 {
		t.Fatalf("error mode buckets: %d", got)
	}
}

func assertLogLinesShort(t *testing.T, out string, max int) {
	t.Helper()
	if out == "" {
		t.Fatal("no log output captured: the test is not exercising anything")
	}
	for _, line := range strings.Split(out, "\n") {
		if len(line) > max {
			t.Fatalf("log line of %d bytes (limit %d): %.200s...", len(line), max, line)
		}
	}
}

func TestTruncateForLog(t *testing.T) {
	if got := truncateForLog("short"); got != "short" {
		t.Fatalf("short value changed: %q", got)
	}
	exact := strings.Repeat("a", maxLogFieldBytes)
	if got := truncateForLog(exact); got != exact {
		t.Fatalf("value at the limit changed: %q", got)
	}
	long := strings.Repeat("b", 10_000)
	got := truncateForLog(long)
	if len(got) > maxLogFieldBytes+len("...(truncated)") || !strings.HasPrefix(got, strings.Repeat("b", maxLogFieldBytes)) {
		t.Fatalf("unexpected truncation: len=%d %q", len(got), got)
	}
	// A multi-byte rune straddling the limit must not be split.
	multi := strings.Repeat("a", maxLogFieldBytes-1) + strings.Repeat("あ", 50)
	if got := truncateForLog(multi); !utf8.ValidString(got) || strings.Contains(got, "�") {
		t.Fatalf("invalid UTF-8 after truncation: %q", got)
	}
	if got := truncateForLog("\xff\xfe bad"); !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8 passed through: %q", got)
	}
}

func TestErrorModeBucketIsFinite(t *testing.T) {
	cases := map[string]string{
		config.DomainTypeHTTP:      config.DomainTypeHTTP,
		config.DomainTypeTCP:       config.DomainTypeTCP,
		"":                         errorModeInvalid,
		"udp":                      errorModeInvalid,
		"HTTP2":                    errorModeInvalid,
		strings.Repeat("x", 1<<16): errorModeInvalid,
	}
	for in, want := range cases {
		if got := errorModeBucket(in); got != want {
			t.Fatalf("errorModeBucket(%.20q) = %q, want %q", in, got, want)
		}
	}
}
