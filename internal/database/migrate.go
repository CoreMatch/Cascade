package database

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	"github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/file"
)

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

	// Get absolute path to migrations directory
	absMigrationsDir, err := filepath.Abs(migrationsDir)
	if err != nil {
		return fmt.Errorf("get absolute path: %w", err)
	}

	// Check if migrations directory exists
	if _, err := os.Stat(absMigrationsDir); os.IsNotExist(err) {
		logger.Printf("[migrate] migrations directory not found: %s, skipping", absMigrationsDir)
		return nil
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