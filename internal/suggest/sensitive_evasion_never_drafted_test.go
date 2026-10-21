package suggest

import (
	"context"
	"testing"

	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
)

// Labels a person reads as sensitive. A model that (wrongly) reads them as
// essays must still not lead to a draft.
var evasionLabels = []string{
	"Are you a сitizen?", "Are you a citizеn?", "Are you a citizén?", "Are you a citi‪zen‬?",
	"Are you a citi؜zen?", "Are you a citi<b>zen</b>?", "Are you a citi&lt;b&gt;zen&lt;/b&gt;?",
	"U.S.person", "Are you 18 or older?", "Are you over 18?", "Armed forces service", "Do you identify as LGBTQ+?",
	"Are you Black or African American?", "Do you require a H-1B transfer?", "Are you a permanent resident?",
	"Are you a national of the US?", "Are you legally permitted to work?", "Do you have unrestricted work rights?",
	"Nationality", "What is your sex?", "Gеnder", "Vеteran", "rаce", "visа",
	"Are you legally allowed to work in the US?", "workauthorization", "Handicap", "Medical condition",
	"Military service", "Are you authorisеd to work",
}

func TestSensitiveEvasionLabelsNeverDrafted(t *testing.T) {
	for _, l := range evasionLabels {
		dr := &fakeDrafter{}
		out, err := Engine{D: constDecider{val: fieldtriage.Essay}, Drafter: dr}.Run(context.Background(),
			Request{Job: job, Fields: []fieldtriage.Field{text("a", l)}, MaxDrafts: MaxDrafts})
		if err != nil {
			t.Fatalf("%q: %v", l, err)
		}
		if dr.calls.Load() != 0 || out[0].Status == Drafted {
			t.Errorf("BREAK: %q reached the drafter (status %s)", l, out[0].Status)
		}
	}
}
