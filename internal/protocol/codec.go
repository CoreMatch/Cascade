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
	buf      bytes.Buffer
	blocks   []blockEntry
	blockBuf *Writer // non-nil when inside a named block
}

type blockEntry struct {
	name       string
	compressed bool
	data       []byte
}

func NewWriter() *Writer {
	return &Writer{}
}

func (w *Writer) Bytes() []byte {
	if w.blockBuf != nil {
		return w.blockBuf.Bytes()
	}
	return w.buf.Bytes()
}

func (w *Writer) Packet(packetType int32) Packet {
	body := append([]byte(nil), w.buf.Bytes()...)
	for _, blk := range w.blocks {
		// Write block name (Java Modified UTF-8 string)
		payload, _ := appendJavaUTF(nil, blk.name)
		body = append(body, payload...)
		// Write block data length (big-endian int32)
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(blk.data)))
		body = append(body, lenBuf[:]...)
		// Write block data
		body = append(body, blk.data...)
	}
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

func (w *Writer) Short(value int16) {
	var buf [2]byte
	binary.BigEndian.PutUint16(buf[:], uint16(value))
	w.buf.Write(buf[:])
}

func (w *Writer) Enum(ordinal int32) {
	w.Int32(ordinal)
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

func (w *Writer) MustString(value string) {
	if err := w.String(value); err != nil {
		panic(fmt.Sprintf("protocol encode string failed: %v", err))
	}
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

// StartBlock begins a named block. Data written after this call goes into
// the block buffer until EndBlock is called. If compress is true, the block
// data is gzip-compressed when flushed.
func (w *Writer) StartBlock(name string, compress bool) {
	w.blockBuf = &Writer{}
	w.blocks = append(w.blocks, blockEntry{name: name, compressed: compress})
}

// EndBlock finalizes the current named block and appends it to the pending list.
func (w *Writer) EndBlock() error {
	if w.blockBuf == nil {
		return fmt.Errorf("EndBlock called without StartBlock")
	}
	blk := &w.blocks[len(w.blocks)-1]
	data := w.blockBuf.Bytes()
	if blk.compressed {
		var compressed bytes.Buffer
		gz := gzip.NewWriter(&compressed)
		if _, err := gz.Write(data); err != nil {
			return err
		}
		if err := gz.Close(); err != nil {
			return err
		}
		blk.data = compressed.Bytes()
	} else {
		blk.data = data
	}
	w.blockBuf = nil
	return nil
}

// GzipSection is a convenience wrapper around StartBlock/EndBlock.
func (w *Writer) GzipSection(name string, fn func(section *Writer) error) error {
	w.StartBlock(name, true)
	if err := fn(w.blockBuf); err != nil {
		return err
	}
	return w.EndBlock()
}

// BlockSection writes a named block without compression.
func (w *Writer) BlockSection(name string, fn func(section *Writer) error) error {
	w.StartBlock(name, false)
	if err := fn(w.blockBuf); err != nil {
		return err
	}
	return w.EndBlock()
}

// DecodeGameCommandPacket decodes a client-sent game command (type 20).
// The payload is wrapped in a gzip block named "c". We extract the raw bytes.
func DecodeGameCommandPacket(body []byte) (GameCommandPacket, error) {
	r := NewReader(body)
	// The client sends the command data wrapped in a "c" block
	// Read block name
	blockName, err := r.String()
	if err != nil {
		// Fallback: some clients may send raw data without block wrapping
		return GameCommandPacket{
			Packet: Packet{Type: TypeGameCommand, Body: body},
			Data:   body,
		}, nil
	}
	// Read block data length
	blockLen, err := r.Int32()
	if err != nil {
		return GameCommandPacket{}, err
	}
	if r.off+int(blockLen) > len(r.data) {
		return GameCommandPacket{}, io.ErrUnexpectedEOF
	}
	data := r.data[r.off : r.off+int(blockLen)]

	// If the block name is "c", it's a raw command block (not gzip-compressed)
	// We need to decompress if it's gzip
	if blockName == "c" {
		decompressed, derr := gunzipBytes(data)
		if derr == nil {
			data = decompressed
		}
		// If decompression fails, use raw data (it may not be compressed)
	}

	return GameCommandPacket{
		Packet: Packet{Type: TypeGameCommand, Body: body},
		Data:   data,
	}, nil
}

func gunzipBytes(data []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	return io.ReadAll(gr)
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
	// RW uses 172 as a threshold for new relay protocol
	if version >= 172 {
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
	} else {
		w.Byte(1) // Packet Version
		w.Bool(true)
		w.Bool(true)
		if err := w.MaybeString(uuid); err != nil {
			return Packet{}, err
		}
		w.Bool(false) // useMods
		w.Bool(true)  // showPublicly
		if err := w.String("Relay: " + roomCode); err != nil {
			return Packet{}, err
		}
		w.Bool(false) // useMulticast
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

// EncodeTickEmpty generates an empty TICK packet (no commands, just advance the tick counter).
func EncodeTickEmpty(tick int32) Packet {
	w := NewWriter()
	w.Int32(tick)
	w.Int32(0) // command count = 0
	return w.Packet(TypeTick)
}

// EncodeTickCommands generates a TICK packet containing one or more game commands.
// Each command's raw bytes are wrapped in an uncompressed "c" block.
func EncodeTickCommands(tick int32, commands [][]byte) Packet {
	w := NewWriter()
	w.Int32(tick)
	w.Int32(int32(len(commands)))
	for _, cmd := range commands {
		w.StartBlock("c", false)
		w.blockBuf.Raw(cmd)
		w.EndBlock()
	}
	return w.Packet(TypeTick)
}

// EncodeKick generates a KICK packet with a reason string.
func EncodeKick(reason string) Packet {
	w := NewWriter()
	RequireNoError(w.String(reason))
	return w.Packet(TypeKick)
}

// EncodeHeartbeat generates a HEART_BEAT packet (server→client).
func EncodeHeartbeat() Packet {
	w := NewWriter()
	w.Int64(0) // random nonce
	w.Byte(0)
	return w.Packet(TypeHeartbeat)
}

// EncodePreRegisterResponse generates the REGISTER_CONNECTION (type 161) response.
func EncodePreRegisterResponse(handshakeID string, uuid string, clientVersion int32) Packet {
	w := NewWriter()
	RequireNoError(w.String(handshakeID))
	w.Int32(1) // packet version
	w.Int32(clientVersion)
	w.Int32(clientVersion)
	RequireNoError(w.String("com.corrodinggames.rts.server"))
	RequireNoError(w.String(uuid))
	w.Int32(javaHash("Dr @ 2022"))
	return w.Packet(TypePreregister)
}

// ModUnitDef is the minimal unit metadata needed for the customUnits block.
type ModUnitDef struct {
	ModName  string
	UnitName string
	UnitID   int32
}

// ServerInfoConfig holds the game settings needed to encode SERVER_INFO.
type ServerInfoConfig struct {
	MapType            int32
	MapName            string
	Credits            int32
	FogType            int32
	IsAdmin            bool
	MaxUnitsPerPlayer  int32
	StartingUnits      int32
	Income             float32
	DisableNuke        bool
	SharedControl      bool
	CustomUnits        []ModUnitDef // mod units to advertise; nil = empty
}

// EncodeServerInfo generates the SERVER_INFO (type 106) packet.
func EncodeServerInfo(cfg ServerInfoConfig, clientVersion int32) Packet {
	w := NewWriter()
	RequireNoError(w.String("com.corrodinggames.rts"))
	w.Int32(clientVersion)
	w.Int32(cfg.MapType)
	RequireNoError(w.String(cfg.MapName))
	w.Int32(cfg.Credits)
	w.Int32(cfg.FogType)
	w.Bool(true)
	w.Int32(1)
	w.Byte(4)
	w.Bool(false)
	w.Bool(cfg.IsAdmin)
	w.Int32(cfg.MaxUnitsPerPlayer)
	w.Int32(cfg.MaxUnitsPerPlayer)
	w.Int32(cfg.StartingUnits)
	w.Float32(cfg.Income)
	w.Bool(cfg.DisableNuke)
	w.Bool(false)
	w.Bool(true)

	// Custom units block
	w.StartBlock("customUnits", false)
	w.Int32(1)
	w.Int32(int32(len(cfg.CustomUnits)))
	for _, u := range cfg.CustomUnits {
		RequireNoError(w.String(u.UnitName))
		w.Int32(u.UnitID)
		w.Bool(true)
		if u.ModName == "" || u.ModName == "default" {
			w.Bool(false) // no mod name prefix
		} else {
			w.Bool(true)
			RequireNoError(w.String(u.ModName))
		}
		w.Int64(0)
		w.Int64(0)
	}
	w.EndBlock()

	w.Bool(cfg.SharedControl)
	w.Bool(false)
	w.Bool(false)
	return w.Packet(TypeServerInfo)
}

// EncodeStartGame generates the START_GAME (type 120) packet with map data.
func EncodeStartGame(mapName string, mapData []byte, isCustomMap bool) Packet {
	w := NewWriter()
	w.Byte(0)
	if isCustomMap {
		w.Int32(1) // custom map
		// Write map data as raw bytes (the client expects the file content)
		w.Int32(int32(len(mapData)))
		w.Raw(mapData)
		RequireNoError(w.String(mapName))
	} else {
		w.Int32(0) // official map
		RequireNoError(w.String("maps/skirmish/" + mapName))
	}
	w.Bool(false)
	return w.Packet(TypeStartGame)
}

// EncodeSyncChecksum generates the SYNC_CHECKSUM (type 30) packet.
func EncodeSyncChecksum(step int32) Packet {
	w := NewWriter()
	w.Int32(step)
	w.Int64(0)
	w.Int32(15) // checksum count
	for i := 0; i < 15; i++ {
		w.Int64(0)
	}
	return w.Packet(TypeSyncChecksum)
}

// EncodeReturnToBattleroom generates the RETURN_TO_BATTLEROOM (type 122) packet.
func EncodeReturnToBattleroom() Packet {
	w := NewWriter()
	w.Byte(0)
	return w.Packet(TypeReturnToBattleRoom)
}

// EncodeDisconnect generates the DISCONNECT (type 111) packet.
func EncodeDisconnect() Packet {
	w := NewWriter()
	return w.Packet(TypeDisconnect)
}

// EncodeRelayVersionInfo generates the RELAY_VERSION_INFO (type 163) packet.
func EncodeRelayVersionInfo(version int32) Packet {
	w := NewWriter()
	w.Byte(0)
	w.Int32(version)
	w.Int32(1)
	w.Bool(false)
	return w.Packet(TypeRelayVersionInfo)
}

func javaHash(s string) int32 {
	var h int32
	for _, r := range s {
		h = 31*h + int32(r)
	}
	return h
}
