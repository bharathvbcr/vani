package suggest

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/decide"
)

// A draft refused for an unbacked claim reaches the field with what it found
// unbacked, so the page can say it; other failures carry no kind.
func TestFailedFieldCarriesWhatWasUnbacked(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		kind string
	}{
		{"ungrounded credential", &draft.Error{Code: draft.CodeUngrounded, Kind: draft.KindCredential, Err: errors.New("the draft says \"PhD\"")}, draft.KindCredential},
		{"ungrounded years", &draft.Error{Code: draft.CodeUngrounded, Kind: draft.KindYears, Err: errors.New("the draft says \"5 years\"")}, draft.KindYears},
		{"timeout", &draft.Error{Code: draft.CodeTimeout, Err: errors.New("slow")}, ""},
		{"plain", errors.New("boom"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &fakeDecider{byLabel: map[string]decide.SlotReading{"E1": answered(fieldtriage.Essay)}}
			out, err := Engine{D: d, Drafter: &fakeDrafter{err: tc.err}}.Run(context.Background(), Request{Job: job, Fields: []fieldtriage.Field{text("1", "E1")}})
			if err != nil {
				t.Fatal(err)
			}
			if out[0].Status != Failed || out[0].Kind != tc.kind {
				t.Fatalf("%+v, want failed with kind %q", out[0], tc.kind)
			}
			raw, _ := json.Marshal(out[0])
			if has := strings.Contains(string(raw), `"kind"`); has != (tc.kind != "") {
				t.Errorf("json %s: kind present = %v", raw, has)
			}
		})
	}
}
