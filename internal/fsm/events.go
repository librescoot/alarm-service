package fsm

type Event interface {
	Type() string
}

type InitCompleteEvent struct{}

func (e InitCompleteEvent) Type() string { return "init_complete" }

type AlarmModeChangedEvent struct {
	Enabled bool
}

func (e AlarmModeChangedEvent) Type() string { return "alarm_mode_changed" }

type HornSettingChangedEvent struct {
	Enabled bool
}

func (e HornSettingChangedEvent) Type() string { return "horn_setting_changed" }

type AlarmDurationChangedEvent struct {
	Duration int
}

func (e AlarmDurationChangedEvent) Type() string { return "alarm_duration_changed" }

type HairTriggerSettingChangedEvent struct {
	Enabled bool
}

func (e HairTriggerSettingChangedEvent) Type() string { return "hair_trigger_setting_changed" }

type HairTriggerDurationChangedEvent struct {
	Duration int
}

func (e HairTriggerDurationChangedEvent) Type() string { return "hair_trigger_duration_changed" }

type L1CooldownDurationChangedEvent struct {
	Duration int
}

func (e L1CooldownDurationChangedEvent) Type() string { return "l1_cooldown_duration_changed" }

type VehicleStateChangedEvent struct {
	State VehicleState
}

func (e VehicleStateChangedEvent) Type() string { return "vehicle_state_changed" }

type BMXInterruptEvent struct {
	Timestamp int64
	Data      string
}

func (e BMXInterruptEvent) Type() string { return "bmx_interrupt" }

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

func (s TriggerSource) isHandlebar() bool {
	return s == TriggerSourceHandlebarLock || s == TriggerSourceHandlebarPosition
}

type InputTriggerEvent struct {
	Source TriggerSource
}

func (e InputTriggerEvent) Type() string { return "input_trigger" }

type MotionTriggerSettingChangedEvent struct {
	Enabled bool
}

func (e MotionTriggerSettingChangedEvent) Type() string { return "motion_trigger_setting_changed" }

type UMSModeChangedEvent struct {
	Active bool
}

func (e UMSModeChangedEvent) Type() string { return "ums_mode_changed" }

type RuntimeArmEvent struct{}

func (e RuntimeArmEvent) Type() string { return "runtime_arm" }

type RuntimeDisarmEvent struct{}

func (e RuntimeDisarmEvent) Type() string { return "runtime_disarm" }

type DelayArmedTimerEvent struct{}

func (e DelayArmedTimerEvent) Type() string { return "delay_armed_timer" }

type Level1TriggerDelayTimerEvent struct{}

func (e Level1TriggerDelayTimerEvent) Type() string { return "level1_trigger_delay_timer" }

type Level1CooldownTimerEvent struct{}

func (e Level1CooldownTimerEvent) Type() string { return "level1_cooldown_timer" }

type Level1CheckTimerEvent struct{}

func (e Level1CheckTimerEvent) Type() string { return "level1_check_timer" }

type Level2CheckTimerEvent struct{}

func (e Level2CheckTimerEvent) Type() string { return "level2_check_timer" }

type HandlebarSettleTimerEvent struct{}

func (e HandlebarSettleTimerEvent) Type() string { return "handlebar_settle_timer" }

type HibernateAfterWakeTimerEvent struct{}

func (e HibernateAfterWakeTimerEvent) Type() string { return "hibernate_after_wake_timer" }

type PostAlarmCooldownTimerEvent struct{}

func (e PostAlarmCooldownTimerEvent) Type() string { return "post_alarm_cooldown_timer" }

type HibernationImminentEvent struct {
	Imminent bool
}

func (e HibernationImminentEvent) Type() string { return "hibernation_imminent" }

type ManualTriggerEvent struct {
	Duration int
}

func (e ManualTriggerEvent) Type() string { return "manual_trigger" }

type SeatboxOpenedEvent struct{}

func (e SeatboxOpenedEvent) Type() string { return "seatbox_opened" }

type SeatboxClosedEvent struct{}

func (e SeatboxClosedEvent) Type() string { return "seatbox_closed" }

type UnauthorizedSeatboxEvent struct{}

func (e UnauthorizedSeatboxEvent) Type() string { return "unauthorized_seatbox" }

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
