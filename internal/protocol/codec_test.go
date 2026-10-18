package protocol

import (
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

func TestEncodeHeartbeat(t *testing.T) {
	p := EncodeHeartbeat()
	if p.Type != TypeHeartbeat {
		t.Errorf("expected type %d, got %d", TypeHeartbeat, p.Type)
	}
}

func TestEncodeReturnToBattleroom(t *testing.T) {
	p := EncodeReturnToBattleroom()
	if p.Type != TypeReturnToBattleRoom {
		t.Errorf("expected type %d, got %d", TypeReturnToBattleRoom, p.Type)
	}
}

func TestEncodeDisconnect(t *testing.T) {
	p := EncodeDisconnect()
	if p.Type != TypeDisconnect {
		t.Errorf("expected type %d, got %d", TypeDisconnect, p.Type)
	}
}

func TestEncodeSyncChecksum(t *testing.T) {
	p := EncodeSyncChecksum(100)
	if p.Type != TypeSyncChecksum {
		t.Errorf("expected type %d, got %d", TypeSyncChecksum, p.Type)
	}
}

func TestEncodeTickEmpty(t *testing.T) {
	p := EncodeTickEmpty(50)
	if p.Type != TypeTick {
		t.Errorf("expected type %d, got %d", TypeTick, p.Type)
	}

	// Verify we can decode it
	r := NewReader(p.Body)
	step, err := r.Int32()
	if err != nil {
		t.Fatalf("failed to read step: %v", err)
	}
	if step != 50 {
		t.Errorf("expected step 50, got %d", step)
	}

	count, err := r.Int32()
	if err != nil {
		t.Fatalf("failed to read count: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}
}

func TestEncodeTickCommands(t *testing.T) {
	cmd1 := []byte{1, 2, 3}
	cmd2 := []byte{4, 5, 6}
	p := EncodeTickCommands(100, [][]byte{cmd1, cmd2})
	if p.Type != TypeTick {
		t.Errorf("expected type %d, got %d", TypeTick, p.Type)
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

func TestEncodeServerInfo(t *testing.T) {
	cfg := ServerInfoConfig{
		MapType:           0,
		MapName:           "test.tmx",
		Credits:           1000,
		FogType:           2,
		IsAdmin:           true,
		MaxUnitsPerPlayer: 200,
		StartingUnits:     1,
		Income:            1.5,
		DisableNuke:       false,
		SharedControl:     false,
		CustomUnits:       nil,
	}
	p := EncodeServerInfo(cfg, 151)
	if p.Type != TypeServerInfo {
		t.Errorf("expected type %d, got %d", TypeServerInfo, p.Type)
	}
	if len(p.Body) == 0 {
		t.Error("expected non-empty body")
	}
}

func TestEncodeStartGame(t *testing.T) {
	// Test with official map
	p := EncodeStartGame("test.tmx", nil, false)
	if p.Type != TypeStartGame {
		t.Errorf("expected type %d, got %d", TypeStartGame, p.Type)
	}

	// Test with custom map
	mapData := []byte("custom map data")
	p = EncodeStartGame("custom.tmx", mapData, true)
	if p.Type != TypeStartGame {
		t.Errorf("expected type %d, got %d", TypeStartGame, p.Type)
	}
}

func TestEncodeSyncPullSave(t *testing.T) {
	p := EncodeSyncPullSave(100, nil)
	if p.Type != TypeSync {
		t.Errorf("expected type %d, got %d", TypeSync, p.Type)
	}
}

func TestEncodeSyncSendSave(t *testing.T) {
	saveData := []byte("test save data")
	p := EncodeSyncSendSave(100, saveData)
	if p.Type != TypeSync {
		t.Errorf("expected type %d, got %d", TypeSync, p.Type)
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

func TestWriterBlockSection(t *testing.T) {
	w := NewWriter()
	w.Int32(1)

	err := w.BlockSection("test", func(section *Writer) error {
		section.Int32(42)
		return nil
	})
	if err != nil {
		t.Fatalf("failed to write block section: %v", err)
	}

	p := w.Packet(100)
	if p.Type != 100 {
		t.Errorf("expected type 100, got %d", p.Type)
	}
}

func TestWriterGzipSection(t *testing.T) {
	w := NewWriter()
	w.Int32(1)

	err := w.GzipSection("test", func(section *Writer) error {
		section.Int32(42)
		return nil
	})
	if err != nil {
		t.Fatalf("failed to write gzip section: %v", err)
	}

	p := w.Packet(100)
	if p.Type != 100 {
		t.Errorf("expected type 100, got %d", p.Type)
	}
}

func TestDecodePromptReply(t *testing.T) {
	w := NewWriter()
	w.Byte(0)
	w.Int32(0)
	_ = w.String("test room")
	body := w.Bytes()

	msg, err := DecodePromptReply(body)
	if err != nil {
		t.Fatalf("failed to decode prompt reply: %v", err)
	}
	if msg != "test room" {
		t.Errorf("expected 'test room', got '%s'", msg)
	}
}

func TestEncodePrompt(t *testing.T) {
	p, err := EncodePrompt("enter room code")
	if err != nil {
		t.Fatalf("failed to encode prompt: %v", err)
	}
	if p.Type != TypeRelayPrompt {
		t.Errorf("expected type %d, got %d", TypeRelayPrompt, p.Type)
	}
}