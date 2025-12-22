package fsm

import "context"

// getTransition determines the next state based on current state and event
func (sm *StateMachine) getTransition(event Event) State {
	switch sm.state {
	case StateInit:
		if e, ok := event.(VehicleStateChangedEvent); ok {
			sm.vehicleStandby = (e.State == VehicleStateStandby)
		}
		if e, ok := event.(AlarmModeChangedEvent); ok {
			sm.alarmEnabled = e.Enabled
		}
		if _, ok := event.(InitCompleteEvent); ok {
			if sm.alarmEnabled {
				if sm.vehicleStandby {
					return StateDelayArmed
				}
				return StateDisarmed
			}
			return StateWaitingEnabled
		}

	case StateWaitingEnabled:
		if e, ok := event.(AlarmModeChangedEvent); ok && e.Enabled {
			sm.alarmEnabled = true
			if sm.vehicleStandby {
				return StateDelayArmed
			}
			return StateDisarmed
		}

	case StateDisarmed:
		if e, ok := event.(VehicleStateChangedEvent); ok && e.State == VehicleStateStandby {
			sm.vehicleStandby = true
			return StateDelayArmed
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}

	case StateDelayArmed:
		if _, ok := event.(DelayArmedTimerEvent); ok {
			return StateArmed
		}
		if _, ok := event.(UnauthorizedSeatboxEvent); ok {
			return StateTriggerLevel2
		}
		if e, ok := event.(VehicleStateChangedEvent); ok && e.State != VehicleStateStandby {
			sm.vehicleStandby = false
			return StateDisarmed
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}

	case StateArmed:
		if _, ok := event.(SeatboxOpenedEvent); ok {
			sm.preSeatboxState = StateArmed
			return StateSeatboxAccess
		}
		if _, ok := event.(UnauthorizedSeatboxEvent); ok {
			return StateTriggerLevel2
		}
		if _, ok := event.(MinorMovementEvent); ok {
			return StateTriggerLevel1Wait
		}
		if _, ok := event.(BMXInterruptEvent); ok {
			return StateTriggerLevel1Wait
		}
		if e, ok := event.(VehicleStateChangedEvent); ok && e.State != VehicleStateStandby {
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

	case StateTriggerLevel1Wait:
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
		if e, ok := event.(VehicleStateChangedEvent); ok && e.State != VehicleStateStandby {
			sm.vehicleStandby = false
			return StateDisarmed
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}

	case StateTriggerLevel1:
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
		if _, ok := event.(MajorMovementEvent); ok {
			return StateTriggerLevel2
		}
		if _, ok := event.(BMXInterruptEvent); ok {
			return StateTriggerLevel2
		}
		if e, ok := event.(VehicleStateChangedEvent); ok && e.State != VehicleStateStandby {
			sm.vehicleStandby = false
			return StateDisarmed
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}

	case StateTriggerLevel2:
		if _, ok := event.(Level2CheckTimerEvent); ok {
			if sm.level2Cycles >= 4 {
				return StateDisarmed
			}
			return StateWaitingMovement
		}
		if e, ok := event.(VehicleStateChangedEvent); ok && e.State != VehicleStateStandby {
			sm.vehicleStandby = false
			return StateDisarmed
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}

	case StateWaitingMovement:
		if _, ok := event.(Level2CheckTimerEvent); ok {
			return StateDelayArmed
		}
		if _, ok := event.(MajorMovementEvent); ok {
			sm.level2Cycles++
			if sm.level2Cycles >= 4 {
				return StateDisarmed
			}
			return StateWaitingMovement
		}
		if e, ok := event.(VehicleStateChangedEvent); ok && e.State != VehicleStateStandby {
			sm.vehicleStandby = false
			return StateDisarmed
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}

	case StateSeatboxAccess:
		if _, ok := event.(SeatboxClosedEvent); ok {
			sm.seatboxLockClosed = true
			return StateDelayArmed
		}
		if e, ok := event.(VehicleStateChangedEvent); ok && e.State != VehicleStateStandby {
			sm.vehicleStandby = false
			return StateDisarmed
		}
		if e, ok := event.(AlarmModeChangedEvent); ok && !e.Enabled {
			sm.alarmEnabled = false
			return StateWaitingEnabled
		}
	}

	return sm.state
}

// enterState handles state entry actions
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

// exitState handles state exit actions
func (sm *StateMachine) exitState(ctx context.Context, state State) {
	switch state {
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
