package fsm

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

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

// Bound an alarm episode so a stuck trigger cannot sound indefinitely.
const maxLevel2Cycles = 6

// Vehicle lock actuation creates handlebar edges; keep only those sensors muted
// until its positioning retries finish. Other tamper sources remain live.
const handlebarSettleDelay = 90 * time.Second

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
	// A durable motion wake is consumed once and drives safe re-hibernation.
	wakeFromHibernation bool
	hibernationImminent bool

	// Motion stays in the FSM so its wake stamp survives a disabled trigger.
	motionTriggerEnabled bool

	handlebarSettled bool

	// UMS mass-storage sessions have the rider replugging USB at the MDB,
	// which trips the motion engine while the work is authorized.
	umsActive bool
}

type MotionRPC interface {
	PrepareHibernation(ctx context.Context) error
}

type StatusPublisher interface {
	PublishStatus(status string) error
	PublishTrigger(source string, at time.Time) error
}

type SuspendInhibitor interface {
	Acquire(reason string) error
	Release() error
}

type PowerCommander interface {
	RequestHibernate() error
}

type AlarmController interface {
	Start(duration time.Duration) error
	Stop() error
	SetHornEnabled(enabled bool)
	BlinkHazards() error
}

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
		l1CooldownDuration:  15,
		preSeatboxState:     StateInit,
		seatboxLockClosed:   true,

		motionTriggerEnabled: true,

		handlebarSettled: true,
		umsActive:        false,
	}
}

func isTamperTrigger(e Event) bool {
	switch e.(type) {
	case BMXInterruptEvent, InputTriggerEvent:
		return true
	}
	return false
}

func triggerSourceOf(e Event) (string, bool) {
	switch ev := e.(type) {
	case InputTriggerEvent:
		return ev.Source.String(), true
	case BMXInterruptEvent:
		return "motion", true
	case UnauthorizedSeatboxEvent:
		return "seatbox", true
	}
	return "", false
}

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

func (sm *StateMachine) SendEvent(event Event) {
	select {
	case sm.events <- event:
	default:
		sm.log.Warn("event queue full, dropping event", "type", event.Type())
	}
}

func (sm *StateMachine) RuntimeArm() { sm.SendEvent(RuntimeArmEvent{}) }

func (sm *StateMachine) RuntimeDisarm() { sm.SendEvent(RuntimeDisarmEvent{}) }

func (sm *StateMachine) State() State {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.state
}

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

	if e, ok := event.(MotionTriggerSettingChangedEvent); ok {
		sm.motionTriggerEnabled = e.Enabled
		sm.log.Info("motion trigger source updated", "enabled", e.Enabled)
		return
	}

	if e, ok := event.(UMSModeChangedEvent); ok {
		if sm.umsActive == e.Active {
			return
		}
		sm.umsActive = e.Active
		sm.log.Info("USB mass-storage mode changed", "active", e.Active)
		// Fall through: the clamp below forces disarmed on entry, and the
		// disarmed transition rule re-arms on exit.
	}

	if _, ok := event.(HandlebarSettleTimerEvent); ok {
		sm.handlebarSettled = true
		sm.log.Debug("handlebar settling window elapsed, handlebar triggers live again")
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
			// Enter Armed first so motion hardware is ready for the next wake.
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

	}

	// Disabled motion must not alarm, but a hibernation wake still needs its
	// cooldown bookkeeping.
	if be, ok := event.(BMXInterruptEvent); ok && !sm.motionTriggerEnabled {
		if be.Data == "wake-hibernation" {
			sm.wakeFromHibernation = true
		}
		sm.log.Debug("dropping motion event, motion trigger source disabled", "data", be.Data)
		return
	}

	// Drop, rather than defer, stale edges from the vehicle's own lock cycle.
	if e, ok := event.(InputTriggerEvent); ok && e.Source.isHandlebar() && !sm.handlebarSettled {
		sm.log.Debug("dropping handlebar trigger, still inside the post-arm settling window",
			"source", e.Source.String())
		return
	}

	oldState := sm.state
	sm.log.Debug("handling event",
		"event", event.Type(),
		"state", oldState.String())

	newState := sm.getTransition(event)

	// Nothing may arm or escalate while the rider is handling the USB cable.
	if sm.umsActive && newState != StateInit && newState != StateDisarmed && newState != StateWaitingEnabled {
		sm.log.Info("suppressing alarm state change, USB mass-storage mode active",
			"event", event.Type(), "from", oldState.String(), "requested", newState.String())
		newState = StateDisarmed
	}

	if newState != oldState {

		if oldState == StateTriggerLevel1 && newState == StateTriggerLevel2 && isTamperTrigger(event) {
			sm.log.Info("tampering detected during L1, blinking hazards", "event", event.Type())
			if err := sm.alarmController.BlinkHazards(); err != nil {
				sm.log.Error("failed to blink hazards", "error", err)
			}
		}

		sm.exitState(ctx, oldState)
		sm.state = newState
		sm.log.Info("state transition",
			"from", oldState.String(),
			"to", newState.String(),
			"event", event.Type())
		sm.enterState(ctx, newState)

		// Publish provenance before status so observers see a matching source.
		sm.publishTriggerSource(event)
		sm.publishCurrentStatus()
	}
}

func (sm *StateMachine) publishTriggerSource(event Event) {
	source, ok := triggerSourceOf(event)
	if !ok {
		return
	}
	if err := sm.publisher.PublishTrigger(source, time.Now()); err != nil {
		sm.log.Error("failed to publish trigger source", "source", source, "error", err)
	}
}

func (sm *StateMachine) publishCurrentStatus() {
	status := sm.stateToStatus(sm.state)
	if err := sm.publisher.PublishStatus(status); err != nil {
		sm.log.Error("failed to publish status", "error", err)
	}
}

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

// confirmHibernationProfile synchronously gates suspend on the stricter motion
// profile; reactive profile updates are insufficient once power is removed.
func (sm *StateMachine) confirmHibernationProfile(ctx context.Context) {
	sm.log.Info("requesting motion-service prepare-hibernation")
	if err := sm.motion.PrepareHibernation(ctx); err != nil {
		// Keep suspend blocked when the chip profile cannot be verified.
		sm.log.Error("prepare-hibernation failed; holding pm-inhibitor to block suspend", "error", err)
		if err := sm.inhibitor.Acquire("Motion-service prepare-hibernation failed"); err != nil {
			sm.log.Error("failed to acquire suspend inhibitor", "error", err)
		}
		return
	}
	sm.log.Info("motion-service confirmed armed-hibernation profile")
}

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

func (sm *StateMachine) stopTimer(name string) {
	if timer, ok := sm.timers[name]; ok {
		timer.Stop()
		delete(sm.timers, name)
		sm.log.Debug("stopped timer", "name", name)
	}
}

func (sm *StateMachine) cleanupTimers() {
	for name := range sm.timers {
		sm.stopTimer(name)
	}
}
