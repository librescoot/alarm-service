package alarm

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	ipc "github.com/librescoot/redis-ipc"
)

// RuntimeCommander handles runtime arm/disarm commands that bypass the settings hash.
type RuntimeCommander interface {
	RuntimeArm()
	RuntimeDisarm()
}

// Controller manages alarm activation (horn + hazard lights).
//
// The controller serializes all writes to scooter:blinker behind a single
// cancelable pattern goroutine. BlinkHazards (the L1 warning flash) and Start
// (the full alarm) never run concurrently — a later request supersedes the
// earlier one, with the prior pattern canceled before the new one writes.
// Without this, the BlinkHazards goroutine kept toggling both/off while the
// alarm was driving hazards solid-on, surfacing as visible flicker.
type Controller struct {
	ipc         *ipc.Client
	alarmPub    *ipc.HashPublisher
	settingsPub *ipc.HashPublisher
	cmdHandler  *ipc.QueueHandler[string]
	commander   RuntimeCommander
	ctx         context.Context
	cancel      context.CancelFunc
	log         *slog.Logger
	mu          sync.Mutex
	active      bool
	hornEnabled atomic.Bool

	// blinkerCancel cancels the active blinker pattern goroutine (BlinkHazards
	// or the alarm's hazard hold). blinkerDone closes when that goroutine exits.
	// Both are nil when no pattern is running. Guarded by mu.
	blinkerCancel context.CancelFunc
	blinkerDone   chan struct{}
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
	}
	c.hornEnabled.Store(hornEnabled)

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

// SetCommander sets the RuntimeCommander used to forward arm/disarm commands to the FSM.
func (c *Controller) SetCommander(commander RuntimeCommander) {
	c.commander = commander
}

// SetHornEnabled updates the horn enabled setting
func (c *Controller) SetHornEnabled(enabled bool) {
	c.hornEnabled.Store(enabled)
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

	// Cancel any in-progress BlinkHazards so its toggles don't fight the
	// alarm's solid-on hazards. cancelBlinkerLocked also waits for the prior
	// goroutine to exit, so any trailing "off" it might write lands before
	// we LPush "both" below — preserving final-write-wins semantics.
	c.cancelBlinkerLocked()

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
	// Always cancel any in-progress BlinkHazards even when the alarm wasn't
	// active — Stop() is the FSM's universal "quiet down" hook on state exit,
	// and a stale BlinkHazards goroutine would otherwise keep writing.
	c.cancelBlinkerLocked()

	if !c.active {
		return nil
	}

	c.log.Info("stopping alarm")

	if c.cancel != nil {
		c.cancel()
	}

	if c.hornEnabled.Load() {
		c.ipc.LPush("scooter:horn", "off")
	}
	c.ipc.LPush("scooter:blinker", "off")

	c.alarmPub.Set("alarm-active", "false")

	c.active = false
	return nil
}

// cancelBlinkerLocked cancels any in-progress blinker pattern goroutine and
// waits for it to exit. Must be called with c.mu held. The lock is kept
// across the wait — the pattern goroutine does not touch any mu-guarded
// state, so holding it is safe and keeps the controller's view of
// blinkerCancel/blinkerDone consistent for concurrent callers (handleCommand
// runs on a separate goroutine from the FSM). After return, no blinker
// pattern goroutine is running, so the next LPush from the caller is the
// final write seen by vehicle-service.
func (c *Controller) cancelBlinkerLocked() {
	if c.blinkerCancel == nil {
		return
	}
	c.blinkerCancel()
	done := c.blinkerDone
	c.blinkerCancel = nil
	c.blinkerDone = nil
	<-done
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
			if c.hornEnabled.Load() {
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

// BlinkHazards flashes the hazard lights 3 times as an L1 warning.
// Each cycle: 600ms on (fade completes at 504ms) + 400ms off.
// This function is non-blocking to avoid stalling the FSM event loop.
//
// If the alarm is already active, the flash is skipped — Start has already
// driven hazards solid-on and a warning pattern on top would only flicker.
// If a previous BlinkHazards is still running, it is canceled before the new
// one starts (latest-wins). The pattern goroutine is cooperatively canceled
// via the controller's blinker context, so Start/Stop can supersede it
// cleanly.
func (c *Controller) BlinkHazards() error {
	c.mu.Lock()
	if c.active {
		c.mu.Unlock()
		c.log.Debug("blink hazards skipped: alarm already active")
		return nil
	}

	c.log.Info("blinking hazards")

	// Cancel any previous BlinkHazards still in flight.
	c.cancelBlinkerLocked()

	ctx, cancel := context.WithCancel(c.ctx)
	done := make(chan struct{})
	c.blinkerCancel = cancel
	c.blinkerDone = done

	// Start the goroutine before releasing the lock so a concurrent
	// cancelBlinkerLocked never blocks waiting on a goroutine that hasn't
	// been spawned yet.
	go func() {
		defer close(done)
		// Cooperative sleep: returns false if canceled mid-wait so the
		// goroutine exits before issuing the next LPush.
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
	case "arm":
		if c.commander != nil {
			c.commander.RuntimeArm()
			c.log.Info("runtime arm requested")
		}
		return
	case "disarm":
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

	c.Start(time.Duration(duration) * time.Second)
}
