package marks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// A failed directory sync on creation removes the new empty file. Under
// concurrent writers no ACKNOWLEDGED mark may be lost to that removal: the
// store's lock must keep every other writer out of the create..remove window.
func TestFailedCreateSyncNeverLosesAnAcknowledgedMark(t *testing.T) {
	for round := range 20 {
		dir := Dir(t.TempDir())
		var calls atomic.Int32
		old := syncDir
		syncDir = func(string) error {
			if calls.Add(1) <= 2 { // the first two creations fail
				return errors.New("injected dir sync failure")
			}
			return nil
		}
		const n = 16
		var mu sync.Mutex
		acked := map[posting.Key]bool{}
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				k := posting.Key(fmt.Sprintf("ab:co:%08d", i))
				if _, err := SetMark(dir, k, Saved, t0); err == nil {
					mu.Lock()
					acked[k] = true
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		syncDir = old
		m, err := ReadMarks(dir)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		for k := range acked {
			if _, ok := m[k]; !ok {
				t.Fatalf("round %d: %s was acknowledged and is gone", round, k)
			}
		}
		if len(acked) < n-2 {
			t.Fatalf("round %d: only %d of %d writes succeeded; at most 2 may fail", round, len(acked), n)
		}
	}
}

func TestFailedCreateSyncLeavesNoFileAndNextWriteRetriesTheSync(t *testing.T) {
	dir := Dir(t.TempDir())
	old := syncDir
	t.Cleanup(func() { syncDir = old })
	syncDir = func(string) error { return errors.New("boom") }
	if _, err := SetMark(dir, k1, Saved, t0); err == nil {
		t.Fatal("want the sync failure")
	}
	if _, err := os.Stat(filepath.Join(dir, fileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new file must be removed: %v", err)
	}
	var calls int
	syncDir = func(string) error { calls++; return nil }
	if _, err := SetMark(dir, k1, Saved, t0); err != nil || calls != 1 {
		t.Fatalf("retry: err=%v syncs=%d, want a fresh sync", err, calls)
	}
}

// padded returns one valid note line for key, space-padded (JSON ignores
// trailing whitespace) so the line, newline included, is exactly total bytes.
func padded(t *testing.T, key posting.Key, note string, total int) string {
	t.Helper()
	base := fmt.Sprintf(`{"v":1,"at":"2026-10-01T00:00:00Z","key":%q,"note":%q}`, string(key), note)
	base = strings.ReplaceAll(base, "<", `<`)
	if len(base)+1 > total {
		t.Fatalf("base line is %d bytes, over %d", len(base)+1, total)
	}
	return base + strings.Repeat(" ", total-len(base)-1) + "\n"
}

func TestReplayLineCapBoundaries(t *testing.T) {
	long := strings.Repeat("<", 2000)
	for _, tc := range []struct {
		name    string
		total   int
		trailer string
		ok      bool
		torn    bool
	}{
		{"exactly cap", maxLineBytes, "", true, false},
		{"cap+1 mid-file", maxLineBytes + 1, `{"v":1,"at":"2026-10-01T00:00:00Z","key":"gh:a:2","note":"x"}` + "\n", false, false},
		{"cap+1 last line", maxLineBytes + 1, "", false, false},
		{"cap-1", maxLineBytes - 1, "", true, false},
		{"legacy 9 KiB line", 9 << 10, "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line := padded(t, "gh:a:1", long, tc.total)
			if len(line) != tc.total {
				t.Fatalf("built %d bytes, want %d", len(line), tc.total)
			}
			r, err := replay(strings.NewReader(line + tc.trailer))
			if tc.ok {
				if err != nil || r.good != int64(tc.total) || r.m["gh:a:1"].Note != long {
					t.Fatalf("err=%v good=%d", err, r.good)
				}
				return
			}
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err = %v, want ErrCorrupt (r=%+v)", err, r)
			}
		})
	}
}

// A torn tail (no newline) between the cap and the 32 KiB buffer: characterize
// whether it is torn (repairable) or corrupt (blocks every write).
func TestReplayOversizedUnterminatedTailIsRepairableOrCorruptNotPanic(t *testing.T) {
	frag := `{"v":1,"at":"2026-10-01T00:00:00Z","key":"gh:a:1","note":"` + strings.Repeat("x", maxLineBytes)
	r, err := replay(strings.NewReader(frag))
	t.Logf("tail of %d bytes: torn=%v err=%v", len(frag), r.torn, err)
	if err != nil && !errors.Is(err, ErrCorrupt) {
		t.Fatalf("unexpected error class: %v", err)
	}
}

func TestWorstCaseEveryEscapingCharacterFitsTheCap(t *testing.T) {
	key := posting.Key("ab:" + strings.Repeat("<", MaxKeyBytes-3))
	for name, unit := range map[string]string{
		"lt": "<", "gt": ">", "amp": "&", "quote": `"`, "backslash": `\`, "newline": "\n", "tab": "\t",
	} {
		note := strings.Repeat(unit, MaxNoteBytes)
		if n := encodedLine(t, key, note); n > maxLineBytes {
			t.Errorf("%s: %d bytes, over cap", name, n)
		}
		if _, err := SetNote(Dir(t.TempDir()), key, note, t0); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Characters the encoder would expand to 6 bytes but that CheckNote must refuse first.
	for _, r := range []string{string(rune(0x2028)), string(rune(0x2029)), "\x00", "\x7f", string(rune(0x85)), "\x1b", string(rune(0xfeff))} {
		err := CheckNote("a" + r + "b")
		t.Logf("CheckNote(%q) = %v", r, err)
	}
	// Invalid UTF-8 would marshal as � (6 bytes per byte): refused.
	if err := CheckNote(strings.Repeat("\xff", MaxNoteBytes)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid UTF-8 note: %v", err)
	}
}
