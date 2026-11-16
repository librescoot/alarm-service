package alarm

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	ipc "github.com/librescoot/redis-ipc"
)

func setupTestController(t *testing.T, hornEnabled bool) (*Controller, *ipc.Client) {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	addr := os.Getenv("ALARM_TEST_REDIS_URL")
	if addr == "" {
		addr = "redis://localhost:6379"
	}
	client, err := ipc.New(
		ipc.WithURL(addr),
		ipc.WithCodec(ipc.StringCodec{}),
	)
	if err != nil {
		t.Fatalf("Failed to create redis-ipc client: %v", err)
	}

	ctx := context.Background()
	if !client.Connected() {
		_ = client.Close()
		t.Skip("Redis not available, skipping test")
	}

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
		_, err := client.LPush(queue, value)
		return err
	}
	c.hornEnabled.Store(hornEnabled)

	return c, client
}

func TestController_BlinkHazards(t *testing.T) {
	c, client := setupTestController(t, false)
	defer client.Close()
	defer c.Stop()

	start := time.Now()
	err := c.BlinkHazards()
	duration := time.Since(start)

	if err != nil {
		t.Fatalf("BlinkHazards failed: %v", err)
	}

	if duration > 100*time.Millisecond {
		t.Errorf("BlinkHazards should return quickly (async), took %v", duration)
	}
}

func TestController_StopCancelsHazardCue(t *testing.T) {
	c, client := setupTestController(t, false)
	defer client.Close()
	defer c.Stop()

	if err := c.BlinkHazards(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		last, err := client.Raw().LIndex(context.Background(), "scooter:blinker", 0).Result()
		if err == nil && last == "both" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hazard cue did not start")
		}
		time.Sleep(time.Millisecond)
	}
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	last, err := client.Raw().LIndex(context.Background(), "scooter:blinker", 0).Result()
	if err != nil || last != "off" {
		t.Fatalf("last blinker command = %q, error = %v; want off", last, err)
	}
}

func TestController_RetriesFailedStop(t *testing.T) {
	c, client := setupTestController(t, true)
	defer c.Close()
	if err := c.Start(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	fail.Store(true)
	c.pushOutput = func(queue, value string) error {
		if fail.Load() {
			return errors.New("redis unavailable")
		}
		_, err := client.LPush(queue, value)
		return err
	}
	if err := c.Stop(); err == nil {
		t.Fatal("stop must report failed output commands")
	}
	if err := c.Start(time.Second); err == nil {
		t.Fatal("new alarm must not overtake failed stop commands")
	}
	fail.Store(false)
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.mu.Lock()
		pending := c.pendingHornOff || c.pendingBlinkerOff || c.pendingInactive
		c.mu.Unlock()
		if !pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("failed stop commands were not retried")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, queue := range []string{"scooter:horn", "scooter:blinker"} {
		last, err := client.Raw().LIndex(context.Background(), queue, 0).Result()
		if err != nil || last != "off" {
			t.Fatalf("last command for %s = %q, error = %v; want off", queue, last, err)
		}
	}
}

func TestController_RetriesFailedCompletedWarningOff(t *testing.T) {
	c, client := setupTestController(t, false)
	defer c.Close()
	var offCalls atomic.Int32
	c.pushOutput = func(queue, value string) error {
		if queue == "scooter:blinker" && value == "off" && offCalls.Add(1) == 3 {
			return errors.New("redis unavailable")
		}
		_, err := client.LPush(queue, value)
		return err
	}
	if err := c.BlinkHazards(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for offCalls.Load() < 4 {
		if time.Now().After(deadline) {
			t.Fatal("failed final warning off was not retried")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.mu.Lock()
	c.mu.Unlock()
	last, err := client.Raw().LIndex(context.Background(), "scooter:blinker", 0).Result()
	if err != nil || last != "off" {
		t.Fatalf("last blinker command = %q, error = %v; want off", last, err)
	}
}

func TestController_HornPattern(t *testing.T) {
	c, client := setupTestController(t, true)
	defer client.Close()

	if err := c.Start(1 * time.Second); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(1500 * time.Millisecond)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active {
		t.Error("expected alarm to be inactive after duration expired")
	}
}

func TestController_HornDisabled(t *testing.T) {
	c, client := setupTestController(t, false)
	defer client.Close()

	if err := c.Start(800 * time.Millisecond); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(1200 * time.Millisecond)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active {
		t.Error("expected alarm to be inactive after stop")
	}
}

type fakeRuntimeCommander struct {
	stopped bool
}

func (f *fakeRuntimeCommander) RuntimeArm()    {}
func (f *fakeRuntimeCommander) RuntimeStop()   { f.stopped = true }
func (f *fakeRuntimeCommander) RuntimeDisarm() {}

func TestController_HandleCommandStopNotifiesStateMachine(t *testing.T) {
	commander := &fakeRuntimeCommander{}
	controller := &Controller{
		commander: commander,
		log:       slog.New(slog.NewTextHandler(os.Stdout, nil)),
	}

	controller.handleCommand("stop")

	if !commander.stopped {
		t.Error("expected stop command to notify the state machine")
	}
}

func TestController_StopWaitsForHornPattern(t *testing.T) {
	c, client := setupTestController(t, true)
	defer client.Close()

	c.hornMu.Lock()
	if err := c.Start(5 * time.Second); err != nil {
		c.hornMu.Unlock()
		t.Fatal(err)
	}
	// Hold a horn write across cancellation to exercise the final-write ordering.
	time.Sleep(500 * time.Millisecond)
	stopped := make(chan struct{})
	go func() {
		_ = c.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		c.hornMu.Unlock()
		t.Fatal("Stop returned before the horn pattern finished")
	case <-time.After(50 * time.Millisecond):
	}
	c.hornMu.Unlock()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish")
	}
	last, err := client.LPop("scooter:horn")
	if err != nil || last != "off" {
		t.Fatalf("last horn command = %q, error = %v; want off", last, err)
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

	if err := c.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	if c.active {
		t.Error("expected alarm to be inactive after stop")
	}
}
