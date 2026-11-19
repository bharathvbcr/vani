package main

import (
	"os"
	"path/filepath"
	"strconv"
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

// A git wrapper that exits 2, printing nothing, for the Nth and later calls whose arguments contain match. An empty
// output from a failed command must never be read as "nothing to report".
func failingGit(t *testing.T, r *releaseRepo, match string, from int) string {
	t.Helper()
	dir := filepath.Dir(r.root)
	wrap := filepath.Join(dir, "gitwrap-"+strings.Fields(match)[0])
	counter := wrap + ".count"
	body := "#!/bin/bash\ncase \" $* \" in *\"" + match + "\"*)\n  n=$(( $(cat " + counter + " 2>/dev/null || echo 0) + 1 ))\n  echo $n > " + counter + "\n  [ $n -lt " + strconv.Itoa(from) + " ] || exit 2 ;;\nesac\nexec " + realGit(t) + " \"$@\"\n"
	if err := os.WriteFile(wrap, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return wrap
}

// Every place release.sh tests a command's output for emptiness: the pre-build status, the post-build status and the
// origin tag lookup. Each must refuse when the command itself fails.
func TestReleaseFailedGitCommandIsNeverEmptyOutput(t *testing.T) {
	cases := []struct {
		name, match string
		from        int
		mutate      string
		want        string
	}{
		{"pre-build status", " status ", 1, "", "git-status-failed"},
		{"post-build status", " status ", 2, "", "git-status-failed"},
		{"post-build status over a dirty tree", " status ", 2, "dirty", "git-status-failed"},
		{"origin tag lookup", " ls-remote --tags ", 1, "", "tag-lookup-failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := "v1.2.3"
			r := newReleaseRepo(t, v)
			r.setBundle(t)
			wrap := failingGit(t, r, c.match, c.from)
			env := []string{"VANI_RELEASE_GIT=" + wrap, "FAKE_INFO_RAW=tessl=1\n"}
			if c.mutate != "" {
				env = append(env, "FAKE_MUTATE="+c.mutate)
			}
			out, err := r.run(t, env, v, "--skip-tests")
			if err == nil || !strings.Contains(out, c.want) {
				t.Fatalf("want a %s refusal, err=%v:\n%s", c.want, err, out)
			}
			r.noTagAnywhere(t, v)
		})
	}
}

// In a dry run a failed git command is reported like any other refusal, not read as clean.
func TestReleaseDryRunReportsFailedGitStatus(t *testing.T) {
	v := "v1.2.3"
	r := newReleaseRepo(t, v)
	r.setBundle(t)
	wrap := failingGit(t, r, " status ", 1)
	out, err := r.run(t, []string{"VANI_RELEASE_GIT=" + wrap, "FAKE_INFO_RAW=tessl=1\n"}, v, "--skip-tests", "--dry-run")
	if err != nil || !strings.Contains(out, "dry-run would refuse: git-status-failed") {
		t.Fatalf("a failed status in a dry run was not reported (err=%v):\n%s", err, out)
	}
}

// A git whose status call fails (output empty, exit 2) must not read as a clean tree.
func TestAttackFailingGitStatusIsNotClean(t *testing.T) {
	v := "v1.2.3"
	r := newReleaseRepo(t, v)
	r.setBundle(t)
	wrap := filepath.Join(filepath.Dir(r.root), "gitwrap")
	body := "#!/bin/bash\ncase \"$*\" in *--untracked-files=no*) exit 2 ;; esac\nexec " + realGit(t) + " \"$@\"\n"
	if err := os.WriteFile(wrap, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := r.run(t, []string{"VANI_RELEASE_GIT=" + wrap, "FAKE_INFO_RAW=tessl=1\n", "FAKE_MUTATE=dirty"}, v, "--skip-tests")
	if err == nil {
		t.Fatalf("a failed tree check let a dirty-tree release through:\n%s", out)
	}
	r.noTagAnywhere(t, v)
}

