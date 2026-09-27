package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rwgin/internal/config"
	"rwgin/internal/protocol"
)

type Server struct {
	cfg      config.Config
	logger   *log.Logger
	rooms    *Manager
	listener net.Listener
}

func NewServer(cfg config.Config, logger *log.Logger) *Server {
	return &Server{
		cfg:    cfg,
		logger: logger,
		rooms:  NewManager(cfg),
	}
}

func (s *Server) Rooms() *Manager {
	return s.rooms
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.GameListenAddr)
	if err != nil {
		return err
	}
	s.listener = ln
	s.logger.Printf("game listener started on %s", s.cfg.GameListenAddr)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}

		session := newSession(s, conn)
		go session.run(ctx)
	}
}

type sessionState int32

const (
	stateInit sessionState = iota
	stateAwaitRegister
	stateAwaitRoomCode
	stateActive
	stateClosed
)

type Session struct {
	server *Server
	conn   net.Conn
	id     string

	send    chan protocol.Packet
	closeCh chan struct{}
	once    sync.Once

	state         atomic.Int32
	name          string
	playerID      string
	clientVersion int
	preregister   protocol.PreregisterInfo

	pendingRoomCode string

	roomMu sync.RWMutex
	room   *Room
	slot   int
}

var sessionCounter atomic.Uint64

// #region debug-point common:runtime-report
func debugReport(runID, hypothesisID, location, message string, data map[string]any) {
	envBytes, err := os.ReadFile(".dbg/rw-client-interop.env")
	if err != nil {
		return
	}
	url := "http://127.0.0.1:7777/event"
	sessionID := "rw-client-interop"
	for _, line := range strings.Split(string(envBytes), "\n") {
		if strings.HasPrefix(line, "DEBUG_SERVER_URL=") {
			url = strings.TrimPrefix(line, "DEBUG_SERVER_URL=")
		}
		if strings.HasPrefix(line, "DEBUG_SESSION_ID=") {
			sessionID = strings.TrimPrefix(line, "DEBUG_SESSION_ID=")
		}
	}
	body, err := json.Marshal(map[string]any{
		"sessionId":    sessionID,
		"runId":        runID,
		"hypothesisId": hypothesisID,
		"location":     location,
		"msg":          message,
		"data":         data,
		"ts":           time.Now().UnixMilli(),
	})
	if err != nil {
		return
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

func shouldDebugPacket(packetType int32) bool {
	switch packetType {
	case protocol.TypePreregisterReceive,
		protocol.TypePreregister,
		protocol.TypeRegisterPlayer,
		protocol.TypeHeartbeat,
		protocol.TypeHeartbeatResponse,
		protocol.TypeDisconnect,
		protocol.TypeRelayVersionInfo,
		protocol.TypeRelayPrompt,
		protocol.TypeRelayPromptReply,
		protocol.TypeRelayBecomeServer,
		protocol.TypeServerInfo,
		protocol.TypeTeamList,
		protocol.TypeStartGame:
		return true
	default:
		return false
	}
}

func debugRunID() string {
	return "post-fix"
}

// #endregion

func newSession(server *Server, conn net.Conn) *Session {
	id := fmt.Sprintf("s-%d", sessionCounter.Add(1))
	session := &Session{
		server:  server,
		conn:    conn,
		id:      id,
		send:    make(chan protocol.Packet, 32),
		closeCh: make(chan struct{}),
		slot:    -1,
	}
	session.state.Store(int32(stateInit))
	return session
}

func (s *Session) run(ctx context.Context) {
	s.server.logger.Printf("client connected: %s", s.conn.RemoteAddr())
	// #region debug-point A:session-connect
	debugReport(debugRunID(), "A", "internal/relay/server.go:run", "[DEBUG] session connected", map[string]any{
		"sessionId": s.id,
		"remote":    s.conn.RemoteAddr().String(),
	})
	// #endregion

	go s.writeLoop()
	defer s.close()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closeCh:
			return
		default:
		}

		_ = s.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		packet, err := protocol.ReadPacket(s.conn)
		if err != nil {
			// #region debug-point E:read-error-close
			debugReport(debugRunID(), "E", "internal/relay/server.go:run", "[DEBUG] read loop ended", map[string]any{
				"sessionId": s.id,
				"state":     s.state.Load(),
				"error":     err.Error(),
			})
			// #endregion
			return
		}
		// #region debug-point A:packet-in
		if shouldDebugPacket(packet.Type) {
			debugReport(debugRunID(), "A", "internal/relay/server.go:run", "[DEBUG] inbound packet", map[string]any{
				"sessionId": s.id,
				"type":      packet.Type,
				"size":      len(packet.Body),
			})
		}
		// #endregion
		// #region debug-point E:active-packet-in
		if sessionState(s.state.Load()) == stateActive {
			debugReport(debugRunID(), "E", "internal/relay/server.go:run", "[DEBUG] active session inbound packet", map[string]any{
				"sessionId": s.id,
				"type":      packet.Type,
				"size":      len(packet.Body),
			})
		}
		// #endregion

		if err := s.handlePacket(packet); err != nil {
			s.server.logger.Printf("session %s handle packet %d failed: %v", s.id, packet.Type, err)
			// #region debug-point E:handle-error-close
			debugReport(debugRunID(), "E", "internal/relay/server.go:run", "[DEBUG] handle packet failed", map[string]any{
				"sessionId": s.id,
				"type":      packet.Type,
				"error":     err.Error(),
			})
			// #endregion
			return
		}
	}
}

func (s *Session) writeLoop() {
	for {
		select {
		case <-s.closeCh:
			return
		case packet := <-s.send:
			// #region debug-point D:packet-out
			if shouldDebugPacket(packet.Type) {
				debugReport(debugRunID(), "D", "internal/relay/server.go:writeLoop", "[DEBUG] outbound packet", map[string]any{
					"sessionId": s.id,
					"type":      packet.Type,
					"size":      len(packet.Body),
				})
			}
			// #endregion
			_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := protocol.WritePacket(s.conn, packet); err != nil {
				s.close()
				return
			}
		}
	}
}

func (s *Session) ID() string {
	return s.playerID
}

func (s *Session) Name() string {
	return s.name
}

func (s *Session) handlePacket(packet protocol.Packet) error {
	switch packet.Type {
	case protocol.TypePreregisterReceive:
		return s.handlePreregister(packet.Body)
	case protocol.TypeRegisterPlayer:
		return s.handleRegister(packet.Body)
	case protocol.TypeRelayPowReceive:
		return s.handlePowReceive(packet.Body)
	case protocol.TypeRelayPromptReply:
		return s.handlePromptReply(packet.Body)
	case protocol.TypeHeartbeat:
		return s.Send(protocol.Packet{Type: protocol.TypeHeartbeatResponse})
	case protocol.TypeChatReceive:
		return s.handleChat(packet.Body)
	case protocol.TypeStartGame:
		return s.handleStartGame(packet)
	case protocol.TypeReturnToBattleRoom:
		return s.handleReturnToBattleRoom(packet)
	case protocol.TypeGameCommand:
		return s.handleGameCommand(packet)
	case protocol.TypeDisconnect:
		return errors.New("client requested disconnect")
	default:
		return nil
	}
}

func (s *Session) handlePreregister(body []byte) error {
	info, err := protocol.DecodePreregister(body)
	if err != nil {
		return err
	}
	s.preregister = info
	s.clientVersion = int(info.ClientVersion)
	s.name = info.PlayerName
	s.pendingRoomCode = info.Query

	s.server.logger.Printf("preregister: version=%d, client=%d, room=%s, name=%s", info.PacketVersion, info.ClientVersion, info.Query, info.PlayerName)

	// Reply with server info
	w := protocol.NewWriter()
	w.String(s.server.cfg.RelayHandshakeID)
	w.Int32(1) // Packet Version
	w.Int32(int32(s.clientVersion))
	w.Int32(int32(s.clientVersion))
	w.String("com.corrodinggames.rts.server")

	serverUUID := randomHex(16)
	if info.Query != "" {
		if room, ok := s.server.rooms.Get(info.Query); ok {
			serverUUID = room.ServerUUID
		}
	}
	w.String(serverUUID)
	w.Int32(javaHash("Dr @ 2022"))

	if err := s.Send(w.Packet(protocol.TypePreregister)); err != nil {
		return err
	}

	s.state.Store(int32(stateAwaitRegister))
	return nil
}

func (s *Session) handleRegister(body []byte) error {
	if sessionState(s.state.Load()) != stateAwaitRegister {
		decodedName, decodedPlayerID, decodeErr := protocol.DecodeRegister(body)
		// #region debug-point E:register-while-active
		debugReport(debugRunID(), "E", "internal/relay/server.go:handleRegister", "[DEBUG] register packet received outside await-register", map[string]any{
			"sessionId":       s.id,
			"state":           s.state.Load(),
			"decodedName":     decodedName,
			"decodedPlayerId": decodedPlayerID,
			"decodeError": func() string {
				if decodeErr != nil {
					return decodeErr.Error()
				}
				return ""
			}(),
		})
		// #endregion
		return nil
	}

	name, playerID, err := protocol.DecodeRegister(body)
	if err != nil {
		return err
	}
	s.name = firstNonBlank(name, s.preregister.PlayerName, "Player")
	s.playerID = firstNonBlank(playerID, s.id)
	// #region debug-point B:decode-register
	debugReport(debugRunID(), "B", "internal/relay/server.go:handleRegister", "[DEBUG] register decoded", map[string]any{
		"sessionId":     s.id,
		"name":          s.name,
		"playerId":      s.playerID,
		"query":         s.preregister.Query,
		"clientVersion": s.clientVersion,
	})
	// #endregion

	if err := s.Send(protocol.EncodeRelayVersionInfo(int32(s.clientVersion))); err != nil {
		return err
	}

	if err := s.Send(protocol.EncodeRelayPow(int32(time.Now().Unix()))); err != nil {
		return err
	}

	return nil
}

func (s *Session) handlePowReceive(body []byte) error {
	// For authenticateType = 0, we just proceed.
	query := strings.TrimSpace(strings.ToUpper(s.pendingRoomCode))
	if query != "" && query != "RELAYCN" {
		return s.joinRoom(query)
	}

	s.state.Store(int32(stateAwaitRoomCode))
	promptPacket, promptErr := protocol.EncodePrompt("请输入房间号，留空会自动创建新房间。")
	if promptErr != nil {
		return promptErr
	}
	return s.Send(promptPacket)
}

func (s *Session) handlePromptReply(body []byte) error {
	if sessionState(s.state.Load()) != stateAwaitRoomCode {
		return nil
	}

	roomCode, err := protocol.DecodePromptReply(body)
	if err != nil {
		return err
	}
	// #region debug-point C:prompt-room-code
	debugReport(debugRunID(), "C", "internal/relay/server.go:handlePromptReply", "[DEBUG] prompt reply decoded", map[string]any{
		"sessionId": s.id,
		"roomCode":  roomCode,
	})
	// #endregion
	return s.joinRoom(roomCode)
}

func (s *Session) handleChat(body []byte) error {
	room := s.Room()
	if room == nil {
		return nil
	}

	message, err := protocol.DecodeChatReceive(body)
	if err != nil {
		return err
	}
	if strings.TrimSpace(message) == "" {
		return nil
	}

	room.broadcastChat(s.name, message)
	return nil
}

func (s *Session) handleStartGame(packet protocol.Packet) error {
	room := s.Room()
	if room == nil {
		return nil
	}

	if !room.IsOwner(s) {
		return s.systemMessage("只有房主可以开始游戏。")
	}

	room.SetInGame(true)
	room.broadcastSystem(fmt.Sprintf("游戏已由 %s 开始。", s.name))

	startGamePacket, err := protocol.EncodeStartGame()
	if err == nil {
		room.broadcast(startGamePacket)
	}

	return nil
}

func (s *Session) handleReturnToBattleRoom(packet protocol.Packet) error {
	room := s.Room()
	if room == nil {
		return nil
	}

	s.server.logger.Printf("player %s is returning to battle room in room %s", s.name, room.Code)
	room.SetInGame(false)

	return room.broadcastTeamList()
}

func (s *Session) handleGameCommand(packet protocol.Packet) error {
	room := s.Room()
	if room == nil {
		return nil
	}

	gp, err := protocol.DecodeGameCommandPacket(packet.Body)
	if err != nil {
		return err
	}

	room.PushCommand(CommandRequest{
		Sender: s,
		Packet: gp,
	})
	return nil
}

func (s *Session) joinRoom(code string) error {
	room, isNew, err := s.server.rooms.JoinOrCreate(code, s)
	if err != nil {
		return err
	}

	s.server.logger.Printf("player %s joined room %s (new: %v)", s.name, room.Code, isNew)

	// If owner, send RELAY_BECOME_SERVER
	if room.IsOwner(s) {
		becomeServerPacket, err := protocol.EncodeRelayBecomeServer(room.ServerUUID, true, room.Code, s.clientVersion)
		if err != nil {
			return err
		}
		if err := s.Send(becomeServerPacket); err != nil {
			return err
		}
	}

	// Send initial room state
	if err := s.sendServerInfo(room); err != nil {
		return err
	}

	if err := room.broadcastTeamList(); err != nil {
		return err
	}

	s.state.Store(int32(stateActive))
	return nil
}

func (s *Session) sendServerInfo(room *Room) error {
	w := protocol.NewWriter()

	// 1. Server ID (String)
	if err := w.String(room.ServerUUID); err != nil {
		return err
	}
	// 2. Client Version (Int32)
	w.Int32(int32(s.server.cfg.DefaultClientVersion))
	// 3. Map Type (Int32) - 0: Custom Map
	w.Int32(0)
	// 4. Map/Room Name (String)
	if err := w.String(room.MapName); err != nil {
		return err
	}
	// 5. Credits (Int32)
	w.Int32(room.Credits)
	// 6. Fog of War (Int32) - 0: None, 1: Fog, 2: Mist
	w.Int32(room.FogOfWar)
	// 7. Revealed Map (Boolean)
	w.Bool(false)
	// 8. AI Difficulty (Int32)
	w.Int32(0)
	// 9. Protocol Version Byte (Byte)
	w.Byte(2)
	// 10. Boolean Flag 1 (Boolean)
	w.Bool(false)
	// 11. Boolean Flag 2 / Admin UI (Boolean)
	w.Bool(room.IsAdmin(s))

	// Protocol Version >= 1 fields (skipped if version >= 1)
	w.Int32(0)
	w.Int32(0)

	// Protocol Version >= 2 extra fields
	// 12. Init Unit (Int32)
	w.Int32(1)
	// 13. Income (Float32)
	w.Float32(room.Income)
	// 14. Nukes (Boolean)
	w.Bool(true)

	return s.Send(w.Packet(protocol.TypeServerInfo))
}

func (s *Session) Send(packet protocol.Packet) error {
	select {
	case <-s.closeCh:
		return net.ErrClosed
	case s.send <- packet:
		return nil
	}
}

func (s *Session) close() {
	s.once.Do(func() {
		room := s.Room()
		// #region debug-point E:session-close
		debugReport(debugRunID(), "E", "internal/relay/server.go:close", "[DEBUG] session closing", map[string]any{
			"sessionId": s.id,
			"playerId":  s.playerID,
			"name":      s.name,
			"slot":      s.Slot(),
			"state":     s.state.Load(),
			"roomCode": func() string {
				if room != nil {
					return room.Code
				}
				return ""
			}(),
		})
		// #endregion
		s.state.Store(int32(stateClosed))
		close(s.closeCh)
		_ = s.conn.Close()

		if room != nil {
			room.Leave(s)
			s.server.logger.Printf("player %s removed from room %s", s.name, room.Code)
			_ = room.broadcastTeamList()
			s.server.rooms.RemoveIfEmpty(room)
		}

		s.server.logger.Printf("client disconnected: %s", s.conn.RemoteAddr())
	})
}

func (s *Session) setRoom(room *Room, slot int) {
	s.roomMu.Lock()
	defer s.roomMu.Unlock()
	s.room = room
	s.slot = slot
}

func (s *Session) Room() *Room {
	s.roomMu.RLock()
	defer s.roomMu.RUnlock()
	return s.room
}

func (s *Session) systemMessage(message string) error {
	packet, err := protocol.EncodeSystemMessage(message)
	if err != nil {
		return err
	}
	return s.Send(packet)
}

func (s *Session) buildPreregisterPacket(handshakeID string) (protocol.Packet, error) {
	w := protocol.NewWriter()
	if err := w.String(handshakeID); err != nil {
		return protocol.Packet{}, err
	}
	return w.Packet(protocol.TypePreregister), nil
}

func (s *Session) setGameSession(slot int) {
	s.roomMu.Lock()
	defer s.roomMu.Unlock()
	s.slot = slot
}

func (s *Session) Slot() int {
	s.roomMu.RLock()
	defer s.roomMu.RUnlock()
	return s.slot
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func javaHash(s string) int32 {
	var h int32
	for _, r := range s {
		h = 31*h + int32(r)
	}
	return h
}
