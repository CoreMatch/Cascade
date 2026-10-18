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
type PlayerBinding struct {
	ID          int64
	GameUUID    string
	HAUserUUID  string
	PlayerName  string
	QuickAuth   bool
	LastLogin   string
	CreatedAt   string
}

// GetBindingByGameUUID looks up a player binding by game UUID.
func (db *DB) GetBindingByGameUUID(gameUUID string) (*PlayerBinding, error) {
	var b PlayerBinding
	var quickAuth int
	err := db.conn.QueryRow(
		"SELECT id, game_uuid, ha_user_uuid, player_name, quick_auth, last_login, created_at FROM player_bindings WHERE game_uuid = ?",
		gameUUID,
	).Scan(&b.ID, &b.GameUUID, &b.HAUserUUID, &b.PlayerName, &quickAuth, &b.LastLogin, &b.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.QuickAuth = quickAuth != 0
	return &b, nil
}

// UpsertBinding creates or updates a player binding.
func (db *DB) UpsertBinding(gameUUID, haUserUUID, playerName string) error {
	_, err := db.conn.Exec(`
		INSERT INTO player_bindings (game_uuid, ha_user_uuid, player_name, quick_auth, last_login)
		VALUES (?, ?, ?, 1, CURRENT_TIMESTAMP)
		ON CONFLICT(game_uuid) DO UPDATE SET
			ha_user_uuid = excluded.ha_user_uuid,
			player_name = excluded.player_name,
			last_login = CURRENT_TIMESTAMP
	`, gameUUID, haUserUUID, playerName)
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