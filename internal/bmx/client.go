package bmx

import (
	"context"
	"fmt"
	"log/slog"

	"alarm-service/internal/fsm"

	"github.com/redis/go-redis/v9"
)

// Client is a client for sending commands to bmx-service
type Client struct {
	redis *redis.Client
	log   *slog.Logger
}

// NewClient creates a new BMX client
func NewClient(redisAddr string, log *slog.Logger) (*Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr: redisAddr,
		DB:   0,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &Client{
		redis: rdb,
		log:   log,
	}, nil
}

// Close closes the client
func (c *Client) Close() error {
	return c.redis.Close()
}

// SetSensitivity sets the BMX sensitivity
func (c *Client) SetSensitivity(ctx context.Context, sens fsm.Sensitivity) error {
	cmd := fmt.Sprintf("sensitivity:%s", sens.String())
	c.log.Debug("sending BMX command", "command", cmd)
	return c.redis.LPush(ctx, "scooter:bmx", cmd).Err()
}

// SetInterruptPin sets the BMX interrupt pin
func (c *Client) SetInterruptPin(ctx context.Context, pin fsm.InterruptPin) error {
	cmd := fmt.Sprintf("pin:%s", pin.String())
	c.log.Debug("sending BMX command", "command", cmd)
	return c.redis.LPush(ctx, "scooter:bmx", cmd).Err()
}

// SoftReset performs a soft reset of BMX sensors
func (c *Client) SoftReset(ctx context.Context) error {
	c.log.Debug("sending BMX command", "command", "reset")
	return c.redis.LPush(ctx, "scooter:bmx", "reset").Err()
}

// EnableInterrupt enables BMX interrupt
func (c *Client) EnableInterrupt(ctx context.Context) error {
	c.log.Debug("sending BMX command", "command", "interrupt:enable")
	return c.redis.LPush(ctx, "scooter:bmx", "interrupt:enable").Err()
}

// DisableInterrupt disables BMX interrupt
func (c *Client) DisableInterrupt(ctx context.Context) error {
	c.log.Debug("sending BMX command", "command", "interrupt:disable")
	return c.redis.LPush(ctx, "scooter:bmx", "interrupt:disable").Err()
}