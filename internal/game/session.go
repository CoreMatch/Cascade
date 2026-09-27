package game

import (
	"fmt"
	"rwgin/internal/protocol"
	"time"
)

// PlayerID is the unique identifier for a player.
type PlayerID string

// PlayerConn represents the network connection for a player.
// It is an interface to decouple the game logic from the network layer.
type PlayerConn interface {
	Send(packet protocol.Packet) error
}

// Player represents a player in the game session.
type Player struct {
	ID   PlayerID
	Name string
	Conn PlayerConn
	// Add other player-specific data here, like team, resources, etc.
}

// GameState represents the state of the game world.
type GameState struct {
	// For now, this is a placeholder.
	// We will add map data, units, etc. here later.
	WorldTime int64 // Example: game time in ticks
}

// CommandRequest represents a command sent by a player.
type CommandRequest struct {
	Sender *Player
	Packet protocol.Packet
}

// Session represents a single game session or room.
type Session struct {
	ID       string
	OwnerID  PlayerID // The ID of the player who owns the session. "system" for system-owned.
	InGame   bool
	players  map[PlayerID]*Player
	commands chan CommandRequest // Channel for incoming game commands
	state    *GameState
	ticker   *time.Ticker
	quit     chan struct{}
}

// NewSession creates and initializes a new game session.
func NewSession(id string) *Session {
	return &Session{
		ID:       id,
		OwnerID:  "system", // Default owner is the system
		InGame:   false,
		players:  make(map[PlayerID]*Player),
		commands: make(chan CommandRequest, 128), // Buffered channel
		state:    &GameState{},
		quit:     make(chan struct{}),
	}
}

// Start begins the game loop for the session.
func (s *Session) Start(tickRate time.Duration) {
	s.ticker = time.NewTicker(tickRate)
	go func() {
		for {
			select {
			case <-s.ticker.C:
				s.update()
			case <-s.quit:
				s.ticker.Stop()
				return
			}
		}
	}()
}

// Stop ends the game loop for the session.
func (s *Session) Stop() {
	close(s.quit)
}

// SubmitCommand adds a command to the session's command queue.
func (s *Session) SubmitCommand(player *Player, packet protocol.Packet) {
	s.commands <- CommandRequest{
		Sender: player,
		Packet: packet,
	}
}

// update is the main game logic update function, called on each tick.
func (s *Session) update() {
	// 1. Process incoming commands
	s.processCommands()

	// 2. Update game state
	s.state.WorldTime++

	// 3. Broadcast state to players (we'll add this later)
}

func (s *Session) processCommands() {
	for {
		select {
		case cmd := <-s.commands:
			// For now, just broadcast the command to all other players.
			// In the future, we'll parse the command and update the game state here.
			s.broadcast(cmd.Packet, cmd.Sender.ID)
		default:
			// No more commands in the channel, so we can exit the loop.
			return
		}
	}
}

// GetPlayer retrieves a player from the session by their ID.
func (s *Session) GetPlayer(id PlayerID) *Player {
	// Note: This is a simple implementation. For sessions with many players,
	// we might want to use a more efficient data structure.
	player, _ := s.players[id]
	return player
}

// AddPlayer adds a player to the session and broadcasts their arrival.
func (s *Session) AddPlayer(player *Player) {
	s.players[player.ID] = player

	// Broadcast the new player's arrival to all players in the session.
	joinMsg := fmt.Sprintf("%s has joined the game.", player.Name)
	packet, err := protocol.EncodeSystemMessage(joinMsg)
	if err == nil {
		s.broadcast(packet, "") // Broadcast to all, excluding no one
	}
}

func (s *Session) HandleStartGame(starter *Player) {
	if starter.ID != s.OwnerID {
		// The player is not the owner, send them an error message.
		errMsg := "Only the session owner can start the game."
		packet, err := protocol.EncodeSystemMessage(errMsg)
		if err == nil && starter.Conn != nil {
			starter.Conn.Send(packet)
		}
		return
	}

	s.InGame = true

	// Broadcast the start game message to all players.
	startMsg := fmt.Sprintf("The game has been started by %s.", starter.Name)
	packet, err := protocol.EncodeSystemMessage(startMsg)
	if err == nil {
		s.broadcast(packet, "")
	}

	// Also broadcast the actual "start game" packet.
	startGamePacket, err := protocol.EncodeStartGame()
	if err == nil {
		s.broadcast(startGamePacket, "")
	}
}

func (s *Session) HandleChatMessage(from *Player, message string) {
	packet, err := protocol.EncodeChat(from.Name, message)
	if err == nil {
		s.broadcast(packet, from.ID) // Broadcast to all, excluding the sender
	}
}

// broadcast sends a packet to all players in the session, optionally excluding one.
func (s *Session) broadcast(packet protocol.Packet, exclude PlayerID) {
	for id, player := range s.players {
		if id != exclude && player.Conn != nil {
			player.Conn.Send(packet)
		}
	}
}

// RemovePlayer removes a player from the session.
func (s *Session) RemovePlayer(playerID PlayerID) {
	delete(s.players, playerID)
}
