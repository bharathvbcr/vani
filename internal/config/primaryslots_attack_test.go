package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
)

// withLappi returns the JSON of a valid config whose decide.lappi is raw, with
// decide.shadow set to shadow.
func withLappi(t *testing.T, raw, shadow string) []byte {
	t.Helper()
	b, err := json.Marshal(valid())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	d := m["decide"].(map[string]any)
	d["shadow"] = shadow
	var l any
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatal(err)
	}
	d["lappi"] = l
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Attack 4. primary_slots as it arrives from a config file: absent, empty,
// null, case and whitespace variants, other questions' slots, long lists.
func TestAttackPrimarySlotsParsing(t *testing.T) {
	a := jdtriage.Families[0].Slot.Name
	cases := []struct {
		name, raw, shadow string
		wantErr           string // "" accepted
		wantPromoted      int
	}{
		{"absent, shadow", `{}`, "lappi", "", 0},
		{"empty list is absent", `{"primary_slots":[]}`, "lappi", "", 0},
		{"null is absent", `{"primary_slots":null}`, "lappi", "", 0},
		{"empty list, no shadow: unused", `{"primary_slots":[]}`, "", "configured but unused", 0},
		{"null, no shadow: unused", `{"primary_slots":null}`, "", "configured but unused", 0},
		{"exact", `{"primary_slots":["` + a + `"]}`, "", "", 1},
		{"upper-case variant", `{"primary_slots":["Role_Fit"]}`, "", `"Role_Fit" is not a triage slot`, 0},
		{"shouting", `{"primary_slots":["ROLE_FIT"]}`, "", "is not a triage slot", 0},
		{"leading space", `{"primary_slots":[" role_fit"]}`, "", "is not a triage slot", 0},
		{"trailing newline", `{"primary_slots":["role_fit\n"]}`, "", "is not a triage slot", 0},
		{"empty string", `{"primary_slots":[""]}`, "", "is not a triage slot", 0},
		{"other question's slot", `{"primary_slots":["kb_match"]}`, "", `"kb_match" is not a triage slot`, 0},
		{"extra (category) slot is not a family", `{"primary_slots":["category"]}`, "", `"category" is not a triage slot`, 0},
		{"non-string element", `{"primary_slots":[1]}`, "", "decode", 0},
		{"a string, not a list", `{"primary_slots":"role_fit"}`, "", "decode", 0},
		{"primary false explicit + slots", `{"primary":false,"primary_slots":["` + a + `"]}`, "", "", 1},
		{"primary true + empty slots is plain primary", `{"primary":true,"primary_slots":[]}`, "", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := Parse(withLappi(t, tc.raw, tc.shadow))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("rejected: %v", err)
				}
				if got := len(c.Decide.Lappi.PromotedSlots()); got != tc.wantPromoted {
					t.Fatalf("promoted %d, want %d", got, tc.wantPromoted)
				}
				return
			}
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

// Whatever Validate would reject must not reach the router as a promoted slot:
// PromotedSlots reports only real triage slots, once each, in fixture order.
func TestAttackPromotedSlotsOnlyRealSlotsOnce(t *testing.T) {
	a := jdtriage.Families[0].Slot.Name
	l := &Lappi{PrimarySlots: []string{"Role_Fit", a, a, "kb_match", ""}}
	got := l.PromotedSlots()
	if len(got) != 1 || got[0] != a {
		t.Fatalf("promoted %v, want [%s]", got, a)
	}
}
