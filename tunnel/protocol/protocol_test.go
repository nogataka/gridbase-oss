package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

func TestClientHelloJSONCompatibility(t *testing.T) {
	legacyJSON := `{"api_key":"legacy-key"}`
	var hello ClientHello
	if err := json.Unmarshal([]byte(legacyJSON), &hello); err != nil {
		t.Fatalf("unexpected error decoding legacy JSON: %v", err)
	}
	if hello.APIKey != "legacy-key" {
		t.Fatalf("unexpected api key %q", hello.APIKey)
	}
	if hello.Mode != "" {
		t.Fatalf("expected empty mode, got %q", hello.Mode)
	}
	if hello.TunnelID != "" {
		t.Fatalf("expected empty tunnel id, got %q", hello.TunnelID)
	}

	hello = ClientHello{APIKey: "new-key", Mode: "tcp", TunnelID: "tunnel-123"}
	data, err := json.Marshal(hello)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var roundTrip ClientHello
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("round-trip decode failed: %v", err)
	}
	if roundTrip.Mode != "tcp" || roundTrip.TunnelID != "tunnel-123" {
		t.Fatalf("unexpected round-trip values: %+v", roundTrip)
	}
}

func TestServerHelloJSONOmitempty(t *testing.T) {
	msg := ServerHello{OK: true, Message: "welcome"}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	payload := string(data)
	if strings.Contains(payload, "mode") || strings.Contains(payload, "tunnel_id") {
		t.Fatalf("expected optional fields to be omitted, got %s", payload)
	}

	withMode := ServerHello{
		OK:       true,
		Mode:     "tcp",
		TunnelID: "tunnel-99",
	}
	data, err = json.Marshal(withMode)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var decoded ServerHello
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unexpected decode error: %v", err)
	}
	if decoded.Mode != "tcp" || decoded.TunnelID != "tunnel-99" {
		t.Fatalf("unexpected decoded values: %+v", decoded)
	}
}

func TestWriteStreamFrameMarshalError(t *testing.T) {
	var buffer bytes.Buffer
	payload := make(chan int)
	err := WriteStreamFrame(&buffer, FrameData, "req-1", payload)
	if err == nil {
		t.Fatal("expected marshal error but got nil")
	}
	if !strings.Contains(err.Error(), "marshal payload") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestReadStreamFrameInvalidEnvelope(t *testing.T) {
	var buffer bytes.Buffer
	if err := binary.Write(&buffer, binary.BigEndian, uint32(5)); err != nil {
		t.Fatalf("write length: %v", err)
	}
	if _, err := buffer.Write([]byte("{")); err != nil {
		t.Fatalf("write body: %v", err)
	}
	_, err := ReadStreamFrame(&buffer, nil)
	if err == nil {
		t.Fatal("expected read error but got nil")
	}
	if !strings.Contains(err.Error(), "read body") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
