package fsm

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// State represents an alarm state
type State int

const (
	StateInit State = iota
	StateWaitingEnabled
	StateDisarmed
	StateDelayArmed
	StateArmed
	StateTriggerLevel1Wait
	StateTriggerLevel1
	StateTriggerLevel2
	StateWaitingMovement
	StateSeatboxAccess
)

func (s State) String() string {
	return []string{
		"init",
		"waiting_enabled",
		"disarmed",
		"delay_armed",
		"armed",
		"trigger_level_1_wait",
		"trigger_level_1",
		"trigger_level_2",
		"waiting_movement",
		"seatbox_access",
	}[s]
}

// Sensitivity represents BMX sensitivity levels
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

// maxLevel2Cycles caps how many TriggerLevel2/WaitingMovement cycles a single
// alarm episode runs before bailing out into Disarmed. With ~50s per cycle
// state, this targets roughly 10 minutes of alarm before the safety valve trips.
const maxLevel2Cycles = 6

// StateMachine implements the alarm FSM
type StateMachine struct {
	mu     sync.RWMutex
	state  State
	events chan Event
	log    *slog.Logger
	ctx    context.Context

	bmxClient        BMXClient
	publisher        StatusPublisher
	inhibitor        SuspendInhibitor
	alarmController  AlarmController
	powerCommander   PowerCommander

	timers               map[string]*time.Timer
	alarmEnabled         bool
	vehicleStandby       bool
	level2Cycles         int
	requestDisarm        bool
	alarmDuration        int
	hairTriggerEnabled   bool
	hairTriggerDuration  int
	l1CooldownDuration   int
	preSeatboxState      State
	seatboxLockClosed    bool
	initWakeL1           bool // L1 triggered from stale BMX latch during startup
	wakeFromHibernation  bool // woken from hibernation by nRF52 accelerometer
	hibernationImminent  bool // pm-service signalled hibernation is imminent or in progress
}

// SensorConfig mirrors hwbmx.SensorConfig at the FSM layer to avoid an import cycle.
type SensorConfig struct {
	AnyMotion bool // true = any-motion engine; false = slow-motion engine
	Bandwidth byte // PMU_BW register value; 0x08=7.81Hz, 0x09=15.63Hz, 0x0A=31.25Hz
	Threshold byte // 1 LSB = 3.91 mg in 2g range
	Duration  byte // N = dur+1 consecutive samples
}

// Per-state sensor configurations.
var (
	// sensorIdle: low-BW slow-motion used in init/delay/disarmed states (interrupt disabled).
	sensorIdle = SensorConfig{AnyMotion: false, Bandwidth: 0x08, Threshold: 0x14, Duration: 0x02}

	// sensorArmed: any-motion at 31.25 Hz — awake-armed profile, requires 64 ms of
	// sustained slope above ~23 mg. Catches contact (hand, kid, dog leaning) while
	// still rejecting brief vibration spikes from passing trucks/trams.
	sensorArmed = SensorConfig{AnyMotion: true, Bandwidth: 0x0A, Threshold: 0x06, Duration: 0x03}

	// sensorArmedHibernation: any-motion at 31.25 Hz — hibernation-armed profile,
	// requires 64 ms of sustained slope above ~31 mg. Programmed into the BMX just
	// before hibernation so the wake threshold survives the MDB power-down. Stricter
	// than the awake profile so urban environmental vibration doesn't wake the MDB.
	sensorArmedHibernation = SensorConfig{AnyMotion: true, Bandwidth: 0x0A, Threshold: 0x08, Duration: 0x03}

	// sensorLevel1: slow-motion at 15.63 Hz — confirms deliberate push/tilt (~256 ms).
	sensorLevel1 = SensorConfig{AnyMotion: false, Bandwidth: 0x09, Threshold: 0x08, Duration: 0x03}

	// sensorWaiting: slow-motion at 7.81 Hz — conservative re-trigger for L2 (~512 ms).
	sensorWaiting = SensorConfig{AnyMotion: false, Bandwidth: 0x08, Threshold: 0x06, Duration: 0x03}
)

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

// PowerCommander interface for sending power state commands
type PowerCommander interface {
	RequestHibernate() error
}

// AlarmController interface for horn and hazard lights
type AlarmController interface {
	Start(duration time.Duration) error
	Stop() error
	SetHornEnabled(enabled bool)
	BlinkHazards() error
}

// New creates a new StateMachine
func New(
	bmx BMXClient,
	pub StatusPublisher,
	inh SuspendInhibitor,
	alarm AlarmController,
	power PowerCommander,
	alarmDuration int,
	log *slog.Logger,
) *StateMachine {
	return &StateMachine{
		state:               StateInit,
		events:              make(chan Event, 100),
		log:                 log,
		bmxClient:           bmx,
		publisher:           pub,
		inhibitor:           inh,
		alarmController:     alarm,
		powerCommander:      power,
		timers:              make(map[string]*time.Timer),
		alarmEnabled:        false,
		vehicleStandby:      false,
		level2Cycles:        0,
		requestDisarm:       false,
		alarmDuration:       alarmDuration,
		hairTriggerEnabled:  false,
		hairTriggerDuration: 3,
		l1CooldownDuration:  5,
		preSeatboxState:     StateInit,
		seatboxLockClosed:   true,
	}
}

// Run runs the state machine event loop
func (sm *StateMachine) Run(ctx context.Context) {
	sm.log.Info("starting state machine")
	sm.ctx = ctx

	for {
		select {
		case event := <-sm.events:
			sm.handleEvent(ctx, event)

		case <-ctx.Done():
			sm.log.Info("state machine stopped")
			sm.cleanupTimers()
			return
		}
	}
}

// SendEvent sends an event to the state machine
func (sm *StateMachine) SendEvent(event Event) {
	select {
	case sm.events <- event:
	default:
		sm.log.Warn("event queue full, dropping event", "type", event.Type())
	}
}

// RuntimeArm implements alarm.RuntimeCommander — forces arming without changing alarm.enabled
func (sm *StateMachine) RuntimeArm() { sm.SendEvent(RuntimeArmEvent{}) }

// RuntimeDisarm implements alarm.RuntimeCommander — forces disarming without changing alarm.enabled
func (sm *StateMachine) RuntimeDisarm() { sm.SendEvent(RuntimeDisarmEvent{}) }

// State returns the current state
func (sm *StateMachine) State() State {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state
}

// handleEvent processes an event
func (sm *StateMachine) handleEvent(ctx context.Context, event Event) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if e, ok := event.(HornSettingChangedEvent); ok {
		sm.alarmController.SetHornEnabled(e.Enabled)
		return
	}

	if e, ok := event.(AlarmDurationChangedEvent); ok {
		sm.alarmDuration = e.Duration
		sm.log.Info("alarm duration updated", "duration", e.Duration)
		return
	}

	if e, ok := event.(HairTriggerSettingChangedEvent); ok {
		sm.hairTriggerEnabled = e.Enabled
		sm.log.Info("hair trigger setting updated", "enabled", e.Enabled)
		return
	}

	if e, ok := event.(HairTriggerDurationChangedEvent); ok {
		sm.hairTriggerDuration = e.Duration
		sm.log.Info("hair trigger duration updated", "duration", e.Duration)
		return
	}

	if e, ok := event.(L1CooldownDurationChangedEvent); ok {
		sm.l1CooldownDuration = e.Duration
		sm.log.Info("L1 cooldown duration updated", "duration", e.Duration)
		return
	}

	if e, ok := event.(HibernationImminentEvent); ok {
		if sm.hibernationImminent == e.Imminent {
			return
		}
		sm.hibernationImminent = e.Imminent
		sm.log.Info("hibernation-imminent flag updated", "imminent", e.Imminent)
		if sm.state == StateArmed {
			sm.reprogramArmed(ctx)
		}
		return
	}

	if _, ok := event.(HibernateAfterWakeTimerEvent); ok {
		if sm.state == StateArmed && sm.wakeFromHibernation && sm.vehicleStandby {
			sm.wakeFromHibernation = false
			sm.log.Info("hibernate cooldown elapsed, requesting re-hibernate")
			if err := sm.powerCommander.RequestHibernate(); err != nil {
				sm.log.Error("failed to request hibernation", "error", err)
			}
		}
		return
	}

	if _, ok := event.(PostAlarmCooldownTimerEvent); ok {
		if sm.state != StateDisarmed || !sm.alarmEnabled || !sm.vehicleStandby {
			return
		}
		if sm.wakeFromHibernation {
			// Transition into StateArmed so the BMX is properly configured for
			// motion detection (and the nRF52 has something to wake on once we
			// hibernate), then request hibernate. Clear the flag first so
			// onEnterArmed doesn't start another 5-min cooldown.
			sm.wakeFromHibernation = false
			sm.log.Info("post-alarm cooldown elapsed, arming and requesting re-hibernate")
			sm.exitState(ctx, StateDisarmed)
			sm.state = StateArmed
			sm.enterState(ctx, StateArmed)
			sm.publishCurrentStatus()
			if err := sm.powerCommander.RequestHibernate(); err != nil {
				sm.log.Error("failed to request hibernation", "error", err)
			}
			return
		}
		// Fall through to normal transition handling so the FSM re-arms via
		// the StateDisarmed handler below (Disarmed → DelayArmed → Armed).
	}

	oldState := sm.state
	sm.log.Debug("handling event",
		"event", event.Type(),
		"state", oldState.String())

	newState := sm.getTransition(event)

	if newState != oldState {
		// Blink hazards when movement detected during L1 (before L2 activation)
		if oldState == StateTriggerLevel1 && newState == StateTriggerLevel2 {
			if _, ok := event.(BMXInterruptEvent); ok {
				sm.log.Info("movement detected during L1, blinking hazards")
				if err := sm.alarmController.BlinkHazards(); err != nil {
					sm.log.Error("failed to blink hazards", "error", err)
				}
			}
		}

		sm.exitState(ctx, oldState)
		sm.state = newState
		sm.log.Info("state transition",
			"from", oldState.String(),
			"to", newState.String(),
			"event", event.Type())
		sm.enterState(ctx, newState)
		sm.publishCurrentStatus()
	}
}

// publishCurrentStatus publishes the current alarm status
func (sm *StateMachine) publishCurrentStatus() {
	status := sm.stateToStatus(sm.state)
	if err := sm.publisher.PublishStatus(status); err != nil {
		sm.log.Error("failed to publish status", "error", err)
	}
}

// stateToStatus converts state to status string
func (sm *StateMachine) stateToStatus(state State) string {
	switch state {
	case StateWaitingEnabled:
		return "disabled"
	case StateDisarmed:
		return "disarmed"
	case StateDelayArmed:
		return "delay-armed"
	case StateArmed:
		return "armed"
	case StateTriggerLevel1Wait, StateTriggerLevel1:
		return "level-1-triggered"
	case StateTriggerLevel2, StateWaitingMovement:
		return "level-2-triggered"
	case StateSeatboxAccess:
		return "seatbox-access"
	default:
		return "unknown"
	}
}

// armedSensorConfig returns the sensor profile to use in the armed state, based on
// whether pm-service has signalled an imminent hibernation transition.
func (sm *StateMachine) armedSensorConfig() SensorConfig {
	if sm.hibernationImminent {
		return sensorArmedHibernation
	}
	return sensorArmed
}

// reprogramArmed reconfigures the BMX in-place while staying in the armed state.
// Used when the hibernation-imminent flag flips so the registers going into (or
// returning from) hibernation reflect the right profile without an FSM transition.
func (sm *StateMachine) reprogramArmed(ctx context.Context) {
	cfg := sm.armedSensorConfig()
	sm.log.Info("reprogramming armed BMX profile",
		"hibernation", sm.hibernationImminent,
		"bw", cfg.Bandwidth,
		"threshold", cfg.Threshold,
		"duration", cfg.Duration)

	if err := sm.bmxClient.ConfigureSensor(ctx, cfg); err != nil {
		sm.log.Error("failed to reprogram sensor", "error", err)
	}
	if err := sm.bmxClient.EnableInterrupt(ctx); err != nil {
		sm.log.Error("failed to re-enable interrupt", "error", err)
	}
}

// configureBMX sends configuration commands to BMX hardware.
func (sm *StateMachine) configureBMX(ctx context.Context, pin InterruptPin, cfg SensorConfig) {
	if err := sm.bmxClient.SetInterruptPin(ctx, pin); err != nil {
		sm.log.Error("failed to set interrupt pin", "pin", pin, "error", err)
	}

	if err := sm.bmxClient.ConfigureSensor(ctx, cfg); err != nil {
		sm.log.Error("failed to configure sensor", "error", err)
	}

	mode := "slow-motion"
	if cfg.AnyMotion {
		mode = "any-motion"
	}
	sm.log.Info("configured BMX", "pin", pin, "mode", mode, "bw", cfg.Bandwidth, "threshold", cfg.Threshold, "duration", cfg.Duration)
}

// startTimer starts a timer
func (sm *StateMachine) startTimer(name string, duration time.Duration, callback func()) {
	sm.stopTimer(name)

	timer := time.AfterFunc(duration, func() {
		if callback != nil {
			callback()
		}
	})

	sm.timers[name] = timer
	sm.log.Debug("started timer", "name", name, "duration", duration)
}

// stopTimer stops a timer
func (sm *StateMachine) stopTimer(name string) {
	if timer, ok := sm.timers[name]; ok {
		timer.Stop()
		delete(sm.timers, name)
		sm.log.Debug("stopped timer", "name", name)
	}
}

// cleanupTimers stops all timers
func (sm *StateMachine) cleanupTimers() {
	for name := range sm.timers {
		sm.stopTimer(name)
	}
}
