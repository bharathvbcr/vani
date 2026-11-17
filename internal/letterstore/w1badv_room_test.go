package letterstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Room must not answer "room" where Save refuses before spending: a posting
// folder the user cannot write into fails Save at the first write.
func TestW1badvRoomAgreesWithSaveOnAReadOnlyPostingFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.Save(ctx, "gh:acme:1", "First letter.", "", nil); err != nil {
		t.Fatal(err)
	}
	dir := s.dir("gh:acme:1")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	rerr := s.Room(ctx, "gh:acme:1")
	_, serr := s.Save(ctx, "gh:acme:1", "Second letter.", "", nil)
	if serr == nil {
		t.Fatal("precondition: Save succeeded in a read-only folder")
	}
	if rerr == nil {
		t.Fatalf("Room said there is room, Save refused with %v (the model would be paid for a letter that cannot be kept)", serr)
	}
}

// A posting folder whose .lock is a directory: Save cannot take the per-posting
// lock, Room never tries.
func TestW1badvRoomAgreesWithSaveWhenThePostingLockIsADirectory(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if _, err := s.Save(ctx, "gh:acme:1", "First letter.", "", nil); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(s.dir("gh:acme:1"), ".lock")
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lock, 0o700); err != nil {
		t.Fatal(err)
	}
	rerr := s.Room(ctx, "gh:acme:1")
	_, serr := s.Save(ctx, "gh:acme:1", "Second letter.", "", nil)
	if serr == nil {
		t.Fatal("precondition: Save succeeded with a directory for a lock")
	}
	if rerr == nil {
		t.Fatalf("Room said there is room, Save refused with %v", serr)
	}
}
