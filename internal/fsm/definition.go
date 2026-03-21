package fsm

import (
	"time"

	"github.com/librescoot/librefsm"
)

func buildDefinition(sys *StateMachine) *librefsm.Definition {
	return librefsm.NewDefinition().
		Initial(StateStarting).

		// --- States ---

		State(StateStarting).
		State(StateWaitingEnabled,
			librefsm.WithOnEnter(sys.enterWaitingEnabled),
		).
		State(StateDisarmed,
			librefsm.WithOnEnter(sys.enterDisarmed),
		).
		State(StateDelayArmed,
			librefsm.WithOnEnter(sys.enterDelayArmed),
			librefsm.WithOnExit(sys.exitDelayArmed),
			librefsm.WithTimeout(5*time.Second, EvDelayArmedTimeout),
		).
		State(StateArmed,
			librefsm.WithOnEnter(sys.enterArmed),
		).
		State(StateTriggerLevel1Wait,
			librefsm.WithOnEnter(sys.enterTriggerLevel1Wait),
			librefsm.WithOnExit(sys.exitTriggerLevel1Wait),
		).
		State(StateTriggerLevel1,
			librefsm.WithOnEnter(sys.enterTriggerLevel1),
			librefsm.WithOnExit(sys.exitTriggerLevel1),
			librefsm.WithTimeout(5*time.Second, EvL1CheckTimeout),
		).
		State(StateTriggerLevel2,
			librefsm.WithOnEnter(sys.enterTriggerLevel2),
			librefsm.WithOnExit(sys.exitTriggerLevel2),
			librefsm.WithTimeout(50*time.Second, EvL2CheckTimeout),
		).
		State(StateWaitingMovement,
			librefsm.WithOnEnter(sys.enterWaitingMovement),
			librefsm.WithOnExit(sys.exitWaitingMovement),
		).
		State(StateSeatboxAccess,
			librefsm.WithOnEnter(sys.enterSeatboxAccess),
			librefsm.WithOnExit(sys.exitSeatboxAccess),
		).

		// --- Starting (accumulate flags, then transition on EvInitComplete) ---

		Transition(StateStarting, EvAlarmEnabled, StateStarting,
			librefsm.WithAction(sys.actionSetAlarmEnabled),
		).
		Transition(StateStarting, EvAlarmDisabled, StateStarting,
			librefsm.WithAction(sys.actionSetAlarmDisabled),
		).
		Transition(StateStarting, EvVehicleState, StateStarting,
			librefsm.WithAction(sys.actionUpdateVehicleState),
		).
		Transition(StateStarting, EvInitComplete, StateTriggerLevel1Wait,
			librefsm.WithGuard(sys.guardInitToL1Wait),
		).
		Transition(StateStarting, EvInitComplete, StateArmed,
			librefsm.WithGuard(sys.guardInitToArmed),
		).
		Transition(StateStarting, EvInitComplete, StateDisarmed,
			librefsm.WithGuard(sys.guardInitToDisarmed),
		).
		Transition(StateStarting, EvInitComplete, StateWaitingEnabled).

		// --- WaitingEnabled ---

		Transition(StateWaitingEnabled, EvAlarmEnabled, StateDelayArmed,
			librefsm.WithGuard(sys.guardVehicleStandby),
		).
		Transition(StateWaitingEnabled, EvAlarmEnabled, StateDisarmed).

		// --- Disarmed ---

		Transition(StateDisarmed, EvVehicleState, StateDelayArmed,
			librefsm.WithGuard(sys.guardVehicleStandbyPayload),
		).
		Transition(StateDisarmed, EvAlarmDisabled, StateWaitingEnabled).
		Transition(StateDisarmed, EvRuntimeArm, StateDelayArmed,
			librefsm.WithGuard(sys.guardAlarmEnabled),
		).

		// --- DelayArmed ---

		Transition(StateDelayArmed, EvDelayArmedTimeout, StateArmed).
		Transition(StateDelayArmed, EvUnauthorizedSeatbox, StateTriggerLevel2).
		Transition(StateDelayArmed, EvVehicleState, StateDisarmed,
			librefsm.WithGuard(sys.guardVehicleActive),
			librefsm.WithAction(sys.actionClearVehicleStandby),
		).
		Transition(StateDelayArmed, EvAlarmDisabled, StateWaitingEnabled).
		Transition(StateDelayArmed, EvRuntimeDisarm, StateDisarmed).

		// --- Armed ---

		Transition(StateArmed, EvSeatboxOpened, StateSeatboxAccess,
			librefsm.WithAction(sys.actionSavePreSeatboxState(StateArmed)),
		).
		Transition(StateArmed, EvUnauthorizedSeatbox, StateTriggerLevel2).
		Transition(StateArmed, EvBMXInterrupt, StateTriggerLevel1Wait).
		Transition(StateArmed, EvVehicleState, StateDisarmed,
			librefsm.WithGuard(sys.guardVehicleActive),
			librefsm.WithAction(sys.actionClearVehicleStandby),
		).
		Transition(StateArmed, EvAlarmDisabled, StateWaitingEnabled).
		Transition(StateArmed, EvManualTrigger, StateTriggerLevel2).
		Transition(StateArmed, EvRuntimeDisarm, StateDisarmed).

		// --- TriggerLevel1Wait ---

		Transition(StateTriggerLevel1Wait, EvSeatboxOpened, StateSeatboxAccess,
			librefsm.WithAction(sys.actionSavePreSeatboxState(StateTriggerLevel1Wait)),
		).
		Transition(StateTriggerLevel1Wait, EvUnauthorizedSeatbox, StateTriggerLevel2).
		Transition(StateTriggerLevel1Wait, EvL1CooldownTimeout, StateTriggerLevel1).
		Transition(StateTriggerLevel1Wait, EvVehicleState, StateDisarmed,
			librefsm.WithGuard(sys.guardVehicleActive),
			librefsm.WithAction(sys.actionClearVehicleStandby),
		).
		Transition(StateTriggerLevel1Wait, EvAlarmDisabled, StateWaitingEnabled).
		Transition(StateTriggerLevel1Wait, EvRuntimeDisarm, StateDisarmed).

		// --- TriggerLevel1 ---

		Transition(StateTriggerLevel1, EvSeatboxOpened, StateSeatboxAccess,
			librefsm.WithAction(sys.actionSavePreSeatboxState(StateTriggerLevel1)),
		).
		Transition(StateTriggerLevel1, EvUnauthorizedSeatbox, StateTriggerLevel2).
		Transition(StateTriggerLevel1, EvL1CheckTimeout, StateDelayArmed).
		Transition(StateTriggerLevel1, EvBMXInterrupt, StateTriggerLevel2,
			librefsm.WithAction(sys.actionBlinkHazards),
		).
		Transition(StateTriggerLevel1, EvVehicleState, StateDisarmed,
			librefsm.WithGuard(sys.guardVehicleActive),
			librefsm.WithAction(sys.actionClearVehicleStandby),
		).
		Transition(StateTriggerLevel1, EvAlarmDisabled, StateWaitingEnabled).
		Transition(StateTriggerLevel1, EvRuntimeDisarm, StateDisarmed).

		// --- TriggerLevel2 ---

		Transition(StateTriggerLevel2, EvL2CheckTimeout, StateWaitingMovement,
			librefsm.WithGuard(sys.guardLevel2CyclesRemaining),
		).
		Transition(StateTriggerLevel2, EvL2CheckTimeout, StateDisarmed).
		Transition(StateTriggerLevel2, EvVehicleState, StateDisarmed,
			librefsm.WithGuard(sys.guardVehicleActive),
			librefsm.WithAction(sys.actionClearVehicleStandby),
		).
		Transition(StateTriggerLevel2, EvAlarmDisabled, StateWaitingEnabled).
		Transition(StateTriggerLevel2, EvRuntimeDisarm, StateDisarmed).

		// --- WaitingMovement ---

		// Chip setup at 47s — self-transition runs the action without exit/enter
		Transition(StateWaitingMovement, EvChipSetupTimeout, StateWaitingMovement,
			librefsm.WithAction(sys.actionChipSetup),
		).
		Transition(StateWaitingMovement, EvWaitingTimeout, StateDelayArmed).
		Transition(StateWaitingMovement, EvBMXInterrupt, StateTriggerLevel2,
			librefsm.WithGuard(sys.guardLevel2CyclesRemainingIncrement),
		).
		Transition(StateWaitingMovement, EvBMXInterrupt, StateDisarmed).
		Transition(StateWaitingMovement, EvVehicleState, StateDisarmed,
			librefsm.WithGuard(sys.guardVehicleActive),
			librefsm.WithAction(sys.actionClearVehicleStandby),
		).
		Transition(StateWaitingMovement, EvAlarmDisabled, StateWaitingEnabled).
		Transition(StateWaitingMovement, EvRuntimeDisarm, StateDisarmed).

		// --- SeatboxAccess ---

		Transition(StateSeatboxAccess, EvSeatboxClosed, StateDelayArmed).
		Transition(StateSeatboxAccess, EvVehicleState, StateDisarmed,
			librefsm.WithGuard(sys.guardVehicleActive),
			librefsm.WithAction(sys.actionClearVehicleStandby),
		).
		Transition(StateSeatboxAccess, EvAlarmDisabled, StateWaitingEnabled).
		Transition(StateSeatboxAccess, EvRuntimeDisarm, StateDisarmed)
}
