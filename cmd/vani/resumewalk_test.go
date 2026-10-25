package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/jobview"
)

// The listing reads the resume store once, when the insight is built, and looks
// each job up after that: with the store gone from under it, every row still
// carries what the one walk found, and no row asks the filesystem again.
func TestInsightReadsTheResumeStoreOnceNotPerJob(t *testing.T) {
	snap, m := rowFixture()
	state := t.TempDir()
	withResume := map[string]bool{string(snap.Records[0].Key): true, string(snap.Records[5].Key): true, string(snap.Records[14].Key): true}
	for k := range withResume {
		rvSave(t, state, k, nil)
	}
	in, err := newInsightFor(config.Config{}, nil, state, 14, snap, m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(state, "resumes")); err != nil {
		t.Fatal(err)
	}
	for _, r := range snap.Records {
		j := jobview.ToJob(r, m[r.Key])
		in.Enrich(&j, r, showKeywords)
		if withResume[string(r.Key)] {
			if j.Resume == nil || j.Resume.Versions != 1 || j.Resume.Error != "" {
				t.Errorf("%s: resume = %+v, want the version the walk found", r.Key, j.Resume)
			}
		} else if j.Resume != nil {
			t.Errorf("%s: resume = %+v, want none", r.Key, j.Resume)
		}
	}
}

// A store that cannot be walked shows on every row as unreadable, with a
// warning on the listing, and never as the absence of a resume.
func TestInsightWithAnUnwalkableResumeStoreSaysUnreadableOnEveryRow(t *testing.T) {
	snap, m := rowFixture()
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(state, "resumes"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	in, err := newInsightFor(config.Config{}, nil, state, 14, snap, m)
	if err != nil {
		t.Fatal(err)
	}
	if in.info.Warning == "" {
		t.Error("no warning that resume versions are not shown")
	}
	for _, r := range snap.Records {
		j := jobview.ToJob(r, m[r.Key])
		in.Enrich(&j, r, showKeywords)
		if j.Resume == nil || j.Resume.Error != "unreadable" {
			t.Fatalf("%s: resume = %+v, want unreadable", r.Key, j.Resume)
		}
	}
}
