package fsm

import (
	"context"
	"time"

	"github.com/librescoot/librefsm"
)

// --- Enter handlers ---

func (sm *StateMachine) enterWaitingEnabled(c *librefsm.Context) error {
	sm.log.Info("entering waiting_enabled state")

	ctx := context.Background()
	if err := sm.bmxClient.SoftReset(ctx); err != nil {
		sm.log.Error("failed to soft reset", "error", err)
	}
	if err := sm.bmxClient.DisableInterrupt(ctx); err != nil {
		sm.log.Error("failed to disable interrupt", "error", err)
	}

	sm.configureBMX(ctx, InterruptPinINT2, sensorIdle)
	sm.inhibitor.Release()
	sm.level2Cycles = 0
	return nil
}

func (sm *StateMachine) enterDisarmed(c *librefsm.Context) error {
	sm.log.Info("entering disarmed state")

	ctx := context.Background()
	if err := sm.bmxClient.SoftReset(ctx); err != nil {
		sm.log.Error("failed to soft reset", "error", err)
	}
	if err := sm.bmxClient.DisableInterrupt(ctx); err != nil {
		sm.log.Error("failed to disable interrupt", "error", err)
	}

	sm.configureBMX(ctx, InterruptPinNone, sensorIdle)
	sm.inhibitor.Release()
	sm.level2Cycles = 0
	return nil
}

func (sm *StateMachine) enterDelayArmed(c *librefsm.Context) error {
	sm.log.Info("entering delay_armed state")

	if err := sm.inhibitor.Acquire("Arming alarm"); err != nil {
		sm.log.Error("failed to acquire inhibitor", "error", err)
	}

	ctx := context.Background()
	if err := sm.bmxClient.SoftReset(ctx); err != nil {
		sm.log.Error("failed to soft reset", "error", err)
	}

	sm.configureBMX(ctx, InterruptPinINT2, sensorIdle)
	sm.level2Cycles = 0
	// Timer is declarative (WithTimeout in definition)
	return nil
}

func (sm *StateMachine) exitDelayArmed(c *librefsm.Context) error {
	// Declarative timeout timer is auto-cancelled by librefsm
	return nil
}

func (sm *StateMachine) enterArmed(c *librefsm.Context) error {
	sm.log.Info("entering armed state")

	sm.inhibitor.Release()

	ctx := context.Background()
	sm.configureBMX(ctx, InterruptPinBoth, sensorArmed)

	if err := sm.bmxClient.EnableInterrupt(ctx); err != nil {
		sm.log.Error("failed to enable interrupt", "error", err)
	}
	return nil
}

func (sm *StateMachine) enterTriggerLevel1Wait(c *librefsm.Context) error {
	sm.log.Info("entering trigger_level_1_wait state", "cooldown", sm.getL1CooldownDuration())

	if err := sm.inhibitor.Acquire("Level 1 cooldown"); err != nil {
		sm.log.Error("failed to acquire inhibitor", "error", err)
	}

	ctx := context.Background()
	if err := sm.bmxClient.SoftReset(ctx); err != nil {
		sm.log.Error("failed to soft reset", "error", err)
	}

	if err := sm.alarmController.BlinkHazards(); err != nil {
		sm.log.Error("failed to blink hazards", "error", err)
	}

	if sm.initWakeL1 {
		sm.log.Info("skipping hair trigger on startup wake (stale BMX latch)")
		sm.initWakeL1 = false
	} else if sm.getHairTriggerEnabled() {
		dur := sm.getHairTriggerDuration()
		sm.log.Info("hair trigger active, starting short alarm", "duration", dur)
		sm.alarmController.Start(time.Duration(dur) * time.Second)
	}

	// L1 cooldown timer — dynamic duration, use imperative timer
	cooldown := sm.getL1CooldownDuration()
	c.StartTimer(TimerL1Cooldown, time.Duration(cooldown)*time.Second,
		librefsm.Event{ID: EvL1CooldownTimeout})

	return nil
}

func (sm *StateMachine) exitTriggerLevel1Wait(c *librefsm.Context) error {
	c.StopTimer(TimerL1Cooldown)
	sm.alarmController.Stop()
	return nil
}

func (sm *StateMachine) enterTriggerLevel1(c *librefsm.Context) error {
	sm.log.Info("entering trigger_level_1 state")

	ctx := context.Background()
	sm.configureBMX(ctx, InterruptPinBoth, sensorLevel1)

	if err := sm.bmxClient.EnableInterrupt(ctx); err != nil {
		sm.log.Error("failed to enable interrupt", "error", err)
	}
	// 5s check timer is declarative (WithTimeout in definition)
	return nil
}

func (sm *StateMachine) exitTriggerLevel1(c *librefsm.Context) error {
	// Declarative timeout timer is auto-cancelled by librefsm
	return nil
}

func (sm *StateMachine) enterTriggerLevel2(c *librefsm.Context) error {
	sm.log.Info("entering trigger_level_2 state")

	if err := sm.inhibitor.Acquire("Level 2 triggered"); err != nil {
		sm.log.Error("failed to acquire inhibitor", "error", err)
	}

	ctx := context.Background()
	if err := sm.bmxClient.SoftReset(ctx); err != nil {
		sm.log.Error("failed to soft reset", "error", err)
	}

	sm.alarmController.Start(time.Duration(sm.getAlarmDuration()) * time.Second)
	// 50s check timer is declarative (WithTimeout in definition)
	return nil
}

func (sm *StateMachine) exitTriggerLevel2(c *librefsm.Context) error {
	sm.alarmController.Stop()
	return nil
}

func (sm *StateMachine) enterWaitingMovement(c *librefsm.Context) error {
	sm.log.Info("entering waiting_movement state", "cycle", sm.level2Cycles)

	ctx := context.Background()
	if err := sm.bmxClient.SoftReset(ctx); err != nil {
		sm.log.Error("failed to soft reset", "error", err)
	}

	sm.alarmController.Start(time.Duration(sm.getAlarmDuration()) * time.Second)

	// At 47s, configure chip for motion detection during final 3s
	c.StartTimer(TimerChipSetup, 47*time.Second,
		librefsm.Event{ID: EvChipSetupTimeout})

	// Full window timeout
	c.StartTimer(TimerWaiting, 50*time.Second,
		librefsm.Event{ID: EvWaitingTimeout})

	return nil
}

func (sm *StateMachine) exitWaitingMovement(c *librefsm.Context) error {
	c.StopTimer(TimerChipSetup)
	c.StopTimer(TimerWaiting)
	sm.alarmController.Stop()
	return nil
}

func (sm *StateMachine) enterSeatboxAccess(c *librefsm.Context) error {
	sm.log.Info("entering seatbox_access state", "previous_state", sm.preSeatboxState)

	if err := sm.inhibitor.Acquire("Seatbox access"); err != nil {
		sm.log.Error("failed to acquire inhibitor", "error", err)
	}

	ctx := context.Background()
	if err := sm.bmxClient.SoftReset(ctx); err != nil {
		sm.log.Error("failed to soft reset", "error", err)
	}
	if err := sm.bmxClient.DisableInterrupt(ctx); err != nil {
		sm.log.Error("failed to disable interrupt", "error", err)
	}

	sm.configureBMX(ctx, InterruptPinNone, sensorIdle)
	return nil
}

func (sm *StateMachine) exitSeatboxAccess(c *librefsm.Context) error {
	sm.inhibitor.Release()
	return nil
}

// --- Guards ---

func (sm *StateMachine) guardInitToL1Wait(c *librefsm.Context) bool {
	if !sm.alarmEnabled || !sm.vehicleStandby {
		return false
	}
	ctx := context.Background()
	motionDetected, err := sm.bmxClient.CheckInterruptStatus(ctx)
	if err != nil {
		return false
	}
	if motionDetected {
		sm.initWakeL1 = true
		return true
	}
	return false
}

func (sm *StateMachine) guardInitToArmed(c *librefsm.Context) bool {
	return sm.alarmEnabled && sm.vehicleStandby
}

func (sm *StateMachine) guardInitToDisarmed(c *librefsm.Context) bool {
	return sm.alarmEnabled
}

func (sm *StateMachine) guardVehicleStandby(c *librefsm.Context) bool {
	return sm.vehicleStandby
}

func (sm *StateMachine) guardVehicleStandbyPayload(c *librefsm.Context) bool {
	if state, ok := c.Event.Payload.(VehicleState); ok {
		if state == VehicleStateStandby {
			sm.vehicleStandby = true
			return true
		}
	}
	return false
}

func (sm *StateMachine) guardVehicleActive(c *librefsm.Context) bool {
	if state, ok := c.Event.Payload.(VehicleState); ok {
		return shouldDisarmForVehicleState(state)
	}
	return false
}

func (sm *StateMachine) guardAlarmEnabled(c *librefsm.Context) bool {
	return sm.alarmEnabled
}

func (sm *StateMachine) guardLevel2CyclesRemaining(c *librefsm.Context) bool {
	return sm.level2Cycles < 4
}

func (sm *StateMachine) guardLevel2CyclesRemainingIncrement(c *librefsm.Context) bool {
	sm.level2Cycles++
	return sm.level2Cycles < 4
}

func shouldDisarmForVehicleState(state VehicleState) bool {
	switch state {
	case VehicleStateParked, VehicleStateReadyToDrive, VehicleStateWaitingSeatbox:
		return true
	default:
		return false
	}
}

// --- Transition actions ---

func (sm *StateMachine) actionSetAlarmEnabled(c *librefsm.Context) error {
	sm.alarmEnabled = true
	return nil
}

func (sm *StateMachine) actionSetAlarmDisabled(c *librefsm.Context) error {
	sm.alarmEnabled = false
	return nil
}

func (sm *StateMachine) actionUpdateVehicleState(c *librefsm.Context) error {
	if state, ok := c.Event.Payload.(VehicleState); ok {
		sm.vehicleStandby = (state == VehicleStateStandby)
	}
	return nil
}

func (sm *StateMachine) actionClearVehicleStandby(c *librefsm.Context) error {
	sm.vehicleStandby = false
	return nil
}

func (sm *StateMachine) actionSavePreSeatboxState(state librefsm.StateID) func(*librefsm.Context) error {
	return func(c *librefsm.Context) error {
		sm.preSeatboxState = state
		return nil
	}
}

func (sm *StateMachine) actionChipSetup(c *librefsm.Context) error {
	ctx := context.Background()
	sm.configureBMX(ctx, InterruptPinNone, sensorWaiting)
	if err := sm.bmxClient.EnableInterrupt(ctx); err != nil {
		sm.log.Error("failed to enable interrupt", "error", err)
	}
	return nil
}

func (sm *StateMachine) actionBlinkHazards(c *librefsm.Context) error {
	sm.log.Info("movement detected during L1, blinking hazards")
	if err := sm.alarmController.BlinkHazards(); err != nil {
		sm.log.Error("failed to blink hazards", "error", err)
	}
	return nil
}

// --- Helpers ---

func (sm *StateMachine) configureBMX(ctx context.Context, pin InterruptPin, cfg SensorConfig) {
	if err := sm.bmxClient.SetInterruptPin(ctx, pin); err != nil {
		sm.log.Error("failed to set interrupt pin", "pin", pin, "error", err)
	}
	if err := sm.bmxClient.ConfigureSensor(ctx, cfg); err != nil {
		sm.log.Error("failed to configure sensor", "error", err)
	}
}
