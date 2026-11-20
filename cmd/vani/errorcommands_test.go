package main

import (
	"regexp"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/eval"
)

var backtickCommand = regexp.MustCompile("`(vani [^`]+)`")

// An error that tells the owner what to run must name a command vani accepts:
// each one is pulled out of the message and run (a trailing N becomes a number).
// An unknown command is a usage error, exit 2.
func TestErrorsNameCommandsThatRun(t *testing.T) {
	psSetup(t, 2, 2, nil)
	for name, err := range map[string]error{"ErrNoPairCap": eval.ErrNoPairCap, "ErrNotFrozen": eval.ErrNotFrozen} {
		m := backtickCommand.FindStringSubmatch(err.Error())
		if m == nil {
			t.Fatalf("%s names no command: %q", name, err)
		}
		args := strings.Fields(m[1])[1:]
		for i, a := range args {
			if a == "N" {
				args[i] = "1"
			}
		}
		if code, _, stderr := do(t, args...); code == 2 {
			t.Errorf("%s names `vani %s`, which is a usage error: %s", name, strings.Join(args, " "), stderr)
		}
	}
}
