package relay

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"rwgin/internal/config"
	"rwgin/internal/game"
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
	mu            sync.RWMutex
	rooms         map[string]*Room
	availableMaps []string
}

func NewManager(cfg config.Config) *Manager {
	m := &Manager{
		cfg:   cfg,
		rooms: make(map[string]*Room),
	}
	m.scanMaps()
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
	var gameMap *game.Map
	mapPath := filepath.Join(m.cfg.MapsDir, mapName)
	if loaded, err := game.LoadMap(mapPath); err == nil {
		gameMap = loaded
	}

	room := &Room{
		Code:         normalized,
		Title:        defaultIfBlank(title, m.cfg.DefaultRoomName+" "+normalized),
		MapName:      mapName,
		GameMap:      gameMap,
		ServerUUID:   defaultIfBlank(uuid, randomHex(16)),
		MaxPlayers:   maxPlayers,
		MaxUnits:     m.cfg.DefaultMaxUnits,
		Credits:      1000,
		FogOfWar:     2,
		Income:       m.cfg.DefaultIncome,
		CreatedAt:    time.Now(),
		players:      make([]*Session, maxPlayers),
		adminIDs:     make(map[string]bool),
		AISlot:       -1,
		gameCommands: make(chan CommandRequest, 128),
		stopCh:       make(chan struct{}),
		gameState:    game.NewState(),
		commands:     DefaultRegistry(),
		startVotes:   make(map[string]bool),
	}
	if gameMap != nil {
		room.gameState.SetMap(gameMap)
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

func (m *Manager) nextCode() string {
	for {
		code := fmt.Sprintf("%s%s", strings.ToUpper(m.cfg.RoomCodePrefix), randomCode(4))
		if _, exists := m.rooms[code]; !exists {
			return code
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
	GameMap    *game.Map
	ServerUUID string
	MaxPlayers int
	MaxUnits   int
	Credits    int32
	FogOfWar   int32
	Income     float32
	CreatedAt  time.Time

	mu           sync.RWMutex
	InGame       bool
	OwnerID      string
	players      []*Session
	adminIDs     map[string]bool
	AISlot       int
	gameCommands chan CommandRequest
	stopCh       chan struct{}
	wg           sync.WaitGroup
	gameState    *game.State
	commands     *CommandRegistry
	startVotes   map[string]bool
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

	ticker := time.NewTicker(time.Second / 60)
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
		// Dropping commands if the channel is full.
		// Consider logging this event if it's important.
	}
}

func (r *Room) processTick() {
	commands := r.drainCommands()
	for _, cmdRequest := range commands {
		subCommands, err := protocol.ParseSubCommands(cmdRequest.Packet.Data)
		if err != nil {
			// Consider logging this error
			continue
		}

		for _, subCmd := range subCommands {
			switch sc := subCmd.(type) {
			case protocol.SubCommandChatPacket:
				r.handleChat(cmdRequest.Sender, sc.Message)
			case protocol.SubCommandUnitAddPacket:
				r.gameState.AddUnit(sc.UnitType, cmdRequest.Sender.ID(), sc.X, sc.Y)
			case protocol.SubCommandUnitMovePacket:
				ids := make([]game.UnitID, len(sc.UnitIDs))
				for i, id := range sc.UnitIDs {
					ids[i] = game.UnitID(id)
				}
				// TODO: Add security check: does cmdRequest.Sender own these units?
				r.gameState.MoveUnits(ids, sc.X, sc.Y)
			}
		}
	}

	// Broadcast game state updates.
	changes := r.gameState.CollectAndClearChanges()
	if len(changes) > 0 {
		var w protocol.Writer
		for unitID, changeType := range changes {
			unit, ok := r.gameState.GetUnit(unitID)
			if !ok {
				continue
			}

			switch changeType {
			case game.ChangeTypeCreated:
				addCmd := protocol.SubCommandServerUnitAddPacket{
					UnitID:   int64(unit.ID),
					UnitType: unit.Type,
					X:        unit.X,
					Y:        unit.Y,
					Owner:    unit.Owner, // This now correctly holds the PlayerID
				}
				_ = addCmd.Encode(&w)
			case game.ChangeTypeMoved:
				moveCmd := protocol.SubCommandServerUnitMovePacket{
					UnitID: int64(unit.ID),
					X:      unit.X,
					Y:      unit.Y,
				}
				_ = moveCmd.Encode(&w)
			}
		}

		if len(w.Bytes()) > 0 {
			gameCmd := protocol.GameCommandPacket{
				Packet: protocol.Packet{
					Type: protocol.TypeGameCommand,
					Body: w.Bytes(),
				},
				Data: w.Bytes(),
			}
			r.broadcast(gameCmd.Packet)
		}
	}
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

	_ = target.systemMessage("你已被移出房间。")
	target.close()
	return nil
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
	w.Int32(1)
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
