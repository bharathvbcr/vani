package eval

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/bharathvbcr/Vani/internal/atomicfile/synctest"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Every label, teacher row and pair row is made durable by one file sync of
// its own (TestMain skips them; this counts them, so deleting one fails here).
func TestEachStoreAppendSyncsTheFileOnce(t *testing.T) {
	root := t.TempDir()
	log := synctest.Record(t, root)

	labels, err := NewStore(filepath.Join(root, "labels"), filepath.Join(root, "text"))
	if err != nil {
		t.Fatal(err)
	}
	log.Reset()
	for i := range 3 {
		if err := labels.Put(Entry{Key: posting.Key(fmt.Sprintf("gh:a:%d", i)), Labels: map[string]string{"work_mode": "remote"}}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(log.Files()); n != 3 {
		t.Fatalf("3 labels: %d file syncs, want 3", n)
	}

	teacher, err := OpenTeacherStore(filepath.Join(root, TeacherFile))
	if err != nil {
		t.Fatal(err)
	}
	log.Reset()
	ans := map[string]TeacherSlot{"role_fit": {Reading: "answered", Value: "yes"}}
	for i := range 2 {
		if err := teacher.Append(currentRow(fmt.Sprintf("gh:t:%d", i), "haiku", FixtureSHA(), PromptHash("s"), ans)); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(log.Files()); n != 2 {
		t.Fatalf("2 teacher rows: %d file syncs, want 2", n)
	}

	pairs, err := OpenPairStore(filepath.Join(root, PairsFile))
	if err != nil {
		t.Fatal(err)
	}
	log.Reset()
	if err := pairs.Append(pairRowOf("k:1", "k:2", "a", pairT0)); err != nil {
		t.Fatal(err)
	}
	if n := len(log.Files()); n != 1 {
		t.Fatalf("1 pair row: %d file syncs, want 1", n)
	}
}

// The real fsync path once per store, end to end: reopened, each holds what
// was written.
func TestEachStoreRealFsyncRoundTrip(t *testing.T) {
	root := t.TempDir()
	synctest.Real(t, root)
	labels, err := NewStore(filepath.Join(root, "labels"), filepath.Join(root, "text"))
	if err != nil {
		t.Fatal(err)
	}
	if err := labels.Put(Entry{Key: "gh:a:1", Labels: map[string]string{"work_mode": "remote"}}); err != nil {
		t.Fatal(err)
	}
	if got, _, err := LabelsAt(filepath.Join(root, "labels")); err != nil || len(got) != 1 {
		t.Fatalf("labels reopened: %d, %v", len(got), err)
	}
	teacherPath := filepath.Join(root, TeacherFile)
	teacher, err := OpenTeacherStore(teacherPath)
	if err != nil {
		t.Fatal(err)
	}
	ans := map[string]TeacherSlot{"role_fit": {Reading: "answered", Value: "yes"}}
	if err := teacher.Append(currentRow("gh:t:1", "haiku", FixtureSHA(), PromptHash("s"), ans)); err != nil {
		t.Fatal(err)
	}
	if again, err := OpenTeacherStore(teacherPath); err != nil || again.Len() != 1 {
		t.Fatalf("teacher reopened: %v", err)
	}
	pairsPath := filepath.Join(root, PairsFile)
	pairs, err := OpenPairStore(pairsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := pairs.Append(pairRowOf("k:1", "k:2", "a", pairT0)); err != nil {
		t.Fatal(err)
	}
	if again, err := OpenPairStore(pairsPath); err != nil || again.Len() != 1 {
		t.Fatalf("pairs reopened: %v", err)
	}
}
