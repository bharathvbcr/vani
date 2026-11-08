package letterstore

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/filelock"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func pdfBytes(fill byte) []byte {
	b := []byte("%PDF-1.4\n")
	return append(b, bytes.Repeat([]byte{fill}, 32)...)
}

func TestSaveAndUseOneLetterPerKey(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	meta, err := s.Save(ctx, "gh:acme:1", "I want this role at Acme.", "named", pdfBytes('A'))
	if err != nil || meta.Version != 1 || meta.PDFBytes == 0 {
		t.Fatalf("meta %+v err %v", meta, err)
	}
	if _, err := s.Used("gh:acme:1"); CodeOf(err) != CodeNotFound {
		t.Fatalf("build marked a letter used: %v", err)
	}
	mode := func(name string) os.FileMode {
		t.Helper()
		fi, err := os.Stat(filepath.Join(s.dir("gh:acme:1"), name))
		if err != nil {
			t.Fatal(err)
		}
		return fi.Mode().Perm()
	}
	if mode("v1.txt") != 0o600 || mode("v1.pdf") != 0o600 || mode("v1.json") != 0o600 {
		t.Fatalf("modes txt %v pdf %v json %v", mode("v1.txt"), mode("v1.pdf"), mode("v1.json"))
	}
	again, err := s.Save(ctx, "gh:acme:1", "I want this role at Acme.", "named", pdfBytes('A'))
	if err != nil || again.Version != 1 {
		t.Fatalf("duplicate wrote v%v err %v", again.Version, err)
	}
	second, err := s.Save(ctx, "gh:acme:1", "A different letter for Acme.", "named", pdfBytes('B'))
	if err != nil || second.Version != 2 {
		t.Fatalf("second %+v err %v", second, err)
	}
	used, err := s.Use(ctx, "gh:acme:1", 2)
	if err != nil || used.Version != 2 {
		t.Fatalf("use %+v err %v", used, err)
	}
	got, err := s.Used("gh:acme:1")
	if err != nil || got.Version != 2 {
		t.Fatalf("used %+v err %v", got, err)
	}
	text, err := s.Text("gh:acme:1", 2)
	if err != nil || text != "A different letter for Acme." {
		t.Fatalf("text %q err %v", text, err)
	}
	other, err := s.Text("gh:other:1", 1)
	if other != "" || CodeOf(err) != CodeNotFound {
		t.Fatalf("other key returned %q err %v", other, err)
	}
}

func TestSaveRefusesEmptyOversizedAndMalformed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Save(ctx, "", "text", "", pdfBytes('A')); CodeOf(err) != CodeBadRequest {
		t.Fatalf("empty key %v", err)
	}
	if _, err := s.Save(ctx, "gh:acme:1", "  ", "", pdfBytes('A')); CodeOf(err) != CodeBadRequest {
		t.Fatalf("empty body %v", err)
	}
	if _, err := s.Save(ctx, "gh:acme:1", strings.Repeat("a", MaxTextBytes+1), "", nil); CodeOf(err) != CodeTooLarge {
		t.Fatalf("oversize text %v", err)
	}
	big := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{'x'}, MaxPDF)...)
	if _, err := s.Save(ctx, "gh:acme:1", "kept", "", big); CodeOf(err) != CodeTooLarge {
		t.Fatalf("oversize pdf %v", err)
	}
	if _, err := s.Save(ctx, "gh:acme:1", "kept", "", []byte("not a pdf")); CodeOf(err) != CodeBadRequest {
		t.Fatalf("malformed pdf %v", err)
	}
	if _, err := s.Used("gh:acme:1"); CodeOf(err) != CodeNotFound {
		t.Fatalf("a refusal left a letter: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Save(ctx, "gh:acme:1", "kept", "", pdfBytes('A')); CodeOf(err) != CodeTimeout {
		t.Fatalf("cancelled save %v", err)
	}
}

func TestAHeldLockTimesOutAndWritesNothing(t *testing.T) {
	s := testStore(t)
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		t.Fatal(err)
	}
	lk, err := filelock.Acquire(context.Background(), filepath.Join(s.root, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := s.Save(ctx, "gh:acme:1", "kept", "", pdfBytes('A')); CodeOf(err) != CodeTimeout {
		t.Fatalf("busy save %v", err)
	}
	if _, err := s.Text("gh:acme:1", 1); CodeOf(err) != CodeNotFound && CodeOf(err) != CodeDamaged {
		t.Fatalf("a timed-out save left a readable letter: %v", err)
	}
}

func TestAMismatchedKeyIsNotServed(t *testing.T) {
	s := testStore(t)
	if _, err := s.Save(context.Background(), "gh:acme:1", "I want this role at Acme.", "named", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir("gh:acme:1"), "key"), []byte("gh:other:9"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Text("gh:acme:1", 1); CodeOf(err) != CodeDamaged {
		t.Fatalf("mismatched key %v", err)
	}
}
