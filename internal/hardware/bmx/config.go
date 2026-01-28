package bmx

// InterruptPin represents which interrupt pin to use
type InterruptPin int

const (
	InterruptPinNone InterruptPin = iota
	InterruptPinINT1
	InterruptPinINT2
)

func (p InterruptPin) String() string {
	switch p {
	case InterruptPinNone:
		return "none"
	case InterruptPinINT1:
		return "int1"
	case InterruptPinINT2:
		return "int2"
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
// Values were empirically tuned for vehicle motion detection:
//   - Low (0x10=16):  16 × 3.91 mg = ~63 mg - minimal sensitivity, ignores small bumps
//   - Medium (0x09=9): 9 × 3.91 mg = ~35 mg - balanced for typical use
//   - High (0x08=8):   8 × 3.91 mg = ~31 mg - detects subtle movement
func (s Sensitivity) GetThreshold() byte {
	switch s {
	case SensitivityLow:
		return 0x10 // ~63 mg
	case SensitivityMedium:
		return 0x09 // ~35 mg
	case SensitivityHigh:
		return 0x08 // ~31 mg
	default:
		return 0x09
	}
}

// GetDuration returns the slow/no-motion duration for register 0x27.
// Per BMX055 datasheet, in slow-motion mode this sets the number of
// consecutive samples (N = dur + 1) that must exceed threshold.
// Duration 0x01 means 2 consecutive samples required, providing
// basic debouncing while maintaining quick response.
func (s Sensitivity) GetDuration() byte {
	return 0x01 // 2 consecutive samples
}
