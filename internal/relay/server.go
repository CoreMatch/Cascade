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

	"rwgin/internal/auth"
	"rwgin/internal/config"
	"rwgin/internal/database"
	"rwgin/internal/i18n"
	"rwgin/internal/protocol"
	"rwgin/internal/redis"
)

type Server struct {
	cfg       config.Config
	logger    *log.Logger
	rooms     *Manager
	auth      *auth.Client
	db        database.Store
	rdb       *redis.Client
	listener  net.Listener
	connCount atomic.Int32
}

func NewServer(cfg config.Config, logger *log.Logger, db database.Store, rdb *redis.Client) *Server {
	s := &Server{
		cfg:    cfg,
		logger: logger,
		db:     db,
		rdb:    rdb,
	}
	if cfg.HRPAuthURL != "" {
		s.auth = auth.New(cfg.HRPAuthURL, cfg.HRPAuthClientID, cfg.HRPAuthClientSecret)
	}
	s.rooms = NewManager(cfg, s)
	return s
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

	// Start room cleanup goroutine
	s.rooms.StartCleanup(ctx)

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

		// Check connection limit
		if s.cfg.MaxConnections > 0 && s.connCount.Load() >= int32(s.cfg.MaxConnections) {
			s.logger.Printf("connection limit reached (%d), rejecting %s", s.cfg.MaxConnections, conn.RemoteAddr())
			_ = conn.Close()
			continue
		}

		s.connCount.Add(1)
		session := newSession(s, conn)
		go func() {
			session.run(ctx)
			s.connCount.Add(-1)
		}()
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

	// HA authentication state
	haUserUUID    string // HA core user UUID (empty if not authenticated)
	authenticated bool   // true if HA credentials verified via /login

	// Language preference
	locale string // "en" or "zh", loaded from DB

	roomMu sync.RWMutex
	room   *Room
	slot   int

	lastSyncTick int32
	checkSumSent bool
	checkList    []int64 // 15 checksum values from client

	// Chat rate limiting
	chatMessages []time.Time
	chatMu       sync.Mutex
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

		_ = s.conn.SetReadDeadline(time.Now().Add(time.Duration(s.server.cfg.ReadTimeoutSec) * time.Second))
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
	ticker := time.NewTicker(time.Duration(s.server.cfg.HeartbeatIntervalSec) * time.Second)
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
		// Parse checksum response from client
		room := s.Room()
		if room == nil {
			return nil
		}
		checksumData, err := protocol.DecodeSyncChecksumResponse(packet.Body)
		if err == nil && len(checksumData) > 0 {
			s.checkList = checksumData
			s.checkSumSent = true
		}
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

	// Create proxy binding (CBH=0) for all players
	if s.server.db != nil {
		if err := s.server.db.CreateProxyBinding(s.playerID, s.name); err != nil {
			s.server.logger.Printf("HA: failed to create proxy binding for %s: %v", s.name, err)
		}
		// Load language preference from DB
		if lang, err := s.server.db.GetLang(s.playerID); err == nil {
			s.locale = lang
		} else {
			s.locale = i18n.LangEN
		}
	} else {
		s.locale = i18n.LangEN
	}

	// Try to restore HA authentication state (from Redis)
	s.restoreHABinding()

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
	promptPacket, promptErr := protocol.EncodePrompt(s.T("msg.enter_room_prompt"))
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

	// Check chat rate limit
	if !s.checkChatRateLimit() {
		_ = s.systemMessage(s.T("msg.rate_limit"))
		return nil
	}

	room.handleChat(s, message)
	return nil
}

// checkChatRateLimit checks if the player is sending messages too fast.
func (s *Session) checkChatRateLimit() bool {
	rateLimit := s.server.cfg.ChatRateLimit
	if rateLimit <= 0 {
		return true // No limit
	}

	s.chatMu.Lock()
	defer s.chatMu.Unlock()

	now := time.Now()
	cutoff := now.Add(-time.Second)

	// Remove old messages
	valid := s.chatMessages[:0]
	for _, t := range s.chatMessages {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	s.chatMessages = valid

	// Check if under limit
	if len(s.chatMessages) >= rateLimit {
		return false
	}

	s.chatMessages = append(s.chatMessages, now)
	return true
}

func (s *Session) handleStartGame(packet protocol.Packet) error {
	room := s.Room()
	if room == nil {
		return nil
	}

	if !room.IsOwner(s) {
		return s.systemMessage(s.T("msg.only_owner_start"))
	}

	room.SetInGame(true)
	room.broadcastSystemKey("msg.game_started_by", s.name)

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
		_ = s.Send(protocol.EncodeKick(s.T("msg.kick_banned")))
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
			_ = s.systemMessage(s.T("msg.reconnecting"))
			room.broadcastSystemKey("msg.reconnected", s.name)

			// Send start game packet so client enters game state
			startGamePacket := protocol.EncodeStartGame(room.MapName, nil, false)
			_ = s.Send(startGamePacket)

			// Trigger sync to get the reconnecting player up to date
			go room.TriggerSync(s.server)
		} else {
			_ = s.systemMessage(s.T("msg.welcome_back", room.Code))
			_ = s.systemMessage(room.commands.PlayerHelpText(s.Locale()))
			room.broadcastSystemKey("msg.reconnected", s.name)
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
	_ = s.systemMessage(s.T("msg.welcome_room", s.name, room.Code))
	_ = s.systemMessage(room.commands.PlayerHelpText(s.Locale()))
	room.broadcastSystemKey("msg.joined", s.name)

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

// Close closes the session connection.
func (s *Session) Close() {
	s.close()
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

// T returns a translated message for this session's locale.
func (s *Session) T(key string, args ...any) string {
	return i18n.T(s.locale, key, args...)
}

// Locale returns the session's current locale.
func (s *Session) Locale() string {
	if s.locale == "" {
		return i18n.LangEN
	}
	return s.locale
}

// restoreHABinding restores HA authentication state from Redis + database (for reconnection).
func (s *Session) restoreHABinding() {
	if s.server.rdb == nil {
		return
	}

	// 1. Check Redis for normal authentication state (transient, 24h TTL)
	if haUserUUID, ok := s.server.rdb.IsAuthenticated(s.playerID); ok {
		s.haUserUUID = haUserUUID
		s.authenticated = true
		s.server.logger.Printf("HA: restored auth for %s -> uuid=%s", s.playerID, haUserUUID)
		return
	}

	// 2. Check Redis for quick auth state (transient, 2 min TTL)
	if haUserUUID, ok := s.server.rdb.CheckQuickAuth(s.playerID); ok {
		// Verify the binding exists in database
		binding, err := s.server.db.GetBindingByGameUUID(s.playerID)
		if err == nil && binding != nil {
			s.haUserUUID = haUserUUID
			s.authenticated = true
			// Upgrade to normal auth (24h TTL)
			_ = s.server.rdb.SetAuthenticated(s.playerID, haUserUUID, 24*time.Hour)
			s.server.logger.Printf("HA: quick auth restored for %s -> uuid=%s", s.playerID, haUserUUID)
		}
	}
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
