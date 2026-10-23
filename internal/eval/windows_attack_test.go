package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func windowRow(t *testing.T, rep WindowReport, w string) Promotion {
	t.Helper()
	ps := rep.Tables[w]
	if len(ps) != 1 {
		t.Fatalf("%s rows: %+v", w, ps)
	}
	return ps[0]
}

// A member whose label is edited to all-skip stays a member (it is counted
// toward the 100) and, labels being frozen as filed, still scores as filed; the
// edit is reported as a changed label.
func TestAttackSkipAfterFilingKeepsMemberButNotAsked(t *testing.T) {
	s, _, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 100, 0, 0)
	if _, err := s.SyncWindows(recs); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{Key: "a000", Labels: map[string]string{"work_mode": Skip}}); err != nil {
		t.Fatal(err)
	}
	rep, err := BuildWindows(recs, s, Names{})
	if err != nil {
		t.Fatal(err)
	}
	if got := memberCount(t, s); got["window-1"] != 100 {
		t.Fatalf("members %v: a filed posting never moves", got)
	}
	if p := windowRow(t, rep, "window-1"); p.Asked != 100 || rep.Changed["window-1"] != 1 {
		t.Fatalf("asked %d changed %d, want 100 and 1 (labels are frozen as filed; the edit is reported)", p.Asked, rep.Changed["window-1"])
	}
	if rep.Members["window-1"] != 100 {
		t.Fatalf("members %d", rep.Members["window-1"])
	}
}

// Label-time ties (every label stamped the same second) are broken by key, so
// the filing is a pure function of the label file, whatever order the ledger
// records arrive in.
func TestAttackLabelTimeTiesAreDeterministic(t *testing.T) {
	s, _, _ := newStore(t) // constant clock: every label has the same LabeledAt
	recs := labelRange(t, s, "a", 130, 0, 0)
	rev := make([]ledger.Record, len(recs))
	for i, r := range recs {
		rev[len(recs)-1-i] = r
	}
	if _, err := s.SyncWindows(rev); err != nil {
		t.Fatal(err)
	}
	m, _ := s.WindowMembers()
	for i := 0; i < 130; i++ {
		key := posting.Key(fmt.Sprintf("a%03d", i))
		want := "window-1"
		if i >= 100 {
			want = "window-2"
		}
		if m[key] != want {
			t.Fatalf("%s in %q, want %q (ties break by key)", key, m[key], want)
		}
	}
}

// Gate arithmetic: a 100-posting window can supply 10 discordant postings all
// won by the shadow, and the bound is reachable at n = MinDiscordant.
func TestAttackGateIsReachableInOneWindow(t *testing.T) {
	need := -1
	for k := 0; k <= MinDiscordant; k++ {
		if lowerBound(k, MinDiscordant, promotionAlpha) >= PromotionBound {
			need = k
			break
		}
	}
	if need < 0 {
		t.Fatal("no k of 10 clears the bound: the gate is unreachable")
	}
	t.Logf("with exactly %d discordant, shadow must win %d (lower bound %.3f)", MinDiscordant, need, lowerBound(need, MinDiscordant, promotionAlpha))
	s, _, _ := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 100, 10, 0)
	recs = append(recs, labelRange(t, s, "b", 100, 10, 0)...)
	if v := windowVerdict(t, s, recs); !v.Promotable {
		t.Fatalf("reachable case refused: %+v", v)
	}
}

// ---- windows.json corruption

func writeWindows(t *testing.T, s *Store, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.labelDir, WindowsFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAttackWindowsFileCorruptionRefused(t *testing.T) {
	reg := `[{"name":"window-1","size":100},{"name":"window-2","size":100}]`
	cases := map[string]string{
		"empty":                 "",
		"truncated":             `{"v":2,"windows":[{"name":"window-1","size":100},{"na`,
		"null":                  `null`,
		"array":                 `[]`,
		"v1 bare-string member": `{"v":1,"windows":` + reg + `,"members":{"k":"window-1"}}`,
		"forged member":         `{"v":2,"windows":` + reg + `,"members":{"k":{"window":"window-1","labeled_at":"2026-10-01T00:00:00Z","labels":{"work_mode":"remote"},"label_sha256":"00"}}}`,
		"unknown field":         `{"v":2,"windows":` + reg + `,"members":{},"closed_at":{}}`,
		"future version":        `{"v":3,"windows":` + reg + `,"members":{}}`,
		"version zero":          `{"windows":` + reg + `,"members":{}}`,
		"no registration":       `{"v":2,"members":{}}`,
		"extra window":          `{"v":2,"windows":[{"name":"window-1","size":100},{"name":"window-2","size":100},{"name":"window-3","size":100}],"members":{}}`,
		"renamed window":        `{"v":2,"windows":[{"name":"w1","size":100},{"name":"window-2","size":100}],"members":{}}`,
		"unknown member":        `{"v":2,"windows":` + reg + `,"members":{"k":{"window":"window-9"}}}`,
		"empty window name":     `{"v":2,"windows":` + reg + `,"members":{"k":{"window":""}}}`,
		"member not object":     `{"v":2,"windows":` + reg + `,"members":{"k":1}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, _ := newStore(t)
			writeWindows(t, s, body)
			if _, err := s.WindowMembers(); err == nil {
				t.Fatalf("%s accepted", name)
			}
			if _, err := s.SyncWindows(nil); err == nil {
				t.Fatalf("%s: sync accepted it and would overwrite", name)
			}
			if _, _, err := s.OpenWindow(); err == nil {
				t.Fatalf("%s: OpenWindow accepted", name)
			}
			raw, _ := os.ReadFile(filepath.Join(s.labelDir, WindowsFile))
			if string(raw) != body {
				t.Fatalf("%s: a refused file was rewritten: %q", name, raw)
			}
		})
	}
}

func TestAttackWindowsFileOverfullRefused(t *testing.T) {
	s, _, _ := newStore(t)
	var b strings.Builder
	b.WriteString(`{"v":2,"windows":[{"name":"window-1","size":100},{"name":"window-2","size":100}],"members":{`)
	for i := 0; i < 101; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		key := posting.Key(fmt.Sprintf("k%03d", i))
		at := time.Date(2026, 10, 1, 0, 0, i, 0, time.UTC)
		labels := map[string]string{"work_mode": "remote"}
		fmt.Fprintf(&b, `"%s":{"window":"window-1","labeled_at":"%s","labels":{"work_mode":"remote"},"label_sha256":"%s"}`, key, at.Format(time.RFC3339), labelIdentity(key, at, labels))
	}
	b.WriteString(`}}`)
	writeWindows(t, s, b.String())
	if _, err := s.WindowMembers(); err == nil {
		t.Fatal("101 members in a 100 window accepted")
	}
}

// Two writers (separate Store handles, as two processes would have) syncing at
// once: the lock is held across read-modify-write, so the union is exactly the
// windows' capacity and nothing is filed twice.
func TestAttackConcurrentSyncKeepsWindowsConsistent(t *testing.T) {
	s, labelDir, textDir := newStore(t)
	tick(s)
	recs := labelRange(t, s, "a", 230, 0, 0)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		s2, err := NewStore(labelDir, textDir)
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s2.SyncWindows(recs)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := memberCount(t, s); got["window-1"] != 100 || got["window-2"] != 100 {
		t.Fatalf("members %v", got)
	}
	m, _ := s.WindowMembers()
	if m["a000"] != "window-1" || m["a099"] != "window-1" || m["a100"] != "window-2" || m["a199"] != "window-2" {
		t.Fatalf("filing out of label-time order: %v %v %v %v", m["a000"], m["a099"], m["a100"], m["a199"])
	}
	if _, ok := m["a200"]; ok {
		t.Fatal("a posting beyond both windows was filed")
	}
}
