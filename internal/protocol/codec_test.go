package protocol

import (
	"bytes"
	"testing"
)

func TestEncodeKick(t *testing.T) {
	p := EncodeKick("test reason")
	if p.Type != TypeKick {
		t.Errorf("expected type %d, got %d", TypeKick, p.Type)
	}
	if len(p.Body) == 0 {
		t.Error("expected non-empty body")
	}
}

func TestReadPacketRawPreservesFrame(t *testing.T) {
	packet := Packet{Type: TypeChatBroadcast, Body: []byte{0x01, 0x02, 0xff}}
	var encoded bytes.Buffer
	if err := WritePacket(&encoded, packet); err != nil {
		t.Fatal(err)
	}
	expected := append([]byte(nil), encoded.Bytes()...)
	decoded, raw, err := ReadPacketRaw(bytes.NewReader(expected))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Type != packet.Type || !bytes.Equal(decoded.Body, packet.Body) {
		t.Fatalf("decoded packet mismatch: %+v", decoded)
	}
	if !bytes.Equal(raw, expected) {
		t.Fatalf("raw frame changed: got %x, want %x", raw, expected)
	}
}

func TestEncodeSystemMessage(t *testing.T) {
	p, err := EncodeSystemMessage("test message")
	if err != nil {
		t.Fatalf("failed to encode system message: %v", err)
	}
	if p.Type != TypeChatBroadcast {
		t.Errorf("expected type %d, got %d", TypeChatBroadcast, p.Type)
	}
}

func TestEncodeChat(t *testing.T) {
	p, err := EncodeChat("player1", "hello")
	if err != nil {
		t.Fatalf("failed to encode chat: %v", err)
	}
	if p.Type != TypeChatBroadcast {
		t.Errorf("expected type %d, got %d", TypeChatBroadcast, p.Type)
	}
}

func TestDecodeChatReceive(t *testing.T) {
	// Create a chat message
	w := NewWriter()
	_ = w.String("hello world")
	body := w.Bytes()

	msg, err := DecodeChatReceive(body)
	if err != nil {
		t.Fatalf("failed to decode chat: %v", err)
	}
	if msg != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", msg)
	}
}

func TestWriterReader(t *testing.T) {
	w := NewWriter()
	w.Byte(42)
	w.Bool(true)
	w.Int32(12345)
	w.Int64(67890)
	w.Float32(3.14)
	_ = w.String("test string")

	body := w.Bytes()
	r := NewReader(body)

	b, err := r.Byte()
	if err != nil {
		t.Fatalf("failed to read byte: %v", err)
	}
	if b != 42 {
		t.Errorf("expected 42, got %d", b)
	}

	bl, err := r.Bool()
	if err != nil {
		t.Fatalf("failed to read bool: %v", err)
	}
	if !bl {
		t.Error("expected true")
	}

	i32, err := r.Int32()
	if err != nil {
		t.Fatalf("failed to read int32: %v", err)
	}
	if i32 != 12345 {
		t.Errorf("expected 12345, got %d", i32)
	}

	i64, err := r.Int64()
	if err != nil {
		t.Fatalf("failed to read int64: %v", err)
	}
	if i64 != 67890 {
		t.Errorf("expected 67890, got %d", i64)
	}

	f32, err := r.Float32()
	if err != nil {
		t.Fatalf("failed to read float32: %v", err)
	}
	if f32 < 3.13 || f32 > 3.15 {
		t.Errorf("expected ~3.14, got %f", f32)
	}

	s, err := r.String()
	if err != nil {
		t.Fatalf("failed to read string: %v", err)
	}
	if s != "test string" {
		t.Errorf("expected 'test string', got '%s'", s)
	}
}
