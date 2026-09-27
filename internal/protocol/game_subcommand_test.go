package protocol

import (
	"reflect"
	"testing"
)

func TestSubCommandUnitAddPacket_EncodeDecode(t *testing.T) {
	packet := &SubCommandUnitAddPacket{
		Count:      10,
		UnitType:   "scout",
		X:          100.5,
		Y:          200.75,
		Owner:      "player1",
		ShouldSync: true,
	}

	w := NewWriter()
	if err := packet.Encode(w); err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	cmds, err := ParseSubCommands(w.Bytes())
	if err != nil {
		t.Fatalf("ParseSubCommands failed: %v", err)
	}

	if len(cmds) != 1 {
		t.Fatalf("Expected 1 command, got %d", len(cmds))
	}

	got, ok := cmds[0].(SubCommandUnitAddPacket)
	if !ok {
		t.Fatalf("Expected SubCommandUnitAddPacket, got %T", cmds[0])
	}

	if !reflect.DeepEqual(got, *packet) {
		t.Errorf("Decoded packet does not match original.\nGot: %+v\nWant: %+v", got, *packet)
	}
}

func TestSubCommandChatPacket_EncodeDecode(t *testing.T) {
	packet := &SubCommandChatPacket{
		Message: "Hello, world!",
	}

	w := NewWriter()
	if err := packet.Encode(w); err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	cmds, err := ParseSubCommands(w.Bytes())
	if err != nil {
		t.Fatalf("ParseSubCommands failed: %v", err)
	}

	if len(cmds) != 1 {
		t.Fatalf("Expected 1 command, got %d", len(cmds))
	}

	got, ok := cmds[0].(SubCommandChatPacket)
	if !ok {
		t.Fatalf("Expected SubCommandChatPacket, got %T", cmds[0])
	}

	if !reflect.DeepEqual(got, *packet) {
		t.Errorf("Decoded packet does not match original.\nGot: %+v\nWant: %+v", got, *packet)
	}
}

func TestSubCommandUnitMovePacket_EncodeDecode(t *testing.T) {
	packet := &SubCommandUnitMovePacket{
		UnitIDs: []int32{1, 2, 3, 4, 5},
		X:       123.45,
		Y:       678.90,
	}

	w := NewWriter()
	if err := packet.Encode(w); err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	cmds, err := ParseSubCommands(w.Bytes())
	if err != nil {
		t.Fatalf("ParseSubCommands failed: %v", err)
	}

	if len(cmds) != 1 {
		t.Fatalf("Expected 1 command, got %d", len(cmds))
	}

	got, ok := cmds[0].(SubCommandUnitMovePacket)
	if !ok {
		t.Fatalf("Expected SubCommandUnitMovePacket, got %T", cmds[0])
	}

	if !reflect.DeepEqual(got, *packet) {
		t.Errorf("Decoded packet does not match original.\nGot: %+v\nWant: %+v", got, *packet)
	}
}

func TestSubCommandServerUnitAddPacket_Encode(t *testing.T) {
	packet := &SubCommandServerUnitAddPacket{
		UnitID:   12345,
		UnitType: "tank",
		X:        50.0,
		Y:        60.0,
		Owner:    "team1",
	}

	w := NewWriter()
	if err := packet.Encode(w); err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	// ParseSubCommands currently returns error for server-only commands
	_, err := ParseSubCommands(w.Bytes())
	if err == nil {
		t.Error("Expected error when parsing server-only command SubCommandServerUnitAdd, but got nil")
	}
}

func TestSubCommandServerUnitMovePacket_Encode(t *testing.T) {
	packet := &SubCommandServerUnitMovePacket{
		UnitID: 67890,
		X:      150.0,
		Y:      250.0,
	}

	w := NewWriter()
	if err := packet.Encode(w); err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	// ParseSubCommands currently returns error for server-only commands
	_, err := ParseSubCommands(w.Bytes())
	if err == nil {
		t.Error("Expected error when parsing server-only command SubCommandServerUnitMove, but got nil")
	}
}

func TestParseSubCommands_Multiple(t *testing.T) {
	p1 := &SubCommandUnitAddPacket{
		Count:      5,
		UnitType:   "builder",
		X:          10,
		Y:          20,
		Owner:      "p1",
		ShouldSync: false,
	}
	
	w := NewWriter()
	if err := p1.Encode(w); err != nil {
		t.Fatalf("p1 Encode failed: %v", err)
	}
	
	p2 := &SubCommandUnitAddPacket{
		Count:      1,
		UnitType:   "soldier",
		X:          30,
		Y:          40,
		Owner:      "p2",
		ShouldSync: true,
	}
	if err := p2.Encode(w); err != nil {
		t.Fatalf("p2 Encode failed: %v", err)
	}

	cmds, err := ParseSubCommands(w.Bytes())
	if err != nil {
		t.Fatalf("ParseSubCommands failed: %v", err)
	}

	if len(cmds) != 2 {
		t.Fatalf("Expected 2 commands, got %d", len(cmds))
	}

	if !reflect.DeepEqual(cmds[0], *p1) {
		t.Errorf("cmd[0] mismatch.\nGot: %+v\nWant: %+v", cmds[0], *p1)
	}
	if !reflect.DeepEqual(cmds[1], *p2) {
		t.Errorf("cmd[1] mismatch.\nGot: %+v\nWant: %+v", cmds[1], *p2)
	}
}
