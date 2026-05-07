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

	motion          MotionRPC
	publisher       StatusPublisher
	inhibitor       SuspendInhibitor
	alarmController AlarmController
	powerCommander  PowerCommander

	timers              map[string]*time.Timer
	alarmEnabled        bool
	vehicleStandby      bool
	level2Cycles        int
	requestDisarm       bool
	alarmDuration       int
	hairTriggerEnabled  bool
	hairTriggerDuration int
	l1CooldownDuration  int
	preSeatboxState     State
	seatboxLockClosed   bool
	wakeFromHibernation bool // woken from hibernation by motion (motion-service stamp or live event)
	hibernationImminent bool // pm-service signalled hibernation is imminent or in progress
}

// MotionRPC is the synchronous motion-service interface alarm-service needs:
// the chip-config-confirmed handshake before pm-service is allowed to suspend.
// Steady-state arm/disarm flows reactively through the alarm hash that
// motion-service watches — no synchronous Call required for those.
type MotionRPC interface {
	PrepareHibernation(ctx context.Context) error
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
	motion MotionRPC,
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
		motion:              motion,
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
		if e.Imminent && sm.state == StateArmed {
			sm.confirmHibernationProfile(ctx)
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

// confirmHibernationProfile is the synchronous handshake that gates pm-service's
// suspend on motion-service having the chip in armed-hibernation profile. Called
// when hibernationImminent flips to true while we're in StateArmed. Steady-state
// arm/disarm/L1/L2 transitions don't need this — motion-service watches the alarm
// hash and reconfigures reactively. This is the one synchronous point: we have
// to be sure the chip is right before pm-service kills the MDB.
func (sm *StateMachine) confirmHibernationProfile(ctx context.Context) {
	sm.log.Info("requesting motion-service prepare-hibernation")
	if err := sm.motion.PrepareHibernation(ctx); err != nil {
		// Keep the inhibitor held — pm-service must not be allowed to suspend
		// with an unverified chip profile. Ops will see the error in journal
		// and either restart motion-service or override.
		sm.log.Error("prepare-hibernation failed; holding pm-inhibitor to block suspend", "error", err)
		if err := sm.inhibitor.Acquire("Motion-service prepare-hibernation failed"); err != nil {
			sm.log.Error("failed to acquire suspend inhibitor", "error", err)
		}
		return
	}
	sm.log.Info("motion-service confirmed armed-hibernation profile")
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
