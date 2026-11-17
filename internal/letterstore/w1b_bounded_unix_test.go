//go:build unix

package letterstore

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A non-regular file in place of current is damaged, for Save and for Room.
func TestW1bNonRegularCurrentIsDamaged(t *testing.T) {
	for name, mk := range map[string]func(p string) error{
		"fifo":    func(p string) error { return syscall.Mkfifo(p, 0o600) },
		"symlink": func(p string) error { return os.Symlink("v1.json", p) },
		"dir":     func(p string) error { return os.Mkdir(p, 0o700) },
	} {
		t.Run(name, func(t *testing.T) {
			s, dir := w1bSaved(t)
			cur := filepath.Join(dir, "current")
			if err := os.Remove(cur); err != nil {
				t.Fatal(err)
			}
			if err := mk(cur); err != nil {
				t.Skipf("cannot make %s: %v", name, err)
			}
			if _, err := os.Lstat(cur); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := s.Save(context.Background(), "gh:acme:1", "Second letter.", "", nil)
				done <- err
			}()
			select {
			case err := <-done:
				if got := CodeOf(err); got != CodeDamaged {
					t.Fatalf("Save: code %q (%v), want %q", got, err, CodeDamaged)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Save did not return")
			}
			if got := CodeOf(s.Room(context.Background(), "gh:acme:1")); got != CodeDamaged {
				t.Fatalf("Room: code %q, want %q", got, CodeDamaged)
			}
		})
	}
}
