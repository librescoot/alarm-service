package fsm

import (
	"context"
	"testing"
	"time"
)

func TestTimerDeliveryWaitsForQueueCapacity(t *testing.T) {
	sm, _ := startTestBurst(t)
	for i := 0; i < cap(sm.events); i++ {
		sm.SendEvent(InputTriggerEvent{Source: TriggerSourceBrakeLeft})
	}
	delivered := make(chan struct{})
	go func() {
		sm.SendEvent(AlarmBurstTimerEvent{Generation: sm.burstGeneration})
		close(delivered)
	}()
	select {
	case <-delivered:
		t.Fatal("full queue must not discard timer events")
	case <-time.After(20 * time.Millisecond):
	}
	<-sm.events
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("timer event was not delivered when space became available")
	}
	for len(sm.events) > 0 {
		sm.handleEvent(context.Background(), <-sm.events)
	}
	if sm.State() != StateWaitingMovement {
		t.Fatalf("state = %s, want waiting_movement", sm.State())
	}
}

func TestStaleLevelOneTimerCannotStartNewCue(t *testing.T) {
	sm, alarm := startTestEpisode(t)
	ctx := context.Background()
	stale := timerExpiry{name: "level1_trigger_delay", generation: sm.timerGeneration["level1_trigger_delay"], event: Level1TriggerDelayTimerEvent{}}
	sm.handleEvent(ctx, VehicleStateChangedEvent{State: VehicleStateParked})
	sm.handleEvent(ctx, VehicleStateChangedEvent{State: VehicleStateStandby})
	sm.handleEvent(ctx, DelayArmedTimerEvent{})
	sm.handleEvent(ctx, BMXInterruptEvent{})
	sm.handleEvent(ctx, stale)
	if alarm.blinkCalled != 0 {
		t.Fatal("previous episode's queued timer started the new warning early")
	}
	sm.handleEvent(ctx, timerExpiry{name: "level1_trigger_delay", generation: sm.timerGeneration["level1_trigger_delay"], event: Level1TriggerDelayTimerEvent{}})
	if alarm.blinkCalled != 1 {
		t.Fatal("current episode's timer did not start the warning")
	}
}

func TestEventDeliveryStopsOnShutdown(t *testing.T) {
	sm, _, _, _, _ := createTestStateMachine()
	ctx, cancel := context.WithCancel(context.Background())
	go sm.Run(ctx)
	cancel()
	select {
	case <-sm.done:
	case <-time.After(time.Second):
		t.Fatal("state machine did not stop")
	}
	for i := 0; i < cap(sm.events); i++ {
		sm.events <- InitCompleteEvent{}
	}
	returned := make(chan struct{})
	go func() { sm.SendEvent(InitCompleteEvent{}); close(returned) }()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("event delivery blocked after shutdown")
	}
}
