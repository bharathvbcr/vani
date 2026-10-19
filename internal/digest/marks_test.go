package digest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestHiddenRolesLeftOut(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:a:1", posting.Explained, "Shown", map[string]string{"rank": "50"}),
		rec("gh:a:2", posting.Explained, "HiddenRanked", map[string]string{"rank": "90"}),
		rec("gh:a:3", posting.Unsure, "HiddenUnsure", nil),
		rec("gh:a:4", posting.Explained, "AppliedStays", map[string]string{"rank": "10"}),
	}
	m := map[posting.Key]marks.Mark{"gh:a:2": marks.Hidden, "gh:a:3": marks.Hidden, "gh:a:4": marks.Applied}
	if sel := Select(recs, m); len(sel) != 2 || sel[0].Key != "gh:a:1" || sel[1].Key != "gh:a:4" {
		t.Errorf("select %v", sel)
	}
	md, keys := Render(recs, m, day, Counts{})
	if len(keys) != 2 || keys[0] != "gh:a:1" || keys[1] != "gh:a:4" {
		t.Errorf("keys %v", keys)
	}
	for _, s := range []string{"HiddenRanked", "HiddenUnsure", "## Unsure"} {
		if strings.Contains(md, s) {
			t.Errorf("hidden role leaked (%q):\n%s", s, md)
		}
	}
	if !strings.Contains(md, "## Ranked roles (2)") {
		t.Errorf("ranked count:\n%s", md)
	}
	// Everything hidden reads as no new roles, not as an empty heading.
	all := map[posting.Key]marks.Mark{"gh:a:1": marks.Hidden, "gh:a:2": marks.Hidden, "gh:a:3": marks.Hidden, "gh:a:4": marks.Hidden}
	if md, keys := Render(recs, all, day, Counts{}); len(keys) != 0 || !strings.Contains(md, "No new roles today.") {
		t.Errorf("all hidden: %v\n%s", keys, md)
	}
}

func TestSavedSection(t *testing.T) {
	recs := []ledger.Record{
		rec("gh:a:1", posting.Digested, "OldSaved", map[string]string{"rank": "40"}),
		rec("gh:a:2", posting.Explained, "NewSaved", map[string]string{"rank": "80"}),
		rec("gh:a:3", posting.Dead, "ClosedSaved", nil),
		rec("gh:a:4", posting.Duplicate, "DupSaved", nil),
		rec("gh:a:5", posting.Scored, "[click](javascript:x) <b>", nil),
		rec("gh:a:6", posting.Digested, "NotSaved", nil),
	}
	recs[4].Posting.URL = "javascript:alert(1)"
	m := map[posting.Key]marks.Mark{"gh:a:1": marks.Saved, "gh:a:2": marks.Saved, "gh:a:3": marks.Saved, "gh:a:4": marks.Saved, "gh:a:5": marks.Saved}
	md, keys := Render(recs, m, day, Counts{})
	// Only the new role is returned: listing a saved role again consumes nothing.
	if len(keys) != 1 || keys[0] != "gh:a:2" {
		t.Errorf("keys %v", keys)
	}
	_, section, ok := strings.Cut(md, "## Saved (3)\n")
	if !ok {
		t.Fatalf("no Saved (3) section:\n%s", md)
	}
	section, _, _ = strings.Cut(section, "---")
	for _, want := range []string{"NewSaved at Acme: <https://example.com/jobs/gh:a:2>", "OldSaved at Acme", `\[click\]\(javascript\:x\) \<b\> at Acme: (no valid link)`} {
		if !strings.Contains(section, want) {
			t.Errorf("Saved section lacks %q:\n%s", want, section)
		}
	}
	for _, not := range []string{"ClosedSaved", "DupSaved", "NotSaved", "javascript:alert"} {
		if strings.Contains(md, not) {
			t.Errorf("%q listed:\n%s", not, md)
		}
	}
	if strings.Index(section, "NewSaved") > strings.Index(section, "OldSaved") {
		t.Error("Saved is not in rank order")
	}
	if md, _ := Render(recs, nil, day, Counts{}); strings.Contains(md, "## Saved") {
		t.Error("Saved section with no marks")
	}
}

func TestSavedSectionIsBounded(t *testing.T) {
	var recs []ledger.Record
	m := map[posting.Key]marks.Mark{}
	for i := range maxSaved + 7 {
		k := fmt.Sprintf("gh:a:%03d", i)
		recs = append(recs, rec(k, posting.Digested, "R"+k, nil))
		m[posting.Key(k)] = marks.Saved
	}
	md, _ := Render(recs, m, day, Counts{})
	if n := strings.Count(md, " at Acme: "); n != maxSaved {
		t.Errorf("%d saved lines, want %d", n, maxSaved)
	}
	if !strings.Contains(md, fmt.Sprintf("## Saved (%d)", maxSaved+7)) || !strings.Contains(md, "…and 7 more in Jobs") {
		t.Errorf("bound not reported:\n%s", md[len(md)-300:])
	}
}

// Saved roles alone are not news: a same-day rerun with nothing new leaves
// today's digest alone even though the Saved list would render.
func TestWriteSavedOnlyRerunLeavesDigestAlone(t *testing.T) {
	dir := t.TempDir()
	recs := []ledger.Record{rec("gh:a:1", posting.Digested, "OldSaved", nil)}
	m := map[posting.Key]marks.Mark{"gh:a:1": marks.Saved}
	path, keys, err := Write(dir, day, recs, m, Counts{})
	if err != nil || len(keys) != 0 {
		t.Fatalf("first: %v %v", keys, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "No new roles today.") || !strings.Contains(string(data), "## Saved (1)") {
		t.Fatalf("first digest: %v\n%s", err, data)
	}
	p2, k2, err := Write(dir, day, recs, m, Counts{})
	if err != nil || p2 != path || len(k2) != 0 {
		t.Fatalf("rerun: %s %v %v", p2, k2, err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(path)); len(entries) != 1 {
		t.Errorf("files: %v", entries)
	}
}
