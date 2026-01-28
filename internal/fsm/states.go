package fsm

import (
	"context"
	"time"
)

// onEnterInit handles entry to init state
func (sm *StateMachine) onEnterInit(ctx context.Context) {
	sm.log.Info("entering init state")
	sm.configureBMX(ctx, InterruptPinINT2, SensitivityLow)
}

// onEnterWaitingEnabled handles entry to waiting_enabled state
func (sm *StateMachine) onEnterWaitingEnabled(ctx context.Context) {
	sm.log.Info("entering waiting_enabled state")

	if err := sm.bmxClient.SoftReset(ctx); err != nil {
		sm.log.Error("failed to soft reset", "error", err)
	}

	if err := sm.bmxClient.DisableInterrupt(ctx); err != nil {
		sm.log.Error("failed to disable interrupt", "error", err)
	}

	sm.configureBMX(ctx, InterruptPinINT2, SensitivityLow)
	sm.inhibitor.Release()
	sm.level2Cycles = 0
}

// onEnterDisarmed handles entry to disarmed state
func (sm *StateMachine) onEnterDisarmed(ctx context.Context) {
	sm.log.Info("entering disarmed state")

	if err := sm.bmxClient.SoftReset(ctx); err != nil {
		sm.log.Error("failed to soft reset", "error", err)
	}

	if err := sm.bmxClient.DisableInterrupt(ctx); err != nil {
		sm.log.Error("failed to disable interrupt", "error", err)
	}

	sm.configureBMX(ctx, InterruptPinNone, SensitivityLow)
	sm.inhibitor.Release()
	sm.level2Cycles = 0
}

// onEnterDelayArmed handles entry to delay_armed state
func (sm *StateMachine) onEnterDelayArmed(ctx context.Context) {
	sm.log.Info("entering delay_armed state", "duration", "5s")

	if err := sm.inhibitor.Acquire("Arming alarm"); err != nil {
		sm.log.Error("failed to acquire inhibitor", "error", err)
	}

	if err := sm.bmxClient.SoftReset(ctx); err != nil {
		sm.log.Error("failed to soft reset", "error", err)
	}

	sm.configureBMX(ctx, InterruptPinINT2, SensitivityLow)

	sm.startTimer("delay_armed", 5*time.Second, func() {
		sm.SendEvent(DelayArmedTimerEvent{})
	})

	sm.level2Cycles = 0
	sm.requestDisarm = false
}

// onExitDelayArmed handles exit from delay_armed state
func (sm *StateMachine) onExitDelayArmed(ctx context.Context) {
	sm.stopTimer("delay_armed")
}

// onEnterArmed handles entry to armed state
func (sm *StateMachine) onEnterArmed(ctx context.Context) {
	sm.log.Info("entering armed state")

	sm.inhibitor.Release()

	sm.configureBMX(ctx, InterruptPinNone, SensitivityMedium)

	if err := sm.bmxClient.EnableInterrupt(ctx); err != nil {
		sm.log.Error("failed to enable interrupt", "error", err)
	}
}

// onEnterTriggerLevel1Wait handles entry to trigger_level_1_wait state
func (sm *StateMachine) onEnterTriggerLevel1Wait(ctx context.Context) {
	sm.log.Info("entering trigger_level_1_wait state", "cooldown", sm.l1CooldownDuration)

	if err := sm.inhibitor.Acquire("Level 1 cooldown"); err != nil {
		sm.log.Error("failed to acquire inhibitor", "error", err)
	}

	if err := sm.bmxClient.SoftReset(ctx); err != nil {
		sm.log.Error("failed to soft reset", "error", err)
	}

	// Blink hazards once when L1 is first triggered
	if err := sm.alarmController.BlinkHazards(); err != nil {
		sm.log.Error("failed to blink hazards", "error", err)
	}

	if sm.hairTriggerEnabled {
		sm.log.Info("hair trigger active, starting short alarm", "duration", sm.hairTriggerDuration)
		sm.alarmController.Start(time.Duration(sm.hairTriggerDuration) * time.Second)
	}

	sm.startTimer("level1_cooldown", time.Duration(sm.l1CooldownDuration)*time.Second, func() {
		sm.SendEvent(Level1CooldownTimerEvent{})
	})
}

// onExitTriggerLevel1Wait handles exit from trigger_level_1_wait state
func (sm *StateMachine) onExitTriggerLevel1Wait(ctx context.Context) {
	sm.stopTimer("level1_cooldown")
	sm.alarmController.Stop()
}

// onEnterTriggerLevel1 handles entry to trigger_level_1 state
func (sm *StateMachine) onEnterTriggerLevel1(ctx context.Context) {
	sm.log.Info("entering trigger_level_1 state", "check_duration", "5s")

	sm.configureBMX(ctx, InterruptPinNone, SensitivityMedium)

	if err := sm.bmxClient.EnableInterrupt(ctx); err != nil {
		sm.log.Error("failed to enable interrupt", "error", err)
	}

	sm.startTimer("level1_check", 5*time.Second, func() {
		sm.SendEvent(Level1CheckTimerEvent{})
	})
}

// onExitTriggerLevel1 handles exit from trigger_level_1 state
func (sm *StateMachine) onExitTriggerLevel1(ctx context.Context) {
	sm.stopTimer("level1_check")
}

// onEnterTriggerLevel2 handles entry to trigger_level_2 state
func (sm *StateMachine) onEnterTriggerLevel2(ctx context.Context) {
	sm.log.Info("entering trigger_level_2 state")

	if err := sm.inhibitor.Acquire("Level 2 triggered"); err != nil {
		sm.log.Error("failed to acquire inhibitor", "error", err)
	}

	if err := sm.bmxClient.SoftReset(ctx); err != nil {
		sm.log.Error("failed to soft reset", "error", err)
	}

	sm.alarmController.Start(time.Duration(sm.alarmDuration) * time.Second)

	sm.startTimer("level2_check", 50*time.Second, func() {
		sm.SendEvent(Level2CheckTimerEvent{})
	})
}

// onExitTriggerLevel2 handles exit from trigger_level_2 state
func (sm *StateMachine) onExitTriggerLevel2(ctx context.Context) {
	sm.stopTimer("level2_check")
	sm.alarmController.Stop()
}

// onEnterWaitingMovement handles entry to waiting_movement state
func (sm *StateMachine) onEnterWaitingMovement(ctx context.Context) {
	sm.log.Info("entering waiting_movement state", "duration", "50s", "cycle", sm.level2Cycles)

	if err := sm.bmxClient.SoftReset(ctx); err != nil {
		sm.log.Error("failed to soft reset", "error", err)
	}

	sm.alarmController.Start(time.Duration(sm.alarmDuration) * time.Second)

	sm.startTimer("chip_setup", 47*time.Second, func() {
		sm.configureBMX(sm.ctx, InterruptPinNone, SensitivityHigh)
		if err := sm.bmxClient.EnableInterrupt(sm.ctx); err != nil {
			sm.log.Error("failed to enable interrupt", "error", err)
		}
	})

	sm.startTimer("waiting_movement", 50*time.Second, func() {
		sm.SendEvent(Level2CheckTimerEvent{})
	})
}

// onExitWaitingMovement handles exit from waiting_movement state
func (sm *StateMachine) onExitWaitingMovement(ctx context.Context) {
	sm.stopTimer("chip_setup")
	sm.stopTimer("waiting_movement")
	sm.alarmController.Stop()
}

// onEnterSeatboxAccess handles entry to seatbox_access state
func (sm *StateMachine) onEnterSeatboxAccess(ctx context.Context) {
	sm.log.Info("entering seatbox_access state", "previous_state", sm.preSeatboxState.String())

	if err := sm.inhibitor.Acquire("Seatbox access"); err != nil {
		sm.log.Error("failed to acquire inhibitor", "error", err)
	}

	if err := sm.bmxClient.SoftReset(ctx); err != nil {
		sm.log.Error("failed to soft reset", "error", err)
	}

	if err := sm.bmxClient.DisableInterrupt(ctx); err != nil {
		sm.log.Error("failed to disable interrupt", "error", err)
	}

	sm.configureBMX(ctx, InterruptPinNone, SensitivityLow)
}

// onExitSeatboxAccess handles exit from seatbox_access state
func (sm *StateMachine) onExitSeatboxAccess(ctx context.Context) {
	sm.log.Info("exiting seatbox_access state")
	sm.inhibitor.Release()
}
