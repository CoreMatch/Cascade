package database

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"cascade/internal/paths"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/file"
)

// ErrMigrationsDirNotFound reports that no migrations directory could be
// located. Starting without versioned migrations would leave the database
// without tables, so callers must treat this as fatal instead of skipping.
var ErrMigrationsDirNotFound = errors.New("migrations directory not found")

// RunMigrations runs database migrations using golang-migrate.
// dbType should be "sqlite" or "mysql".
// dsn is the database connection string.
// migrationsDir is the path to the migrations directory.
func RunMigrations(dbType, dsn, migrationsDir string, logger *log.Logger) error {
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
	default:
		return fmt.Errorf("unsupported database type for migrations: %s", dbType)
	}
	if err != nil {
		return fmt.Errorf("create migration driver: %w", err)
	}

	// Resolve the migrations directory. Relative paths are looked up in the
	// working directory first (source checkout / go run) and next to the
	// executable second (standalone installs). A missing directory is an
	// error: silently skipping would leave the database without its tables.
	resolvedDir, ok := paths.FirstExisting(migrationsDir)
	if !ok {
		return fmt.Errorf("%w: tried %s", ErrMigrationsDirNotFound, strings.Join(paths.Candidates(migrationsDir), ", "))
	}
	absMigrationsDir, err := filepath.Abs(resolvedDir)
	if err != nil {
		return fmt.Errorf("get absolute path: %w", err)
	}

	// Create file source
	src, err := (&file.File{}).Open("file://" + absMigrationsDir)
	if err != nil {
		return fmt.Errorf("open migration source: %w", err)
	}

	// Create migrate instance
	m, err := migrate.NewWithInstance("file", src, dbType, driver)
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
	// Determine the migrations directory based on database type
	var migrationsDir string
	switch dbType {
	case "sqlite", "sqlite3":
		migrationsDir = "internal/database/migrations/sqlite"
	case "mysql":
		migrationsDir = "internal/database/migrations/mysql"
	default:
		return fmt.Errorf("unsupported database type: %s", dbType)
	}

	// Normalize dbType for the driver
	driverType := dbType
	if dbType == "sqlite" {
		driverType = "sqlite3"
	}

	return RunMigrations(driverType, dsn, migrationsDir, logger)
}