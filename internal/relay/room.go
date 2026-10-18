package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"rwgin/internal/config"
	"rwgin/internal/game/mod"
	"rwgin/internal/protocol"
)

type RoomSummary struct {
	Code       string    `json:"code"`
	Title      string    `json:"title"`
	MapName    string    `json:"mapName"`
	MaxPlayers int       `json:"maxPlayers"`
	Players    int       `json:"players"`
	InGame     bool      `json:"inGame"`
	OwnerID    string    `json:"ownerId"`
	OwnerName  string    `json:"ownerName"`
	AdminCount int       `json:"adminCount"`
	CreatedAt  time.Time `json:"createdAt"`
}

type RoomPlayerSummary struct {
	PlayerID string `json:"playerId"`
	Name     string `json:"name"`
	Slot     int    `json:"slot"`
	IsOwner  bool   `json:"isOwner"`
	IsAdmin  bool   `json:"isAdmin"`
	IsAI     bool   `json:"isAi"`
}

type RoomDetail struct {
	RoomSummary
	Members []RoomPlayerSummary `json:"members"`
}

type Manager struct {
	cfg           config.Config
	server        *Server
	mu            sync.RWMutex
	rooms         map[string]*Room
	availableMaps []string
	mods          *mod.Registry
}

func NewManager(cfg config.Config, server *Server) *Manager {
	m := &Manager{
		cfg:    cfg,
		server: server,
		rooms:  make(map[string]*Room),
		mods:   mod.NewRegistry(),
	}
	m.scanMaps()
	m.loadMods()
	return m
}

func (m *Manager) scanMaps() {
	files, err := os.ReadDir(m.cfg.MapsDir)
	if err != nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.availableMaps = nil
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".tmx") {
			m.availableMaps = append(m.availableMaps, f.Name())
		}
	}
}

func (m *Manager) loadMods() {
	if m.cfg.ModsDir == "" {
		return
	}
	if err := m.mods.LoadDir(m.cfg.ModsDir); err != nil {
		log.Printf("[mod] failed to load mods from %s: %v", m.cfg.ModsDir, err)
	}
}

// Mods returns the mod registry.
func (m *Manager) Mods() *mod.Registry {
	return m.mods
}

func (m *Manager) ListMaps() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.availableMaps
}

func (m *Manager) List() []RoomSummary {
	m.mu.RLock()
	defer m.mu.RUnlock()

	items := make([]RoomSummary, 0, len(m.rooms))
	for _, room := range m.rooms {
		items = append(items, room.Summary())
	}
	return items
}

func (m *Manager) Get(code string) (*Room, bool) {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	m.mu.RLock()
	defer m.mu.RUnlock()
	room, ok := m.rooms[normalized]
	return room, ok
}

func (m *Manager) Create(code, title, uuid string, maxPlayers int) (*Room, error) {
	if maxPlayers <= 0 {
		maxPlayers = m.cfg.DefaultMaxPlayers
	}

	normalized := strings.ToUpper(strings.TrimSpace(code))
	if normalized == "" {
		normalized = m.nextCode()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.rooms[normalized]; exists {
		return nil, fmt.Errorf("room already exists: %s", normalized)
	}

	mapName := defaultIfBlank(m.cfg.DefaultMapName, "unit_types_test.tmx")

	room := &Room{
		Code:         normalized,
		Title:        defaultIfBlank(title, m.cfg.DefaultRoomName+" "+normalized),
		MapName:      mapName,
		ServerUUID:   defaultIfBlank(uuid, randomHex(16)),
		server:       m.server,
		MaxPlayers:   maxPlayers,
		MaxUnits:     m.cfg.DefaultMaxUnits,
		Credits:      1000,
		FogOfWar:     2,
		Income:       m.cfg.DefaultIncome,
		DisableNuke:  false,
		SharedControl: false,
		StartingUnits: 1,
		CreatedAt:    time.Now(),
		LastActivity: time.Now(),
		players:      make([]*Session, maxPlayers),
		adminIDs:     make(map[string]bool),
		mutedIDs:     make(map[string]bool),
		disconnectedPlayers: make(map[string]*DisconnectedPlayer),
		bannedIDs:    make(map[string]bool),
		AISlot:       -1,
		gameCommands: make(chan CommandRequest, 128),
		stopCh:       make(chan struct{}),
		commands:     DefaultRegistry(),
		startVotes:   make(map[string]bool),
	}
	m.rooms[normalized] = room

	room.wg.Add(1)
	go room.run()

	return room, nil
}

func (m *Manager) JoinOrCreate(code, uuid string, session *Session) (*Room, bool, error) {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	if normalized == "" {
		var err error
		room, err := m.Create("", "", uuid, m.cfg.DefaultMaxPlayers)
		if err != nil {
			return nil, false, err
		}
		if err := room.Join(session); err != nil {
			return nil, false, err
		}
		return room, true, nil
	}

	m.mu.RLock()
	room, ok := m.rooms[normalized]
	m.mu.RUnlock()
	if !ok {
		var err error
		room, err = m.Create(normalized, "", uuid, m.cfg.DefaultMaxPlayers)
		if err != nil {
			return nil, false, err
		}
		if err := room.Join(session); err != nil {
			return nil, false, err
		}
		return room, true, nil
	}

	if err := room.Join(session); err != nil {
		return nil, false, err
	}
	return room, false, nil
}

func (m *Manager) RemoveIfEmpty(room *Room) {
	if room == nil || room.PlayerCount() != 0 {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.rooms[room.Code]; ok && current == room && room.PlayerCount() == 0 {
		delete(m.rooms, room.Code)
	}
}

// Remove removes a room by code.
func (m *Manager) Remove(code string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if room, ok := m.rooms[code]; ok {
		room.Stop()
		delete(m.rooms, code)
	}
}

func (m *Manager) nextCode() string {
	for {
		code := fmt.Sprintf("%s%s", strings.ToUpper(m.cfg.RoomCodePrefix), randomCode(4))
		if _, exists := m.rooms[code]; !exists {
			return code
		}
	}
}

// StartCleanup starts a goroutine that periodically cleans up empty rooms.
func (m *Manager) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.cleanupStaleRooms()
			}
		}
	}()
}

// cleanupStaleRooms removes rooms that have been empty for more than 5 minutes.
func (m *Manager) cleanupStaleRooms() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for code, room := range m.rooms {
		if room.PlayerCount() == 0 && now.Sub(room.LastActivity) > 5*time.Minute {
			room.Stop()
			delete(m.rooms, code)
		}
	}
}

// Identity represents the source of a command or the owner of a unit.
type Identity interface {
	ID() string   // A unique identifier (e.g., PlayerID for a session, "system" for the server)
	Name() string // A display name (e.g., player's name, "AI Player 1", "System")
}

// SystemIdentity represents the server itself.
type SystemIdentity struct{}

func (s SystemIdentity) ID() string   { return "system" }
func (s SystemIdentity) Name() string { return "System" }

// CommandRequest wraps a game command with the identity of its sender.
type CommandRequest struct {
	Sender Identity
	Packet protocol.GameCommandPacket
}

type Room struct {
	Code       string
	Title      string
	MapName    string
	ServerUUID string
	server     *Server
	MaxPlayers int
	MaxUnits       int
	Credits        int32
	FogOfWar       int32
	Income         float32
	DisableNuke    bool
	SharedControl  bool
	StartingUnits  int32
	CreatedAt      time.Time
	LastActivity   time.Time

	mu           sync.RWMutex
	InGame       bool
	paused       bool
	currentStep  int32
	OwnerID      string
	players      []*Session
	adminIDs     map[string]bool
	mutedIDs     map[string]bool
	disconnectedPlayers map[string]*DisconnectedPlayer
	lastSave     []byte
	syncing      bool
	bannedIDs    map[string]bool
	activeVote   *Vote
	checkRequested bool
	checkSumReceived int32
	checkSumDeadline time.Time
	AISlot       int
	gameCommands chan CommandRequest
	stopCh       chan struct{}
	wg           sync.WaitGroup
	commands     *CommandRegistry
	startVotes   map[string]bool
}

// DisconnectedPlayer tracks a player who disconnected during a game.
type DisconnectedPlayer struct {
	PlayerID string
	Name     string
	Slot     int
	Team     int32
	LeftAt   time.Time
}

// Vote represents an active vote in the room.
type Vote struct {
	Type      string // "start", "sync", "gameover"
	Initiator string // playerID of the initiator
	Votes     map[string]bool
	ExpiresAt time.Time
}

func (r *Room) Summary() RoomSummary {
	ownerName := ""
	if owner := r.FindByPlayerID(r.OwnerID); owner != nil {
		ownerName = owner.name
	}
	return RoomSummary{
		Code:       r.Code,
		Title:      r.Title,
		MapName:    r.MapName,
		MaxPlayers: r.MaxPlayers,
		Players:    r.PlayerCount(),
		InGame:     r.IsInGame(),
		OwnerID:    r.OwnerID,
		OwnerName:  ownerName,
		AdminCount: r.AdminCount(),
		CreatedAt:  r.CreatedAt,
	}
}

func (r *Room) run() {
	defer r.wg.Done()

	// 10 frames at 60fps = ~167ms per tick (6 ticks/second)
	ticker := time.NewTicker(10 * time.Second / 60)
	teamListTicker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer teamListTicker.Stop()

	for {
		select {
		case <-ticker.C:
			r.processTick()
		case <-teamListTicker.C:
			if !r.IsInGame() && r.PlayerCount() > 0 {
				_ = r.broadcastTeamList()
			}
		case <-r.stopCh:
			return
		}
	}
}

func (r *Room) Stop() {
	close(r.stopCh)
	r.wg.Wait()
}

func (r *Room) PushCommand(cmd CommandRequest) {
	select {
	case r.gameCommands <- cmd:
	default:
		// Log when command queue is full
		if r.server != nil && r.server.logger != nil {
			r.server.logger.Printf("command queue full for room %s, dropping command", r.Code)
		}
	}
}

func (r *Room) processTick() {
	// When paused, still drain commands but don't process or advance
	if r.IsPaused() {
		r.drainCommands()
		return
	}

	commands := r.drainCommands()

	// Collect raw command bytes for relay (no server-side parsing)
	var rawCommands [][]byte
	for _, cmdRequest := range commands {
		rawCommands = append(rawCommands, cmdRequest.Packet.Data)
	}

	// Build and broadcast TICK packet
	var tickPacket protocol.Packet
	if len(rawCommands) > 0 {
		tickPacket = protocol.EncodeTickCommands(r.currentStep, rawCommands)
	} else {
		tickPacket = protocol.EncodeTickEmpty(r.currentStep)
	}
	r.broadcast(tickPacket)

	// Advance step counter (FRAMES_PER_WINDOW = 10)
	r.currentStep += 10

	// Trigger checksum verification every 300 ticks (~5 seconds)
	if r.currentStep%300 == 0 && !r.checkRequested {
		go r.doChecksum()
	}
}

// doChecksum sends checksum requests to all clients and waits for responses.
func (r *Room) doChecksum() {
	r.mu.Lock()
	if r.checkRequested {
		r.mu.Unlock()
		return
	}
	r.checkRequested = true
	r.checkSumReceived = 0
	r.checkSumDeadline = time.Now().Add(5 * time.Second)
	r.mu.Unlock()

	// Reset checksum sent flags for all players
	for _, player := range r.SnapshotPlayers() {
		if player != nil {
			player.checkSumSent = false
			player.checkList = nil
		}
	}

	// Send checksum request to all clients
	checksumReq := protocol.EncodeSyncChecksum(r.currentStep)
	r.broadcast(checksumReq)

	// Wait for responses with timeout
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(5 * time.Second)

	for {
		select {
		case <-deadline:
			// Timeout - check what we have
			r.processChecksumResults()
			r.mu.Lock()
			r.checkRequested = false
			r.mu.Unlock()
			return
		case <-ticker.C:
			// Check if all players have responded
			players := r.SnapshotPlayers()
			responded := 0
			for _, p := range players {
				if p != nil && p.checkSumSent {
					responded++
				}
			}
			if responded >= len(players) {
				r.processChecksumResults()
				r.mu.Lock()
				r.checkRequested = false
				r.mu.Unlock()
				return
			}
		}
	}
}

// processChecksumResults compares checksums from all clients.
func (r *Room) processChecksumResults() {
	players := r.SnapshotPlayers()

	// Collect players who have responded
	var responded []*Session
	for _, p := range players {
		if p != nil && p.checkSumSent && len(p.checkList) > 0 {
			responded = append(responded, p)
		}
	}

	if len(responded) < 2 {
		// Not enough players to compare
		return
	}

	// Use the first player as reference
	reference := responded[0].checkList
	diffCount := 0

	for i := 1; i < len(responded); i++ {
		if !checksumListsMatch(reference, responded[i].checkList) {
			diffCount++
		}
	}

	// If 2 or more players have different checksums, trigger sync
	if diffCount >= 2 {
		// Desync detected - trigger sync
		go r.TriggerSync(r.server)
	}
}

// checksumListsMatch compares two checksum lists with tolerance.
func checksumListsMatch(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	// Tolerance prefixes for each checksum item (from Rukkit)
	prefixes := []int64{100000, 100000, 10000, 5, 5, 5, 500, 5, 10, 10, 500, 500, 500, 1000, 1000}
	for i := range a {
		prefix := int64(1000) // default tolerance
		if i < len(prefixes) {
			prefix = prefixes[i]
		}
		diff := a[i] - b[i]
		if diff < 0 {
			diff = -diff
		}
		if diff > prefix {
			return false
		}
	}
	return true
}

func (r *Room) drainCommands() []CommandRequest {
	var packets []CommandRequest
	for {
		select {
		case cmd := <-r.gameCommands:
			packets = append(packets, cmd)
		default:
			return packets
		}
	}
}

func (r *Room) Detail() RoomDetail {
	r.mu.RLock()
	defer r.mu.RUnlock()

	members := make([]RoomPlayerSummary, 0, len(r.players))
	ownerName := ""
	for slot, participant := range r.slotParticipantsLocked() {
		if participant == nil {
			continue
		}
		if participant.session != nil {
			isOwner := participant.session.playerID == r.OwnerID
			if isOwner {
				ownerName = participant.name
			}
			members = append(members, RoomPlayerSummary{
				PlayerID: participant.session.playerID,
				Name:     participant.name,
				Slot:     slot,
				IsOwner:  isOwner,
				IsAdmin:  r.adminIDs[participant.session.playerID],
				IsAI:     false,
			})
			continue
		}
		members = append(members, RoomPlayerSummary{
			PlayerID: "AI",
			Name:     participant.name,
			Slot:     slot,
			IsOwner:  false,
			IsAdmin:  false,
			IsAI:     true,
		})
	}

	return RoomDetail{
		RoomSummary: RoomSummary{
			Code:       r.Code,
			Title:      r.Title,
			MapName:    r.MapName,
			MaxPlayers: r.MaxPlayers,
			Players:    len(members),
			InGame:     r.InGame,
			OwnerID:    r.OwnerID,
			OwnerName:  ownerName,
			AdminCount: len(r.adminIDs),
			CreatedAt:  r.CreatedAt,
		},
		Members: members,
	}
}

// TouchActivity updates the last activity timestamp.
func (r *Room) TouchActivity() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.LastActivity = time.Now()
}

func (r *Room) Join(session *Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.syncAutoAILockedForJoin()
	for idx, existing := range r.players {
		if existing == nil {
			r.players[idx] = session
			session.setRoom(r, idx)
			if r.OwnerID == "" {
				r.OwnerID = session.playerID
				r.adminIDs[session.playerID] = true
			}
			r.LastActivity = time.Now()
			r.syncAutoAILocked()
			return nil
		}
	}
	return fmt.Errorf("room is full")
}

func (r *Room) Leave(session *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for idx, existing := range r.players {
		if existing == session {
			// Track disconnected player during game for potential reconnection
			if r.InGame && session.playerID != "" {
				r.disconnectedPlayers[session.playerID] = &DisconnectedPlayer{
					PlayerID: session.playerID,
					Name:     session.name,
					Slot:     idx,
					Team:     session.team,
					LeftAt:   time.Now(),
				}

				// Set quick auth in Redis if player is authenticated and has quick_auth enabled
				if session.authenticated && session.haUserUUID != "" && r.server.rdb != nil {
					binding, err := r.server.db.GetBindingByGameUUID(session.playerID)
					if err == nil && binding != nil && binding.QuickAuth {
						if err := r.server.rdb.SetQuickAuth(session.playerID, session.haUserUUID, 2*time.Minute); err != nil {
							r.server.logger.Printf("HA: failed to set quick auth for %s: %v", session.playerID, err)
						} else {
							r.server.logger.Printf("HA: quick auth set for %s (2 min TTL)", session.playerID)
						}
					}
				}
			}

			r.players[idx] = nil
			delete(r.adminIDs, session.playerID)
			if r.OwnerID == session.playerID {
				r.OwnerID = ""
				r.promoteSuccessorLocked()
			}
			delete(r.startVotes, session.playerID)
			if r.PlayerCountLocked() == 0 {
				r.InGame = false
			}
			r.syncAutoAILocked()
			return
		}
	}
}

// RestoreSlot restores a session to a specific slot (used for reconnection).
func (r *Room) RestoreSlot(session *Session, slot int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if slot < 0 || slot >= len(r.players) {
		return fmt.Errorf("invalid slot: %d", slot)
	}

	// If slot is occupied, try to find an empty slot instead
	if r.players[slot] != nil {
		emptySlot := r.firstEmptySlotLocked()
		if emptySlot == -1 {
			return fmt.Errorf("room is full, cannot restore slot")
		}
		slot = emptySlot
	}

	r.players[slot] = session
	session.setRoom(r, slot)
	r.syncAutoAILocked()
	return nil
}

func (r *Room) PlayerCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.PlayerCountLocked()
}

func (r *Room) PlayerCountLocked() int {
	count := 0
	for _, player := range r.players {
		if player != nil {
			count++
		}
	}
	if r.AISlot >= 0 {
		count++
	}
	return count
}

func (r *Room) HumanCountLocked() int {
	count := 0
	for _, player := range r.players {
		if player != nil {
			count++
		}
	}
	return count
}

func (r *Room) SnapshotPlayers() []*Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]*Session, len(r.players))
	copy(items, r.players)
	return items
}

func (r *Room) SetInGame(value bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.InGame = value
	if value {
		r.currentStep = 0
		r.paused = false
		r.startVotes = make(map[string]bool)
	}
}

func (r *Room) AddStartVote(playerID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startVotes[playerID] = true
	return len(r.startVotes)
}

func (r *Room) ClearStartVotes() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.startVotes = make(map[string]bool)
}

// StartVote initiates a new vote of the given type.
func (r *Room) StartVote(voteType, initiatorID string) *Vote {
	r.mu.Lock()
	defer r.mu.Unlock()

	// If there's already an active vote, expire it
	if r.activeVote != nil {
		r.activeVote = nil
	}

	vote := &Vote{
		Type:      voteType,
		Initiator: initiatorID,
		Votes:     map[string]bool{initiatorID: true},
		ExpiresAt: time.Now().Add(30 * time.Second),
	}
	r.activeVote = vote
	return vote
}

// CastVote adds a vote from a player. Returns the current vote count.
func (r *Room) CastVote(voteType, playerID string) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.activeVote == nil || r.activeVote.Type != voteType {
		return 0, false // No active vote of this type
	}

	if time.Now().After(r.activeVote.ExpiresAt) {
		r.activeVote = nil
		return 0, false // Vote expired
	}

	r.activeVote.Votes[playerID] = true
	return len(r.activeVote.Votes), true
}

// ClearVote clears the active vote.
func (r *Room) ClearVote() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.activeVote = nil
}

// GetActiveVote returns the active vote, or nil if none.
func (r *Room) GetActiveVote() *Vote {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.activeVote
}

// BanPlayer adds a player to the ban list.
func (r *Room) BanPlayer(playerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bannedIDs[playerID] = true
}

// UnbanPlayer removes a player from the ban list.
func (r *Room) UnbanPlayer(playerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.bannedIDs, playerID)
}

// IsBanned checks if a player is banned.
func (r *Room) IsBanned(playerID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.bannedIDs[playerID]
}

func (r *Room) IsInGame() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.InGame
}

func (r *Room) IsOwner(session *Session) bool {
	if session == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return session.playerID != "" && session.playerID == r.OwnerID
}

func (r *Room) IsAdmin(session *Session) bool {
	if session == nil {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.adminIDs[session.playerID]
}

func (r *Room) CanModerate(session *Session) bool {
	return r.IsAdmin(session)
}

func (r *Room) AdminCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.adminIDs)
}

func (r *Room) FindByPlayerID(playerID string) *Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, session := range r.players {
		if session != nil && session.playerID == playerID {
			return session
		}
	}
	return nil
}

func (r *Room) TransferOwnership(actor, target *Session) error {
	if actor == nil || target == nil {
		return fmt.Errorf("actor or target is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if actor.playerID != r.OwnerID {
		return fmt.Errorf("only owner can transfer ownership")
	}
	if !r.containsLocked(target) {
		return fmt.Errorf("target is not in room")
	}
	r.OwnerID = target.playerID
	r.adminIDs[target.playerID] = true
	return nil
}

func (r *Room) SetAdmin(actor, target *Session, value bool) error {
	if actor == nil || target == nil {
		return fmt.Errorf("actor or target is nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if actor.playerID != r.OwnerID {
		return fmt.Errorf("only owner can manage admins")
	}
	if !r.containsLocked(target) {
		return fmt.Errorf("target is not in room")
	}
	if value {
		r.adminIDs[target.playerID] = true
		return nil
	}
	if target.playerID == r.OwnerID {
		return fmt.Errorf("owner cannot be removed from admins")
	}
	delete(r.adminIDs, target.playerID)
	return nil
}

func (r *Room) Kick(actor, target *Session) error {
	if actor == nil || target == nil {
		return fmt.Errorf("actor or target is nil")
	}
	r.mu.RLock()
	canModerate := r.adminIDs[actor.playerID]
	actorIsOwner := actor.playerID == r.OwnerID
	targetIsOwner := target.playerID == r.OwnerID
	targetIsAdmin := r.adminIDs[target.playerID]
	r.mu.RUnlock()

	if !canModerate {
		return fmt.Errorf("only admins can kick players")
	}
	if targetIsOwner && !actorIsOwner {
		return fmt.Errorf("only owner can kick current owner")
	}
	if targetIsAdmin && !actorIsOwner {
		return fmt.Errorf("only owner can kick another admin")
	}

	reason := fmt.Sprintf("你已被 %s 移出房间。", actor.Name())
	_ = target.Send(protocol.EncodeKick(reason))
	target.close()
	return nil
}

// Mute adds a player to the mute list.
func (r *Room) Mute(actor, target *Session) error {
	if actor == nil || target == nil {
		return fmt.Errorf("actor or target is nil")
	}
	r.mu.RLock()
	canModerate := r.adminIDs[actor.playerID]
	actorIsOwner := actor.playerID == r.OwnerID
	targetIsOwner := target.playerID == r.OwnerID
	r.mu.RUnlock()

	if !canModerate {
		return fmt.Errorf("only admins can mute players")
	}
	if targetIsOwner && !actorIsOwner {
		return fmt.Errorf("only owner can mute current owner")
	}

	r.mu.Lock()
	r.mutedIDs[target.playerID] = true
	r.mu.Unlock()
	return nil
}

// Unmute removes a player from the mute list.
func (r *Room) Unmute(actor, target *Session) error {
	if actor == nil || target == nil {
		return fmt.Errorf("actor or target is nil")
	}
	r.mu.RLock()
	canModerate := r.adminIDs[actor.playerID]
	r.mu.RUnlock()

	if !canModerate {
		return fmt.Errorf("only admins can unmute players")
	}

	r.mu.Lock()
	delete(r.mutedIDs, target.playerID)
	r.mu.Unlock()
	return nil
}

// IsMuted checks if a player is muted.
func (r *Room) IsMuted(playerID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mutedIDs[playerID]
}

// SetPaused sets the paused state of the room.
func (r *Room) SetPaused(value bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused = value
}

// IsPaused returns whether the room is paused.
func (r *Room) IsPaused() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.paused
}

// FindDisconnected checks if a playerID matches a recently disconnected player.
func (r *Room) FindDisconnected(playerID string) *DisconnectedPlayer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	dp, ok := r.disconnectedPlayers[playerID]
	if !ok {
		return nil
	}
	// Expire after 5 minutes
	if time.Since(dp.LeftAt) > 5*time.Minute {
		delete(r.disconnectedPlayers, playerID)
		return nil
	}
	return dp
}

// ClearDisconnected removes a player from the disconnected tracking.
func (r *Room) ClearDisconnected(playerID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.disconnectedPlayers, playerID)
}

// CleanupDisconnectedStale removes stale disconnected player entries.
func (r *Room) CleanupDisconnectedStale() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, dp := range r.disconnectedPlayers {
		if time.Since(dp.LeftAt) > 5*time.Minute {
			delete(r.disconnectedPlayers, id)
		}
	}
}

// SetLastSave stores the last known game save data.
func (r *Room) SetLastSave(data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastSave = data
}

// GetLastSave returns the last known game save data.
func (r *Room) GetLastSave() []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastSave
}

// IsSyncing returns whether a sync operation is in progress.
func (r *Room) IsSyncing() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.syncing
}

// SetSyncing sets the syncing state.
func (r *Room) SetSyncing(value bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.syncing = value
}

// TriggerSync initiates a save sync by requesting saves from connected clients.
func (r *Room) TriggerSync(server *Server) {
	if r.IsSyncing() {
		return
	}
	r.SetSyncing(true)
	r.SetPaused(true)

	// Request save from all connected clients
	pullSave := protocol.EncodeSyncPullSave(r.currentStep, nil)
	r.broadcast(pullSave)

	// Start a goroutine to wait for the save with timeout
	go func() {
		defer r.SetSyncing(false)
		deadline := time.After(5 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-deadline:
				// Timeout - resume without sync
				r.SetPaused(false)
				server.logger.Printf("sync timeout for room %s", r.Code)
				return
			case <-ticker.C:
				save := r.GetLastSave()
				if save != nil && len(save) > 20 {
					// Got a valid save, distribute it to all clients
					sendSave := protocol.EncodeSyncSendSave(r.currentStep, save)
					r.broadcast(sendSave)
					r.SetPaused(false)
					server.logger.Printf("sync completed for room %s (%d bytes)", r.Code, len(save))
					return
				}
			}
		}
	}()
}

// SetTitle sets the room title.
func (r *Room) SetTitle(title string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Title = title
}

// SetMapName sets the map name.
func (r *Room) SetMapName(mapName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.MapName = mapName
}

// SetCredits sets the credits.
func (r *Room) SetCredits(credits int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Credits = credits
}

// SetFogOfWar sets the fog of war type.
func (r *Room) SetFogOfWar(fog int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.FogOfWar = fog
}

// SetIncome sets the income multiplier.
func (r *Room) SetIncome(income float32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Income = income
}

// SetDisableNuke sets the disable nuke flag.
func (r *Room) SetDisableNuke(disable bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.DisableNuke = disable
}

// SetSharedControl sets the shared control flag.
func (r *Room) SetSharedControl(shared bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.SharedControl = shared
}

// SetStartingUnits sets the starting units.
func (r *Room) SetStartingUnits(units int32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.StartingUnits = units
}

// SetMaxUnits sets the max units per player.
func (r *Room) SetMaxUnits(maxUnits int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.MaxUnits = maxUnits
}

func (r *Room) FindTarget(input string) (*Session, error) {
	normalized := strings.TrimSpace(input)
	if normalized == "" {
		return nil, fmt.Errorf("missing target")
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if slot, err := parseSlot(normalized); err == nil {
		if slot >= 0 && slot < len(r.players) && r.players[slot] != nil {
			return r.players[slot], nil
		}
	}

	var exact *Session
	for _, session := range r.players {
		if session == nil {
			continue
		}
		if strings.EqualFold(session.name, normalized) || session.playerID == normalized {
			if exact != nil {
				return nil, fmt.Errorf("multiple players matched target")
			}
			exact = session
		}
	}
	if exact == nil {
		return nil, fmt.Errorf("target not found")
	}
	return exact, nil
}

func (r *Room) MembersText() string {
	detail := r.Detail()
	if len(detail.Members) == 0 {
		return "当前房间为空"
	}
	lines := make([]string, 0, len(detail.Members))
	for _, member := range detail.Members {
		role := "player"
		if member.IsAI {
			role = "ai"
		} else if member.IsOwner {
			role = "owner"
		} else if member.IsAdmin {
			role = "admin"
		}
		lines = append(lines, fmt.Sprintf("[%d] %s (%s)", member.Slot, member.Name, role))
	}
	return strings.Join(lines, "\n")
}

func (r *Room) broadcast(packet protocol.Packet) {
	for _, player := range r.SnapshotPlayers() {
		if player == nil {
			continue
		}
		_ = player.Send(packet)
	}
}

func (r *Room) broadcastSystem(message string) {
	packet := buildChatPacket("SERVER", message, 5)
	r.broadcast(packet)
}

func (r *Room) handleChat(sender Identity, message string) {
	// Check if the sender is muted (commands still work for muted players)
	if s, ok := sender.(*Session); ok {
		if r.IsMuted(s.playerID) && !strings.HasPrefix(message, r.commands.prefix) {
			_ = s.systemMessage("你已被禁言，无法发送消息。")
			return
		}
	}

	// Broadcast the chat message to everyone (including the sender)
	packet := buildChatPacket(sender.Name(), message, 0)
	r.broadcast(packet)

	// Process as a command if applicable
	r.commands.Handle(r, sender, message)
}

func (r *Room) broadcastTeamList() error {
	for _, player := range r.SnapshotPlayers() {
		if player == nil {
			continue
		}
		if err := r.sendTeamListTo(player); err != nil {
			return err
		}
	}
	return nil
}

func (r *Room) sendTeamListTo(player *Session) error {
	packet, err := r.buildTeamListPacket(player)
	if err != nil {
		return err
	}
	return player.Send(packet)
}

func buildChatPacket(sender, message string, team int32) protocol.Packet {
	w := protocol.NewWriter()
	protocol.RequireNoError(w.String(message))
	w.Byte(3)
	protocol.RequireNoError(w.MaybeString(sender))
	w.Int32(team)
	w.Int32(team)
	return w.Packet(protocol.TypeChatBroadcast)
}

func (r *Room) buildTeamListPacket(player *Session) (protocol.Packet, error) {
	w := protocol.NewWriter()
	w.Int32(int32(max(player.Slot(), 0)))
	w.Bool(false)
	w.Int32(int32(r.MaxPlayers))

	r.mu.RLock()
	participants := r.slotParticipantsLocked()
	r.mu.RUnlock()
	if err := w.GzipSection("teams", func(section *protocol.Writer) error {
		for slot, member := range participants {
			if member == nil {
				section.Bool(false)
				continue
			}

			section.Bool(true)
			roleInt := int32(0)
			if member.session != nil && r.adminIDs[member.session.playerID] {
				roleInt = 1
			}
			slotPayload, err := buildTeamSlotPayload(slot, member, roleInt)
			if err != nil {
				return err
			}
			section.Raw(slotPayload.Bytes())
		}
		return nil
	}); err != nil {
		return protocol.Packet{}, err
	}

	w.Int32(2)
	w.Int32(0)
	w.Bool(true)
	w.Int32(1)
	w.Byte(5)
	w.Int32(int32(r.MaxUnits))
	w.Int32(int32(r.MaxUnits))
	w.Int32(r.StartingUnits)
	w.Float32(r.Income)
	w.Bool(true)
	w.Bool(false)
	w.Bool(false)
	w.Bool(false)
	w.Bool(false)

	return w.Packet(protocol.TypeTeamList), nil
}

func buildTeamSlotPayload(slot int, member *slotParticipant, roleInt int32) (*protocol.Writer, error) {
	w := protocol.NewWriter()

	w.Int32(int32(slot))
	w.Byte(byte(slot))
	w.Int32(0)
	w.Int32(int32(slot))

	if err := w.MaybeString(member.name); err != nil {
		return nil, err
	}

	w.Bool(false) // isReady
	w.Int32(0)    // ping
	w.Int64(0)    // UID

	// Version >= 55
	w.Bool(false)
	w.Int32(0)

	// Version >= 91
	w.Int32(0)
	w.Byte(0)

	// Version >= 97
	w.Bool(false)
	w.Bool(false)

	// Version >= 125
	w.Bool(false)
	w.Bool(false)
	w.Int32(0)

	// Version >= 149
	if err := w.MaybeString(""); err != nil {
		return nil, err
	}
	w.Int32(roleInt) // Admin marker

	// Version >= 156
	for i := 0; i < 4; i++ {
		w.Bool(false) // IsInt (Boolean prefix)
	}
	w.Int32(0)

	return w, nil
}

func randomCode(length int) string {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		return "ROOM"
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "rwgin-room"
	}
	return hex.EncodeToString(buf)
}

func defaultIfBlank(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func parseSlot(value string) (int, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "#")
	return strconv.Atoi(value)
}

func (r *Room) containsLocked(target *Session) bool {
	for _, member := range r.players {
		if member == target {
			return true
		}
	}
	return false
}

func (r *Room) promoteSuccessorLocked() {
	for _, member := range r.players {
		if member != nil && r.adminIDs[member.playerID] {
			r.OwnerID = member.playerID
			return
		}
	}
	for _, member := range r.players {
		if member != nil {
			r.OwnerID = member.playerID
			r.adminIDs[member.playerID] = true
			return
		}
	}
}

type slotParticipant struct {
	name    string
	session *Session
}

func (r *Room) slotParticipantsLocked() []*slotParticipant {
	participants := make([]*slotParticipant, len(r.players))
	for slot, member := range r.players {
		if member != nil {
			participants[slot] = &slotParticipant{
				name:    member.name,
				session: member,
			}
		}
	}
	if r.AISlot >= 0 && r.AISlot < len(participants) && participants[r.AISlot] == nil {
		participants[r.AISlot] = &slotParticipant{
			name:    "AI",
			session: nil,
		}
	}
	return participants
}

func (r *Room) firstEmptySlotLocked() int {
	for idx, member := range r.players {
		if member == nil && idx != r.AISlot {
			return idx
		}
	}
	for idx, member := range r.players {
		if member == nil {
			return idx
		}
	}
	return -1
}

func (r *Room) syncAutoAILockedForJoin() {
	if r.HumanCountLocked() >= 1 {
		r.AISlot = -1
	}
}

func (r *Room) syncAutoAILocked() {
	humanCount := r.HumanCountLocked()
	switch humanCount {
	case 0:
		r.AISlot = -1
	case 1:
		if r.AISlot >= 0 {
			return
		}
		r.AISlot = r.firstEmptySlotLocked()
	default:
		r.AISlot = -1
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
