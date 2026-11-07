package config

import (
	"log"
	"os"
	"strconv"

	"cascade/internal/paths"

	"github.com/goccy/go-yaml"
)

const (
	configVersion  = 2
	configFilePath = "config.yaml"
)

type BackendConfig struct {
	ID          string   `yaml:"id" json:"id"`
	ControlURL  string   `yaml:"control_url" json:"controlUrl"`
	GameAddress string   `yaml:"game_address" json:"gameAddress"`
	Prefixes    []string `yaml:"prefixes" json:"prefixes"`
	// TrustMode is the backend trust strategy the operator declares for this
	// backend ("open", "ip", or "pinned"). It drives the transport selection
	// when TLSMode is "auto"; see backend.ResolveTransport.
	TrustMode string `yaml:"trust_mode" json:"trustMode"`
	// TLSMode selects the transport toward this backend: "auto" (default,
	// derived from TrustMode), "mtls", or "plain".
	TLSMode string `yaml:"tls_mode" json:"tlsMode"`
	// JumpPort, when > 0, makes the proxy listen on an extra public port that
	// routes straight to this backend regardless of the Preregister room code.
	// It is the landing port used by the lobby jump (PACKET_RECONNECT_TO), so a
	// vanilla client can be steered here without exposing the backend address.
	JumpPort int `yaml:"jump_port" json:"jumpPort"`
}

// Config holds all runtime configuration.
type Config struct {
	GameListenAddr           string
	HTTPListenAddr           string
	// PublicGameHost is the host (domain or IP) vanilla clients use to reach the
	// proxy. It is placed into PACKET_RECONNECT_TO targets together with a
	// backend jump port, so it must be publicly resolvable/reachable. Empty
	// disables the lobby jump (the proxy then refuses /goto).
	PublicGameHost string
	DBType                   string // "sqlite" or "mysql"
	DBPath                   string // SQLite path (used when DBType=sqlite)
	MySQLDSN                 string // MySQL DSN (used when DBType=mysql), e.g. "user:pass@tcp(host:3306)/dbname?parseTime=true"
	RedisAddr                string
	RedisPassword            string
	RedisDB                  int
	RedisPrefix              string
	HRPAuthURL               string
	HRPAuthClientID          string // OAuth2 client_id
	HRPAuthClientSecret      string // OAuth2 client_secret
	HRPAuthServiceName       string // Service name for Core /services/auth-key registration
	HRPAuthRelayPrefix       string // Relay path prefix for Core gateway (e.g. /rusted-warfare)
	PresenceTTLSec           int    // Presence record TTL in seconds; renewed at half this interval, <=0 disables renewal (record never expires)
	RegisterTimeoutSec       int
	ReadTimeoutSec           int
	MaxConnections           int
	ChatRateLimit            int // max messages per second per player
	DefaultBackendID         string
	Backends                 []BackendConfig
	ProxyIssuer              string
	ProxyKeyID               string
	ProxyPrivateKeyPath      string
	BackendClientCert        string
	BackendClientKey         string
	BackendCAPool            string
	HealthIntervalSec        int
	RoomSnapshotIntervalSec  int
	PendingSessionTimeoutSec int
}

// fileConfig mirrors the YAML structure, with a Version field for migration.
// Keys removed over time (legacy game-server fields such as server_id,
// relay_handshake_id, room_code_prefix, default_*, maps_dir, mods_dir,
// heartbeat_interval_sec) are ignored on load; see knowledge entry 0001.
type fileConfig struct {
	Version                  int             `yaml:"version"`
	GameListenAddr           string          `yaml:"game_listen_addr"`
	HTTPListenAddr           string          `yaml:"http_listen_addr"`
	PublicGameHost           string          `yaml:"public_game_host"`
	DBType                   string          `yaml:"db_type"`
	DBPath                   string          `yaml:"db_path"`
	MySQLDSN                 string          `yaml:"mysql_dsn"`
	RedisAddr                string          `yaml:"redis_addr"`
	RedisPassword            string          `yaml:"redis_password"`
	RedisDB                  int             `yaml:"redis_db"`
	RedisPrefix              string          `yaml:"redis_prefix"`
	HRPAuthURL               string          `yaml:"hrpauth_url"`
	HRPAuthClientID          string          `yaml:"hrpauth_client_id"`
	HRPAuthClientSecret      string          `yaml:"hrpauth_client_secret"`
	HRPAuthServiceName       string          `yaml:"hrpauth_service_name"`
	HRPAuthRelayPrefix       string          `yaml:"hrpauth_relay_prefix"`
	PresenceTTLSec           int             `yaml:"presence_ttl_sec"`
	RegisterTimeoutSec       int             `yaml:"register_timeout_sec"`
	ReadTimeoutSec           int             `yaml:"read_timeout_sec"`
	MaxConnections           int             `yaml:"max_connections"`
	ChatRateLimit            int             `yaml:"chat_rate_limit"`
	DefaultBackendID         string          `yaml:"default_backend_id"`
	Backends                 []BackendConfig `yaml:"backends"`
	ProxyIssuer              string          `yaml:"proxy_issuer"`
	ProxyKeyID               string          `yaml:"proxy_key_id"`
	ProxyPrivateKeyPath      string          `yaml:"proxy_private_key_path"`
	BackendClientCert        string          `yaml:"backend_client_cert"`
	BackendClientKey         string          `yaml:"backend_client_key"`
	BackendCAPool            string          `yaml:"backend_ca_pool"`
	HealthIntervalSec        int             `yaml:"health_interval_sec"`
	RoomSnapshotIntervalSec  int             `yaml:"room_snapshot_interval_sec"`
	PendingSessionTimeoutSec int             `yaml:"pending_session_timeout_sec"`
}

// Load reads config.yaml (creating/migrating as needed), then overlays env vars.
func Load() Config {
	fc := loadOrCreateFile()
	cfg := fileConfigToConfig(fc)
	applyEnvOverrides(&cfg)
	return cfg
}

// loadOrCreateFile reads the config file, migrating if version is outdated,
// and creating with defaults if missing. An existing config.yaml in the
// working directory takes precedence, then one next to the executable, so a
// standalone binary started from a foreign cwd still finds its config. A
// brand-new file is always created in the working directory.
func loadOrCreateFile() fileConfig {
	configPath, found := paths.FirstExisting(configFilePath)
	if !found {
		configPath = configFilePath
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[config] %s not found, creating with defaults (version %d)", configPath, configVersion)
			fc := defaultFileConfig()
			saveToFile(configPath, fc)
			return fc
		}
		log.Printf("[config] failed to read %s: %v, using defaults", configPath, err)
		return defaultFileConfig()
	}

	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		log.Printf("[config] failed to parse %s: %v, using defaults", configPath, err)
		return defaultFileConfig()
	}

	if fc.Version < configVersion {
		log.Printf("[config] migrating %s from version %d to %d", configPath, fc.Version, configVersion)
		data = migrateConfig(fc.Version, data)
		if err := yaml.Unmarshal(data, &fc); err != nil {
			log.Printf("[config] failed to parse migrated config %s: %v, using defaults", configPath, err)
			return defaultFileConfig()
		}
		saveToFile(configPath, fc)
	} else if fc.Version > configVersion {
		log.Printf("[config] %s has version %d, but expected <= %d; using defaults",
			configPath, fc.Version, configVersion)
		return defaultFileConfig()
	}

	return fc
}

// defaultFileConfig returns a fileConfig populated with defaults at the current version.
func defaultFileConfig() fileConfig {
	return fileConfig{
		Version:                  configVersion,
		GameListenAddr:           ":5123",
		HTTPListenAddr:           ":8080",
		DBType:                   "sqlite",
		DBPath:                   "data/cascade.db",
		HRPAuthServiceName:       "Cascade",
		HRPAuthRelayPrefix:       "/rusted-warfare",
		PresenceTTLSec:           120,
		RegisterTimeoutSec:       5,
		ReadTimeoutSec:           90,
		MaxConnections:           0,
		ChatRateLimit:            5,
		DefaultBackendID:         "",
		ProxyIssuer:              "cascade",
		ProxyKeyID:               "",
		HealthIntervalSec:        5,
		RoomSnapshotIntervalSec:  5,
		PendingSessionTimeoutSec: 5,
	}
}

// saveToFile writes the fileConfig to disk as YAML at the given path.
func saveToFile(path string, fc fileConfig) {
	data, err := yaml.Marshal(fc)
	if err != nil {
		log.Printf("[config] failed to marshal config: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("[config] failed to write %s: %v", path, err)
	}
}

// migrateConfig upgrades config data from oldVersion to the next version.
// Add new cases as versions are incremented.
func migrateConfig(oldVersion int, data []byte) []byte {
	switch oldVersion {
	case 0:
		log.Printf("[config] migrating from v0 to v2")
		var m map[string]any
		if err := yaml.Unmarshal(data, &m); err != nil {
			log.Printf("[config] migration unmarshal error: %v", err)
			return data
		}
		m["version"] = 2
		if _, ok := m["register_timeout_sec"]; !ok {
			m["register_timeout_sec"] = 5
		}
		m["backends"] = []any{}
		m["health_interval_sec"] = 5
		m["room_snapshot_interval_sec"] = 5
		m["pending_session_timeout_sec"] = 5
		m["proxy_issuer"] = "cascade"
		out, err := yaml.Marshal(m)
		if err != nil {
			log.Printf("[config] migration marshal error: %v", err)
			return data
		}
		return out
	case 1:
		var m map[string]any
		if err := yaml.Unmarshal(data, &m); err != nil {
			log.Printf("[config] migration unmarshal error: %v", err)
			return data
		}
		m["version"] = 2
		if _, ok := m["backends"]; !ok {
			m["backends"] = []any{}
		}
		if _, ok := m["health_interval_sec"]; !ok {
			m["health_interval_sec"] = 5
		}
		if _, ok := m["room_snapshot_interval_sec"]; !ok {
			m["room_snapshot_interval_sec"] = 5
		}
		if _, ok := m["pending_session_timeout_sec"]; !ok {
			m["pending_session_timeout_sec"] = 5
		}
		if _, ok := m["proxy_issuer"]; !ok {
			m["proxy_issuer"] = "cascade"
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
		GameListenAddr:           fc.GameListenAddr,
		HTTPListenAddr:           fc.HTTPListenAddr,
		PublicGameHost:           fc.PublicGameHost,
		DBType:                   fc.DBType,
		DBPath:                   fc.DBPath,
		MySQLDSN:                 fc.MySQLDSN,
		RedisAddr:                fc.RedisAddr,
		RedisPassword:            fc.RedisPassword,
		RedisDB:                  fc.RedisDB,
		RedisPrefix:              fc.RedisPrefix,
		HRPAuthURL:               fc.HRPAuthURL,
		HRPAuthClientID:          fc.HRPAuthClientID,
		HRPAuthClientSecret:      fc.HRPAuthClientSecret,
		HRPAuthServiceName:       fc.HRPAuthServiceName,
		HRPAuthRelayPrefix:       fc.HRPAuthRelayPrefix,
		PresenceTTLSec:           fc.PresenceTTLSec,
		RegisterTimeoutSec:       fc.RegisterTimeoutSec,
		ReadTimeoutSec:           fc.ReadTimeoutSec,
		MaxConnections:           fc.MaxConnections,
		ChatRateLimit:            fc.ChatRateLimit,
		DefaultBackendID:         fc.DefaultBackendID,
		Backends:                 fc.Backends,
		ProxyIssuer:              fc.ProxyIssuer,
		ProxyKeyID:               fc.ProxyKeyID,
		ProxyPrivateKeyPath:      fc.ProxyPrivateKeyPath,
		BackendClientCert:        fc.BackendClientCert,
		BackendClientKey:         fc.BackendClientKey,
		BackendCAPool:            fc.BackendCAPool,
		HealthIntervalSec:        fc.HealthIntervalSec,
		RoomSnapshotIntervalSec:  fc.RoomSnapshotIntervalSec,
		PendingSessionTimeoutSec: fc.PendingSessionTimeoutSec,
	}
	// Apply defaults for zero-valued string fields
	if cfg.GameListenAddr == "" {
		cfg.GameListenAddr = d.GameListenAddr
	}
	if cfg.HTTPListenAddr == "" {
		cfg.HTTPListenAddr = d.HTTPListenAddr
	}
	if cfg.DBPath == "" {
		cfg.DBPath = d.DBPath
	}
	if cfg.DBType == "" {
		cfg.DBType = d.DBType
	}
	// Apply defaults for zero-valued int fields
	if cfg.RegisterTimeoutSec == 0 {
		cfg.RegisterTimeoutSec = d.RegisterTimeoutSec
	}
	if cfg.ReadTimeoutSec == 0 {
		cfg.ReadTimeoutSec = d.ReadTimeoutSec
	}
	if cfg.ChatRateLimit == 0 {
		cfg.ChatRateLimit = d.ChatRateLimit
	}
	if cfg.PresenceTTLSec == 0 {
		cfg.PresenceTTLSec = d.PresenceTTLSec
	}
	if cfg.ProxyIssuer == "" {
		cfg.ProxyIssuer = d.ProxyIssuer
	}
	if cfg.HealthIntervalSec == 0 {
		cfg.HealthIntervalSec = d.HealthIntervalSec
	}
	if cfg.RoomSnapshotIntervalSec == 0 {
		cfg.RoomSnapshotIntervalSec = d.RoomSnapshotIntervalSec
	}
	if cfg.PendingSessionTimeoutSec == 0 {
		cfg.PendingSessionTimeoutSec = d.PendingSessionTimeoutSec
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
	if v, ok := lookupEnv("RWGIN_PUBLIC_GAME_HOST"); ok {
		cfg.PublicGameHost = v
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
	if v, ok := lookupEnv("RWGIN_HRPAUTH_RELAY_PREFIX"); ok {
		cfg.HRPAuthRelayPrefix = v
	}
	if v, ok := lookupEnvInt("RWGIN_PRESENCE_TTL_SEC"); ok {
		cfg.PresenceTTLSec = v
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
	if v, ok := lookupEnv("RWGIN_DEFAULT_BACKEND_ID"); ok {
		cfg.DefaultBackendID = v
	}
	if v, ok := lookupEnv("RWGIN_PROXY_KEY_ID"); ok {
		cfg.ProxyKeyID = v
	}
	if v, ok := lookupEnv("RWGIN_PROXY_PRIVATE_KEY"); ok {
		cfg.ProxyPrivateKeyPath = v
	}
	if v, ok := lookupEnv("RWGIN_BACKEND_CLIENT_CERT"); ok {
		cfg.BackendClientCert = v
	}
	if v, ok := lookupEnv("RWGIN_BACKEND_CLIENT_KEY"); ok {
		cfg.BackendClientKey = v
	}
	if v, ok := lookupEnv("RWGIN_BACKEND_CA"); ok {
		cfg.BackendCAPool = v
	}
	if v, ok := lookupEnv("RWGIN_PROXY_ISSUER"); ok {
		cfg.ProxyIssuer = v
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
