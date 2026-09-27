package protocol

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"math"
)

type Reader struct {
	data []byte
	off  int
}

func NewReader(data []byte) *Reader {
	return &Reader{data: data}
}

func (r *Reader) Remaining() int {
	return len(r.data) - r.off
}

func (r *Reader) Byte() (byte, error) {
	if r.off+1 > len(r.data) {
		return 0, io.ErrUnexpectedEOF
	}
	value := r.data[r.off]
	r.off++
	return value, nil
}

func (r *Reader) Bool() (bool, error) {
	value, err := r.Byte()
	return value != 0, err
}

func (r *Reader) Int32() (int32, error) {
	if r.off+4 > len(r.data) {
		return 0, io.ErrUnexpectedEOF
	}
	value := int32(binary.BigEndian.Uint32(r.data[r.off : r.off+4]))
	r.off += 4
	return value, nil
}

func (r *Reader) Int64() (int64, error) {
	if r.off+8 > len(r.data) {
		return 0, io.ErrUnexpectedEOF
	}
	value := int64(binary.BigEndian.Uint64(r.data[r.off : r.off+8]))
	r.off += 8
	return value, nil
}

func (r *Reader) Float32() (float32, error) {
	value, err := r.Int32()
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(uint32(value)), nil
}

func (r *Reader) Skip(n int) error {
	if r.off+n > len(r.data) {
		return io.ErrUnexpectedEOF
	}
	r.off += n
	return nil
}

func (r *Reader) String() (string, error) {
	if r.off+2 > len(r.data) {
		return "", io.ErrUnexpectedEOF
	}
	size := int(binary.BigEndian.Uint16(r.data[r.off : r.off+2]))
	r.off += 2
	if r.off+size > len(r.data) {
		return "", io.ErrUnexpectedEOF
	}
	value, err := decodeJavaUTF(r.data[r.off : r.off+size])
	if err != nil {
		return "", err
	}
	r.off += size
	return value, nil
}

func (r *Reader) MaybeString() (string, error) {
	present, err := r.Bool()
	if err != nil {
		return "", err
	}
	if !present {
		return "", nil
	}
	return r.String()
}

type Writer struct {
	buf bytes.Buffer
}

func NewWriter() *Writer {
	return &Writer{}
}

func (w *Writer) Bytes() []byte {
	return w.buf.Bytes()
}

func (w *Writer) Packet(packetType int32) Packet {
	body := append([]byte(nil), w.buf.Bytes()...)
	return Packet{Type: packetType, Body: body}
}

func (w *Writer) Byte(value byte) {
	w.buf.WriteByte(value)
}

func (w *Writer) Bool(value bool) {
	if value {
		w.buf.WriteByte(1)
		return
	}
	w.buf.WriteByte(0)
}

func (w *Writer) Int32(value int32) {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(value))
	w.buf.Write(buf[:])
}

func (w *Writer) Int64(value int64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(value))
	w.buf.Write(buf[:])
}

func (w *Writer) Float32(value float32) {
	w.Int32(int32(math.Float32bits(value)))
}

func (w *Writer) Raw(value []byte) {
	w.buf.Write(value)
}

func (w *Writer) String(value string) error {
	payload, err := appendJavaUTF(nil, value)
	if err != nil {
		return err
	}
	w.buf.Write(payload)
	return nil
}

func (w *Writer) MaybeString(value string) error {
	if value == "" {
		w.Bool(false)
		return nil
	}
	w.Bool(true)
	return w.String(value)
}

func (w *Writer) MaybeInt32(value int32, exists bool) {
	w.Bool(exists)
	if exists {
		w.Int32(value)
	}
}

func (w *Writer) GzipSection(name string, fn func(section *Writer) error) error {
	section := NewWriter()
	if err := fn(section); err != nil {
		return err
	}

	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(section.Bytes()); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}

	if err := w.String(name); err != nil {
		return err
	}
	w.Int32(int32(compressed.Len()))
	w.Raw(compressed.Bytes())
	return nil
}

func DecodeGameCommandPacket(body []byte) (GameCommandPacket, error) {
	r := NewReader(body)
	if err := r.Skip(4); err != nil {
		return GameCommandPacket{}, err
	}
	return GameCommandPacket{
		Packet: Packet{Type: TypeGameCommand, Body: body},
		Data:   r.data[r.off:],
	}, nil
}

func DecodePromptReply(body []byte) (string, error) {
	r := NewReader(body)
	if err := r.Skip(5); err != nil {
		return "", err
	}
	return r.String()
}

func EncodePrompt(message string) (Packet, error) {
	w := NewWriter()
	w.Byte(1)
	w.Int32(5)
	if err := w.String(message); err != nil {
		return Packet{}, err
	}
	return w.Packet(TypeRelayPrompt), nil
}

func EncodeRelayVersionInfo(version int32) Packet {
	w := NewWriter()
	w.Byte(0)
	w.Int32(version)
	w.Int32(1)
	w.Bool(false)
	return w.Packet(TypeRelayVersionInfo)
}

func EncodeStartGame() (Packet, error) {
	w := NewWriter()
	// The start game packet typically has no body.
	return w.Packet(TypeStartGame), nil
}

func EncodeChat(from, message string) (Packet, error) {
	w := NewWriter()
	if err := w.String(from); err != nil {
		return Packet{}, err
	}
	if err := w.String(message); err != nil {
		return Packet{}, err
	}
	return w.Packet(TypeChatBroadcast), nil
}

func EncodeSystemMessage(message string) (Packet, error) {
	return EncodeChat("SERVER", message)
}

func DecodeChatReceive(body []byte) (string, error) {
	r := NewReader(body)
	return r.String()
}

func DecodeRegister(body []byte) (name, playerID string, err error) {
	r := NewReader(body)
	if _, err = r.String(); err != nil {
		return "", "", err
	}
	if err = r.Skip(12); err != nil {
		return "", "", err
	}
	if name, err = r.String(); err != nil {
		return "", "", err
	}
	if _, err = r.MaybeString(); err != nil {
		return "", "", err
	}
	if _, err = r.String(); err != nil {
		return "", "", err
	}
	playerID, err = r.String()
	return name, playerID, err
}

type PreregisterInfo struct {
	PacketVersion int32
	ClientVersion int32
	Query         string
	PlayerName    string
}

func DecodePreregister(body []byte) (PreregisterInfo, error) {
	r := NewReader(body)
	var info PreregisterInfo
	if _, err := r.String(); err != nil {
		return info, err
	}
	packetVersion, err := r.Int32()
	if err != nil {
		return info, err
	}
	info.PacketVersion = packetVersion
	clientVersion, err := r.Int32()
	if err != nil {
		return info, err
	}
	info.ClientVersion = clientVersion
	if packetVersion >= 1 {
		if err := r.Skip(4); err != nil {
			return info, err
		}
	}
	if packetVersion >= 2 {
		query, err := r.MaybeString()
		if err != nil {
			return info, err
		}
		info.Query = query
	}
	if packetVersion >= 3 {
		playerName, err := r.String()
		if err != nil {
			return info, err
		}
		info.PlayerName = playerName
	}
	return info, nil
}

func EncodeRelayBecomeServer(uuid string, isOwner bool, roomCode string, version int) (Packet, error) {
	w := NewWriter()
	// Always use version 2 for RELAY_BECOME_SERVER if possible, as it's more complete
	w.Byte(2) // Packet Version
	w.Bool(true)
	w.Bool(true)
	w.Bool(true)
	if err := w.String(uuid); err != nil {
		return Packet{}, err
	}
	w.Bool(false) // isMod
	w.Bool(true)  // showPublicly
	w.Bool(true)
	if err := w.String("Relay: " + roomCode); err != nil {
		return Packet{}, err
	}
	w.Bool(false) // useMulticast
	if err := w.MaybeString(uuid); err != nil {
		return Packet{}, err
	}
	return w.Packet(TypeRelayBecomeServer), nil
}

func EncodeRelayPow(resultInt int32) Packet {
	w := NewWriter()
	w.Int32(resultInt)
	w.Int32(0)     // authenticateType = 0 (No PoW required)
	w.Bool(false)  // hasInitInt1
	w.Bool(false)  // hasInitInt2
	w.Bool(false)  // unknown_false
	return w.Packet(TypeRelayPow)
}

func RequireNoError(err error) {
	if err != nil {
		panic(fmt.Sprintf("protocol encode failed: %v", err))
	}
}
