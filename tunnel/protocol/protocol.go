package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	// DefaultMaxMessageBytes is the largest length-prefixed message ReadMessage
	// accepts. A peer announcing more is rejected before any body is read, so a
	// forged length header can never make the receiver allocate that much memory.
	// Servers can override it per call with ReadMessageLimit.
	DefaultMaxMessageBytes int64 = 16 << 20

	// MaxHandshakeBytes bounds the unauthenticated hello messages. A ClientHello
	// or ServerHello is a few hundred bytes of JSON.
	MaxHandshakeBytes int64 = 64 << 10

	// preallocLimit is the biggest body allocated up front. Larger (but still
	// allowed) bodies are buffered while they arrive, so memory grows with the
	// bytes the peer really sends rather than with the length it announces.
	preallocLimit = 64 << 10
)

// ErrMessageTooLarge is returned when a message header announces more than the
// permitted number of bytes.
var ErrMessageTooLarge = errors.New("protocol: message exceeds size limit")

// ClientHello はクライアントが送信する握手メッセージ。
type ClientHello struct {
	APIKey   string `json:"api_key"`
	Mode     string `json:"mode,omitempty"`
	TunnelID string `json:"tunnel_id,omitempty"`
}

// ServerHello はサーバー側の認証応答。
type ServerHello struct {
	OK       bool   `json:"ok"`
	Message  string `json:"message,omitempty"`
	Mode     string `json:"mode,omitempty"`
	TunnelID string `json:"tunnel_id,omitempty"`
}

// RequestPayload は HTTP リクエスト情報。
type RequestPayload struct {
	Method    string              `json:"method"`
	Path      string              `json:"path"`
	Target    string              `json:"target"`
	Header    map[string][]string `json:"header"`
	Body      []byte              `json:"body"`
	Subdomain string              `json:"subdomain"`
}

// ResponsePayload は HTTP レスポンス情報。
type ResponsePayload struct {
	StatusCode int                 `json:"status_code"`
	Header     map[string][]string `json:"header"`
	Body       []byte              `json:"body"`
	Error      string              `json:"error,omitempty"`
}

// WriteMessage は長さヘッダー付き JSON を送信する。
func WriteMessage(w io.Writer, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	if int64(len(data)) > math.MaxUint32 {
		return fmt.Errorf("marshal payload: %w", ErrMessageTooLarge)
	}
	length := uint32(len(data))
	if err := binary.Write(w, binary.BigEndian, length); err != nil {
		return fmt.Errorf("write length: %w", err)
	}

	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}

// ReadMessage は長さヘッダー付き JSON を受信する。DefaultMaxMessageBytes を超える長さは拒否する。
func ReadMessage(r io.Reader, dest any) error {
	return ReadMessageLimit(r, dest, DefaultMaxMessageBytes)
}

// ReadMessageLimit は ReadMessage と同じだが、受け付ける最大バイト数を指定できる。
// 上限を超える長さヘッダーは本文を読む前に ErrMessageTooLarge で拒否する。
func ReadMessageLimit(r io.Reader, dest any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxMessageBytes
	}

	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return fmt.Errorf("read length: %w", err)
	}
	if int64(length) > maxBytes {
		return fmt.Errorf("%w: announced %d bytes, limit %d", ErrMessageTooLarge, length, maxBytes)
	}

	var buf []byte
	if int64(length) <= preallocLimit {
		buf = make([]byte, length)
		if _, err := io.ReadFull(r, buf); err != nil {
			return fmt.Errorf("read body: %w", err)
		}
	} else {
		var body bytes.Buffer
		n, err := io.CopyN(&body, r, int64(length))
		if err != nil {
			return fmt.Errorf("read body: %w (got %d of %d bytes)", err, n, length)
		}
		buf = body.Bytes()
	}

	if err := json.Unmarshal(buf, dest); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}
	return nil
}
