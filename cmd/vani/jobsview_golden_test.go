package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// longFloat matches a float printed at full float64 precision.
var longFloat = regexp.MustCompile(`-?\d+\.\d{13,}(?:[eE][-+]?\d+)?`)

// archStableFloats rounds every full-precision float to 12 significant digits.
// The last bit of a float64 can differ by architecture: arm64 fuses a*b+c into
// one rounding and amd64 runs its own math.Exp assembly, so the keyword part
// printed 0.5447979521118679 on arm64 and ...678 on amd64 (CI). The golden
// pins the views, not the last ulp of the arithmetic behind them.
func archStableFloats(s string) string {
	return longFloat.ReplaceAllStringFunc(s, func(m string) string {
		v, err := strconv.ParseFloat(m, 64)
		if err != nil {
			return m
		}
		return strconv.FormatFloat(v, 'g', 12, 64)
	})
}

// TestJobsViewGolden pins the exact `vani jobs list|stats --json` output of
// the ranking views (every view and sort, the filters, stats, folding), so the
// selection, sort and stats logic can move between packages without a change
// in output. Regenerate with VANI_UPDATE_GOLDEN=1 only for an intended change.
func TestJobsViewGolden(t *testing.T) {
	cfgPath, state := setup(t)
	fixedNow(t)
	writeTarget(t, cfgPath, "Inference engineering in Go and Rust, with Kubernetes.")
	seedInsight(t, state, insightSeeds...)
	mustCode(t, 0, "jobs", "mark", "gh:a:gone", "saved")
	mustCode(t, 0, "jobs", "mark", "gh:b:sales", "hidden")
	cases := []string{
		"list --view new", "list --view all", "list --view saved", "list --view hidden",
		"list --sort priority", "list --sort relevance", "list --sort fresh", "list --sort company", "list --sort salary",
		"list --view all --sort company", "list --view all --sort fresh",
		"list --fit strong_fit", "list --view all --fit none --max-age 10", "list --q engineer",
		"list --view all --open --min-relevance 10", "list --view all --exclude old --company acme",
		"stats --view all", "stats --view new", "stats --view all --fit strong_fit,none",
	}
	var b strings.Builder
	for _, c := range cases {
		o, _ := mustCode(t, 0, append([]string{"jobs"}, append(strings.Fields(c), "--json")...)...)
		b.WriteString("## " + c + "\n" + strings.ReplaceAll(o, state, "<state>") + "\n")
	}
	golden := filepath.Join("testdata", "jobsview.golden")
	if os.Getenv("VANI_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if w, g := archStableFloats(string(want)), archStableFloats(b.String()); w != g {
		// Name the first line that differs, so a failure on another machine (CI)
		// says what changed without a local rerun.
		wl, gl := strings.Split(w, "\n"), strings.Split(g, "\n")
		i := 0
		for i < len(wl) && i < len(gl) && wl[i] == gl[i] {
			i++
		}
		clip := func(ls []string) string {
			if i >= len(ls) {
				return "<end of output>"
			}
			if s := ls[i]; len(s) > 2000 {
				return s[:2000] + "…"
			}
			return ls[i]
		}
		t.Fatalf("jobs views changed at line %d; compare %s against a VANI_UPDATE_GOLDEN=1 run\nwant: %s\ngot:  %s", i+1, golden, clip(wl), clip(gl))
	}
}

func TestArchStableFloats(t *testing.T) {
	arm, amd := `{"value":0.5447979521118679}`, `{"value":0.5447979521118678}`
	if archStableFloats(arm) != archStableFloats(amd) {
		t.Errorf("a last-bit difference must compare equal: %s vs %s", archStableFloats(arm), archStableFloats(amd))
	}
	if archStableFloats(`{"value":0.5447979521118679}`) == archStableFloats(`{"value":0.5447979531118679}`) {
		t.Error("a difference in the 10th digit must still show")
	}
	for _, keep := range []string{`{"value":0.4,"weight":0.25,"n":12}`, `"2026-10-18T12:00:00Z"`, `{"x":-1.5e-07}`} {
		if got := archStableFloats(keep); got != keep {
			t.Errorf("short numbers must be left alone: %s -> %s", keep, got)
		}
	}
}
