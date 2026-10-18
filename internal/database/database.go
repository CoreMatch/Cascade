package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

// DB wraps the sql.DB connection with convenience methods.
type DB struct {
	conn   *sql.DB
	logger *log.Logger
}

// Open opens (or creates) the SQLite database at the given path and runs migrations.
func Open(dbPath string, logger *log.Logger) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	conn, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	conn.SetMaxOpenConns(1)

	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	db := &DB{conn: conn, logger: logger}

	if err := Migrate(conn, logger, 0); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return db, nil
}

// Close closes the underlying database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// Conn returns the raw *sql.DB connection.
func (db *DB) Conn() *sql.DB {
	return db.conn
}

// PlayerBinding represents a mapping between a game UUID and an HA user UUID.
// CBH (Created By Human): 0 = proxy account (guest), 1 = claimed account (authenticated).
type PlayerBinding struct {
	ID          int64
	GameUUID    string
	HAUserUUID  string
	PlayerName  string
	CBH         int // 0=proxy, 1=claimed
	QuickAuth   bool
	LastLogin   string
	ClaimedAt   *string
	CreatedAt   string
}

// GetBindingByGameUUID looks up a player binding by game UUID.
func (db *DB) GetBindingByGameUUID(gameUUID string) (*PlayerBinding, error) {
	var b PlayerBinding
	var quickAuth int
	var claimedAt sql.NullString
	err := db.conn.QueryRow(
		"SELECT id, game_uuid, ha_user_uuid, player_name, cbh, quick_auth, last_login, claimed_at, created_at FROM player_bindings WHERE game_uuid = ?",
		gameUUID,
	).Scan(&b.ID, &b.GameUUID, &b.HAUserUUID, &b.PlayerName, &b.CBH, &quickAuth, &b.LastLogin, &claimedAt, &b.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.QuickAuth = quickAuth != 0
	if claimedAt.Valid {
		b.ClaimedAt = &claimedAt.String
	}
	return &b, nil
}

// GetBindingByPlayerName looks up a player binding by player name.
func (db *DB) GetBindingByPlayerName(playerName string) (*PlayerBinding, error) {
	var b PlayerBinding
	var quickAuth int
	var claimedAt sql.NullString
	err := db.conn.QueryRow(
		"SELECT id, game_uuid, ha_user_uuid, player_name, cbh, quick_auth, last_login, claimed_at, created_at FROM player_bindings WHERE player_name = ?",
		playerName,
	).Scan(&b.ID, &b.GameUUID, &b.HAUserUUID, &b.PlayerName, &b.CBH, &quickAuth, &b.LastLogin, &claimedAt, &b.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.QuickAuth = quickAuth != 0
	if claimedAt.Valid {
		b.ClaimedAt = &claimedAt.String
	}
	return &b, nil
}

// CreateProxyBinding creates a new proxy binding (CBH=0) for a guest player.
func (db *DB) CreateProxyBinding(gameUUID, playerName string) error {
	_, err := db.conn.Exec(`
		INSERT INTO player_bindings (game_uuid, ha_user_uuid, player_name, cbh, quick_auth, last_login)
		VALUES (?, '', ?, 0, 1, CURRENT_TIMESTAMP)
		ON CONFLICT(game_uuid) DO UPDATE SET
			player_name = excluded.player_name,
			last_login = CURRENT_TIMESTAMP
	`, gameUUID, playerName)
	return err
}

// ClaimBinding marks a proxy binding as claimed (CBH=1) and links it to an HA user.
func (db *DB) ClaimBinding(gameUUID, haUserUUID string) error {
	_, err := db.conn.Exec(`
		UPDATE player_bindings
		SET ha_user_uuid = ?, cbh = 1, claimed_at = CURRENT_TIMESTAMP, last_login = CURRENT_TIMESTAMP
		WHERE game_uuid = ? AND cbh = 0
	`, haUserUUID, gameUUID)
	return err
}

// UpdateBindingHAUUID updates the HA user UUID for an existing binding.
func (db *DB) UpdateBindingHAUUID(gameUUID, haUserUUID string) error {
	_, err := db.conn.Exec(`
		UPDATE player_bindings
		SET ha_user_uuid = ?, last_login = CURRENT_TIMESTAMP
		WHERE game_uuid = ?
	`, haUserUUID, gameUUID)
	return err
}

// SetQuickAuth updates the quick_auth setting for a player binding.
func (db *DB) SetQuickAuth(gameUUID string, enabled bool) error {
	val := 0
	if enabled {
		val = 1
	}
	_, err := db.conn.Exec("UPDATE player_bindings SET quick_auth = ? WHERE game_uuid = ?", val, gameUUID)
	return err
}

// CleanupStaleProxyBindings deletes proxy bindings (CBH=0) that haven't logged in for30 days.
func (db *DB) CleanupStaleProxyBindings() (int64, error) {
	result, err := db.conn.Exec(`
		DELETE FROM player_bindings
		WHERE cbh = 0 AND last_login < datetime('now', '-30 days')
	`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}