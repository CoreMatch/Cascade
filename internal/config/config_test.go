package config

import (
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestMigrateV1ToV2PreservesExistingSettings(t *testing.T) {
	input := []byte("version: 1\ngame_listen_addr: ':6000'\ndb_path: data/players.db\nmax_connections: 200\n")
	output := migrateConfig(1, input)
	var migrated fileConfig
	if err := yaml.Unmarshal(output, &migrated); err != nil {
		t.Fatalf("unmarshal migrated config: %v", err)
	}
	if migrated.Version != 2 {
		t.Fatalf("version = %d, want 2", migrated.Version)
	}
	if migrated.GameListenAddr != ":6000" || migrated.DBPath != "data/players.db" {
		t.Fatalf("migration lost existing settings: %+v", migrated)
	}
	if migrated.MaxConnections != 200 {
		t.Fatalf("max_connections = %d, want preserved 200", migrated.MaxConnections)
	}
	if migrated.HealthIntervalSec != 5 || migrated.PendingSessionTimeoutSec != 5 {
		t.Fatalf("v2 proxy defaults missing: %+v", migrated)
	}
}

func TestConfigDefaultsDoNotInventBackend(t *testing.T) {
	cfg := fileConfigToConfig(defaultFileConfig())
	if cfg.DefaultBackendID != "" || len(cfg.Backends) != 0 {
		t.Fatalf("default config should require explicit backend configuration: %+v", cfg)
	}
	if cfg.MaxConnections != 0 {
		t.Fatalf("max connections = %d, want no default limit", cfg.MaxConnections)
	}
	if strings.TrimSpace(cfg.ProxyIssuer) == "" {
		t.Fatal("proxy issuer default must be set")
	}
}
