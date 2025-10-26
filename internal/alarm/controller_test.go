package alarm

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func TestController_BlinkHazards(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping test")
	}

	defer rdb.FlushDB(ctx)
	defer rdb.Close()

	c := &Controller{
		redis: rdb,
		ctx:   ctx,
		log:   log,
	}

	start := time.Now()
	err := c.BlinkHazards()
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("BlinkHazards failed: %v", err)
	}

	if duration < 800*time.Millisecond {
		t.Errorf("BlinkHazards should take at least 800ms, took %v", duration)
	}

	blinkerCmds := rdb.LRange(ctx, "scooter:blinker", 0, -1).Val()
	if len(blinkerCmds) < 2 {
		t.Error("expected at least 2 blinker commands (on and off)")
	}

	if blinkerCmds[len(blinkerCmds)-1] != "both" {
		t.Error("expected first command to be 'both'")
	}
	if blinkerCmds[0] != "off" {
		t.Error("expected last command to be 'off'")
	}
}

func TestController_HornPattern(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping test")
	}

	defer rdb.FlushDB(ctx)
	defer rdb.Close()

	c := &Controller{
		redis:       rdb,
		ctx:         ctx,
		log:         log,
		active:      false,
		hornEnabled: true,
	}

	c.Start(1 * time.Second)

	time.Sleep(1500 * time.Millisecond)

	hornCmds := rdb.LRange(ctx, "scooter:horn", 0, -1).Val()

	if len(hornCmds) < 2 {
		t.Errorf("expected multiple horn commands, got %d", len(hornCmds))
	}

	hasOn := false
	hasOff := false
	for _, cmd := range hornCmds {
		if cmd == "on" {
			hasOn = true
		}
		if cmd == "off" {
			hasOff = true
		}
	}

	if !hasOn {
		t.Error("expected horn 'on' command")
	}
	if !hasOff {
		t.Error("expected horn 'off' command")
	}
}

func TestController_HornDisabled(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping test")
	}

	defer rdb.FlushDB(ctx)
	rdb.FlushDB(ctx)
	defer rdb.Close()

	c := &Controller{
		redis:       rdb,
		ctx:         ctx,
		log:         log,
		active:      false,
		hornEnabled: false,
	}

	c.Start(800 * time.Millisecond)

	time.Sleep(1200 * time.Millisecond)

	hornCmds := rdb.LRange(ctx, "scooter:horn", 0, -1).Val()

	if len(hornCmds) > 0 {
		t.Errorf("expected no horn commands when disabled, got %d commands", len(hornCmds))
	}

	blinkerCmds := rdb.LRange(ctx, "scooter:blinker", 0, -1).Val()
	if len(blinkerCmds) < 2 {
		t.Error("expected blinker commands even when horn is disabled")
	}
}

func TestController_HandleCommand_StartWithDuration(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
		DB:   15,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skip("Redis not available, skipping test")
	}

	defer rdb.FlushDB(ctx)
	defer rdb.Close()

	c := &Controller{
		redis:       rdb,
		ctx:         ctx,
		log:         log,
		active:      false,
		hornEnabled: true,
	}

	c.handleCommand("start:15")

	time.Sleep(100 * time.Millisecond)

	if !c.active {
		t.Error("expected alarm to be active after start command")
	}

	c.Stop()
}
