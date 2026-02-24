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

// StateMachine implements the alarm FSM
type StateMachine struct {
	mu     sync.RWMutex
	state  State
	events chan Event
	log    *slog.Logger
	ctx    context.Context

	bmxClient       BMXClient
	publisher       StatusPublisher
	inhibitor       SuspendInhibitor
	alarmController AlarmController

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
}

// BMXClient interface for BMX commands
type BMXClient interface {
	SetSensitivity(ctx context.Context, sens Sensitivity) error
	SetInterruptPin(ctx context.Context, pin InterruptPin) error
	SoftReset(ctx context.Context) error
	EnableInterrupt(ctx context.Context) error
	DisableInterrupt(ctx context.Context) error
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

// New creates a new StateMachine
func New(
	bmx BMXClient,
	pub StatusPublisher,
	inh SuspendInhibitor,
	alarm AlarmController,
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

// configureBMX sends configuration commands to BMX service
func (sm *StateMachine) configureBMX(ctx context.Context, pin InterruptPin, sens Sensitivity) {
	if err := sm.bmxClient.SetInterruptPin(ctx, pin); err != nil {
		sm.log.Error("failed to set interrupt pin", "pin", pin, "error", err)
	}

	if err := sm.bmxClient.SetSensitivity(ctx, sens); err != nil {
		sm.log.Error("failed to set sensitivity", "sensitivity", sens, "error", err)
	}

	sm.log.Info("configured BMX", "pin", pin, "sensitivity", sens)
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
