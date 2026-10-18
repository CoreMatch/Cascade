package database

import "time"

// Store defines the database operations interface.
// All database implementations (SQLite, MySQL) must satisfy this interface.
type Store interface {
	// GetBindingByGameUUID looks up a player binding by game UUID.
	// Returns nil, nil if not found.
	GetBindingByGameUUID(gameUUID string) (*PlayerBinding, error)

	// GetBindingByPlayerName looks up a player binding by player name.
	// Returns nil, nil if not found.
	GetBindingByPlayerName(playerName string) (*PlayerBinding, error)

	// GetBindingsByHAUUID returns all bindings linked to the given HA user UUID.
	// Returns an empty slice (not nil) if none found.
	GetBindingsByHAUUID(haUserUUID string) ([]PlayerBinding, error)

	// CreateProxyBinding creates or updates a proxy binding (CBH=0) for a guest player.
	CreateProxyBinding(gameUUID, playerName string) error

	// ClaimBinding marks a proxy binding as claimed (CBH=1) and links it to an HA user.
	ClaimBinding(gameUUID, haUserUUID string) error

	// UpdateBindingHAUUID updates the HA user UUID for an existing binding.
	UpdateBindingHAUUID(gameUUID, haUserUUID string) error

	// SetQuickAuth updates the quick_auth setting for a player binding.
	SetQuickAuth(gameUUID string, enabled bool) error

	// GetLang returns the language preference for a player binding.
	// Returns "en" if not found.
	GetLang(gameUUID string) (string, error)

	// SetLang updates the language preference for a player binding.
	SetLang(gameUUID, lang string) error

	// CleanupStaleProxyBindings deletes proxy bindings (CBH=0) that haven't logged in for 30 days.
	CleanupStaleProxyBindings() (int64, error)

	// Close closes the underlying database connection.
	Close() error
}

// Player represents a game player record.
type Player struct {
	ID        int64     `gorm:"primaryKey;autoIncrement"`
	PlayerID  string    `gorm:"uniqueIndex;not null;size:255"`
	Name      string    `gorm:"not null;default:'';size:255"`
	CreatedAt time.Time `gorm:"not null;autoCreateTime"`
	UpdatedAt time.Time `gorm:"not null;autoUpdateTime"`
}

// TableName overrides the table name for Player.
func (Player) TableName() string { return "players" }

// PlayerBinding represents a mapping between a game UUID and an HA user UUID.
// CBH (Created By Human): 0 = proxy account (guest), 1 = claimed account (authenticated).
type PlayerBinding struct {
	ID         int64      `gorm:"primaryKey;autoIncrement"`
	GameUUID   string     `gorm:"uniqueIndex;not null;size:255"`
	HAUserUUID string     `gorm:"index;not null;default:'';size:255"`
	PlayerName string     `gorm:"not null;default:'';size:255"`
	Lang       string     `gorm:"not null;default:'en';size:10"`
	CBH        int        `gorm:"not null;default:0;index"`
	QuickAuth  bool       `gorm:"not null;default:true"`
	LastLogin  time.Time  `gorm:"not null;autoCreateTime"`
	ClaimedAt  *time.Time `gorm:"default:null"`
	CreatedAt  time.Time  `gorm:"not null;autoCreateTime"`
}

// TableName overrides the table name for PlayerBinding.
func (PlayerBinding) TableName() string { return "player_bindings" }