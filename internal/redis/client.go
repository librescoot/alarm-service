package redis

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"
)

// Client wraps redis.Client with additional functionality
type Client struct {
	rdb *redis.Client
	log *slog.Logger
}

// NewClient creates a new Redis client
func NewClient(addr string, log *slog.Logger) *Client {
	rdb := redis.NewClient(&redis.Options{
		Addr: addr,
		DB:   0,
	})

	return &Client{
		rdb: rdb,
		log: log,
	}
}

// Connect tests the connection to Redis
func (c *Client) Connect(ctx context.Context) error {
	if err := c.rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}
	c.log.Info("connected to Redis")
	return nil
}

// Close closes the Redis connection
func (c *Client) Close() error {
	return c.rdb.Close()
}

// HSet sets a field in a hash
func (c *Client) HSet(ctx context.Context, key string, field string, value interface{}) error {
	return c.rdb.HSet(ctx, key, field, value).Err()
}

// HGet gets a field from a hash
func (c *Client) HGet(ctx context.Context, key string, field string) (string, error) {
	return c.rdb.HGet(ctx, key, field).Result()
}

// Publish publishes a message to a channel
func (c *Client) Publish(ctx context.Context, channel string, message string) error {
	return c.rdb.Publish(ctx, channel, message).Err()
}

// Subscribe subscribes to a channel
func (c *Client) Subscribe(ctx context.Context, channel string) *redis.PubSub {
	return c.rdb.Subscribe(ctx, channel)
}

// GetRedisClient returns the underlying redis client
func (c *Client) GetRedisClient() *redis.Client {
	return c.rdb
}