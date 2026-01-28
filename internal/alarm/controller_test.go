package alarm

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	ipc "github.com/librescoot/redis-ipc"
)

func setupTestController(t *testing.T, hornEnabled bool) (*Controller, *ipc.Client) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	client, err := ipc.New(
		ipc.WithAddress("localhost"),
		ipc.WithPort(6379),
		ipc.WithCodec(ipc.StringCodec{}),
	)
	if err != nil {
		t.Fatalf("Failed to create redis-ipc client: %v", err)
	}

	ctx := context.Background()
	if !client.Connected() {
		client.Close()
		t.Skip("Redis not available, skipping test")
	}

	c := &Controller{
		ipc:         client,
		alarmPub:    client.NewHashPublisher("alarm"),
		settingsPub: client.NewHashPublisher("settings"),
		ctx:         ctx,
		log:         log,
		active:      false,
	}
	c.hornEnabled.Store(hornEnabled)

	return c, client
}

func TestController_BlinkHazards(t *testing.T) {
	c, client := setupTestController(t, false)
	defer client.Close()

	start := time.Now()
	err := c.BlinkHazards()
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("BlinkHazards failed: %v", err)
	}

	// BlinkHazards is now async - it should return quickly
	if duration > 100*time.Millisecond {
		t.Errorf("BlinkHazards should return quickly (async), took %v", duration)
	}
}

func TestController_HornPattern(t *testing.T) {
	c, client := setupTestController(t, true)
	defer client.Close()

	c.Start(1 * time.Second)

	time.Sleep(1500 * time.Millisecond)

	if c.active {
		t.Error("expected alarm to be inactive after duration expired")
	}
}

func TestController_HornDisabled(t *testing.T) {
	c, client := setupTestController(t, false)
	defer client.Close()

	c.Start(800 * time.Millisecond)

	time.Sleep(1200 * time.Millisecond)

	if c.active {
		t.Error("expected alarm to be inactive after stop")
	}
}

func TestController_HandleCommand_StartWithDuration(t *testing.T) {
	c, client := setupTestController(t, true)
	defer client.Close()

	c.handleCommand("start:15")

	time.Sleep(100 * time.Millisecond)

	if !c.active {
		t.Error("expected alarm to be active after start command")
	}

	c.Stop()

	if c.active {
		t.Error("expected alarm to be inactive after stop")
	}
}
