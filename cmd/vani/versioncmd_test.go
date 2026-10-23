package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionCommand(t *testing.T) {
	old := version
	t.Cleanup(func() { version = old })
	version = "v9.8.7"

	for _, arg := range []string{"version", "--version"} {
		var out, errb bytes.Buffer
		e := &env{stdout: &out, stderr: &errb}
		if code := runEnv(e, []string{arg}); code != exitOK {
			t.Fatalf("%s: exit %d, stderr %q", arg, code, errb.String())
		}
		if got := out.String(); got != "vani v9.8.7\n" {
			t.Fatalf("%s: stdout %q", arg, got)
		}
	}

	var out, errb bytes.Buffer
	e := &env{stdout: &out, stderr: &errb}
	if code := runEnv(e, []string{"version", "extra"}); code != exitUsage {
		t.Fatalf("extra arg: exit %d", code)
	}
	if !strings.Contains(errb.String(), "version takes no arguments") {
		t.Fatalf("stderr %q", errb.String())
	}
}
