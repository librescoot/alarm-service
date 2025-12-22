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

// GetThreshold returns the threshold value for a given sensitivity
func (s Sensitivity) GetThreshold() byte {
	switch s {
	case SensitivityLow:
		return 0x10
	case SensitivityMedium:
		return 0x09
	case SensitivityHigh:
		return 0x08
	default:
		return 0x09
	}
}

// GetDuration returns the duration value for a given sensitivity
func (s Sensitivity) GetDuration() byte {
	return 0x01
}
