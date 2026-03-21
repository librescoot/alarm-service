package fsm

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/librescoot/librefsm"
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

// startTestSM creates and starts a StateMachine, returning it and mocks.
// The FSM is started so events can be processed via SendSync.
func startTestSM(t *testing.T) (*StateMachine, *mockBMXClient, *mockStatusPublisher, *mockSuspendInhibitor, *mockAlarmController, context.CancelFunc) {
	t.Helper()
	bmx := &mockBMXClient{}
	pub := &mockStatusPublisher{}
	inh := &mockSuspendInhibitor{}
	alarm := &mockAlarmController{}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	sm := New(bmx, pub, inh, alarm, 10, log)

	ctx, cancel := context.WithCancel(context.Background())
	go sm.Run(ctx)
	// Give the FSM goroutine time to start and enter init state
	time.Sleep(10 * time.Millisecond)

	return sm, bmx, pub, inh, alarm, cancel
}

func sendSync(t *testing.T, sm *StateMachine, ev librefsm.Event) {
	t.Helper()
	if err := sm.machine.SendSync(ev); err != nil {
		t.Fatalf("SendSync(%s) failed: %v", ev.ID, err)
	}
}

func assertState(t *testing.T, sm *StateMachine, expected librefsm.StateID) {
	t.Helper()
	got := sm.State()
	if got != expected {
		t.Errorf("expected state %s, got %s", expected, got)
	}
}

func TestInitialState(t *testing.T) {
	sm, _, _, _, _, cancel := startTestSM(t)
	defer cancel()
	assertState(t, sm, StateStarting)
}

func TestInitToWaitingEnabled(t *testing.T) {
	sm, bmx, pub, _, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmDisabled})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})

	assertState(t, sm, StateWaitingEnabled)

	if pub.lastStatus != "disabled" {
		t.Errorf("expected status 'disabled', got %s", pub.lastStatus)
	}
	if bmx.interruptEnabled {
		t.Error("expected interrupt to be disabled")
	}
}

func TestInitToDisarmed(t *testing.T) {
	sm, _, pub, _, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})

	assertState(t, sm, StateDisarmed)
	if pub.lastStatus != "disarmed" {
		t.Errorf("expected status 'disarmed', got %s", pub.lastStatus)
	}
}

func TestInitToArmedWhenStandby(t *testing.T) {
	sm, bmx, pub, _, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})

	assertState(t, sm, StateArmed)
	if pub.lastStatus != "armed" {
		t.Errorf("expected status 'armed', got %s", pub.lastStatus)
	}
	if !bmx.interruptEnabled {
		t.Error("expected interrupt to be enabled")
	}
}

func TestDisarmedToDelayArmed(t *testing.T) {
	sm, _, _, inh, _, cancel := startTestSM(t)
	defer cancel()

	// Get to Disarmed first
	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	assertState(t, sm, StateDisarmed)

	// Vehicle goes to standby
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	assertState(t, sm, StateDelayArmed)

	if !inh.acquired {
		t.Error("expected suspend inhibitor to be acquired")
	}
}

func TestArmedToTriggerLevel1Wait(t *testing.T) {
	sm, bmx, _, inh, alarm, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	assertState(t, sm, StateArmed)

	sendSync(t, sm, librefsm.Event{ID: EvBMXInterrupt})
	assertState(t, sm, StateTriggerLevel1Wait)

	if !inh.acquired {
		t.Error("expected suspend inhibitor to be acquired")
	}
	if bmx.resetCalled == 0 {
		t.Error("expected BMX to be reset")
	}
	if alarm.blinkCalled != 1 {
		t.Errorf("expected hazards to blink once, got %d", alarm.blinkCalled)
	}
}

func TestLevel1ToLevel2OnMovement(t *testing.T) {
	sm, _, _, _, alarm, cancel := startTestSM(t)
	defer cancel()

	// Get to Armed
	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})

	// Armed → L1Wait
	sendSync(t, sm, librefsm.Event{ID: EvBMXInterrupt})
	assertState(t, sm, StateTriggerLevel1Wait)

	// L1Wait → L1 (via cooldown timeout)
	sendSync(t, sm, librefsm.Event{ID: EvL1CooldownTimeout})
	assertState(t, sm, StateTriggerLevel1)

	alarm.blinkCalled = 0
	// L1 → L2 on movement
	sendSync(t, sm, librefsm.Event{ID: EvBMXInterrupt})
	assertState(t, sm, StateTriggerLevel2)

	if !alarm.active {
		t.Error("expected alarm to be active")
	}
	if alarm.blinkCalled != 1 {
		t.Errorf("expected hazards to blink during L1→L2, got %d", alarm.blinkCalled)
	}
}

func TestLevel1ToDelayArmedOnTimeout(t *testing.T) {
	sm, _, _, _, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	sendSync(t, sm, librefsm.Event{ID: EvBMXInterrupt})
	sendSync(t, sm, librefsm.Event{ID: EvL1CooldownTimeout})
	assertState(t, sm, StateTriggerLevel1)

	sendSync(t, sm, librefsm.Event{ID: EvL1CheckTimeout})
	assertState(t, sm, StateDelayArmed)
}

func TestLevel2ToWaitingMovement(t *testing.T) {
	sm, _, _, _, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	sendSync(t, sm, librefsm.Event{ID: EvBMXInterrupt})
	sendSync(t, sm, librefsm.Event{ID: EvL1CooldownTimeout})
	sendSync(t, sm, librefsm.Event{ID: EvBMXInterrupt})
	assertState(t, sm, StateTriggerLevel2)

	sendSync(t, sm, librefsm.Event{ID: EvL2CheckTimeout})
	assertState(t, sm, StateWaitingMovement)
}

func TestWaitingMovementRetriggersLevel2(t *testing.T) {
	sm, _, _, _, _, cancel := startTestSM(t)
	defer cancel()

	// Get to WaitingMovement
	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	sendSync(t, sm, librefsm.Event{ID: EvBMXInterrupt})
	sendSync(t, sm, librefsm.Event{ID: EvL1CooldownTimeout})
	sendSync(t, sm, librefsm.Event{ID: EvBMXInterrupt})
	sendSync(t, sm, librefsm.Event{ID: EvL2CheckTimeout})
	assertState(t, sm, StateWaitingMovement)

	sendSync(t, sm, librefsm.Event{ID: EvBMXInterrupt})
	assertState(t, sm, StateTriggerLevel2)
}

func TestWaitingMovementToDelayArmed(t *testing.T) {
	sm, _, _, _, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	sendSync(t, sm, librefsm.Event{ID: EvBMXInterrupt})
	sendSync(t, sm, librefsm.Event{ID: EvL1CooldownTimeout})
	sendSync(t, sm, librefsm.Event{ID: EvBMXInterrupt})
	sendSync(t, sm, librefsm.Event{ID: EvL2CheckTimeout})
	assertState(t, sm, StateWaitingMovement)

	sendSync(t, sm, librefsm.Event{ID: EvWaitingTimeout})
	assertState(t, sm, StateDelayArmed)
}

func TestDisableFromArmedStates(t *testing.T) {
	// For each armed state, verify EvAlarmDisabled → WaitingEnabled
	sm, _, _, _, _, cancel := startTestSM(t)
	defer cancel()

	// Get to Armed
	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	assertState(t, sm, StateArmed)

	sendSync(t, sm, librefsm.Event{ID: EvAlarmDisabled})
	assertState(t, sm, StateWaitingEnabled)
}

func TestVehicleActiveDisarms(t *testing.T) {
	sm, _, _, _, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	assertState(t, sm, StateArmed)

	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateReadyToDrive})
	assertState(t, sm, StateDisarmed)
}

func TestManualTriggerFromArmed(t *testing.T) {
	sm, _, _, _, alarm, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	assertState(t, sm, StateArmed)

	sendSync(t, sm, librefsm.Event{ID: EvManualTrigger})
	assertState(t, sm, StateTriggerLevel2)
	if !alarm.active {
		t.Error("expected alarm to be active")
	}
}

func TestUnauthorizedSeatboxFromArmed(t *testing.T) {
	sm, _, _, _, alarm, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	assertState(t, sm, StateArmed)

	sendSync(t, sm, librefsm.Event{ID: EvUnauthorizedSeatbox})
	assertState(t, sm, StateTriggerLevel2)
	if !alarm.active {
		t.Error("expected alarm to be active")
	}
}

func TestAuthorizedSeatboxAccess(t *testing.T) {
	sm, bmx, _, inh, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	assertState(t, sm, StateArmed)

	sendSync(t, sm, librefsm.Event{ID: EvSeatboxOpened})
	assertState(t, sm, StateSeatboxAccess)
	if !inh.acquired {
		t.Error("expected inhibitor acquired")
	}
	if bmx.interruptEnabled {
		t.Error("expected interrupt disabled during seatbox access")
	}
}

func TestSeatboxAccessToDelayArmed(t *testing.T) {
	sm, _, _, _, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	sendSync(t, sm, librefsm.Event{ID: EvSeatboxOpened})
	assertState(t, sm, StateSeatboxAccess)

	sendSync(t, sm, librefsm.Event{ID: EvSeatboxClosed})
	assertState(t, sm, StateDelayArmed)
}

func TestRuntimeDisarmFromArmed(t *testing.T) {
	sm, _, _, _, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	assertState(t, sm, StateArmed)

	sendSync(t, sm, librefsm.Event{ID: EvRuntimeDisarm})
	assertState(t, sm, StateDisarmed)

	// alarmEnabled preserved — re-arm on next standby should work
	if !sm.alarmEnabled {
		t.Error("alarmEnabled must remain true after runtime disarm")
	}
}

func TestRuntimeDisarmFromSeatboxAccess(t *testing.T) {
	sm, _, _, _, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	sendSync(t, sm, librefsm.Event{ID: EvSeatboxOpened})
	assertState(t, sm, StateSeatboxAccess)

	sendSync(t, sm, librefsm.Event{ID: EvRuntimeDisarm})
	assertState(t, sm, StateDisarmed)
}

func TestRuntimeArmFromDisarmed(t *testing.T) {
	sm, _, _, inh, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	assertState(t, sm, StateDisarmed)

	sendSync(t, sm, librefsm.Event{ID: EvRuntimeArm})
	assertState(t, sm, StateDelayArmed)
	if !inh.acquired {
		t.Error("expected inhibitor acquired")
	}
}

func TestRuntimeArmIgnoredWhenDisabled(t *testing.T) {
	sm, _, _, _, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmDisabled})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	assertState(t, sm, StateWaitingEnabled)

	// Enable to get to Disarmed
	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	assertState(t, sm, StateDisarmed)

	// Disable again
	sendSync(t, sm, librefsm.Event{ID: EvAlarmDisabled})
	assertState(t, sm, StateWaitingEnabled)

	// RuntimeArm shouldn't work from WaitingEnabled (alarm disabled)
	sm.machine.Send(librefsm.Event{ID: EvRuntimeArm})
	time.Sleep(10 * time.Millisecond)
	assertState(t, sm, StateWaitingEnabled)
}

func TestWaitingHibernationDoesNotDisarm(t *testing.T) {
	sm, _, _, _, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	assertState(t, sm, StateArmed)

	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateWaitingHibernation})
	assertState(t, sm, StateArmed)
}

func TestShuttingDownDoesNotDisarm(t *testing.T) {
	sm, _, _, _, _, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	assertState(t, sm, StateArmed)

	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateShuttingDown})
	assertState(t, sm, StateArmed)
}

func TestStateToStatus(t *testing.T) {
	tests := []struct {
		state    librefsm.StateID
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
		result := stateToStatus(tt.state)
		if result != tt.expected {
			t.Errorf("stateToStatus(%s) = %s, expected %s", tt.state, result, tt.expected)
		}
	}
}

func TestAlarmStopsOnLevel2Exit(t *testing.T) {
	sm, _, _, _, alarm, cancel := startTestSM(t)
	defer cancel()

	sendSync(t, sm, librefsm.Event{ID: EvAlarmEnabled})
	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateStandby})
	sendSync(t, sm, librefsm.Event{ID: EvInitComplete})
	sendSync(t, sm, librefsm.Event{ID: EvBMXInterrupt})
	sendSync(t, sm, librefsm.Event{ID: EvL1CooldownTimeout})
	sendSync(t, sm, librefsm.Event{ID: EvBMXInterrupt})
	assertState(t, sm, StateTriggerLevel2)
	if !alarm.active {
		t.Fatal("expected alarm active in L2")
	}

	sendSync(t, sm, librefsm.Event{ID: EvVehicleState, Payload: VehicleStateReadyToDrive})
	assertState(t, sm, StateDisarmed)
	if alarm.active {
		t.Error("expected alarm stopped after L2 exit")
	}
}
