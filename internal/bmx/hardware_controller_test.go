package bmx

import (
	"context"
	"log/slog"
	"os"
	"testing"
)

// mockAccelerometer for testing
type mockAccelerometer struct {
	interruptEnabled bool
}

func (m *mockAccelerometer) ConfigureSlowNoMotion(threshold, duration byte) error {
	return nil
}

func (m *mockAccelerometer) DisableInterruptMapping() error {
	return nil
}

func (m *mockAccelerometer) ConfigureInterruptPin(useInt2, latched bool) error {
	return nil
}

func (m *mockAccelerometer) MapInterruptToPin(useInt2 bool) error {
	return nil
}

func (m *mockAccelerometer) SoftReset() error {
	return nil
}

func (m *mockAccelerometer) EnableSlowNoMotionInterrupt(latched bool) error {
	m.interruptEnabled = true
	return nil
}

func (m *mockAccelerometer) DisableSlowNoMotionInterrupt() error {
	m.interruptEnabled = false
	return nil
}

// mockGyroscope for testing
type mockGyroscope struct{}

func (m *mockGyroscope) SoftReset() error {
	return nil
}

// mockInterruptPoller for testing
type mockInterruptPoller struct {
	enabled      bool
	enableCount  int
	disableCount int
}

func (m *mockInterruptPoller) Enable() {
	m.enabled = true
	m.enableCount++
}

func (m *mockInterruptPoller) Disable() {
	m.enabled = false
	m.disableCount++
}

func TestHardwareController_EnableInterrupt(t *testing.T) {
	accel := &mockAccelerometer{}
	gyro := &mockGyroscope{}
	poller := &mockInterruptPoller{}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	controller := NewHardwareController(accel, gyro, poller, log)

	ctx := context.Background()
	if err := controller.EnableInterrupt(ctx); err != nil {
		t.Fatalf("EnableInterrupt failed: %v", err)
	}

	if !accel.interruptEnabled {
		t.Error("expected accelerometer interrupt to be enabled")
	}

	if !poller.enabled {
		t.Error("expected interrupt poller to be enabled - this was the bug!")
	}

	if poller.enableCount != 1 {
		t.Errorf("expected poller.Enable() to be called once, got %d", poller.enableCount)
	}
}

func TestHardwareController_DisableInterrupt(t *testing.T) {
	accel := &mockAccelerometer{interruptEnabled: true}
	gyro := &mockGyroscope{}
	poller := &mockInterruptPoller{enabled: true}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	controller := NewHardwareController(accel, gyro, poller, log)

	ctx := context.Background()
	if err := controller.DisableInterrupt(ctx); err != nil {
		t.Fatalf("DisableInterrupt failed: %v", err)
	}

	if accel.interruptEnabled {
		t.Error("expected accelerometer interrupt to be disabled")
	}

	if poller.enabled {
		t.Error("expected interrupt poller to be disabled")
	}

	if poller.disableCount != 1 {
		t.Errorf("expected poller.Disable() to be called once, got %d", poller.disableCount)
	}
}

func TestHardwareController_EnableDisableCycle(t *testing.T) {
	accel := &mockAccelerometer{}
	gyro := &mockGyroscope{}
	poller := &mockInterruptPoller{}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	controller := NewHardwareController(accel, gyro, poller, log)
	ctx := context.Background()

	// Enable
	if err := controller.EnableInterrupt(ctx); err != nil {
		t.Fatalf("EnableInterrupt failed: %v", err)
	}

	if !poller.enabled {
		t.Error("expected poller to be enabled after EnableInterrupt")
	}

	// Disable
	if err := controller.DisableInterrupt(ctx); err != nil {
		t.Fatalf("DisableInterrupt failed: %v", err)
	}

	if poller.enabled {
		t.Error("expected poller to be disabled after DisableInterrupt")
	}

	// Enable again
	if err := controller.EnableInterrupt(ctx); err != nil {
		t.Fatalf("second EnableInterrupt failed: %v", err)
	}

	if !poller.enabled {
		t.Error("expected poller to be enabled after second EnableInterrupt")
	}

	if poller.enableCount != 2 {
		t.Errorf("expected Enable() to be called twice, got %d", poller.enableCount)
	}

	if poller.disableCount != 1 {
		t.Errorf("expected Disable() to be called once, got %d", poller.disableCount)
	}
}
