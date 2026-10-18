package config

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"os"
	"strconv"

	"github.com/goccy/go-yaml"
)

const (
	configVersion   = 1
	configFilePath  = "config.yaml"
)

// Config holds all runtime configuration.
type Config struct {
	GameListenAddr       string
	HTTPListenAddr       string
	ServerID             string
	RelayHandshakeID     string
	RoomCodePrefix       string
	DefaultMapName       string
	DefaultRoomName      string
	DefaultMaxPlayers    int
	DefaultClientVersion int
	DefaultIncome        float32
	DefaultMaxUnits      int
	MapsDir              string
	ModsDir              string
	DBType               string // "sqlite" or "mysql"
	DBPath               string // SQLite path (used when DBType=sqlite)
	MySQLDSN             string // MySQL DSN (used when DBType=mysql), e.g. "user:pass@tcp(host:3306)/dbname?parseTime=true"
	RedisAddr            string
	RedisPassword        string
	RedisDB              int
	RedisPrefix          string
	HRPAuthURL           string
	HRPAuthClientID      string // OAuth2 client_id
	HRPAuthClientSecret  string // OAuth2 client_secret
	HRPAuthServiceName   string // Service name for Core /services/auth-key registration
	HeartbeatIntervalSec int
	RegisterTimeoutSec   int
	ReadTimeoutSec       int
	MaxConnections       int
	ChatRateLimit        int // max messages per second per player
}

// fileConfig mirrors the YAML structure, with a Version field for migration.
type fileConfig struct {
	Version              int     `yaml:"version"`
	GameListenAddr       string  `yaml:"game_listen_addr"`
	HTTPListenAddr       string  `yaml:"http_listen_addr"`
	ServerID             string  `yaml:"server_id"`
	RelayHandshakeID     string  `yaml:"relay_handshake_id"`
	RoomCodePrefix       string  `yaml:"room_code_prefix"`
	DefaultMapName       string  `yaml:"default_map_name"`
	DefaultRoomName      string  `yaml:"default_room_name"`
	DefaultMaxPlayers    int     `yaml:"default_max_players"`
	DefaultClientVersion int     `yaml:"default_client_version"`
	DefaultIncome        float32 `yaml:"default_income"`
	DefaultMaxUnits      int     `yaml:"default_max_units"`
	MapsDir              string  `yaml:"maps_dir"`
	ModsDir              string  `yaml:"mods_dir"`
	DBType               string  `yaml:"db_type"`
	DBPath               string  `yaml:"db_path"`
	MySQLDSN             string  `yaml:"mysql_dsn"`
	RedisAddr            string  `yaml:"redis_addr"`
	RedisPassword        string  `yaml:"redis_password"`
	RedisDB              int     `yaml:"redis_db"`
	RedisPrefix          string  `yaml:"redis_prefix"`
	HRPAuthURL           string `yaml:"hrpauth_url"`
	HRPAuthClientID      string `yaml:"hrpauth_client_id"`
	HRPAuthClientSecret  string `yaml:"hrpauth_client_secret"`
	HRPAuthServiceName   string `yaml:"hrpauth_service_name"`
	HeartbeatIntervalSec int     `yaml:"heartbeat_interval_sec"`
	RegisterTimeoutSec   int     `yaml:"register_timeout_sec"`
	ReadTimeoutSec       int     `yaml:"read_timeout_sec"`
	MaxConnections       int     `yaml:"max_connections"`
	ChatRateLimit        int     `yaml:"chat_rate_limit"`
}

// Load reads config.yaml (creating/migrating as needed), then overlays env vars.
func Load() Config {
	fc := loadOrCreateFile()
	cfg := fileConfigToConfig(fc)
	applyEnvOverrides(&cfg)
	return cfg
}

// loadOrCreateFile reads the config file, migrating if version is outdated,
// and creating with defaults if missing.
func loadOrCreateFile() fileConfig {
	data, err := os.ReadFile(configFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[config] %s not found, creating with defaults (version %d)", configFilePath, configVersion)
			fc := defaultFileConfig()
			saveToFile(fc)
			return fc
		}
		log.Printf("[config] failed to read %s: %v, using defaults", configFilePath, err)
		return defaultFileConfig()
	}

	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		log.Printf("[config] failed to parse %s: %v, using defaults", configFilePath, err)
		return defaultFileConfig()
	}

	if fc.Version < configVersion {
		log.Printf("[config] migrating %s from version %d to %d", configFilePath, fc.Version, configVersion)
		data = migrateConfig(fc.Version, data)
		if err := yaml.Unmarshal(data, &fc); err != nil {
			log.Printf("[config] failed to parse migrated config %s: %v, using defaults", configFilePath, err)
			return defaultFileConfig()
		}
		saveToFile(fc)
	} else if fc.Version > configVersion {
		log.Printf("[config] %s has version %d, but expected <= %d; using defaults",
			configFilePath, fc.Version, configVersion)
		return defaultFileConfig()
	}

	return fc
}

// defaultFileConfig returns a fileConfig populated with defaults at the current version.
func defaultFileConfig() fileConfig {
	return fileConfig{
		Version:              configVersion,
		GameListenAddr:       ":5123",
		HTTPListenAddr:       ":8080",
		ServerID:             "rwgin",
		RelayHandshakeID:     "net.rwhps.server",
		RoomCodePrefix:       "GW",
		DefaultMapName:       "unit_types_test.tmx",
		DefaultRoomName:      "Gin Room",
		DefaultMaxPlayers:    8,
		DefaultClientVersion: 151,
		DefaultIncome:        1.0,
		DefaultMaxUnits:      200,
		MapsDir:              "assets/maps",
		ModsDir:              "assets/mods",
		DBType:               "sqlite",
		DBPath:               "data/rwgin.db",
		HRPAuthServiceName:   "MCAGS-RW",
		HeartbeatIntervalSec: 2,
		RegisterTimeoutSec:   5,
		ReadTimeoutSec:       90,
		MaxConnections:       200,
		ChatRateLimit:        5,
	}
}

// saveToFile writes the fileConfig to disk as YAML.
func saveToFile(fc fileConfig) {
	data, err := yaml.Marshal(fc)
	if err != nil {
		log.Printf("[config] failed to marshal config: %v", err)
		return
	}
	if err := os.WriteFile(configFilePath, data, 0644); err != nil {
		log.Printf("[config] failed to write %s: %v", configFilePath, err)
	}
}

// migrateConfig upgrades config data from oldVersion to the next version.
// Add new cases as versions are incremented.
func migrateConfig(oldVersion int, data []byte) []byte {
	switch oldVersion {
	case 0:
		// Version 0 -> 1: add heartbeat_interval_sec and register_timeout_sec
		log.Printf("[config] migrating from v0 to v1: adding heartbeat_interval_sec, register_timeout_sec")
		var m map[string]any
		if err := yaml.Unmarshal(data, &m); err != nil {
			log.Printf("[config] migration unmarshal error: %v", err)
			return data
		}
		m["version"] = 1
		if _, ok := m["heartbeat_interval_sec"]; !ok {
			m["heartbeat_interval_sec"] = 2
		}
		if _, ok := m["register_timeout_sec"]; !ok {
			m["register_timeout_sec"] = 5
		}
		out, err := yaml.Marshal(m)
		if err != nil {
			log.Printf("[config] migration marshal error: %v", err)
			return data
		}
		return out
	default:
		log.Printf("[config] unsupported migration from version %d", oldVersion)
		return data
	}
}

// fileConfigToConfig converts a fileConfig to the runtime Config struct,
// filling in defaults for zero-valued fields.
func fileConfigToConfig(fc fileConfig) Config {
	d := defaultFileConfig()
	cfg := Config{
		GameListenAddr:       fc.GameListenAddr,
		HTTPListenAddr:       fc.HTTPListenAddr,
		ServerID:             fc.ServerID,
		RelayHandshakeID:     fc.RelayHandshakeID,
		RoomCodePrefix:       fc.RoomCodePrefix,
		DefaultMapName:       fc.DefaultMapName,
		DefaultRoomName:      fc.DefaultRoomName,
		DefaultMaxPlayers:    fc.DefaultMaxPlayers,
		DefaultClientVersion: fc.DefaultClientVersion,
		DefaultIncome:        fc.DefaultIncome,
		DefaultMaxUnits:      fc.DefaultMaxUnits,
		MapsDir:              fc.MapsDir,
		ModsDir:              fc.ModsDir,
		DBType:               fc.DBType,
		DBPath:               fc.DBPath,
		MySQLDSN:             fc.MySQLDSN,
		RedisAddr:            fc.RedisAddr,
		RedisPassword:        fc.RedisPassword,
		RedisDB:              fc.RedisDB,
		RedisPrefix:          fc.RedisPrefix,
		HRPAuthURL:           fc.HRPAuthURL,
		HRPAuthClientID:      fc.HRPAuthClientID,
		HRPAuthClientSecret:  fc.HRPAuthClientSecret,
		HRPAuthServiceName:   fc.HRPAuthServiceName,
		HeartbeatIntervalSec: fc.HeartbeatIntervalSec,
		RegisterTimeoutSec:   fc.RegisterTimeoutSec,
		ReadTimeoutSec:       fc.ReadTimeoutSec,
		MaxConnections:       fc.MaxConnections,
		ChatRateLimit:        fc.ChatRateLimit,
	}
	// Apply defaults for zero-valued string fields
	if cfg.GameListenAddr == "" {
		cfg.GameListenAddr = d.GameListenAddr
	}
	if cfg.HTTPListenAddr == "" {
		cfg.HTTPListenAddr = d.HTTPListenAddr
	}
	if cfg.ServerID == "" {
		cfg.ServerID = d.ServerID
	}
	if cfg.RelayHandshakeID == "" {
		cfg.RelayHandshakeID = d.RelayHandshakeID
	}
	if cfg.RoomCodePrefix == "" {
		cfg.RoomCodePrefix = d.RoomCodePrefix
	}
	if cfg.DefaultMapName == "" {
		cfg.DefaultMapName = d.DefaultMapName
	}
	if cfg.DefaultRoomName == "" {
		cfg.DefaultRoomName = d.DefaultRoomName
	}
	if cfg.MapsDir == "" {
		cfg.MapsDir = d.MapsDir
	}
	if cfg.ModsDir == "" {
		cfg.ModsDir = d.ModsDir
	}
	if cfg.DBPath == "" {
		cfg.DBPath = d.DBPath
	}
	if cfg.DBType == "" {
		cfg.DBType = d.DBType
	}
	// Apply defaults for zero-valued int/float fields
	if cfg.DefaultMaxPlayers == 0 {
		cfg.DefaultMaxPlayers = d.DefaultMaxPlayers
	}
	if cfg.DefaultClientVersion == 0 {
		cfg.DefaultClientVersion = d.DefaultClientVersion
	}
	if cfg.DefaultIncome == 0 {
		cfg.DefaultIncome = d.DefaultIncome
	}
	if cfg.DefaultMaxUnits == 0 {
		cfg.DefaultMaxUnits = d.DefaultMaxUnits
	}
	if cfg.HeartbeatIntervalSec == 0 {
		cfg.HeartbeatIntervalSec = d.HeartbeatIntervalSec
	}
	if cfg.RegisterTimeoutSec == 0 {
		cfg.RegisterTimeoutSec = d.RegisterTimeoutSec
	}
	if cfg.ReadTimeoutSec == 0 {
		cfg.ReadTimeoutSec = d.ReadTimeoutSec
	}
	if cfg.MaxConnections == 0 {
		cfg.MaxConnections = d.MaxConnections
	}
	if cfg.ChatRateLimit == 0 {
		cfg.ChatRateLimit = d.ChatRateLimit
	}
	return cfg
}

// applyEnvOverrides overlays environment variable values onto the config.
// Env vars always take precedence over file values.
func applyEnvOverrides(cfg *Config) {
	if v, ok := lookupEnv("RWGIN_GAME_ADDR"); ok {
		cfg.GameListenAddr = v
	}
	if v, ok := lookupEnv("RWGIN_HTTP_ADDR"); ok {
		cfg.HTTPListenAddr = v
	}
	if v, ok := lookupEnv("RWGIN_SERVER_ID"); ok {
		cfg.ServerID = v
	}
	if v, ok := lookupEnv("RWGIN_RELAY_ID"); ok {
		cfg.RelayHandshakeID = v
	}
	if v, ok := lookupEnv("RWGIN_ROOM_PREFIX"); ok {
		cfg.RoomCodePrefix = v
	}
	if v, ok := lookupEnv("RWGIN_DEFAULT_MAP"); ok {
		cfg.DefaultMapName = v
	}
	if v, ok := lookupEnv("RWGIN_DEFAULT_ROOM"); ok {
		cfg.DefaultRoomName = v
	}
	if v, ok := lookupEnvInt("RWGIN_MAX_PLAYERS"); ok {
		cfg.DefaultMaxPlayers = v
	}
	if v, ok := lookupEnvInt("RWGIN_CLIENT_VERSION"); ok {
		cfg.DefaultClientVersion = v
	}
	if v, ok := lookupEnvFloat32("RWGIN_INCOME"); ok {
		cfg.DefaultIncome = v
	}
	if v, ok := lookupEnvInt("RWGIN_MAX_UNITS"); ok {
		cfg.DefaultMaxUnits = v
	}
	if v, ok := lookupEnv("RWGIN_MAPS_DIR"); ok {
		cfg.MapsDir = v
	}
	if v, ok := lookupEnv("RWGIN_MODS_DIR"); ok {
		cfg.ModsDir = v
	}
	if v, ok := lookupEnv("RWGIN_DB_TYPE"); ok {
		cfg.DBType = v
	}
	if v, ok := lookupEnv("RWGIN_DB_PATH"); ok {
		cfg.DBPath = v
	}
	if v, ok := lookupEnv("RWGIN_MYSQL_DSN"); ok {
		cfg.MySQLDSN = v
	}
	if v, ok := lookupEnv("RWGIN_HRPAUTH_URL"); ok {
		cfg.HRPAuthURL = v
	}
	if v, ok := lookupEnv("RWGIN_HRPAUTH_CLIENT_ID"); ok {
		cfg.HRPAuthClientID = v
	}
	if v, ok := lookupEnv("RWGIN_HRPAUTH_CLIENT_SECRET"); ok {
		cfg.HRPAuthClientSecret = v
	}
	if v, ok := lookupEnv("RWGIN_HRPAUTH_SERVICE_NAME"); ok {
		cfg.HRPAuthServiceName = v
	}
	if v, ok := lookupEnvInt("RWGIN_HEARTBEAT_SEC"); ok {
		cfg.HeartbeatIntervalSec = v
	}
	if v, ok := lookupEnvInt("RWGIN_REGISTER_TIMEOUT_SEC"); ok {
		cfg.RegisterTimeoutSec = v
	}
	if v, ok := lookupEnvInt("RWGIN_READ_TIMEOUT_SEC"); ok {
		cfg.ReadTimeoutSec = v
	}
	if v, ok := lookupEnvInt("RWGIN_MAX_CONNECTIONS"); ok {
		cfg.MaxConnections = v
	}
	if v, ok := lookupEnvInt("RWGIN_CHAT_RATE_LIMIT"); ok {
		cfg.ChatRateLimit = v
	}
}

func lookupEnv(key string) (string, bool) {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value, true
	}
	return "", false
}

func lookupEnvInt(key string) (int, bool) {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func lookupEnvFloat32(key string) (float32, bool) {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		if parsed, err := strconv.ParseFloat(value, 32); err == nil {
			return float32(parsed), true
		}
	}
	return 0, false
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "rwgin-relay"
	}
	return hex.EncodeToString(buf)
}

