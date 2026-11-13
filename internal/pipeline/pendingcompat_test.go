package pipeline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// The pending.json the installed build (1ad3d2e) writes is version 1 with
// entries of class, at and reason only. The reader here must keep every one of
// them, however old (nothing is cleared for age on load), with the new counters
// at zero, so installing this build over a live file loses no pending reopen.
func TestAPendingFileFromThePreviousBuildLoadsWithEveryEntryKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending.json")
	old := `{
 "v": 1,
 "updated": "2026-11-12T20:00:00Z",
 "items": {
  "gh:acme:1": {"class": "title", "at": "2026-11-12T19:00:00Z", "reason": "title rule loosened"},
  "gh:acme:2": {"class": "model", "at": "2026-10-01T19:00:00Z"},
  "wd:t/Ext:R-3": {"class": "exclusion", "at": "2026-09-01T19:00:00Z", "reason": "exclude edited"},
  "gh:acme:4": {"class": "fresh", "at": "2026-11-01T00:00:00Z"},
  "gh:acme:5": {"class": "refresh", "at": "2026-11-02T00:00:00Z"}
 }
}`
	if err := os.WriteFile(path, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	s, err := LoadPendingReopens(path, now)
	if err != nil {
		t.Fatalf("a file the installed build wrote is refused: %v", err)
	}
	if s.Len() != 5 || s.Expired != 0 || s.GoneLen() != 0 {
		t.Fatalf("loaded %d entries (expired %d, gone %d), want 5, 0, 0", s.Len(), s.Expired, s.GoneLen())
	}
	for key, class := range map[posting.Key]string{
		"gh:acme:1": reopenClassTitle, "gh:acme:2": reopenClassModel, "wd:t/Ext:R-3": reopenClassExclusion,
		"gh:acme:4": reopenClassFresh, "gh:acme:5": reopenClassRefresh,
	} {
		e, ok := s.Items[key]
		if !ok || e.Class != class || e.Out != 0 || !e.Last.IsZero() {
			t.Errorf("%s: %+v (found %v), want class %s with no read time counted", key, e, ok, class)
		}
	}
	// Saved again, it is still a file this reader loads whole.
	if err := s.Save(path, now); err != nil {
		t.Fatal(err)
	}
	if again, err := LoadPendingReopens(path, now); err != nil || again.Len() != 5 {
		t.Fatalf("after a save: %v, %d entries", err, again.Len())
	}
}

// What the build at 1ad3d2e does with a file that carries the new keys (out,
// last, gone): its reader decodes strictly (DisallowUnknownFields), so it
// refuses the file loudly rather than loading it as empty or dropping entries;
// its run logs "pending reopens not kept/applied this run", applies nothing and
// never overwrites the file. This pins that shape: the old decoder, copied
// here, rejects a file this build wrote, naming the unknown key. It is why the
// version stays 1: the old reader's refusal already names the key, and a file
// without the new keys still loads there.
func TestTheNewKeysAreRefusedLoudlyByTheStrictReaderOfThePreviousBuild(t *testing.T) {
	type oldEntry struct {
		Class  string    `json:"class"`
		At     time.Time `json:"at"`
		Reason string    `json:"reason,omitempty"`
	}
	type oldFile struct {
		V       int                 `json:"v"`
		Updated time.Time           `json:"updated"`
		Items   map[string]oldEntry `json:"items"`
	}
	now := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	decode := func(raw []byte) error {
		var f oldFile
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		return dec.Decode(&f)
	}
	save := func(s *PendingReopens) []byte {
		path := filepath.Join(t.TempDir(), "pending.json")
		if err := s.Save(path, now); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	plain := NewPendingReopens()
	plain.Items["gh:acme:1"] = PendingReopen{Class: reopenClassTitle, At: now}
	if err := decode(save(plain)); err != nil {
		t.Fatalf("a file with no new keys must stay readable by the previous build: %v", err)
	}

	counted := NewPendingReopens()
	counted.Items["gh:acme:1"] = PendingReopen{Class: reopenClassExclusion, At: now, Out: 24 * time.Hour, Last: now}
	if err := decode(save(counted)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("the previous build should refuse an entry carrying out/last, naming the key: %v", err)
	}

	gone := NewPendingReopens()
	gone.Gone["gh:acme:2"] = now
	if err := decode(save(gone)); err == nil || !strings.Contains(err.Error(), `"gone"`) {
		t.Fatalf("the previous build should refuse a gone key, naming it: %v", err)
	}
}
