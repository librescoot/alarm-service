package pm

import (
	"encoding/json"
	"testing"
)

// The inhibitor must register a pm-service block inhibitor in the power:inhibits
// hash. pm-service's suspend gate honors its own registry (the power:inhibits
// hash + the /tmp/suspend_inhibitor socket), so the payload shape and "who"
// must match what pm-service reconciles.
func TestInhibitPayloadShape(t *testing.T) {
	raw, err := buildInhibitPayload("Level 2 triggered")
	if err != nil {
		t.Fatalf("buildInhibitPayload: %v", err)
	}

	var data inhibitData
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}

	if data.ID != inhibitID {
		t.Errorf("id = %q, want %q", data.ID, inhibitID)
	}
	// Must NOT be "librescoot-modem": pm-service special-cases the modem block
	// (hasOnlyModemBlockingInhibitors) and suspends anyway once the modem is off.
	// An alarm block has to read as a genuine "other" blocker.
	if data.Who != inhibitWho || data.Who == "librescoot-modem" {
		t.Errorf("who = %q, want %q (and never librescoot-modem)", data.Who, inhibitWho)
	}
	// "block" blocks suspend AND hibernate AND poweroff/reboot. During an active
	// alarm we want none of those to silence it. "suspend-only" would let
	// hibernate through, which also kills the MDB, so it is wrong here.
	if data.Type != "block" {
		t.Errorf("type = %q, want \"block\"", data.Type)
	}
	if data.Why != "Level 2 triggered" {
		t.Errorf("why = %q, want the acquire reason", data.Why)
	}
}
