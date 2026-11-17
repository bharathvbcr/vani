package lca

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/indexutil"
)

func w4badvTotal(x *Index, fy int) int {
	n := 0
	for _, byFY := range x.merged {
		n += byFY[fy].H1B
	}
	return n
}

// A smaller file for a fiscal year must not silently replace a bigger one:
// importing a Q1 (cumulative, so a subset) after the Q4 deletes most of the
// year with only a "replaced" line to show for it.
func TestW4badvSmallerFileDoesNotSilentlyDropBiggerYear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	w4bImport(t, path, w4bRows(100, "2024-03-01", "Acme Inc"), "fy2024-q4.csv")
	p, err := Import(strings.NewReader(w4bRows(3, "2024-03-01", "Acme Inc")), "fy2024-q1.csv")
	if err != nil {
		t.Fatal(err)
	}
	x, replaced, err := Update(path, p)
	if err == nil && len(replaced) > 0 && w4badvTotal(x, 2024) < 100 {
		t.Fatalf("FY2024 went from 100 to %d cases: %+v replaced by a 3-case file, no refusal", w4badvTotal(x, 2024), replaced)
	}
}

// A file that straddles two fiscal years evenly is replaced wholesale by a
// later file of its earlier year, taking the other year's cases with it. The
// import must either keep both years, or be refused with ErrShrinks and leave
// the index file byte-identical; silently losing FY2024 is the failure.
func TestW4badvTieFileLosesNeighbourYear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	w4bImport(t, path, w4bRows(3, "2023-03-01", "Acme Inc")+strings.TrimPrefix(w4bRows(3, "2024-03-01", "Acme Inc"), hdr), "span.csv")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Import(strings.NewReader(w4bRows(10, "2023-03-01", "Acme Inc")), "fy2023.csv")
	if err != nil {
		t.Fatal(err)
	}
	_, _, uerr := Update(path, p)
	if uerr != nil {
		if !errors.Is(uerr, ErrShrinks) {
			t.Fatalf("import failed with %v, want success or ErrShrinks", uerr)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("a refused import changed the index file:\nbefore %s\nafter  %s", before, after)
		}
	}
	x, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := w4badvTotal(x, 2024); got != 3 {
		t.Fatalf("FY2024 cases = %d (import err = %v), want the 3 the spanning file carried", got, uerr)
	}
}

// Legacy index (built with additive Merge): two sources for one year. The next
// import for that year drops BOTH.
func TestW4badvLegacyTwoSourcesBothDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	x := NewIndex()
	for _, n := range []struct{ name, sha string }{{"a.csv", "a"}, {"b.csv", "b"}} {
		if err := x.Merge(Partial{Source: SourceMeta{Name: n.name, SHA256: n.sha, RowsRead: 1, RowsCounted: 1},
			Employers: map[string]map[int]Counts{"ACME": {2024: {H1B: 5}}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := x.Save(path); err != nil {
		t.Fatal(err)
	}
	_, replaced, err := UpdateWith(path, Partial{Source: SourceMeta{Name: "c.csv", SHA256: "c", RowsRead: 1, RowsCounted: 1},
		Employers: map[string]map[int]Counts{"ACME": {2024: {H1B: 7}}}}, UpdateOptions{Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("replaced %d sources", len(replaced))
	if len(replaced) != 2 {
		t.Fatalf("replaced = %+v", replaced)
	}
}

// Cumulative quarters of one year: Q2 replaces Q1 (documented behaviour).
func TestW4badvCumulativeQuarterReplacesEarlier(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	w4bImport(t, path, w4bRows(3, "2024-03-01", "Acme Inc"), "q1.csv")
	w4bImport(t, path, w4bRows(5, "2024-03-01", "Acme Inc"), "q2.csv")
	x, _ := Load(path)
	if got := w4badvTotal(x, 2024); got != 5 {
		t.Fatalf("got %d", got)
	}
}

// ErrBusy surfaces promptly at a shortened wait and leaves the file alone.
func TestW4badvBusyLeavesIndexUntouched(t *testing.T) {
	old := indexutil.LockWait
	indexutil.LockWait = 200 * time.Millisecond
	defer func() { indexutil.LockWait = old }()
	path := filepath.Join(t.TempDir(), "index.json")
	w4bImport(t, path, w4bRows(3, "2024-03-01", "Acme Inc"), "q1.csv")
	_, release, err := indexutil.Lock(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	p, _ := Import(strings.NewReader(w4bRows(4, "2023-03-01", "Acme Inc")), "x.csv")
	start := time.Now()
	_, _, err = Update(path, p)
	if err == nil || !strings.Contains(err.Error(), "busy") || time.Since(start) > 3*time.Second {
		t.Fatalf("err=%v after %v", err, time.Since(start))
	}
}
