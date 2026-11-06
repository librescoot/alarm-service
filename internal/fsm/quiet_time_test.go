package fsm

import (
	"context"
	"testing"
	"time"
)

func startTestEpisode(t *testing.T) (*StateMachine, *mockAlarmController) {
	t.Helper()
	sm, _, _, _, alarm := createTestStateMachine()
	t.Cleanup(sm.cleanupTimers)
	sm.state = StateArmed
	sm.alarmEnabled = true
	sm.vehicleStandby = true
	sm.alarmDuration = 30
	sm.handleEvent(context.Background(), BMXInterruptEvent{})
	return sm, alarm
}

func startTestBurst(t *testing.T) (*StateMachine, *mockAlarmController) {
	t.Helper()
	sm, alarm := startTestEpisode(t)
	sm.handleEvent(context.Background(), Level1CooldownTimerEvent{})
	sm.handleEvent(context.Background(), BMXInterruptEvent{})
	return sm, alarm
}

func TestQuietTimeDefault(t *testing.T) {
	sm, _ := startTestEpisode(t)
	if sm.quietTime != 30 {
		t.Fatalf("quiet time default = %d, want 30 seconds", sm.quietTime)
	}
}

func TestShorteningQuietTimeUsesLastDisturbance(t *testing.T) {
	sm, _ := startTestEpisode(t)
	sm.lastDisturbance = time.Now().Add(-2 * time.Second)
	sm.handleEvent(context.Background(), QuietTimeChangedEvent{Duration: 1})
	select {
	case event := <-sm.events:
		if _, ok := event.(QuietTimerEvent); !ok {
			t.Fatalf("expected quiet expiry, got %T", event)
		}
		sm.handleEvent(context.Background(), event)
		if sm.State() != StateArmed {
			t.Fatal("shortening quiet time must use the elapsed quiet interval")
		}
	case <-time.After(time.Second):
		t.Fatal("shortened quiet time was incorrectly restarted from the settings edit")
	}
}

func TestQuietTimeResetsDuringEscalationDelay(t *testing.T) {
	sm, _ := startTestEpisode(t)
	ctx := context.Background()
	first := sm.quietGeneration
	sm.handleEvent(ctx, BMXInterruptEvent{})
	if sm.State() != StateTriggerLevel1Wait || sm.quietGeneration == first {
		t.Fatal("disturbance must reset quiet time without escalating during the delay")
	}
	sm.handleEvent(ctx, QuietTimerEvent{Generation: first})
	if sm.State() != StateTriggerLevel1Wait {
		t.Fatal("stale quiet expiry ended a renewed episode")
	}
	sm.handleEvent(ctx, Level1CooldownTimerEvent{})
	if sm.State() != StateTriggerLevel1 {
		t.Fatal("escalation delay must not reset or end the episode")
	}
	sm.handleEvent(ctx, QuietTimerEvent{Generation: sm.quietGeneration})
	if sm.State() != StateArmed {
		t.Fatal("quiet L1 must return directly to armed")
	}
	if _, exists := sm.timers["delay_armed"]; exists {
		t.Fatal("quiet expiry must not create a blind rearming delay")
	}
}

func TestQuietTimeDoesNotCutAnActiveBurst(t *testing.T) {
	sm, alarm := startTestBurst(t)
	ctx := context.Background()
	sm.handleEvent(ctx, QuietTimerEvent{Generation: sm.quietGeneration})
	if sm.State() != StateTriggerLevel2 || !alarm.active {
		t.Fatal("quiet expiry must allow the running burst to finish")
	}
	sm.handleEvent(ctx, AlarmBurstTimerEvent{Generation: sm.burstGeneration})
	if sm.State() != StateArmed || alarm.active {
		t.Fatal("completed burst after quiet must return directly to armed")
	}
}

func TestDisturbanceDuringBurstRenewsQuietWithoutExtendingBurst(t *testing.T) {
	sm, alarm := startTestBurst(t)
	ctx := context.Background()
	burst := sm.burstGeneration
	quiet := sm.quietGeneration
	sm.handleEvent(ctx, QuietTimerEvent{Generation: quiet})
	sm.handleEvent(ctx, InputTriggerEvent{Source: TriggerSourceBrakeLeft})
	if sm.quietElapsed || sm.quietGeneration == quiet || sm.burstGeneration != burst || alarm.duration != 30*time.Second {
		t.Fatal("disturbance must renew quiet time without restarting the burst")
	}
	sm.handleEvent(ctx, QuietTimerEvent{Generation: quiet})
	sm.handleEvent(ctx, AlarmBurstTimerEvent{Generation: burst})
	if sm.State() != StateWaitingMovement || alarm.active {
		t.Fatal("burst must end silently while waiting for the renewed quiet deadline")
	}
	sm.handleEvent(ctx, QuietTimerEvent{Generation: sm.quietGeneration})
	if sm.State() != StateArmed {
		t.Fatal("quiet waiting state must return directly to armed")
	}
}

func TestWaitingForQuietOnlySoundsOnAnotherDisturbance(t *testing.T) {
	sm, alarm := startTestBurst(t)
	ctx := context.Background()
	oldBurst := sm.burstGeneration
	sm.handleEvent(ctx, AlarmBurstTimerEvent{Generation: oldBurst})
	if sm.State() != StateWaitingMovement || alarm.active {
		t.Fatal("waiting must not automatically start another burst")
	}
	sm.handleEvent(ctx, UnauthorizedSeatboxEvent{})
	if sm.State() != StateTriggerLevel2 || !alarm.active || sm.level2Cycles != 2 {
		t.Fatal("another accepted disturbance must start the next burst")
	}
	sm.handleEvent(ctx, AlarmBurstTimerEvent{Generation: oldBurst})
	if sm.State() != StateTriggerLevel2 || !alarm.active {
		t.Fatal("a queued old burst expiry must not stop the next burst")
	}
}

func TestContinuousDisturbancesReachBurstSafetyLimit(t *testing.T) {
	sm, alarm := startTestBurst(t)
	ctx := context.Background()
	for burst := 1; burst <= maxLevel2Cycles; burst++ {
		if !alarm.active || sm.level2Cycles != burst {
			t.Fatalf("expected active burst %d, got %d", burst, sm.level2Cycles)
		}
		sm.handleEvent(ctx, BMXInterruptEvent{})
		sm.handleEvent(ctx, AlarmBurstTimerEvent{Generation: sm.burstGeneration})
		if burst < maxLevel2Cycles {
			if sm.State() != StateWaitingMovement || alarm.active {
				t.Fatal("burst completion must stop outputs without an extra fixed wait")
			}
			sm.handleEvent(ctx, BMXInterruptEvent{})
		}
	}
	if sm.State() != StateDisarmed || alarm.active {
		t.Fatal("continuous disturbances must not exceed the maximum burst count")
	}
	if _, exists := sm.timers["post_alarm_cooldown"]; !exists {
		t.Fatal("burst exhaustion must retain the bounded post-alarm cooldown")
	}
}

func TestQuietTimeLiveSettingAndInvalidValues(t *testing.T) {
	sm, _ := startTestEpisode(t)
	ctx := context.Background()
	last := sm.lastDisturbance
	generation := sm.quietGeneration
	sm.handleEvent(ctx, QuietTimeChangedEvent{Duration: 60})
	if sm.quietTime != 60 || sm.lastDisturbance != last || sm.quietGeneration == generation {
		t.Fatal("live change must reschedule against the last disturbance")
	}
	generation = sm.quietGeneration
	for _, seconds := range []int{-1, 0, 301} {
		sm.handleEvent(ctx, QuietTimeChangedEvent{Duration: seconds})
		if sm.quietTime != 60 || sm.quietGeneration != generation {
			t.Fatal("invalid quiet time must leave the active timer unchanged")
		}
	}
}

func TestDisabledTriggersDoNotExtendQuietTime(t *testing.T) {
	sm, _ := startTestEpisode(t)
	ctx := context.Background()
	generation := sm.quietGeneration
	sm.handleEvent(ctx, MotionTriggerSettingChangedEvent{Enabled: false})
	sm.handleEvent(ctx, BMXInterruptEvent{})
	sm.handlebarSettled = false
	sm.handleEvent(ctx, InputTriggerEvent{Source: TriggerSourceHandlebarLock})
	if sm.quietGeneration != generation {
		t.Fatal("filtered triggers must not keep an episode open")
	}
}

func TestLeavingEpisodeInvalidatesQuietExpiry(t *testing.T) {
	for _, event := range []Event{
		VehicleStateChangedEvent{State: VehicleStateParked},
		AlarmModeChangedEvent{Enabled: false},
		RuntimeDisarmEvent{},
		RuntimeStopEvent{},
		UMSModeChangedEvent{Active: true},
		SeatboxOpenedEvent{},
	} {
		t.Run(event.Type(), func(t *testing.T) {
			sm, alarm := startTestEpisode(t)
			ctx := context.Background()
			if _, seatbox := event.(SeatboxOpenedEvent); !seatbox {
				sm.handleEvent(ctx, Level1CooldownTimerEvent{})
				sm.handleEvent(ctx, BMXInterruptEvent{})
			}
			generation := sm.quietGeneration
			sm.handleEvent(ctx, event)
			state := sm.State()
			sm.handleEvent(ctx, QuietTimerEvent{Generation: generation})
			if sm.State() != state || alarm.active {
				t.Fatal("leaving the episode must stop outputs and ignore its queued expiry")
			}
			if _, exists := sm.timers["quiet"]; exists {
				t.Fatal("episode quiet timer was not stopped")
			}
		})
	}
}
