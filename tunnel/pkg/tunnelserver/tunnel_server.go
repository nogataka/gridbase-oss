package tunnelserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/policy"
	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

// ErrNoTunnelClient はサブドメインに対応するトンネルが存在しない場合のエラー。
var ErrNoTunnelClient = errors.New("tunnel client not connected")

// ErrResponseTooLarge is returned when a tunnel client's response body exceeds
// the configured limit. The tunnel connection is dropped afterwards because the
// remaining response frames are still in flight.
var ErrResponseTooLarge = errors.New("tunnel response body exceeds size limit")

type metricsState struct {
	mu                sync.RWMutex
	bytesIn           map[string]int64
	bytesOut          map[string]int64
	bytesInByMode     map[string]map[string]int64
	bytesOutByMode    map[string]map[string]int64
	errorCounts       map[string]int
	errorCountsByMode map[string]map[string]int
	lastReload        time.Time
	certExpiry        time.Time
}

type tunnelConn struct {
	apiKey           string
	mode             string
	tunnelID         string
	conn             net.Conn
	frameLimit       int64 // max protocol frame size read from conn (0 = protocol default)
	mu               sync.Mutex
	connectedAt      time.Time
	lastActivity     time.Time
	remoteAddr       string
	peerCommonName   string
	peerSerialNumber string
	bytesIn          int64
	bytesOut         int64
	errorCounts      map[string]int
	streams          map[string]*tcpStream
	done             chan struct{}
	closeOnce        sync.Once
}

func (tc *tunnelConn) close() {
	tc.closeOnce.Do(func() {
		close(tc.done)
	})
	_ = tc.conn.Close()

	tc.mu.Lock()
	streams := tc.streams
	tc.streams = nil
	tc.mu.Unlock()

	for _, stream := range streams {
		if stream != nil {
			stream.close(errors.New("tunnel connection closed"))
		}
	}
}

func normaliseMode(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return config.DomainTypeHTTP
	}
	return mode
}

func buildConnectionKey(apiKey, mode, tunnelID string) string {
	apiKey = strings.TrimSpace(apiKey)
	mode = normaliseMode(mode)
	tunnelID = strings.TrimSpace(tunnelID)
	if mode == config.DomainTypeTCP {
		if tunnelID != "" {
			return fmt.Sprintf("%s|%s|%s", apiKey, mode, tunnelID)
		}
		return fmt.Sprintf("%s|%s", apiKey, mode)
	}
	return fmt.Sprintf("%s|%s", apiKey, mode)
}

func (tc *tunnelConn) addStream(stream *tcpStream) {
	if stream == nil {
		return
	}
	tc.mu.Lock()
	if tc.streams == nil {
		tc.streams = make(map[string]*tcpStream)
	}
	tc.streams[stream.id] = stream
	tc.mu.Unlock()
}

func (tc *tunnelConn) removeStream(id string) *tcpStream {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.streams == nil {
		return nil
	}
	stream := tc.streams[id]
	delete(tc.streams, id)
	return stream
}

func (tc *tunnelConn) loadStream(id string) (*tcpStream, bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if tc.streams == nil {
		return nil, false
	}
	stream, ok := tc.streams[id]
	return stream, ok
}

func (tc *tunnelConn) sendFrame(frameType protocol.FrameType, requestID string, payload any) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	if err := protocol.WriteStreamFrame(tc.conn, frameType, requestID, payload); err != nil {
		return err
	}
	tc.lastActivity = time.Now()
	return nil
}

func (tc *tunnelConn) readFrame() (protocol.StreamFrame, error) {
	return protocol.ReadStreamFrameLimit(tc.conn, nil, tc.frameLimit)
}

func (tc *tunnelConn) incrementBytes(inDelta, outDelta int64) {
	tc.mu.Lock()
	if inDelta > 0 {
		tc.bytesIn += inDelta
	}
	if outDelta > 0 {
		tc.bytesOut += outDelta
	}
	tc.lastActivity = time.Now()
	tc.mu.Unlock()
}

// send forwards one request and collects the response. maxResponseBytes caps the
// response body (<= 0 means the package default).
func (tc *tunnelConn) send(ctx context.Context, payload *protocol.RequestPayload, maxResponseBytes int64) (*protocol.ResponsePayload, error) {
	if maxResponseBytes <= 0 {
		maxResponseBytes = config.DefaultMaxBodyBytes
	}
	tc.mu.Lock()
	defer tc.mu.Unlock()

	deadline := time.Now().Add(30 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok {
		deadline = ctxDeadline
	}
	if err := tc.conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}
	defer tc.conn.SetDeadline(time.Time{})

	// Random and unrelated to the API key: request ids travel in every frame and
	// can surface in errors.
	requestID := newRequestID()

	startPayload := protocol.RequestStartPayload{
		Method:    payload.Method,
		Path:      payload.Path,
		Target:    payload.Target,
		Header:    payload.Header,
		Subdomain: payload.Subdomain,
	}
	if err := protocol.WriteStreamFrame(tc.conn, protocol.FrameRequestStart, requestID, startPayload); err != nil {
		return nil, fmt.Errorf("write request start: %w", err)
	}

	if len(payload.Body) > 0 {
		for offset := 0; offset < len(payload.Body); {
			end := offset + protocol.StreamChunkSize
			if end > len(payload.Body) {
				end = len(payload.Body)
			}
			chunk := payload.Body[offset:end]
			if err := protocol.WriteStreamFrame(tc.conn, protocol.FrameRequestBody, requestID, protocol.BodyChunkPayload{Data: chunk}); err != nil {
				return nil, fmt.Errorf("write request body: %w", err)
			}
			tc.bytesIn += int64(len(chunk))
			offset = end
		}
	}

	if err := protocol.WriteStreamFrame(tc.conn, protocol.FrameRequestEnd, requestID, nil); err != nil {
		return nil, fmt.Errorf("write request end: %w", err)
	}

	response := &protocol.ResponsePayload{
		Header: make(map[string][]string),
	}

	for {
		frame, err := tc.readFrame()
		if err != nil {
			return nil, fmt.Errorf("read response frame: %w", err)
		}

		if frame.RequestID != requestID {
			return nil, errors.New("mismatched response request id")
		}

		switch frame.Type {
		case protocol.FrameResponseStart:
			var respStart protocol.ResponseStartPayload
			if len(frame.Payload) > 0 {
				if err := json.Unmarshal(frame.Payload, &respStart); err != nil {
					return nil, fmt.Errorf("decode response start: %w", err)
				}
			}
			response.StatusCode = respStart.StatusCode
			if respStart.Header != nil {
				response.Header = respStart.Header
			}
			response.Error = respStart.Error
		case protocol.FrameResponseBody:
			var chunk protocol.BodyChunkPayload
			if len(frame.Payload) > 0 {
				if err := json.Unmarshal(frame.Payload, &chunk); err != nil {
					return nil, fmt.Errorf("decode response body: %w", err)
				}
			}
			if int64(len(response.Body))+int64(len(chunk.Data)) > maxResponseBytes {
				return nil, fmt.Errorf("%w (limit %d bytes)", ErrResponseTooLarge, maxResponseBytes)
			}
			response.Body = append(response.Body, chunk.Data...)
			tc.bytesOut += int64(len(chunk.Data))
		case protocol.FrameResponseEnd:
			now := time.Now()
			tc.lastActivity = now
			return response, nil
		case protocol.FrameError:
			var errPayload protocol.ErrorPayload
			if len(frame.Payload) > 0 {
				if err := json.Unmarshal(frame.Payload, &errPayload); err != nil {
					return nil, fmt.Errorf("decode error payload: %w", err)
				}
			}
			if errPayload.Message == "" {
				errPayload.Message = "unknown tunnel error"
			}
			return nil, errors.New(errPayload.Message)
		default:
			return nil, fmt.Errorf("unexpected frame type %s", frame.Type)
		}
	}
}

// TunnelServer は TLS トンネル接続と HTTP 転送を管理する。
type TunnelServer struct {
	streamCounter      atomic.Uint64
	cfg                *config.Config
	cfgMu              sync.RWMutex
	logger             *logging.Logger
	events             *eventRecorder
	tlsListener        net.Listener
	connections        map[string]*tunnelConn
	connectionsMu      sync.RWMutex
	domainsBySubdomain map[string]config.DomainConfig
	domainsByAPIKey    map[string][]config.DomainConfig
	domainsMu          sync.RWMutex
	policyEngine       *policy.Engine
	metrics            metricsState
}

func newTunnelServerCore(cfg *config.Config, logger *logging.Logger) *TunnelServer {
	if logger == nil {
		logger = logging.New("tunnel-server")
	}

	ts := &TunnelServer{
		cfg:                cfg,
		logger:             logger,
		events:             newEventRecorder(logger),
		connections:        make(map[string]*tunnelConn),
		domainsBySubdomain: make(map[string]config.DomainConfig),
		domainsByAPIKey:    make(map[string][]config.DomainConfig),
		policyEngine:       policy.NewEngine(),
	}

	ts.metrics.bytesIn = make(map[string]int64)
	ts.metrics.bytesOut = make(map[string]int64)
	ts.metrics.bytesInByMode = make(map[string]map[string]int64)
	ts.metrics.bytesOutByMode = make(map[string]map[string]int64)
	ts.metrics.errorCounts = make(map[string]int)
	ts.metrics.errorCountsByMode = make(map[string]map[string]int)
	ts.metrics.lastReload = time.Now()

	ts.policyEngine.SetTrustedProxies(cfg.HTTP.TrustedProxies)

	ts.domainsMu.Lock()
	for _, domain := range cfg.Domains {
		ts.domainsBySubdomain[domain.Subdomain] = domain
		ts.domainsByAPIKey[domain.APIKey] = append(ts.domainsByAPIKey[domain.APIKey], domain)
	}
	ts.domainsMu.Unlock()
	return ts
}

// NewTunnelServer initialises the tunnel listener. With tls.cert/tls.key set it
// serves TLS (optionally with mutual TLS when tls.client_ca is set); otherwise it
// listens on plain TCP.
func NewTunnelServer(cfg *config.Config, logger *logging.Logger) (*TunnelServer, error) {
	ts := newTunnelServerCore(cfg, logger)
	address := fmt.Sprintf(":%d", cfg.Tunnel.Port)

	if !ts.tlsEnabled() {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			return nil, fmt.Errorf("listen tcp %s: %w", address, err)
		}
		ts.events.Warn("tunnel.listener_plaintext", "tunnel listener started WITHOUT TLS (tls.cert/tls.key not set); use only on trusted networks or behind a TLS terminator", nil, logging.Fields{
			"address": address,
		})
		ts.tlsListener = listener
		return ts, nil
	}

	tlsConfig, certExpiry, err := ts.buildTLSConfig()
	if err != nil {
		return nil, err
	}
	ts.metrics.mu.Lock()
	ts.metrics.certExpiry = certExpiry
	ts.metrics.mu.Unlock()

	listener, err := tls.Listen("tcp", address, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("listen tls %s: %w", address, err)
	}

	ts.events.Info("tunnel.listener_started", "TLS tunnel listener started", logging.Fields{
		"address":     address,
		"client_auth": tlsConfig.ClientAuth.String(),
	})
	ts.tlsListener = listener
	return ts, nil
}

func (ts *TunnelServer) tlsEnabled() bool {
	return strings.TrimSpace(ts.cfg.TLS.CertPath) != "" || strings.TrimSpace(ts.cfg.TLS.KeyPath) != ""
}

// NewTunnelServerWithListener はテスト向けに任意の net.Listener を注入してトンネルサーバーを組み立てる。
func NewTunnelServerWithListener(cfg *config.Config, logger *logging.Logger, listener net.Listener) (*TunnelServer, error) {
	if listener == nil {
		return nil, errors.New("tunnelserver: listener is required")
	}

	ts := newTunnelServerCore(cfg, logger)
	ts.metrics.mu.Lock()
	ts.metrics.certExpiry = time.Now()
	ts.metrics.mu.Unlock()
	ts.tlsListener = listener

	address := listener.Addr().String()
	ts.events.Info("tunnel.listener_started", "custom listener attached", logging.Fields{
		"address": address,
		"source":  "in-memory",
	})
	return ts, nil
}

func (ts *TunnelServer) buildTLSConfig() (*tls.Config, time.Time, error) {
	cert, err := tls.LoadX509KeyPair(ts.cfg.TLS.CertPath, ts.cfg.TLS.KeyPath)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("load server cert/key: %w", err)
	}

	var certExpiry time.Time
	if len(cert.Certificate) > 0 {
		if leaf, parseErr := x509.ParseCertificate(cert.Certificate[0]); parseErr == nil {
			cert.Leaf = leaf
			certExpiry = leaf.NotAfter
		} else {
			ts.events.Warn("tunnel.certificate_parse_failed", "failed to parse server certificate leaf", parseErr, nil)
		}
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.NoClientCert,
		MinVersion:   tls.VersionTLS12,
	}

	// mTLS is optional: only verify client certificates when a client CA is configured.
	if strings.TrimSpace(ts.cfg.TLS.ClientCA) != "" {
		caBytes, err := os.ReadFile(ts.cfg.TLS.ClientCA)
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("read client CA: %w", err)
		}

		clientCAPool := x509.NewCertPool()
		if ok := clientCAPool.AppendCertsFromPEM(caBytes); !ok {
			return nil, time.Time{}, errors.New("failed to append client CA certs")
		}
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		tlsConfig.ClientCAs = clientCAPool
	}

	return tlsConfig, certExpiry, nil
}

// Close はリスナーとアクティブな接続を閉じる。
func (ts *TunnelServer) Close() error {
	if ts.tlsListener != nil {
		_ = ts.tlsListener.Close()
	}

	ts.connectionsMu.Lock()
	defer ts.connectionsMu.Unlock()
	for key, conn := range ts.connections {
		ts.events.Info("tunnel.connection_closing", "closing connection", ts.events.withTunnelContext(conn.mode, conn.tunnelID, logging.Fields{
			"key_id": APIKeyFingerprint(conn.apiKey),
		}))
		conn.close()
		delete(ts.connections, key)
	}
	return nil
}

// ListenerAddr returns the active TLS listener address. Primarily used by tests.
func (ts *TunnelServer) ListenerAddr() net.Addr {
	if ts == nil {
		return nil
	}
	listener := ts.tlsListener
	if listener == nil {
		return nil
	}
	return listener.Addr()
}

// AcceptLoop は TLS 接続を受け付け続ける。
func (ts *TunnelServer) AcceptLoop(ctx context.Context) error {
	for {
		conn, err := ts.tlsListener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			if netErr, ok := err.(net.Error); ok && netErr.Temporary() {
				ts.events.Warn("tunnel.accept_temporary_error", "temporary accept error", err, nil)
				time.Sleep(200 * time.Millisecond)
				continue
			}
			ts.events.Error("tunnel.accept_failed", "TLS accept error", err, nil)
			ts.recordError("", "", "tls_accept")
			time.Sleep(200 * time.Millisecond)
			continue
		}
		go ts.handleConnection(conn)
	}
}

func (ts *TunnelServer) handleConnection(rawConn net.Conn) {
	connID := rawConn.RemoteAddr().String()
	ts.events.Info("tunnel.connection_incoming", "incoming tunnel connection", logging.Fields{
		"remote_addr": connID,
	})

	defer func() {
		if r := recover(); r != nil {
			ts.events.Error("tunnel.connection_panic", "panic in handleConnection", fmt.Errorf("%v", r), logging.Fields{
				"remote_addr": connID,
			})
		}
	}()

	// The whole TLS + hello exchange shares one deadline so that a peer which
	// connects and then stays silent (or trickles bytes) cannot hold the goroutine
	// and file descriptor. The deadline is lifted once the client is registered.
	if err := rawConn.SetDeadline(time.Now().Add(ts.handshakeTimeout())); err != nil {
		ts.events.Warn("tunnel.handshake_deadline_failed", "could not set handshake deadline", err, logging.Fields{
			"remote_addr": connID,
		})
	}

	var peerCommonName, peerSerial string
	if tlsConn, ok := rawConn.(*tls.Conn); ok {
		// Complete the TLS handshake under the deadline so the peer certificate is known.
		if err := tlsConn.Handshake(); err != nil {
			ts.events.Warn("tunnel.tls_handshake_failed", "TLS handshake failed", err, logging.Fields{
				"remote_addr": connID,
			})
			ts.recordError("", "", "tls_handshake")
			_ = rawConn.Close()
			return
		}
		state := tlsConn.ConnectionState()
		if len(state.PeerCertificates) > 0 {
			cert := state.PeerCertificates[0]
			peerCommonName = cert.Subject.CommonName
			if peerCommonName == "" {
				peerCommonName = cert.Subject.String()
			}
			if cert.SerialNumber != nil {
				peerSerial = cert.SerialNumber.Text(16)
			}
		}
	}

	// The hello is read before authentication, so it gets a small, fixed bound.
	var hello protocol.ClientHello
	if err := protocol.ReadMessageLimit(rawConn, &hello, protocol.MaxHandshakeBytes); err != nil {
		ts.events.Error("tunnel.handshake_read_failed", "handshake read failed", err, logging.Fields{
			"remote_addr": connID,
		})
		ts.recordError("", "", "handshake_read")
		_ = rawConn.Close()
		return
	}

	mode := normaliseMode(hello.Mode)
	tunnelID := strings.TrimSpace(hello.TunnelID)

	apiKey := strings.TrimSpace(hello.APIKey)
	if apiKey == "" {
		ts.events.Warn("tunnel.handshake_rejected", "handshake rejected: empty api key", nil, ts.events.withTunnelContext(mode, tunnelID, logging.Fields{
			"remote_addr": connID,
		}))
		ts.recordError("", mode, "handshake_empty_api_key")
		_ = protocol.WriteMessage(rawConn, protocol.ServerHello{OK: false, Message: "empty api key"})
		_ = rawConn.Close()
		return
	}

	if mode != config.DomainTypeHTTP && mode != config.DomainTypeTCP {
		ts.events.Warn("tunnel.handshake_rejected", "handshake rejected: invalid mode", nil, ts.events.withTunnelContext(mode, tunnelID, logging.Fields{
			"remote_addr": connID,
			"key_id":      APIKeyFingerprint(apiKey),
		}))
		ts.recordError(apiKey, mode, "handshake_invalid_mode")
		_ = protocol.WriteMessage(rawConn, protocol.ServerHello{OK: false, Message: "invalid mode"})
		_ = rawConn.Close()
		return
	}

	ts.domainsMu.RLock()
	domainEntries, ok := ts.domainsByAPIKey[apiKey]
	ts.domainsMu.RUnlock()
	if !ok {
		ts.events.Warn("tunnel.handshake_rejected", "handshake rejected: unknown api key", nil, ts.events.withTunnelContext(mode, tunnelID, logging.Fields{
			"remote_addr": connID,
			"key_id":      APIKeyFingerprint(apiKey),
		}))
		ts.recordError(apiKey, mode, "handshake_unknown_api_key")
		_ = protocol.WriteMessage(rawConn, protocol.ServerHello{OK: false, Message: "unknown api key"})
		_ = rawConn.Close()
		return
	}

	var permittedDomains []config.DomainConfig
	for _, d := range domainEntries {
		domainMode := normaliseMode(d.Type)
		if domainMode == mode {
			permittedDomains = append(permittedDomains, d)
		}
	}

	if len(permittedDomains) == 0 {
		ts.events.Warn("tunnel.handshake_rejected", "handshake rejected: mode not permitted for api key", nil, ts.events.withTunnelContext(mode, tunnelID, logging.Fields{
			"remote_addr": connID,
			"key_id":      APIKeyFingerprint(apiKey),
		}))
		ts.recordError(apiKey, mode, "handshake_mode_mismatch")
		_ = protocol.WriteMessage(rawConn, protocol.ServerHello{OK: false, Message: "mode mismatch"})
		_ = rawConn.Close()
		return
	}

	if mode == config.DomainTypeTCP {
		if tunnelID == "" && len(permittedDomains) == 1 {
			tunnelID = permittedDomains[0].Subdomain
		}
		if tunnelID == "" {
			ts.events.Warn("tunnel.handshake_rejected", "handshake rejected: missing tunnel id for tcp mode", nil, ts.events.withTunnelContext(mode, tunnelID, logging.Fields{
				"remote_addr": connID,
				"key_id":      APIKeyFingerprint(apiKey),
			}))
			ts.recordError(apiKey, mode, "handshake_missing_tunnel_id")
			_ = protocol.WriteMessage(rawConn, protocol.ServerHello{OK: false, Message: "missing tunnel id"})
			_ = rawConn.Close()
			return
		}

		var tunnelAllowed bool
		for _, d := range permittedDomains {
			if strings.EqualFold(d.Subdomain, tunnelID) {
				tunnelAllowed = true
				break
			}
		}
		if !tunnelAllowed {
			ts.events.Warn("tunnel.handshake_rejected", "handshake rejected: tunnel id not registered", nil, ts.events.withTunnelContext(mode, tunnelID, logging.Fields{
				"remote_addr": connID,
				"key_id":      APIKeyFingerprint(apiKey),
			}))
			ts.recordError(apiKey, mode, "handshake_unknown_tunnel")
			_ = protocol.WriteMessage(rawConn, protocol.ServerHello{OK: false, Message: "unknown tunnel id"})
			_ = rawConn.Close()
			return
		}
	} else {
		tunnelID = ""
	}

	now := time.Now()
	tConn := &tunnelConn{
		apiKey:           apiKey,
		mode:             mode,
		tunnelID:         tunnelID,
		conn:             rawConn,
		frameLimit:       ts.frameLimit(),
		connectedAt:      now,
		lastActivity:     now,
		remoteAddr:       connID,
		peerCommonName:   peerCommonName,
		peerSerialNumber: peerSerial,
		errorCounts:      make(map[string]int),
		streams:          make(map[string]*tcpStream),
		done:             make(chan struct{}),
	}

	connKey := buildConnectionKey(apiKey, mode, tunnelID)
	ts.connectionsMu.Lock()
	if existing, ok := ts.connections[connKey]; ok {
		ts.events.Warn("tunnel.connection_replaced", "replacing existing connection", nil, ts.events.withTunnelContext(mode, tunnelID, logging.Fields{
			"key_id":        APIKeyFingerprint(apiKey),
			"previous_addr": existing.remoteAddr,
		}))
		existing.close()
	}
	ts.connections[connKey] = tConn
	ts.connectionsMu.Unlock()

	if err := protocol.WriteMessage(rawConn, protocol.ServerHello{OK: true, Message: "registered", Mode: mode, TunnelID: tunnelID}); err != nil {
		ts.events.Error("tunnel.handshake_ack_failed", "handshake ack failed", err, ts.events.withTunnelContext(mode, tunnelID, logging.Fields{
			"remote_addr": connID,
			"key_id":      APIKeyFingerprint(apiKey),
		}))
		ts.recordError(apiKey, mode, "handshake_ack_failed")
		ts.removeConnectionByKey(connKey)
		return
	}
	// Handshake finished: from here on, liveness is handled by pings and per-request deadlines.
	_ = rawConn.SetDeadline(time.Time{})

	if mode == config.DomainTypeTCP {
		ts.events.Info("tunnel.connection_registered", "tunnel client registered", ts.events.withTunnelContext(mode, tunnelID, logging.Fields{
			"key_id":       APIKeyFingerprint(apiKey),
			"remote_addr":  connID,
			"peer_subject": peerCommonName,
			"peer_serial":  peerSerial,
		}))

		go ts.runTCPReceiver(connKey, tConn)
		return
	}

	subdomains := make([]string, len(permittedDomains))
	for i, d := range permittedDomains {
		subdomains[i] = d.Subdomain
	}
	ts.events.Info("tunnel.connection_registered", "tunnel client registered", ts.events.withTunnelContext(mode, tunnelID, logging.Fields{
		"key_id":       APIKeyFingerprint(apiKey),
		"remote_addr":  connID,
		"peer_subject": peerCommonName,
		"peer_serial":  peerSerial,
		"subdomains":   subdomains,
	}))

	go ts.monitorHTTPConnection(connKey, tConn)
}

func (ts *TunnelServer) monitorHTTPConnection(connKey string, conn *tunnelConn) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-conn.done:
			return
		case <-ticker.C:
		}

		conn.mu.Lock()
		err := protocol.WriteStreamFrame(conn.conn, protocol.FramePing, "", nil)
		conn.mu.Unlock()

		if err != nil {
			break
		}
	}

	// Only remove the connection if it is still the one we are monitoring.
	ts.connectionsMu.Lock()
	current, ok := ts.connections[connKey]
	if ok && current == conn {
		delete(ts.connections, connKey)
	} else {
		ok = false
	}
	ts.connectionsMu.Unlock()

	if ok {
		conn.close()
		uptime := time.Since(conn.connectedAt)
		uptimeMillis := uptime.Truncate(time.Millisecond).Seconds() * 1000
		ts.events.Info("tunnel.connection_removed", "removed tunnel connection", ts.events.withTunnelContext(conn.mode, conn.tunnelID, logging.Fields{
			"key_id":      APIKeyFingerprint(conn.apiKey),
			"remote_addr": conn.remoteAddr,
			"uptime_ms":   uptimeMillis,
			"reason":      "ping_failed",
		}))
	}
}

func (ts *TunnelServer) removeConnectionByKey(key string) {
	ts.connectionsMu.Lock()
	conn, ok := ts.connections[key]
	if ok {
		delete(ts.connections, key)
	}
	ts.connectionsMu.Unlock()

	if !ok {
		return
	}

	conn.close()
	uptime := time.Since(conn.connectedAt)
	uptimeMillis := uptime.Truncate(time.Millisecond).Seconds() * 1000
	ts.events.Info("tunnel.connection_removed", "removed tunnel connection", ts.events.withTunnelContext(conn.mode, conn.tunnelID, logging.Fields{
		"key_id":      APIKeyFingerprint(conn.apiKey),
		"remote_addr": conn.remoteAddr,
		"uptime_ms":   uptimeMillis,
	}))
}

// removeConnectionIfSame removes a connection only if the currently stored
// connection for the given key is the same object as expected. This prevents a
// stale goroutine from removing a replacement connection.
func (ts *TunnelServer) removeConnectionIfSame(apiKey, mode, tunnelID string, expected *tunnelConn) {
	key := buildConnectionKey(apiKey, mode, tunnelID)
	ts.connectionsMu.Lock()
	current, ok := ts.connections[key]
	if ok && current == expected {
		delete(ts.connections, key)
	} else {
		ok = false
	}
	ts.connectionsMu.Unlock()

	if !ok {
		return
	}

	expected.close()
	uptime := time.Since(expected.connectedAt)
	uptimeMillis := uptime.Truncate(time.Millisecond).Seconds() * 1000
	ts.events.Info("tunnel.connection_removed", "removed tunnel connection", ts.events.withTunnelContext(expected.mode, expected.tunnelID, logging.Fields{
		"key_id":      APIKeyFingerprint(expected.apiKey),
		"remote_addr": expected.remoteAddr,
		"uptime_ms":   uptimeMillis,
	}))
}

func (ts *TunnelServer) removeConnection(apiKey, mode, tunnelID string) {
	ts.removeConnectionByKey(buildConnectionKey(apiKey, mode, tunnelID))
}

func (ts *TunnelServer) getConnection(apiKey, mode, tunnelID string) (*tunnelConn, bool) {
	ts.connectionsMu.RLock()
	defer ts.connectionsMu.RUnlock()
	conn, ok := ts.connections[buildConnectionKey(apiKey, mode, tunnelID)]
	return conn, ok
}

// ForwardRequest はサブドメインに対応するクライアントへリクエストを転送する。
func (ts *TunnelServer) ForwardRequest(ctx context.Context, subdomain string, payload *protocol.RequestPayload) (*protocol.ResponsePayload, error) {
	domain, ok := ts.LookupDomain(subdomain)
	if !ok {
		return nil, fmt.Errorf("unknown subdomain: %s", subdomain)
	}

	domainMode := normaliseMode(domain.Type)
	if domainMode != config.DomainTypeHTTP {
		return nil, fmt.Errorf("domain %s is not configured for http mode", subdomain)
	}

	conn, ok := ts.getConnection(domain.APIKey, domainMode, subdomain)
	if !ok {
		ts.recordError(domain.APIKey, domainMode, "missing_client")
		return nil, ErrNoTunnelClient
	}

	payload.Subdomain = subdomain
	payload.Target = joinURL(domain.LocalURL, payload.Path)

	maxResponse := ts.responseLimit(domain)
	resp, err := conn.send(ctx, payload, maxResponse)
	if err != nil {
		ts.events.Error("tunnel.forward_failed", "tunnel send failed", err, logging.Fields{
			"subdomain": subdomain,
			"key_id":    APIKeyFingerprint(domain.APIKey),
		})
		ts.recordError(domain.APIKey, domainMode, "forward_send")
		// Only remove the connection for fatal I/O errors (EOF, closed).
		// Transient errors like context cancellation should not kill the tunnel.
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				ts.removeConnectionIfSame(domain.APIKey, domainMode, subdomain, conn)
			}
		}
		return nil, err
	}

	var reqBytes int
	if payload != nil {
		reqBytes = len(payload.Body)
	}
	ts.recordTransfer(domain.APIKey, domainMode, reqBytes, len(resp.Body))

	return resp, nil
}

// responseLimit returns the response body cap for a domain: the server-wide
// ceiling, lowered by the domain policy's max_response_bytes when set.
func (ts *TunnelServer) responseLimit(domain config.DomainConfig) int64 {
	_, limit := ts.BodyLimits()
	if p := domain.Policy.MaxResponseBytes; p > 0 && p < limit {
		limit = p
	}
	return limit
}

// Reload は設定を差し替え、ドメインキャッシュを更新する。
func (ts *TunnelServer) Reload(cfg *config.Config) {
	if cfg == nil {
		return
	}

	ts.cfgMu.Lock()
	ts.cfg = cfg
	ts.cfgMu.Unlock()
	ts.policyEngine.SetTrustedProxies(cfg.HTTP.TrustedProxies)

	updatedSubdomain := make(map[string]config.DomainConfig, len(cfg.Domains))
	updatedByAPIKey := make(map[string][]config.DomainConfig)
	for _, domain := range cfg.Domains {
		updatedSubdomain[domain.Subdomain] = domain
		updatedByAPIKey[domain.APIKey] = append(updatedByAPIKey[domain.APIKey], domain)
	}

	ts.domainsMu.Lock()
	ts.domainsBySubdomain = updatedSubdomain
	ts.domainsByAPIKey = updatedByAPIKey
	ts.domainsMu.Unlock()

	ts.metrics.mu.Lock()
	ts.metrics.lastReload = time.Now()
	ts.metrics.mu.Unlock()
}

func joinURL(base, requestPath string) string {
	if base == "" {
		return requestPath
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	requestPath = strings.TrimPrefix(requestPath, "/")
	return base + requestPath
}

// LookupDomain はサブドメインに対するドメイン設定を返す。
func (ts *TunnelServer) LookupDomain(subdomain string) (config.DomainConfig, bool) {
	ts.domainsMu.RLock()
	domain, ok := ts.domainsBySubdomain[subdomain]
	ts.domainsMu.RUnlock()
	return domain, ok
}

// PolicyEngine はドメインポリシーの評価エンジンを返す。
func (ts *TunnelServer) PolicyEngine() *policy.Engine {
	return ts.policyEngine
}

func (ts *TunnelServer) recordTransfer(apiKey, mode string, reqBytes, respBytes int) {
	if reqBytes == 0 && respBytes == 0 {
		return
	}
	mode = normaliseMode(mode)
	ts.metrics.mu.Lock()
	if reqBytes > 0 {
		ts.metrics.bytesIn[apiKey] += int64(reqBytes)
		ensureModeBucket(ts.metrics.bytesInByMode, mode)
		ts.metrics.bytesInByMode[mode][apiKey] += int64(reqBytes)
	}
	if respBytes > 0 {
		ts.metrics.bytesOut[apiKey] += int64(respBytes)
		ensureModeBucket(ts.metrics.bytesOutByMode, mode)
		ts.metrics.bytesOutByMode[mode][apiKey] += int64(respBytes)
	}
	ts.metrics.mu.Unlock()
}

func ensureModeBucket(dest map[string]map[string]int64, mode string) {
	if mode == "" {
		return
	}
	if _, ok := dest[mode]; !ok {
		dest[mode] = make(map[string]int64)
	}
}

// errorModeInvalid is the single bucket for every mode that is neither "http" nor "tcp".
// The mode in a hello is attacker-controlled before authentication, so it must never
// become a map key as-is (that would let an anonymous peer grow the map without bound).
const errorModeInvalid = "invalid"

// errorModeBucket maps a (normalised) mode to one of a fixed set of metric buckets:
// http, tcp or invalid.
func errorModeBucket(mode string) string {
	switch mode {
	case config.DomainTypeHTTP, config.DomainTypeTCP:
		return mode
	default:
		return errorModeInvalid
	}
}

func ensureErrorModeBucket(dest map[string]map[string]int, mode string) {
	if mode == "" {
		return
	}
	if _, ok := dest[mode]; !ok {
		dest[mode] = make(map[string]int)
	}
}

func (ts *TunnelServer) recordError(apiKey, mode, code string) {
	if code == "" {
		return
	}
	mode = normaliseMode(mode)

	ts.metrics.mu.Lock()
	ts.metrics.errorCounts[code]++
	bucket := errorModeBucket(mode)
	ensureErrorModeBucket(ts.metrics.errorCountsByMode, bucket)
	ts.metrics.errorCountsByMode[bucket][code]++
	ts.metrics.mu.Unlock()

	if apiKey == "" {
		return
	}

	ts.connectionsMu.RLock()
	targets := make([]*tunnelConn, 0, 1)
	for _, conn := range ts.connections {
		if conn == nil {
			continue
		}
		if conn.apiKey != apiKey {
			continue
		}
		if mode != "" && conn.mode != mode {
			continue
		}
		targets = append(targets, conn)
	}
	ts.connectionsMu.RUnlock()

	for _, conn := range targets {
		conn.mu.Lock()
		if conn.errorCounts == nil {
			conn.errorCounts = make(map[string]int)
		}
		conn.errorCounts[code]++
		conn.mu.Unlock()
	}
}

// Stats はトンネルサーバー全体の稼働状況を返す。
func (ts *TunnelServer) Stats() ServerStats {
	stats := ServerStats{
		ActiveByMode:      make(map[string]int),
		Domains:           make(map[string]DomainRuntimeStats),
		ErrorCounts:       make(map[string]int),
		ErrorCountsByMode: make(map[string]map[string]int),
		BytesIn:           make(map[string]int64),
		BytesOut:          make(map[string]int64),
		BytesInByMode:     make(map[string]map[string]int64),
		BytesOutByMode:    make(map[string]map[string]int64),
	}

	ts.metrics.mu.RLock()
	stats.LastReload = ts.metrics.lastReload
	stats.CertExpiry = ts.metrics.certExpiry
	for k, v := range ts.metrics.errorCounts {
		stats.ErrorCounts[k] = v
	}
	for k, v := range ts.metrics.bytesIn {
		stats.BytesIn[k] = v
	}
	for k, v := range ts.metrics.bytesOut {
		stats.BytesOut[k] = v
	}
	stats.BytesInByMode = copyNestedInt64Map(ts.metrics.bytesInByMode)
	stats.BytesOutByMode = copyNestedInt64Map(ts.metrics.bytesOutByMode)
	stats.ErrorCountsByMode = copyNestedIntMap(ts.metrics.errorCountsByMode)
	ts.metrics.mu.RUnlock()

	ts.domainsMu.RLock()
	domainsSnapshot := make(map[string][]config.DomainConfig, len(ts.domainsByAPIKey))
	for apiKey, domains := range ts.domainsByAPIKey {
		copied := make([]config.DomainConfig, len(domains))
		copy(copied, domains)
		domainsSnapshot[apiKey] = copied
	}
	ts.domainsMu.RUnlock()

	ts.connectionsMu.RLock()
	stats.ActiveConnections = len(ts.connections)
	for connKey, conn := range ts.connections {
		conn.mu.Lock()
		mode := conn.mode
		domainStats := DomainRuntimeStats{
			APIKey:           conn.apiKey,
			Mode:             mode,
			TunnelID:         conn.tunnelID,
			RemoteAddr:       conn.remoteAddr,
			ConnectedAt:      conn.connectedAt,
			LastActivity:     conn.lastActivity,
			BytesIn:          conn.bytesIn,
			BytesOut:         conn.bytesOut,
			Errors:           copyStringIntMap(conn.errorCounts),
			PeerCommonName:   conn.peerCommonName,
			PeerSerialNumber: conn.peerSerialNumber,
		}
		conn.mu.Unlock()

		modeKey := normaliseMode(mode)
		stats.ActiveByMode[modeKey]++

		var subdomains []string
		if mode == config.DomainTypeTCP {
			if domainStats.TunnelID != "" {
				subdomains = []string{domainStats.TunnelID}
			} else {
				subdomains = []string{}
			}
			for _, d := range domainsSnapshot[conn.apiKey] {
				if normaliseMode(d.Type) != config.DomainTypeTCP {
					continue
				}
				if domainStats.TunnelID != "" && strings.EqualFold(d.Subdomain, domainStats.TunnelID) {
					break
				}
			}
		} else {
			domains := domainsSnapshot[conn.apiKey]
			subdomains = make([]string, 0, len(domains))
			for _, domain := range domains {
				if normaliseMode(domain.Type) != config.DomainTypeHTTP {
					continue
				}
				subdomains = append(subdomains, domain.Subdomain)
			}
		}
		domainStats.Subdomains = subdomains

		stats.Domains[connKey] = domainStats
	}
	ts.connectionsMu.RUnlock()

	return stats
}

// TunnelsSnapshot はアクティブなトンネルのメタデータ一覧を返す。
func (ts *TunnelServer) TunnelsSnapshot() []TunnelInfo {
	ts.domainsMu.RLock()
	domainSnapshot := make(map[string][]config.DomainConfig, len(ts.domainsByAPIKey))
	for apiKey, domains := range ts.domainsByAPIKey {
		copied := make([]config.DomainConfig, len(domains))
		copy(copied, domains)
		domainSnapshot[apiKey] = copied
	}
	ts.domainsMu.RUnlock()

	ts.connectionsMu.RLock()
	infos := make([]TunnelInfo, 0, len(ts.connections))
	for connKey, conn := range ts.connections {
		_ = connKey
		conn.mu.Lock()
		info := TunnelInfo{
			APIKey:           conn.apiKey,
			Mode:             conn.mode,
			TunnelID:         conn.tunnelID,
			RemoteAddr:       conn.remoteAddr,
			ConnectedAt:      conn.connectedAt,
			LastActivity:     conn.lastActivity,
			PeerCommonName:   conn.peerCommonName,
			PeerSerialNumber: conn.peerSerialNumber,
			BytesIn:          conn.bytesIn,
			BytesOut:         conn.bytesOut,
		}
		if len(conn.errorCounts) > 0 {
			info.Errors = copyStringIntMap(conn.errorCounts)
		}
		conn.mu.Unlock()

		if conn.mode == config.DomainTypeTCP {
			if conn.tunnelID != "" {
				info.Subdomains = []string{conn.tunnelID}
				for _, domain := range domainSnapshot[conn.apiKey] {
					if normaliseMode(domain.Type) != config.DomainTypeTCP {
						continue
					}
					if strings.EqualFold(domain.Subdomain, conn.tunnelID) {
						info.LocalHost = domain.LocalHost
						info.LocalPort = domain.LocalPort
						break
					}
				}
			} else {
				info.Subdomains = []string{}
			}
		} else {
			domains := domainSnapshot[conn.apiKey]
			subdomains := make([]string, 0, len(domains))
			for _, domain := range domains {
				if normaliseMode(domain.Type) != config.DomainTypeHTTP {
					continue
				}
				subdomains = append(subdomains, domain.Subdomain)
			}
			info.Subdomains = subdomains
		}

		infos = append(infos, info)
	}
	ts.connectionsMu.RUnlock()

	sort.Slice(infos, func(i, j int) bool {
		if infos[i].APIKey == infos[j].APIKey {
			return infos[i].ConnectedAt.Before(infos[j].ConnectedAt)
		}
		return infos[i].APIKey < infos[j].APIKey
	})

	return infos
}

func copyStringIntMap(src map[string]int) map[string]int {
	if len(src) == 0 {
		return map[string]int{}
	}
	dst := make(map[string]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func copyNestedInt64Map(src map[string]map[string]int64) map[string]map[string]int64 {
	if len(src) == 0 {
		return map[string]map[string]int64{}
	}
	dst := make(map[string]map[string]int64, len(src))
	for mode, values := range src {
		if len(values) == 0 {
			continue
		}
		modeCopy := make(map[string]int64, len(values))
		for key, value := range values {
			modeCopy[key] = value
		}
		dst[mode] = modeCopy
	}
	return dst
}

func copyNestedIntMap(src map[string]map[string]int) map[string]map[string]int {
	if len(src) == 0 {
		return map[string]map[string]int{}
	}
	dst := make(map[string]map[string]int, len(src))
	for mode, values := range src {
		if len(values) == 0 {
			continue
		}
		modeCopy := make(map[string]int, len(values))
		for key, value := range values {
			modeCopy[key] = value
		}
		dst[mode] = modeCopy
	}
	return dst
}

// ExtractSubdomain returns the part of the Host header that precedes
// ".<baseDomain>". The port, if any, is ignored and the comparison is
// case-insensitive. It returns "" when the host is not strictly below baseDomain
// (including the apex itself) or when baseDomain is empty.
//
// For host "foo.gridbase.localhost:8080" and baseDomain "gridbase.localhost" it
// returns "foo".
func ExtractSubdomain(host, baseDomain string) string {
	baseDomain = config.NormalizeBaseDomain(baseDomain)
	if baseDomain == "" {
		return ""
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(host, ".")

	suffix := "." + baseDomain
	if !strings.HasSuffix(host, suffix) {
		return ""
	}
	return strings.TrimSuffix(host, suffix)
}

// frameLimit returns the configured per-frame size limit for tunnel connections.
func (ts *TunnelServer) frameLimit() int64 {
	ts.cfgMu.RLock()
	defer ts.cfgMu.RUnlock()
	if ts.cfg == nil {
		return config.DefaultMaxFrameBytes
	}
	return ts.cfg.Tunnel.FrameLimit()
}

// handshakeTimeout returns how long a new tunnel connection may take to finish
// the TLS and hello exchange.
func (ts *TunnelServer) handshakeTimeout() time.Duration {
	ts.cfgMu.RLock()
	defer ts.cfgMu.RUnlock()
	if ts.cfg == nil {
		return config.DefaultHandshakeTimeoutSeconds * time.Second
	}
	return ts.cfg.Tunnel.HandshakeDeadline()
}

// BodyLimits returns the server-wide ceilings for visitor request bodies and
// tunnel client response bodies.
func (ts *TunnelServer) BodyLimits() (maxRequestBytes, maxResponseBytes int64) {
	ts.cfgMu.RLock()
	defer ts.cfgMu.RUnlock()
	if ts.cfg == nil {
		return config.DefaultMaxBodyBytes, config.DefaultMaxBodyBytes
	}
	return ts.cfg.HTTP.RequestBodyLimit(), ts.cfg.HTTP.ResponseBodyLimit()
}

// BaseDomain returns the currently configured base domain.
func (ts *TunnelServer) BaseDomain() string {
	ts.cfgMu.RLock()
	defer ts.cfgMu.RUnlock()
	if ts.cfg == nil {
		return ""
	}
	return ts.cfg.BaseDomain
}
