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
	sensitivity       Sensitivity
	interruptPin      InterruptPin
	interruptEnabled  bool
	resetCalled       int
}

func (m *mockBMXClient) SetSensitivity(ctx context.Context, sens Sensitivity) error {
	m.sensitivity = sens
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

type mockStatusPublisher struct {
	lastStatus string
}

func (m *mockStatusPublisher) PublishStatus(ctx context.Context, status string) error {
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
	active       bool
	duration     time.Duration
	hornEnabled  bool
	blinkCalled  int
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

func createTestStateMachine() (*StateMachine, *mockBMXClient, *mockStatusPublisher, *mockSuspendInhibitor, *mockAlarmController) {
	bmx := &mockBMXClient{}
	pub := &mockStatusPublisher{}
	inh := &mockSuspendInhibitor{}
	alarm := &mockAlarmController{}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	sm := New(bmx, pub, inh, alarm, 10, log)
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

func TestStateMachine_InitToDelayArmed(t *testing.T) {
	sm, _, pub, inh, _ := createTestStateMachine()
	ctx := context.Background()

	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.SendEvent(InitCompleteEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.State() != StateDelayArmed {
		t.Errorf("expected StateDelayArmed, got %s", sm.State())
	}

	if pub.lastStatus != "delay-armed" {
		t.Errorf("expected status 'delay-armed', got %s", pub.lastStatus)
	}

	if !inh.acquired {
		t.Error("expected suspend inhibitor to be acquired in delay_armed state")
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

	if bmx.sensitivity != SensitivityMedium {
		t.Errorf("expected sensitivity MEDIUM, got %s", bmx.sensitivity)
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

	if bmx.sensitivity != SensitivityMedium {
		t.Errorf("expected sensitivity MEDIUM, got %s", bmx.sensitivity)
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

func TestStateMachine_WaitingMovementCycleIncrement(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateWaitingMovement
	sm.level2Cycles = 1

	sm.SendEvent(MajorMovementEvent{})
	sm.handleEvent(ctx, <-sm.events)

	if sm.level2Cycles != 2 {
		t.Errorf("expected level2Cycles to be 2, got %d", sm.level2Cycles)
	}

	if sm.State() != StateWaitingMovement {
		t.Errorf("expected to stay in StateWaitingMovement, got %s", sm.State())
	}
}

func TestStateMachine_WaitingMovementToDisarmedAfterMaxCycles(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx := context.Background()

	sm.state = StateWaitingMovement
	sm.level2Cycles = 3

	sm.SendEvent(MajorMovementEvent{})
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
		state        State
		expectedPin  InterruptPin
		expectedSens Sensitivity
	}{
		{StateInit, InterruptPinINT2, SensitivityLow},
		{StateWaitingEnabled, InterruptPinINT2, SensitivityLow},
		{StateDisarmed, InterruptPinNone, SensitivityLow},
		{StateDelayArmed, InterruptPinINT2, SensitivityLow},
		{StateArmed, InterruptPinNone, SensitivityMedium},
		{StateTriggerLevel1, InterruptPinNone, SensitivityMedium},
	}

	for _, tt := range tests {
		sm, bmx, _, _, _ := createTestStateMachine()
		ctx := context.Background()

		sm.state = tt.state
		sm.enterState(ctx, tt.state)

		if bmx.interruptPin != tt.expectedPin {
			t.Errorf("state %s: expected pin %s, got %s", tt.state, tt.expectedPin, bmx.interruptPin)
		}

		if bmx.sensitivity != tt.expectedSens {
			t.Errorf("state %s: expected sensitivity %s, got %s", tt.state, tt.expectedSens, bmx.sensitivity)
		}
	}
}
