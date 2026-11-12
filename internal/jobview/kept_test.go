package jobview

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Kept rows are a generation's: kept while the generation and the record
// count hold, built again otherwise, and a record that is not the one kept at
// its index (a snapshot read another way) is worked out, not read from them.
func TestKeptRowsKeyedByGeneration(t *testing.T) {
	snap := pageSnap(4)
	var k KeptRows
	a := k.at(1, snap)
	if k.at(1, snap) != a {
		t.Fatal("one generation built its rows twice")
	}
	if k.at(2, snap) == a {
		t.Fatal("a new generation kept the old rows")
	}
	b := k.at(2, snap)
	if k.at(2, ledger.Snapshot{Records: snap.Records[:3]}) == b {
		t.Fatal("a snapshot of another size kept the rows")
	}
	c := k.at(2, snap)
	moved := snap.Records[2]
	moved.Posting.Title = "Moved Title"
	if got := c.static(1, moved); got != staticOf(moved) {
		t.Fatalf("record at the wrong index: got %+v", got)
	}
	retitled := snap.Records[1]
	retitled.Key = posting.Key("gh:acme:999")
	if got := c.static(1, retitled); got.title != staticOf(retitled).title {
		t.Fatal("a record with another key read the kept row")
	}
	var none *keptRows
	if got := none.static(0, snap.Records[0]); got != staticOf(snap.Records[0]) {
		t.Fatal("no kept rows did not work the row out")
	}
	if got := c.static(-1, snap.Records[0]); got != staticOf(snap.Records[0]) {
		t.Fatal("index -1 did not work the row out")
	}
}
