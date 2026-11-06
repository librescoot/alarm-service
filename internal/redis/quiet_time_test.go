package redis

import (
	"testing"

	"alarm-service/internal/fsm"
)

func TestQuietTimeSettingValidation(t *testing.T) {
	for _, value := range []string{"1", "30", "300"} {
		t.Run(value, func(t *testing.T) {
			s, sink := newTestSubscriber()
			if err := s.handleQuietTime(value); err != nil {
				t.Fatal(err)
			}
			events := sink.snapshot()
			if len(events) != 1 {
				t.Fatalf("got %d events, want one", len(events))
			}
			if event, ok := events[0].(fsm.QuietTimeChangedEvent); !ok || event.Duration < 1 || event.Duration > 300 {
				t.Fatalf("unexpected event: %#v", events[0])
			}
		})
	}
	for _, value := range []string{"", "0", "-1", "301", "30seconds", "1.5", "true"} {
		t.Run("invalid_"+value, func(t *testing.T) {
			s, sink := newTestSubscriber()
			if err := s.handleQuietTime(value); err != nil {
				t.Fatal(err)
			}
			if len(sink.snapshot()) != 0 {
				t.Fatal("invalid value changed quiet time")
			}
		})
	}
}
