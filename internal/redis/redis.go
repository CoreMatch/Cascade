package redis

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// Client wraps the go-redis client with convenience methods.
type Client struct {
	rdb    *redis.Client
	prefix string
	logger *log.Logger
}

// New creates a new Redis client.
func New(addr, password string, db int, prefix string, logger *log.Logger) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}

	logger.Printf("[redis] connected to %s", addr)
	return &Client{rdb: rdb, prefix: prefix, logger: logger}, nil
}

// Close closes the Redis connection.
func (c *Client) Close() error {
	return c.rdb.Close()
}

// SetAuthenticated marks a game_uuid as authenticated in Redis with a TTL.
func (c *Client) SetAuthenticated(gameUUID, haUserUUID string, ttl time.Duration) error {
	ctx := context.Background()
	key := c.prefix + "auth:" + gameUUID
	return c.rdb.Set(ctx, key, haUserUUID, ttl).Err()
}

// IsAuthenticated checks if a game_uuid is authenticated in Redis.
// Returns the HA user UUID and true if authenticated, empty string and false otherwise.
func (c *Client) IsAuthenticated(gameUUID string) (string, bool) {
	ctx := context.Background()
	key := c.prefix + "auth:" + gameUUID
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return "", false
	}
	return val, true
}

// ClearAuthenticated removes the authentication state for a game_uuid.
func (c *Client) ClearAuthenticated(gameUUID string) error {
	ctx := context.Background()
	key := c.prefix + "auth:" + gameUUID
	return c.rdb.Del(ctx, key).Err()
}

// SetQuickAuth marks a game_uuid for quick reconnection auth with a short TTL.
func (c *Client) SetQuickAuth(gameUUID, haUserUUID string, ttl time.Duration) error {
	ctx := context.Background()
	key := c.prefix + "quick_auth:" + gameUUID
	return c.rdb.Set(ctx, key, haUserUUID, ttl).Err()
}

// CheckQuickAuth checks if a game_uuid has quick auth available.
// Returns the HA user UUID and true if quick auth is valid, empty string and false otherwise.
func (c *Client) CheckQuickAuth(gameUUID string) (string, bool) {
	ctx := context.Background()
	key := c.prefix + "quick_auth:" + gameUUID
	val, err := c.rdb.Get(ctx, key).Result()
	if err != nil {
		return "", false
	}
	return val, true
}