package config

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
)

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
}

func Load() Config {
	return Config{
		GameListenAddr:       envOrDefault("RWGIN_GAME_ADDR", ":5123"),
		HTTPListenAddr:       envOrDefault("RWGIN_HTTP_ADDR", ":8080"),
		ServerID:             envOrDefault("RWGIN_SERVER_ID", "rwgin"),
		RelayHandshakeID:     envOrDefault("RWGIN_RELAY_ID", "net.rwhps.server"),
		RoomCodePrefix:       envOrDefault("RWGIN_ROOM_PREFIX", "GW"),
		DefaultMapName:       envOrDefault("RWGIN_DEFAULT_MAP", "unit_types_test.tmx"),
		DefaultRoomName:      envOrDefault("RWGIN_DEFAULT_ROOM", "Gin Room"),
		DefaultMaxPlayers:    envIntOrDefault("RWGIN_MAX_PLAYERS", 8),
		DefaultClientVersion: envIntOrDefault("RWGIN_CLIENT_VERSION", 151),
		DefaultIncome:        envFloat32OrDefault("RWGIN_INCOME", 1.0),
		DefaultMaxUnits:      envIntOrDefault("RWGIN_MAX_UNITS", 200),
		MapsDir:              envOrDefault("RWGIN_MAPS_DIR", "assets/maps"),
	}
}

func envOrDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func envIntOrDefault(key string, fallback int) int {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func envFloat32OrDefault(key string, fallback float32) float32 {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		if parsed, err := strconv.ParseFloat(value, 32); err == nil {
			return float32(parsed)
		}
	}
	return fallback
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "rwgin-relay"
	}
	return hex.EncodeToString(buf)
}
