package backend

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestPreambleRoundTrip(t *testing.T) {
	id, err := NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodePreamble(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != PreambleSize || binary.BigEndian.Uint16(encoded[4:6]) != PreambleVersion {
		t.Fatalf("unexpected preamble bytes: %x", encoded)
	}
	decoded, err := ReadPreamble(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if decoded != id {
		t.Fatalf("session ID round trip = %s, want %s", decoded, id)
	}
}

func TestPreambleRejectsInvalidHeader(t *testing.T) {
	header, err := EncodePreamble("01020304-0506-4708-890a-0b0c0d0e0f10")
	if err != nil {
		t.Fatal(err)
	}
	header[5] = 0
	if _, err := ReadPreamble(bytes.NewReader(header)); err == nil {
		t.Fatal("expected unsupported version error")
	}
}
