package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// scripts/release.sh refuses before it builds or touches git state, so these
// tests drive only those refusals, with gh and git replaced by fakes through
// the documented VANI_RELEASE_GH / VANI_RELEASE_GIT seams. Nothing here
// reaches the network or the real repo.

func releaseScript(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("release.sh reads the Info.plist with PlistBuddy (darwin only)")
	}
	p, err := filepath.Abs("../../scripts/release.sh")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func plistVersion(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../macos/Support/App-Info.plist")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`<key>CFBundleShortVersionString</key>\s*<string>([^<]+)</string>`).FindSubmatch(b)
	if m == nil {
		t.Fatal("no CFBundleShortVersionString in App-Info.plist")
	}
	return "v" + string(m[1])
}

// fakeTool writes an executable shell script that prints out and exits status.
func fakeTool(t *testing.T, name, out string, status int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	body := "#!/bin/sh\necho '" + out + "'\nexit " + string(rune('0'+status)) + "\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func runRelease(t *testing.T, gh, git string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(releaseScript(t), args...)
	cmd.Env = append(os.Environ(), "VANI_RELEASE_GH="+gh, "VANI_RELEASE_GIT="+git)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestReleaseRefusals(t *testing.T) {
	good := plistVersion(t)
	privateGH := fakeTool(t, "gh", "true", 0)
	publicGH := fakeTool(t, "gh", "false", 0)
	brokenGH := fakeTool(t, "gh", "boom", 1)
	// A git that fails any call: the refusals below must come before git is used.
	noGit := fakeTool(t, "git", "git must not be called", 1)

	cases := []struct {
		name string
		gh   string
		args []string
		want string
	}{
		{"no version", privateGH, nil, "VERSION missing"},
		{"bad format", privateGH, []string{"0.1.0"}, "version-format"},
		{"bad format prerelease", privateGH, []string{"v0.1.0-rc1"}, "version-format"},
		{"leading zero", privateGH, []string{"v01.1.0"}, "version-format"},
		{"two versions", privateGH, []string{good, good}, "more than one VERSION"},
		{"unknown flag", privateGH, []string{good, "--force"}, "unknown flag"},
		{"mismatch", privateGH, []string{"v9.9.9", "--dry-run"}, "version-mismatch"},
		{"public repo", publicGH, []string{good, "--dry-run"}, "repo-not-private"},
		{"public repo real", publicGH, []string{good}, "repo-not-private"},
		{"gh fails", brokenGH, []string{good, "--dry-run"}, "repo-visibility-unknown"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := runRelease(t, c.gh, noGit, c.args...)
			if err == nil {
				t.Fatalf("expected refusal, got success:\n%s", out)
			}
			if !strings.Contains(out, c.want) {
				t.Fatalf("output lacks %q:\n%s", c.want, out)
			}
			if strings.Contains(out, "built ") || strings.Contains(out, "published") {
				t.Fatalf("a refusal must not build or publish:\n%s", out)
			}
		})
	}
}

func TestReleaseRefusesVaniCLIOverride(t *testing.T) {
	cmd := exec.Command(releaseScript(t), plistVersion(t), "--dry-run")
	cmd.Env = append(os.Environ(), "VANI_CLI_BINARY=/bin/echo", "VANI_RELEASE_GH="+fakeTool(t, "gh", "true", 0))
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "vani-cli-override") {
		t.Fatalf("want vani-cli-override refusal, err=%v:\n%s", err, out)
	}
}

// The tests below run the whole script, build and tag steps included, inside a scratch git repo
// under the worktree's gitignored tmp/ whose origin is a local bare repo. The build is a fake
// macos/scripts/bundle.sh (it can move HEAD or edit a tracked file mid-build, and reports whether
// it "built with tessl"), codesign and ditto are fakes on PATH, and gh is a fake that logs its
// calls. Nothing reaches the network, the real repo or a real signing identity.

type releaseRepo struct {
	root, origin, ghLog string
	env                 []string
}

const fakeBundle = `#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
APP="$ROOT/macos/build/Vani Apply.app"
mkdir -p "$APP/Contents/Helpers"
printf '#!/bin/sh\necho "vani %s"\n' "$VANI_VERSION" > "$APP/Contents/Helpers/vani"
chmod +x "$APP/Contents/Helpers/vani"
if [ -n "${VANI_BUILD_INFO_OUT:-}" ] && [ "${FAKE_TESSL:-1}" != "none" ]; then echo "tessl=${FAKE_TESSL:-1}" > "$VANI_BUILD_INFO_OUT"; fi
case "${FAKE_MUTATE:-}" in
  head) echo more >> "$ROOT/other.txt"; git -C "$ROOT" add other.txt; git -C "$ROOT" commit -q -m "moved during the build" ;;
  dirty) echo changed >> "$ROOT/README.md" ;;
  revert) echo changed >> "$ROOT/README.md"; git -C "$ROOT" checkout -q -- README.md ;;
esac
`

func newReleaseRepo(t *testing.T, version string) *releaseRepo {
	t.Helper()
	script := releaseScript(t)
	base, err := filepath.Abs("../../tmp")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(base, "release-scratch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	r := &releaseRepo{root: filepath.Join(dir, "repo"), origin: filepath.Join(dir, "origin.git"), ghLog: filepath.Join(dir, "gh.log")}
	write := func(path, body string, mode os.FileMode) {
		p := filepath.Join(r.root, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	src, err := os.ReadFile(script)
	if err != nil {
		t.Fatal(err)
	}
	write("scripts/release.sh", string(src), 0o755)
	write("macos/scripts/bundle.sh", fakeBundle, 0o755)
	write("macos/Support/App-Info.plist", fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>CFBundleShortVersionString</key><string>%s</string></dict></plist>
`, strings.TrimPrefix(version, "v")), 0o644)
	write(".gitignore", "dist/\nmacos/build/\ntmp/\n", 0o644)
	write("README.md", "scratch\n", 0o644)
	write("other.txt", "other\n", 0o644)
	bin := filepath.Join(dir, "bin")
	write("../bin/codesign", "#!/bin/sh\necho 'TeamIdentifier=SCRATCH' >&2\necho 'Authority=Scratch Identity: test' >&2\nexit 0\n", 0o755)
	write("../bin/ditto", "#!/bin/sh\nfor a in \"$@\"; do d=\"$a\"; done\necho zip > \"$d\"\n", 0o755)
	write("../gh", "#!/bin/sh\necho \"$@\" >> '"+r.ghLog+"'\ncase \"$1 $2\" in \"repo view\") echo true ;; esac\n", 0o755)
	r.env = []string{"PATH=" + bin + ":" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"VANI_RELEASE_GH=" + filepath.Join(dir, "gh"), "VANI_RELEASE_GIT=" + realGit(t)}
	r.git(t, r.root, "init", "-q", "-b", "main")
	r.git(t, dir, "init", "-q", "--bare", "-b", "main", r.origin)
	r.git(t, r.root, "add", ".")
	r.git(t, r.root, "commit", "-q", "-m", "scratch")
	r.git(t, r.root, "remote", "add", "origin", r.origin)
	r.git(t, r.root, "push", "-q", "origin", "main")
	return r
}

func realGit(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	return p
}

func (r *releaseRepo) cleanEnv(extra ...string) []string {
	var env []string
	for _, kv := range r.env {
		env = append(env, kv)
	}
	return append(env, extra...)
}

func (r *releaseRepo) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(realGit(t), append([]string{"-C", dir}, args...)...)
	cmd.Env = r.cleanEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *releaseRepo) run(t *testing.T, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(filepath.Join(r.root, "scripts/release.sh"), args...)
	cmd.Env = r.cleanEnv(extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *releaseRepo) ghCalls(t *testing.T) string {
	b, _ := os.ReadFile(r.ghLog)
	return string(b)
}

// noTagAnywhere fails if the version was tagged locally or on origin, or a release was created.
func (r *releaseRepo) noTagAnywhere(t *testing.T, version string) {
	t.Helper()
	if tags := r.git(t, r.root, "tag", "--list", version); tags != "" {
		t.Errorf("a local tag %s exists after a refusal", tags)
	}
	if tags := r.git(t, r.origin, "tag", "--list", version); tags != "" {
		t.Errorf("origin has tag %s after a refusal", tags)
	}
	if strings.Contains(r.ghCalls(t), "release create") {
		t.Errorf("a release was created after a refusal:\n%s", r.ghCalls(t))
	}
}

func TestReleaseTagsTheBuiltCommit(t *testing.T) {
	v := "v1.2.3"
	r := newReleaseRepo(t, v)
	head := r.git(t, r.root, "rev-parse", "HEAD")
	out, err := r.run(t, nil, v, "--skip-tests")
	if err != nil {
		t.Fatalf("release failed: %v\n%s", err, out)
	}
	if got := r.git(t, r.root, "rev-parse", v+"^{commit}"); got != head {
		t.Fatalf("tag points at %s, built %s", got, head)
	}
	if got := r.git(t, r.origin, "rev-parse", v+"^{commit}"); got != head {
		t.Fatalf("origin tag points at %s, built %s", got, head)
	}
	if !strings.Contains(r.ghCalls(t), "release create "+v) {
		t.Fatalf("no release created:\n%s", r.ghCalls(t))
	}
	notes, err := os.ReadFile(filepath.Join(r.root, "dist/.release-notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(notes), "WITHOUT the Tessl") {
		t.Fatalf("notes claim a no-tessl build for a tessl build:\n%s", notes)
	}
}

func TestReleaseRefusesWhenTheTreeMovesDuringTheBuild(t *testing.T) {
	cases := []struct{ name, mutate, want string }{
		{"head moves", "head", "head-moved"},
		{"tracked file changes", "dirty", "tree-changed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := "v1.2.3"
			r := newReleaseRepo(t, v)
			out, err := r.run(t, []string{"FAKE_MUTATE=" + c.mutate}, v, "--skip-tests")
			if err == nil {
				t.Fatalf("expected refusal, got success:\n%s", out)
			}
			if !strings.Contains(out, c.want) {
				t.Fatalf("output lacks %q:\n%s", c.want, out)
			}
			if strings.Contains(out, "published") {
				t.Fatalf("a refusal must not publish:\n%s", out)
			}
			r.noTagAnywhere(t, v)
		})
	}
}

// A tracked file edited and restored during the build leaves HEAD and the tree as they were: not a refusal.
func TestReleaseAllowsAnEditThatIsRestored(t *testing.T) {
	v := "v1.2.3"
	r := newReleaseRepo(t, v)
	if out, err := r.run(t, []string{"FAKE_MUTATE=revert"}, v, "--skip-tests"); err != nil {
		t.Fatalf("release failed: %v\n%s", err, out)
	}
}

func TestReleaseDryRunReportsAMovedTreeAndNeverTags(t *testing.T) {
	v := "v1.2.3"
	r := newReleaseRepo(t, v)
	out, err := r.run(t, []string{"FAKE_MUTATE=head"}, v, "--skip-tests", "--dry-run")
	if err != nil {
		t.Fatalf("dry run must report, not fail: %v\n%s", err, out)
	}
	if !strings.Contains(out, "dry-run would refuse: head-moved") || !strings.Contains(out, "a real release would be refused for") {
		t.Fatalf("dry run did not report the moved HEAD:\n%s", out)
	}
	r.noTagAnywhere(t, v)
}

func TestReleaseRefusesABuildWithoutTessl(t *testing.T) {
	for _, state := range []string{"0", "unknown", "none", "garbage"} {
		t.Run("tessl="+state, func(t *testing.T) {
			v := "v1.2.3"
			r := newReleaseRepo(t, v)
			out, err := r.run(t, []string{"FAKE_TESSL=" + state}, v, "--skip-tests")
			if err == nil || !strings.Contains(out, "no-tessl") {
				t.Fatalf("want a no-tessl refusal, err=%v:\n%s", err, out)
			}
			r.noTagAnywhere(t, v)
		})
	}
}

func TestReleaseAllowNoTesslSaysSoInTheNotes(t *testing.T) {
	v := "v1.2.3"
	r := newReleaseRepo(t, v)
	out, err := r.run(t, []string{"FAKE_TESSL=0"}, v, "--skip-tests", "--allow-no-tessl")
	if err != nil {
		t.Fatalf("release failed: %v\n%s", err, out)
	}
	notes, err := os.ReadFile(filepath.Join(r.root, "dist/.release-notes.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(notes), "Built WITHOUT the Tessl embedding engine") {
		t.Fatalf("notes do not say the build lacks tessl:\n%s", notes)
	}
	if !strings.Contains(r.ghCalls(t), "release create "+v) {
		t.Fatalf("no release created:\n%s", r.ghCalls(t))
	}
}

func TestReleaseDryRunReportsANoTesslBuild(t *testing.T) {
	v := "v1.2.3"
	r := newReleaseRepo(t, v)
	out, err := r.run(t, []string{"FAKE_TESSL=0"}, v, "--skip-tests", "--dry-run")
	if err != nil || !strings.Contains(out, "dry-run would refuse: no-tessl") {
		t.Fatalf("want a dry-run report, err=%v:\n%s", err, out)
	}
	r.noTagAnywhere(t, v)
}
