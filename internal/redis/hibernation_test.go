package redis

import "testing"

func TestScheduledHibernationIsRecognized(t *testing.T) {
	for _, state := range []string{"hibernating-for-imminent", "hibernating-for"} {
		if !isHibernatingImminentState(state) {
			t.Errorf("%s must be treated as hibernation", state)
		}
	}
	if isHibernatingImminentState("running") {
		t.Error("running must not be treated as hibernation")
	}
}
