package bmx

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"alarm-service/internal/fsm"
	"alarm-service/internal/hardware/bmx"
)

// Accelerometer interface for testing
type Accelerometer interface {
	ConfigureSlowNoMotion(threshold, duration byte) error
	DisableInterruptMapping() error
	ConfigureInterruptPin(useInt2, latched bool) error
	MapInterruptToPin(useInt2 bool) error
	SoftReset() error
	EnableSlowNoMotionInterrupt(latched bool) error
	DisableSlowNoMotionInterrupt() error
}

// Gyroscope interface for testing
type Gyroscope interface {
	SoftReset() error
}

// InterruptPoller interface for enabling/disabling interrupt polling
type InterruptPoller interface {
	Enable()
	Disable()
}

// HardwareController controls the BMX055 hardware directly
type HardwareController struct {
	accel  Accelerometer
	gyro   Gyroscope
	poller InterruptPoller
	log    *slog.Logger
}

// NewHardwareController creates a new hardware controller
func NewHardwareController(accel Accelerometer, gyro Gyroscope, poller InterruptPoller, log *slog.Logger) *HardwareController {
	return &HardwareController{
		accel:  accel,
		gyro:   gyro,
		poller: poller,
		log:    log,
	}
}

// SetSensitivity sets the BMX sensitivity
func (c *HardwareController) SetSensitivity(ctx context.Context, sens fsm.Sensitivity) error {
	hwSens := bmx.ParseSensitivity(sens.String())
	threshold := hwSens.GetThreshold()
	duration := hwSens.GetDuration()

	c.log.Info("setting sensitivity", "level", sens.String(), "threshold", threshold, "duration", duration)

	if err := c.accel.ConfigureSlowNoMotion(threshold, duration); err != nil {
		return fmt.Errorf("failed to configure slow/no-motion: %w", err)
	}

	return nil
}

// SetInterruptPin sets the BMX interrupt pin
func (c *HardwareController) SetInterruptPin(ctx context.Context, pin fsm.InterruptPin) error {
	hwPin := bmx.ParseInterruptPin(pin.String())
	c.log.Info("setting interrupt pin", "pin", pin.String())

	if hwPin == bmx.InterruptPinNone {
		if err := c.accel.DisableInterruptMapping(); err != nil {
			return fmt.Errorf("failed to disable interrupt mapping: %w", err)
		}
	} else {
		useInt2 := hwPin == bmx.InterruptPinINT2
		if err := c.accel.ConfigureInterruptPin(useInt2, true); err != nil {
			return fmt.Errorf("failed to configure interrupt pin: %w", err)
		}
		if err := c.accel.MapInterruptToPin(useInt2); err != nil {
			return fmt.Errorf("failed to map interrupt to pin: %w", err)
		}
	}

	return nil
}

// SoftReset performs a soft reset of BMX sensors
func (c *HardwareController) SoftReset(ctx context.Context) error {
	c.log.Info("performing soft reset")

	var errs []error
	if err := c.accel.SoftReset(); err != nil {
		errs = append(errs, fmt.Errorf("accelerometer: %w", err))
	}

	if err := c.gyro.SoftReset(); err != nil {
		errs = append(errs, fmt.Errorf("gyroscope: %w", err))
	}

	time.Sleep(10 * time.Millisecond)

	if len(errs) > 0 {
		return fmt.Errorf("soft reset failed: %v", errs)
	}
	return nil
}

// EnableInterrupt enables BMX interrupt
func (c *HardwareController) EnableInterrupt(ctx context.Context) error {
	c.log.Info("enabling interrupt")

	if err := c.accel.EnableSlowNoMotionInterrupt(true); err != nil {
		return fmt.Errorf("failed to enable interrupt: %w", err)
	}

	c.poller.Enable()

	return nil
}

// DisableInterrupt disables BMX interrupt
func (c *HardwareController) DisableInterrupt(ctx context.Context) error {
	c.log.Info("disabling interrupt")

	err := c.accel.DisableSlowNoMotionInterrupt()

	// Always disable the poller, even if hardware disable fails
	// to prevent polling potentially broken hardware
	c.poller.Disable()

	if err != nil {
		return fmt.Errorf("failed to disable interrupt: %w", err)
	}

	return nil
}

// Close closes the hardware controller
func (c *HardwareController) Close() error {
	return nil
}
