package fsm

import (
	"context"
	"time"
)

// State entry handlers. After Phase 4 these no longer touch the BMX055
// directly — chip configuration is reactive in motion-service, which
// watches the `alarm` hash that we publish via publishCurrentStatus().
// Each state-transition publish takes ~50 ms (HashWatcher debounce) +
// ~150 ms (controller.Apply) before the chip is in the new profile;
// fine for arm/disarm/L1/L2. Hibernation entry is the exception — it's
// gated synchronously on motion-service confirming the armed-hibernation
// profile, see confirmHibernationProfile().

// onEnterInit handles entry to init state.
func (sm *StateMachine) onEnterInit(ctx context.Context) {
	sm.log.Info("entering init state")
}

// onEnterWaitingEnabled handles entry to waiting_enabled state.
func (sm *StateMachine) onEnterWaitingEnabled(ctx context.Context) {
	sm.log.Info("entering waiting_enabled state")
	sm.inhibitor.Release()
	sm.level2Cycles = 0
	sm.wakeFromHibernation = false
}

// onEnterDisarmed handles entry to disarmed state.
func (sm *StateMachine) onEnterDisarmed(ctx context.Context) {
	sm.log.Info("entering disarmed state")
	sm.inhibitor.Release()
	sm.level2Cycles = 0

	// If we got here with the vehicle still in stand-by, this is the L2-exhaustion
	// path: the alarm gave up after a long blare. Start a quiet window before
	// re-arming (or handing back to nRF52 hibernation), so a stuck/false trigger
	// can't blare all night and a thief can't simply wait it out.
	if sm.vehicleStandby && sm.alarmEnabled {
		sm.log.Info("post-alarm cooldown started", "duration", "5m", "wake_from_hibernation", sm.wakeFromHibernation)
		sm.startTimer("post_alarm_cooldown", 5*time.Minute, func() {
			sm.SendEvent(PostAlarmCooldownTimerEvent{})
		})
	} else {
		sm.wakeFromHibernation = false
	}
}

// onExitDisarmed handles exit from disarmed state.
func (sm *StateMachine) onExitDisarmed(_ context.Context) {
	sm.stopTimer("post_alarm_cooldown")
}

// onEnterDelayArmed handles entry to delay_armed state.
func (sm *StateMachine) onEnterDelayArmed(ctx context.Context) {
	sm.log.Info("entering delay_armed state", "duration", "5s")

	if err := sm.inhibitor.Acquire("Arming alarm"); err != nil {
		sm.log.Error("failed to acquire inhibitor", "error", err)
	}

	sm.startTimer("delay_armed", 5*time.Second, func() {
		sm.SendEvent(DelayArmedTimerEvent{})
	})

	sm.level2Cycles = 0
	sm.requestDisarm = false
}

// onExitDelayArmed handles exit from delay_armed state.
func (sm *StateMachine) onExitDelayArmed(ctx context.Context) {
	sm.stopTimer("delay_armed")
}

// onEnterArmed handles entry to armed state.
func (sm *StateMachine) onEnterArmed(ctx context.Context) {
	sm.log.Info("entering armed state", "hibernation_imminent", sm.hibernationImminent)

	sm.inhibitor.Release()

	// If pm-service already signalled hibernation-imminent before we got
	// here, perform the synchronous prepare-hibernation handshake now —
	// without it, motion-service might still be programming the
	// armed-hibernation profile when the system suspends.
	if sm.hibernationImminent {
		sm.confirmHibernationProfile(ctx)
	}

	// If we were woken from hibernation and vehicle is still in stand-by, start a
	// cooldown timer. After 5 minutes with no further triggers, re-hibernate.
	if sm.wakeFromHibernation && sm.vehicleStandby {
		sm.log.Info("armed after hibernation wake, starting re-hibernate cooldown", "duration", "5m")
		sm.startTimer("hibernate_cooldown", 5*time.Minute, func() {
			sm.SendEvent(HibernateAfterWakeTimerEvent{})
		})
	}
}

// onExitArmed handles exit from armed state.
func (sm *StateMachine) onExitArmed(_ context.Context) {
	sm.stopTimer("hibernate_cooldown")
}

// onEnterTriggerLevel1Wait handles entry to trigger_level_1_wait state.
func (sm *StateMachine) onEnterTriggerLevel1Wait(ctx context.Context) {
	sm.log.Info("entering trigger_level_1_wait state", "cooldown", sm.l1CooldownDuration)

	if err := sm.inhibitor.Acquire("Level 1 cooldown"); err != nil {
		sm.log.Error("failed to acquire inhibitor", "error", err)
	}

	// Blink hazards once when L1 is first triggered.
	if err := sm.alarmController.BlinkHazards(); err != nil {
		sm.log.Error("failed to blink hazards", "error", err)
	}

	// Skip the hair trigger when we just came up from a hibernation-wake
	// motion edge — that initial edge is the wake event, not a tampering.
	if sm.wakeFromHibernation {
		sm.log.Info("skipping hair trigger on hibernation-wake edge")
	} else if sm.hairTriggerEnabled {
		sm.log.Info("hair trigger active, starting short alarm", "duration", sm.hairTriggerDuration)
		sm.alarmController.Start(time.Duration(sm.hairTriggerDuration) * time.Second)
	}

	sm.startTimer("level1_cooldown", time.Duration(sm.l1CooldownDuration)*time.Second, func() {
		sm.SendEvent(Level1CooldownTimerEvent{})
	})
}

// onExitTriggerLevel1Wait handles exit from trigger_level_1_wait state.
func (sm *StateMachine) onExitTriggerLevel1Wait(ctx context.Context) {
	sm.stopTimer("level1_cooldown")
	sm.alarmController.Stop()
}

// onEnterTriggerLevel1 handles entry to trigger_level_1 state.
func (sm *StateMachine) onEnterTriggerLevel1(ctx context.Context) {
	sm.log.Info("entering trigger_level_1 state", "check_duration", "5s")

	sm.startTimer("level1_check", 5*time.Second, func() {
		sm.SendEvent(Level1CheckTimerEvent{})
	})
}

// onExitTriggerLevel1 handles exit from trigger_level_1 state.
func (sm *StateMachine) onExitTriggerLevel1(ctx context.Context) {
	sm.stopTimer("level1_check")
}

// onEnterTriggerLevel2 handles entry to trigger_level_2 state.
func (sm *StateMachine) onEnterTriggerLevel2(ctx context.Context) {
	sm.log.Info("entering trigger_level_2 state")

	if err := sm.inhibitor.Acquire("Level 2 triggered"); err != nil {
		sm.log.Error("failed to acquire inhibitor", "error", err)
	}

	sm.alarmController.Start(time.Duration(sm.alarmDuration) * time.Second)

	sm.startTimer("level2_check", 50*time.Second, func() {
		sm.SendEvent(Level2CheckTimerEvent{})
	})
}

// onExitTriggerLevel2 handles exit from trigger_level_2 state.
func (sm *StateMachine) onExitTriggerLevel2(ctx context.Context) {
	sm.stopTimer("level2_check")
	sm.alarmController.Stop()
}

// onEnterWaitingMovement handles entry to waiting_movement state.
func (sm *StateMachine) onEnterWaitingMovement(ctx context.Context) {
	sm.log.Info("entering waiting_movement state", "duration", "50s", "cycle", sm.level2Cycles)

	sm.alarmController.Start(time.Duration(sm.alarmDuration) * time.Second)

	sm.startTimer("waiting_movement", 50*time.Second, func() {
		sm.SendEvent(Level2CheckTimerEvent{})
	})
}

// onExitWaitingMovement handles exit from waiting_movement state.
func (sm *StateMachine) onExitWaitingMovement(ctx context.Context) {
	sm.stopTimer("chip_setup")
	sm.stopTimer("waiting_movement")
	sm.alarmController.Stop()
}

// onEnterSeatboxAccess handles entry to seatbox_access state.
func (sm *StateMachine) onEnterSeatboxAccess(ctx context.Context) {
	sm.log.Info("entering seatbox_access state", "previous_state", sm.preSeatboxState.String())

	if err := sm.inhibitor.Acquire("Seatbox access"); err != nil {
		sm.log.Error("failed to acquire inhibitor", "error", err)
	}
}

// onExitSeatboxAccess handles exit from seatbox_access state.
func (sm *StateMachine) onExitSeatboxAccess(ctx context.Context) {
	sm.log.Info("exiting seatbox_access state")
	sm.inhibitor.Release()
}
