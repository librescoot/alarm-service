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

// HairTriggerSettingChangedEvent signals hair trigger mode enabled/disabled
type HairTriggerSettingChangedEvent struct {
	Enabled bool
}

func (e HairTriggerSettingChangedEvent) Type() string { return "hair_trigger_setting_changed" }

// HairTriggerDurationChangedEvent signals hair trigger duration changed
type HairTriggerDurationChangedEvent struct {
	Duration int
}

func (e HairTriggerDurationChangedEvent) Type() string { return "hair_trigger_duration_changed" }

// L1CooldownDurationChangedEvent signals L1 cooldown duration changed
type L1CooldownDurationChangedEvent struct {
	Duration int
}

func (e L1CooldownDurationChangedEvent) Type() string { return "l1_cooldown_duration_changed" }

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

// TriggerSource identifies which discrete input caused an InputTriggerEvent.
// Motion and seatbox are kept on their own event types for historical reasons;
// this covers the additional non-motion sources (buttons, handlebar sensors).
type TriggerSource int

const (
	TriggerSourceUnknown TriggerSource = iota
	TriggerSourceBrakeLeft
	TriggerSourceBrakeRight
	TriggerSourceSeatboxButton
	TriggerSourceHornButton
	TriggerSourceHandlebarLock
	TriggerSourceHandlebarPosition
)

func (s TriggerSource) String() string {
	switch s {
	case TriggerSourceBrakeLeft:
		return "brake_left"
	case TriggerSourceBrakeRight:
		return "brake_right"
	case TriggerSourceSeatboxButton:
		return "seatbox_button"
	case TriggerSourceHornButton:
		return "horn_button"
	case TriggerSourceHandlebarLock:
		return "handlebar_lock"
	case TriggerSourceHandlebarPosition:
		return "handlebar_position"
	default:
		return "unknown"
	}
}

// InputTriggerEvent signals tamper detected via a discrete input other than
// the BMX accelerometer — brake levers, handlebar buttons, handlebar lock
// sensor, handlebar position sensor. Treated by the FSM like BMXInterruptEvent
// for escalation purposes. Emitted by the subscriber only when the matching
// per-source flag is enabled.
type InputTriggerEvent struct {
	Source TriggerSource
}

func (e InputTriggerEvent) Type() string { return "input_trigger" }

// TriggerSourceCategory groups the per-source enable flags exposed on the
// settings hash. Motion and seatbox get their own categories; other discrete
// inputs split into "buttons" and "handlebar" for coarser-grained control.
type TriggerSourceCategory int

const (
	TriggerCategoryMotion TriggerSourceCategory = iota
	TriggerCategoryButtons
	TriggerCategoryHandlebar
)

func (c TriggerSourceCategory) String() string {
	switch c {
	case TriggerCategoryMotion:
		return "motion"
	case TriggerCategoryButtons:
		return "buttons"
	case TriggerCategoryHandlebar:
		return "handlebar"
	default:
		return "unknown"
	}
}

// TriggerSourceSettingChangedEvent signals that a per-source trigger enable
// flag was toggled via the settings hash.
type TriggerSourceSettingChangedEvent struct {
	Category TriggerSourceCategory
	Enabled  bool
}

func (e TriggerSourceSettingChangedEvent) Type() string { return "trigger_source_setting_changed" }

// SensitivityChangedEvent signals that alarm.sensitivity changed.
type SensitivityChangedEvent struct {
	Sensitivity Sensitivity
}

func (e SensitivityChangedEvent) Type() string { return "sensitivity_changed" }

// RuntimeArmEvent forces the FSM to arm without changing alarm.enabled
type RuntimeArmEvent struct{}

func (e RuntimeArmEvent) Type() string { return "runtime_arm" }

// RuntimeDisarmEvent forces the FSM to disarm without changing alarm.enabled
type RuntimeDisarmEvent struct{}

func (e RuntimeDisarmEvent) Type() string { return "runtime_disarm" }

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

// HibernateAfterWakeTimerEvent signals the post-hibernation-wake cooldown has elapsed
type HibernateAfterWakeTimerEvent struct{}

func (e HibernateAfterWakeTimerEvent) Type() string { return "hibernate_after_wake_timer" }

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
