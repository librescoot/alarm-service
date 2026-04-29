package fsm

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"
)

// Mock implementations for testing
type mockBMXClient struct {
	lastConfig       SensorConfig
	interruptPin     InterruptPin
	interruptEnabled bool
	resetCalled      int
	interruptStatus  bool
}

func (m *mockBMXClient) ConfigureSensor(ctx context.Context, cfg SensorConfig) error {
	m.lastConfig = cfg
	return nil
}

func (m *mockBMXClient) SetInterruptPin(ctx context.Context, pin InterruptPin) error {
	m.interruptPin = pin
	return nil
}

func (m *mockBMXClient) SoftReset(ctx context.Context) error {
	m.resetCalled++
	return nil
}

func (m *mockBMXClient) EnableInterrupt(ctx context.Context) error {
	m.interruptEnabled = true
	return nil
}

func (m *mockBMXClient) DisableInterrupt(ctx context.Context) error {
	m.interruptEnabled = false
	return nil
}

func (m *mockBMXClient) CheckInterruptStatus(ctx context.Context) (bool, error) {
	return m.interruptStatus, nil
}

type mockStatusPublisher struct {
	lastStatus string
}

func (m *mockStatusPublisher) PublishStatus(status string) error {
	m.lastStatus = status
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

func createTestStateMachine() (*StateMachine, *mockBMXClient, *mockStatusPublisher, *mockSuspendInhibitor, *mockAlarmController) {
	bmx := &mockBMXClient{}
	pub := &mockStatusPublisher{}
	inh := &mockSuspendInhibitor{}
	alarm := &mockAlarmController{}
	power := &mockPowerCommander{}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	sm := New(bmx, pub, inh, alarm, power, 10, log)
	return sm, bmx, pub, inh, alarm
}

func TestStateMachine_InitialState(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()

	if sm.State() != StateInit {
		t.Errorf("expected initial state to be StateInit, got %s", sm.State())
	}
}

func TestStateMachine_InitToWaitingEnabled(t *testing.T) {
	sm, bmx, pub, _, _ := createTestStateMachine()
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

	if bmx.interruptEnabled {
		t.Error("expected interrupt to be disabled in waiting_enabled state")
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
	sm, bmx, pub, _, _ := createTestStateMachine()
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

	if !bmx.interruptEnabled {
		t.Error("expected interrupt to be enabled in armed state")
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
	sm, bmx, _, inh, _ := createTestStateMachine()
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

	if !bmx.lastConfig.AnyMotion {
		t.Error("expected any-motion mode in armed state")
	}

	if !bmx.interruptEnabled {
		t.Error("expected interrupt to be enabled in armed state")
	}
}

func TestStateMachine_ArmedToTriggerLevel1Wait(t *testing.T) {
	sm, bmx, _, inh, alarm := createTestStateMachine()
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

	if bmx.resetCalled == 0 {
		t.Error("expected BMX to be reset on entering level 1 wait")
	}

	if alarm.blinkCalled != 1 {
		t.Errorf("expected hazards to blink once, got %d blinks", alarm.blinkCalled)
	}
}

func TestStateMachine_Level1WaitToLevel1(t *testing.T) {
	sm, bmx, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateTriggerLevel1Wait

	sm.SendEvent(Level1CooldownTimerEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateTriggerLevel1 {
		t.Errorf("expected StateTriggerLevel1, got %s", sm.State())
	}

	if bmx.lastConfig.AnyMotion {
		t.Error("expected slow-motion mode in level 1 state")
	}

	if !bmx.interruptEnabled {
		t.Error("expected interrupt to be enabled in level 1")
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
	sm.level2Cycles = 4

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
	sm.level2Cycles = 3

	sm.SendEvent(BMXInterruptEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDisarmed {
		t.Errorf("expected StateDisarmed after 4 cycles, got %s", sm.State())
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

func TestStateMachine_BMXConfigurationInStates(t *testing.T) {
	tests := []struct {
		state           State
		expectedPin     InterruptPin
		expectedAnyMot  bool
	}{
		{StateInit, InterruptPinINT2, false},
		{StateWaitingEnabled, InterruptPinINT2, false},
		{StateDisarmed, InterruptPinNone, false},
		{StateDelayArmed, InterruptPinINT2, false},
		{StateArmed, InterruptPinBoth, true},
		{StateTriggerLevel1, InterruptPinBoth, false},
	}

	for _, tt := range tests {
		sm, bmx, _, _, _ := createTestStateMachine()
		ctx := context.Background()

		sm.state = tt.state
		sm.enterState(ctx, tt.state)

		if bmx.interruptPin != tt.expectedPin {
			t.Errorf("state %s: expected pin %s, got %s", tt.state, tt.expectedPin, bmx.interruptPin)
		}

		if bmx.lastConfig.AnyMotion != tt.expectedAnyMot {
			t.Errorf("state %s: expected AnyMotion=%v, got %v", tt.state, tt.expectedAnyMot, bmx.lastConfig.AnyMotion)
		}
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

func TestStateMachine_AuthorizedSeatboxAccess(t *testing.T) {
	sm, bmx, _, inh, _ := createTestStateMachine()
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

	if bmx.interruptEnabled {
		t.Error("expected interrupt to be disabled during seatbox access")
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
	// statesWithAlarm: alarm controller is running in these states (exit handler calls Stop)
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

	// alarmEnabled must not be touched — re-arm on next standby should work
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

	// Simulate scooter going active then returning to standby
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
	sm.vehicleStandby = false // not in standby — arm forced anyway

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
	sm, bmx, pub, _, _ := createTestStateMachine()
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

	if !bmx.interruptEnabled {
		t.Error("expected interrupt to be enabled in armed state")
	}
}

func TestStateMachine_ArmedUsesAwakeProfileByDefault(t *testing.T) {
	sm, bmx, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.SendEvent(InitCompleteEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateArmed {
		t.Fatalf("expected StateArmed, got %s", sm.State())
	}
	if bmx.lastConfig != sensorArmed {
		t.Errorf("expected awake-armed profile %+v, got %+v", sensorArmed, bmx.lastConfig)
	}
}

func TestStateMachine_HibernationImminentReprogramsArmed(t *testing.T) {
	sm, bmx, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.SendEvent(InitCompleteEvent{})
	sm.handleEvent(ctx, <-sm.events)
	if sm.State() != StateArmed {
		t.Fatalf("expected StateArmed, got %s", sm.State())
	}
	if bmx.lastConfig != sensorArmed {
		t.Fatalf("expected awake profile before imminent, got %+v", bmx.lastConfig)
	}

	sm.SendEvent(HibernationImminentEvent{Imminent: true})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateArmed {
		t.Errorf("expected to stay in StateArmed, got %s", sm.State())
	}
	if !sm.hibernationImminent {
		t.Error("expected hibernationImminent flag to be set")
	}
	if bmx.lastConfig != sensorArmedHibernation {
		t.Errorf("expected hibernation-armed profile %+v, got %+v", sensorArmedHibernation, bmx.lastConfig)
	}

	sm.SendEvent(HibernationImminentEvent{Imminent: false})
	sm.handleEvent(ctx, <-sm.events)

	if sm.hibernationImminent {
		t.Error("expected hibernationImminent flag to be cleared")
	}
	if bmx.lastConfig != sensorArmed {
		t.Errorf("expected awake-armed profile after running, got %+v", bmx.lastConfig)
	}
}

func TestStateMachine_HibernationImminentNoOpWhenIdempotent(t *testing.T) {
	sm, bmx, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.SendEvent(InitCompleteEvent{})
	sm.handleEvent(ctx, <-sm.events)
	bmx.lastConfig = SensorConfig{}

	sm.SendEvent(HibernationImminentEvent{Imminent: false})
	sm.handleEvent(ctx, <-sm.events)

	if (bmx.lastConfig != SensorConfig{}) {
		t.Errorf("expected no reprogram on idempotent imminent=false, got %+v", bmx.lastConfig)
	}
}

func TestStateMachine_HibernationImminentBeforeArmedAppliesOnEntry(t *testing.T) {
	sm, bmx, _, _, _ := createTestStateMachine()
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
	if bmx.lastConfig != sensorArmedHibernation {
		t.Errorf("expected hibernation-armed profile on armed entry, got %+v", bmx.lastConfig)
	}
}
