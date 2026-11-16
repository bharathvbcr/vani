package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Only Go sources decide whether the binary is stale. The skill's assets are
// read at run time, so a skill that holds only assets (rkb built from its own
// module) has no sources to compare, and a newer template is no reason to
// rebuild.
func TestNewestSourceCountsOnlyGoFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string, mod time.Time) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	old, newer := time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	write("assets/resume_template.html", newer)
	write("assets/config.json", newer)

	if got, f := newestSource(dir); !got.IsZero() {
		t.Fatalf("assets only: newest = %v (%s), want none", got, f)
	}

	write("main.go", old)
	write("main_test.go", newer)
	got, f := newestSource(dir)
	if filepath.Base(f) != "main.go" || !got.Equal(old.Truncate(time.Second)) && !got.Equal(old) {
		t.Fatalf("newest = %v (%s), want main.go at %v", got, f, old)
	}
}
