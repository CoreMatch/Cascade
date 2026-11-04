package database

import (
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// migratedTestStore returns a GORMStore backed by a freshly migrated
// temporary sqlite database.
func migratedTestStore(t *testing.T) *GORMStore {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "cascade.db")
	logger := log.New(io.Discard, "", 0)
	if err := RunMigrations("sqlite3", dsn, "migrations/sqlite", logger); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return &GORMStore{db: db, logger: logger}
}

func TestCreateProxyBindingUpsertsWithoutClobberingClaims(t *testing.T) {
	store := migratedTestStore(t)

	if err := store.CreateProxyBinding("game-uuid-a", "first-name"); err != nil {
		t.Fatalf("create binding: %v", err)
	}
	if err := store.ClaimBinding("game-uuid-a", "ha-user-1"); err != nil {
		t.Fatalf("claim binding: %v", err)
	}

	// A re-login (CreateProxyBinding again) refreshes name/last_login but
	// must not reset the claim state.
	if err := store.CreateProxyBinding("game-uuid-a", "renamed"); err != nil {
		t.Fatalf("re-create binding: %v", err)
	}
	binding, err := store.GetBindingByGameUUID("game-uuid-a")
	if err != nil {
		t.Fatal(err)
	}
	if binding == nil {
		t.Fatal("binding disappeared after upsert")
	}
	if binding.PlayerName != "renamed" {
		t.Fatalf("player name = %q, want refreshed %q", binding.PlayerName, "renamed")
	}
	if binding.CBH != 1 || binding.HAUserUUID != "ha-user-1" {
		t.Fatalf("upsert clobbered the claim: cbh=%d haUserUUID=%q", binding.CBH, binding.HAUserUUID)
	}
}

func TestClaimBindingRejectsDoubleClaimAndUnknownPlayers(t *testing.T) {
	store := migratedTestStore(t)

	if err := store.ClaimBinding("never-seen", "ha-user-1"); err == nil {
		t.Fatal("claiming an unknown game UUID must fail")
	}

	if err := store.CreateProxyBinding("game-uuid-b", "guest"); err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimBinding("game-uuid-b", "ha-user-1"); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if err := store.ClaimBinding("game-uuid-b", "ha-user-2"); err == nil {
		t.Fatal("second claim on the same binding must fail")
	}

	binding, err := store.GetBindingByGameUUID("game-uuid-b")
	if err != nil {
		t.Fatal(err)
	}
	if binding == nil || binding.HAUserUUID != "ha-user-1" || binding.CBH != 1 {
		t.Fatalf("binding changed under a rejected claim: %+v", binding)
	}
}

func TestCleanupStaleProxyBindings(t *testing.T) {
	store := migratedTestStore(t)

	for _, gameUUID := range []string{"stale-guest", "stale-claimed", "fresh-guest"} {
		if err := store.CreateProxyBinding(gameUUID, "player-"+gameUUID); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ClaimBinding("stale-claimed", "ha-user-1"); err != nil {
		t.Fatal(err)
	}
	// Backdate two bindings beyond the 30-day policy window.
	stale := time.Now().AddDate(0, 0, -31)
	if err := store.db.Model(&PlayerBinding{}).
		Where("game_uuid IN ?", []string{"stale-guest", "stale-claimed"}).
		Update("last_login", stale).Error; err != nil {
		t.Fatal(err)
	}

	removed, err := store.CleanupStaleProxyBindings()
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if removed != 1 {
		t.Fatalf("cleanup removed %d bindings, want 1 (only the stale unclaimed one)", removed)
	}

	if binding, err := store.GetBindingByGameUUID("stale-guest"); err != nil || binding != nil {
		t.Fatalf("stale guest binding = %+v (err %v), want deleted", binding, err)
	}
	if binding, err := store.GetBindingByGameUUID("stale-claimed"); err != nil || binding == nil {
		t.Fatalf("claimed binding must survive cleanup regardless of age: %+v (err %v)", binding, err)
	}
	if binding, err := store.GetBindingByGameUUID("fresh-guest"); err != nil || binding == nil {
		t.Fatalf("fresh guest binding must survive cleanup: %+v (err %v)", binding, err)
	}

	// A second pass over a clean table removes nothing and does not fail.
	removed, err = store.CleanupStaleProxyBindings()
	if err != nil || removed != 0 {
		t.Fatalf("second cleanup = (%d, %v), want (0, nil)", removed, err)
	}
}

func TestGetBindingsByHAUUIDReturnsOwnedBindings(t *testing.T) {
	store := migratedTestStore(t)

	bindings, err := store.GetBindingsByHAUUID("ha-user-none")
	if err != nil {
		t.Fatal(err)
	}
	if bindings == nil || len(bindings) != 0 {
		t.Fatalf("unknown HA user = %v, want empty non-nil slice", bindings)
	}

	for _, gameUUID := range []string{"game-uuid-x", "game-uuid-y"} {
		if err := store.CreateProxyBinding(gameUUID, "guest-"+gameUUID); err != nil {
			t.Fatal(err)
		}
		if err := store.ClaimBinding(gameUUID, "ha-user-9"); err != nil {
			t.Fatal(err)
		}
	}
	// An unclaimed binding must not appear under any HA user.
	if err := store.CreateProxyBinding("game-uuid-z", "guest-z"); err != nil {
		t.Fatal(err)
	}

	bindings, err = store.GetBindingsByHAUUID("ha-user-9")
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 {
		t.Fatalf("bindings for ha-user-9 = %d, want 2 (%+v)", len(bindings), bindings)
	}
}
