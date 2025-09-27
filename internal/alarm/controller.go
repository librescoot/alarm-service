package alarm

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Controller manages alarm activation (horn + hazard lights)
type Controller struct {
	redis  *redis.Client
	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger
	mu     sync.Mutex
	active bool
}

// NewController creates a new alarm controller
func NewController(redisAddr string, log *slog.Logger) (*Controller, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr: redisAddr,
		DB:   0,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}

	return &Controller{
		redis:  rdb,
		ctx:    ctx,
		log:    log,
		active: false,
	}, nil
}

// Close closes the controller
func (c *Controller) Close() error {
	c.Stop()
	return c.redis.Close()
}

// Start starts the alarm for the specified duration
func (c *Controller) Start(duration time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.active {
		c.log.Warn("alarm already active, stopping previous alarm")
		c.stopUnsafe()
	}

	c.log.Info("starting alarm", "duration", duration)

	ctx, cancel := context.WithCancel(c.ctx)
	c.cancel = cancel
	c.active = true

	if err := c.redis.LPush(ctx, "scooter:blinker", "both").Err(); err != nil {
		c.log.Error("failed to activate hazard lights", "error", err)
	}

	c.redis.HSet(ctx, "alarm", "alarm-active", "true")
	c.redis.Publish(ctx, "alarm", "alarm-active")

	go c.runHornPattern(ctx, duration)

	return nil
}

// Stop stops the alarm immediately
func (c *Controller) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopUnsafe()
}

// stopUnsafe stops the alarm without locking (internal use)
func (c *Controller) stopUnsafe() error {
	if !c.active {
		return nil
	}

	c.log.Info("stopping alarm")

	if c.cancel != nil {
		c.cancel()
	}

	ctx := context.Background()
	// Horn disabled for testing
	// c.redis.LPush(ctx, "scooter:horn", "off")
	c.redis.LPush(ctx, "scooter:blinker", "off")

	c.redis.HSet(ctx, "alarm", "alarm-active", "false")
	c.redis.Publish(ctx, "alarm", "alarm-active")

	c.active = false
	return nil
}

// runHornPattern runs the horn on/off pattern
func (c *Controller) runHornPattern(ctx context.Context, duration time.Duration) {
	c.log.Info("starting horn pattern", "duration", duration)

	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()

	timeout := time.After(duration)
	hornOn := true

	for {
		select {
		case <-ctx.Done():
			c.log.Info("horn pattern cancelled")
			return

		case <-timeout:
			c.log.Info("alarm duration expired")
			c.Stop()
			return

		case <-ticker.C:
			// Horn disabled for testing - only hazard lights active
			// if hornOn {
			// 	c.redis.LPush(ctx, "scooter:horn", "on")
			// } else {
			// 	c.redis.LPush(ctx, "scooter:horn", "off")
			// }
			hornOn = !hornOn
		}
	}
}

// ListenForCommands listens for alarm commands on scooter:alarm
func (c *Controller) ListenForCommands(ctx context.Context) {
	c.log.Info("starting alarm command listener")

	for {
		select {
		case <-ctx.Done():
			return

		default:
			result, err := c.redis.BRPop(ctx, 5*time.Second, "scooter:alarm").Result()
			if err != nil {
				if err == redis.Nil || err == context.Canceled {
					continue
				}
				c.log.Error("error reading from scooter:alarm", "error", err)
				continue
			}

			if len(result) >= 2 {
				command := result[1]
				c.log.Info("received alarm command", "command", command)
				c.handleCommand(command)
			}
		}
	}
}

// handleCommand handles a command string
func (c *Controller) handleCommand(cmd string) {
	if cmd == "stop" {
		c.Stop()
		return
	}

	var duration int
	_, err := fmt.Sscanf(cmd, "start:%d", &duration)
	if err != nil {
		c.log.Error("invalid alarm command", "command", cmd, "error", err)
		return
	}

	c.Start(time.Duration(duration) * time.Second)
}