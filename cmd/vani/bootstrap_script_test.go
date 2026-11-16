package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scripts/bootstrap.sh takes a fresh clone to a built vani. These tests run it against a throwaway repo root,
// with real git cloning from local file:// repositories (no network) and a fake go that answers `env`, `list -m`
// and `build` (the stub binaries it writes stand in for vani and rkb). They pin what the script must never do:
// touch an existing go.work or checkout, leave a half-cloned directory behind, or carry on past a refusal.

const fakeGoScript = `#!/bin/sh
# FAKE_GO_VERSION, FAKE_GO_TOOLCHAIN: what go env reports. FAKE_GO_DIR: the Dir every module resolves to
# (FAKE_GO_LIST_FAIL=1 makes list fail). FAKE_VANI_INIT_RC/OUT and FAKE_VANI_CHECK_RC: the stub vani's answers.
echo "go $* GOWORK=${GOWORK-unset}" >> "$FAKE_GO_LOG"
if [ "$1" = env ]; then
  case "$2" in
    GOVERSION) echo "go${FAKE_GO_VERSION:-1.27.4}" ;;
    GOTOOLCHAIN) echo "${FAKE_GO_TOOLCHAIN:-auto}" ;;
  esac
  exit 0
fi
dir=.
[ "$1" = -C ] && { dir=$2; shift 2; }
case "$1" in
  list)
    [ -z "$FAKE_GO_LIST_FAIL" ] || { echo "go: cannot find module"; exit 1; }
    shift 3
    for m in "$@"; do
      case "$m" in
        github.com/bharathvbcr/decide) echo "$m ${FAKE_GO_DECIDE_DIR:-$dir/decide}" ;;
        *) echo "$m $FAKE_GO_DIR" ;;
      esac
    done ;;
  build)
    out=$3
    cat > "$out" <<'STUB'
#!/bin/sh
case "$1" in
  init) [ -n "$FAKE_VANI_INIT_OUT" ] && echo "$FAKE_VANI_INIT_OUT"; exit ${FAKE_VANI_INIT_RC:-0} ;;
  config) echo "ok: stub config"; exit ${FAKE_VANI_CHECK_RC:-0} ;;
esac
exit 0
STUB
    chmod +x "$out" ;;
esac
exit 0
`

type bootFixture struct {
	t     *testing.T
	root  string // fake repo root: go.mod + scripts/
	deps  string
	lock  string
	goBin string
	goLog string
	env   []string
	srcs  map[string]srcRepo
}

type srcRepo struct {
	url  string
	shas []string // commits, oldest first
}

func bootstrapScript(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bootstrap.sh needs bash")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("bootstrap.sh tests clone with real git")
	}
	p, err := filepath.Abs("../../scripts/bootstrap.sh")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// git runs git with the owner's global and system config shut out, so hooks, signing and templates on this
// machine cannot change what the tests see.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// newSrcRepo makes a repository with two commits that serves any reachable sha to a fetch.
func newSrcRepo(t *testing.T, name string) srcRepo {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "config", "uploadpack.allowAnySHA1InWant", "true")
	var shas []string
	for _, body := range []string{"one\n", "two\n"} {
		if err := os.WriteFile(filepath.Join(dir, "README"), []byte(name+" "+body), 0o644); err != nil {
			t.Fatal(err)
		}
		gitIn(t, dir, "add", "README")
		gitIn(t, dir, "commit", "-q", "-m", body)
		shas = append(shas, gitIn(t, dir, "rev-parse", "HEAD"))
	}
	return srcRepo{url: "file://" + dir, shas: shas}
}

func newBootFixture(t *testing.T) *bootFixture {
	t.Helper()
	bootstrapScript(t)
	base := t.TempDir()
	f := &bootFixture{t: t, root: filepath.Join(base, "Vani"), deps: filepath.Join(base, "deps"),
		goBin: filepath.Join(base, "bin", "go"), goLog: filepath.Join(base, "go.log"), srcs: map[string]srcRepo{}}
	for _, d := range []string{filepath.Join(f.root, "scripts"), filepath.Join(f.root, "rkb"), filepath.Join(f.root, "decide"), filepath.Dir(f.goBin)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.root, "go.mod"), []byte("module example.invalid/vani\n\ngo 1.27.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.goBin, []byte(fakeGoScript), 0o755); err != nil {
		t.Fatal(err)
	}
	var lock strings.Builder
	lock.WriteString("# test lock\n\n")
	for _, n := range []string{"Manvi", "DevCouncil", "gusset", "tessl"} {
		s := newSrcRepo(t, n)
		f.srcs[n] = s
		lock.WriteString(n + "  " + s.url + "  " + s.shas[1] + "  # trailing comment\n")
	}
	f.lock = filepath.Join(f.root, "scripts", "deps.lock")
	f.writeLock(lock.String())
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	// A built environment, not os.Environ(): no OPENROUTER_API_KEY, HF_HOME or RKB_CHROME leaks in.
	f.env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + t.TempDir(),
		"VANI_NO_KEYCHAIN=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"VANI_BOOTSTRAP_ROOT=" + f.root, "VANI_BOOTSTRAP_LOCK=" + f.lock, "VANI_BOOTSTRAP_GO=" + f.goBin,
		"FAKE_GO_LOG=" + f.goLog, "FAKE_GO_DIR=" + base,
	}
	return f
}

func (f *bootFixture) writeLock(s string) {
	f.t.Helper()
	if err := os.WriteFile(f.lock, []byte(s), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *bootFixture) run(extraEnv []string, args ...string) (string, int) {
	f.t.Helper()
	cmd := exec.Command(bootstrapScript(f.t), args...)
	cmd.Env = append(append([]string{}, f.env...), extraEnv...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			f.t.Fatalf("bootstrap did not run: %v", err)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestBootstrapFreshClonesAtLockWritesGoWorkAndBuilds(t *testing.T) {
	f := newBootFixture(t)
	out, code := f.run(nil, "--deps", f.deps)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for n, s := range f.srcs {
		if got := gitIn(t, filepath.Join(f.deps, n), "rev-parse", "HEAD"); got != s.shas[1] {
			t.Errorf("%s at %s, lock says %s", n, got, s.shas[1])
		}
	}
	work, err := os.ReadFile(filepath.Join(f.root, "go.work"))
	if err != nil {
		t.Fatalf("no go.work written: %v\n%s", err, out)
	}
	for _, want := range []string{
		"go 1.27.4", "\t./decide\n", `"` + f.deps + `/Manvi/manvi"`,
		`replace github.com/bharathvbcr/Manvi/manvi v0.0.0 => "` + f.deps + `/Manvi/manvi"`,
		`replace github.com/bharathvbcr/tessl/go v0.0.0 => "` + f.deps + `/tessl/go"`,
	} {
		if !strings.Contains(string(work), want) {
			t.Errorf("go.work lacks %q:\n%s", want, work)
		}
	}
	for _, b := range []string{"vani", "rkb"} {
		if !exists(filepath.Join(f.root, "build", b)) {
			t.Errorf("build/%s not built", b)
		}
	}
	log, _ := os.ReadFile(f.goLog)
	if !strings.Contains(string(log), "/rkb build -o "+f.root+"/build/rkb . GOWORK=off") {
		t.Errorf("rkb must build outside the workspace; go calls:\n%s", log)
	}
	for _, want := range []string{"done:", "OpenRouter key", "rkb", "DevPrism",
		"Chrome", "poppler", "Tessl engine", "Embedding model", "lightpanda"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// No key in the environment: on macOS the Keychain is the next place, and
	// VANI_NO_KEYCHAIN keeps it unasked; elsewhere there is no Keychain at all.
	keyRow := "missing      export OPENROUTER_API_KEY"
	if runtime.GOOS == "darwin" {
		keyRow = "not checked  VANI_NO_KEYCHAIN is set"
	}
	if !strings.Contains(out, keyRow) {
		t.Errorf("OpenRouter key row on %s should read %q:\n%s", runtime.GOOS, keyRow, out)
	}
	// A second run finds everything in place and changes nothing.
	before, _ := os.ReadFile(filepath.Join(f.root, "go.work"))
	out2, code2 := f.run(nil, "--deps", f.deps)
	after, _ := os.ReadFile(filepath.Join(f.root, "go.work"))
	if code2 != 0 || string(before) != string(after) || !strings.Contains(out2, "go.work exists") {
		t.Fatalf("rerun: exit %d, go.work changed=%v:\n%s", code2, string(before) != string(after), out2)
	}
}

func TestBootstrapLeavesExistingGoWorkAndClonesNothing(t *testing.T) {
	f := newBootFixture(t)
	mine := "go 1.27.4\n\nuse .\n// the owner's own\n"
	if err := os.WriteFile(filepath.Join(f.root, "go.work"), []byte(mine), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := f.run(nil, "--deps", f.deps)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	got, _ := os.ReadFile(filepath.Join(f.root, "go.work"))
	if string(got) != mine {
		t.Fatalf("go.work was changed:\n%s", got)
	}
	if exists(f.deps) {
		t.Fatal("cloned although go.work exists")
	}
	if !strings.Contains(out, "--deps "+f.deps+" ignored") {
		t.Errorf("an ignored --deps must be said:\n%s", out)
	}
}

func TestBootstrapLeavesCheckoutAtOtherCommitAlone(t *testing.T) {
	f := newBootFixture(t)
	m := f.srcs["Manvi"]
	dir := filepath.Join(f.deps, "Manvi")
	if err := os.MkdirAll(f.deps, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.deps, "clone", "-q", m.url, dir)
	gitIn(t, dir, "checkout", "-q", "--detach", m.shas[0])
	if err := os.WriteFile(filepath.Join(dir, "local-edit"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := f.run(nil, "--deps", f.deps)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := gitIn(t, dir, "rev-parse", "HEAD"); got != m.shas[0] {
		t.Fatalf("an existing checkout was moved to %s", got)
	}
	if !exists(filepath.Join(dir, "local-edit")) {
		t.Fatal("an uncommitted file in an existing checkout was removed")
	}
	for _, want := range []string{"Manvi: at " + m.shas[0][:12] + ", the lock says " + m.shas[1][:12], "Manvi: has uncommitted changes"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestBootstrapFailedFetchRemovesPartialAndWritesNoGoWork(t *testing.T) {
	f := newBootFixture(t)
	lock, _ := os.ReadFile(f.lock)
	missing := strings.Repeat("ab", 20) // well-formed, in no repository
	f.writeLock(strings.Replace(string(lock), f.srcs["gusset"].shas[1], missing, 1))
	out, code := f.run(nil, "--deps", f.deps)
	if code != 1 || !strings.Contains(out, "gusset: could not fetch "+missing[:12]) {
		t.Fatalf("want a fetch failure for gusset, exit %d:\n%s", code, out)
	}
	if exists(filepath.Join(f.deps, "gusset")) {
		t.Fatal("the partial gusset clone was left behind")
	}
	if exists(filepath.Join(f.root, "go.work")) {
		t.Fatal("go.work written after a failed clone")
	}
	if exists(filepath.Join(f.root, "build", "vani")) {
		t.Fatal("built after a failed clone")
	}
}

func TestBootstrapRefusals(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  []string
		lock func(f *bootFixture, orig string) string
		prep func(f *bootFixture)
		code int
		want string
	}{
		{name: "unknown flag", args: []string{"--force"}, code: 2, want: "unknown argument --force"},
		{name: "deps without value", args: []string{"--deps"}, code: 2, want: "--deps needs a directory"},
		{name: "deps empty", args: []string{"--deps", ""}, code: 2, want: "--deps needs a directory"},
		{name: "go missing", env: []string{"VANI_BOOTSTRAP_GO=/nonexistent/go"}, code: 1, want: "go not found"},
		{name: "go too old, local toolchain", env: []string{"FAKE_GO_VERSION=1.26.9", "FAKE_GO_TOOLCHAIN=local"}, code: 1,
			want: "older than 1.27.4 and GOTOOLCHAIN=local"},
		{name: "lock missing column", code: 1, want: "want 'name url commit'",
			lock: func(f *bootFixture, o string) string { return o + "extra https://example.invalid/x\n" }},
		{name: "lock short sha", code: 1, want: "full 40-character sha",
			lock: func(f *bootFixture, o string) string { return o + "extra https://example.invalid/x abc123\n" }},
		{name: "lock plain http", code: 1, want: "url must start with https://",
			lock: func(f *bootFixture, o string) string {
				return o + "extra http://example.invalid/x " + strings.Repeat("a", 40) + "\n"
			}},
		{name: "lock duplicate", code: 1, want: "tessl is listed twice",
			lock: func(f *bootFixture, o string) string {
				return o + "tessl " + f.srcs["tessl"].url + " " + f.srcs["tessl"].shas[0] + "\n"
			}},
		{name: "lock without tessl", code: 1, want: "does not list tessl",
			lock: func(f *bootFixture, o string) string {
				var keep []string
				for _, l := range strings.Split(o, "\n") {
					if !strings.HasPrefix(l, "tessl ") {
						keep = append(keep, l)
					}
				}
				return strings.Join(keep, "\n")
			}},
		{name: "deps entry not a checkout", code: 1, want: "is not a git checkout",
			prep: func(f *bootFixture) {
				if err := os.MkdirAll(filepath.Join(f.deps, "DevCouncil"), 0o755); err != nil {
					f.t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.deps, "DevCouncil", "keep"), []byte("x"), 0o644); err != nil {
					f.t.Fatal(err)
				}
			}},
		{name: "modules do not resolve", env: []string{"FAKE_GO_LIST_FAIL=1"}, code: 1, want: "go list could not resolve"},
		// An old go.work that still uses an outside decide checkout wins over go.mod's ./decide replace.
		{name: "decide from outside the repo", env: []string{"FAKE_GO_DECIDE_DIR=/"}, code: 1,
			want: "decide resolves to /, not"},
		{name: "init fails", env: []string{"FAKE_VANI_INIT_RC=1", "FAKE_VANI_INIT_OUT=config: permission denied"}, code: 1,
			want: "vani init failed"},
		{name: "config check fails", env: []string{"FAKE_VANI_CHECK_RC=1"}, code: 1, want: "vani config check failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newBootFixture(t)
			if c.lock != nil {
				orig, _ := os.ReadFile(f.lock)
				f.writeLock(c.lock(f, string(orig)))
			}
			if c.prep != nil {
				c.prep(f)
			}
			args := c.args
			if args == nil {
				args = []string{"--deps", f.deps}
			}
			out, code := f.run(c.env, args...)
			if code != c.code || !strings.Contains(out, c.want) {
				t.Fatalf("want exit %d with %q, got exit %d:\n%s", c.code, c.want, code, out)
			}
			if c.name == "deps entry not a checkout" && !exists(filepath.Join(f.deps, "DevCouncil", "keep")) {
				t.Fatal("a directory that is not a checkout was changed")
			}
			if c.code == 2 && exists(f.deps) {
				t.Fatal("a refused argument still created the deps directory")
			}
		})
	}
}

func TestBootstrapOldGoWithAutoToolchainProceeds(t *testing.T) {
	f := newBootFixture(t)
	out, code := f.run([]string{"FAKE_GO_VERSION=1.26.9", "FAKE_GO_TOOLCHAIN=auto"}, "--deps", f.deps)
	if code != 0 || !strings.Contains(out, "Go downloads go1.27.4") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestBootstrapExistingConfigIsKept(t *testing.T) {
	f := newBootFixture(t)
	out, code := f.run([]string{"FAKE_VANI_INIT_RC=1", "FAKE_VANI_INIT_OUT=/x/config.json already exists; refusing to overwrite"},
		"--deps", f.deps)
	if code != 0 || !strings.Contains(out, "a config exists; left as it is") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestBootstrapNoBuildStopsAfterResolving(t *testing.T) {
	f := newBootFixture(t)
	out, code := f.run(nil, "--deps", f.deps, "--no-build")
	if code != 0 || !strings.Contains(out, "--no-build") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if exists(filepath.Join(f.root, "build")) {
		t.Fatal("--no-build still built")
	}
	if !exists(filepath.Join(f.root, "go.work")) {
		t.Fatal("--no-build should still write go.work")
	}
}
