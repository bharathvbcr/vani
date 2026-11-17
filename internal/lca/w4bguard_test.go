package lca

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func w4bguardPartial(t *testing.T, csv, name string) Partial {
	t.Helper()
	p, err := Import(strings.NewReader(csv), name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A refused shrink names the year, the files and both totals, and leaves the
// index file byte for byte as it was.
func TestW4bguardShrinkRefusedNamesAndLeavesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	w4bImport(t, path, w4bRows(100, "2024-03-01", "Acme Inc"), "fy2024-q4.csv")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = Update(path, w4bguardPartial(t, w4bRows(3, "2024-03-01", "Acme Inc"), "fy2024-q1.csv"))
	if !errors.Is(err, ErrShrinks) {
		t.Fatalf("err = %v, want ErrShrinks", err)
	}
	for _, want := range []string{"2024", "fy2024-q4.csv", "fy2024-q1.csv", "100", "3"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("a refused import changed the index file")
	}
}

// With Replace the shrinking import goes through and reports what it replaced.
func TestW4bguardReplaceAllowsShrink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	w4bImport(t, path, w4bRows(100, "2024-03-01", "Acme Inc"), "fy2024-q4.csv")
	_, replaced, err := UpdateWith(path, w4bguardPartial(t, w4bRows(3, "2024-03-01", "Acme Inc"), "fy2024-q1.csv"), UpdateOptions{Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced) != 1 || replaced[0].Name != "fy2024-q4.csv" {
		t.Fatalf("replaced = %+v", replaced)
	}
	if got := w4bTotal(t, path, "Acme"); got != 3 {
		t.Fatalf("total = %d, want 3", got)
	}
}

// Equal size is a correction, not a shrink.
func TestW4bguardEqualSizeReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	w4bImport(t, path, w4bRows(5, "2024-03-01", "Acme Inc"), "a.csv")
	if r := w4bImport(t, path, w4bRows(5, "2024-03-01", "Acme Co"), "b.csv"); len(r) != 1 {
		t.Fatalf("replaced = %+v", r)
	}
}

// The measure is counted cases per fiscal year, not which employers hold them:
// a same-size file naming other employers is a correction.
func TestW4bguardMeasureIsFiscalYearTotal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	w4bImport(t, path, w4bRows(5, "2024-03-01", "Acme Inc"), "a.csv")
	if r := w4bImport(t, path, w4bRows(5, "2024-03-01", "Other Inc"), "b.csv"); len(r) != 1 {
		t.Fatalf("replaced = %+v", r)
	}
}

// A file spanning two fiscal years is not silently trimmed by a later file of
// its first year: the neighbour year's cases it holds make that a refusal.
func TestW4bguardTieFileRefusedWhenItWouldLoseNeighbourYear(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	w4bImport(t, path, w4bRows(3, "2023-03-01", "Acme Inc")+strings.TrimPrefix(w4bRows(3, "2024-03-01", "Acme Inc"), hdr), "span.csv")
	_, _, err := Update(path, w4bguardPartial(t, w4bRows(10, "2023-03-01", "Acme Inc"), "fy2023.csv"))
	if !errors.Is(err, ErrShrinks) || !strings.Contains(err.Error(), "2024") {
		t.Fatalf("err = %v, want ErrShrinks naming 2024", err)
	}
	x, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := w4badvTotal(x, 2024); got != 3 {
		t.Fatalf("FY2024 = %d, want the 3 the spanning file carried", got)
	}
}

// A file for another fiscal year replaces nothing, so the guard has nothing to
// refuse.
func TestW4bguardNothingToReplaceNeverRefuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.json")
	if r := w4bImport(t, path, w4bRows(2, "2024-03-01", "Acme Inc"), "a.csv"); len(r) != 0 {
		t.Fatalf("replaced = %+v", r)
	}
	if r := w4bImport(t, path, w4bRows(2, "2023-03-01", "Acme Inc"), "b.csv"); len(r) != 0 {
		t.Fatalf("replaced = %+v", r)
	}
}
