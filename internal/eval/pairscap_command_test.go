package eval

import (
	"strings"
	"testing"
)

// The errors that tell the owner what to run name commands that exist: the
// auditor's cap and exemplar set are under `vani teacher`, never `vani silver`
// (cmd/vani's TestErrorsNameCommandsThatRun runs what they name).
func TestAuditorErrorsNameTheTeacherCommands(t *testing.T) {
	for name, err := range map[string]error{"ErrNoPairCap": ErrNoPairCap, "ErrNotFrozen": ErrNotFrozen} {
		msg := err.Error()
		if strings.Contains(msg, "silver") {
			t.Errorf("%s names a command that does not exist: %q", name, msg)
		}
		if !strings.Contains(msg, "`vani teacher ") {
			t.Errorf("%s does not name a vani teacher command: %q", name, msg)
		}
	}
}
