package bmx

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"alarm-service/internal/fsm"
	hwbmx "alarm-service/internal/hardware/bmx"
)

// Accelerometer interface for testing
type Accelerometer interface {
	SetBandwidth(bw byte) error
	// Slow/no-motion engine
	ConfigureSlowNoMotion(threshold, duration byte) error
	EnableSlowNoMotionInterrupt(latched bool) error
	DisableSlowNoMotionInterrupt() error
	GetInterruptStatus() (bool, error)
	// Any-motion (slope) engine
	EnableAnyMotionInterrupt(threshold, duration byte) error
	DisableAnyMotionInterrupt() error
	MapAnyMotionToPins(pin hwbmx.InterruptPin) error
	GetAnyMotionInterruptStatus() (bool, error)
	// Shared
	DisableInterruptMapping() error
	ConfigureInterruptPin(useInt2, latched bool) error
	ConfigureInterruptPins(pin hwbmx.InterruptPin, latched bool) error
	MapInterruptToPin(useInt2 bool) error
	MapInterruptToPins(pin hwbmx.InterruptPin) error
	SoftReset() error
	ClearLatchedInterrupt() error
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
	accel       Accelerometer
	gyro        Gyroscope
	poller      InterruptPoller
	log         *slog.Logger
	currentMode hwbmx.InterruptMode
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

// ConfigureSensor satisfies the fsm.BMXClient interface. Translates fsm.SensorConfig
// (AnyMotion bool) to the hardware-layer hwbmx.SensorConfig and applies it.
func (c *HardwareController) ConfigureSensor(ctx context.Context, cfg fsm.SensorConfig) error {
	mode := hwbmx.InterruptModeSlowMotion
	if cfg.AnyMotion {
		mode = hwbmx.InterruptModeAnyMotion
	}
	return c.configureSensorHW(ctx, hwbmx.SensorConfig{
		Mode:      mode,
		Bandwidth: cfg.Bandwidth,
		Threshold: cfg.Threshold,
		Duration:  cfg.Duration,
	})
}

// configureSensorHW applies a full hardware SensorConfig: bandwidth, interrupt mode, threshold, duration.
// Disables whichever interrupt engine is not in use to avoid crosstalk.
func (c *HardwareController) configureSensorHW(ctx context.Context, cfg hwbmx.SensorConfig) error {
	c.log.Info("configuring sensor", "mode", cfg.Mode.String(), "bw", cfg.Bandwidth, "threshold", cfg.Threshold, "duration", cfg.Duration)

	if err := c.accel.SetBandwidth(cfg.Bandwidth); err != nil {
		return fmt.Errorf("failed to set bandwidth: %w", err)
	}

	switch cfg.Mode {
	case hwbmx.InterruptModeAnyMotion:
		if err := c.accel.DisableSlowNoMotionInterrupt(); err != nil {
			return fmt.Errorf("failed to disable slow-motion: %w", err)
		}
		if err := c.accel.EnableAnyMotionInterrupt(cfg.Threshold, cfg.Duration); err != nil {
			return fmt.Errorf("failed to enable any-motion: %w", err)
		}
	case hwbmx.InterruptModeSlowMotion:
		if err := c.accel.DisableAnyMotionInterrupt(); err != nil {
			return fmt.Errorf("failed to disable any-motion: %w", err)
		}
		if err := c.accel.ConfigureSlowNoMotion(cfg.Threshold, cfg.Duration); err != nil {
			return fmt.Errorf("failed to configure slow-motion: %w", err)
		}
	}

	c.currentMode = cfg.Mode
	return nil
}

// SetInterruptPin sets the BMX interrupt pin
func (c *HardwareController) SetInterruptPin(ctx context.Context, pin fsm.InterruptPin) error {
	hwPin := hwbmx.ParseInterruptPin(pin.String())
	c.log.Info("setting interrupt pin", "pin", pin.String())

	if hwPin == hwbmx.InterruptPinNone {
		if err := c.accel.DisableInterruptMapping(); err != nil {
			return fmt.Errorf("failed to disable interrupt mapping: %w", err)
		}
	} else if hwPin == hwbmx.InterruptPinBoth {
		if err := c.accel.ConfigureInterruptPins(hwbmx.InterruptPinBoth, true); err != nil {
			return fmt.Errorf("failed to configure interrupt pins: %w", err)
		}
		if err := c.accel.MapInterruptToPins(hwbmx.InterruptPinBoth); err != nil {
			return fmt.Errorf("failed to map interrupt to pins: %w", err)
		}
	} else {
		useInt2 := hwPin == hwbmx.InterruptPinINT2
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

// EnableInterrupt maps interrupt pins and enables the poller for the current mode.
// Must be called after ConfigureSensor. Clears any stale latched interrupt first
// to avoid false triggers from filter-settling transients during configuration.
func (c *HardwareController) EnableInterrupt(ctx context.Context) error {
	c.log.Info("enabling interrupt", "mode", c.currentMode.String())

	if c.currentMode == hwbmx.InterruptModeAnyMotion {
		if err := c.accel.MapAnyMotionToPins(hwbmx.InterruptPinBoth); err != nil {
			return fmt.Errorf("failed to map any-motion interrupt: %w", err)
		}
	} else {
		if err := c.accel.EnableSlowNoMotionInterrupt(true); err != nil {
			return fmt.Errorf("failed to enable slow-motion interrupt: %w", err)
		}
	}

	if err := c.accel.ClearLatchedInterrupt(); err != nil {
		c.log.Warn("failed to clear latched interrupt before enabling poller", "error", err)
	}

	// Wait for the low-pass filter to settle after bandwidth change, then clear
	// again. The first samples after reconfiguration can produce a transient slope
	// that triggers a false interrupt. One sample period is sufficient (32ms at
	// 31.25 Hz, 64ms at 15.63 Hz); 100ms covers all bandwidths with margin.
	time.Sleep(100 * time.Millisecond)

	if err := c.accel.ClearLatchedInterrupt(); err != nil {
		c.log.Warn("failed to clear latched interrupt after settle", "error", err)
	}

	c.poller.Enable()
	return nil
}

// DisableInterrupt disables both interrupt engines and the poller.
func (c *HardwareController) DisableInterrupt(ctx context.Context) error {
	c.log.Info("disabling interrupt")

	var errs []error
	if err := c.accel.DisableSlowNoMotionInterrupt(); err != nil {
		errs = append(errs, err)
	}
	if err := c.accel.DisableAnyMotionInterrupt(); err != nil {
		errs = append(errs, err)
	}

	// Always disable the poller even if hardware disable fails.
	c.poller.Disable()

	if len(errs) > 0 {
		return fmt.Errorf("failed to disable interrupt: %v", errs)
	}
	return nil
}

// CheckInterruptStatus reads the appropriate interrupt status register for the
// current mode, clears the latch if triggered, and returns whether motion was detected.
func (c *HardwareController) CheckInterruptStatus(ctx context.Context) (bool, error) {
	var triggered bool
	var err error

	if c.currentMode == hwbmx.InterruptModeAnyMotion {
		triggered, err = c.accel.GetAnyMotionInterruptStatus()
	} else {
		triggered, err = c.accel.GetInterruptStatus()
	}
	if err != nil {
		return false, fmt.Errorf("failed to read interrupt status: %w", err)
	}
	if triggered {
		if err := c.accel.ClearLatchedInterrupt(); err != nil {
			c.log.Warn("failed to clear latched interrupt", "error", err)
		}
	}
	return triggered, nil
}

// Close closes the hardware controller
func (c *HardwareController) Close() error {
	return nil
}
