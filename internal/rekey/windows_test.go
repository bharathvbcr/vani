package rekey

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The Windows refusal comes before the ledger is opened, the backup written or
// the pending marker created: nothing in the state directory changes. goos is
// swapped so every host exercises the branch.
func TestRunRefusesOnWindowsBeforeTouchingAnything(t *testing.T) {
	for _, dry := range []bool{false, true} {
		state := t.TempDir()
		ldir := filepath.Join(state, "ledger")
		if err := os.MkdirAll(ldir, 0o700); err != nil {
			t.Fatal(err)
		}
		events := filepath.Join(ldir, eventsName)
		if err := os.WriteFile(events, []byte(`{"not":"opened"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := goos
		goos = "windows"
		defer func() { goos = old }()

		if _, err := Run(state, dry, time.Now()); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("dry=%v: err %v, want ErrUnsupported", dry, err)
		}
		ents, err := os.ReadDir(ldir)
		if err != nil {
			t.Fatal(err)
		}
		// Only the events file: no lock (the ledger was not opened), no backup,
		// no marker.
		if len(ents) != 1 || ents[0].Name() != eventsName {
			t.Fatalf("dry=%v: ledger dir holds %v", dry, ents)
		}
		if b, _ := os.ReadFile(events); string(b) != `{"not":"opened"}`+"\n" {
			t.Fatalf("events changed: %q", b)
		}
	}
}
