package main

import (
	"bytes"
	"testing"
)

// An env built without config.Resolve (tests, library use, jobs serve clones)
// must not open the resume store on a relative state dir: the PDF paths a
// reply names and the build folders are absolute.
func TestW5aadvResumeStoreRefusesRelativeStateDir(t *testing.T) {
	for _, dir := range []string{"state", "./state", "~/state", "../s"} {
		e := &env{stateDir: dir, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
		if _, serr := e.resumeStore(); serr == nil {
			t.Errorf("resumeStore accepted relative state dir %q", dir)
		}
	}
}
