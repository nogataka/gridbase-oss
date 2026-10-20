package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/nogataka/gridbase-oss/tunnel/protocol"
)

func TestPipeLocalToServerForwardsBytes(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	runner := NewRunner(logger, NewClientMetrics())

	writerConn, readerConn := net.Pipe()
	defer writerConn.Close()
	defer readerConn.Close()

	localServer, localClient := net.Pipe()
	defer localClient.Close()

	stream := newTCPClientStream("stream-1", localServer)
	writer := &frameWriter{conn: writerConn}

	go runner.pipeLocalToServer(context.Background(), stream, writer)

	if _, err := localClient.Write([]byte("hello")); err != nil {
		t.Fatalf("write local client: %v", err)
	}

	var chunk protocol.BodyChunkPayload
	frame, err := protocol.ReadStreamFrame(readerConn, &chunk)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if frame.Type != protocol.FrameData {
		t.Fatalf("expected FrameData, got %s", frame.Type)
	}
	if string(chunk.Data) != "hello" {
		t.Fatalf("unexpected data payload %q", string(chunk.Data))
	}

	if err := localClient.Close(); err != nil {
		t.Fatalf("close local client: %v", err)
	}

	var closePayload protocol.ClosePayload
	frame, err = protocol.ReadStreamFrame(readerConn, &closePayload)
	if err != nil {
		t.Fatalf("read close frame: %v", err)
	}
	if frame.Type != protocol.FrameClose {
		t.Fatalf("expected FrameClose, got %s", frame.Type)
	}
	if closePayload.Code != "client_eof" {
		t.Fatalf("unexpected close code %q", closePayload.Code)
	}

	time.Sleep(50 * time.Millisecond)
	if stream.bytesOut.Load() == 0 {
		t.Fatalf("expected bytesOut to be tracked")
	}

	select {
	case res := <-stream.done:
		if !errors.Is(res.err, io.EOF) {
			t.Fatalf("expected io.EOF, got %v", res.err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stream completion")
	}
}

func TestHandleTCPDataWriteFailureSendsErrorFrame(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	runner := NewRunner(logger, NewClientMetrics())

	writerConn, readerConn := net.Pipe()

	brokenConn, peer := net.Pipe()
	_ = peer.Close()

	stream := newTCPClientStream("stream-1", brokenConn)
	writer := &frameWriter{conn: writerConn}
	manager := newTCPStreamManager()
	manager.add(stream)

	type frameResult struct {
		frame   protocol.StreamFrame
		payload protocol.ErrorPayload
		err     error
	}
	resultCh := make(chan frameResult, 1)
	go func() {
		var payload protocol.ErrorPayload
		frame, err := protocol.ReadStreamFrame(readerConn, &payload)
		resultCh <- frameResult{frame: frame, payload: payload, err: err}
	}()

	payload, err := json.Marshal(protocol.BodyChunkPayload{Data: []byte("ping")})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	frame := protocol.StreamFrame{
		Type:      protocol.FrameData,
		RequestID: "stream-1",
		Payload:   payload,
	}

	if err := runner.handleTCPData(frame, writer, manager); err != nil {
		t.Fatalf("handleTCPData: %v", err)
	}

	select {
	case res := <-resultCh:
		if res.err != nil {
			t.Fatalf("read error frame: %v", res.err)
		}
		if res.frame.Type != protocol.FrameError {
			t.Fatalf("expected FrameError, got %s", res.frame.Type)
		}
		if res.payload.Message == "" || !strings.Contains(res.payload.Message, "closed") {
			t.Fatalf("unexpected error payload: %q", res.payload.Message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for error frame")
	}

	if err := writerConn.Close(); err != nil {
		t.Fatalf("close writerConn: %v", err)
	}
	if err := readerConn.Close(); err != nil {
		t.Fatalf("read error frame: %v", err)
	}

	if _, ok := manager.load("stream-1"); ok {
		t.Fatalf("expected stream to be removed after failure")
	}
}

func TestServeTCPUnexpectedFrameReturnsFatal(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	runner := NewRunner(logger, NewClientMetrics())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := &ClientConfig{}
	cfg.Heartbeat.Timeout = NewDuration(2 * time.Second)

	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	result := make(chan error, 1)
	go func() {
		result <- runner.serveTCP(ctx, serverConn, cfg, protocol.ServerHello{})
	}()

	if err := protocol.WriteStreamFrame(clientConn, protocol.FrameResponseStart, "stream-1", nil); err != nil {
		t.Fatalf("write unexpected frame: %v", err)
	}

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected fatal error, got nil")
		}
		if !isFatalError(err) {
			t.Fatalf("expected fatal error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveTCP did not return fatal error")
	}
}
