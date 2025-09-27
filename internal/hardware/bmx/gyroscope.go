package bmx

import (
	"fmt"
	"math"
)

// Gyroscope represents the BMX055 gyroscope
type Gyroscope struct {
	*i2cDevice
}

// NewGyroscope creates and initializes the gyroscope
func NewGyroscope(bus string) (*Gyroscope, error) {
	dev, err := openI2C(bus, BMX055_GYRO_ADDR)
	if err != nil {
		return nil, err
	}
	dev.name = "Gyroscope"

	gyro := &Gyroscope{i2cDevice: dev}

	chipID, err := gyro.ReadByteData(GYRO_CHIP_ID_REG)
	if err != nil {
		gyro.Close()
		return nil, fmt.Errorf("failed to read gyroscope chip ID: %w", err)
	}

	if chipID != 0x0F {
		gyro.Close()
		return nil, fmt.Errorf("invalid gyroscope chip ID: 0x%02X (expected 0x0F)", chipID)
	}

	if err := gyro.WriteByteData(GYRO_LPM1, 0x00); err != nil {
		gyro.Close()
		return nil, fmt.Errorf("failed to set gyroscope power mode: %w", err)
	}

	return gyro, nil
}

// ReadData reads raw gyroscope data (16-bit)
func (g *Gyroscope) ReadData() (x, y, z int16, err error) {
	xLSB, err := g.ReadByteData(GYRO_RATE_X_LSB)
	if err != nil {
		return 0, 0, 0, err
	}
	xMSB, err := g.ReadByteData(GYRO_RATE_X_LSB + 1)
	if err != nil {
		return 0, 0, 0, err
	}

	yLSB, err := g.ReadByteData(GYRO_RATE_Y_LSB)
	if err != nil {
		return 0, 0, 0, err
	}
	yMSB, err := g.ReadByteData(GYRO_RATE_Y_LSB + 1)
	if err != nil {
		return 0, 0, 0, err
	}

	zLSB, err := g.ReadByteData(GYRO_RATE_Z_LSB)
	if err != nil {
		return 0, 0, 0, err
	}
	zMSB, err := g.ReadByteData(GYRO_RATE_Z_LSB + 1)
	if err != nil {
		return 0, 0, 0, err
	}

	x = int16(xMSB)<<8 | int16(xLSB)
	y = int16(yMSB)<<8 | int16(yLSB)
	z = int16(zMSB)<<8 | int16(zLSB)

	return x, y, z, nil
}

// ReadDataInDPS reads gyroscope data converted to degrees/second
func (g *Gyroscope) ReadDataInDPS() (x, y, z, magnitude float64, err error) {
	rawX, rawY, rawZ, err := g.ReadData()
	if err != nil {
		return 0, 0, 0, 0, err
	}

	const scale = 16.4
	x = float64(rawX) / scale
	y = float64(rawY) / scale
	z = float64(rawZ) / scale
	magnitude = math.Sqrt(x*x + y*y + z*z)

	return x, y, z, magnitude, nil
}

// SoftReset performs a soft reset of the gyroscope
func (g *Gyroscope) SoftReset() error {
	if err := g.WriteByteData(GYRO_BGW_SOFTRESET, 0xB6); err != nil {
		return fmt.Errorf("failed to soft reset gyroscope: %w", err)
	}
	return nil
}