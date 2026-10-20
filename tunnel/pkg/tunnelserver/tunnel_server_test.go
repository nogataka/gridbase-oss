package tunnelserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/policy"
	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

func newTestServer() *TunnelServer {
	return newTestServerWithLogger(logging.New("tunnel-test", logging.WithWriter(io.Discard)))
}

func newTestServerWithLogger(logger *logging.Logger) *TunnelServer {
	if logger == nil {
		logger = logging.New("tunnel-test")
	}
	ts := &TunnelServer{
		connections:        make(map[string]*tunnelConn),
		domainsBySubdomain: make(map[string]config.DomainConfig),
		domainsByAPIKey:    make(map[string][]config.DomainConfig),
		logger:             logger,
		events:             newEventRecorder(logger),
		policyEngine:       policy.NewEngine(),
	}
	ts.metrics.bytesIn = make(map[string]int64)
	ts.metrics.bytesOut = make(map[string]int64)
	ts.metrics.bytesInByMode = make(map[string]map[string]int64)
	ts.metrics.bytesOutByMode = make(map[string]map[string]int64)
	ts.metrics.errorCounts = make(map[string]int)
	ts.metrics.errorCountsByMode = make(map[string]map[string]int)
	return ts
}

func newCapturingLogger() (*logging.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	logger := logging.New("tunnel-test", logging.WithWriter(buf), logging.WithLevel(logging.LevelDebug))
	return logger, buf
}

type staticAddr string

func (a staticAddr) Network() string { return "tcp" }

func (a staticAddr) String() string { return string(a) }

type connWithRemote struct {
	net.Conn
	remote net.Addr
}

func (c *connWithRemote) RemoteAddr() net.Addr {
	if c == nil {
		return nil
	}
	if c.remote != nil {
		return c.remote
	}
	if c.Conn != nil {
		return c.Conn.RemoteAddr()
	}
	return nil
}

func TestTunnelServerStatsIncludesActiveConnections(t *testing.T) {
	ts := newTestServer()
	now := time.Now().Add(-time.Minute)
	ts.metrics.lastReload = now
	ts.metrics.certExpiry = now.Add(24 * time.Hour)

	domain := config.DomainConfig{APIKey: "key-1", Subdomain: "foo.gridbase.dev", Type: config.DomainTypeHTTP}
	ts.domainsByAPIKey["key-1"] = []config.DomainConfig{domain}

	connKey := buildConnectionKey("key-1", config.DomainTypeHTTP, "")
	ts.connections[connKey] = &tunnelConn{
		apiKey:           "key-1",
		mode:             config.DomainTypeHTTP,
		connectedAt:      now,
		lastActivity:     now.Add(30 * time.Second),
		remoteAddr:       "203.0.113.10:44321",
		bytesIn:          123,
		bytesOut:         456,
		errorCounts:      map[string]int{"forward_send": 2},
		peerCommonName:   "client.gridbase.dev",
		peerSerialNumber: "ab12",
		done:             make(chan struct{}),
	}

	ts.metrics.bytesIn["key-1"] = 123
	ts.metrics.bytesOut["key-1"] = 456
	ts.metrics.errorCounts["forward_send"] = 2
	ts.metrics.bytesInByMode[config.DomainTypeHTTP] = map[string]int64{"key-1": 123}
	ts.metrics.bytesOutByMode[config.DomainTypeHTTP] = map[string]int64{"key-1": 456}
	ts.metrics.errorCountsByMode[config.DomainTypeHTTP] = map[string]int{"forward_send": 2}

	stats := ts.Stats()
	if stats.ActiveConnections != 1 {
		t.Fatalf("expected 1 active connection, got %d", stats.ActiveConnections)
	}
	if stats.ActiveByMode[config.DomainTypeHTTP] != 1 {
		t.Fatalf("expected http active connections 1, got %d", stats.ActiveByMode[config.DomainTypeHTTP])
	}
	if got := stats.BytesIn["key-1"]; got != 123 {
		t.Errorf("expected bytesIn=123, got %d", got)
	}
	if got := stats.BytesOut["key-1"]; got != 456 {
		t.Errorf("expected bytesOut=456, got %d", got)
	}
	if stats.BytesInByMode[config.DomainTypeHTTP]["key-1"] != 123 {
		t.Errorf("expected mode bytesIn to equal 123")
	}
	if stats.ErrorCountsByMode[config.DomainTypeHTTP]["forward_send"] != 2 {
		t.Errorf("expected error_counts_by_mode forward_send=2")
	}
	if stats.ErrorCounts["forward_send"] != 2 {
		t.Errorf("expected error count 2, got %d", stats.ErrorCounts["forward_send"])
	}
	domainStats, ok := stats.Domains[connKey]
	if !ok {
		t.Fatalf("expected domain stats for key-1")
	}
	if domainStats.Mode != config.DomainTypeHTTP {
		t.Fatalf("expected mode http, got %s", domainStats.Mode)
	}
	if domainStats.TunnelID != "" {
		t.Fatalf("expected empty tunnel id for http mode, got %s", domainStats.TunnelID)
	}
	if domainStats.RemoteAddr != "203.0.113.10:44321" {
		t.Errorf("unexpected remote addr: %s", domainStats.RemoteAddr)
	}
	if got := domainStats.Errors["forward_send"]; got != 2 {
		t.Errorf("expected domain error count 2, got %d", got)
	}
	if len(domainStats.Subdomains) != 1 || domainStats.Subdomains[0] != "foo.gridbase.dev" {
		t.Errorf("unexpected subdomains: %#v", domainStats.Subdomains)
	}
	if domainStats.BytesIn != 123 || domainStats.BytesOut != 456 {
		t.Errorf("unexpected byte counters: in=%d out=%d", domainStats.BytesIn, domainStats.BytesOut)
	}
	if !domainStats.ConnectedAt.Equal(now) {
		t.Errorf("expected ConnectedAt to equal %v, got %v", now, domainStats.ConnectedAt)
	}
}

func TestTunnelServerTunnelsSnapshotDeterministic(t *testing.T) {
	ts := newTestServer()
	ts.domainsByAPIKey["b-key"] = []config.DomainConfig{{APIKey: "b-key", Subdomain: "beta.gridbase.dev", Type: config.DomainTypeHTTP}}
	ts.domainsByAPIKey["a-key"] = []config.DomainConfig{{APIKey: "a-key", Subdomain: "alpha.gridbase.dev", Type: config.DomainTypeHTTP}}

	now := time.Now()
	ts.connections[buildConnectionKey("b-key", config.DomainTypeHTTP, "")] = &tunnelConn{
		apiKey:       "b-key",
		mode:         config.DomainTypeHTTP,
		connectedAt:  now,
		lastActivity: now,
		remoteAddr:   "198.51.100.8:5000",
		done:         make(chan struct{}),
	}
	ts.connections[buildConnectionKey("a-key", config.DomainTypeHTTP, "")] = &tunnelConn{
		apiKey:       "a-key",
		mode:         config.DomainTypeHTTP,
		connectedAt:  now.Add(-5 * time.Second),
		lastActivity: now.Add(-3 * time.Second),
		remoteAddr:   "198.51.100.7:5001",
		done:         make(chan struct{}),
	}

	snapshot := ts.TunnelsSnapshot()
	if len(snapshot) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snapshot))
	}
	if snapshot[0].APIKey != "a-key" || snapshot[1].APIKey != "b-key" {
		t.Fatalf("snapshot not sorted by api key: %#v", snapshot)
	}
	if snapshot[0].Subdomains[0] != "alpha.gridbase.dev" {
		t.Errorf("unexpected subdomain for a-key: %#v", snapshot[0].Subdomains)
	}
	if snapshot[0].Mode != config.DomainTypeHTTP {
		t.Fatalf("expected first snapshot mode http, got %s", snapshot[0].Mode)
	}
	if snapshot[0].TunnelID != "" {
		t.Fatalf("expected empty tunnel id for http snapshot, got %s", snapshot[0].TunnelID)
	}

	// Mutate the returned slice and ensure internal state is unaffected.
	snapshot[0].Subdomains[0] = "mutated"
	if ts.domainsByAPIKey["a-key"][0].Subdomain != "alpha.gridbase.dev" {
		t.Errorf("internal domain state mutated by snapshot")
	}
}

func TestTunnelServerRecordError(t *testing.T) {
	ts := newTestServer()
	connKey := buildConnectionKey("key-1", config.DomainTypeHTTP, "")
	ts.connections[connKey] = &tunnelConn{
		apiKey:      "key-1",
		mode:        config.DomainTypeHTTP,
		errorCounts: make(map[string]int),
		done:        make(chan struct{}),
	}

	ts.recordError("key-1", config.DomainTypeHTTP, "forward_send")
	ts.recordError("missing-key", config.DomainTypeHTTP, "missing_client")

	if ts.metrics.errorCounts["forward_send"] != 1 {
		t.Errorf("expected forward_send count 1, got %d", ts.metrics.errorCounts["forward_send"])
	}
	if ts.metrics.errorCounts["missing_client"] != 1 {
		t.Errorf("expected missing_client count 1, got %d", ts.metrics.errorCounts["missing_client"])
	}
	if ts.connections[connKey].errorCounts["forward_send"] != 1 {
		t.Errorf("expected connection error counter 1, got %d", ts.connections[connKey].errorCounts["forward_send"])
	}
}

func TestBuildTunnelsResponseCopiesData(t *testing.T) {
	ts := newTestServer()
	ts.domainsByAPIKey["a-key"] = []config.DomainConfig{{APIKey: "a-key", Subdomain: "alpha", Type: config.DomainTypeHTTP}}
	connKey := buildConnectionKey("a-key", config.DomainTypeHTTP, "")
	ts.connections[connKey] = &tunnelConn{
		apiKey:           "a-key",
		mode:             config.DomainTypeHTTP,
		connectedAt:      time.Unix(10, 0),
		lastActivity:     time.Unix(20, 0),
		remoteAddr:       "127.0.0.1:5000",
		peerCommonName:   "client",
		peerSerialNumber: "deadbeef",
		done:             make(chan struct{}),
	}

	resp := ts.BuildTunnelsResponse()
	if len(resp.Tunnels) != 1 {
		t.Fatalf("expected one tunnel, got %d", len(resp.Tunnels))
	}
	if resp.Tunnels[0].KeyID != APIKeyFingerprint("a-key") {
		t.Fatalf("unexpected key id %s", resp.Tunnels[0].KeyID)
	}
	if resp.Tunnels[0].Mode != config.DomainTypeHTTP {
		t.Fatalf("expected mode http, got %s", resp.Tunnels[0].Mode)
	}
	if resp.Tunnels[0].RemoteAddr != "127.0.0.1:5000" {
		t.Fatalf("unexpected remote addr %s", resp.Tunnels[0].RemoteAddr)
	}

	// Mutating response should not affect server state.
	resp.Tunnels[0].Subdomains[0] = "mutated"
	if ts.domainsByAPIKey["a-key"][0].Subdomain != "alpha" {
		t.Fatalf("server state mutated when editing response")
	}
}

func TestBuildMetricsResponseAggregatesDomains(t *testing.T) {
	ts := newTestServer()
	ts.domainsByAPIKey["test-key"] = []config.DomainConfig{{APIKey: "test-key", Subdomain: "foo", Type: config.DomainTypeHTTP}}
	connKey := buildConnectionKey("test-key", config.DomainTypeHTTP, "")
	ts.connections[connKey] = &tunnelConn{
		apiKey:       "test-key",
		mode:         config.DomainTypeHTTP,
		connectedAt:  time.Now(),
		lastActivity: time.Now(),
		errorCounts:  map[string]int{"forward_send": 1},
		bytesIn:      42,
		bytesOut:     84,
		done:         make(chan struct{}),
	}
	ts.metrics.bytesIn["test-key"] = 42
	ts.metrics.bytesOut["test-key"] = 84
	ts.metrics.errorCounts["forward_send"] = 3
	ts.metrics.bytesInByMode[config.DomainTypeHTTP] = map[string]int64{"test-key": 42}
	ts.metrics.bytesOutByMode[config.DomainTypeHTTP] = map[string]int64{"test-key": 84}
	ts.metrics.errorCountsByMode[config.DomainTypeHTTP] = map[string]int{"forward_send": 3}

	resp := ts.BuildMetricsResponse()
	if resp.Active != 1 {
		t.Fatalf("expected active connections to be 1, got %d", resp.Active)
	}
	if resp.ActiveByMode[config.DomainTypeHTTP] != 1 {
		t.Fatalf("expected http active connections 1, got %d", resp.ActiveByMode[config.DomainTypeHTTP])
	}
	// The API key must not be used as a key in the published maps; its fingerprint is.
	keyID := APIKeyFingerprint("test-key")
	if _, leaked := resp.BytesIn["test-key"]; leaked {
		t.Fatalf("bytes_in is keyed by the plaintext API key")
	}
	if resp.BytesIn[keyID] != 42 || resp.BytesOut[keyID] != 84 {
		t.Fatalf("unexpected byte counters in response: in=%v out=%v", resp.BytesIn, resp.BytesOut)
	}
	if resp.BytesInByMode[config.DomainTypeHTTP][keyID] != 42 {
		t.Fatalf("unexpected bytes_in_by_mode values")
	}
	if resp.ErrorCountsByMode[config.DomainTypeHTTP]["forward_send"] != 3 {
		t.Fatalf("expected error_counts_by_mode forward_send=3")
	}
	if resp.ErrorCounts["forward_send"] != 3 {
		t.Fatalf("expected forward_send count 3, got %d", resp.ErrorCounts["forward_send"])
	}
	if _, leaked := resp.Domains[connKey]; leaked {
		t.Fatalf("domains is keyed by the plaintext API key")
	}
	domainStats, ok := resp.Domains[buildConnectionKey(keyID, config.DomainTypeHTTP, "")]
	if !ok {
		t.Fatalf("expected domain stats keyed by fingerprint, got %v", resp.Domains)
	}
	if domainStats.Mode != config.DomainTypeHTTP {
		t.Fatalf("expected domain mode http, got %s", domainStats.Mode)
	}
	if len(domainStats.Subdomains) != 1 || domainStats.Subdomains[0] != "foo" {
		t.Fatalf("unexpected subdomains %v", domainStats.Subdomains)
	}
	if domainStats.BytesIn != 42 || domainStats.BytesOut != 84 {
		t.Fatalf("unexpected domain byte counters")
	}
	if domainStats.ErrorCounts["forward_send"] != 1 {
		t.Fatalf("expected per-domain error count 1, got %d", domainStats.ErrorCounts["forward_send"])
	}
}

func TestStartTCPStreamRoundTrip(t *testing.T) {
	ts := newTestServer()
	apiKey := "tcp-key"
	tunnelID := "tcp-service"

	domain := config.DomainConfig{APIKey: apiKey, Subdomain: tunnelID, Type: config.DomainTypeTCP}
	ts.domainsByAPIKey[apiKey] = []config.DomainConfig{domain}
	ts.domainsBySubdomain[tunnelID] = domain

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	go ts.handleConnection(serverConn)

	hello := protocol.ClientHello{APIKey: apiKey, Mode: config.DomainTypeTCP, TunnelID: tunnelID}
	if err := protocol.WriteMessage(clientConn, hello); err != nil {
		t.Fatalf("write client hello: %v", err)
	}

	var serverHello protocol.ServerHello
	if err := protocol.ReadMessage(clientConn, &serverHello); err != nil {
		t.Fatalf("read server hello: %v", err)
	}
	if !serverHello.OK {
		t.Fatalf("expected handshake to succeed, got %#v", serverHello)
	}

	if _, ok := ts.getConnection(apiKey, config.DomainTypeTCP, tunnelID); !ok {
		t.Fatalf("expected tcp connection to be registered")
	}

	upstreamServer, upstreamClient := net.Pipe()
	defer upstreamClient.Close()
	defer upstreamServer.Close()

	streamDone := make(chan error, 1)
	go func() {
		streamDone <- ts.StartTCPStream(context.Background(), tunnelID, upstreamServer)
	}()

	startFrame, err := protocol.ReadStreamFrame(clientConn, nil)
	if err != nil {
		t.Fatalf("read stream start: %v", err)
	}
	if startFrame.Type != protocol.FrameRequestStart {
		t.Fatalf("expected request_start frame, got %s", startFrame.Type)
	}
	var startPayload protocol.RequestStartPayload
	if len(startFrame.Payload) > 0 {
		if err := json.Unmarshal(startFrame.Payload, &startPayload); err != nil {
			t.Fatalf("decode start payload: %v", err)
		}
	}
	if startPayload.Subdomain != tunnelID {
		t.Fatalf("expected subdomain %s, got %s", tunnelID, startPayload.Subdomain)
	}
	requestID := startFrame.RequestID
	if requestID == "" {
		t.Fatalf("expected non-empty request id")
	}

	if _, err := upstreamClient.Write([]byte("ping")); err != nil {
		t.Fatalf("write upstream: %v", err)
	}

	dataFrame, err := protocol.ReadStreamFrame(clientConn, nil)
	if err != nil {
		t.Fatalf("read data frame: %v", err)
	}
	if dataFrame.Type != protocol.FrameData {
		t.Fatalf("expected data frame, got %s", dataFrame.Type)
	}
	var chunk protocol.BodyChunkPayload
	if len(dataFrame.Payload) > 0 {
		if err := json.Unmarshal(dataFrame.Payload, &chunk); err != nil {
			t.Fatalf("decode data payload: %v", err)
		}
	}
	if string(chunk.Data) != "ping" {
		t.Fatalf("expected payload 'ping', got %q", string(chunk.Data))
	}

	sendPayload := protocol.BodyChunkPayload{Data: []byte("pong")}
	if err := protocol.WriteStreamFrame(clientConn, protocol.FrameData, requestID, sendPayload); err != nil {
		t.Fatalf("write client data: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(upstreamClient, buf); err != nil {
		t.Fatalf("read from upstream: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("expected upstream to receive 'pong', got %q", string(buf))
	}

	if err := protocol.WriteStreamFrame(clientConn, protocol.FrameClose, requestID, protocol.ClosePayload{Code: "done"}); err != nil {
		t.Fatalf("write close frame: %v", err)
	}

	select {
	case err := <-streamDone:
		if err != nil {
			t.Fatalf("tcp stream returned error: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("tcp stream did not complete")
	}

	// Clean up the tunnel connection
	ts.removeConnection(apiKey, config.DomainTypeTCP, tunnelID)
}

func TestStartTCPStreamPolicyViolation(t *testing.T) {
	ts := newTestServer()
	apiKey := "tcp-key"
	tunnelID := "tcp-service"
	domain := config.DomainConfig{
		APIKey:    apiKey,
		Subdomain: tunnelID,
		Type:      config.DomainTypeTCP,
		Policy: config.DomainPolicy{
			IPAllowList: []string{"203.0.113.10"},
		},
	}
	ts.domainsByAPIKey[apiKey] = []config.DomainConfig{domain}
	ts.domainsBySubdomain[tunnelID] = domain

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	go ts.handleConnection(serverConn)

	hello := protocol.ClientHello{APIKey: apiKey, Mode: config.DomainTypeTCP, TunnelID: tunnelID}
	if err := protocol.WriteMessage(clientConn, hello); err != nil {
		t.Fatalf("write client hello: %v", err)
	}

	var serverHello protocol.ServerHello
	if err := protocol.ReadMessage(clientConn, &serverHello); err != nil {
		t.Fatalf("read server hello: %v", err)
	}
	if !serverHello.OK {
		t.Fatalf("expected successful handshake, got %#v", serverHello)
	}

	upstreamServer, upstreamClient := net.Pipe()
	defer upstreamClient.Close()

	wrapped := &connWithRemote{Conn: upstreamServer, remote: staticAddr("198.51.100.25:1000")}
	errCh := make(chan error, 1)
	go func() {
		errCh <- ts.StartTCPStream(context.Background(), tunnelID, wrapped)
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatalf("expected policy violation error")
		}
		var violation *policy.ViolationError
		if !errors.As(err, &violation) {
			t.Fatalf("expected violation error, got %v", err)
		}
		if violation.Code != policy.ViolationIP {
			t.Fatalf("unexpected violation code: got %s want %s", violation.Code, policy.ViolationIP)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("timeout waiting for policy violation")
	}
}

func TestHandleConnectionHandshakeReadFailure(t *testing.T) {
	logger, buf := newCapturingLogger()
	ts := newTestServerWithLogger(logger)

	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		ts.handleConnection(serverConn)
		close(done)
	}()

	clientConn.Close()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("handleConnection did not return after handshake failure")
	}

	ts.metrics.mu.RLock()
	count := ts.metrics.errorCounts["handshake_read"]
	ts.metrics.mu.RUnlock()
	if count != 1 {
		t.Fatalf("expected handshake_read error count 1, got %d", count)
	}

	entries := decodeLogEntries(t, buf)
	if _, ok := findEvent(entries, "tunnel.handshake_read_failed"); !ok {
		t.Fatalf("expected log entry for tunnel.handshake_read_failed, got %#v", entries)
	}
}

func TestHandleConnectionRejectsUnknownAPIKey(t *testing.T) {
	logger, buf := newCapturingLogger()
	ts := newTestServerWithLogger(logger)

	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		ts.handleConnection(serverConn)
		close(done)
	}()

	if err := protocol.WriteMessage(clientConn, protocol.ClientHello{APIKey: "unknown"}); err != nil {
		t.Fatalf("failed to write client hello: %v", err)
	}

	var resp protocol.ServerHello
	if err := protocol.ReadMessage(clientConn, &resp); err != nil {
		t.Fatalf("failed to read server hello: %v", err)
	}
	if resp.OK {
		t.Fatal("expected handshake to be rejected")
	}

	clientConn.Close()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("handleConnection did not return after unknown api key")
	}

	ts.metrics.mu.RLock()
	count := ts.metrics.errorCounts["handshake_unknown_api_key"]
	ts.metrics.mu.RUnlock()
	if count != 1 {
		t.Fatalf("expected handshake_unknown_api_key error count 1, got %d", count)
	}

	if _, ok := ts.getConnection("unknown", config.DomainTypeHTTP, ""); ok {
		t.Fatal("unexpected connection stored for unknown api key")
	}

	entries := decodeLogEntries(t, buf)
	entry, ok := findEvent(entries, "tunnel.handshake_rejected")
	if !ok {
		t.Fatalf("expected handshake_rejected log entry, got %#v", entries)
	}
	if _, present := entry["api_key"]; present {
		t.Fatalf("log entry must not carry a plaintext api_key field: %#v", entry)
	}
	got, ok := entry["key_id"].(string)
	if !ok {
		t.Fatalf("expected key_id field to be string, got %#v", entry["key_id"])
	}
	if got != APIKeyFingerprint("unknown") {
		t.Fatalf("expected key_id to be the fingerprint of the key, got %v", got)
	}
}

func TestHandleConnectionRegistersConnectionAndLogs(t *testing.T) {
	logger, buf := newCapturingLogger()
	ts := newTestServerWithLogger(logger)

	domain := config.DomainConfig{
		APIKey:    "alpha",
		Subdomain: "alpha.gridbase.dev",
		LocalURL:  "http://localhost:8080",
		Type:      config.DomainTypeHTTP,
	}
	ts.domainsByAPIKey["alpha"] = []config.DomainConfig{domain}
	ts.domainsBySubdomain[domain.Subdomain] = domain

	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		ts.handleConnection(serverConn)
		close(done)
	}()

	if err := protocol.WriteMessage(clientConn, protocol.ClientHello{APIKey: "alpha"}); err != nil {
		t.Fatalf("failed to write client hello: %v", err)
	}

	var resp protocol.ServerHello
	if err := protocol.ReadMessage(clientConn, &resp); err != nil {
		t.Fatalf("failed to read server hello: %v", err)
	}
	if !resp.OK {
		t.Fatalf("expected handshake to succeed, got response %#v", resp)
	}

	clientConn.Close()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("handleConnection did not return after successful handshake")
	}

	if _, ok := ts.getConnection("alpha", config.DomainTypeHTTP, ""); !ok {
		t.Fatal("expected connection to be stored for api key alpha")
	}

	snapshot := ts.TunnelsSnapshot()
	if len(snapshot) != 1 {
		t.Fatalf("expected 1 tunnel in snapshot, got %d", len(snapshot))
	}
	if snapshot[0].APIKey != "alpha" {
		t.Fatalf("unexpected api key in snapshot: %s", snapshot[0].APIKey)
	}
	if len(snapshot[0].Subdomains) != 1 || snapshot[0].Subdomains[0] != "alpha.gridbase.dev" {
		t.Fatalf("unexpected subdomains in snapshot: %#v", snapshot[0].Subdomains)
	}

	entries := decodeLogEntries(t, buf)
	if _, ok := findEvent(entries, "tunnel.connection_registered"); !ok {
		t.Fatalf("expected connection_registered log entry, got %#v", entries)
	}
}

func decodeLogEntries(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	if buf.Len() == 0 {
		return nil
	}

	var entries []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(buf.Bytes()))
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("failed to decode log entry: %v", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("log scanner error: %v", err)
	}
	return entries
}

func findEvent(entries []map[string]any, event string) (map[string]any, bool) {
	for _, entry := range entries {
		if e, ok := entry["event"].(string); ok && e == event {
			return entry, true
		}
	}
	return nil, false
}
