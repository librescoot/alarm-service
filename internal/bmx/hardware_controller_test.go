package bmx

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
)

// mockAccelerometer for testing
type mockAccelerometer struct {
	interruptEnabled      bool
	slowNoMotionThreshold byte
	slowNoMotionDuration  byte
	interruptMappingOff   bool
	interruptPinInt2      bool
	interruptPinLatched   bool
	interruptMappedInt2   bool
	resetCount            int
	enableInterruptError  error
	disableInterruptError error
}

func (m *mockAccelerometer) ConfigureSlowNoMotion(threshold, duration byte) error {
	m.slowNoMotionThreshold = threshold
	m.slowNoMotionDuration = duration
	return nil
}

func (m *mockAccelerometer) DisableInterruptMapping() error {
	m.interruptMappingOff = true
	return nil
}

func (m *mockAccelerometer) ConfigureInterruptPin(useInt2, latched bool) error {
	m.interruptPinInt2 = useInt2
	m.interruptPinLatched = latched
	return nil
}

func (m *mockAccelerometer) MapInterruptToPin(useInt2 bool) error {
	m.interruptMappedInt2 = useInt2
	return nil
}

func (m *mockAccelerometer) SoftReset() error {
	m.resetCount++
	return nil
}

func (m *mockAccelerometer) EnableSlowNoMotionInterrupt(latched bool) error {
	if m.enableInterruptError != nil {
		return m.enableInterruptError
	}
	m.interruptEnabled = true
	return nil
}

func (m *mockAccelerometer) DisableSlowNoMotionInterrupt() error {
	if m.disableInterruptError != nil {
		return m.disableInterruptError
	}
	m.interruptEnabled = false
	return nil
}

// mockGyroscope for testing
type mockGyroscope struct {
	resetCount int
}

func (m *mockGyroscope) SoftReset() error {
	m.resetCount++
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

func TestHardwareController_SoftReset(t *testing.T) {
	accel := &mockAccelerometer{}
	gyro := &mockGyroscope{}
	poller := &mockInterruptPoller{}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	controller := NewHardwareController(accel, gyro, poller, log)

	ctx := context.Background()
	if err := controller.SoftReset(ctx); err != nil {
		t.Fatalf("SoftReset failed: %v", err)
	}

	if accel.resetCount != 1 {
		t.Errorf("expected accelerometer to be reset once, got %d", accel.resetCount)
	}

	if gyro.resetCount != 1 {
		t.Errorf("expected gyroscope to be reset once, got %d", gyro.resetCount)
	}

	// Call again to verify multiple resets work
	if err := controller.SoftReset(ctx); err != nil {
		t.Fatalf("second SoftReset failed: %v", err)
	}

	if accel.resetCount != 2 {
		t.Errorf("expected accelerometer to be reset twice, got %d", accel.resetCount)
	}

	if gyro.resetCount != 2 {
		t.Errorf("expected gyroscope to be reset twice, got %d", gyro.resetCount)
	}
}

func TestHardwareController_EnableInterruptError(t *testing.T) {
	accel := &mockAccelerometer{
		enableInterruptError: fmt.Errorf("hardware failure"),
	}
	gyro := &mockGyroscope{}
	poller := &mockInterruptPoller{}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	controller := NewHardwareController(accel, gyro, poller, log)

	ctx := context.Background()
	err := controller.EnableInterrupt(ctx)

	if err == nil {
		t.Fatal("expected EnableInterrupt to return error")
	}

	if poller.enabled {
		t.Error("expected poller to NOT be enabled when hardware enable fails")
	}
}

func TestHardwareController_DisableInterruptError(t *testing.T) {
	accel := &mockAccelerometer{
		disableInterruptError: fmt.Errorf("hardware failure"),
	}
	gyro := &mockGyroscope{}
	poller := &mockInterruptPoller{enabled: true}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	controller := NewHardwareController(accel, gyro, poller, log)

	ctx := context.Background()
	err := controller.DisableInterrupt(ctx)

	if err == nil {
		t.Fatal("expected DisableInterrupt to return error")
	}

	// Even though hardware disable failed, the poller should still be disabled
	// to prevent it from polling failed hardware
	if poller.enabled {
		t.Error("expected poller to be disabled even when hardware disable fails")
	}
}
