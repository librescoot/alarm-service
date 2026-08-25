package redis

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"alarm-service/internal/fsm"
)

// fakeEventSink records what the subscriber emits without needing a running
// state machine. Implements the package-private eventSink interface.
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

// snapshot copies the recorded events. The handlebar dwell timer fires on its
// own goroutine, so tests must not read the slice directly.
func (f *fakeEventSink) snapshot() []fsm.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]fsm.Event(nil), f.events...)
}

// newTestSubscriber populates only the fields the tamper-input handlers touch,
// which is enough to exercise the baseline and transition logic without Redis.
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

// testDwell keeps the dwell-timer tests fast while staying long enough that a
// scheduling hiccup on a loaded machine does not read as a real expiry.
const testDwell = 30 * time.Millisecond

// pastDwell waits comfortably beyond testDwell so a pending timer has fired.
func pastDwell() { time.Sleep(4 * testDwell) }

// parseButtonPayload has to accept the payloads vehicle-service publishes on
// the `buttons` channel and map them to the right TriggerSource. Anything else
// (blinkers, garbage) must come back not-ok so the subscriber ignores it.
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

		// Blinkers share the channel but are not tampering.
		{"blinker:left:on", fsm.TriggerSourceUnknown, "", false},
		{"blinker:right:off", fsm.TriggerSourceUnknown, "", false},

		// Garbage must not panic or match.
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

// A press emits exactly one trigger; the matching release emits none.
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

// alarm.trigger.buttons=false drops the press at the subscriber.
func TestHandleButtonEvent_FlagDisabledSuppresses(t *testing.T) {
	s, sink := newTestSubscriber()
	s.buttonsTriggerEnabled.Store(false)

	_ = s.handleButtonEvent("horn:on")
	_ = s.handleButtonEvent("seatbox:on")

	if len(sink.events) != 0 {
		t.Fatalf("expected no events when buttons disabled, got %v", sink.events)
	}
}

// Blinker edges arrive on the same channel and must never trigger.
func TestHandleButtonEvent_BlinkerIgnored(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleButtonEvent("blinker:left:on")
	_ = s.handleButtonEvent("blinker:right:on")

	if len(sink.events) != 0 {
		t.Fatalf("expected no events for blinkers, got %v", sink.events)
	}
}

// The first callback is StartWithSync delivering the value that was already
// there, so it must not emit even when that value is "unlocked".
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

// Only locked -> unlocked after the baseline emits a trigger.
func TestHandlebarLock_LockedToUnlockedTriggers(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarLockField("locked") // baseline
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

// Repeated "unlocked" reports are not transitions and must stay quiet.
func TestHandlebarLock_RepeatedUnlockedNoTrigger(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarLockField("unlocked") // baseline
	_ = s.handleHandlebarLockField("unlocked") // spurious repeat
	_ = s.handleHandlebarLockField("unlocked") // more noise

	if len(sink.events) != 0 {
		t.Fatalf("expected no events on repeats, got %v", sink.events)
	}
}

// unlocked -> locked -> unlocked fires once, on the second transition.
func TestHandlebarLock_CycleTriggersOnce(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarLockField("unlocked") // baseline
	_ = s.handleHandlebarLockField("locked")   // safe direction, no event
	_ = s.handleHandlebarLockField("unlocked") // unsafe direction, event

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d: %v", len(sink.events), sink.events)
	}
}

// alarm.trigger.handlebar=false drops the transition at the subscriber.
func TestHandlebarLock_FlagDisabledSuppresses(t *testing.T) {
	s, sink := newTestSubscriber()
	s.handlebarTriggerEnabled.Store(false)

	_ = s.handleHandlebarLockField("locked")
	_ = s.handleHandlebarLockField("unlocked")

	if len(sink.events) != 0 {
		t.Fatalf("expected no events when handlebar disabled, got %v", sink.events)
	}
}

// Position sensor follows the same baseline rule as the lock sensor.
func TestHandlebarPosition_OffPlaceBaselineNoTrigger(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("off-place")

	if len(sink.events) != 0 {
		t.Fatalf("expected no events on baseline, got %v", sink.events)
	}
}

// The bars leaving on-place starts the dwell timer; it must not emit anything
// until the timer expires. This is what stops a gust that nudges the bars for a
// few hundred milliseconds from honking the horn.
func TestHandlebarPosition_NoTriggerBeforeDwellExpires(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("on-place") // baseline
	_ = s.handleHandlebarPositionField("off-place")

	if evs := sink.snapshot(); len(evs) != 0 {
		t.Fatalf("expected no events before dwell expires, got %v", evs)
	}
}

func TestHandlebarPosition_OnPlaceToOffPlaceTriggers(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("on-place") // baseline
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

// The bug this whole change exists for: on 2026-08-25 the sensor reported
// off-place and back inside ~1s while the steering lock was engaged, and the
// alarm honked. Returning to on-place must cancel the pending trigger.
func TestHandlebarPosition_ReturnWithinDwellSuppresses(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("on-place") // baseline
	_ = s.handleHandlebarPositionField("off-place")
	_ = s.handleHandlebarPositionField("on-place")
	pastDwell()

	if evs := sink.snapshot(); len(evs) != 0 {
		t.Fatalf("expected the excursion to be suppressed, got %v", evs)
	}
}

// A chattering sensor restarts the dwell on every off-place edge, so it never
// accumulates a full quiet window and never fires.
func TestHandlebarPosition_ChatterNeverTriggers(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("on-place") // baseline
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

// A second excursion after a suppressed one still has to arm the alarm. The
// cancel path must not latch the source off.
func TestHandlebarPosition_TriggersAfterEarlierSuppression(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("on-place") // baseline
	_ = s.handleHandlebarPositionField("off-place")
	_ = s.handleHandlebarPositionField("on-place") // suppressed excursion
	pastDwell()

	_ = s.handleHandlebarPositionField("off-place") // real one, held
	pastDwell()

	if evs := sink.snapshot(); len(evs) != 1 {
		t.Fatalf("expected 1 event from the held excursion, got %v", evs)
	}
}

// Staying off-place fires once, not once per redundant off-place publish.
func TestHandlebarPosition_HeldOffPlaceTriggersOnce(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("on-place") // baseline
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

// alarm.seatbox-trigger=false downgrades an unauthorized opening to an
// authorized one instead of escalating.
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

// With the flag on, the same edge is tampering.
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

// The settings watcher writes the trigger flags from its own goroutine while
// the vehicle watcher and the buttons subscription read them from theirs.
// Under -race this fails if any of the three goes back to a plain bool.
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
