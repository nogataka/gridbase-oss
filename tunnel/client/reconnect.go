package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

var excludedHeaders = map[string]struct{}{
	"host":               {},
	"connection":         {},
	"content-length":     {},
	"accept-encoding":    {},
	"transfer-encoding":  {},
	"keep-alive":         {},
	"x-tunnel-subdomain": {},
	"x-local-url":        {},
}

var (
	errHeartbeatTimeout = errors.New("heartbeat timeout")
	errStreamShutdown   = errors.New("stream closed by server")
)

// fatalError wraps errors that should abort the reconnect loop.
type fatalError struct {
	cause error
}

func (f *fatalError) Error() string {
	return f.cause.Error()
}

func (f *fatalError) Unwrap() error {
	return f.cause
}

func newFatalError(err error) error {
	if err == nil {
		return nil
	}
	return &fatalError{cause: err}
}

func isFatalError(err error) bool {
	var fe *fatalError
	return errors.As(err, &fe)
}

// ClientMetrics captures connection and payload counters for observability.
type ClientMetrics struct {
	connectAttempts       atomic.Int64
	successfulConnections atomic.Int64
	reconnects            atomic.Int64
	requests              atomic.Int64
	bytesIn               atomic.Int64
	bytesOut              atomic.Int64
	lastError             atomic.Pointer[string]
	lastConnectedUnixNano atomic.Int64
}

// NewClientMetrics initialises a metrics container.
func NewClientMetrics() *ClientMetrics {
	m := &ClientMetrics{}
	m.lastError.Store(newString(""))
	return m
}

// ObserveConnectAttempt increments the dial attempt counter.
func (m *ClientMetrics) ObserveConnectAttempt() {
	m.connectAttempts.Add(1)
}

// ObserveConnectSuccess records a successful handshake.
func (m *ClientMetrics) ObserveConnectSuccess() {
	m.successfulConnections.Add(1)
	m.lastConnectedUnixNano.Store(time.Now().UnixNano())
	m.lastError.Store(newString(""))
}

// ObserveReconnect records a reconnect-worthy error.
func (m *ClientMetrics) ObserveReconnect(err error) {
	m.reconnects.Add(1)
	if err != nil {
		m.lastError.Store(newString(err.Error()))
	}
}

// ObserveRequest records payload sizes for a completed request cycle.
func (m *ClientMetrics) ObserveRequest(bytesIn, bytesOut int64) {
	m.requests.Add(1)
	m.bytesIn.Add(bytesIn)
	m.bytesOut.Add(bytesOut)
}

// Snapshot returns a point-in-time metrics view.
func (m *ClientMetrics) Snapshot() ClientMetricsSnapshot {
	lastErrPtr := m.lastError.Load()
	lastError := ""
	if lastErrPtr != nil {
		lastError = *lastErrPtr
	}
	lastConnected := time.Time{}
	if unix := m.lastConnectedUnixNano.Load(); unix > 0 {
		lastConnected = time.Unix(0, unix)
	}

	return ClientMetricsSnapshot{
		ConnectAttempts:       m.connectAttempts.Load(),
		SuccessfulConnections: m.successfulConnections.Load(),
		Reconnects:            m.reconnects.Load(),
		Requests:              m.requests.Load(),
		BytesIn:               m.bytesIn.Load(),
		BytesOut:              m.bytesOut.Load(),
		LastError:             lastError,
		LastConnected:         lastConnected,
	}
}

// ClientMetricsSnapshot is a serialisable view of metrics.
type ClientMetricsSnapshot struct {
	ConnectAttempts       int64     `json:"connect_attempts"`
	SuccessfulConnections int64     `json:"successful_connections"`
	Reconnects            int64     `json:"reconnects"`
	Requests              int64     `json:"requests"`
	BytesIn               int64     `json:"bytes_in"`
	BytesOut              int64     `json:"bytes_out"`
	LastError             string    `json:"last_error,omitempty"`
	LastConnected         time.Time `json:"last_connected,omitempty"`
}

func newString(value string) *string {
	s := value
	return &s
}

// Runner encapsulates the tunnelling loop, reconnect logic, and HTTP forwarding.
type Runner struct {
	logger  *log.Logger
	metrics *ClientMetrics
}

// NewRunner constructs a Runner with defaults.
func NewRunner(logger *log.Logger, metrics *ClientMetrics) *Runner {
	if logger == nil {
		logger = log.New(os.Stdout, "[gridbase-client] ", log.LstdFlags|log.Lmicroseconds)
	}
	if metrics == nil {
		metrics = NewClientMetrics()
	}
	return &Runner{
		logger:  logger,
		metrics: metrics,
	}
}

// newLocalHTTPClient builds the client used to call the local application. It
// never follows redirects: a 3xx is returned to the visitor unchanged, so an
// open redirect in the local app cannot make this machine fetch internal URLs
// (SSRF) on the visitor's behalf, and Set-Cookie on the redirect is preserved.
func newLocalHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Run maintains the tunnel connection until the context is cancelled or a fatal error occurs.
func (r *Runner) Run(ctx context.Context, cfg *ClientConfig) error {
	httpClient := newLocalHTTPClient(cfg.HTTP.Timeout.Duration)

	backoff := newBackoffStrategy(cfg.Reconnect)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		r.metrics.ObserveConnectAttempt()

		conn, serverHello, err := r.establishConnection(ctx, cfg)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return context.Canceled
			}
			if isFatalError(err) {
				return err
			}
			delay := backoff.NextDelay()
			r.metrics.ObserveReconnect(err)
			r.logReconnect("dial failed", err, delay, backoff.attempts)
			if err := waitWithContext(ctx, delay); err != nil {
				return err
			}
			continue
		}

		backoff.Reset()
		var serveErr error
		if cfg.Mode == config.DomainTypeTCP {
			serveErr = r.serveTCP(ctx, conn, cfg, serverHello)
		} else {
			serveErr = r.serveHTTP(ctx, conn, cfg, httpClient)
		}
		if serveErr != nil {
			_ = conn.Close()
			if errors.Is(serveErr, context.Canceled) {
				return nil
			}
			if isFatalError(serveErr) {
				return serveErr
			}
			delay := backoff.NextDelay()
			r.metrics.ObserveReconnect(serveErr)
			r.logReconnect("connection lost", serveErr, delay, backoff.attempts)
			if err := waitWithContext(ctx, delay); err != nil {
				return err
			}
			continue
		}

		_ = conn.Close()
		return nil
	}
}

func (r *Runner) logReconnect(reason string, err error, delay time.Duration, attempt int) {
	snapshot := r.metrics.Snapshot()
	body, marshalErr := json.Marshal(snapshot)
	if marshalErr != nil {
		r.logger.Printf("%s: error=%v delay=%s attempt=%d reconnects=%d (metrics marshal error: %v)",
			reason, err, delay, attempt, snapshot.Reconnects, marshalErr)
		return
	}
	r.logger.Printf("%s: error=%v delay=%s attempt=%d metrics=%s",
		reason, err, delay, attempt, body)
}

func (r *Runner) establishConnection(ctx context.Context, cfg *ClientConfig) (net.Conn, protocol.ServerHello, error) {
	netDialer := &net.Dialer{Timeout: 10 * time.Second}

	var (
		conn net.Conn
		err  error
	)
	if cfg.TLS.UseTLS() {
		tlsConfig, tlsErr := buildTLSConfig(cfg)
		if tlsErr != nil {
			return nil, protocol.ServerHello{}, newFatalError(fmt.Errorf("build tls config: %w", tlsErr))
		}
		dialer := &tls.Dialer{NetDialer: netDialer, Config: tlsConfig}
		conn, err = dialer.DialContext(ctx, "tcp", cfg.ServerAddr)
	} else {
		conn, err = netDialer.DialContext(ctx, "tcp", cfg.ServerAddr)
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, protocol.ServerHello{}, ctx.Err()
		}
		return nil, protocol.ServerHello{}, err
	}

	serverHello, err := r.performHandshake(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return nil, protocol.ServerHello{}, err
	}

	r.metrics.ObserveConnectSuccess()
	snapshot := r.metrics.Snapshot()
	r.logger.Printf("connected remote=%s attempts=%d successes=%d", conn.RemoteAddr(), snapshot.ConnectAttempts, snapshot.SuccessfulConnections)
	return conn, serverHello, nil
}

func (r *Runner) performHandshake(conn net.Conn, cfg *ClientConfig) (protocol.ServerHello, error) {
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		r.logger.Printf("warn: set handshake deadline: %v", err)
	}
	clientHello := protocol.ClientHello{
		APIKey:   cfg.APIKey,
		Mode:     cfg.Mode,
		TunnelID: cfg.TunnelID,
	}
	if err := protocol.WriteMessage(conn, clientHello); err != nil {
		return protocol.ServerHello{}, fmt.Errorf("send client hello: %w", err)
	}

	var hello protocol.ServerHello
	if err := protocol.ReadMessageLimit(conn, &hello, protocol.MaxHandshakeBytes); err != nil {
		if isTimeoutErr(err) {
			return protocol.ServerHello{}, err
		}
		return protocol.ServerHello{}, fmt.Errorf("read server hello: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	if !hello.OK {
		message := strings.TrimSpace(hello.Message)
		if message == "" {
			message = "server rejected handshake"
		}
		return protocol.ServerHello{}, newFatalError(errors.New(message))
	}
	return hello, nil
}

// buildTLSConfig assembles the client TLS settings. The system trust store is
// used unless tls.ca points at a private CA; a client certificate is presented
// only when tls.cert and tls.key are both set.
func buildTLSConfig(cfg *ClientConfig) (*tls.Config, error) {
	tlsConfig := &tls.Config{
		ServerName: cfg.ServerName,
		MinVersion: tls.VersionTLS12,
	}

	if cfg.TLS.Insecure {
		tlsConfig.InsecureSkipVerify = true //nolint:gosec // explicit opt-in via --insecure
	}

	if cfg.TLS.CAPath != "" {
		caBytes, err := os.ReadFile(cfg.TLS.CAPath)
		if err != nil {
			return nil, fmt.Errorf("read ca certificate: %w", err)
		}
		roots := x509.NewCertPool()
		if ok := roots.AppendCertsFromPEM(caBytes); !ok {
			return nil, errors.New("failed to append ca certificate")
		}
		tlsConfig.RootCAs = roots
	}

	if cfg.TLS.CertPath != "" || cfg.TLS.KeyPath != "" {
		if cfg.TLS.CertPath == "" || cfg.TLS.KeyPath == "" {
			return nil, errors.New("tls.cert and tls.key must be set together")
		}
		cert, err := tls.LoadX509KeyPair(cfg.TLS.CertPath, cfg.TLS.KeyPath)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	return tlsConfig, nil
}

func (r *Runner) serveHTTP(ctx context.Context, conn net.Conn, cfg *ClientConfig, httpClient *http.Client) error {
	heartbeat := cfg.Heartbeat.Timeout.Duration

	for {
		if ctx.Err() != nil {
			return context.Canceled
		}

		if err := conn.SetReadDeadline(time.Now().Add(heartbeat)); err != nil {
			r.logger.Printf("warn: set read deadline: %v", err)
		}

		frame, err := protocol.ReadStreamFrame(conn, nil)
		if err != nil {
			if isTimeoutErr(err) {
				return errHeartbeatTimeout
			}
			if errors.Is(err, io.EOF) {
				return errStreamShutdown
			}
			return fmt.Errorf("read stream frame: %w", err)
		}

		if frame.Type == protocol.FramePing {
			continue
		}

		if frame.Type != protocol.FrameRequestStart {
			return newFatalError(fmt.Errorf("unexpected frame type %s while awaiting request_start", frame.Type))
		}

		var start protocol.RequestStartPayload
		if len(frame.Payload) > 0 {
			if err := json.Unmarshal(frame.Payload, &start); err != nil {
				return fmt.Errorf("decode request_start: %w", err)
			}
		}

		body, err := r.readRequestBody(ctx, conn, cfg, frame.RequestID)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return context.Canceled
			}
			if errors.Is(err, errHeartbeatTimeout) {
				return err
			}
			r.sendErrorFrame(conn, frame.RequestID, err)
			continue
		}

		reqPayload := &protocol.RequestPayload{
			Method:    start.Method,
			Path:      start.Path,
			Target:    start.Target,
			Header:    start.Header,
			Subdomain: start.Subdomain,
			Body:      body,
		}

		respPayload := r.handleRequest(cfg, httpClient, reqPayload)
		r.metrics.ObserveRequest(int64(len(body)), int64(len(respPayload.Body)))

		if err := r.writeResponseFrames(conn, frame.RequestID, respPayload); err != nil {
			if isTimeoutErr(err) {
				return errHeartbeatTimeout
			}
			return fmt.Errorf("write response frames: %w", err)
		}
	}
}

func (r *Runner) readRequestBody(ctx context.Context, conn net.Conn, cfg *ClientConfig, requestID string) ([]byte, error) {
	var buf bytes.Buffer
	heartbeat := cfg.Heartbeat.Timeout.Duration
	maxBody := cfg.HTTP.MaxRequestBytes
	if maxBody <= 0 {
		maxBody = config.DefaultMaxBodyBytes
	}

	for {
		if err := conn.SetReadDeadline(time.Now().Add(heartbeat)); err != nil {
			r.logger.Printf("warn: set read deadline: %v", err)
		}

		frame, err := protocol.ReadStreamFrame(conn, nil)
		if err != nil {
			if ctx.Err() != nil {
				return nil, context.Canceled
			}
			if isTimeoutErr(err) {
				return nil, errHeartbeatTimeout
			}
			return nil, fmt.Errorf("read request body frame: %w", err)
		}

		if frame.RequestID != requestID {
			return nil, fmt.Errorf("unexpected request id %s (want %s)", frame.RequestID, requestID)
		}

		switch frame.Type {
		case protocol.FrameRequestBody:
			var chunk protocol.BodyChunkPayload
			if len(frame.Payload) > 0 {
				if err := json.Unmarshal(frame.Payload, &chunk); err != nil {
					return nil, fmt.Errorf("decode request chunk: %w", err)
				}
			}
			if int64(buf.Len())+int64(len(chunk.Data)) > maxBody {
				return nil, fmt.Errorf("request body exceeds %d bytes", maxBody)
			}
			if _, err := buf.Write(chunk.Data); err != nil {
				return nil, fmt.Errorf("buffer chunk: %w", err)
			}
		case protocol.FrameRequestEnd:
			return buf.Bytes(), nil
		case protocol.FrameError:
			var payload protocol.ErrorPayload
			if len(frame.Payload) > 0 {
				if err := json.Unmarshal(frame.Payload, &payload); err != nil {
					return nil, fmt.Errorf("decode error payload: %w", err)
				}
			}
			if payload.Message == "" {
				payload.Message = "request aborted"
			}
			return nil, errors.New(payload.Message)
		default:
			return nil, fmt.Errorf("unexpected frame type %s in request stream", frame.Type)
		}
	}
}

func (r *Runner) handleRequest(cfg *ClientConfig, httpClient *http.Client, reqPayload *protocol.RequestPayload) *protocol.ResponsePayload {
	respPayload := &protocol.ResponsePayload{
		Header: make(map[string][]string),
	}

	targetURL := resolveTargetURL(cfg, reqPayload)
	if targetURL == "" {
		respPayload.StatusCode = http.StatusBadGateway
		respPayload.Error = "missing target url"
		respPayload.Body = []byte("missing target url")
		return respPayload
	}

	requestCtx, cancel := context.WithTimeout(context.Background(), httpClient.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, reqPayload.Method, targetURL, bytes.NewReader(reqPayload.Body))
	if err != nil {
		respPayload.StatusCode = http.StatusBadGateway
		respPayload.Error = fmt.Sprintf("build request: %v", err)
		respPayload.Body = []byte(respPayload.Error)
		return respPayload
	}

	for key, values := range reqPayload.Header {
		if _, skip := excludedHeaders[strings.ToLower(key)]; skip {
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		// The error text names the local URL; keep it in this machine's log and
		// give the visitor a generic body.
		r.logger.Printf("warn: forward request failed: %v", err)
		respPayload.StatusCode = http.StatusBadGateway
		respPayload.Error = "forward request failed"
		respPayload.Body = []byte("bad gateway: local application unreachable")
		return respPayload
	}
	defer resp.Body.Close()

	maxResponse := config.DefaultMaxBodyBytes
	if cfg != nil && cfg.HTTP.MaxResponseBytes > 0 {
		maxResponse = cfg.HTTP.MaxResponseBytes
	}
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		respPayload.StatusCode = http.StatusBadGateway
		respPayload.Error = fmt.Sprintf("read response: %v", err)
		respPayload.Body = []byte(respPayload.Error)
		return respPayload
	}
	if int64(len(bodyBytes)) > maxResponse {
		respPayload.StatusCode = http.StatusBadGateway
		respPayload.Error = fmt.Sprintf("local response exceeds %d bytes", maxResponse)
		respPayload.Body = []byte(respPayload.Error)
		return respPayload
	}

	respPayload.StatusCode = resp.StatusCode
	for key, values := range resp.Header {
		respPayload.Header[key] = append([]string(nil), values...)
	}
	respPayload.Body = bodyBytes
	return respPayload
}

func resolveTargetURL(cfg *ClientConfig, payload *protocol.RequestPayload) string {
	if payload == nil {
		return ""
	}
	if cfg != nil {
		override := strings.TrimSpace(cfg.HTTP.OverrideURL)
		if override != "" {
			return joinOverrideURL(override, payload.Path)
		}
	}
	return strings.TrimSpace(payload.Target)
}

func joinOverrideURL(base, requestPath string) string {
	if base == "" {
		return requestPath
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	requestPath = strings.TrimPrefix(requestPath, "/")
	return base + requestPath
}

func (r *Runner) writeResponseFrames(conn net.Conn, requestID string, payload *protocol.ResponsePayload) error {
	if err := conn.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		r.logger.Printf("warn: set write deadline: %v", err)
	}

	start := protocol.ResponseStartPayload{
		StatusCode: payload.StatusCode,
		Header:     payload.Header,
		Error:      payload.Error,
	}
	if err := protocol.WriteStreamFrame(conn, protocol.FrameResponseStart, requestID, start); err != nil {
		return fmt.Errorf("write response start: %w", err)
	}

	if len(payload.Body) > 0 {
		for offset := 0; offset < len(payload.Body); {
			end := offset + protocol.StreamChunkSize
			if end > len(payload.Body) {
				end = len(payload.Body)
			}
			chunk := payload.Body[offset:end]
			if err := protocol.WriteStreamFrame(conn, protocol.FrameResponseBody, requestID, protocol.BodyChunkPayload{Data: chunk}); err != nil {
				return fmt.Errorf("write response chunk: %w", err)
			}
			offset = end
		}
	}

	if err := protocol.WriteStreamFrame(conn, protocol.FrameResponseEnd, requestID, nil); err != nil {
		return fmt.Errorf("write response end: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Time{})
	return nil
}

func (r *Runner) sendErrorFrame(conn net.Conn, requestID string, err error) {
	payload := protocol.ErrorPayload{Message: err.Error()}
	if writeErr := protocol.WriteStreamFrame(conn, protocol.FrameError, requestID, payload); writeErr != nil {
		r.logger.Printf("warn: failed to send error frame: %v (original error: %v)", writeErr, err)
	}
}

func waitWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isTimeoutErr(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

type backoffStrategy struct {
	initial    time.Duration
	max        time.Duration
	multiplier float64
	jitter     float64
	attempts   int
	source     *rand.Rand
}

func newBackoffStrategy(cfg ReconnectConfig) *backoffStrategy {
	multiplier := cfg.Multiplier
	if multiplier < 1.0 {
		multiplier = 1.0
	}
	jitter := cfg.Jitter
	if jitter < 0 {
		jitter = 0
	}
	if jitter > 1 {
		jitter = 1
	}
	return &backoffStrategy{
		initial:    cfg.Initial.Duration,
		max:        cfg.Max.Duration,
		multiplier: multiplier,
		jitter:     jitter,
		source:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (b *backoffStrategy) NextDelay() time.Duration {
	base := float64(b.initial)
	power := math.Pow(b.multiplier, float64(b.attempts))
	b.attempts++

	delay := time.Duration(base * power)
	if delay > b.max {
		delay = b.max
	}

	if b.jitter > 0 {
		jitterRange := b.jitter
		jitterOffset := (b.source.Float64()*2 - 1) * jitterRange
		delay = time.Duration(float64(delay) * (1 + jitterOffset))
		if delay < b.initial {
			delay = b.initial
		}
		if delay > b.max {
			delay = b.max
		}
	}

	if delay <= 0 {
		delay = b.initial
	}
	return delay
}

func (b *backoffStrategy) Reset() {
	b.attempts = 0
}
