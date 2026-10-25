package protocol

import (
	"encoding/binary"
	"fmt"
	"io"
)

const maxPacketSize = 50 * 1024 * 1024

func ReadPacket(r io.Reader) (Packet, error) {
	packet, _, err := ReadPacketRaw(r)
	return packet, err
}

// ReadPacketRaw reads one framed packet and also returns its original bytes.
// The proxy uses the raw frame to preserve client/backend protocol data exactly.
func ReadPacketRaw(r io.Reader) (Packet, []byte, error) {
	var header [8]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return Packet{}, nil, err
	}

	length := int(binary.BigEndian.Uint32(header[0:4]))
	packetType := int32(binary.BigEndian.Uint32(header[4:8]))
	if length < 0 || length > maxPacketSize {
		return Packet{}, nil, fmt.Errorf("invalid packet length: %d", length)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return Packet{}, nil, err
	}

	raw := make([]byte, 0, len(header)+len(body))
	raw = append(raw, header[:]...)
	raw = append(raw, body...)
	return Packet{Type: packetType, Body: body}, raw, nil
}

func WritePacket(w io.Writer, packet Packet) error {
	var header [8]byte
	binary.BigEndian.PutUint32(header[0:4], uint32(len(packet.Body)))
	binary.BigEndian.PutUint32(header[4:8], uint32(packet.Type))

	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(packet.Body) == 0 {
		return nil
	}
	_, err := w.Write(packet.Body)
	return err
}
