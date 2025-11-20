package fsm

// Event represents an event that can trigger state transitions
type Event interface {
	Type() string
}

// InitCompleteEvent signals that BMX service is initialized
type InitCompleteEvent struct{}

func (e InitCompleteEvent) Type() string { return "init_complete" }

// AlarmModeChangedEvent signals alarm mode enabled/disabled
type AlarmModeChangedEvent struct {
	Enabled bool
}

func (e AlarmModeChangedEvent) Type() string { return "alarm_mode_changed" }

// HornSettingChangedEvent signals horn setting changed
type HornSettingChangedEvent struct {
	Enabled bool
}

func (e HornSettingChangedEvent) Type() string { return "horn_setting_changed" }

// AlarmDurationChangedEvent signals alarm duration changed
type AlarmDurationChangedEvent struct {
	Duration int
}

func (e AlarmDurationChangedEvent) Type() string { return "alarm_duration_changed" }

// VehicleStateChangedEvent signals vehicle state change
type VehicleStateChangedEvent struct {
	State VehicleState
}

func (e VehicleStateChangedEvent) Type() string { return "vehicle_state_changed" }

// BMXInterruptEvent signals motion detected by BMX
type BMXInterruptEvent struct {
	Timestamp int64
	Data      string
}

func (e BMXInterruptEvent) Type() string { return "bmx_interrupt" }

// TemporarilyDisarmEvent signals temporary disarm request
type TemporarilyDisarmEvent struct{}

func (e TemporarilyDisarmEvent) Type() string { return "temporarily_disarm" }

// DelayArmedTimerEvent signals delay armed timer expired
type DelayArmedTimerEvent struct{}

func (e DelayArmedTimerEvent) Type() string { return "delay_armed_timer" }

// Level1CooldownTimerEvent signals level 1 cooldown complete
type Level1CooldownTimerEvent struct{}

func (e Level1CooldownTimerEvent) Type() string { return "level1_cooldown_timer" }

// Level1CheckTimerEvent signals time to check for level 1 movement
type Level1CheckTimerEvent struct{}

func (e Level1CheckTimerEvent) Type() string { return "level1_check_timer" }

// Level2CheckTimerEvent signals level 2 check complete
type Level2CheckTimerEvent struct{}

func (e Level2CheckTimerEvent) Type() string { return "level2_check_timer" }

// ChipSetupTimerEvent signals time to setup chip for level 2
type ChipSetupTimerEvent struct{}

func (e ChipSetupTimerEvent) Type() string { return "chip_setup_timer" }

// MinorMovementEvent signals minor movement detected
type MinorMovementEvent struct{}

func (e MinorMovementEvent) Type() string { return "minor_movement" }

// MajorMovementEvent signals major movement detected
type MajorMovementEvent struct{}

func (e MajorMovementEvent) Type() string { return "major_movement" }

// NoMovementEvent signals no movement detected
type NoMovementEvent struct{}

func (e NoMovementEvent) Type() string { return "no_movement" }

// ManualTriggerEvent signals manual alarm trigger
type ManualTriggerEvent struct {
	Duration int
}

func (e ManualTriggerEvent) Type() string { return "manual_trigger" }

// SeatboxOpenedEvent signals authorized seatbox opening
type SeatboxOpenedEvent struct{}

func (e SeatboxOpenedEvent) Type() string { return "seatbox_opened" }

// SeatboxClosedEvent signals seatbox was closed
type SeatboxClosedEvent struct{}

func (e SeatboxClosedEvent) Type() string { return "seatbox_closed" }

// UnauthorizedSeatboxEvent signals unauthorized seatbox opening
type UnauthorizedSeatboxEvent struct{}

func (e UnauthorizedSeatboxEvent) Type() string { return "unauthorized_seatbox" }

// VehicleState represents the vehicle state
type VehicleState int

const (
	VehicleStateUnknown VehicleState = iota
	VehicleStateInit
	VehicleStateStandby
	VehicleStateParked
	VehicleStateReadyToDrive
	VehicleStateWaitingSeatbox
	VehicleStateShuttingDown
	VehicleStateWaitingHibernation
)

func (s VehicleState) String() string {
	switch s {
	case VehicleStateInit:
		return "init"
	case VehicleStateStandby:
		return "stand-by"
	case VehicleStateParked:
		return "parked"
	case VehicleStateReadyToDrive:
		return "ready-to-drive"
	case VehicleStateWaitingSeatbox:
		return "waiting-seatbox"
	case VehicleStateShuttingDown:
		return "shutting-down"
	case VehicleStateWaitingHibernation:
		return "waiting-hibernation"
	default:
		return "unknown"
	}
}

// ParseVehicleState parses a string to VehicleState
func ParseVehicleState(s string) VehicleState {
	switch s {
	case "init":
		return VehicleStateInit
	case "stand-by":
		return VehicleStateStandby
	case "parked":
		return VehicleStateParked
	case "ready-to-drive":
		return VehicleStateReadyToDrive
	case "waiting-seatbox":
		return VehicleStateWaitingSeatbox
	case "shutting-down":
		return VehicleStateShuttingDown
	case "waiting-hibernation":
		return VehicleStateWaitingHibernation
	default:
		return VehicleStateUnknown
	}
}