package database

import (
	"database/sql"
	"fmt"
	"log"
)

const currentSchemaVersion = 1

// Migration represents a single database migration step.
type Migration struct {
	Version     int
	Description string
	Up          func(tx *sql.Tx) error
}

// migrations holds all registered migrations in order.
var migrations = []Migration{
	{
		Version:     1,
		Description: "create players table",
		Up: func(tx *sql.Tx) error {
			_, err := tx.Exec(`
				CREATE TABLE IF NOT EXISTS players (
					id         INTEGER PRIMARY KEY AUTOINCREMENT,
					player_id  TEXT    NOT NULL UNIQUE,
					name       TEXT    NOT NULL DEFAULT '',
					created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
					updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
				);
				CREATE INDEX IF NOT EXISTS idx_players_player_id ON players(player_id);
			`)
			return err
		},
	},
}

// EnsureSchemaVersionTable creates the schema_version tracking table if it doesn't exist.
func EnsureSchemaVersionTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_version (
			version     INTEGER NOT NULL,
			applied_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);
	`)
	return err
}

// GetCurrentVersion reads the current schema version from the database.
// Returns 0 if no version has been recorded yet.
func GetCurrentVersion(db *sql.DB) (int, error) {
	var version int
	err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}

// Migrate runs all pending migrations from the current version to the target version.
// If target is 0, it migrates to the latest available version.
func Migrate(db *sql.DB, logger *log.Logger, target int) error {
	if err := EnsureSchemaVersionTable(db); err != nil {
		return fmt.Errorf("ensure schema_version table: %w", err)
	}

	current, err := GetCurrentVersion(db)
	if err != nil {
		return err
	}

	if target == 0 {
		target = currentSchemaVersion
	}

	if current >= target {
		logger.Printf("[db] schema is up to date (version %d)", current)
		return nil
	}

	logger.Printf("[db] current schema version: %d, target: %d", current, target)

	for _, m := range migrations {
		if m.Version <= current {
			continue
		}
		if m.Version > target {
			break
		}

		logger.Printf("[db] applying migration v%d: %s", m.Version, m.Description)

		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin transaction for v%d: %w", m.Version, err)
		}

		if err := m.Up(tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration v%d failed: %w", m.Version, err)
		}

		if _, err := tx.Exec("INSERT INTO schema_version (version) VALUES (?)", m.Version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record version v%d: %w", m.Version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration v%d: %w", m.Version, err)
		}

		logger.Printf("[db] migration v%d applied successfully", m.Version)
	}

	return nil
}