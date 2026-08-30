package pm

import (
	"encoding/json"
	"testing"
)

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

	if data.Who != inhibitWho || data.Who == "librescoot-modem" {
		t.Errorf("who = %q, want %q (and never librescoot-modem)", data.Who, inhibitWho)
	}

	if data.Type != "block" {
		t.Errorf("type = %q, want \"block\"", data.Type)
	}
	if data.Why != "Level 2 triggered" {
		t.Errorf("why = %q, want the acquire reason", data.Why)
	}
}
