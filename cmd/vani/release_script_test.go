package main

import (
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
