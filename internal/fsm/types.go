package fsm

import (
	"context"
	"time"

	"github.com/librescoot/librefsm"
)

// FSM state IDs
const (
	StateStarting          librefsm.StateID = "starting"
	StateWaitingEnabled    librefsm.StateID = "waiting_enabled"
	StateDisarmed          librefsm.StateID = "disarmed"
	StateDelayArmed        librefsm.StateID = "delay_armed"
	StateArmed             librefsm.StateID = "armed"
	StateTriggerLevel1Wait librefsm.StateID = "trigger_level_1_wait"
	StateTriggerLevel1     librefsm.StateID = "trigger_level_1"
	StateTriggerLevel2     librefsm.StateID = "trigger_level_2"
	StateWaitingMovement   librefsm.StateID = "waiting_movement"
	StateSeatboxAccess     librefsm.StateID = "seatbox_access"
)

// FSM event IDs
const (
	EvInitComplete       librefsm.EventID = "init_complete"
	EvAlarmEnabled       librefsm.EventID = "alarm_enabled"
	EvAlarmDisabled      librefsm.EventID = "alarm_disabled"
	EvVehicleState       librefsm.EventID = "vehicle_state"
	EvBMXInterrupt       librefsm.EventID = "bmx_interrupt"
	EvRuntimeArm         librefsm.EventID = "runtime_arm"
	EvRuntimeDisarm      librefsm.EventID = "runtime_disarm"
	EvManualTrigger      librefsm.EventID = "manual_trigger"
	EvSeatboxOpened      librefsm.EventID = "seatbox_opened"
	EvSeatboxClosed      librefsm.EventID = "seatbox_closed"
	EvUnauthorizedSeatbox librefsm.EventID = "unauthorized_seatbox"

	// Timer events
	EvDelayArmedTimeout  librefsm.EventID = "delay_armed_timeout"
	EvL1CooldownTimeout  librefsm.EventID = "l1_cooldown_timeout"
	EvL1CheckTimeout     librefsm.EventID = "l1_check_timeout"
	EvL2CheckTimeout     librefsm.EventID = "l2_check_timeout"
	EvChipSetupTimeout   librefsm.EventID = "chip_setup_timeout"
	EvWaitingTimeout     librefsm.EventID = "waiting_timeout"
)

// Timer names
const (
	TimerDelayArmed  = "delay_armed"
	TimerL1Cooldown  = "l1_cooldown"
	TimerL1Check     = "l1_check"
	TimerL2Check     = "l2_check"
	TimerChipSetup   = "chip_setup"
	TimerWaiting     = "waiting"
)

// InterruptPin represents BMX interrupt pin
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

// SensorConfig mirrors hwbmx.SensorConfig at the FSM layer.
type SensorConfig struct {
	AnyMotion bool
	Bandwidth byte
	Threshold byte
	Duration  byte
}

// Per-state sensor configurations
var (
	sensorIdle    = SensorConfig{AnyMotion: false, Bandwidth: 0x08, Threshold: 0x14, Duration: 0x02}
	sensorArmed   = SensorConfig{AnyMotion: true, Bandwidth: 0x0A, Threshold: 0x04, Duration: 0x01}
	sensorLevel1  = SensorConfig{AnyMotion: false, Bandwidth: 0x09, Threshold: 0x08, Duration: 0x03}
	sensorWaiting = SensorConfig{AnyMotion: false, Bandwidth: 0x08, Threshold: 0x06, Duration: 0x03}
)

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

// BMXClient interface for BMX commands
type BMXClient interface {
	ConfigureSensor(ctx context.Context, cfg SensorConfig) error
	SetInterruptPin(ctx context.Context, pin InterruptPin) error
	SoftReset(ctx context.Context) error
	EnableInterrupt(ctx context.Context) error
	DisableInterrupt(ctx context.Context) error
	CheckInterruptStatus(ctx context.Context) (bool, error)
}

// StatusPublisher interface for publishing alarm status
type StatusPublisher interface {
	PublishStatus(status string) error
}

// SuspendInhibitor interface for managing wake locks
type SuspendInhibitor interface {
	Acquire(reason string) error
	Release() error
}

// AlarmController interface for horn and hazard lights
type AlarmController interface {
	Start(duration time.Duration) error
	Stop() error
	SetHornEnabled(enabled bool)
	BlinkHazards() error
}

// Sensitivity represents BMX sensitivity levels (kept for compatibility)
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
