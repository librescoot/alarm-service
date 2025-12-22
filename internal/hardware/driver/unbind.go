package driver

import (
	"fmt"
	"os"
	"path/filepath"
)

// DriverBinding represents a kernel driver that needs to be unbound
type DriverBinding struct {
	DriverName string
	DeviceID   string
}

// Unbind unbinds a kernel driver from a device
func Unbind(driverName, deviceID string) error {
	unbindPath := filepath.Join("/sys/bus/i2c/drivers", driverName, "unbind")

	file, err := os.OpenFile(unbindPath, os.O_WRONLY, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to open unbind file %s: %w", unbindPath, err)
	}
	defer file.Close()

	_, err = file.WriteString(deviceID)
	if err != nil {
		return fmt.Errorf("failed to write device ID to unbind file: %w", err)
	}

	return nil
}

// UnbindBMX055 unbinds all three BMX055 kernel drivers
func UnbindBMX055() error {
	drivers := []DriverBinding{
		{"bmc150_accel_i2c", "3-0018"},
		{"bmg160_i2c", "3-0068"},
		{"bmm150_i2c", "3-0010"},
	}

	for _, d := range drivers {
		if err := Unbind(d.DriverName, d.DeviceID); err != nil {
			return fmt.Errorf("failed to unbind %s: %w", d.DriverName, err)
		}
	}

	return nil
}
