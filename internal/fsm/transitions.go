package fsm

import "context"

func shouldDisarmForVehicleState(state VehicleState) bool {
	switch state {
	case VehicleStateParked, VehicleStateReadyToDrive, VehicleStateWaitingSeatbox:
		return true
	default:
		return false
	}
}

// vehicleInUse reports whether the state means the rider is actually using the
// scooter. Only those states cancel a pending post-wake re-hibernate; transient
// states (waiting-seatbox, shutting-down, waiting-hibernation) must not, so an
// alarm wake that lands there still goes back to hibernation once quiet.
func vehicleInUse(state VehicleState) bool {
	switch state {
	case VehicleStateParked, VehicleStateReadyToDrive:
		return true
	default:
		return false
	}
}

func (sm *StateMachine) getTransition(event Event) State {
	switch sm.state {
	case StateInit:
		if e, ok := event.(VehicleStateChangedEvent); ok {
			sm.vehicleStandby = (e.State == VehicleStateStandby)
		}
		if e, ok := event.(AlarmModeChangedEvent); ok {
			sm.alarmEnabled = e.Enabled
		}
		if be, ok := event.(BMXInterruptEvent); ok && be.Data == "wake-hibernation" {
			sm.wakeFromHibernation = true
		}
		if _, ok := event.(InitCompleteEvent); ok {
			if sm.alarmEnabled {
				if sm.vehicleStandby {

					if sm.wakeFromHibernation && sm.motionTriggerEnabled {
						sm.log.Info("init wake-from-hibernation, triggering L1")
						return StateTriggerLevel1Wait
					}
					return StateArmed
				}
				return StateDisarmed
			}
			return StateWaitingEnabled
		}

	case StateWaitingEnabled:

		if e, ok := event.(VehicleStateChangedEvent); ok {
			sm.vehicleStandby = (e.State == VehicleStateStandby)
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && e.Enabled {
			sm.alarmEnabled = true
			if sm.vehicleStandby {
				return StateDelayArmed
			}
			return StateDisarmed
		}

	case StateDisarmed:
		if e, ok := event.(VehicleStateChangedEvent); ok {
			sm.vehicleStandby = e.State == VehicleStateStandby
			if e.State == VehicleStateStandby {
				if sm.runtimeDisarmed {
					if !sm.runtimeDisarmSawInUse {
						return StateDisarmed
					}
					sm.clearRuntimeDisarm("vehicle parked")
				}
				return StateDelayArmed
			}
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}
		if _, ok := event.(RuntimeArmEvent); ok && sm.alarmEnabled {
			return StateDelayArmed
		}
		if e, ok := event.(UMSModeChangedEvent); ok && !e.Active && sm.alarmEnabled && sm.vehicleStandby {
			return StateDelayArmed
		}
		if _, ok := event.(PostAlarmCooldownTimerEvent); ok && sm.alarmEnabled && sm.vehicleStandby && !sm.runtimeDisarmed {
			return StateDelayArmed
		}
		if _, ok := event.(RuntimeDisarmTimerEvent); ok && sm.alarmEnabled && sm.vehicleStandby {
			return StateDelayArmed
		}
		if e, ok := event.(HibernationImminentEvent); ok && e.Imminent && sm.alarmEnabled && sm.vehicleStandby {
			return StateArmed
		}

	case StateDelayArmed:
		if _, ok := event.(DelayArmedTimerEvent); ok {
			return StateArmed
		}
		if _, ok := event.(SeatboxOpenedEvent); ok {
			sm.preSeatboxState = StateDelayArmed
			return StateSeatboxAccess
		}
		if _, ok := event.(UnauthorizedSeatboxEvent); ok {
			return StateTriggerLevel2
		}
		if e, ok := event.(VehicleStateChangedEvent); ok && shouldDisarmForVehicleState(e.State) {
			sm.vehicleStandby = false
			return StateDisarmed
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}
		if _, ok := event.(RuntimeDisarmEvent); ok {
			return StateDisarmed
		}

	case StateArmed:
		if _, ok := event.(SeatboxOpenedEvent); ok {
			sm.preSeatboxState = StateArmed
			return StateSeatboxAccess
		}
		if _, ok := event.(UnauthorizedSeatboxEvent); ok {
			return StateTriggerLevel2
		}
		if be, ok := event.(BMXInterruptEvent); ok {
			if be.Data == "wake-hibernation" {
				sm.wakeFromHibernation = true
			}
			return StateTriggerLevel1Wait
		}
		if _, ok := event.(InputTriggerEvent); ok {
			return StateTriggerLevel1Wait
		}
		if e, ok := event.(VehicleStateChangedEvent); ok && shouldDisarmForVehicleState(e.State) {
			sm.vehicleStandby = false
			return StateDisarmed
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}
		if _, ok := event.(ManualTriggerEvent); ok {
			return StateTriggerLevel2
		}
		if _, ok := event.(RuntimeDisarmEvent); ok {
			return StateDisarmed
		}

	case StateTriggerLevel1Wait:
		if _, ok := event.(RuntimeStopEvent); ok {
			return StateDisarmed
		}
		if _, ok := event.(SeatboxOpenedEvent); ok {
			sm.preSeatboxState = StateTriggerLevel1Wait
			return StateSeatboxAccess
		}
		if _, ok := event.(UnauthorizedSeatboxEvent); ok {
			return StateTriggerLevel2
		}
		if _, ok := event.(Level1CooldownTimerEvent); ok {
			return StateTriggerLevel1
		}
		if e, ok := event.(VehicleStateChangedEvent); ok && shouldDisarmForVehicleState(e.State) {
			sm.vehicleStandby = false
			return StateDisarmed
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}
		if _, ok := event.(RuntimeDisarmEvent); ok {
			return StateDisarmed
		}

	case StateTriggerLevel1:
		if _, ok := event.(RuntimeStopEvent); ok {
			return StateDisarmed
		}
		if _, ok := event.(SeatboxOpenedEvent); ok {
			sm.preSeatboxState = StateTriggerLevel1
			return StateSeatboxAccess
		}
		if _, ok := event.(UnauthorizedSeatboxEvent); ok {
			return StateTriggerLevel2
		}
		if _, ok := event.(Level1CheckTimerEvent); ok {
			return StateDelayArmed
		}
		if isTamperTrigger(event) {
			return StateTriggerLevel2
		}
		if e, ok := event.(VehicleStateChangedEvent); ok && shouldDisarmForVehicleState(e.State) {
			sm.vehicleStandby = false
			return StateDisarmed
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}
		if _, ok := event.(RuntimeDisarmEvent); ok {
			return StateDisarmed
		}

	case StateTriggerLevel2:
		if _, ok := event.(RuntimeStopEvent); ok {
			return StateDisarmed
		}
		if _, ok := event.(Level2CheckTimerEvent); ok {
			if sm.level2Cycles >= maxLevel2Cycles {
				return StateDisarmed
			}
			return StateWaitingMovement
		}
		if e, ok := event.(VehicleStateChangedEvent); ok && shouldDisarmForVehicleState(e.State) {
			sm.vehicleStandby = false
			return StateDisarmed
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}
		if _, ok := event.(RuntimeDisarmEvent); ok {
			return StateDisarmed
		}

	case StateWaitingMovement:
		if _, ok := event.(RuntimeStopEvent); ok {
			return StateDisarmed
		}
		if _, ok := event.(Level2CheckTimerEvent); ok {
			return StateDelayArmed
		}
		if isTamperTrigger(event) {
			sm.level2Cycles++
			if sm.level2Cycles >= maxLevel2Cycles {
				return StateDisarmed
			}
			return StateTriggerLevel2
		}
		if e, ok := event.(VehicleStateChangedEvent); ok && shouldDisarmForVehicleState(e.State) {
			sm.vehicleStandby = false
			return StateDisarmed
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}
		if _, ok := event.(RuntimeDisarmEvent); ok {
			return StateDisarmed
		}

	case StateSeatboxAccess:
		if _, ok := event.(SeatboxClosedEvent); ok {
			sm.seatboxLockClosed = true
			return StateDelayArmed
		}
		if e, ok := event.(VehicleStateChangedEvent); ok && shouldDisarmForVehicleState(e.State) {
			sm.vehicleStandby = false
			return StateDisarmed
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}
		if _, ok := event.(RuntimeDisarmEvent); ok {
			return StateDisarmed
		}
	}

	return sm.state
}

func (sm *StateMachine) enterState(ctx context.Context, state State) {
	switch state {
	case StateInit:
		sm.onEnterInit(ctx)
	case StateWaitingEnabled:
		sm.onEnterWaitingEnabled(ctx)
	case StateDisarmed:
		sm.onEnterDisarmed(ctx)
	case StateDelayArmed:
		sm.onEnterDelayArmed(ctx)
	case StateArmed:
		sm.onEnterArmed(ctx)
	case StateTriggerLevel1Wait:
		sm.onEnterTriggerLevel1Wait(ctx)
	case StateTriggerLevel1:
		sm.onEnterTriggerLevel1(ctx)
	case StateTriggerLevel2:
		sm.onEnterTriggerLevel2(ctx)
	case StateWaitingMovement:
		sm.onEnterWaitingMovement(ctx)
	case StateSeatboxAccess:
		sm.onEnterSeatboxAccess(ctx)
	}
}

func (sm *StateMachine) exitState(ctx context.Context, state State) {
	switch state {
	case StateDisarmed:
		sm.onExitDisarmed(ctx)
	case StateDelayArmed:
		sm.onExitDelayArmed(ctx)
	case StateArmed:
		sm.onExitArmed(ctx)
	case StateTriggerLevel1Wait:
		sm.onExitTriggerLevel1Wait(ctx)
	case StateTriggerLevel1:
		sm.onExitTriggerLevel1(ctx)
	case StateTriggerLevel2:
		sm.onExitTriggerLevel2(ctx)
	case StateWaitingMovement:
		sm.onExitWaitingMovement(ctx)
	case StateSeatboxAccess:
		sm.onExitSeatboxAccess(ctx)
	}
}
