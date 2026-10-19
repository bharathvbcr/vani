package eval

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A nil store used to panic in Labels. Now the report is built, says its
// labels could not be read, and counts nothing as labelled; the held-out
// report refuses outright.
func TestNilStoreIsAnUnreadableLabelSetNotAPanic(t *testing.T) {
	recs := []ledger.Record{jevRec("gh:a:1", "remote", "remote", "0.9", "", true), mkRec("gh:a:2", posting.Triaged, nil)}
	rep := BuildReport(recs, nil, Names{})
	if rep.LoadError == "" {
		t.Fatal("a missing store must be reported as a load error")
	}
	if rep.Matched != 0 || rep.LabelEntries != 0 {
		t.Fatalf("nothing can be matched without labels: %+v", rep)
	}
	if md := rep.Markdown(); !strings.Contains(md, "LABELS COULD NOT BE READ") {
		t.Fatalf("report does not say the figures are invalid:\n%s", md)
	}
	sp, err := LookupSplit(SplitNames()[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := BuildHeldOut(recs, nil, Names{}, sp); err == nil {
		t.Fatal("a held-out report without labels must be refused")
	}
}
