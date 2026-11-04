package database

import (
	"errors"
	"fmt"
	"log"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ErrBindingNotFound reports that no player binding exists for the lookup
// key. ClaimBinding returns it when the target binding is absent or already
// claimed, so the API layer can map the failure to a 404.
var ErrBindingNotFound = errors.New("no unclaimed player binding found")

// GORMStore implements the Store interface using GORM.
type GORMStore struct {
	db     *gorm.DB
	logger *log.Logger
}

// DBConfig holds database connection configuration.
type DBConfig struct {
	Type     string // "sqlite" or "mysql"
	DSN      string // Data source name
	LogLevel gormlogger.LogLevel
}

// NewGORMStore creates a new GORMStore with the given configuration.
func NewGORMStore(cfg DBConfig, appLogger *log.Logger) (*GORMStore, error) {
	// golang-migrate owns the schema. GORM AutoMigrate is only a fallback for
	// transient migration failures. A missing migrations directory is fatal:
	// silently continuing would leave the database without its tables.
	// Running AutoMigrate on a schema created by the SQL migrations is unsafe:
	// the sqlite driver parses CREATE TABLE DDL to diff columns, and a
	// table-level UNIQUE(...) constraint is misread as a column, so any
	// table rebuild fails with "table ...__temp has no column named UNIQUE".
	migrated := true
	if err := RunMigrationsForDB(cfg.Type, cfg.DSN, appLogger); err != nil {
		if errors.Is(err, ErrMigrationsDirNotFound) {
			return nil, fmt.Errorf("refusing to start without database migrations: %w", err)
		}
		appLogger.Printf("[db] warning: golang-migrate failed: %v, falling back to GORM AutoMigrate", err)
		migrated = false
	}

	var dialector gorm.Dialector

	switch cfg.Type {
	case "sqlite":
		dialector = sqlite.Open(cfg.DSN + "?_journal_mode=WAL&_busy_timeout=5000")
	case "mysql":
		dialector = mysql.Open(cfg.DSN)
	default:
		return nil, fmt.Errorf("unsupported database type: %s", cfg.Type)
	}

	gormCfg := &gorm.Config{
		Logger: gormlogger.Default.LogMode(cfg.LogLevel),
	}

	db, err := gorm.Open(dialector, gormCfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Configure connection pool for MySQL
	if cfg.Type == "mysql" {
		sqlDB, err := db.DB()
		if err != nil {
			return nil, fmt.Errorf("get underlying sql.DB: %w", err)
		}
		sqlDB.SetMaxOpenConns(25)
		sqlDB.SetMaxIdleConns(5)
		sqlDB.SetConnMaxLifetime(5 * time.Minute)
	}

	// Only fall back to AutoMigrate when the versioned migrations did not run.
	if !migrated {
		if err := db.AutoMigrate(&Player{}, &PlayerBinding{}); err != nil {
			return nil, fmt.Errorf("auto migrate: %w", err)
		}
	}

	appLogger.Printf("[db] connected to %s database", cfg.Type)

	return &GORMStore{
		db:     db,
		logger: appLogger,
	}, nil
}

// Close closes the underlying database connection.
func (s *GORMStore) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// GetBindingByGameUUID looks up a player binding by game UUID.
func (s *GORMStore) GetBindingByGameUUID(gameUUID string) (*PlayerBinding, error) {
	var binding PlayerBinding
	err := s.db.Where("game_uuid = ?", gameUUID).First(&binding).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &binding, nil
}

// GetBindingsByHAUUID returns all bindings linked to the given HA user UUID.
func (s *GORMStore) GetBindingsByHAUUID(haUserUUID string) ([]PlayerBinding, error) {
	var bindings []PlayerBinding
	err := s.db.Where("ha_user_uuid = ?", haUserUUID).Order("last_login DESC").Find(&bindings).Error
	if err != nil {
		return nil, err
	}
	if bindings == nil {
		bindings = []PlayerBinding{}
	}
	return bindings, nil
}

// CreateProxyBinding creates or updates a proxy binding (CBH=0) for a guest player.
func (s *GORMStore) CreateProxyBinding(gameUUID, playerName string) error {
	now := time.Now()
	binding := PlayerBinding{
		GameUUID:   gameUUID,
		PlayerName: playerName,
		CBH:        0,
		QuickAuth:  true,
		LastLogin:  now,
	}

	// Use GORM's Save with Where for upsert behavior
	result := s.db.Where("game_uuid = ?", gameUUID).Assign(map[string]interface{}{
		"player_name": playerName,
		"last_login":  now,
	}).FirstOrCreate(&binding)

	return result.Error
}

// ClaimBinding marks a proxy binding as claimed (CBH=1) and links it to an HA user.
func (s *GORMStore) ClaimBinding(gameUUID, haUserUUID string) error {
	now := time.Now()
	result := s.db.Model(&PlayerBinding{}).
		Where("game_uuid = ? AND cbh = 0", gameUUID).
		Updates(map[string]interface{}{
			"ha_user_uuid": haUserUUID,
			"cbh":          1,
			"claimed_at":   now,
			"last_login":   now,
		})

	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("game_uuid %s: %w", gameUUID, ErrBindingNotFound)
	}
	return nil
}

// GetLang returns the language preference for a player binding.
func (s *GORMStore) GetLang(gameUUID string) (string, error) {
	var binding PlayerBinding
	err := s.db.Select("lang").Where("game_uuid = ?", gameUUID).First(&binding).Error
	if err == gorm.ErrRecordNotFound {
		return "en", nil
	}
	if err != nil {
		return "en", err
	}
	if binding.Lang == "" {
		return "en", nil
	}
	return binding.Lang, nil
}

// SetLang updates the language preference for a player binding.
func (s *GORMStore) SetLang(gameUUID, lang string) error {
	result := s.db.Model(&PlayerBinding{}).
		Where("game_uuid = ?", gameUUID).
		Update("lang", lang)
	return result.Error
}

// CleanupStaleProxyBindings deletes proxy bindings (CBH=0) that haven't
// logged in for 30 days. The expiry boundary is strict: a binding is deleted
// only when last_login < now-30d, so a binding whose last_login equals the
// cutoff exactly is kept for one more period.
func (s *GORMStore) CleanupStaleProxyBindings() (int64, error) {
	return s.cleanupStaleBefore(time.Now().AddDate(0, 0, -30))
}

// cleanupStaleBefore deletes unclaimed bindings whose last_login is strictly
// before cutoff. Tests inject an exact cutoff to pin the boundary semantics.
func (s *GORMStore) cleanupStaleBefore(cutoff time.Time) (int64, error) {
	result := s.db.Where("cbh = 0 AND last_login < ?", cutoff).Delete(&PlayerBinding{})
	return result.RowsAffected, result.Error
}
