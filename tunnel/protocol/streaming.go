package protocol

import (
	"encoding/json"
	"fmt"
	"io"
)

// FrameType identifies the kind of streaming frame exchanged between server and client.
type FrameType string

const (
	// FrameRequestStart contains HTTP method, path, headers and metadata.
	FrameRequestStart FrameType = "request_start"
	// FrameRequestBody carries a chunk of request body bytes.
	FrameRequestBody FrameType = "request_body"
	// FrameRequestEnd signals the end of the request body stream.
	FrameRequestEnd FrameType = "request_end"

	// FrameResponseStart contains status code and response headers.
	FrameResponseStart FrameType = "response_start"
	// FrameResponseBody carries a chunk of response body bytes.
	FrameResponseBody FrameType = "response_body"
	// FrameResponseEnd signals the end of the response body stream.
	FrameResponseEnd FrameType = "response_end"

	// FrameError is emitted when a streaming error occurs.
	FrameError FrameType = "error"
	// FrameData carries raw byte chunks for TCP tunnel streams.
	FrameData FrameType = "data"
	// FrameClose indicates the termination of one side of a TCP stream.
	FrameClose FrameType = "close"
	// FramePing is a server-to-client keepalive frame. The client should ignore or
	// acknowledge it and continue its read loop.
	FramePing FrameType = "ping"
)

// StreamChunkSize defines the default size used when chunking stream payloads.
const StreamChunkSize = 32 * 1024

// StreamFrame is the top-level envelope used by streaming communications.
type StreamFrame struct {
	Type      FrameType       `json:"type"`
	RequestID string          `json:"request_id"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// RequestStartPayload captures request metadata for streaming transfer.
type RequestStartPayload struct {
	Method    string              `json:"method"`
	Path      string              `json:"path"`
	Target    string              `json:"target"`
	Header    map[string][]string `json:"header"`
	Subdomain string              `json:"subdomain"`
}

// ResponseStartPayload captures response metadata when streaming back to the proxy.
type ResponseStartPayload struct {
	StatusCode int                 `json:"status_code"`
	Header     map[string][]string `json:"header"`
	Error      string              `json:"error,omitempty"`
}

// BodyChunkPayload carries a chunk of bytes for either request/response bodies or raw TCP data.
type BodyChunkPayload struct {
	Data []byte `json:"data"`
}

// ErrorPayload is emitted when a streaming pipeline cannot be completed.
type ErrorPayload struct {
	Message string `json:"message"`
}

// ClosePayload provides context when a TCP stream is closed intentionally or due to an error.
type ClosePayload struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// WriteStreamFrame serialises a StreamFrame with the provided frame-specific payload.
func WriteStreamFrame(w io.Writer, frameType FrameType, requestID string, payload any) error {
	envelope := StreamFrame{
		Type:      frameType,
		RequestID: requestID,
	}

	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal payload: %w", err)
		}
		envelope.Payload = raw
	}

	return WriteMessage(w, envelope)
}

// ReadStreamFrame deserialises the next StreamFrame from the reader and optionally unmarshals the payload.
// Frames larger than DefaultMaxMessageBytes are rejected.
func ReadStreamFrame(r io.Reader, payloadDest any) (StreamFrame, error) {
	return ReadStreamFrameLimit(r, payloadDest, DefaultMaxMessageBytes)
}

// ReadStreamFrameLimit is ReadStreamFrame with an explicit per-frame size limit.
func ReadStreamFrameLimit(r io.Reader, payloadDest any, maxBytes int64) (StreamFrame, error) {
	var envelope StreamFrame
	if err := ReadMessageLimit(r, &envelope, maxBytes); err != nil {
		return StreamFrame{}, err
	}

	if payloadDest != nil && len(envelope.Payload) > 0 {
		if err := json.Unmarshal(envelope.Payload, payloadDest); err != nil {
			return StreamFrame{}, fmt.Errorf("unmarshal stream payload: %w", err)
		}
	}
	return envelope, nil
}
