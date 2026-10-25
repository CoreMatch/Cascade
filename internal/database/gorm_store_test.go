package database

import (
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestMigratedSchemaSurvivesAutoMigrate guards the sqlite schema against GORM's
// DDL parser. GORM re-reads CREATE TABLE statements to diff columns, and a
// table-level UNIQUE(...) constraint is parsed as a column named "UNIQUE", so
// any table rebuild fails with
// "table player_bindings__temp has no column named UNIQUE".
func TestMigratedSchemaSurvivesAutoMigrate(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "cascade.db")
	logger := log.New(io.Discard, "", 0)

	if err := RunMigrations("sqlite3", dsn, "migrations/sqlite", logger); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	if err := db.AutoMigrate(&Player{}, &PlayerBinding{}); err != nil {
		t.Fatalf("auto migrate on migrated schema: %v", err)
	}

	store := &GORMStore{db: db, logger: logger}

	if err := store.CreateProxyBinding("game-uuid-1", "guest"); err != nil {
		t.Fatalf("create proxy binding: %v", err)
	}

	if err := store.SetLang("game-uuid-1", "zh-CN"); err != nil {
		t.Fatalf("set lang: %v", err)
	}
	lang, err := store.GetLang("game-uuid-1")
	if err != nil {
		t.Fatalf("get lang: %v", err)
	}
	if lang != "zh-CN" {
		t.Fatalf("lang = %q, want %q", lang, "zh-CN")
	}

	binding, err := store.GetBindingByGameUUID("game-uuid-1")
	if err != nil {
		t.Fatalf("get binding: %v", err)
	}
	if binding == nil || binding.PlayerName != "guest" {
		t.Fatalf("binding = %+v, want player name %q", binding, "guest")
	}

	// The unique constraint must survive the migration rewrite.
	dup := db.Exec(
		"INSERT INTO player_bindings (game_uuid, last_login, created_at) VALUES (?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)",
		"game-uuid-1",
	)
	if dup.Error == nil {
		t.Fatal("duplicate game_uuid was accepted, unique constraint is missing")
	}
	if !strings.Contains(strings.ToUpper(dup.Error.Error()), "UNIQUE") {
		t.Fatalf("duplicate insert failed for an unexpected reason: %v", dup.Error)
	}
}
