package redis

import (
	"io"
	"log/slog"
	"testing"

	"alarm-service/internal/fsm"
)

// fakeEventSink records what the subscriber emits without needing a running
// state machine. Implements the package-private eventSink interface.
type fakeEventSink struct {
	events []fsm.Event
	state  fsm.State
}

func (f *fakeEventSink) SendEvent(e fsm.Event) { f.events = append(f.events, e) }
func (f *fakeEventSink) State() fsm.State      { return f.state }

// newTestSubscriber populates only the fields the tamper-input handlers touch,
// which is enough to exercise the baseline and transition logic without Redis.
func newTestSubscriber() (*Subscriber, *fakeEventSink) {
	sink := &fakeEventSink{}
	s := &Subscriber{
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		sm:  sink,
	}
	s.buttonsTriggerEnabled.Store(true)
	s.handlebarTriggerEnabled.Store(true)
	return s, sink
}

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

func TestHandlebarPosition_OnPlaceToOffPlaceTriggers(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarPositionField("on-place") // baseline
	_ = s.handleHandlebarPositionField("off-place")

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sink.events))
	}
	ev, ok := sink.events[0].(fsm.InputTriggerEvent)
	if !ok {
		t.Fatalf("expected InputTriggerEvent, got %T", sink.events[0])
	}
	if ev.Source != fsm.TriggerSourceHandlebarPosition {
		t.Errorf("wrong source: %s", ev.Source)
	}
}

func TestHandlebarPosition_FlagDisabledSuppresses(t *testing.T) {
	s, sink := newTestSubscriber()
	s.handlebarTriggerEnabled.Store(false)

	_ = s.handleHandlebarPositionField("on-place")
	_ = s.handleHandlebarPositionField("off-place")

	if len(sink.events) != 0 {
		t.Fatalf("expected no events when handlebar disabled, got %v", sink.events)
	}
}
