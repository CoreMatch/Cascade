package backend

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	PreambleSize    = 24
	PreambleVersion = 1
)

var preambleMagic = [4]byte{'C', 'A', 'S', 'C'}

func NewSessionID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
	}
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]), nil
}

func EncodePreamble(sessionID string) ([]byte, error) {
	if len(sessionID) != 36 || sessionID[8] != '-' || sessionID[13] != '-' ||
		sessionID[18] != '-' || sessionID[23] != '-' {
		return nil, errors.New("invalid session ID")
	}
	compact := strings.ReplaceAll(sessionID, "-", "")
	raw, err := hex.DecodeString(compact)
	if err != nil || len(raw) != 16 {
		return nil, errors.New("invalid session ID")
	}
	if raw[6]&0xf0 != 0x40 || raw[8]&0xc0 != 0x80 {
		return nil, errors.New("session ID must be UUIDv4")
	}
	header := make([]byte, PreambleSize)
	copy(header[:4], preambleMagic[:])
	binary.BigEndian.PutUint16(header[4:6], PreambleVersion)
	binary.BigEndian.PutUint16(header[6:8], PreambleSize)
	copy(header[8:], raw)
	return header, nil
}

func WritePreamble(w io.Writer, sessionID string) error {
	header, err := EncodePreamble(sessionID)
	if err != nil {
		return err
	}
	for len(header) > 0 {
		n, err := w.Write(header)
		if err != nil {
			return err
		}
		header = header[n:]
	}
	return nil
}

func ReadPreamble(r io.Reader) (string, error) {
	header := make([]byte, PreambleSize)
	if _, err := io.ReadFull(r, header); err != nil {
		return "", err
	}
	if string(header[:4]) != string(preambleMagic[:]) {
		return "", errors.New("invalid backend preamble magic")
	}
	if version := binary.BigEndian.Uint16(header[4:6]); version != PreambleVersion {
		return "", fmt.Errorf("unsupported backend preamble version %d", version)
	}
	if size := binary.BigEndian.Uint16(header[6:8]); size != PreambleSize {
		return "", fmt.Errorf("invalid backend preamble size %d", size)
	}
	b := header[8:]
	if b[6]&0xf0 != 0x40 || b[8]&0xc0 != 0x80 {
		return "", errors.New("session ID is not UUIDv4")
	}
	compact := hex.EncodeToString(b)
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		compact[0:8], compact[8:12], compact[12:16], compact[16:20], compact[20:32]), nil
}
