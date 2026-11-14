package srcutil

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestStarvedRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "starved.json")
	if got, warn := LoadStarved(p, t0); got != nil || warn != "" {
		t.Fatalf("a missing file: %v, %q", got, warn)
	}
	want := []string{"gh:acme", "lv:beta", "wd:ten/site"}
	if err := SaveStarved(p, want, t0); err != nil {
		t.Fatal(err)
	}
	got, warn := LoadStarved(p, t0.Add(time.Hour))
	if !slices.Equal(got, want) || warn != "" {
		t.Fatalf("got %v, %q; want %v in file order", got, warn, want)
	}
}

func TestStarvedSaveBoundsAndDedupes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "starved.json")
	keys := []string{"a", "a", "", strings.Repeat("k", maxStarvedKey+1), "b"}
	for i := 0; i < maxStarvedBoards+10; i++ {
		keys = append(keys, fmt.Sprintf("gh:board%d", i))
	}
	if err := SaveStarved(p, keys, t0); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadStarved(p, t0)
	if len(got) != maxStarvedBoards || got[0] != "a" || got[1] != "b" {
		t.Fatalf("%d keys, starting %v; want %d, deduped, the empty and oversized ones dropped", len(got), got[:2], maxStarvedBoards)
	}
	if err := SaveStarved(p, nil, t0); err != nil {
		t.Fatal(err)
	}
	if got, warn := LoadStarved(p, t0); len(got) != 0 || warn != "" {
		t.Errorf("an empty list read back as %v, %q", got, warn)
	}
}

// Whatever is on disk never fails a run: it is none, and says why.
func TestStarvedLoadIgnoresWhatItCannotUse(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for name, tc := range map[string]struct {
		body string
		warn string
	}{
		"not json":      {`<html>`, "ignored"},
		"other version": {`{"version":9,"at":"2026-10-22T12:00:00Z","boards":["a"]}`, "ignored"},
		"oversized":     {`{"version":1,"at":"2026-10-22T12:00:00Z","boards":["` + strings.Repeat("x", maxStarvedFile) + `"]}`, "over"},
		"stale":         {`{"version":1,"at":"2026-01-01T00:00:00Z","boards":["a"]}`, ""},
		"unstamped":     {`{"version":1,"boards":["a"]}`, ""},
		"bad stamp":     {`{"version":1,"at":"yesterday","boards":["a"]}`, ""},
	} {
		got, warn := LoadStarved(write(strings.ReplaceAll(name, " ", "_"), tc.body), t0)
		if len(got) != 0 || !strings.Contains(warn, tc.warn) || (tc.warn != "" && warn == "") {
			t.Errorf("%s: %v, %q", name, got, warn)
		}
	}
	// A directory where the file should be is a warning, not a panic.
	if got, warn := LoadStarved(dir, t0); len(got) != 0 || warn == "" {
		t.Errorf("a directory: %v, %q", got, warn)
	}
}

func TestStarvedLoadBoundsWhatItKeeps(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"version":1,"at":"2026-10-22T12:00:00Z","boards":["dup","dup","",`)
	fmt.Fprintf(&sb, "%q", strings.Repeat("k", maxStarvedKey+1))
	for i := 0; i < maxStarvedBoards+5; i++ {
		fmt.Fprintf(&sb, `,"b%d"`, i)
	}
	sb.WriteString(`]}`)
	p := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(p, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got, _ := LoadStarved(p, t0)
	if len(got) != maxStarvedBoards || got[0] != "dup" || got[1] != "b0" {
		t.Fatalf("%d keys starting %v", len(got), got[:2])
	}
}
