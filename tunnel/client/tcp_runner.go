package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

type frameWriter struct {
	conn net.Conn
	mu   sync.Mutex
}

func (fw *frameWriter) WriteFrame(frameType protocol.FrameType, requestID string, payload any) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	return protocol.WriteStreamFrame(fw.conn, frameType, requestID, payload)
}

type streamResult struct {
	bytesOut int64
	err      error
}

type tcpClientStream struct {
	id        string
	localConn net.Conn
	done      chan streamResult
	bytesIn   atomic.Int64
	bytesOut  atomic.Int64
}

func newTCPClientStream(id string, conn net.Conn) *tcpClientStream {
	return &tcpClientStream{
		id:        id,
		localConn: conn,
		done:      make(chan streamResult, 1),
	}
}

type tcpStreamManager struct {
	streams map[string]*tcpClientStream
	mu      sync.Mutex
}

func newTCPStreamManager() *tcpStreamManager {
	return &tcpStreamManager{
		streams: make(map[string]*tcpClientStream),
	}
}

func (m *tcpStreamManager) add(stream *tcpClientStream) {
	m.mu.Lock()
	m.streams[stream.id] = stream
	m.mu.Unlock()
}

func (m *tcpStreamManager) load(id string) (*tcpClientStream, bool) {
	m.mu.Lock()
	stream, ok := m.streams[id]
	m.mu.Unlock()
	return stream, ok
}

func (m *tcpStreamManager) remove(id string) *tcpClientStream {
	m.mu.Lock()
	stream := m.streams[id]
	if stream != nil {
		delete(m.streams, id)
	}
	m.mu.Unlock()
	return stream
}

func (m *tcpStreamManager) drainAll() []*tcpClientStream {
	m.mu.Lock()
	streams := make([]*tcpClientStream, 0, len(m.streams))
	for _, stream := range m.streams {
		streams = append(streams, stream)
	}
	m.streams = make(map[string]*tcpClientStream)
	m.mu.Unlock()
	return streams
}

func (r *Runner) serveTCP(ctx context.Context, conn net.Conn, cfg *ClientConfig, hello protocol.ServerHello) error {
	heartbeat := cfg.Heartbeat.Timeout.Duration
	writer := &frameWriter{conn: conn}
	manager := newTCPStreamManager()

	tunnelID := strings.TrimSpace(cfg.TunnelID)
	if hello.TunnelID != "" {
		tunnelID = hello.TunnelID
	}

	defer func() {
		for _, stream := range manager.drainAll() {
			_ = stream.localConn.Close()
			select {
			case res := <-stream.done:
				if res.err != nil && !errors.Is(res.err, context.Canceled) && !errors.Is(res.err, io.EOF) {
					r.logger.Printf("tcp stream ended with error stream_id=%s err=%v", stream.id, res.err)
				}
			default:
			}
		}
	}()

	for {
		if ctx.Err() != nil {
			return context.Canceled
		}

		if err := conn.SetReadDeadline(time.Now().Add(heartbeat)); err != nil {
			r.logger.Printf("warn: set tcp read deadline: %v", err)
		}

		frame, err := protocol.ReadStreamFrame(conn, nil)
		if err != nil {
			if isTimeoutErr(err) {
				return errHeartbeatTimeout
			}
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return errStreamShutdown
			}
			return fmt.Errorf("read tcp frame: %w", err)
		}

		switch frame.Type {
		case protocol.FrameRequestStart:
			if err := r.handleTCPStart(ctx, frame, cfg, writer, manager, tunnelID); err != nil {
				return err
			}
		case protocol.FrameData:
			if err := r.handleTCPData(frame, writer, manager); err != nil {
				return err
			}
		case protocol.FramePing:
			continue
		case protocol.FrameClose:
			r.finalizeTCPStream(frame.RequestID, nil, manager)
		case protocol.FrameError:
			var payload protocol.ErrorPayload
			if len(frame.Payload) > 0 {
				if err := json.Unmarshal(frame.Payload, &payload); err != nil {
					payload.Message = fmt.Sprintf("decode error: %v", err)
				}
			}
			var streamErr error
			if payload.Message != "" {
				streamErr = errors.New(payload.Message)
			}
			r.finalizeTCPStream(frame.RequestID, streamErr, manager)
		default:
			return newFatalError(fmt.Errorf("unexpected frame type %s in tcp mode", frame.Type))
		}
	}
}

func (r *Runner) handleTCPStart(ctx context.Context, frame protocol.StreamFrame, cfg *ClientConfig, writer *frameWriter, manager *tcpStreamManager, tunnelID string) error {
	var payload protocol.RequestStartPayload
	if len(frame.Payload) > 0 {
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			r.logger.Printf("tcp stream start payload decode failed: %v", err)
		}
	}

	if payload.Subdomain != "" && tunnelID != "" && !strings.EqualFold(payload.Subdomain, tunnelID) {
		r.logger.Printf("tcp stream subdomain mismatch expected=%s got=%s", tunnelID, payload.Subdomain)
	}

	address := net.JoinHostPort(cfg.TCP.LocalHost, strconv.Itoa(cfg.TCP.LocalPort))
	localConn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		msg := fmt.Sprintf("dial local backend: %v", err)
		_ = writer.WriteFrame(protocol.FrameError, frame.RequestID, protocol.ErrorPayload{Message: msg})
		r.logger.Printf("tcp stream start failed request_id=%s err=%v", frame.RequestID, err)
		return nil
	}

	stream := newTCPClientStream(frame.RequestID, localConn)
	manager.add(stream)

	go r.pipeLocalToServer(ctx, stream, writer)
	return nil
}

func (r *Runner) handleTCPData(frame protocol.StreamFrame, writer *frameWriter, manager *tcpStreamManager) error {
	var payload protocol.BodyChunkPayload
	if len(frame.Payload) > 0 {
		if err := json.Unmarshal(frame.Payload, &payload); err != nil {
			r.logger.Printf("tcp data payload decode failed: %v", err)
			return nil
		}
	}
	if len(payload.Data) == 0 {
		return nil
	}

	stream, ok := manager.load(frame.RequestID)
	if !ok {
		r.logger.Printf("tcp data for unknown stream stream_id=%s", frame.RequestID)
		return nil
	}

	if _, err := stream.localConn.Write(payload.Data); err != nil {
		r.logger.Printf("tcp write to local backend failed stream_id=%s err=%v", frame.RequestID, err)
		_ = writer.WriteFrame(protocol.FrameError, frame.RequestID, protocol.ErrorPayload{Message: err.Error()})
		r.finalizeTCPStream(frame.RequestID, err, manager)
		return nil
	}

	stream.bytesIn.Add(int64(len(payload.Data)))
	return nil
}

func (r *Runner) finalizeTCPStream(requestID string, reason error, manager *tcpStreamManager) {
	stream := manager.remove(requestID)
	if stream == nil {
		return
	}
	_ = stream.localConn.Close()

	var result streamResult
	if stream.done != nil {
		select {
		case result = <-stream.done:
		case <-time.After(2 * time.Second):
			result = streamResult{err: errors.New("timeout waiting for stream shutdown")}
		}
	}

	bytesIn := stream.bytesIn.Load()
	bytesOut := stream.bytesOut.Load()
	if result.bytesOut > 0 {
		bytesOut = result.bytesOut
	}
	r.metrics.ObserveRequest(bytesIn, bytesOut)

	if result.err != nil && !errors.Is(result.err, context.Canceled) && !errors.Is(result.err, io.EOF) {
		reason = result.err
	}

	if reason != nil && !errors.Is(reason, context.Canceled) && reason.Error() != "" {
		r.logger.Printf("tcp stream closed with error stream_id=%s err=%v", requestID, reason)
	}
}

func (r *Runner) pipeLocalToServer(ctx context.Context, stream *tcpClientStream, writer *frameWriter) {
	var resultErr error
	defer func() {
		stream.done <- streamResult{
			bytesOut: stream.bytesOut.Load(),
			err:      resultErr,
		}
		close(stream.done)
	}()

	buffer := make([]byte, protocol.StreamChunkSize)

	for {
		if ctx.Err() != nil {
			if err := writer.WriteFrame(protocol.FrameClose, stream.id, protocol.ClosePayload{
				Code:    "context_cancelled",
				Message: ctx.Err().Error(),
			}); err != nil {
				resultErr = err
			} else {
				resultErr = ctx.Err()
			}
			return
		}

		n, err := stream.localConn.Read(buffer)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buffer[:n])
			if writeErr := writer.WriteFrame(protocol.FrameData, stream.id, protocol.BodyChunkPayload{Data: chunk}); writeErr != nil {
				resultErr = writeErr
				return
			}
			stream.bytesOut.Add(int64(n))
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				if writeErr := writer.WriteFrame(protocol.FrameClose, stream.id, protocol.ClosePayload{Code: "client_eof"}); writeErr != nil {
					resultErr = writeErr
				} else {
					resultErr = io.EOF
				}
				return
			}
			if writeErr := writer.WriteFrame(protocol.FrameError, stream.id, protocol.ErrorPayload{Message: err.Error()}); writeErr != nil {
				resultErr = writeErr
			} else {
				resultErr = err
			}
			return
		}
	}
}
