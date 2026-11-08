package database

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// ErrMigrationsDirNotFound reports that no migrations could be located.
// Starting without versioned migrations would leave the database without
// tables, so callers must treat this as fatal instead of skipping.
// Since migrations are embedded into the binary (see migrationsFS), this
// can only trigger if the embedded source is unreadable — a build defect.
var ErrMigrationsDirNotFound = errors.New("migrations directory not found")

// migrationsFS embeds the migration SQL so the artifact carries them:
// a single self-contained binary runs migrations from any working
// directory, with no migrations directory required next to the binary or
// in the install root (2026-11-08: a bare binary started from an
// arbitrary CWD failed to boot because the CWD-relative directory was
// absent).
//
//go:embed migrations/sqlite/*.sql migrations/mysql/*.sql
var migrationsFS embed.FS

// RunMigrations runs database migrations using golang-migrate.
// dbType should be "sqlite3" or "mysql"; dsn is the database connection
// string. The migration source is the embedded migrationsFS (subdirectory
// per dialect), independent of the process working directory.
func RunMigrations(dbType, dsn string, logger *log.Logger) error {
	var sub string
	switch dbType {
	case "sqlite", "sqlite3":
		sub = "sqlite"
	case "mysql":
		sub = "mysql"
	default:
		return fmt.Errorf("unsupported database type for migrations: %s", dbType)
	}

	// Open database connection for migrations
	db, err := sql.Open(dbType, dsn)
	if err != nil {
		return fmt.Errorf("open database for migrations: %w", err)
	}
	defer db.Close()

	// Get the appropriate database driver
	var driver database.Driver
	switch dbType {
	case "sqlite3":
		driver, err = sqlite3.WithInstance(db, &sqlite3.Config{})
	case "mysql":
		driver, err = mysql.WithInstance(db, &mysql.Config{})
	}
	if err != nil {
		return fmt.Errorf("create migration driver: %w", err)
	}

	// Embedded source: migrations/<sub> inside the binary.
	src, err := iofs.New(migrationsFS, filepath.Join("migrations", sub))
	if err != nil {
		return fmt.Errorf("%w: embedded migrations/%s: %v", ErrMigrationsDirNotFound, sub, err)
	}

	// Create migrate instance
	m, err := migrate.NewWithInstance("iofs", src, dbType, driver)
	if err != nil {
		return fmt.Errorf("create migrate instance: %w", err)
	}
	defer m.Close()

	// Run migrations
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("run migrations: %w", err)
	}

	logger.Printf("[migrate] migrations completed successfully")
	return nil
}

// RunMigrationsForDB runs migrations for the given database configuration.
func RunMigrationsForDB(dbType, dsn string, logger *log.Logger) error {
	// Normalize dbType for the driver
	driverType := dbType
	if dbType == "sqlite" {
		driverType = "sqlite3"
	}
	return RunMigrations(driverType, dsn, logger)
}
