package rankfit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Update's doc says "a cancelled ctx ... writes nothing". A ctx that is
// already cancelled while the lock is free still gets the lock (TryLock runs
// before the ctx is looked at) and writes.
func TestW1cAdvUpdateCancelledBeforeStartWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rank", "w.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ran := false
	err := Update(ctx, path, func(cur *File, exists bool) error {
		ran = true
		*cur = goodFile()
		return nil
	})
	if _, serr := os.Stat(path); serr == nil {
		t.Errorf("a cancelled ctx wrote the file (Update err %v, fn ran %v)", err, ran)
	}
}

// The same promise when the ctx is cancelled while fn runs: the write after fn
// must not happen.
func TestW1cAdvUpdateCancelledInsideFnWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "w.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := Update(ctx, path, func(cur *File, exists bool) error {
		*cur = goodFile()
		cancel()
		return nil
	})
	if _, serr := os.Stat(path); serr == nil {
		t.Errorf("a ctx cancelled during fn still wrote the file (Update err %v)", err)
	}
}

// An Update whose fn refuses leaves no directory behind where there was none.
func TestW1cAdvUpdateRefusedLeavesNoDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "rank")
	_ = Update(context.Background(), filepath.Join(dir, "w.json"), func(cur *File, exists bool) error {
		return os.ErrInvalid
	})
	if _, err := os.Stat(dir); err == nil {
		ents, _ := os.ReadDir(dir)
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("a refused Update created %s holding %v", dir, names)
	}
}

// A leaf symlink spelled two ways is two lock files: a writer holding the lock
// through the link does not exclude a writer who names the target.
func TestW1cAdvUpdateLinkAndTargetShareALock(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real.json")
	if err := Write(target, goodFile()); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	in, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- Update(context.Background(), link, func(cur *File, exists bool) error {
			close(in)
			<-release
			return os.ErrInvalid
		})
	}()
	<-in
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := Update(ctx, target, func(cur *File, exists bool) error { return os.ErrInvalid })
	close(release)
	<-done
	if err != os.ErrInvalid && err == nil {
		t.Fatal("unexpected nil")
	}
	if err == os.ErrInvalid {
		t.Errorf("the target's writer got in while the link's writer held the lock: two spellings, two locks")
	}
}

// A lock path that is a directory is an error, not a hang, and the weights
// file is untouched.
func TestW1cAdvUpdateLockPathIsADirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "w.json")
	if err := Write(path, goodFile()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".lock", 0o700); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	done := make(chan error, 1)
	go func() {
		done <- Update(context.Background(), path, func(cur *File, exists bool) error { return nil })
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("Update succeeded with a directory in the lock's place")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Update hung on a directory lock path")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Error("the file changed")
	}
}
