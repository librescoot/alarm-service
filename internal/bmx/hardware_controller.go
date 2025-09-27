package bmx

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"alarm-service/internal/fsm"
	"alarm-service/internal/hardware/bmx"
)

// HardwareController controls the BMX055 hardware directly
type HardwareController struct {
	accel *bmx.Accelerometer
	gyro  *bmx.Gyroscope
	log   *slog.Logger
}

// NewHardwareController creates a new hardware controller
func NewHardwareController(accel *bmx.Accelerometer, gyro *bmx.Gyroscope, log *slog.Logger) *HardwareController {
	return &HardwareController{
		accel: accel,
		gyro:  gyro,
		log:   log,
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

	if err := c.accel.SoftReset(); err != nil {
		c.log.Error("failed to reset accelerometer", "error", err)
	}

	if err := c.gyro.SoftReset(); err != nil {
		c.log.Error("failed to reset gyroscope", "error", err)
	}

	time.Sleep(10 * time.Millisecond)
	return nil
}

// EnableInterrupt enables BMX interrupt
func (c *HardwareController) EnableInterrupt(ctx context.Context) error {
	c.log.Info("enabling interrupt")

	if err := c.accel.EnableSlowNoMotionInterrupt(true); err != nil {
		return fmt.Errorf("failed to enable interrupt: %w", err)
	}

	return nil
}

// DisableInterrupt disables BMX interrupt
func (c *HardwareController) DisableInterrupt(ctx context.Context) error {
	c.log.Info("disabling interrupt")

	if err := c.accel.DisableSlowNoMotionInterrupt(); err != nil {
		return fmt.Errorf("failed to disable interrupt: %w", err)
	}

	return nil
}

// Close closes the hardware controller
func (c *HardwareController) Close() error {
	return nil
}
