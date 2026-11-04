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
	valid, err := EncodePreamble("01020304-0506-4708-890a-0b0c0d0e0f10")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func([]byte)
	}{
		{"bad magic", func(h []byte) { copy(h[:4], "XXXX") }},
		{"unsupported version", func(h []byte) { h[5] = 0 }},
		{"bad header size", func(h []byte) { h[7] = 0xff }},
		{"session ID is not UUIDv4", func(h []byte) { h[8+6] &^= 0x40 }},
		{"truncated header", func(h []byte) { h[0] = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			header := append([]byte(nil), valid...)
			test.mutate(header)
			if test.name == "truncated header" {
				header = header[:10]
			}
			if _, err := ReadPreamble(bytes.NewReader(header)); err == nil {
				t.Fatalf("expected %s to be rejected", test.name)
			}
		})
	}
}
