package bmx

// InterruptPin represents which interrupt pin to use
type InterruptPin int

const (
	InterruptPinNone InterruptPin = iota
	InterruptPinINT1
	InterruptPinINT2
	InterruptPinBoth
)

func (p InterruptPin) String() string {
	switch p {
	case InterruptPinNone:
		return "none"
	case InterruptPinINT1:
		return "int1"
	case InterruptPinINT2:
		return "int2"
	case InterruptPinBoth:
		return "both"
	default:
		return "unknown"
	}
}

// ParseInterruptPin parses a string to InterruptPin
func ParseInterruptPin(s string) InterruptPin {
	switch s {
	case "int1":
		return InterruptPinINT1
	case "int2":
		return InterruptPinINT2
	case "both":
		return InterruptPinBoth
	case "none":
		return InterruptPinNone
	default:
		return InterruptPinNone
	}
}

// Sensitivity represents motion detection sensitivity levels
type Sensitivity int

const (
	SensitivityLow Sensitivity = iota
	SensitivityMedium
	SensitivityHigh
)

func (s Sensitivity) String() string {
	switch s {
	case SensitivityLow:
		return "low"
	case SensitivityMedium:
		return "medium"
	case SensitivityHigh:
		return "high"
	default:
		return "unknown"
	}
}

// ParseSensitivity parses a string to Sensitivity
func ParseSensitivity(s string) Sensitivity {
	switch s {
	case "low":
		return SensitivityLow
	case "medium":
		return SensitivityMedium
	case "high":
		return SensitivityHigh
	default:
		return SensitivityMedium
	}
}

// GetThreshold returns the slow/no-motion threshold for register 0x29.
// Per BMX055 datasheet (BST-BMX055-DS000), 1 LSB = 3.91 mg in 2g range.
// These values are tuned for use with BW=7.81 Hz (sample period 64 ms).
// At low bandwidth, high-frequency vibrations are filtered out, so the
// thresholds can be lower than they'd need to be at 1 kHz.
//
//   - Low (0x14=20):    20 × 3.91 mg = ~78 mg  — ignores minor bumps/wind
//   - Medium (0x09=9):   9 × 3.91 mg = ~35 mg  — detects deliberate movement
//   - High (0x06=6):     6 × 3.91 mg = ~23 mg  — detects subtle tilting
func (s Sensitivity) GetThreshold() byte {
	switch s {
	case SensitivityLow:
		return 0x14 // ~78 mg
	case SensitivityMedium:
		return 0x09 // ~35 mg
	case SensitivityHigh:
		return 0x06 // ~23 mg
	default:
		return 0x09
	}
}

// GetDuration returns the slow/no-motion duration for register 0x27.
// In slow-motion mode N = dur + 1 consecutive samples must exceed the threshold.
// With BW=7.81 Hz (64 ms/sample), duration 0x02 → 3 samples = ~192 ms debounce.
func (s Sensitivity) GetDuration() byte {
	return 0x02 // 3 consecutive samples (~192 ms at 7.81 Hz)
}

// GetBandwidth returns the PMU_BW register value for this sensitivity level.
// All levels use 7.81 Hz — the lowest available bandwidth — to maximise rejection
// of high-frequency vibration (wind, traffic, engine noise) that would otherwise
// cause false L1 triggers. The power-on default is 1000 Hz so this must be set
// explicitly after every soft reset.
func (s Sensitivity) GetBandwidth() byte {
	return ACCEL_BW_7_81HZ
}
