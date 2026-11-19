package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Break tests for release.sh provenance (ft-0ca0 #91, round 2). Same scratch harness and fake bundle.sh as
// release_attack_test.go.

// An untracked file that the build includes (a new .go file in a package dir) appears during the build.
func TestAttackUntrackedFileDuringBuildIsRefused(t *testing.T) {
	v := "v1.2.3"
	r := newReleaseRepo(t, v)
	r.setBundle(t)
	out, err := r.run(t, []string{"FAKE_INFO_RAW=tessl=1\n", "FAKE_MUTATE=untracked"}, v, "--skip-tests")
	if err == nil {
		t.Fatalf("release shipped a build made while an untracked file appeared:\n%s", out)
	}
	r.noTagAnywhere(t, v)
}

// go.work is gitignored, so git cannot see the build's input change; the script hashes it before and after.
func TestReleaseGoWorkChangedDuringBuildIsRefused(t *testing.T) {
	v := "v1.2.3"
	r := newReleaseRepo(t, v)
	r.setBundle(t)
	if err := os.WriteFile(filepath.Join(r.root, ".gitignore"), []byte("dist/\nmacos/build/\ntmp/\ngo.work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(t, r.root, "commit", "-q", "-am", "ignore go.work")
	r.git(t, r.root, "push", "-q", "origin", "main")
	if err := os.WriteFile(filepath.Join(r.root, "go.work"), []byte("go 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := r.run(t, []string{"FAKE_INFO_RAW=tessl=1\n", "FAKE_MUTATE=gowork"}, v, "--skip-tests")
	if err == nil || !strings.Contains(out, "build-inputs-changed") {
		t.Fatalf("a go.work edit during the build was not refused (err=%v):\n%s", err, out)
	}
	r.noTagAnywhere(t, v)
}

// An unchanged ignored go.work must not refuse a release.
func TestReleaseUnchangedGoWorkIsNotRefused(t *testing.T) {
	v := "v1.2.3"
	r := newReleaseRepo(t, v)
	r.setBundle(t)
	if err := os.WriteFile(filepath.Join(r.root, ".gitignore"), []byte("dist/\nmacos/build/\ntmp/\ngo.work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(t, r.root, "commit", "-q", "-am", "ignore go.work")
	r.git(t, r.root, "push", "-q", "origin", "main")
	if err := os.WriteFile(filepath.Join(r.root, "go.work"), []byte("go 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := r.run(t, []string{"FAKE_INFO_RAW=tessl=1\n"}, v, "--skip-tests"); err != nil {
		t.Fatalf("a clean release with an ignored go.work was refused: %v\n%s", err, out)
	}
}
