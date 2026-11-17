package letterstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func w1bSaved(t *testing.T) (*Store, string) {
	t.Helper()
	s := testStore(t)
	if _, err := s.Save(context.Background(), "gh:acme:1", "First letter.", "", nil); err != nil {
		t.Fatal(err)
	}
	return s, s.dir("gh:acme:1")
}

// A pointer or meta file over its bound is refused unread past the bound.
func TestW1bOversizedPointerAndMetaAreDamaged(t *testing.T) {
	s, dir := w1bSaved(t)
	if err := os.WriteFile(filepath.Join(dir, "current"), []byte(strings.Repeat("1", maxPointer+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Current("gh:acme:1"); CodeOf(err) != CodeDamaged {
		t.Fatalf("oversized current: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "current"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "v1.json"), []byte(strings.Repeat(" ", maxMeta+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Current("gh:acme:1"); CodeOf(err) != CodeDamaged {
		t.Fatalf("oversized meta: %v", err)
	}
}

// At the bound exactly, a file still reads.
func TestW1bReadBoundedAtTheBound(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte("abcd"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := readBounded(p, 4); err != nil || string(b) != "abcd" {
		t.Fatalf("at the bound: %q %v", b, err)
	}
	if _, err := readBounded(p, 3); CodeOf(err) != CodeDamaged {
		t.Fatalf("one over: %v", err)
	}
	if _, err := readBounded(p+"x", 3); !os.IsNotExist(err) {
		t.Fatalf("missing: %v", err)
	}
}

// Room leaves no temp file behind and agrees with Save on a new posting in a
// read-only store root.
func TestW1bRoomWritesNothingAndSeesAReadOnlyRoot(t *testing.T) {
	s, dir := w1bSaved(t)
	before, _ := os.ReadDir(dir)
	if err := s.Room(context.Background(), "gh:acme:1"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadDir(dir)
	if len(before) != len(after) {
		t.Fatalf("Room left files: %d before, %d after", len(before), len(after))
	}
	if os.Geteuid() == 0 {
		return
	}
	if err := os.Chmod(s.root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.root, 0o700) })
	rerr := s.Room(context.Background(), "gh:acme:2")
	_, serr := s.Save(context.Background(), "gh:acme:2", "A letter.", "", nil)
	if serr == nil || rerr == nil || CodeOf(rerr) != CodeOf(serr) {
		t.Fatalf("room %v, save %v", rerr, serr)
	}
}
