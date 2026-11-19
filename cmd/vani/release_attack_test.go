package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Attack tests for the release.sh provenance fix (ft-0ca0 #91). Same scratch harness as release_script_test.go.

const attackBundle = `#!/bin/bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
APP="$ROOT/macos/build/Vani Apply.app"
mkdir -p "$APP/Contents/Helpers"
printf '#!/bin/sh\necho "vani %s"\n' "$VANI_VERSION" > "$APP/Contents/Helpers/vani"
chmod +x "$APP/Contents/Helpers/vani"
if [ -n "${FAKE_INFO_RAW+x}" ]; then printf "$FAKE_INFO_RAW" > "$VANI_BUILD_INFO_OUT"; fi
case "${FAKE_MUTATE:-}" in
  untracked) echo 'package x' > "$ROOT/injected.go" ;;
  gowork) echo '// edited' >> "$ROOT/go.work" ;;
  dirty) echo changed >> "$ROOT/README.md" ;;
  origintag) git -C "$ROOT" push -q origin "HEAD:refs/tags/$VANI_VERSION" ;;
  localtag) git -C "$ROOT" tag "$VANI_VERSION" ;;
esac
`

func (r *releaseRepo) setBundle(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.root, "macos/scripts/bundle.sh"), []byte(attackBundle), 0o755); err != nil {
		t.Fatal(err)
	}
	r.git(t, r.root, "commit", "-q", "-am", "attack bundle")
	r.git(t, r.root, "push", "-q", "origin", "main")
}

func TestAttackTagAndBranchRefusals(t *testing.T) {
	v := "v1.2.3"
	t.Run("local tag exists", func(t *testing.T) {
		r := newReleaseRepo(t, v)
		r.git(t, r.root, "tag", v)
		out, err := r.run(t, nil, v, "--skip-tests")
		if err == nil || !strings.Contains(out, "tag-exists-locally") || strings.Contains(out, "built ") {
			t.Fatalf("err=%v\n%s", err, out)
		}
	})
	t.Run("origin tag exists", func(t *testing.T) {
		r := newReleaseRepo(t, v)
		r.git(t, r.root, "push", "-q", "origin", "HEAD:refs/tags/"+v)
		out, err := r.run(t, nil, v, "--skip-tests")
		if err == nil || !strings.Contains(out, "tag-exists-on-origin") || strings.Contains(out, "built ") {
			t.Fatalf("err=%v\n%s", err, out)
		}
		if strings.Contains(r.ghCalls(t), "release create") {
			t.Fatal("release created")
		}
	})
	t.Run("detached head", func(t *testing.T) {
		r := newReleaseRepo(t, v)
		r.git(t, r.root, "checkout", "-q", "--detach")
		out, err := r.run(t, nil, v, "--skip-tests")
		if err == nil || !strings.Contains(out, "not-on-main") {
			t.Fatalf("err=%v\n%s", err, out)
		}
		r.noTagAnywhere(t, v)
	})
	t.Run("tag appears on origin during the build", func(t *testing.T) {
		r := newReleaseRepo(t, v)
		r.setBundle(t)
		out, err := r.run(t, []string{"FAKE_INFO_RAW=tessl=1\n", "FAKE_MUTATE=origintag"}, v, "--skip-tests")
		if err == nil {
			t.Fatalf("expected the push to fail:\n%s", out)
		}
		if strings.Contains(r.ghCalls(t), "release create") {
			t.Fatalf("release created after a failed tag push:\n%s", out)
		}
	})
	t.Run("local tag appears during the build", func(t *testing.T) {
		r := newReleaseRepo(t, v)
		r.setBundle(t)
		out, err := r.run(t, []string{"FAKE_INFO_RAW=tessl=1\n", "FAKE_MUTATE=localtag"}, v, "--skip-tests")
		if err == nil || strings.Contains(r.ghCalls(t), "release create") {
			t.Fatalf("err=%v\n%s\n%s", err, out, r.ghCalls(t))
		}
	})
}

func TestAttackBuildInfoVariants(t *testing.T) {
	v := "v1.2.3"
	refused := map[string]string{
		"crlf":          "tessl=1\\r\\n",
		"leading space": " tessl=1\\n",
		"empty":         "",
		"zero then one": "tessl=0\\ntessl=1\\n",
		"uppercase":     "TESSL=1\\n",
		"two digits":    "tessl=11\\n",
		"no key":        "1\\n",
		"unknown":       "tessl=unknown\\n",
	}
	for name, raw := range refused {
		t.Run("refused/"+name, func(t *testing.T) {
			r := newReleaseRepo(t, v)
			r.setBundle(t)
			out, err := r.run(t, []string{"FAKE_INFO_RAW=" + raw}, v, "--skip-tests")
			if err == nil || !strings.Contains(out, "no-tessl") {
				t.Fatalf("want no-tessl refusal, err=%v\n%s", err, out)
			}
			r.noTagAnywhere(t, v)
		})
	}
	t.Run("stale info in env and dist is not read", func(t *testing.T) {
		r := newReleaseRepo(t, v)
		r.setBundle(t)
		stale := filepath.Join(r.root, "dist")
		if err := os.MkdirAll(stale, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stale, ".build-info"), []byte("tessl=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		pre := filepath.Join(filepath.Dir(r.root), "pre-info")
		if err := os.WriteFile(pre, []byte("tessl=1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		// the fake bundle writes no info at all: only a stale file could make this pass
		out, err := r.run(t, []string{"VANI_BUILD_INFO_OUT=" + pre}, v, "--skip-tests")
		if err == nil || !strings.Contains(out, "no-tessl") {
			t.Fatalf("stale tessl=1 was believed, err=%v\n%s", err, out)
		}
	})
}

func TestAttackAllowNoTesslNotesOnceAndDryRun(t *testing.T) {
	v := "v1.2.3"
	for _, extra := range [][]string{{"--allow-no-tessl"}, {"--allow-no-tessl", "--dry-run"}} {
		r := newReleaseRepo(t, v)
		r.setBundle(t)
		out, err := r.run(t, []string{"FAKE_INFO_RAW=tessl=0\\n"}, append([]string{v, "--skip-tests"}, extra...)...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", extra, err, out)
		}
		notes, err := os.ReadFile(filepath.Join(r.root, "dist/.release-notes.md"))
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(notes), "WITHOUT the Tessl"); n != 1 {
			t.Fatalf("%v: wording present %d times:\n%s", extra, n, notes)
		}
		if len(extra) == 2 {
			r.noTagAnywhere(t, v)
		}
	}
}
