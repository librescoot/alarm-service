package redis

import (
	"context"
	"fmt"
	"log/slog"

	ipc "github.com/librescoot/redis-ipc"
)

// Client wraps redis-ipc client
type Client struct {
	ipc *ipc.Client
	log *slog.Logger
}

// NewClient creates a new Redis client using redis-ipc
func NewClient(addr string, log *slog.Logger) (*Client, error) {
	client, err := ipc.New(
		ipc.WithURL(addr),
		ipc.WithCodec(ipc.StringCodec{}),
		ipc.WithOnDisconnect(func(err error) {
			if err != nil {
				log.Warn("Redis disconnected", "error", err)
			}
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create redis-ipc client: %w", err)
	}

	return &Client{
		ipc: client,
		log: log,
	}, nil
}

// Connect tests the connection to Redis
func (c *Client) Connect(ctx context.Context) error {
	if !c.ipc.Connected() {
		return fmt.Errorf("not connected to Redis")
	}
	c.log.Info("connected to Redis")
	return nil
}

// Close closes the Redis connection
func (c *Client) Close() error {
	return c.ipc.Close()
}

// IPC returns the underlying redis-ipc client for direct access
func (c *Client) IPC() *ipc.Client {
	return c.ipc
}
