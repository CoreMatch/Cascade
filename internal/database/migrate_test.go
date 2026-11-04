package database

import (
	"database/sql"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cascade/internal/paths"
)

// stubExecutableDir points paths.ExecutableDir at dir for the duration of
// the test, simulating a binary installed away from the source checkout.
func stubExecutableDir(t *testing.T, dir string) {
	t.Helper()
	orig := paths.ExecutableDir
	paths.ExecutableDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { paths.ExecutableDir = orig })
}

// copySQLiteMigrations copies the migrations shipped with this package
// (present because tests run from the package directory) into dst.
func copySQLiteMigrations(t *testing.T, dst string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("migrations", "sqlite"))
	if err != nil {
		t.Fatalf("read bundled migrations: %v", err)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join("migrations", "sqlite", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dst, entry.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRunMigrationsResolvesFromExecutableDir covers the standalone-binary
// deployment: the working directory differs from the executable directory
// and only the executable directory carries the migrations.
func TestRunMigrationsResolvesFromExecutableDir(t *testing.T) {
	exeDir := t.TempDir()
	copySQLiteMigrations(t, filepath.Join(exeDir, "internal", "database", "migrations", "sqlite"))
	stubExecutableDir(t, exeDir)

	// Run from a working directory that has no migrations at all.
	t.Chdir(t.TempDir())

	dsn := filepath.Join(t.TempDir(), "cascade.db")
	logger := log.New(io.Discard, "", 0)
	if err := RunMigrationsForDB("sqlite", dsn, logger); err != nil {
		t.Fatalf("migrations failed when only the executable dir has them: %v", err)
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

// TestRunMigrationsMissingDirectoryIsFatal ensures a missing migrations
// directory aborts startup instead of silently skipping table creation.
func TestRunMigrationsMissingDirectoryIsFatal(t *testing.T) {
	stubExecutableDir(t, t.TempDir())
	t.Chdir(t.TempDir())

	dsn := filepath.Join(t.TempDir(), "cascade.db")
	logger := log.New(io.Discard, "", 0)

	err := RunMigrationsForDB("sqlite", dsn, logger)
	if !errors.Is(err, ErrMigrationsDirNotFound) {
		t.Fatalf("expected ErrMigrationsDirNotFound, got: %v", err)
	}

	if _, err := NewGORMStore(DBConfig{Type: "sqlite", DSN: dsn}, logger); err == nil {
		t.Fatal("NewGORMStore must refuse to start without a migrations directory")
	} else if !strings.Contains(err.Error(), "migrations") {
		t.Fatalf("startup error should point at migrations, got: %v", err)
	}
}
