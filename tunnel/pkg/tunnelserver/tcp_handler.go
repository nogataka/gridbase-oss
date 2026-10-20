package tunnelserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/config"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/logging"
	"github.com/nogataka/gridbase-oss/tunnel/pkg/policy"
	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

type tcpStream struct {
	id       string
	apiKey   string
	tunnelID string
	conn     net.Conn

	resultCh  chan error
	closeOnce sync.Once
}

func newTCPStream(id, apiKey, tunnelID string, conn net.Conn) *tcpStream {
	return &tcpStream{
		id:       id,
		apiKey:   apiKey,
		tunnelID: tunnelID,
		conn:     conn,
		resultCh: make(chan error, 1),
	}
}

func (s *tcpStream) result() <-chan error {
	return s.resultCh
}

func (s *tcpStream) writeFromClient(data []byte) error {
	if len(data) == 0 || s.conn == nil {
		return nil
	}
	_, err := s.conn.Write(data)
	return err
}

func (s *tcpStream) close(err error) {
	s.closeOnce.Do(func() {
		if s.conn != nil {
			_ = s.conn.Close()
		}
		s.resultCh <- err
		close(s.resultCh)
	})
}

// StartTCPStream establishes a TCP data channel between an upstream connection and the tunnel client.
func (ts *TunnelServer) StartTCPStream(ctx context.Context, tunnelID string, upstream net.Conn) error {
	if upstream == nil {
		return errors.New("tcp upstream connection is required")
	}

	domain, ok := ts.LookupDomain(tunnelID)
	if !ok {
		return fmt.Errorf("unknown tunnel id: %s", tunnelID)
	}
	if normaliseMode(domain.Type) != config.DomainTypeTCP {
		return fmt.Errorf("domain %s not configured for tcp mode", tunnelID)
	}

	conn, ok := ts.getConnection(domain.APIKey, config.DomainTypeTCP, tunnelID)
	if !ok {
		ts.recordError(domain.APIKey, config.DomainTypeTCP, "missing_tcp_client")
		return ErrNoTunnelClient
	}

	if engine := ts.PolicyEngine(); engine != nil {
		remoteAddr := ""
		if upstreamAddr := upstream.RemoteAddr(); upstreamAddr != nil {
			remoteAddr = upstreamAddr.String()
		}
		if err := engine.EvaluateTCP(domain.Policy, policy.TCPContext{RemoteAddr: remoteAddr}); err != nil {
			var violation *policy.ViolationError
			if errors.As(err, &violation) {
				ts.recordError(domain.APIKey, config.DomainTypeTCP, fmt.Sprintf("policy_%s", violation.Code))
				ts.events.Warn("tunnel.tcp_policy_violation", "tcp policy violation", err, ts.events.withTunnelContext(config.DomainTypeTCP, tunnelID, logging.Fields{
					"key_id":      APIKeyFingerprint(domain.APIKey),
					"remote_addr": remoteAddr,
					"violation":   string(violation.Code),
					"details":     violation.Details,
				}))
			} else {
				ts.recordError(domain.APIKey, config.DomainTypeTCP, "policy_eval_error")
				ts.events.Error("tunnel.tcp_policy_evaluation_failed", "tcp policy evaluation failed", err, ts.events.withTunnelContext(config.DomainTypeTCP, tunnelID, logging.Fields{
					"key_id":      APIKeyFingerprint(domain.APIKey),
					"remote_addr": remoteAddr,
				}))
			}
			if upstream != nil {
				_ = upstream.Close()
			}
			return err
		}
	}

	streamID := fmt.Sprintf("%s-%d", tunnelID, ts.streamCounter.Add(1))
	stream := newTCPStream(streamID, domain.APIKey, tunnelID, upstream)
	conn.addStream(stream)

	startPayload := protocol.RequestStartPayload{
		Method:    "CONNECT",
		Target:    tunnelID,
		Subdomain: tunnelID,
	}
	if err := conn.sendFrame(protocol.FrameRequestStart, streamID, startPayload); err != nil {
		conn.removeStream(streamID)
		stream.close(err)
		ts.recordError(domain.APIKey, config.DomainTypeTCP, "tcp_stream_start_failed")
		return fmt.Errorf("write tcp start frame: %w", err)
	}

	ts.events.Info("tunnel.tcp_stream_open", "tcp stream opened", ts.events.withTunnelContext(config.DomainTypeTCP, tunnelID, logging.Fields{
		"key_id":    APIKeyFingerprint(domain.APIKey),
		"stream_id": streamID,
	}))

	done := stream.result()
	go ts.pipeTCPServerToClient(conn, stream)

	var resultErr error
	select {
	case resultErr = <-done:
	case <-ctx.Done():
		resultErr = ctx.Err()
		closePayload := protocol.ClosePayload{
			Code:    "context_cancelled",
			Message: resultErr.Error(),
		}
		if err := conn.sendFrame(protocol.FrameClose, streamID, closePayload); err != nil {
			ts.events.Warn("tunnel.tcp_stream_close_failed", "failed to send close frame on cancellation", err, ts.events.withTunnelContext(config.DomainTypeTCP, tunnelID, logging.Fields{
				"key_id":    APIKeyFingerprint(domain.APIKey),
				"stream_id": streamID,
			}))
		}
		stream.close(resultErr)
	}

	conn.removeStream(streamID)

	if resultErr != nil && !errors.Is(resultErr, context.Canceled) && !errors.Is(resultErr, io.EOF) {
		ts.recordError(domain.APIKey, config.DomainTypeTCP, "tcp_stream_error")
		ts.events.Warn("tunnel.tcp_stream_ended", "tcp stream ended with error", resultErr, ts.events.withTunnelContext(config.DomainTypeTCP, tunnelID, logging.Fields{
			"key_id":    APIKeyFingerprint(domain.APIKey),
			"stream_id": streamID,
		}))
		return resultErr
	}

	ts.events.Info("tunnel.tcp_stream_closed", "tcp stream closed", ts.events.withTunnelContext(config.DomainTypeTCP, tunnelID, logging.Fields{
		"key_id":    APIKeyFingerprint(domain.APIKey),
		"stream_id": streamID,
	}))
	return nil
}

func (ts *TunnelServer) pipeTCPServerToClient(conn *tunnelConn, stream *tcpStream) {
	buffer := make([]byte, protocol.StreamChunkSize)
	for {
		n, err := stream.conn.Read(buffer)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buffer[:n])
			if sendErr := conn.sendFrame(protocol.FrameData, stream.id, protocol.BodyChunkPayload{Data: chunk}); sendErr != nil {
				stream.close(sendErr)
				ts.events.Error("tunnel.tcp_stream_forward_failed", "failed to forward tcp data to client", sendErr, ts.events.withTunnelContext(config.DomainTypeTCP, stream.tunnelID, logging.Fields{
					"key_id":    APIKeyFingerprint(stream.apiKey),
					"stream_id": stream.id,
				}))
				return
			}
			conn.incrementBytes(int64(n), 0)
			ts.recordTransfer(stream.apiKey, config.DomainTypeTCP, n, 0)
		}
		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
				_ = conn.sendFrame(protocol.FrameClose, stream.id, protocol.ClosePayload{Code: "eof"})
				stream.close(nil)
			case errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed):
				// upstream はローカルで Close 済み（例: クライアントの close frame により
				// stream.close→upstream Close）。既に teardown 中なのでここでフレームを
				// 送らない。sendFrame は tc.mu を保持したまま書き込むため、相手が読まない
				// 状況で書込がブロックすると StartTCPStream の removeStream と
				// デッドロックする。stream.close は closeOnce で冪等（no-op）。
				stream.close(nil)
			default:
				_ = conn.sendFrame(protocol.FrameError, stream.id, protocol.ErrorPayload{Message: err.Error()})
				stream.close(err)
			}
			return
		}
	}
}

func (ts *TunnelServer) runTCPReceiver(connKey string, conn *tunnelConn) {
	for {
		frame, err := conn.readFrame()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				ts.events.Error("tunnel.tcp_stream_read_failed", "failed to read tcp frame", err, ts.events.withTunnelContext(conn.mode, conn.tunnelID, logging.Fields{
					"key_id": APIKeyFingerprint(conn.apiKey),
				}))
			}
			break
		}

		switch frame.Type {
		case protocol.FrameData:
			var payload protocol.BodyChunkPayload
			if len(frame.Payload) > 0 {
				if err := json.Unmarshal(frame.Payload, &payload); err != nil {
					ts.events.Warn("tunnel.tcp_stream_payload_decode_failed", "failed to decode tcp data payload", err, ts.events.withTunnelContext(conn.mode, conn.tunnelID, logging.Fields{
						"key_id":    APIKeyFingerprint(conn.apiKey),
						"stream_id": frame.RequestID,
					}))
					continue
				}
			}
			if len(payload.Data) == 0 {
				continue
			}
			stream, ok := conn.loadStream(frame.RequestID)
			if !ok {
				ts.events.Warn("tunnel.tcp_stream_unknown", "received tcp data for unknown stream", nil, ts.events.withTunnelContext(conn.mode, conn.tunnelID, logging.Fields{
					"key_id":    APIKeyFingerprint(conn.apiKey),
					"stream_id": frame.RequestID,
				}))
				continue
			}
			if err := stream.writeFromClient(payload.Data); err != nil {
				stream.close(err)
				continue
			}
			conn.incrementBytes(0, int64(len(payload.Data)))
			ts.recordTransfer(conn.apiKey, conn.mode, 0, len(payload.Data))
		case protocol.FrameClose:
			if stream := conn.removeStream(frame.RequestID); stream != nil {
				stream.close(nil)
			}
		case protocol.FrameError:
			var payload protocol.ErrorPayload
			if len(frame.Payload) > 0 {
				if err := json.Unmarshal(frame.Payload, &payload); err != nil {
					payload.Message = fmt.Sprintf("decode error: %v", err)
				}
			}
			if stream := conn.removeStream(frame.RequestID); stream != nil {
				if payload.Message == "" {
					payload.Message = "tcp stream error"
				}
				stream.close(errors.New(payload.Message))
			}
		default:
			ts.events.Warn("tunnel.tcp_stream_unexpected_frame", "unexpected frame type in tcp mode", nil, ts.events.withTunnelContext(conn.mode, conn.tunnelID, logging.Fields{
				"key_id":     APIKeyFingerprint(conn.apiKey),
				"frame_type": frame.Type,
				"stream_id":  frame.RequestID,
			}))
		}
	}

	conn.mu.Lock()
	pending := make([]*tcpStream, 0, len(conn.streams))
	for _, stream := range conn.streams {
		pending = append(pending, stream)
	}
	conn.streams = make(map[string]*tcpStream)
	conn.mu.Unlock()

	for _, stream := range pending {
		stream.close(errors.New("tunnel connection closed"))
	}

	ts.removeConnectionByKey(connKey)
}
