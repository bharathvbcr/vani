package resumestore

import (
	"errors"
	"strings"
	"testing"
)

// Open refuses an empty or relative state directory: the PDF paths a reply
// names and the folders builds work in must be absolute.
func TestW5aOpenRefusesNonAbsoluteStateDir(t *testing.T) {
	for _, dir := range []string{"", "state", "./state", "~/state", "../s", "."} {
		s, err := Open(dir)
		if err == nil || s != nil {
			t.Errorf("Open(%q) = %v, %v; want an error and no store", dir, s, err)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("Open(%q) err = %v, want ErrInvalid", dir, err)
		}
		if dir != "" && !strings.Contains(err.Error(), "absolute") {
			t.Errorf("Open(%q) err = %q, want it to say the directory must be absolute", dir, err)
		}
	}
	if _, err := Open(t.TempDir()); err != nil {
		t.Errorf("Open(absolute) = %v, want nil", err)
	}
}
