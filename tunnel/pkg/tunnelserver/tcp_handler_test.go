package tunnelserver

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

func TestStartTCPStreamRequiresUpstream(t *testing.T) {
	ts := newTestServer()
	err := ts.StartTCPStream(context.Background(), "missing", nil)
	if err == nil {
		t.Fatal("expected error but got nil")
	}
	if !strings.Contains(err.Error(), "tcp upstream connection is required") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestStartTCPStreamRejectsNonTCPDomain(t *testing.T) {
	ts := newTestServer()
	subdomain := "demo.gridbase.dev"
	domain := config.DomainConfig{
		APIKey:    "demo-key",
		Subdomain: subdomain,
		Type:      config.DomainTypeHTTP,
	}
	ts.domainsBySubdomain[subdomain] = domain

	upstream, peer := net.Pipe()
	defer upstream.Close()
	defer peer.Close()

	err := ts.StartTCPStream(context.Background(), subdomain, upstream)
	if err == nil {
		t.Fatal("expected error for non tcp domain")
	}
	if !strings.Contains(err.Error(), "not configured for tcp mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStartTCPStreamMissingClientRecordsError(t *testing.T) {
	ts := newTestServer()
	tunnelID := "tcp-demo"
	apiKey := "tcp-key"
	domain := config.DomainConfig{
		APIKey:    apiKey,
		Subdomain: tunnelID,
		Type:      config.DomainTypeTCP,
	}
	ts.domainsBySubdomain[tunnelID] = domain
	ts.domainsByAPIKey[apiKey] = []config.DomainConfig{domain}

	upstream, peer := net.Pipe()
	defer upstream.Close()
	defer peer.Close()

	err := ts.StartTCPStream(context.Background(), tunnelID, upstream)
	if !errors.Is(err, ErrNoTunnelClient) {
		t.Fatalf("expected ErrNoTunnelClient, got %v", err)
	}

	ts.metrics.mu.RLock()
	count := ts.metrics.errorCounts["missing_tcp_client"]
	modeCount := ts.metrics.errorCountsByMode[config.DomainTypeTCP]["missing_tcp_client"]
	ts.metrics.mu.RUnlock()

	if count != 1 {
		t.Fatalf("expected missing_tcp_client error count 1, got %d", count)
	}
	if modeCount != 1 {
		t.Fatalf("expected missing_tcp_client mode error count 1, got %d", modeCount)
	}
}

func TestPipeTCPServerToClientForwardsData(t *testing.T) {
	ts := newTestServer()
	apiKey := "tcp-key"
	tunnelID := "tcp-service"

	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	conn := &tunnelConn{
		apiKey:      apiKey,
		mode:        config.DomainTypeTCP,
		tunnelID:    tunnelID,
		conn:        serverSide,
		errorCounts: make(map[string]int),
		streams:     make(map[string]*tcpStream),
		done:        make(chan struct{}),
	}

	upstreamServer, upstreamClient := net.Pipe()
	defer upstreamServer.Close()
	defer upstreamClient.Close()

	stream := newTCPStream("stream-1", apiKey, tunnelID, upstreamServer)
	conn.addStream(stream)

	done := make(chan struct{})
	go func() {
		ts.pipeTCPServerToClient(conn, stream)
		close(done)
	}()

	if _, err := upstreamClient.Write([]byte("hello")); err != nil {
		t.Fatalf("write upstream: %v", err)
	}

	var dataPayload protocol.BodyChunkPayload
	frame, err := protocol.ReadStreamFrame(clientSide, &dataPayload)
	if err != nil {
		t.Fatalf("read data frame: %v", err)
	}
	if frame.Type != protocol.FrameData {
		t.Fatalf("expected FrameData, got %s", frame.Type)
	}
	if string(dataPayload.Data) != "hello" {
		t.Fatalf("unexpected payload %q", string(dataPayload.Data))
	}

	upstreamClient.Close()

	var closePayload protocol.ClosePayload
	frame, err = protocol.ReadStreamFrame(clientSide, &closePayload)
	if err != nil {
		t.Fatalf("read close frame: %v", err)
	}
	if frame.Type != protocol.FrameClose {
		t.Fatalf("expected FrameClose, got %s", frame.Type)
	}
	if closePayload.Code != "eof" {
		t.Fatalf("unexpected close payload: %#v", closePayload)
	}

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("pipeTCPServerToClient did not finish")
	}

	ts.metrics.mu.RLock()
	bytesIn := ts.metrics.bytesIn[apiKey]
	modeBytes := ts.metrics.bytesInByMode[config.DomainTypeTCP][apiKey]
	ts.metrics.mu.RUnlock()

	if bytesIn != int64(len("hello")) {
		t.Fatalf("expected bytesIn to equal %d, got %d", len("hello"), bytesIn)
	}
	if modeBytes != int64(len("hello")) {
		t.Fatalf("expected bytesInByMode to equal %d, got %d", len("hello"), modeBytes)
	}
}
