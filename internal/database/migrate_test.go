package database

import (
	"database/sql"
	"io"
	"io/fs"
	"log"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunMigrationsEmbeddedFromAnyDirectory is the pitfall regression:
// a bare binary started from an arbitrary working directory must run its
// migrations with nothing on disk — the SQL ships inside the binary.
// (Previously migrations were resolved CWD-relative, so a deployed binary
// without a bundled migrations directory failed to boot.)
func TestRunMigrationsEmbeddedFromAnyDirectory(t *testing.T) {
	// Empty CWD and empty executable dir: no migrations anywhere on disk.
	t.Chdir(t.TempDir())

	dsn := filepath.Join(t.TempDir(), "cascade.db")
	logger := log.New(io.Discard, "", 0)
	if err := RunMigrationsForDB("sqlite", dsn, logger); err != nil {
		t.Fatalf("migrations failed without any migrations on disk: %v", err)
	}

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	tables := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"players", "player_bindings", "schema_migrations"} {
		if !tables[want] {
			t.Fatalf("table %q was not created, got tables: %v", want, tables)
		}
	}
}

// TestEmbeddedMigrationsShipBothDialects guards the embed glob: both
// dialect directories must be compiled into the binary with up-migrations
// present. An empty embed would surface only at runtime otherwise.
func TestEmbeddedMigrationsShipBothDialects(t *testing.T) {
	for _, sub := range []string{"sqlite", "mysql"} {
		entries, err := fs.ReadDir(migrationsFS, filepath.Join("migrations", sub))
		if err != nil {
			t.Fatalf("embedded migrations/%s unreadable: %v", sub, err)
		}
		ups := 0
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".up.sql") {
				ups++
			}
		}
		if ups == 0 {
			t.Fatalf("embedded migrations/%s contains no .up.sql files", sub)
		}
	}
}

// TestRunMigrationsUnsupportedType keeps the explicit refusal for unknown
// database types (no silent skip).
func TestRunMigrationsUnsupportedType(t *testing.T) {
	logger := log.New(io.Discard, "", 0)
	if err := RunMigrationsForDB("postgres", "dsn", logger); err == nil {
		t.Fatal("unsupported database type must be rejected")
	}

	// NewGORMStore must surface the refusal as a startup failure.
	if _, err := NewGORMStore(DBConfig{Type: "postgres", DSN: "dsn"}, logger); err == nil {
		t.Fatal("NewGORMStore must refuse an unsupported database type")
	}
}
