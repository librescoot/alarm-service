package alarm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	ipc "github.com/librescoot/redis-ipc"
)

type RuntimeCommander interface {
	RuntimeArm()
	RuntimeStop()
	RuntimeDisarm()
}

type Controller struct {
	ipc         *ipc.Client
	alarmPub    *ipc.HashPublisher
	settingsPub *ipc.HashPublisher
	cmdHandler  *ipc.QueueHandler[string]
	commander   RuntimeCommander
	ctx         context.Context
	cancel      context.CancelFunc
	hornDone    chan struct{}
	log         *slog.Logger
	mu          sync.Mutex
	active      bool
	hornMu      sync.Mutex
	hornEnabled atomic.Bool

	pendingHornOff    bool
	pendingBlinkerOff bool
	pendingInactive   bool
	retrying          bool
	closed            bool
	retryStop         chan struct{}
	pushOutput        func(string, string) error

	// Exactly one hazard pattern may write at once; cancellation waits so the
	// next command is final-write-wins rather than visible flicker.
	blinkerCancel context.CancelFunc
	blinkerDone   chan struct{}
}

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
		retryStop:   make(chan struct{}),
	}
	c.pushOutput = func(queue, value string) error {
		_, err := c.ipc.LPush(queue, value)
		return err
	}
	c.hornEnabled.Store(hornEnabled)

	c.cmdHandler = ipc.HandleRequests(client, "scooter:alarm", func(cmd string) error {
		c.log.Info("received alarm command", "command", cmd)
		c.handleCommand(cmd)
		return nil
	})

	return c, nil
}

func (c *Controller) Close() error {
	if err := c.Stop(); err != nil {
		c.log.Error("failed to stop alarm during close", "error", err)
	}
	if c.cmdHandler != nil {
		c.cmdHandler.Stop()
	}
	c.mu.Lock()
	c.closed = true
	if c.retryStop != nil {
		close(c.retryStop)
	}
	c.mu.Unlock()
	return c.ipc.Close()
}

func (c *Controller) SetCommander(commander RuntimeCommander) {
	c.commander = commander
}

func (c *Controller) SetHornEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hornMu.Lock()
	defer c.hornMu.Unlock()
	c.hornEnabled.Store(enabled)
	c.log.Info("horn setting updated", "enabled", enabled)
	if !enabled {
		c.pendingHornOff = true
		if err := c.flushStopLocked(); err != nil {
			c.log.Error("failed to silence horn", "error", err)
		}
	} else {
		c.pendingHornOff = false
	}
}

func (c *Controller) Start(duration time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.active {
		c.log.Warn("alarm already active, stopping previous alarm")
		if err := c.stopUnsafe(); err != nil {
			c.log.Error("failed to stop previous alarm", "error", err)
		}
	}

	if c.pendingHornOff || c.pendingBlinkerOff || c.pendingInactive {
		if err := c.flushStopLocked(); err != nil {
			return fmt.Errorf("previous alarm has not stopped: %w", err)
		}
	}

	c.log.Info("starting alarm", "duration", duration)

	c.cancelBlinkerLocked()

	ctx, cancel := context.WithCancel(c.ctx)
	c.cancel = cancel
	c.active = true

	if _, err := c.ipc.LPush("scooter:blinker", "both"); err != nil {
		c.log.Error("failed to activate hazard lights", "error", err)
	}

	if err := c.alarmPub.Set("alarm-active", "true"); err != nil {
		c.log.Error("failed to publish alarm-active", "error", err)
	}

	done := make(chan struct{})
	c.hornDone = done
	go func() {
		expired := c.runHornPattern(ctx, duration)
		close(done)
		if expired {
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.hornDone == done {
				if err := c.stopUnsafe(); err != nil {
					c.log.Error("failed to stop alarm after duration expired", "error", err)
				}
			}
		}
	}()

	return nil
}

func (c *Controller) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopUnsafe()
}

func (c *Controller) stopUnsafe() error {
	cueInterrupted := c.cancelBlinkerLocked()
	if cueInterrupted {
		c.pendingBlinkerOff = true
	}
	if c.active {
		c.log.Info("stopping alarm")
		if c.cancel != nil {
			c.cancel()
		}
		if c.hornDone != nil {
			<-c.hornDone
			c.hornDone = nil
		}
		c.pendingHornOff = true
		c.pendingBlinkerOff = true
		c.pendingInactive = true
		c.active = false
	}
	return c.flushStopLocked()
}

// Pending stop commands stay ordered ahead of any subsequent alarm start.
func (c *Controller) flushStopLocked() error {
	var result error
	if c.pendingHornOff {
		if err := c.pushOutput("scooter:horn", "off"); err != nil {
			result = errors.Join(result, fmt.Errorf("turn off horn: %w", err))
		} else {
			c.pendingHornOff = false
		}
	}
	if c.pendingBlinkerOff {
		if err := c.pushOutput("scooter:blinker", "off"); err != nil {
			result = errors.Join(result, fmt.Errorf("turn off blinker: %w", err))
		} else {
			c.pendingBlinkerOff = false
		}
	}
	if c.pendingInactive {
		if err := c.alarmPub.Set("alarm-active", "false"); err != nil {
			result = errors.Join(result, fmt.Errorf("publish alarm inactive: %w", err))
		} else {
			c.pendingInactive = false
		}
	}
	if result != nil && !c.retrying && !c.closed {
		c.retrying = true
		go c.retryStopCommands()
	}
	return result
}

func (c *Controller) retryStopCommands() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.retryStop:
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.closed {
				c.mu.Unlock()
				return
			}
			err := c.flushStopLocked()
			if err == nil {
				c.retrying = false
				c.mu.Unlock()
				return
			}
			c.log.Warn("retrying alarm stop", "error", err)
			c.mu.Unlock()
		}
	}
}

// cancelBlinkerLocked waits while holding mu; the pattern does not need mu,
// preserving a coherent cancellation state for concurrent callers.
func (c *Controller) cancelBlinkerLocked() bool {
	if c.blinkerCancel == nil {
		return false
	}
	done := c.blinkerDone
	interrupted := true
	select {
	case <-done:
		interrupted = false
	default:
	}
	c.blinkerCancel()
	c.blinkerCancel = nil
	c.blinkerDone = nil
	<-done
	return interrupted
}

func (c *Controller) runHornPattern(ctx context.Context, duration time.Duration) bool {
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
	totalTicks := cycles * 2

	for {
		select {
		case <-ctx.Done():
			c.log.Info("horn pattern cancelled")
			return false

		case <-ticker.C:
			c.hornMu.Lock()
			if c.hornEnabled.Load() {
				if ticks%2 == 0 {
					_, _ = c.ipc.LPush("scooter:horn", "on")
				} else {
					_, _ = c.ipc.LPush("scooter:horn", "off")
				}
			}
			c.hornMu.Unlock()
			ticks++
			if ticks >= totalTicks {
				c.log.Info("alarm duration expired", "cycles", cycles)
				return true
			}
		}
	}
}

// BlinkHazards is asynchronous, latest-wins, and never overlays active alarm hazards.
func (c *Controller) BlinkHazards() error {
	c.mu.Lock()
	if c.active {
		c.mu.Unlock()
		c.log.Debug("blink hazards skipped: alarm already active")
		return nil
	}

	if c.pendingHornOff || c.pendingBlinkerOff || c.pendingInactive {
		if err := c.flushStopLocked(); err != nil {
			c.mu.Unlock()
			return fmt.Errorf("previous alarm has not stopped: %w", err)
		}
	}

	c.log.Info("blinking hazards")

	c.cancelBlinkerLocked()

	ctx, cancel := context.WithCancel(c.ctx)
	done := make(chan struct{})
	c.blinkerCancel = cancel
	c.blinkerDone = done

	go func() {
		defer close(done)

		wait := func(d time.Duration) bool {
			select {
			case <-time.After(d):
				return true
			case <-ctx.Done():
				return false
			}
		}
		push := func(value string) {
			if _, err := c.ipc.LPush("scooter:blinker", value); err != nil {
				c.log.Error("blink hazards LPush failed", "value", value, "error", err)
			}
		}

		push("both")
		for i := 0; i < 2; i++ {
			if !wait(600 * time.Millisecond) {
				return
			}
			push("off")
			if !wait(400 * time.Millisecond) {
				return
			}
			push("both")
		}
		if !wait(600 * time.Millisecond) {
			return
		}
		push("off")
	}()

	c.mu.Unlock()
	return nil
}

func (c *Controller) handleCommand(cmd string) {
	switch cmd {
	case "stop":
		if err := c.Stop(); err != nil {
			c.log.Error("failed to stop alarm", "error", err)
		}
		if c.commander != nil {
			c.commander.RuntimeStop()
			c.log.Info("runtime alarm stop requested")
		}
		return
	case "enable":
		if err := c.settingsPub.Set("alarm.enabled", "true"); err != nil {
			c.log.Error("failed to enable alarm", "error", err)
		}
		c.log.Info("alarm enabled via command")
		return
	case "disable":
		if err := c.Stop(); err != nil {
			c.log.Error("failed to stop alarm", "error", err)
		}
		if err := c.settingsPub.Set("alarm.enabled", "false"); err != nil {
			c.log.Error("failed to disable alarm", "error", err)
		}
		c.log.Info("alarm disabled via command")
		return
	case "arm":
		if c.commander != nil {
			c.commander.RuntimeArm()
			c.log.Info("runtime arm requested")
		}
		return
	case "disarm":
		if err := c.Stop(); err != nil {
			c.log.Error("failed to stop alarm", "error", err)
		}
		if c.commander != nil {
			c.commander.RuntimeDisarm()
			c.log.Info("runtime disarm requested")
		}
		return
	}

	var duration int
	_, err := fmt.Sscanf(cmd, "start:%d", &duration)
	if err != nil {
		c.log.Error("invalid alarm command", "command", cmd, "error", err)
		return
	}

	if err := c.Start(time.Duration(duration) * time.Second); err != nil {
		c.log.Error("failed to start alarm", "error", err)
	}
}
