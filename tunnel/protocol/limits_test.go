package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"strings"
	"testing"
)

func lengthHeader(n uint32) *bytes.Buffer {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.BigEndian, n)
	return &b
}

func totalAlloc() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.TotalAlloc
}

// A forged length header must be rejected without allocating the announced size.
func TestReadMessageRejectsHugeLengthWithoutAllocating(t *testing.T) {
	for _, announced := range []uint32{0x7fffffff, 0xffffffff, uint32(DefaultMaxMessageBytes) + 1} {
		var dest ClientHello
		before := totalAlloc()
		err := ReadMessage(lengthHeader(announced), &dest)
		delta := totalAlloc() - before

		if !errors.Is(err, ErrMessageTooLarge) {
			t.Fatalf("length %#x: expected ErrMessageTooLarge, got %v", announced, err)
		}
		if delta > 1<<20 {
			t.Fatalf("length %#x: allocated %d bytes while rejecting the header (want < 1 MiB)", announced, delta)
		}
	}
}

func TestReadMessageLimitHandshakeBound(t *testing.T) {
	var dest ClientHello
	err := ReadMessageLimit(lengthHeader(uint32(MaxHandshakeBytes)+1), &dest, MaxHandshakeBytes)
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("expected ErrMessageTooLarge for handshake limit, got %v", err)
	}
}

// An allowed but large announced length must not be allocated up front: a peer
// that announces 10 MiB and sends nothing costs only what it actually sent.
func TestReadMessageDoesNotPreallocateAnnouncedBody(t *testing.T) {
	buf := lengthHeader(10 << 20)
	buf.WriteString(`{"api_key":"x"`) // truncated body

	var dest ClientHello
	before := totalAlloc()
	err := ReadMessageLimit(buf, &dest, 16<<20)
	delta := totalAlloc() - before

	if err == nil || !strings.Contains(err.Error(), "read body") {
		t.Fatalf("expected read body error for truncated message, got %v", err)
	}
	if delta > 1<<20 {
		t.Fatalf("allocated %d bytes for a truncated 10 MiB message (want < 1 MiB)", delta)
	}
}

func TestReadMessageAcceptsLargeMessageWithinLimit(t *testing.T) {
	want := strings.Repeat("a", 3<<20) // above the prealloc threshold
	var wire bytes.Buffer
	if err := WriteMessage(&wire, ClientHello{APIKey: want}); err != nil {
		t.Fatalf("write: %v", err)
	}

	var got ClientHello
	if err := ReadMessage(&wire, &got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.APIKey != want {
		t.Fatalf("round trip mismatch: got %d bytes want %d", len(got.APIKey), len(want))
	}
}

func TestReadStreamFrameLimitRejectsOversizedFrame(t *testing.T) {
	var wire bytes.Buffer
	if err := WriteStreamFrame(&wire, FrameData, "r", BodyChunkPayload{Data: make([]byte, 4096)}); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := ReadStreamFrameLimit(&wire, nil, 1024)
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("expected ErrMessageTooLarge, got %v", err)
	}
}
