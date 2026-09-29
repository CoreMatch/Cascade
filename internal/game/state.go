package game

import "sync"

type ChangeType int

const (
	ChangeTypeCreated ChangeType = iota
	ChangeTypeMoved
)

// UnitID uniquely identifies a unit in the game world.
type UnitID int64

// Unit represents a single entity in the game world.
type Unit struct {
	ID    UnitID
	Type  string
	X, Y  float32
	Owner string // PlayerID
}

// State represents the entire state of a game room.
type State struct {
	mu         sync.RWMutex
	units      map[UnitID]*Unit
	nextUnitID UnitID
	changes    map[UnitID]ChangeType
	gameMap    *Map
}

// NewState creates a new game state.
func NewState() *State {
	return &State{
		units:      make(map[UnitID]*Unit),
		nextUnitID: 1,
		changes:    make(map[UnitID]ChangeType),
	}
}

func (s *State) SetMap(m *Map) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gameMap = m
}

// AddUnit creates a new unit, assigns it a unique ID, and adds it to the game state.
func (s *State) AddUnit(unitType, owner string, x, y float32) *Unit {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Optional: Check if the position is walkable
	if s.gameMap != nil && !s.gameMap.IsWalkable(x, y) {
		// For now, we allow adding units to blocked tiles (e.g., initial spawns)
		// but you might want to find the nearest walkable tile here.
	}

	newUnit := &Unit{
		ID:    s.nextUnitID,
		Type:  unitType,
		Owner: owner,
		X:     x,
		Y:     y,
	}
	s.units[s.nextUnitID] = newUnit
	s.changes[s.nextUnitID] = ChangeTypeCreated
	s.nextUnitID++

	return newUnit
}

// MoveUnits updates the position of the given units.
func (s *State) MoveUnits(ids []UnitID, x, y float32) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Collision check
	if s.gameMap != nil && !s.gameMap.IsWalkable(x, y) {
		// If the destination is blocked, we don't move the units.
		// In a real game, you might move them as close as possible.
		return
	}

	for _, id := range ids {
		if unit, ok := s.units[id]; ok {
			unit.X = x
			unit.Y = y
			// If the unit was just created in this tick, we don't need to
			// also mark it as moved.
			if _, exists := s.changes[id]; !exists {
				s.changes[id] = ChangeTypeMoved
			}
		}
	}
}

// CollectAndClearChanges returns the list of units that have changed since the last call
// and clears the internal list.
func (s *State) CollectAndClearChanges() map[UnitID]ChangeType {
	s.mu.Lock()
	defer s.mu.Unlock()

	changes := s.changes
	s.changes = make(map[UnitID]ChangeType)
	return changes
}

func (s *State) GetUnit(id UnitID) (*Unit, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	unit, ok := s.units[id]
	return unit, ok
}
