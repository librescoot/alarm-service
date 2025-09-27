package bmx

import (
	"fmt"
	"math"
)

// Accelerometer represents the BMX055 accelerometer
type Accelerometer struct {
	*i2cDevice
}

// NewAccelerometer creates and initializes the accelerometer
func NewAccelerometer(bus string) (*Accelerometer, error) {
	dev, err := openI2C(bus, BMX055_ACCEL_ADDR)
	if err != nil {
		return nil, err
	}
	dev.name = "Accelerometer"

	accel := &Accelerometer{i2cDevice: dev}

	chipID, err := accel.ReadByteData(ACCEL_CHIP_ID_REG)
	if err != nil {
		accel.Close()
		return nil, fmt.Errorf("failed to read accelerometer chip ID: %w", err)
	}

	if chipID != 0xFA && chipID != 0xFB {
		accel.Close()
		return nil, fmt.Errorf("invalid accelerometer chip ID: 0x%02X (expected 0xFA or 0xFB)", chipID)
	}

	if err := accel.WriteByteData(ACCEL_PMU_LPW, 0x00); err != nil {
		accel.Close()
		return nil, fmt.Errorf("failed to set accelerometer power mode: %w", err)
	}

	return accel, nil
}

// ReadData reads raw acceleration data (12-bit)
func (a *Accelerometer) ReadData() (x, y, z int16, err error) {
	xLSB, err := a.ReadByteData(ACCEL_ACCD_X_LSB_REG)
	if err != nil {
		return 0, 0, 0, err
	}
	xMSB, err := a.ReadByteData(ACCEL_ACCD_X_LSB_REG + 1)
	if err != nil {
		return 0, 0, 0, err
	}

	yLSB, err := a.ReadByteData(ACCEL_ACCD_Y_LSB_REG)
	if err != nil {
		return 0, 0, 0, err
	}
	yMSB, err := a.ReadByteData(ACCEL_ACCD_Y_LSB_REG + 1)
	if err != nil {
		return 0, 0, 0, err
	}

	zLSB, err := a.ReadByteData(ACCEL_ACCD_Z_LSB_REG)
	if err != nil {
		return 0, 0, 0, err
	}
	zMSB, err := a.ReadByteData(ACCEL_ACCD_Z_LSB_REG + 1)
	if err != nil {
		return 0, 0, 0, err
	}

	x = int16(xMSB)<<8 | int16(xLSB)
	y = int16(yMSB)<<8 | int16(yLSB)
	z = int16(zMSB)<<8 | int16(zLSB)

	x = x >> 4
	y = y >> 4
	z = z >> 4

	return x, y, z, nil
}

// ReadDataInG reads acceleration data converted to g-force
func (a *Accelerometer) ReadDataInG() (x, y, z, magnitude float64, err error) {
	rawX, rawY, rawZ, err := a.ReadData()
	if err != nil {
		return 0, 0, 0, 0, err
	}

	const scale = 1024.0
	x = float64(rawX) / scale
	y = float64(rawY) / scale
	z = float64(rawZ) / scale
	magnitude = math.Sqrt(x*x + y*y + z*z)

	return x, y, z, magnitude, nil
}

// ConfigureSlowNoMotion configures slow/no-motion detection
func (a *Accelerometer) ConfigureSlowNoMotion(threshold, duration byte) error {
	if err := a.WriteByteData(ACCEL_SLO_NO_MOT_THRESHOLD, threshold); err != nil {
		return fmt.Errorf("failed to set slow/no-motion threshold: %w", err)
	}

	if err := a.WriteByteData(ACCEL_SLO_NO_MOT_DURATION, duration); err != nil {
		return fmt.Errorf("failed to set slow/no-motion duration: %w", err)
	}

	return nil
}

// ConfigureInterruptPin configures the interrupt pin behavior
func (a *Accelerometer) ConfigureInterruptPin(useInt2 bool, latched bool) error {
	outCtrl, err := a.ReadByteData(ACCEL_INT_OUT_CTRL)
	if err != nil {
		return fmt.Errorf("failed to read interrupt output control: %w", err)
	}

	if useInt2 {
		outCtrl |= ACCEL_INT2_ACTIVE_HIGH
		outCtrl &^= ACCEL_INT2_OPEN_DRAIN
	} else {
		outCtrl |= ACCEL_INT1_ACTIVE_HIGH
		outCtrl &^= ACCEL_INT1_OPEN_DRAIN
	}

	if err := a.WriteByteData(ACCEL_INT_OUT_CTRL, outCtrl); err != nil {
		return fmt.Errorf("failed to write interrupt output control: %w", err)
	}

	latchMode := byte(ACCEL_INT_NON_LATCHED)
	if latched {
		latchMode = byte(ACCEL_INT_LATCHED)
	}

	if err := a.WriteByteData(ACCEL_INT_LATCH, latchMode); err != nil {
		return fmt.Errorf("failed to write interrupt latch: %w", err)
	}

	return nil
}

// MapInterruptToPin maps slow/no-motion interrupt to INT1 or INT2
func (a *Accelerometer) MapInterruptToPin(useInt2 bool) error {
	if useInt2 {
		if err := a.WriteByteData(ACCEL_INT_MAP_2, ACCEL_INT2_MAP_SLOW_NO_MOTION); err != nil {
			return fmt.Errorf("failed to map interrupt to INT2: %w", err)
		}
	} else {
		if err := a.WriteByteData(ACCEL_INT_MAP_0, ACCEL_INT1_MAP_SLOW_NO_MOTION); err != nil {
			return fmt.Errorf("failed to map interrupt to INT1: %w", err)
		}
	}
	return nil
}

// DisableInterruptMapping disables interrupt mapping to any pin
func (a *Accelerometer) DisableInterruptMapping() error {
	if err := a.WriteByteData(ACCEL_INT_MAP_0, 0x00); err != nil {
		return fmt.Errorf("failed to clear INT1 mapping: %w", err)
	}
	if err := a.WriteByteData(ACCEL_INT_MAP_2, 0x00); err != nil {
		return fmt.Errorf("failed to clear INT2 mapping: %w", err)
	}
	return nil
}

// EnableSlowNoMotionInterrupt enables slow-motion interrupts on X/Y/Z axes
func (a *Accelerometer) EnableSlowNoMotionInterrupt(slowMotion bool) error {
	intEn := byte(ACCEL_INT_EN_SLOW_NO_MOTION_X | ACCEL_INT_EN_SLOW_NO_MOTION_Y | ACCEL_INT_EN_SLOW_NO_MOTION_Z)

	if !slowMotion {
		intEn |= ACCEL_INT_EN_SLOW_NO_MOTION_SEL
	}

	if err := a.WriteByteData(ACCEL_INT_EN_2, intEn); err != nil {
		return fmt.Errorf("failed to enable slow/no-motion interrupt: %w", err)
	}

	return nil
}

// DisableSlowNoMotionInterrupt disables slow/no-motion interrupts
func (a *Accelerometer) DisableSlowNoMotionInterrupt() error {
	if err := a.WriteByteData(ACCEL_INT_EN_2, 0x00); err != nil {
		return fmt.Errorf("failed to disable slow/no-motion interrupt: %w", err)
	}
	return nil
}

// GetInterruptStatus reads and checks if slow/no-motion interrupt occurred
func (a *Accelerometer) GetInterruptStatus() (bool, error) {
	status, err := a.ReadByteData(ACCEL_INT_STATUS_0)
	if err != nil {
		return false, fmt.Errorf("failed to read interrupt status: %w", err)
	}

	return (status & ACCEL_INT_STATUS_SLOW_NO_MOT) != 0, nil
}

// ClearLatchedInterrupt clears a latched interrupt
func (a *Accelerometer) ClearLatchedInterrupt() error {
	if err := a.WriteByteData(ACCEL_INT_RST_LATCH, 0x80); err != nil {
		return fmt.Errorf("failed to clear latched interrupt: %w", err)
	}
	return nil
}

// SoftReset performs a soft reset of the accelerometer
func (a *Accelerometer) SoftReset() error {
	if err := a.WriteByteData(ACCEL_BGW_SOFTRESET, 0xB6); err != nil {
		return fmt.Errorf("failed to soft reset accelerometer: %w", err)
	}
	return nil
}

// SetupMotionDetection configures the accelerometer for motion detection
func (a *Accelerometer) SetupMotionDetection(threshold, duration byte, useInt2, latched bool) error {
	if err := a.ConfigureSlowNoMotion(threshold, duration); err != nil {
		return err
	}

	if err := a.WriteByteData(ACCEL_INT_SRC, 0x00); err != nil {
		return fmt.Errorf("failed to set interrupt source: %w", err)
	}

	if err := a.ConfigureInterruptPin(useInt2, latched); err != nil {
		return err
	}

	if err := a.MapInterruptToPin(useInt2); err != nil {
		return err
	}

	if err := a.EnableSlowNoMotionInterrupt(true); err != nil {
		return err
	}

	return nil
}