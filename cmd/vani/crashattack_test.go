package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

type badString struct{}

func (badString) String() string { panic("inner String panic") }

type nilErr struct{ msg string }

func (n *nilErr) Error() string { return n.msg }

// A panic inside a deferred function of the command replaces the first one;
// the seam still recovers it and exits exitCrash.
func TestAttackDeferredPanicStillCrash(t *testing.T) {
	var o, e bytes.Buffer
	en := &env{stdout: &o, stderr: &e}
	en.dispatchFn = func([]string) error {
		defer func() { panic("second") }()
		panic("first")
	}
	if code := runEnv(en, []string{"draft"}); code != exitCrash {
		t.Fatalf("exit %d, want %d", code, exitCrash)
	}
	if !strings.HasPrefix(lastLine(e.String()), "vani: crashed: ") {
		t.Errorf("stderr %q", e.String())
	}
}

// Hostile panic values: control characters, ANSI escapes, NUL, invalid UTF-8,
// U+2028, a value whose String method panics, a typed nil error.
func TestAttackHostilePanicValues(t *testing.T) {
	for name, v := range map[string]any{
		"ansi":        "a\x1b[31mred\x1b[0m",
		"nul":         "a\x00b",
		"invalid":     "\xff\xfe\xfd bad",
		"separator":   "a\u2028b\u2029c",
		"crlf":        "a\r\nb\rc",
		"stringpanic": badString{},
		"typednil":    (*nilErr)(nil),
		"bidi":        "\u202eevil",
		"int":         42,
		"struct":      struct{ A, B string }{"x", "y"},
	} {
		t.Run(name, func(t *testing.T) {
			var o, e bytes.Buffer
			if code := runEnv(panicEnv(&o, &e, "", v), []string{"draft"}); code != exitCrash {
				t.Fatalf("exit %d, want %d", code, exitCrash)
			}
			last := lastLine(e.String())
			if !strings.HasPrefix(last, "vani: crashed: ") {
				t.Fatalf("last line %q", last)
			}
			if !utf8.ValidString(last) || strings.ContainsAny(last, "\x1b\x00\r") {
				t.Errorf("last stderr line is not clean: %q", last)
			}
			var r errorReply
			if err := json.Unmarshal(o.Bytes(), &r); err != nil {
				t.Fatalf("stdout %q not an envelope: %v", o.String(), err)
			}
			if r.Code != codeCrash {
				t.Errorf("code %q", r.Code)
			}
		})
	}
}

// A partial reply with no trailing newline, then a panic: stdout is exactly the
// partial bytes (no second value glued on), exit is exitCrash.
func TestAttackPartialReplyNoNewline(t *testing.T) {
	var o, e bytes.Buffer
	if code := runEnv(panicEnv(&o, &e, "{\"a\":", "x"), []string{"--json", "status"}); code != exitCrash {
		t.Fatalf("exit %d", code)
	}
	if o.String() != "{\"a\":" {
		t.Errorf("stdout %q", o.String())
	}
}

// A stack on stderr stays bounded for deep recursion.
func TestAttackDeepStackBounded(t *testing.T) {
	var rec func(n int)
	rec = func(n int) {
		if n == 0 {
			panic("deep")
		}
		rec(n - 1)
	}
	var o, e bytes.Buffer
	en := &env{stdout: &o, stderr: &e}
	en.dispatchFn = func([]string) error { rec(50000); return nil }
	if code := runEnv(en, []string{"status"}); code != exitCrash {
		t.Fatalf("exit %d", code)
	}
	if e.Len() > 16<<10 {
		t.Errorf("stderr %d bytes", e.Len())
	}
}

// A panic whose command also returned usage/restart-like errors via panic(err).
func TestAttackPanicWithTypedErrorsStaysCrash(t *testing.T) {
	for name, v := range map[string]any{
		"usage":   usagef("bad"),
		"restart": errConfigRestart,
		"relpath": errNoJSON(jsonLeaf{Command: "x"}),
	} {
		var o, e bytes.Buffer
		if code := runEnv(panicEnv(&o, &e, "", v), []string{"daemon"}); code != exitCrash {
			t.Errorf("%s: exit %d, want %d", name, code, exitCrash)
		}
	}
}

func TestAttackWantsJSONReply(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want bool
	}{
		{[]string{"draft"}, true},
		{[]string{"status"}, false},
		{[]string{"status", "--json"}, true},
		{[]string{"status", "-json"}, true},
		{[]string{"status", "--json=1"}, true},
		{[]string{"status", "--json=TRUE"}, true},
		{[]string{"status", "--json=0"}, false},
		{[]string{"status", "--json=false"}, false},
		{[]string{"status", "--json="}, false},
		{[]string{"--json", "status"}, true},
		{[]string{"--json=false", "status"}, false},
		{[]string{"--json"}, true},
		{nil, false},
		{[]string{""}, false},
		{[]string{"status", "--json=false", "--json"}, true},
		{[]string{"STATUS", "--json"}, false},
		{[]string{"bogus", "--json"}, false},
	} {
		if got := wantsJSONReply(tc.args); got != tc.want {
			t.Errorf("wantsJSONReply(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

// ft-152c boundary values for the path env vars.
func TestAttackRelativeEnvBoundaries(t *testing.T) {
	abs := t.TempDir()
	for _, v := range []string{"~", "~/x.json", "C:\\x\\c.json", "\\\\srv\\share\\c", " /abs/with/leading/space", "./", "..", "a/../b", "x\ny"} {
		for _, name := range []string{"VANI_CONFIG", "VANI_STATE_DIR"} {
			t.Run(name+"="+v, func(t *testing.T) {
				t.Setenv("VANI_CONFIG", filepath.Join(abs, "c.json"))
				t.Setenv("VANI_STATE_DIR", filepath.Join(abs, "s"))
				t.Setenv(name, v)
				code, o, e := do(t, "status", "--json")
				if code != exitUsage {
					t.Fatalf("exit %d stdout %q stderr %q", code, o, e)
				}
				var r errorReply
				if err := json.Unmarshal([]byte(o), &r); err != nil || r.Code != codeRelativePath {
					t.Fatalf("stdout %q (%v)", o, err)
				}
				if strings.Count(o, "\n") != 1 {
					t.Errorf("not one reply: %q", o)
				}
			})
		}
	}
}

// An empty variable is unset, not relative.
func TestAttackEmptyEnvIsUnset(t *testing.T) {
	abs := t.TempDir()
	t.Setenv("VANI_STATE_DIR", filepath.Join(abs, "s"))
	t.Setenv("VANI_CONFIG", "")
	code, o, e := do(t, "help")
	if code != exitOK {
		t.Fatalf("exit %d stdout %q stderr %q", code, o, e)
	}
}

// A usage error under a command's own --json still writes nothing on stdout.
func TestAttackUsageUnderOwnJSONStaysSilent(t *testing.T) {
	code, o, _ := do(t, "status", "--json", "--nosuchflag")
	if code != exitUsage || o != "" {
		t.Fatalf("exit %d stdout %q", code, o)
	}
}
