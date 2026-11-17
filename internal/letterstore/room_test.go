package letterstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRoomAnswersLikeSaveAtEachBound(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	if err := s.Room(ctx, "gh:acme:1"); err != nil {
		t.Fatalf("an empty store has room: %v", err)
	}
	if _, err := os.Lstat(s.root); !os.IsNotExist(err) {
		t.Fatalf("Room created the store: %v", err)
	}
	for i := 1; i <= MaxVersions; i++ {
		if err := s.Room(ctx, "gh:acme:1"); err != nil {
			t.Fatalf("v%d: room refused with %d saved: %v", i, i-1, err)
		}
		if _, err := s.Save(ctx, "gh:acme:1", fmt.Sprintf("Letter %d.", i), "", nil); err != nil {
			t.Fatal(err)
		}
	}
	err := s.Room(ctx, "gh:acme:1")
	_, serr := s.Save(ctx, "gh:acme:1", "One letter too many.", "", nil)
	if CodeOf(err) != CodeFull || CodeOf(serr) != CodeFull || err.Error() != serr.Error() {
		t.Fatalf("room %v, save %v", err, serr)
	}
	if err := s.Room(ctx, "gh:acme:2"); err != nil {
		t.Fatalf("another posting still has room: %v", err)
	}
}

func TestRoomRefusesANewPostingWhenTheStoreHoldsMaxJobs(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	for i := 0; i < MaxJobs-1; i++ {
		if err := os.MkdirAll(filepath.Join(s.root, fmt.Sprintf("%064x", i)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Room(ctx, "gh:new:1"); err != nil {
		t.Fatalf("one below the cap: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(s.root, fmt.Sprintf("%064x", MaxJobs)), 0o700); err != nil {
		t.Fatal(err)
	}
	err := s.Room(ctx, "gh:new:1")
	_, serr := s.Save(ctx, "gh:new:1", "A letter.", "", nil)
	if CodeOf(err) != CodeFull || CodeOf(serr) != CodeFull || err.Error() != serr.Error() {
		t.Fatalf("room %v, save %v", err, serr)
	}
	// A posting that already has a folder is not a new one.
	existing := s.dir("gh:old:1")
	if err := os.MkdirAll(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Room(ctx, "gh:old:1"); err != nil {
		t.Fatalf("an existing posting is not refused by the job cap: %v", err)
	}
}

func TestRoomRejectsBadKeysAndACancelledContext(t *testing.T) {
	s := testStore(t)
	if CodeOf(s.Room(context.Background(), "")) != CodeBadRequest {
		t.Fatal("an empty key was accepted")
	}
	if _, err := s.Save(context.Background(), "gh:acme:1", "A letter.", "", nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.Room(ctx, "gh:acme:1"); err == nil {
		t.Fatal("a cancelled context was accepted")
	}
}
