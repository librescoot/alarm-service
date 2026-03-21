package fsm

import (
	"context"
	"log/slog"
	"sync"

	"github.com/librescoot/librefsm"
)

// StateMachine wraps a librefsm.Machine with alarm-specific state.
type StateMachine struct {
	machine *librefsm.Machine
	log     *slog.Logger

	bmxClient       BMXClient
	publisher       StatusPublisher
	inhibitor       SuspendInhibitor
	alarmController AlarmController

	// FSM-internal state (accessed from the event loop goroutine)
	alarmEnabled    bool
	vehicleStandby  bool
	level2Cycles    int
	initWakeL1      bool
	preSeatboxState librefsm.StateID

	// Settings — written by subscriber goroutines, read by FSM handlers.
	settingsMu          sync.RWMutex
	alarmDuration       int
	hairTriggerEnabled  bool
	hairTriggerDuration int
	l1CooldownDuration  int
}

// New creates a new StateMachine.
func New(
	bmx BMXClient,
	pub StatusPublisher,
	inh SuspendInhibitor,
	alarm AlarmController,
	alarmDuration int,
	log *slog.Logger,
) *StateMachine {
	sm := &StateMachine{
		log:                 log,
		bmxClient:           bmx,
		publisher:           pub,
		inhibitor:           inh,
		alarmController:     alarm,
		alarmDuration:       alarmDuration,
		hairTriggerDuration: 3,
		l1CooldownDuration:  5,
	}

	def := buildDefinition(sm)

	machine, err := def.Build(
		librefsm.WithLogger(log),
		librefsm.WithEventQueueSize(100),
		librefsm.WithStateChangeCallback(sm.onStateChange),
	)
	if err != nil {
		log.Error("failed to build FSM", "error", err)
		panic("failed to build alarm FSM: " + err.Error())
	}

	sm.machine = machine
	return sm
}

// Run starts the FSM event loop. Blocks until ctx is cancelled.
func (sm *StateMachine) Run(ctx context.Context) {
	if err := sm.machine.Start(ctx); err != nil {
		sm.log.Error("failed to start FSM", "error", err)
		return
	}

	<-ctx.Done()
	sm.machine.Stop()
	sm.log.Info("state machine stopped")
}

// SendEvent sends an event to the FSM.
func (sm *StateMachine) SendEvent(event Event) {
	sm.machine.Send(event.ToLibreFSM())
}

// State returns the current state.
func (sm *StateMachine) State() librefsm.StateID {
	return sm.machine.CurrentState()
}

// RuntimeArm implements alarm.RuntimeCommander.
func (sm *StateMachine) RuntimeArm() {
	sm.machine.Send(librefsm.Event{ID: EvRuntimeArm})
}

// RuntimeDisarm implements alarm.RuntimeCommander.
func (sm *StateMachine) RuntimeDisarm() {
	sm.machine.Send(librefsm.Event{ID: EvRuntimeDisarm})
}

// --- Settings mutators (called from subscriber goroutines) ---

func (sm *StateMachine) SetHornEnabled(enabled bool) {
	sm.alarmController.SetHornEnabled(enabled)
}

func (sm *StateMachine) SetAlarmDuration(duration int) {
	sm.settingsMu.Lock()
	sm.alarmDuration = duration
	sm.settingsMu.Unlock()
	sm.log.Info("alarm duration updated", "duration", duration)
}

func (sm *StateMachine) SetHairTriggerEnabled(enabled bool) {
	sm.settingsMu.Lock()
	sm.hairTriggerEnabled = enabled
	sm.settingsMu.Unlock()
	sm.log.Info("hair trigger setting updated", "enabled", enabled)
}

func (sm *StateMachine) SetHairTriggerDuration(duration int) {
	sm.settingsMu.Lock()
	sm.hairTriggerDuration = duration
	sm.settingsMu.Unlock()
	sm.log.Info("hair trigger duration updated", "duration", duration)
}

func (sm *StateMachine) SetL1CooldownDuration(duration int) {
	sm.settingsMu.Lock()
	sm.l1CooldownDuration = duration
	sm.settingsMu.Unlock()
	sm.log.Info("L1 cooldown duration updated", "duration", duration)
}

// --- Settings accessors (called from FSM handlers) ---

func (sm *StateMachine) getAlarmDuration() int {
	sm.settingsMu.RLock()
	defer sm.settingsMu.RUnlock()
	return sm.alarmDuration
}

func (sm *StateMachine) getHairTriggerEnabled() bool {
	sm.settingsMu.RLock()
	defer sm.settingsMu.RUnlock()
	return sm.hairTriggerEnabled
}

func (sm *StateMachine) getHairTriggerDuration() int {
	sm.settingsMu.RLock()
	defer sm.settingsMu.RUnlock()
	return sm.hairTriggerDuration
}

func (sm *StateMachine) getL1CooldownDuration() int {
	sm.settingsMu.RLock()
	defer sm.settingsMu.RUnlock()
	return sm.l1CooldownDuration
}

// --- State change callback ---

func (sm *StateMachine) onStateChange(from, to librefsm.StateID) {
	status := stateToStatus(to)
	if err := sm.publisher.PublishStatus(status); err != nil {
		sm.log.Error("failed to publish status", "error", err)
	}
}

func stateToStatus(state librefsm.StateID) string {
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

// --- Event adapter ---

// Event wraps librefsm event creation for subscribers.
type Event struct {
	id      librefsm.EventID
	payload any
}

func (e Event) ToLibreFSM() librefsm.Event {
	return librefsm.Event{ID: e.id, Payload: e.payload}
}

func (e Event) Type() string { return string(e.id) }

// Event constructors

func NewInitCompleteEvent() Event { return Event{id: EvInitComplete} }
func NewAlarmModeChangedEvent(enabled bool) Event {
	if enabled {
		return Event{id: EvAlarmEnabled}
	}
	return Event{id: EvAlarmDisabled}
}
func NewVehicleStateChangedEvent(state VehicleState) Event { return Event{id: EvVehicleState, payload: state} }
func NewBMXInterruptEvent(data string) Event               { return Event{id: EvBMXInterrupt, payload: data} }
func NewSeatboxOpenedEvent() Event                         { return Event{id: EvSeatboxOpened} }
func NewSeatboxClosedEvent() Event                         { return Event{id: EvSeatboxClosed} }
func NewUnauthorizedSeatboxEvent() Event                   { return Event{id: EvUnauthorizedSeatbox} }
