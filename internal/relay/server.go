package relay

import (
        "context"
        "errors"
        "fmt"
        "log"
        "net"
        "os"
        "path/filepath"
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
	team          int32
	clientVersion int
	preregister   protocol.PreregisterInfo

	pendingRoomCode string
	pendingUUID     string

	roomMu sync.RWMutex
	room   *Room
	slot   int
}

var sessionCounter atomic.Uint64

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

	go s.writeLoop()
	go s.heartbeatLoop(ctx)
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
			return
		}

		if err := s.handlePacket(packet); err != nil {
			s.server.logger.Printf("session %s handle packet %d failed: %v", s.id, packet.Type, err)
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
			_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := protocol.WritePacket(s.conn, packet); err != nil {
				s.close()
				return
			}
		}
	}
}

func (s *Session) heartbeatLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.closeCh:
			return
		case <-ticker.C:
			_ = s.Send(protocol.EncodeHeartbeat())
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
	case protocol.TypeSyncChecksum:
		// Acknowledge, no-op for now
		return nil
	case protocol.TypeSync:
		room := s.Room()
		if room == nil {
			return nil
		}
		// Try to parse save data from the sync response
		_, saveData, err := protocol.DecodeSyncSave(packet.Body)
		if err == nil && len(saveData) > 20 {
			// Store the save data for reconnection sync
			room.SetLastSave(saveData)
			s.server.logger.Printf("received save data from %s (%d bytes)", s.name, len(saveData))
		}
		// Also forward to other clients for relay
		room.broadcast(packet)
		return nil
	case protocol.TypeReady:
		// Acknowledge, no-op for now
		return nil
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
	s.pendingUUID = randomHex(16)
	if info.Query != "" {
		if room, ok := s.server.rooms.Get(info.Query); ok {
			s.pendingUUID = room.ServerUUID
		}
	}

	s.server.logger.Printf("preregister: version=%d, client=%d, room=%s, name=%s", info.PacketVersion, info.ClientVersion, info.Query, info.PlayerName)

	// Reply with server info
	w := protocol.NewWriter()
	w.String(s.server.cfg.RelayHandshakeID)
	w.Int32(1) // Packet Version
	w.Int32(int32(s.clientVersion))
	w.Int32(int32(s.clientVersion))
	w.String("com.corrodinggames.rts.server")
	w.String(s.pendingUUID)
	w.Int32(javaHash("Dr @ 2022"))

	if err := s.Send(w.Packet(protocol.TypePreregister)); err != nil {
		return err
	}

	s.state.Store(int32(stateAwaitRegister))
	return nil
}

func (s *Session) handleRegister(body []byte) error {
	if sessionState(s.state.Load()) != stateAwaitRegister {
		return nil
	}

	name, playerID, err := protocol.DecodeRegister(body)
	if err != nil {
		return err
	}
	s.name = firstNonBlank(name, s.preregister.PlayerName, "Player")
	s.playerID = firstNonBlank(playerID, s.id)

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
    
	room.handleChat(s, message)
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

	mapName := room.MapName
	mapPath := filepath.Join(s.server.cfg.MapsDir, mapName)
	mapData, err := os.ReadFile(mapPath)
	isCustomMap := false
	if err != nil {
		// If map file not found, send without map data (client uses built-in)
		mapData = nil
		isCustomMap = false
	}

	startGamePacket := protocol.EncodeStartGame(mapName, mapData, isCustomMap)
	room.broadcast(startGamePacket)

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
	room, isNew, err := s.server.rooms.JoinOrCreate(code, s.pendingUUID, s)
	if err != nil {
		return err
	}

	s.server.logger.Printf("player %s joined room %s (new: %v)", s.name, room.Code, isNew)

	// Check if player is banned
	if room.IsBanned(s.playerID) {
		_ = s.Send(protocol.EncodeKick("你已被封禁，无法加入此房间。"))
		room.Leave(s)
		return fmt.Errorf("player is banned")
	}

	// Check for reconnection
	if dp := room.FindDisconnected(s.playerID); dp != nil {
		// This is a reconnection - restore the player's slot
		s.server.logger.Printf("player %s reconnecting to room %s (was in slot %d)", s.name, room.Code, dp.Slot)

		// Try to restore the original slot
		if err := room.RestoreSlot(s, dp.Slot); err != nil {
			// If slot is taken, just join normally
			s.server.logger.Printf("could not restore slot %d for %s: %v, joining normally", dp.Slot, s.name, err)
		} else {
			// Restore team
			s.team = dp.Team
		}

		room.ClearDisconnected(s.playerID)

		// Send initial room state
		if err := s.sendServerInfo(room); err != nil {
			return err
		}

		if err := room.broadcastTeamList(); err != nil {
			return err
		}

		// If game is in progress, send start game packet and trigger sync
		if room.IsInGame() {
			_ = s.systemMessage("正在重新连接到游戏...")
			room.broadcastSystem(fmt.Sprintf("%s 已重连。", s.name))

			// Send start game packet so client enters game state
			startGamePacket := protocol.EncodeStartGame(room.MapName, nil, false)
			_ = s.Send(startGamePacket)

			// Trigger sync to get the reconnecting player up to date
			go room.TriggerSync(s.server)
		} else {
			_ = s.systemMessage(fmt.Sprintf("欢迎回到房间 %s!", room.Code))
			room.broadcastSystem(fmt.Sprintf("%s 已重连。", s.name))
		}

		s.state.Store(int32(stateActive))
		return nil
	}

	// Normal join flow
	// Send initial room state
	if err := s.sendServerInfo(room); err != nil {
		return err
	}

	if err := room.broadcastTeamList(); err != nil {
		return err
	}

	// Send welcome message
	_ = s.systemMessage(fmt.Sprintf("欢迎 %s 加入房间 %s!", s.name, room.Code))
	room.broadcastSystem(fmt.Sprintf("%s 加入了房间。", s.name))

	s.state.Store(int32(stateActive))
	return nil
}

func (s *Session) sendServerInfo(room *Room) error {
	clientVersion := int32(s.clientVersion)
	if clientVersion <= 0 {
		clientVersion = int32(s.server.cfg.DefaultClientVersion)
	}

	// Collect mod units from the registry
	var customUnits []protocol.ModUnitDef
	if mods := s.server.rooms.Mods(); mods != nil {
		for _, u := range mods.AllUnits() {
			customUnits = append(customUnits, protocol.ModUnitDef{
				ModName:  u.ModName,
				UnitName: u.UnitName,
				UnitID:   u.UnitID,
			})
		}
	}

	cfg := protocol.ServerInfoConfig{
		MapType:           0,
		MapName:           room.MapName,
		Credits:           room.Credits,
		FogType:           room.FogOfWar,
		IsAdmin:           room.IsAdmin(s),
		MaxUnitsPerPlayer: int32(room.MaxUnits),
		StartingUnits:     room.StartingUnits,
		Income:            room.Income,
		DisableNuke:       room.DisableNuke,
		SharedControl:     room.SharedControl,
		CustomUnits:       customUnits,
	}

	packet := protocol.EncodeServerInfo(cfg, clientVersion)
	return s.Send(packet)
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
