package redis

import (
	"io"
	"log/slog"
	"testing"

	"alarm-service/internal/fsm"
)

// fakeEventSink records events the subscriber emits without requiring a real
// state machine. Implements the package-private eventSink interface.
type fakeEventSink struct {
	events []fsm.Event
	state  fsm.State
}

func (f *fakeEventSink) SendEvent(e fsm.Event) { f.events = append(f.events, e) }
func (f *fakeEventSink) State() fsm.State      { return f.state }

// newTestSubscriber builds a Subscriber with only the fields the handlebar
// handlers touch populated. Sufficient for exercising the baseline/
// transition logic without hitting Redis.
func newTestSubscriber() (*Subscriber, *fakeEventSink) {
	sink := &fakeEventSink{}
	s := &Subscriber{
		log:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		sm:               sink,
		handlebarEnabled: true,
		buttonsEnabled:   true,
	}
	return s, sink
}

// parseButtonPayload must accept the payloads vehicle-service publishes on the
// `buttons` channel and map them to the right TriggerSource. Unknown payloads
// (blinkers, garbage, etc.) return !ok and must be ignored.
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

		// Blinkers are published on the same channel but are not tamper
		// triggers — the subscriber must ignore them.
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

// Handlebar lock baseline: the first callback (StartWithSync) must not emit,
// even when the value is "unlocked" (rider parked without engaging the lock).
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

// Handlebar lock transition: only locked -> unlocked after baseline emits.
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

// A second "unlocked" report after we already reported "unlocked" must not
// emit a new trigger (no state change).
func TestHandlebarLock_RepeatedUnlockedNoTrigger(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarLockField("unlocked") // baseline
	_ = s.handleHandlebarLockField("unlocked") // spurious repeat
	_ = s.handleHandlebarLockField("unlocked") // more noise

	if len(sink.events) != 0 {
		t.Fatalf("expected no events on repeats, got %v", sink.events)
	}
}

// unlocked -> locked -> unlocked fires once on the second transition.
func TestHandlebarLock_CycleTriggersOnce(t *testing.T) {
	s, sink := newTestSubscriber()

	_ = s.handleHandlebarLockField("unlocked") // baseline
	_ = s.handleHandlebarLockField("locked")   // safe transition, no event
	_ = s.handleHandlebarLockField("unlocked") // unsafe transition, event

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d: %v", len(sink.events), sink.events)
	}
}

// handlebar trigger disabled via setting: transition happens but no event
// is sent.
func TestHandlebarLock_FlagDisabledSuppresses(t *testing.T) {
	s, sink := newTestSubscriber()
	s.handlebarEnabled = false

	_ = s.handleHandlebarLockField("locked")
	_ = s.handleHandlebarLockField("unlocked")

	if len(sink.events) != 0 {
		t.Fatalf("expected no events when handlebar disabled, got %v", sink.events)
	}
}

// Handlebar position: same baseline/transition rules as the lock sensor.
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
	ev := sink.events[0].(fsm.InputTriggerEvent)
	if ev.Source != fsm.TriggerSourceHandlebarPosition {
		t.Errorf("wrong source: %s", ev.Source)
	}
}

func TestParseBoolSetting(t *testing.T) {
	truthy := []string{"true", "True", "TRUE", "1", "yes", "on", " true ", "YES"}
	for _, v := range truthy {
		if !parseBoolSetting(v) {
			t.Errorf("parseBoolSetting(%q) = false, want true", v)
		}
	}
	falsy := []string{"false", "0", "no", "off", "", "maybe", "2"}
	for _, v := range falsy {
		if parseBoolSetting(v) {
			t.Errorf("parseBoolSetting(%q) = true, want false", v)
		}
	}
}
