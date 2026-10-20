package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/admin"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/tunnelserver"
	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

type memoryAddr string

func (m memoryAddr) Network() string { return "memory" }
func (m memoryAddr) String() string  { return string(m) }

type memoryListener struct {
	connCh  chan net.Conn
	closeCh chan struct{}
}

func newMemoryListener() *memoryListener {
	return &memoryListener{
		connCh:  make(chan net.Conn, 1),
		closeCh: make(chan struct{}),
	}
}

func (l *memoryListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connCh:
		return conn, nil
	case <-l.closeCh:
		return nil, net.ErrClosed
	}
}

func (l *memoryListener) Close() error {
	select {
	case <-l.closeCh:
	default:
		close(l.closeCh)
	}
	return nil
}

func (l *memoryListener) Addr() net.Addr {
	return memoryAddr("memory-listener")
}

func (l *memoryListener) Dial() net.Conn {
	serverConn, clientConn := net.Pipe()
	select {
	case l.connCh <- serverConn:
	case <-l.closeCh:
		_ = serverConn.Close()
		_ = clientConn.Close()
		return nil
	}
	return clientConn
}

func discardLogger(name string) *logging.Logger {
	return logging.New(name, logging.WithWriter(io.Discard))
}

func TestTCPIntegrationRoundTrip(t *testing.T) {
	listener := newMemoryListener()
	cfg := &config.Config{
		TLS:        config.TLSConfig{CertPath: "testdata/server.crt", KeyPath: "testdata/server.key", ClientCA: "testdata/ca.crt"},
		Tunnel:     config.TunnelConfig{Port: 9443},
		HTTP:       config.HTTPConfig{Port: 9080},
		BaseDomain: "gridbase.test",
		Domains: []config.DomainConfig{
			{
				APIKey:    "tcp-key",
				Subdomain: "tcp-service",
				Type:      config.DomainTypeTCP,
				LocalHost: "127.0.0.1",
				LocalPort: 6600,
			},
		},
		Admin: config.AdminConfig{
			BearerToken: "integration-test-admin-token-0123456789",
			Host:        "admin.gridbase.test",
		},
	}

	ts, err := tunnelserver.NewTunnelServerWithListener(cfg, discardLogger("tunnel-integration"), listener)
	if err != nil {
		t.Fatalf("failed to create tunnel server: %v", err)
	}
	t.Cleanup(func() {
		_ = ts.Close()
		_ = listener.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	acceptErr := make(chan error, 1)
	go func() {
		acceptErr <- ts.AcceptLoop(ctx)
	}()

	clientConn := listener.Dial()
	if clientConn == nil {
		t.Fatal("memory listener returned nil client connection")
	}
	defer clientConn.Close()

	clientHello := protocol.ClientHello{
		APIKey:   "tcp-key",
		Mode:     config.DomainTypeTCP,
		TunnelID: "tcp-service",
	}
	if err := protocol.WriteMessage(clientConn, clientHello); err != nil {
		t.Fatalf("write client hello: %v", err)
	}

	var serverHello protocol.ServerHello
	if err := protocol.ReadMessage(clientConn, &serverHello); err != nil {
		t.Fatalf("read server hello: %v", err)
	}
	if !serverHello.OK {
		t.Fatalf("expected success server hello, got %#v", serverHello)
	}
	if serverHello.Mode != config.DomainTypeTCP {
		t.Fatalf("expected tcp mode, got %s", serverHello.Mode)
	}

	upstreamServer, upstreamClient := net.Pipe()
	defer upstreamClient.Close()

	streamErr := make(chan error, 1)
	go func() {
		streamErr <- ts.StartTCPStream(context.Background(), "tcp-service", upstreamServer)
	}()

	var startPayload protocol.RequestStartPayload
	startFrame, err := protocol.ReadStreamFrame(clientConn, &startPayload)
	if err != nil {
		t.Fatalf("read request start frame: %v", err)
	}
	if startFrame.Type != protocol.FrameRequestStart {
		t.Fatalf("expected request_start frame, got %s", startFrame.Type)
	}
	if startPayload.Subdomain != "tcp-service" {
		t.Fatalf("unexpected subdomain %s", startPayload.Subdomain)
	}
	streamID := startFrame.RequestID
	if streamID == "" {
		t.Fatalf("expected non-empty request id")
	}

	if _, err := upstreamClient.Write([]byte("hello")); err != nil {
		t.Fatalf("write upstream: %v", err)
	}

	var dataPayload protocol.BodyChunkPayload
	dataFrame, err := protocol.ReadStreamFrame(clientConn, &dataPayload)
	if err != nil {
		t.Fatalf("read data frame: %v", err)
	}
	if dataFrame.Type != protocol.FrameData {
		t.Fatalf("expected data frame, got %s", dataFrame.Type)
	}
	if string(dataPayload.Data) != "hello" {
		t.Fatalf("unexpected payload %q", string(dataPayload.Data))
	}

	if err := protocol.WriteStreamFrame(clientConn, protocol.FrameData, streamID, protocol.BodyChunkPayload{Data: []byte("pong")}); err != nil {
		t.Fatalf("send data frame to server: %v", err)
	}

	buff := make([]byte, len("pong"))
	if _, err := io.ReadFull(upstreamClient, buff); err != nil {
		t.Fatalf("read upstream response: %v", err)
	}
	if string(buff) != "pong" {
		t.Fatalf("unexpected upstream bytes %q", string(buff))
	}

	if err := upstreamClient.Close(); err != nil {
		t.Fatalf("close upstream client: %v", err)
	}

	var closePayload protocol.ClosePayload
	closeFrame, err := protocol.ReadStreamFrame(clientConn, &closePayload)
	if err != nil {
		t.Fatalf("read close frame: %v", err)
	}
	if closeFrame.Type != protocol.FrameClose {
		t.Fatalf("expected close frame, got %s", closeFrame.Type)
	}
	if closePayload.Code != "eof" {
		t.Fatalf("unexpected close payload: %#v", closePayload)
	}

	if err := protocol.WriteStreamFrame(clientConn, protocol.FrameClose, streamID, protocol.ClosePayload{Code: "client_done"}); err != nil {
		t.Fatalf("send client close frame: %v", err)
	}

	select {
	case err := <-streamErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("StartTCPStream returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for stream completion")
	}

	cancel()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	select {
	case <-acceptErr:
	case <-time.After(2 * time.Second):
		t.Fatal("accept loop did not exit")
	}
}

func TestAdminAPIModeSnapshots(t *testing.T) {
	listener := newMemoryListener()
	cfg := &config.Config{
		TLS:        config.TLSConfig{CertPath: "testdata/server.crt", KeyPath: "testdata/server.key", ClientCA: "testdata/ca.crt"},
		Tunnel:     config.TunnelConfig{Port: 9443},
		HTTP:       config.HTTPConfig{Port: 9080},
		BaseDomain: "gridbase.test",
		Domains: []config.DomainConfig{
			{
				APIKey:    "http-key",
				Subdomain: "web",
				Type:      config.DomainTypeHTTP,
				LocalURL:  "http://localhost:8080",
			},
			{
				APIKey:    "tcp-key",
				Subdomain: "tcp-service",
				Type:      config.DomainTypeTCP,
				LocalHost: "127.0.0.1",
				LocalPort: 6500,
			},
		},
		Admin: config.AdminConfig{
			BearerToken: "integration-test-admin-token-0123456789",
			Host:        "admin.gridbase.test",
		},
	}

	ts, err := tunnelserver.NewTunnelServerWithListener(cfg, discardLogger("tunnel-admin"), listener)
	if err != nil {
		t.Fatalf("failed to create tunnel server: %v", err)
	}
	t.Cleanup(func() {
		_ = ts.Close()
		_ = listener.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = ts.AcceptLoop(ctx)
	}()

	httpConn := listener.Dial()
	if httpConn == nil {
		t.Fatal("failed to dial http connection")
	}
	t.Cleanup(func() { _ = httpConn.Close() })

	if err := protocol.WriteMessage(httpConn, protocol.ClientHello{APIKey: "http-key"}); err != nil {
		t.Fatalf("write http client hello: %v", err)
	}
	var httpHello protocol.ServerHello
	if err := protocol.ReadMessage(httpConn, &httpHello); err != nil {
		t.Fatalf("read http server hello: %v", err)
	}
	if !httpHello.OK {
		t.Fatalf("http handshake failed: %#v", httpHello)
	}

	tcpConn := listener.Dial()
	if tcpConn == nil {
		t.Fatal("failed to dial tcp connection")
	}
	t.Cleanup(func() { _ = tcpConn.Close() })

	if err := protocol.WriteMessage(tcpConn, protocol.ClientHello{
		APIKey:   "tcp-key",
		Mode:     config.DomainTypeTCP,
		TunnelID: "tcp-service",
	}); err != nil {
		t.Fatalf("write tcp client hello: %v", err)
	}
	var tcpHello protocol.ServerHello
	if err := protocol.ReadMessage(tcpConn, &tcpHello); err != nil {
		t.Fatalf("read tcp server hello: %v", err)
	}
	if !tcpHello.OK {
		t.Fatalf("tcp handshake failed: %#v", tcpHello)
	}

	manager := config.NewManager(cfg)
	router := admin.NewRouter(ts, manager, discardLogger("admin"))

	req := httptest.NewRequest("GET", "https://admin.gridbase.test/api/tunnels", nil)
	req.Host = "admin.gridbase.test"
	req.Header.Set("Authorization", "Bearer "+cfg.Admin.BearerToken)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("expected status 200, got %d", rr.Code)
	}

	var tunnelsResp tunnelserver.TunnelsAPIResponse
	if err := json.NewDecoder(rr.Body).Decode(&tunnelsResp); err != nil {
		t.Fatalf("decode tunnels response: %v", err)
	}

	if len(tunnelsResp.Tunnels) < 2 {
		t.Fatalf("expected at least 2 tunnels, got %d", len(tunnelsResp.Tunnels))
	}

	var tcpFound bool
	for _, tunnel := range tunnelsResp.Tunnels {
		if tunnel.Mode == config.DomainTypeTCP {
			tcpFound = true
			if tunnel.TunnelID != "tcp-service" {
				t.Fatalf("unexpected tunnel id %s", tunnel.TunnelID)
			}
			if tunnel.LocalHost != "127.0.0.1" || tunnel.LocalPort != 6500 {
				t.Fatalf("unexpected tcp tunnel local endpoint: host=%s port=%d", tunnel.LocalHost, tunnel.LocalPort)
			}
		}
		if tunnel.Mode == config.DomainTypeHTTP && len(tunnel.Subdomains) == 0 {
			t.Fatalf("http tunnel missing subdomains: %+v", tunnel)
		}
	}
	if !tcpFound {
		t.Fatal("expected tcp tunnel entry in /api/tunnels response")
	}

	reqMetrics := httptest.NewRequest("GET", "https://admin.gridbase.test/api/metrics", nil)
	reqMetrics.Host = "admin.gridbase.test"
	reqMetrics.Header.Set("Authorization", "Bearer "+cfg.Admin.BearerToken)
	rrMetrics := httptest.NewRecorder()
	router.ServeHTTP(rrMetrics, reqMetrics)

	if rrMetrics.Code != 200 {
		t.Fatalf("expected status 200 for metrics, got %d", rrMetrics.Code)
	}

	var metricsResp tunnelserver.MetricsAPIResponse
	if err := json.NewDecoder(rrMetrics.Body).Decode(&metricsResp); err != nil {
		t.Fatalf("decode metrics response: %v", err)
	}

	if metricsResp.ActiveByMode[config.DomainTypeTCP] != 1 {
		t.Fatalf("expected tcp active count 1, got %d", metricsResp.ActiveByMode[config.DomainTypeTCP])
	}
	if metricsResp.ActiveByMode[config.DomainTypeHTTP] != 1 {
		t.Fatalf("expected http active count 1, got %d", metricsResp.ActiveByMode[config.DomainTypeHTTP])
	}

	cancel()
	_ = listener.Close()
}
