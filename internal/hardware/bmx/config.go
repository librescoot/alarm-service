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

// InterruptMode selects which BMX055 interrupt engine to use.
type InterruptMode int

const (
	// InterruptModeAnyMotion uses the slope/any-motion engine (register 0x16).
	// Fires when |accel[n] - accel[n-2]| exceeds threshold for N consecutive samples.
	// Responsive to brief impacts — suitable for initial alertness detection.
	InterruptModeAnyMotion InterruptMode = iota

	// InterruptModeSlowMotion uses the slow-motion engine (register 0x18).
	// Fires when the slope exceeds threshold for N consecutive samples.
	// Requires sustained movement — suitable for confirming deliberate manipulation.
	InterruptModeSlowMotion
)

func (m InterruptMode) String() string {
	switch m {
	case InterruptModeAnyMotion:
		return "any-motion"
	case InterruptModeSlowMotion:
		return "slow-motion"
	default:
		return "unknown"
	}
}

// SensorConfig holds the full hardware configuration for a detection stage.
type SensorConfig struct {
	Mode      InterruptMode
	Bandwidth byte // PMU_BW register value
	Threshold byte // 1 LSB = 3.91 mg in 2g range
	Duration  byte // N = dur+1 consecutive samples must exceed threshold
}

// ArmedConfig is used in the Armed state: any-motion at 31.25 Hz.
// Catches brief impacts (dog bump, hand on handlebar) within ~32 ms.
// threshold 4 → ~16 mg; 2 samples = 32 ms debounce.
var ArmedConfig = SensorConfig{
	Mode:      InterruptModeAnyMotion,
	Bandwidth: ACCEL_BW_31_25HZ,
	Threshold: 0x04, // ~16 mg
	Duration:  0x01, // 2 samples (32 ms at 31.25 Hz)
}

// Level1Config is used in TriggerLevel1 state: slow-motion at 15.63 Hz.
// Requires sustained slope (~256 ms) — confirms deliberate push or tilt.
// threshold 8 → ~31 mg; 4 samples = 256 ms debounce.
var Level1Config = SensorConfig{
	Mode:      InterruptModeSlowMotion,
	Bandwidth: ACCEL_BW_15_63HZ,
	Threshold: 0x08, // ~31 mg
	Duration:  0x03, // 4 samples (256 ms at 15.63 Hz)
}

// WaitingMovementConfig is used in WaitingMovement state: slow-motion at 7.81 Hz.
// Conservative — only re-triggers L2 for ongoing large-scale manipulation.
// threshold 6 → ~23 mg; 4 samples = 512 ms debounce.
var WaitingMovementConfig = SensorConfig{
	Mode:      InterruptModeSlowMotion,
	Bandwidth: ACCEL_BW_7_81HZ,
	Threshold: 0x06, // ~23 mg
	Duration:  0x03, // 4 samples (512 ms at 7.81 Hz)
}

// GetThreshold returns the slow/no-motion threshold for register 0x29.
// 1 LSB = 3.91 mg in 2g range. Used as fallback where no SensorConfig is specified.
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
// N = dur + 1 consecutive samples must exceed threshold.
func (s Sensitivity) GetDuration() byte {
	return 0x02 // 3 consecutive samples
}

// GetBandwidth returns the PMU_BW register value.
// The power-on default is 1000 Hz and must be set explicitly after every soft reset.
func (s Sensitivity) GetBandwidth() byte {
	return ACCEL_BW_7_81HZ
}
