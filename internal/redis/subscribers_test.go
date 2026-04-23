package redis

import (
	"testing"

	"alarm-service/internal/fsm"
)

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
