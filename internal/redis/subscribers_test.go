package redis

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"alarm-service/internal/fsm"
)

type fakeEventSink struct {
	mu     sync.Mutex
	events []fsm.Event
	state  fsm.State
}

func (f *fakeEventSink) SendEvent(e fsm.Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}
func (f *fakeEventSink) State() fsm.State { return f.state }

func (f *fakeEventSink) snapshot() []fsm.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fsm.Event(nil), f.events...)
}

func newTestSubscriber() (*Subscriber, *fakeEventSink) {
	sink := &fakeEventSink{}
	s := &Subscriber{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		sm:  sink,
	}
	s.seatboxTriggerEnabled.Store(true)
	s.buttonsTriggerEnabled.Store(true)
	s.handlebarTriggerEnabled.Store(true)
	s.handlebarPositionDwell = testDwell
	return s, sink
}

const testDwell = 30 * time.Millisecond

func pastDwell() { time.Sleep(4 * testDwell) }

func TestParseButtonPayload(t *testing.T) {
	cases := []struct {
		payload    string
		wantSource fsm.TriggerSource
		wantEdge   string
		wantOK     bool
	}{
		{"seatbox:on", fsm.TriggerSourceSeatboxButton, "on", true},
		{"seatbox:off", fsm.TriggerSourceSeatboxButton, "off", true},
		{"horn:on", fsm.TriggerSourceHornButton, "on", true},
		{"horn:off", fsm.TriggerSourceHornButton, "off", true},
		{"brake:left:on", fsm.TriggerSourceBrakeLeft, "on", true},
		{"brake:left:off", fsm.TriggerSourceBrakeLeft, "off", true},
		{"brake:right:on", fsm.TriggerSourceBrakeRight, "on", true},
		{"brake:right:off", fsm.TriggerSourceBrakeRight, "off", true},

		{"blinker:left:on", fsm.TriggerSourceUnknown, "", false},
		{"blinker:right:off", fsm.TriggerSourceUnknown, "", false},

		{"", fsm.TriggerSourceUnknown, "", false},
		{"nope", fsm.TriggerSourceUnknown, "", false},
		{"brake::on", fsm.TriggerSourceUnknown, "", false},
		{"brake:middle:on", fsm.TriggerSourceUnknown, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.payload, func(t *testing.T) {
			src, edge, ok := parseButtonPayload(tc.payload)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if src != tc.wantSource {
				t.Errorf("source = %s, want %s", src, tc.wantSource)
			}
			if edge != tc.wantEdge {
				t.Errorf("edge = %q, want %q", edge, tc.wantEdge)
			}
		})
	}
}

func TestHandleButtonEvent_PressTriggersReleaseDoesNot(t *testing.T) {
	s, sink := newTestSubscriber()

	if err := s.handleButtonEvent("brake:left:on"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := s.handleButtonEvent("brake:left:off"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d: %v", len(sink.events), sink.events)
	}
	ev, ok := sink.events[0].(fsm.InputTriggerEvent)
	if !ok {
		t.Fatalf("expected InputTriggerEvent, got %T", sink.events[0])
	}
	if ev.Source != fsm.TriggerSourceBrakeLeft {
		t.Errorf("wrong source: %s", ev.Source)
	}
}

func TestHandleButtonEvent_FlagDisabledSuppresses(t *testing.T) {
	s, sink := newTestSubscriber()
	s.buttonsTriggerEnabled.Store(false)

	_ = s.handleButtonEvent("horn:on")
	_ = s.handleButtonEvent("seatbox:on")

	if len(sink.events) != 0 {
		t.Fatalf("expected no events when buttons disabled, got %v", sink.events)
	}
}

func TestHandleButtonEvent_BlinkerIgnored(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleButtonEvent("blinker:left:on")
	_ = s.handleButtonEvent("blinker:right:on")

	if len(sink.events) != 0 {
		t.Fatalf("expected no events for blinkers, got %v", sink.events)
	}
}

func TestHandlebarLock_InitialSyncNoTrigger(t *testing.T) {
	s, sink := newTestSubscriber()

	if err := s.handleHandlebarLockField("unlocked"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sink.events) != 0 {
		t.Fatalf("expected no events on baseline capture, got %v", sink.events)
	}
	if s.handlebarLockLast != "unlocked" {
		t.Errorf("baseline not captured, got %q", s.handlebarLockLast)
	}
}

func TestHandlebarLock_LockedToUnlockedTriggers(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarLockField("locked")
	if err := s.handleHandlebarLockField("unlocked"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d: %v", len(sink.events), sink.events)
	}
	ev, ok := sink.events[0].(fsm.InputTriggerEvent)
	if !ok {
		t.Fatalf("expected InputTriggerEvent, got %T", sink.events[0])
	}
	if ev.Source != fsm.TriggerSourceHandlebarLock {
		t.Errorf("wrong source: %s", ev.Source)
	}
}

func TestHandlebarLock_RepeatedUnlockedNoTrigger(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarLockField("unlocked")
	_ = s.handleHandlebarLockField("unlocked")
	_ = s.handleHandlebarLockField("unlocked")

	if len(sink.events) != 0 {
		t.Fatalf("expected no events on repeats, got %v", sink.events)
	}
}

func TestHandlebarLock_CycleTriggersOnce(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarLockField("unlocked")
	_ = s.handleHandlebarLockField("locked")
	_ = s.handleHandlebarLockField("unlocked")

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d: %v", len(sink.events), sink.events)
	}
}

func TestHandlebarLock_FlagDisabledSuppresses(t *testing.T) {
	s, sink := newTestSubscriber()
	s.handlebarTriggerEnabled.Store(false)

	_ = s.handleHandlebarLockField("locked")
	_ = s.handleHandlebarLockField("unlocked")

	if len(sink.events) != 0 {
		t.Fatalf("expected no events when handlebar disabled, got %v", sink.events)
	}
}

func TestHandlebarPosition_OffPlaceBaselineNoTrigger(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("off-place")

	if len(sink.events) != 0 {
		t.Fatalf("expected no events on baseline, got %v", sink.events)
	}
}

func TestHandlebarPosition_NoTriggerBeforeDwellExpires(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("on-place")
	_ = s.handleHandlebarPositionField("off-place")

	if evs := sink.snapshot(); len(evs) != 0 {
		t.Fatalf("expected no events before dwell expires, got %v", evs)
	}
}

func TestHandlebarPosition_OnPlaceToOffPlaceTriggers(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("on-place")
	_ = s.handleHandlebarPositionField("off-place")
	pastDwell()

	evs := sink.snapshot()
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	ev, ok := evs[0].(fsm.InputTriggerEvent)
	if !ok {
		t.Fatalf("expected InputTriggerEvent, got %T", evs[0])
	}
	if ev.Source != fsm.TriggerSourceHandlebarPosition {
		t.Errorf("wrong source: %s", ev.Source)
	}
}

func TestHandlebarPosition_ReturnWithinDwellSuppresses(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("on-place")
	_ = s.handleHandlebarPositionField("off-place")
	_ = s.handleHandlebarPositionField("on-place")
	pastDwell()

	if evs := sink.snapshot(); len(evs) != 0 {
		t.Fatalf("expected the excursion to be suppressed, got %v", evs)
	}
}

func TestHandlebarPosition_ChatterNeverTriggers(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("on-place")
	for i := 0; i < 5; i++ {
		_ = s.handleHandlebarPositionField("off-place")
		time.Sleep(testDwell / 4)
		_ = s.handleHandlebarPositionField("on-place")
		time.Sleep(testDwell / 4)
	}
	pastDwell()

	if evs := sink.snapshot(); len(evs) != 0 {
		t.Fatalf("expected chatter to be suppressed, got %v", evs)
	}
}

func TestHandlebarPosition_TriggersAfterEarlierSuppression(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("on-place")
	_ = s.handleHandlebarPositionField("off-place")
	_ = s.handleHandlebarPositionField("on-place")
	pastDwell()

	_ = s.handleHandlebarPositionField("off-place")
	pastDwell()

	if evs := sink.snapshot(); len(evs) != 1 {
		t.Fatalf("expected 1 event from the held excursion, got %v", evs)
	}
}

func TestHandlebarPosition_HeldOffPlaceTriggersOnce(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("on-place")
	_ = s.handleHandlebarPositionField("off-place")
	_ = s.handleHandlebarPositionField("off-place")
	pastDwell()

	if evs := sink.snapshot(); len(evs) != 1 {
		t.Fatalf("expected exactly 1 event, got %v", evs)
	}
}

func TestHandlebarPosition_FlagDisabledSuppresses(t *testing.T) {
	s, sink := newTestSubscriber()
	s.handlebarTriggerEnabled.Store(false)

	_ = s.handleHandlebarPositionField("on-place")
	_ = s.handleHandlebarPositionField("off-place")
	pastDwell()

	if evs := sink.snapshot(); len(evs) != 0 {
		t.Fatalf("expected no events when handlebar disabled, got %v", evs)
	}
}

func TestSeatboxLock_FlagDisabledTreatsOpenAsAuthorized(t *testing.T) {
	s, sink := newTestSubscriber()
	s.seatboxTriggerEnabled.Store(false)

	if err := s.handleSeatboxLockField("open"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d: %v", len(sink.events), sink.events)
	}
	if _, ok := sink.events[0].(fsm.SeatboxOpenedEvent); !ok {
		t.Errorf("expected SeatboxOpenedEvent, got %T", sink.events[0])
	}
}

func TestSeatboxLock_FlagEnabledTriggers(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleSeatboxLockField("open")

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d: %v", len(sink.events), sink.events)
	}
	if _, ok := sink.events[0].(fsm.UnauthorizedSeatboxEvent); !ok {
		t.Errorf("expected UnauthorizedSeatboxEvent, got %T", sink.events[0])
	}
}

func TestSubscriber_TriggerFlagsCrossGoroutine(t *testing.T) {
	s, _ := newTestSubscriber()

	const iterations = 500
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			enabled := i%2 == 0
			s.seatboxTriggerEnabled.Store(enabled)
			s.buttonsTriggerEnabled.Store(enabled)
			s.handlebarTriggerEnabled.Store(enabled)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = s.handleSeatboxLockField("open")
			_ = s.handleSeatboxLockField("closed")
			_ = s.handleHandlebarLockField("locked")
			_ = s.handleHandlebarLockField("unlocked")
			_ = s.handleHandlebarPositionField("on-place")
			_ = s.handleHandlebarPositionField("off-place")
			_ = s.handleButtonEvent("horn:on")
		}
	}()

	wg.Wait()
}
