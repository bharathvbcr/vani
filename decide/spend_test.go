package decide

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newCap(t *testing.T, capUSD float64) (*DailyCap, string, *time.Time) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "spend.json")
	d, err := NewDailyCap(path, capUSD)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 18, 23, 59, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	return d, path, &now
}

func TestDailyCapEnforcedPerKey(t *testing.T) {
	d, _, _ := newCap(t, 1.0)
	ctx := context.Background()
	if err := d.Allow(ctx, "jev"); err != nil {
		t.Fatal(err)
	}
	if err := d.Charge(ctx, "jev", 0.6); err != nil {
		t.Fatal(err)
	}
	if err := d.Allow(ctx, "jev"); err != nil {
		t.Fatalf("under cap refused: %v", err)
	}
	if err := d.Charge(ctx, "jev", 0.5); err != nil { // overshoots; still recorded
		t.Fatal(err)
	}
	if err := d.Allow(ctx, "jev"); !errors.Is(err, ErrBudget) {
		t.Fatalf("over cap allowed: %v", err)
	}
	if err := d.Allow(ctx, "other"); err != nil {
		t.Fatalf("other key affected: %v", err)
	}
}

func TestDailyCapRollsOverAtUTCMidnightAndSurvivesRestart(t *testing.T) {
	d, path, now := newCap(t, 1.0)
	ctx := context.Background()
	_ = d.Charge(ctx, "jev", 1.0)
	if err := d.Allow(ctx, "jev"); !errors.Is(err, ErrBudget) {
		t.Fatal("should be capped")
	}
	// A new process on the same file still sees today's spend.
	d2, err := NewDailyCap(path, 1.0)
	if err != nil {
		t.Fatal(err)
	}
	d2.now = d.now
	if err := d2.Allow(ctx, "jev"); !errors.Is(err, ErrBudget) {
		t.Fatal("restart forgot spend")
	}
	*now = now.Add(2 * time.Minute) // 00:01 UTC next day
	if err := d.Allow(ctx, "jev"); err != nil {
		t.Fatalf("no rollover: %v", err)
	}
	// A non-UTC clock must still bucket by UTC day.
	loc := time.FixedZone("x", -8*3600)
	*now = time.Date(2026, 10, 18, 17, 0, 0, 0, loc) // 01:00 UTC on the 19th
	if err := d.Allow(ctx, "jev"); err != nil {
		t.Fatalf("day boundary used local time: %v", err)
	}
}

func TestDailyCapFileModeAndAtomicity(t *testing.T) {
	d, path, _ := newCap(t, 1.0)
	if err := d.Charge(context.Background(), "jev", 0.1); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("leftover temp files: %v", entries)
	}
	// Overwrites replace the file by rename, never truncating it in place.
	before, _ := os.Stat(path)
	_ = d.Charge(context.Background(), "jev", 0.1)
	after, _ := os.Stat(path)
	if os.SameFile(before, after) {
		t.Error("file was rewritten in place, not replaced atomically")
	}
}

func TestDailyCapRejectsBadInput(t *testing.T) {
	if _, err := NewDailyCap("relative.json", 1); err == nil {
		t.Error("relative path accepted")
	}
	for _, c := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := NewDailyCap(filepath.Join(t.TempDir(), "s.json"), c); err == nil {
			t.Errorf("cap %v accepted", c)
		}
	}
	d, path, _ := newCap(t, 1.0)
	for _, v := range []float64{-0.1, math.NaN(), math.Inf(1)} {
		if err := d.Charge(context.Background(), "jev", v); err == nil {
			t.Errorf("charge %v accepted", v)
		}
	}
	if err := d.Charge(context.Background(), "", 0.1); err == nil {
		t.Error("empty key accepted")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("rejected charges created a file")
	}
}

func TestDailyCapCorruptFileFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spend.json")
	for _, content := range []string{`not json`, `{"version":9,"days":{}}`, `{"version":1,"days":{"2026-10-18":{"jev":-5}}}`, `{"version":1,"days":{}} junk`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewDailyCap(path, 1.0); err == nil {
			t.Errorf("accepted corrupt file %q", content)
		}
	}
	// A file that goes bad after construction refuses rather than allows.
	d, _, _ := newCap(t, 1.0)
	_ = d.Charge(context.Background(), "jev", 0.1)
	if err := os.WriteFile(d.path, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.Allow(context.Background(), "jev"); err == nil {
		t.Error("Allow passed on an unreadable file")
	}
}

// The spend file's bytes: days and keys sorted, two-space indent, no trailing
// newline, amounts as encoding/json v1 wrote them. A file in the old
// spelling must keep loading, and a key in the wrong case is refused.
func TestDailyCapFileBytes(t *testing.T) {
	d, path, now := newCap(t, 100)
	ctx := context.Background()
	for _, c := range []struct {
		key string
		usd float64
	}{{"jev", 0.1}, {"jev", 0.2}, {"b-key", 1e-7}, {"a_key", 12.5}} {
		if err := d.Charge(ctx, c.key, c.usd); err != nil {
			t.Fatal(err)
		}
	}
	*now = now.Add(24 * time.Hour)
	if err := d.Charge(ctx, "jev", 3); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{
  "version": 1,
  "days": {
    "2026-10-18": {
      "a_key": 12.5,
      "b-key": 1e-7,
      "jev": 0.30000000000000004
    },
    "2026-10-19": {
      "jev": 3
    }
  }
}`
	if string(got) != want {
		t.Fatalf("spend file:\n%s\nwant\n%s", got, want)
	}
	if _, err := NewDailyCap(path, 1); err != nil {
		t.Fatalf("own file does not load: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"Version":1,"days":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDailyCap(path, 1); err == nil {
		t.Error(`accepted "Version" for "version"`)
	}
}

func TestDailyCapPrunesOldDays(t *testing.T) {
	d, path, now := newCap(t, 1.0)
	for i := 0; i < spendKeepDays+10; i++ {
		*now = now.Add(24 * time.Hour)
		if err := d.Charge(context.Background(), "jev", 0.01); err != nil {
			t.Fatal(err)
		}
	}
	s, err := d.load()
	if err != nil || len(s.Days) != spendKeepDays {
		t.Fatalf("days = %d, err = %v (%s)", len(s.Days), err, path)
	}
}
