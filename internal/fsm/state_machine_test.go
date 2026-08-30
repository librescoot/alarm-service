package fsm

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

type mockMotionRPC struct {
	prepareCalls int
	prepareErr   error
}

func (m *mockMotionRPC) PrepareHibernation(ctx context.Context) error {
	m.prepareCalls++
	return m.prepareErr
}

type mockStatusPublisher struct {
	lastStatus        string
	lastTriggerSource string
	lastTriggerAt     time.Time
	triggerCalls      int
}

func (m *mockStatusPublisher) PublishStatus(status string) error {
	m.lastStatus = status
	return nil
}

func (m *mockStatusPublisher) PublishTrigger(source string, at time.Time) error {
	m.lastTriggerSource = source
	m.lastTriggerAt = at
	m.triggerCalls++
	return nil
}

type mockSuspendInhibitor struct {
	acquired bool
	reason   string
}

func (m *mockSuspendInhibitor) Acquire(reason string) error {
	m.acquired = true
	m.reason = reason
	return nil
}

func (m *mockSuspendInhibitor) Release() error {
	m.acquired = false
	m.reason = ""
	return nil
}

type mockAlarmController struct {
	active      bool
	duration    time.Duration
	hornEnabled bool
	blinkCalled int
}

func (m *mockAlarmController) Start(duration time.Duration) error {
	m.active = true
	m.duration = duration
	return nil
}

func (m *mockAlarmController) Stop() error {
	m.active = false
	return nil
}

func (m *mockAlarmController) SetHornEnabled(enabled bool) {
	m.hornEnabled = enabled
}

func (m *mockAlarmController) BlinkHazards() error {
	m.blinkCalled++
	return nil
}

type mockPowerCommander struct {
	hibernateCalled int
}

func (m *mockPowerCommander) RequestHibernate() error {
	m.hibernateCalled++
	return nil
}

func createTestStateMachine() (*StateMachine, *mockMotionRPC, *mockStatusPublisher, *mockSuspendInhibitor, *mockAlarmController) {
	sm, motion, pub, inh, alarm, _ := createTestStateMachineWithPower()
	return sm, motion, pub, inh, alarm
}

func createTestStateMachineWithPower() (*StateMachine, *mockMotionRPC, *mockStatusPublisher, *mockSuspendInhibitor, *mockAlarmController, *mockPowerCommander) {
	motion := &mockMotionRPC{}
	pub := &mockStatusPublisher{}
	inh := &mockSuspendInhibitor{}
	alarm := &mockAlarmController{}
	power := &mockPowerCommander{}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	sm := New(motion, pub, inh, alarm, power, 10, log)
	return sm, motion, pub, inh, alarm, power
}

func TestStateMachine_InitialState(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()

	if sm.State() != StateInit {
		t.Errorf("expected initial state to be StateInit, got %s", sm.State())
	}
}

func TestStateMachine_InitToWaitingEnabled(t *testing.T) {
	sm, _, pub, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.alarmEnabled = false
	sm.SendEvent(InitCompleteEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateWaitingEnabled {
		t.Errorf("expected StateWaitingEnabled, got %s", sm.State())
	}

	if pub.lastStatus != "disabled" {
		t.Errorf("expected status 'disabled', got %s", pub.lastStatus)
	}
}

func TestStateMachine_InitToDisarmed(t *testing.T) {
	sm, _, pub, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.alarmEnabled = true
	sm.vehicleStandby = false
	sm.SendEvent(InitCompleteEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDisarmed {
		t.Errorf("expected StateDisarmed, got %s", sm.State())
	}

	if pub.lastStatus != "disarmed" {
		t.Errorf("expected status 'disarmed', got %s", pub.lastStatus)
	}
}

func TestStateMachine_InitToArmedWhenStandby(t *testing.T) {
	sm, _, pub, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.SendEvent(InitCompleteEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateArmed {
		t.Errorf("expected StateArmed (skip delay on startup), got %s", sm.State())
	}

	if pub.lastStatus != "armed" {
		t.Errorf("expected status 'armed', got %s", pub.lastStatus)
	}
}

func TestStateMachine_DisarmedToDelayArmed(t *testing.T) {
	sm, _, _, inh, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateDisarmed
	sm.alarmEnabled = true
	sm.vehicleStandby = false

	sm.SendEvent(VehicleStateChangedEvent{State: VehicleStateStandby})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDelayArmed {
		t.Errorf("expected StateDelayArmed, got %s", sm.State())
	}

	if !inh.acquired {
		t.Error("expected suspend inhibitor to be acquired")
	}

	if sm.level2Cycles != 0 {
		t.Errorf("expected level2Cycles to be reset to 0, got %d", sm.level2Cycles)
	}
}

func TestStateMachine_DelayArmedToArmed(t *testing.T) {
	sm, _, _, inh, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateDelayArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true
	inh.acquired = true

	sm.SendEvent(DelayArmedTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateArmed {
		t.Errorf("expected StateArmed, got %s", sm.State())
	}

	if inh.acquired {
		t.Error("expected suspend inhibitor to be released in armed state")
	}
}

func TestStateMachine_ArmedToTriggerLevel1Wait(t *testing.T) {
	sm, _, _, inh, alarm := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true

	sm.SendEvent(BMXInterruptEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel1Wait {
		t.Errorf("expected StateTriggerLevel1Wait, got %s", sm.State())
	}

	if !inh.acquired {
		t.Error("expected suspend inhibitor to be acquired in level 1 wait")
	}

	if alarm.blinkCalled != 1 {
		t.Errorf("expected hazards to blink once, got %d blinks", alarm.blinkCalled)
	}
}

func TestStateMachine_PublishesTriggerSource(t *testing.T) {
	cases := []struct {
		name  string
		event Event
		want  string
	}{
		{"handlebar position", InputTriggerEvent{Source: TriggerSourceHandlebarPosition}, "handlebar_position"},
		{"handlebar lock", InputTriggerEvent{Source: TriggerSourceHandlebarLock}, "handlebar_lock"},
		{"brake", InputTriggerEvent{Source: TriggerSourceBrakeLeft}, "brake_left"},
		{"motion", BMXInterruptEvent{}, "motion"},
		{"seatbox", UnauthorizedSeatboxEvent{}, "seatbox"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sm, _, pub, _, _ := createTestStateMachine()
			ctx := context.Background()

			sm.state = StateArmed
			sm.alarmEnabled = true
			sm.vehicleStandby = true
			sm.handlebarSettled = true

			before := time.Now()
			sm.handleEvent(ctx, tc.event)

			if pub.lastTriggerSource != tc.want {
				t.Errorf("expected trigger source %q, got %q", tc.want, pub.lastTriggerSource)
			}
			if pub.lastTriggerAt.Before(before) {
				t.Errorf("expected a trigger timestamp at or after %v, got %v", before, pub.lastTriggerAt)
			}
		})
	}
}

func TestStateMachine_DroppedTriggerPublishesNoSource(t *testing.T) {
	sm, _, pub, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.handlebarSettled = false

	sm.handleEvent(ctx, InputTriggerEvent{Source: TriggerSourceHandlebarPosition})

	if pub.triggerCalls != 0 {
		t.Errorf("expected no trigger publish for a dropped event, got %d", pub.triggerCalls)
	}
}

func TestStateMachine_TriggerSourcePersistsAfterDisarm(t *testing.T) {
	sm, _, pub, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.handlebarSettled = true

	sm.handleEvent(ctx, InputTriggerEvent{Source: TriggerSourceHandlebarPosition})
	sm.handleEvent(ctx, VehicleStateChangedEvent{State: VehicleStateParked})

	if sm.State() == StateTriggerLevel1Wait {
		t.Fatal("precondition failed: expected the vehicle change to leave the trigger state")
	}
	if pub.lastTriggerSource != "handlebar_position" {
		t.Errorf("expected the last trigger to survive disarm, got %q", pub.lastTriggerSource)
	}
}

func TestStateMachine_Level1WaitToLevel1(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateTriggerLevel1Wait

	sm.SendEvent(Level1CooldownTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel1 {
		t.Errorf("expected StateTriggerLevel1, got %s", sm.State())
	}
}

func TestStateMachine_Level1ToLevel2OnMovement(t *testing.T) {
	sm, _, _, _, alarm := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateTriggerLevel1
	sm.alarmDuration = 10

	sm.SendEvent(BMXInterruptEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel2 {
		t.Errorf("expected StateTriggerLevel2, got %s", sm.State())
	}

	if !alarm.active {
		t.Error("expected alarm to be active in level 2")
	}

	if alarm.duration != 10*time.Second {
		t.Errorf("expected alarm duration 10s, got %v", alarm.duration)
	}

	if alarm.blinkCalled != 1 {
		t.Errorf("expected hazards to blink once during L1->L2 transition, got %d", alarm.blinkCalled)
	}
}

func TestStateMachine_Level1ToDelayArmedOnTimeout(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateTriggerLevel1

	sm.SendEvent(Level1CheckTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDelayArmed {
		t.Errorf("expected StateDelayArmed, got %s", sm.State())
	}
}

func TestStateMachine_Level2ToWaitingMovement(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateTriggerLevel2
	sm.level2Cycles = 0

	sm.SendEvent(Level2CheckTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateWaitingMovement {
		t.Errorf("expected StateWaitingMovement, got %s", sm.State())
	}
}

func TestStateMachine_Level2ToDisarmedAfterMaxCycles(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateTriggerLevel2
	sm.level2Cycles = maxLevel2Cycles

	sm.SendEvent(Level2CheckTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDisarmed {
		t.Errorf("expected StateDisarmed after max cycles, got %s", sm.State())
	}
}

func TestStateMachine_WaitingMovementRetriggersLevel2(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateWaitingMovement
	sm.level2Cycles = 1

	sm.SendEvent(BMXInterruptEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.level2Cycles != 2 {
		t.Errorf("expected level2Cycles to be 2, got %d", sm.level2Cycles)
	}

	if sm.State() != StateTriggerLevel2 {
		t.Errorf("expected StateTriggerLevel2, got %s", sm.State())
	}
}

func TestStateMachine_WaitingMovementToDisarmedAfterMaxCycles(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateWaitingMovement
	sm.level2Cycles = maxLevel2Cycles - 1

	sm.SendEvent(BMXInterruptEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDisarmed {
		t.Errorf("expected StateDisarmed after max cycles, got %s", sm.State())
	}
}

func TestStateMachine_WaitingMovementToDelayArmed(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateWaitingMovement
	sm.level2Cycles = 2

	sm.SendEvent(Level2CheckTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDelayArmed {
		t.Errorf("expected StateDelayArmed, got %s", sm.State())
	}
}

func TestStateMachine_DisableFromAnyState(t *testing.T) {
	states := []State{
		StateDisarmed,
		StateDelayArmed,
		StateArmed,
		StateTriggerLevel1Wait,
		StateTriggerLevel1,
		StateTriggerLevel2,
		StateWaitingMovement,
	}

	for _, initialState := range states {
		sm, _, _, _, _ := createTestStateMachine()
		ctx := context.Background()

		sm.state = initialState
		sm.alarmEnabled = true

		sm.SendEvent(AlarmModeChangedEvent{Enabled: false})
		sm.handleEvent(ctx, <-sm.events)

		if sm.State() != StateWaitingEnabled {
			t.Errorf("expected StateWaitingEnabled from %s, got %s", initialState, sm.State())
		}

		if sm.alarmEnabled {
			t.Error("expected alarmEnabled to be false")
		}
	}
}

func TestStateMachine_VehicleNotStandbyFromArmedStates(t *testing.T) {
	states := []State{
		StateDelayArmed,
		StateArmed,
		StateTriggerLevel1Wait,
		StateTriggerLevel1,
		StateTriggerLevel2,
		StateWaitingMovement,
	}

	for _, initialState := range states {
		sm, _, _, _, _ := createTestStateMachine()
		ctx := context.Background()

		sm.state = initialState
		sm.vehicleStandby = true
		sm.alarmEnabled = true

		sm.SendEvent(VehicleStateChangedEvent{State: VehicleStateReadyToDrive})
		sm.handleEvent(ctx, <-sm.events)

		if sm.State() != StateDisarmed {
			t.Errorf("expected StateDisarmed from %s on vehicle not standby, got %s", initialState, sm.State())
		}

		if sm.vehicleStandby {
			t.Error("expected vehicleStandby to be false")
		}
	}
}

func TestStateMachine_HornSettingChanged(t *testing.T) {
	sm, _, _, _, alarm := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed

	sm.SendEvent(HornSettingChangedEvent{Enabled: true})
	sm.handleEvent(ctx, <-sm.events)

	if !alarm.hornEnabled {
		t.Error("expected horn to be enabled")
	}

	if sm.State() != StateArmed {
		t.Error("expected state to remain unchanged")
	}
}

func TestStateMachine_AlarmDurationChanged(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed
	sm.alarmDuration = 10

	sm.SendEvent(AlarmDurationChangedEvent{Duration: 30})
	sm.handleEvent(ctx, <-sm.events)

	if sm.alarmDuration != 30 {
		t.Errorf("expected alarm duration to be 30, got %d", sm.alarmDuration)
	}

	if sm.State() != StateArmed {
		t.Error("expected state to remain unchanged")
	}
}

func TestStateMachine_ManualTriggerFromArmed(t *testing.T) {
	sm, _, _, _, alarm := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed
	sm.alarmDuration = 10

	sm.SendEvent(ManualTriggerEvent{Duration: 15})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel2 {
		t.Errorf("expected StateTriggerLevel2, got %s", sm.State())
	}

	if !alarm.active {
		t.Error("expected alarm to be active")
	}
}

func TestStateMachine_StateToStatus(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()

	tests := []struct {
		state    State
		expected string
	}{
		{StateWaitingEnabled, "disabled"},
		{StateDisarmed, "disarmed"},
		{StateDelayArmed, "delay-armed"},
		{StateArmed, "armed"},
		{StateTriggerLevel1Wait, "level-1-triggered"},
		{StateTriggerLevel1, "level-1-triggered"},
		{StateTriggerLevel2, "level-2-triggered"},
		{StateWaitingMovement, "level-2-triggered"},
	}

	for _, tt := range tests {
		result := sm.stateToStatus(tt.state)
		if result != tt.expected {
			t.Errorf("stateToStatus(%s) = %s, expected %s", tt.state, result, tt.expected)
		}
	}
}

func TestStateMachine_EventQueueFull(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()

	for i := 0; i < 150; i++ {
		sm.SendEvent(InitCompleteEvent{})
	}

	if len(sm.events) > 100 {
		t.Error("expected event queue to drop events when full")
	}
}

func TestStateMachine_AlarmStopsOnLevel2Exit(t *testing.T) {
	sm, _, _, _, alarm := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateTriggerLevel2
	alarm.active = true

	sm.SendEvent(VehicleStateChangedEvent{State: VehicleStateReadyToDrive})
	sm.handleEvent(ctx, <-sm.events)

	if alarm.active {
		t.Error("expected alarm to be stopped when exiting level 2")
	}
}

func TestStateMachine_UnauthorizedSeatboxFromArmed(t *testing.T) {
	sm, _, _, _, alarm := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.alarmDuration = 10

	sm.SendEvent(UnauthorizedSeatboxEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel2 {
		t.Errorf("expected StateTriggerLevel2 on unauthorized seatbox, got %s", sm.State())
	}

	if !alarm.active {
		t.Error("expected alarm to be active")
	}
}

func TestStateMachine_UnauthorizedSeatboxFromDelayArmed(t *testing.T) {
	sm, _, _, _, alarm := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateDelayArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.alarmDuration = 10

	sm.SendEvent(UnauthorizedSeatboxEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel2 {
		t.Errorf("expected StateTriggerLevel2 on unauthorized seatbox, got %s", sm.State())
	}

	if !alarm.active {
		t.Error("expected alarm to be active")
	}
}

func TestStateMachine_UnauthorizedSeatboxFromLevel1Wait(t *testing.T) {
	sm, _, _, _, alarm := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateTriggerLevel1Wait
	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.alarmDuration = 10

	sm.SendEvent(UnauthorizedSeatboxEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel2 {
		t.Errorf("expected StateTriggerLevel2 on unauthorized seatbox, got %s", sm.State())
	}

	if !alarm.active {
		t.Error("expected alarm to be active")
	}
}

func TestStateMachine_UnauthorizedSeatboxFromLevel1(t *testing.T) {
	sm, _, _, _, alarm := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateTriggerLevel1
	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.alarmDuration = 10

	sm.SendEvent(UnauthorizedSeatboxEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel2 {
		t.Errorf("expected StateTriggerLevel2 on unauthorized seatbox, got %s", sm.State())
	}

	if !alarm.active {
		t.Error("expected alarm to be active")
	}
}

func TestStateMachine_AuthorizedSeatboxFromDelayArmed(t *testing.T) {
	sm, _, _, inh, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateDelayArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true

	sm.SendEvent(SeatboxOpenedEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateSeatboxAccess {
		t.Errorf("expected StateSeatboxAccess on authorized opening from delay_armed, got %s", sm.State())
	}

	if !inh.acquired {
		t.Error("expected suspend inhibitor to be acquired in seatbox access")
	}

	if sm.preSeatboxState != StateDelayArmed {
		t.Errorf("expected preSeatboxState to be StateDelayArmed, got %s", sm.preSeatboxState)
	}
}

func TestStateMachine_AuthorizedSeatboxAccess(t *testing.T) {
	sm, _, _, inh, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true

	sm.SendEvent(SeatboxOpenedEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateSeatboxAccess {
		t.Errorf("expected StateSeatboxAccess on authorized opening, got %s", sm.State())
	}

	if !inh.acquired {
		t.Error("expected suspend inhibitor to be acquired in seatbox access")
	}

	if sm.preSeatboxState != StateArmed {
		t.Errorf("expected preSeatboxState to be StateArmed, got %s", sm.preSeatboxState)
	}
}

func TestStateMachine_SeatboxAccessToDelayArmed(t *testing.T) {
	sm, _, _, inh, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateSeatboxAccess
	sm.preSeatboxState = StateArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true
	inh.acquired = true

	sm.SendEvent(SeatboxClosedEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDelayArmed {
		t.Errorf("expected StateDelayArmed after seatbox closed, got %s", sm.State())
	}

	if !sm.seatboxLockClosed {
		t.Error("expected seatboxLockClosed to be true")
	}
}

func TestStateMachine_RuntimeDisarmFromArmedStates(t *testing.T) {

	statesWithAlarm := map[State]bool{
		StateTriggerLevel1Wait: true,
		StateTriggerLevel2:     true,
		StateWaitingMovement:   true,
	}

	states := []State{
		StateDelayArmed,
		StateArmed,
		StateTriggerLevel1Wait,
		StateTriggerLevel1,
		StateTriggerLevel2,
		StateWaitingMovement,
	}

	for _, initialState := range states {
		sm, _, _, _, alarm := createTestStateMachine()
		ctx := context.Background()

		sm.state = initialState
		sm.alarmEnabled = true
		sm.vehicleStandby = true
		alarm.active = statesWithAlarm[initialState]

		sm.SendEvent(RuntimeDisarmEvent{})
		sm.handleEvent(ctx, <-sm.events)

		if sm.State() != StateDisarmed {
			t.Errorf("RuntimeDisarm from %s: expected StateDisarmed, got %s", initialState, sm.State())
		}

		if !sm.alarmEnabled {
			t.Errorf("RuntimeDisarm from %s: alarmEnabled should remain true", initialState)
		}

		if statesWithAlarm[initialState] && alarm.active {
			t.Errorf("RuntimeDisarm from %s: alarm should be stopped", initialState)
		}
	}
}

func TestStateMachine_RuntimeDisarmPreservesAlarmEnabled(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true

	sm.SendEvent(RuntimeDisarmEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDisarmed {
		t.Errorf("expected StateDisarmed, got %s", sm.State())
	}

	if !sm.alarmEnabled {
		t.Error("alarmEnabled must remain true after runtime disarm")
	}
}

func TestStateMachine_RuntimeDisarmThenRearmOnStandby(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true

	sm.SendEvent(RuntimeDisarmEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDisarmed {
		t.Fatalf("expected StateDisarmed, got %s", sm.State())
	}

	sm.SendEvent(VehicleStateChangedEvent{State: VehicleStateReadyToDrive})
	sm.handleEvent(ctx, <-sm.events)
	sm.SendEvent(VehicleStateChangedEvent{State: VehicleStateStandby})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDelayArmed {
		t.Errorf("expected StateDelayArmed after returning to standby, got %s", sm.State())
	}
}

func TestStateMachine_RuntimeArmFromDisarmed(t *testing.T) {
	sm, _, _, inh, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateDisarmed
	sm.alarmEnabled = true
	sm.vehicleStandby = false

	sm.SendEvent(RuntimeArmEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDelayArmed {
		t.Errorf("expected StateDelayArmed, got %s", sm.State())
	}

	if !inh.acquired {
		t.Error("expected suspend inhibitor to be acquired")
	}
}

func TestStateMachine_RuntimeArmIgnoredWhenDisabled(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateDisarmed
	sm.alarmEnabled = false

	sm.SendEvent(RuntimeArmEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDisarmed {
		t.Errorf("RuntimeArm with disabled alarm should be ignored, got %s", sm.State())
	}
}

func TestStateMachine_WaitingHibernationDoesNotDisarm(t *testing.T) {
	armedStates := []State{
		StateDelayArmed,
		StateArmed,
		StateTriggerLevel1Wait,
		StateTriggerLevel1,
		StateTriggerLevel2,
		StateWaitingMovement,
		StateSeatboxAccess,
	}

	for _, initialState := range armedStates {
		sm, _, _, _, _ := createTestStateMachine()
		ctx := context.Background()

		sm.state = initialState
		sm.vehicleStandby = true
		sm.alarmEnabled = true

		sm.SendEvent(VehicleStateChangedEvent{State: VehicleStateWaitingHibernation})
		sm.handleEvent(ctx, <-sm.events)

		if sm.State() != initialState {
			t.Errorf("expected to stay in %s on waiting-hibernation, got %s", initialState, sm.State())
		}
	}
}

func TestStateMachine_ShuttingDownDoesNotDisarm(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed
	sm.vehicleStandby = true
	sm.alarmEnabled = true

	sm.SendEvent(VehicleStateChangedEvent{State: VehicleStateShuttingDown})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateArmed {
		t.Errorf("expected to stay in StateArmed on shutting-down, got %s", sm.State())
	}
}

func TestStateMachine_InitToArmedSkipsDelay(t *testing.T) {
	sm, _, pub, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.SendEvent(InitCompleteEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateArmed {
		t.Errorf("expected StateArmed on startup with alarm+standby, got %s", sm.State())
	}

	if pub.lastStatus != "armed" {
		t.Errorf("expected status 'armed', got %s", pub.lastStatus)
	}
}

func TestStateMachine_ArmedUsesAwakeProfileByDefault(t *testing.T) {
	sm, motion, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.SendEvent(InitCompleteEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateArmed {
		t.Fatalf("expected StateArmed, got %s", sm.State())
	}
	if motion.prepareCalls != 0 {
		t.Errorf("expected no PrepareHibernation calls without imminent flag, got %d", motion.prepareCalls)
	}
}

func TestStateMachine_HibernationImminentReprogramsArmed(t *testing.T) {
	sm, motion, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.SendEvent(InitCompleteEvent{})
	sm.handleEvent(ctx, <-sm.events)
	if sm.State() != StateArmed {
		t.Fatalf("expected StateArmed, got %s", sm.State())
	}
	if motion.prepareCalls != 0 {
		t.Fatalf("expected no PrepareHibernation calls before imminent, got %d", motion.prepareCalls)
	}

	sm.SendEvent(HibernationImminentEvent{Imminent: true})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateArmed {
		t.Errorf("expected to stay in StateArmed, got %s", sm.State())
	}
	if !sm.hibernationImminent {
		t.Error("expected hibernationImminent flag to be set")
	}
	if motion.prepareCalls != 1 {
		t.Errorf("expected PrepareHibernation to be called once on imminent=true, got %d", motion.prepareCalls)
	}

	sm.SendEvent(HibernationImminentEvent{Imminent: false})
	sm.handleEvent(ctx, <-sm.events)

	if sm.hibernationImminent {
		t.Error("expected hibernationImminent flag to be cleared")
	}
}

func TestStateMachine_HibernationImminentNoOpWhenIdempotent(t *testing.T) {
	sm, motion, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.SendEvent(InitCompleteEvent{})
	sm.handleEvent(ctx, <-sm.events)
	motion.prepareCalls = 0

	sm.SendEvent(HibernationImminentEvent{Imminent: false})
	sm.handleEvent(ctx, <-sm.events)

	if motion.prepareCalls != 0 {
		t.Errorf("expected no PrepareHibernation call on idempotent imminent=false, got %d", motion.prepareCalls)
	}
}

func TestStateMachine_HibernationImminentBeforeArmedAppliesOnEntry(t *testing.T) {
	sm, motion, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.alarmEnabled = false
	sm.vehicleStandby = true
	sm.SendEvent(InitCompleteEvent{})
	sm.handleEvent(ctx, <-sm.events)
	if sm.State() != StateWaitingEnabled {
		t.Fatalf("expected StateWaitingEnabled, got %s", sm.State())
	}

	sm.SendEvent(HibernationImminentEvent{Imminent: true})
	sm.handleEvent(ctx, <-sm.events)
	if !sm.hibernationImminent {
		t.Fatal("expected hibernationImminent flag to be set in non-armed state")
	}
	prepareBefore := motion.prepareCalls

	sm.SendEvent(AlarmModeChangedEvent{Enabled: true})
	sm.handleEvent(ctx, <-sm.events)
	if sm.State() != StateDelayArmed {
		t.Fatalf("expected StateDelayArmed, got %s", sm.State())
	}
	sm.SendEvent(DelayArmedTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateArmed {
		t.Fatalf("expected StateArmed, got %s", sm.State())
	}
	if motion.prepareCalls != prepareBefore+1 {
		t.Errorf("expected PrepareHibernation to be called on armed entry with hibernationImminent=true, got %d (was %d)", motion.prepareCalls, prepareBefore)
	}
}

func TestStateMachine_UserDisarmClearsWakeFromHibernation(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.wakeFromHibernation = true

	sm.SendEvent(VehicleStateChangedEvent{State: VehicleStateParked})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDisarmed {
		t.Fatalf("expected StateDisarmed, got %s", sm.State())
	}
	if sm.wakeFromHibernation {
		t.Error("expected wakeFromHibernation to be cleared on user-intervention disarm")
	}
}

func TestStateMachine_L2ExhaustionPreservesWakeFromHibernationInDisarmed(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateWaitingMovement
	sm.level2Cycles = maxLevel2Cycles - 1
	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.wakeFromHibernation = true

	sm.SendEvent(BMXInterruptEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDisarmed {
		t.Fatalf("expected StateDisarmed after L2 exhaustion, got %s", sm.State())
	}
	if !sm.wakeFromHibernation {
		t.Error("expected wakeFromHibernation to survive Disarmed entry when vehicle still in stand-by")
	}
}

func TestStateMachine_PostAlarmCooldownRequestsHibernateWhenWakeFlag(t *testing.T) {
	sm, _, _, _, _, power := createTestStateMachineWithPower()
	ctx := context.Background()

	sm.state = StateDisarmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.wakeFromHibernation = true

	sm.SendEvent(PostAlarmCooldownTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if power.hibernateCalled != 1 {
		t.Errorf("expected RequestHibernate to be called once, got %d", power.hibernateCalled)
	}
	if sm.wakeFromHibernation {
		t.Error("expected wakeFromHibernation to be cleared after re-hibernate request")
	}
	if sm.State() != StateArmed {
		t.Errorf("expected StateArmed before hibernation request (so motion-service arms BMX), got %s", sm.State())
	}
}

func TestStateMachine_PostAlarmCooldownRearmsWhenNoWakeFlag(t *testing.T) {
	sm, _, _, _, _, power := createTestStateMachineWithPower()
	ctx := context.Background()

	sm.state = StateDisarmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.wakeFromHibernation = false

	sm.SendEvent(PostAlarmCooldownTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if power.hibernateCalled != 0 {
		t.Errorf("expected RequestHibernate not to be called, got %d", power.hibernateCalled)
	}
	if sm.State() != StateDelayArmed {
		t.Errorf("expected StateDelayArmed after cooldown re-arm, got %s", sm.State())
	}
}

func TestStateMachine_PostAlarmCooldownIgnoredWhenAlarmDisabled(t *testing.T) {
	sm, _, _, _, _, power := createTestStateMachineWithPower()
	ctx := context.Background()

	sm.state = StateDisarmed
	sm.alarmEnabled = false
	sm.vehicleStandby = true
	sm.wakeFromHibernation = true

	sm.SendEvent(PostAlarmCooldownTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if power.hibernateCalled != 0 {
		t.Errorf("expected RequestHibernate not to be called when alarm disabled, got %d", power.hibernateCalled)
	}
	if sm.State() != StateDisarmed {
		t.Errorf("expected to remain in StateDisarmed, got %s", sm.State())
	}
}

func TestStateMachine_ArmedInputTriggerEscalatesToL1Wait(t *testing.T) {
	sm, _, _, inh, alarm := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true

	sm.SendEvent(InputTriggerEvent{Source: TriggerSourceBrakeLeft})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel1Wait {
		t.Errorf("expected StateTriggerLevel1Wait on input trigger, got %s", sm.State())
	}
	if !inh.acquired {
		t.Error("expected inhibitor acquired in L1 wait")
	}
	if alarm.blinkCalled != 1 {
		t.Errorf("expected hazards to blink once entering L1 wait, got %d", alarm.blinkCalled)
	}
}

func TestStateMachine_Level1InputTriggerEscalatesToL2(t *testing.T) {
	sm, _, _, _, alarm := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateTriggerLevel1
	sm.alarmDuration = 10

	sm.SendEvent(InputTriggerEvent{Source: TriggerSourceHandlebarLock})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel2 {
		t.Errorf("expected StateTriggerLevel2, got %s", sm.State())
	}
	if !alarm.active {
		t.Error("expected alarm to be active in level 2")
	}
	if alarm.blinkCalled != 1 {
		t.Errorf("expected hazards to blink once during L1->L2, got %d", alarm.blinkCalled)
	}
}

func TestStateMachine_WaitingMovementInputTriggerEscalates(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateWaitingMovement
	sm.level2Cycles = 1

	sm.SendEvent(InputTriggerEvent{Source: TriggerSourceHornButton})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel2 {
		t.Errorf("expected StateTriggerLevel2, got %s", sm.State())
	}
	if sm.level2Cycles != 2 {
		t.Errorf("expected level2Cycles 2, got %d", sm.level2Cycles)
	}
}

func TestStateMachine_MotionDisabledDropsMotionKeepsInputs(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed
	sm.motionTriggerEnabled = false

	sm.SendEvent(BMXInterruptEvent{})
	sm.handleEvent(ctx, <-sm.events)
	if sm.State() != StateArmed {
		t.Errorf("expected StateArmed with motion disabled, got %s", sm.State())
	}

	sm.SendEvent(InputTriggerEvent{Source: TriggerSourceBrakeLeft})
	sm.handleEvent(ctx, <-sm.events)
	if sm.State() != StateTriggerLevel1Wait {
		t.Errorf("expected StateTriggerLevel1Wait on input trigger, got %s", sm.State())
	}
}

func TestStateMachine_MotionDisabledKeepsWakeStamp(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.motionTriggerEnabled = false
	sm.alarmEnabled = true
	sm.vehicleStandby = true

	sm.SendEvent(BMXInterruptEvent{Data: "wake-hibernation"})
	sm.handleEvent(ctx, <-sm.events)

	if !sm.wakeFromHibernation {
		t.Error("expected wake-from-hibernation to be recorded even with motion disabled")
	}

	sm.SendEvent(InitCompleteEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateArmed {
		t.Errorf("expected StateArmed instead of an L1 escalation, got %s", sm.State())
	}
}

func TestStateMachine_MotionEnabledWakeStampEscalatesOnInit(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.alarmEnabled = true
	sm.vehicleStandby = true

	sm.SendEvent(BMXInterruptEvent{Data: "wake-hibernation"})
	sm.handleEvent(ctx, <-sm.events)
	sm.SendEvent(InitCompleteEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel1Wait {
		t.Errorf("expected StateTriggerLevel1Wait, got %s", sm.State())
	}
}

func TestStateMachine_MotionTriggerSettingChange(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateArmed
	if !sm.motionTriggerEnabled {
		t.Fatal("motion trigger should default to enabled")
	}

	sm.SendEvent(MotionTriggerSettingChangedEvent{Enabled: false})
	sm.handleEvent(ctx, <-sm.events)

	if sm.motionTriggerEnabled {
		t.Error("motion trigger should be disabled after the setting change")
	}
	if sm.State() != StateArmed {
		t.Errorf("state should not change on a setting update, got %s", sm.State())
	}
}

func armForTest(t *testing.T, ctx context.Context, sm *StateMachine) {
	t.Helper()

	sm.state = StateDelayArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true

	sm.SendEvent(DelayArmedTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateArmed {
		t.Fatalf("expected StateArmed, got %s", sm.State())
	}
}

func TestStateMachine_HandlebarTriggersMutedAfterArming(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	armForTest(t, ctx, sm)

	if sm.handlebarSettled {
		t.Fatal("expected the settling window to be open right after arming")
	}
	if _, ok := sm.timers["handlebar_settle"]; !ok {
		t.Error("expected a handlebar_settle timer to be running")
	}

	for _, source := range []TriggerSource{TriggerSourceHandlebarLock, TriggerSourceHandlebarPosition} {
		sm.SendEvent(InputTriggerEvent{Source: source})
		sm.handleEvent(ctx, <-sm.events)

		if sm.State() != StateArmed {
			t.Fatalf("%s should have been dropped inside the settling window, got %s", source, sm.State())
		}
	}

	sm.SendEvent(BMXInterruptEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel1Wait {
		t.Errorf("motion must not be muted by the handlebar window, got %s", sm.State())
	}
}

func TestStateMachine_ButtonTriggersIgnoreHandlebarWindow(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	armForTest(t, ctx, sm)

	sm.SendEvent(InputTriggerEvent{Source: TriggerSourceBrakeLeft})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel1Wait {
		t.Errorf("expected a brake press to escalate during the handlebar window, got %s", sm.State())
	}
}

func TestStateMachine_HandlebarTriggerEscalatesAfterSettling(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	armForTest(t, ctx, sm)

	sm.SendEvent(HandlebarSettleTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if !sm.handlebarSettled {
		t.Fatal("expected the settling window to be closed")
	}
	if sm.State() != StateArmed {
		t.Fatalf("the settle timer must not move the FSM, got %s", sm.State())
	}

	sm.SendEvent(InputTriggerEvent{Source: TriggerSourceHandlebarLock})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel1Wait {
		t.Errorf("expected StateTriggerLevel1Wait after the window closed, got %s", sm.State())
	}
}

func TestStateMachine_HandlebarWindowResetsOnRearm(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	armForTest(t, ctx, sm)

	sm.SendEvent(HandlebarSettleTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)

	sm.SendEvent(VehicleStateChangedEvent{State: VehicleStateParked})
	sm.handleEvent(ctx, <-sm.events)
	if sm.State() != StateDisarmed {
		t.Fatalf("expected StateDisarmed, got %s", sm.State())
	}

	sm.SendEvent(VehicleStateChangedEvent{State: VehicleStateStandby})
	sm.handleEvent(ctx, <-sm.events)
	if sm.State() != StateDelayArmed {
		t.Fatalf("expected StateDelayArmed, got %s", sm.State())
	}

	sm.SendEvent(DelayArmedTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)
	if sm.State() != StateArmed {
		t.Fatalf("expected StateArmed, got %s", sm.State())
	}

	if sm.handlebarSettled {
		t.Error("expected a fresh settling window after rearming")
	}

	sm.SendEvent(InputTriggerEvent{Source: TriggerSourceHandlebarPosition})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateArmed {
		t.Errorf("expected the handlebar trigger to be dropped again, got %s", sm.State())
	}
}
