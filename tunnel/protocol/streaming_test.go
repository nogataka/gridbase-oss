package protocol

import (
	"bytes"
	"testing"
)

func TestWriteAndReadStreamFrame(t *testing.T) {
	buffer := &bytes.Buffer{}
	requestID := "req-123"
	payload := RequestStartPayload{
		Method: "POST",
		Path:   "/api",
		Target: "http://local/api",
		Header: map[string][]string{
			"Content-Type": {"application/json"},
		},
		Subdomain: "demo",
	}

	if err := WriteStreamFrame(buffer, FrameRequestStart, requestID, payload); err != nil {
		t.Fatalf("WriteStreamFrame returned error: %v", err)
	}

	var decoded RequestStartPayload
	frame, err := ReadStreamFrame(buffer, &decoded)
	if err != nil {
		t.Fatalf("ReadStreamFrame returned error: %v", err)
	}

	if frame.Type != FrameRequestStart {
		t.Fatalf("unexpected frame type %s", frame.Type)
	}
	if frame.RequestID != requestID {
		t.Fatalf("unexpected request id %s", frame.RequestID)
	}
	if decoded.Method != payload.Method || decoded.Path != payload.Path || decoded.Target != payload.Target {
		t.Fatalf("decoded payload mismatch: %#v", decoded)
	}
}

func TestReadStreamFrameWithoutPayload(t *testing.T) {
	buffer := &bytes.Buffer{}
	if err := WriteStreamFrame(buffer, FrameRequestEnd, "req-1", nil); err != nil {
		t.Fatalf("WriteStreamFrame: %v", err)
	}

	frame, err := ReadStreamFrame(buffer, nil)
	if err != nil {
		t.Fatalf("ReadStreamFrame: %v", err)
	}

	if frame.Type != FrameRequestEnd {
		t.Fatalf("expected frame type request_end, got %s", frame.Type)
	}
	if frame.RequestID != "req-1" {
		t.Fatalf("unexpected request id %s", frame.RequestID)
	}
	if len(frame.Payload) != 0 {
		t.Fatalf("expected empty payload, got %d bytes", len(frame.Payload))
	}
}

func TestTCPFrameRoundTrip(t *testing.T) {
	buffer := &bytes.Buffer{}
	dataPayload := BodyChunkPayload{Data: []byte("hello")}
	if err := WriteStreamFrame(buffer, FrameData, "tcp-1", dataPayload); err != nil {
		t.Fatalf("WriteStreamFrame data: %v", err)
	}

	var decodedData BodyChunkPayload
	frame, err := ReadStreamFrame(buffer, &decodedData)
	if err != nil {
		t.Fatalf("ReadStreamFrame data: %v", err)
	}
	if frame.Type != FrameData {
		t.Fatalf("expected frame type data, got %s", frame.Type)
	}
	if frame.RequestID != "tcp-1" {
		t.Fatalf("unexpected request id %s", frame.RequestID)
	}
	if string(decodedData.Data) != "hello" {
		t.Fatalf("unexpected data payload %q", string(decodedData.Data))
	}

	closePayload := ClosePayload{Code: "EOF", Message: "client closed"}
	if err := WriteStreamFrame(buffer, FrameClose, "tcp-1", closePayload); err != nil {
		t.Fatalf("WriteStreamFrame close: %v", err)
	}

	var decodedClose ClosePayload
	frame, err = ReadStreamFrame(buffer, &decodedClose)
	if err != nil {
		t.Fatalf("ReadStreamFrame close: %v", err)
	}
	if frame.Type != FrameClose {
		t.Fatalf("expected frame type close, got %s", frame.Type)
	}
	if decodedClose.Code != "EOF" || decodedClose.Message != "client closed" {
		t.Fatalf("unexpected close payload %#v", decodedClose)
	}
}
