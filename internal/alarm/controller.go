package alarm

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	ipc "github.com/librescoot/redis-ipc"
)

// Controller manages alarm activation (horn + hazard lights)
type Controller struct {
	ipc         *ipc.Client
	alarmPub    *ipc.HashPublisher
	settingsPub *ipc.HashPublisher
	cmdHandler  *ipc.QueueHandler[string]
	ctx         context.Context
	cancel      context.CancelFunc
	log         *slog.Logger
	mu          sync.Mutex
	active      bool
	hornEnabled bool
}

// NewController creates a new alarm controller using redis-ipc
func NewController(redisAddr string, hornEnabled bool, log *slog.Logger) (*Controller, error) {
	client, err := ipc.New(
		ipc.WithURL(redisAddr),
		ipc.WithCodec(ipc.StringCodec{}),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create redis-ipc client: %w", err)
	}

	ctx := context.Background()

	c := &Controller{
		ipc:         client,
		alarmPub:    client.NewHashPublisher("alarm"),
		settingsPub: client.NewHashPublisher("settings"),
		ctx:         ctx,
		log:         log,
		active:      false,
		hornEnabled: hornEnabled,
	}

	c.cmdHandler = ipc.HandleRequests(client, "scooter:alarm", func(cmd string) error {
		c.log.Info("received alarm command", "command", cmd)
		c.handleCommand(cmd)
		return nil
	})

	return c, nil
}

// Close closes the controller
func (c *Controller) Close() error {
	c.Stop()
	if c.cmdHandler != nil {
		c.cmdHandler.Stop()
	}
	return c.ipc.Close()
}

// SetHornEnabled updates the horn enabled setting
func (c *Controller) SetHornEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hornEnabled = enabled
	c.log.Info("horn setting updated", "enabled", enabled)
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

	if _, err := c.ipc.LPush("scooter:blinker", "both"); err != nil {
		c.log.Error("failed to activate hazard lights", "error", err)
	}

	c.alarmPub.Set("alarm-active", "true")

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

	if c.hornEnabled {
		c.ipc.LPush("scooter:horn", "off")
	}
	c.ipc.LPush("scooter:blinker", "off")

	c.alarmPub.Set("alarm-active", "false")

	c.active = false
	return nil
}

// runHornPattern runs the horn on/off pattern with integral cycles.
// Each cycle is 800ms (400ms on + 400ms off). The pattern runs for
// the number of complete cycles that fit within the given duration.
func (c *Controller) runHornPattern(ctx context.Context, duration time.Duration) {
	const cycleDuration = 800 * time.Millisecond
	const buffer = 200 * time.Millisecond
	cycles := int((duration - buffer) / cycleDuration)
	if cycles < 1 {
		cycles = 1
	}
	actualDuration := time.Duration(cycles) * cycleDuration

	c.log.Info("starting horn pattern", "duration", duration, "cycles", cycles, "actual_duration", actualDuration)

	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()

	ticks := 0
	totalTicks := cycles * 2 // 2 ticks per cycle (on + off)

	for {
		select {
		case <-ctx.Done():
			c.log.Info("horn pattern cancelled")
			return

		case <-ticker.C:
			if c.hornEnabled {
				if ticks%2 == 0 {
					_, _ = c.ipc.LPush("scooter:horn", "on")
				} else {
					_, _ = c.ipc.LPush("scooter:horn", "off")
				}
			}
			ticks++
			if ticks >= totalTicks {
				c.log.Info("alarm duration expired", "cycles", cycles)
				c.Stop()
				return
			}
		}
	}
}

// BlinkHazards briefly flashes the hazard lights once (one blink cycle = 800ms)
func (c *Controller) BlinkHazards() error {
	c.log.Info("blinking hazards")

	if _, err := c.ipc.LPush("scooter:blinker", "both"); err != nil {
		c.log.Error("failed to activate hazard lights", "error", err)
		return err
	}

	time.Sleep(800 * time.Millisecond)

	if _, err := c.ipc.LPush("scooter:blinker", "off"); err != nil {
		c.log.Error("failed to deactivate hazard lights", "error", err)
		return err
	}

	return nil
}

// handleCommand handles a command string
func (c *Controller) handleCommand(cmd string) {
	switch cmd {
	case "stop":
		c.Stop()
		return
	case "enable":
		c.settingsPub.Set("alarm.enabled", "true")
		c.log.Info("alarm enabled via command")
		return
	case "disable":
		c.settingsPub.Set("alarm.enabled", "false")
		c.log.Info("alarm disabled via command")
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
