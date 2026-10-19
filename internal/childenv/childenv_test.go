package childenv

import (
	"slices"
	"testing"
)

func fakeEnv(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

var full = map[string]string{
	"PATH": "/bin", "HOME": "/h", "TMPDIR": "/t",
	"SystemRoot": `C:\Windows`, "TEMP": `C:\T`, "TMP": `C:\T`, "USERPROFILE": `C:\Users\u`,
	"ANTHROPIC_API_KEY": "secret", "OPENROUTER_API_KEY": "secret",
}

func TestUnixPassesOnlyTheBaseKeys(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		got := minimal(goos, fakeEnv(full), nil)
		if want := []string{"PATH=/bin", "HOME=/h", "TMPDIR=/t"}; !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", goos, got, want)
		}
	}
}

// Without SystemRoot a Windows child cannot load Winsock or the crypto
// providers; the old per-package copies never passed it.
func TestWindowsAddsWhatAWindowsChildNeeds(t *testing.T) {
	got := minimal("windows", fakeEnv(full), nil)
	want := []string{"PATH=/bin", "HOME=/h", "TMPDIR=/t", `SystemRoot=C:\Windows`, `TEMP=C:\T`, `TMP=C:\T`, `USERPROFILE=C:\Users\u`}
	if !slices.Equal(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
}

func TestUnsetKeysAreSkippedAndExtrasAppended(t *testing.T) {
	got := minimal("darwin", fakeEnv(map[string]string{"PATH": "/bin", "HOME": ""}), []string{"X=1"})
	if want := []string{"PATH=/bin", "HOME=", "X=1"}; !slices.Equal(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
	if got := minimal("darwin", fakeEnv(nil), nil); len(got) != 0 {
		t.Errorf("empty environment gave %v", got)
	}
}

func TestSecretsNeverPass(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, e := range minimal(goos, fakeEnv(full), nil) {
			if e == "ANTHROPIC_API_KEY=secret" || e == "OPENROUTER_API_KEY=secret" {
				t.Errorf("%s: %s reached the child", goos, e)
			}
		}
	}
}
