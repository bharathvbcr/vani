package resumestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)

func hexOf(c byte) string { return strings.Repeat(string(c), 64) }

func goodMeta() Meta {
	return Meta{
		Source:    "tailored",
		Model:     "claude-opus-5-5",
		Cost:      Cost{USD: 0.42, Complete: true, Turns: 1},
		Checks:    Checks{Eligibility: "current", Lint: "passed", Fit: "passed", Figures: "verified"},
		Pages:     1,
		Notes:     []string{"tightened a bullet"},
		Keywords:  []string{"go", "kubernetes"},
		Selection: json.RawMessage(`{"sections":[{"title":"Experience","entries":[{"id":"e1","bullets":["b1","b2"]}]}]}`),
		Bullets:   []BulletRef{{ID: "b1", TextHash: "0123456789ab"}, {ID: "b2", TextHash: "ba9876543210"}},
		FitLines:  map[string]int{"b1": 2, "b2": 1},
		Prints:    Fingerprints{Bank: hexOf('a'), BaseSelection: hexOf('b'), Selections: hexOf('c'), Claims: hexOf('d'), RKB: hexOf('e')},
	}
}

func pdfBytes(tag string) []byte { return []byte("%PDF-1.7\n" + tag + "\n%%EOF\n") }

func newStore(t *testing.T) (*Store, string) {
	t.Helper()
	state := t.TempDir()
	s, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	return s, state
}

func mustSave(t *testing.T, s *Store, key string, m Meta, pdf []byte) (Meta, bool) {
	t.Helper()
	got, cur, err := s.Save(context.Background(), key, m, pdf)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	return got, cur
}

func TestSaveGetRoundTripAndFileModes(t *testing.T) {
	s, state := newStore(t)
	s.Now = func() time.Time { return t0 }
	pdf := pdfBytes("one")
	m := goodMeta()
	m.Version, m.Schema, m.Key, m.PDFSHA256, m.Created = 77, 9, "other", "junk", t0.Add(-time.Hour) // all overwritten
	got, cur := mustSave(t, s, "gh:acme:1", m, pdf)
	if got.Version != 1 || got.Schema != Schema || got.Key != "gh:acme:1" || !got.Created.Equal(t0) || !cur {
		t.Fatalf("saved %+v current=%v", got, cur)
	}
	if len(got.PDFSHA256) != 64 {
		t.Fatalf("pdf hash %q", got.PDFSHA256)
	}
	back, gotPDF, err := s.Get("gh:acme:1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPDF, pdf) || back.PDFSHA256 != got.PDFSHA256 || string(back.Selection) != string(m.Selection) || back.Bullets[1].ID != "b2" || back.FitLines["b1"] != 2 {
		t.Fatalf("round trip lost data: %+v", back)
	}
	folder := filepath.Join(state, "resumes", jobFolder("gh:acme:1"))
	for name, mode := range map[string]os.FileMode{"": 0o700, "key": 0o600, "v1.json": 0o600, "v1.pdf": 0o600, "current": 0o600, ".lock": 0o600} {
		st, err := os.Stat(filepath.Join(folder, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if st.Mode().Perm() != mode {
			t.Errorf("%q mode %v, want %v", name, st.Mode().Perm(), mode)
		}
	}
	// The PDF holds the owner's header: it must not be inlined in the JSON.
	raw, _ := os.ReadFile(filepath.Join(folder, "v1.json"))
	if bytes.Contains(raw, []byte("%PDF")) || bytes.Contains(raw, []byte("one")) && bytes.Contains(raw, []byte("EOF")) {
		t.Fatalf("PDF content leaked into the JSON: %s", raw)
	}
}

func TestVersionsAreNumberedInOrderAndGetNoPDF(t *testing.T) {
	s, _ := newStore(t)
	m := goodMeta()
	m.Checks.Fit = "not_run"
	m.Pages = 0
	for i := 1; i <= 3; i++ {
		got, _ := mustSave(t, s, "k", m, nil)
		if got.Version != i || got.PDFSHA256 != "" {
			t.Fatalf("save %d: %+v", i, got)
		}
	}
	_, pdf, err := s.Get("k", 2)
	if err != nil || pdf != nil {
		t.Fatalf("Get without a PDF: %v %v", len(pdf), err)
	}
	ms, err := s.List("k")
	if err != nil || len(ms) != 3 || ms[0].Version != 1 || ms[2].Version != 3 {
		t.Fatalf("List = %d, %v", len(ms), err)
	}
}

func TestSaveRefusesInvalidRecords(t *testing.T) {
	s, state := newStore(t)
	big := bytes.Repeat([]byte("x"), MaxPDFBytes)
	cases := []struct {
		name string
		mod  func(*Meta)
		pdf  []byte
	}{
		{"unknown source", func(m *Meta) { m.Source = "magic" }, pdfBytes("a")},
		{"empty source", func(m *Meta) { m.Source = "" }, pdfBytes("a")},
		{"empty eligibility", func(m *Meta) { m.Checks.Eligibility = "" }, pdfBytes("a")},
		{"empty fit", func(m *Meta) { m.Checks.Fit = "" }, pdfBytes("a")},
		{"fit passed without a pdf", func(m *Meta) {}, nil},
		{"selection with header", func(m *Meta) { m.Selection = json.RawMessage(`{"header":{"name":"x"}}`) }, pdfBytes("a")},
		{"selection with bank", func(m *Meta) { m.Selection = json.RawMessage(`{"bank":"/x/bank.json"}`) }, pdfBytes("a")},
		{"selection not an object", func(m *Meta) { m.Selection = json.RawMessage(`[1]`) }, pdfBytes("a")},
		{"no selection", func(m *Meta) { m.Selection = nil }, pdfBytes("a")},
		{"selection over 64 KiB", func(m *Meta) {
			m.Selection = json.RawMessage(`{"x":"` + strings.Repeat("a", MaxSelectionBytes) + `"}`)
		}, pdfBytes("a")},
		{"pdf not a pdf", func(m *Meta) {}, []byte("<html>")},
		{"pdf empty", func(m *Meta) {}, []byte{}},
		{"pdf over cap", func(m *Meta) {}, append([]byte("%PDF-"), big...)},
		{"bad fingerprint", func(m *Meta) { m.Prints.Bank = "nothex" }, pdfBytes("a")},
		{"bad bullet hash", func(m *Meta) { m.Bullets[0].TextHash = "short" }, pdfBytes("a")},
		{"nan cost", func(m *Meta) { m.Cost.USD = math.NaN() }, pdfBytes("a")},
		{"negative cost", func(m *Meta) { m.Cost.USD = -1 }, pdfBytes("a")},
		{"control char in keyword", func(m *Meta) { m.Keywords = []string{"a\x00b"} }, pdfBytes("a")},
		{"too many notes", func(m *Meta) { m.Notes = make([]string, maxListEntries+1) }, pdfBytes("a")},
		{"pages over cap", func(m *Meta) { m.Pages = 99 }, pdfBytes("a")},
	}
	for _, c := range cases {
		m := goodMeta()
		c.mod(&m)
		_, _, err := s.Save(context.Background(), "k", m, c.pdf)
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", c.name, err)
		}
	}
	// Nothing was written for any of them, not even a folder.
	if _, err := os.Stat(filepath.Join(state, "resumes", jobFolder("k"))); err == nil {
		t.Errorf("a refused Save left a folder behind")
	}
}

func TestParentMustBeOlder(t *testing.T) {
	s, _ := newStore(t)
	m := goodMeta()
	m.Parent = 5
	if _, _, err := s.Save(context.Background(), "k", m, pdfBytes("a")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Save with a parent newer than itself = %v", err)
	}
	if _, err := s.List("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("List = %v", err)
	}
}

func TestMetaOverSizeCapRefused(t *testing.T) {
	s, _ := newStore(t)
	m := goodMeta()
	m.Notes = make([]string, maxListEntries)
	for i := range m.Notes {
		m.Notes[i] = strings.Repeat("n", maxItemBytes)
	}
	_, _, err := s.Save(context.Background(), "k", m, pdfBytes("a"))
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "bytes, over") {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.List("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused record left a version: %v", err)
	}
}

func TestKeyValidationAndTraversal(t *testing.T) {
	s, state := newStore(t)
	bad := []string{"", "a\x00b", strings.Repeat("k", MaxKeyBytes+1), "a\xffb", "a\nb", "a\u0085b"}
	for _, k := range bad {
		if _, _, err := s.Save(context.Background(), k, goodMeta(), pdfBytes("a")); !errors.Is(err, ErrInvalid) {
			t.Errorf("Save(%q) = %v, want ErrInvalid", truncate(k, 20), err)
		}
		if _, err := s.List(k); !errors.Is(err, ErrInvalid) {
			t.Errorf("List(%q) = %v, want ErrInvalid", truncate(k, 20), err)
		}
		if _, err := s.Summary(k); !errors.Is(err, ErrInvalid) {
			t.Errorf("Summary(%q) = %v, want ErrInvalid", truncate(k, 20), err)
		}
	}
	// A key may hold slashes and dots: the folder is the hash, so it is just a key.
	sandbox := filepath.Join(state, "sandbox")
	keys := []string{"../../escape", "/etc/passwd", "a/../../b", "..", ".", strings.Repeat("k", MaxKeyBytes)}
	for _, k := range keys {
		got, _ := mustSave(t, s, k, goodMeta(), pdfBytes(k))
		if got.Key != k {
			t.Errorf("key %q stored as %q", k, got.Key)
		}
		if _, _, err := s.Get(k, 1); err != nil {
			t.Errorf("Get(%q): %v", truncate(k, 20), err)
		}
	}
	_ = sandbox
	// Everything is under <state>/resumes/<hash>; nothing beside it.
	ents, _ := os.ReadDir(state)
	if len(ents) != 1 || ents[0].Name() != "resumes" {
		t.Fatalf("state dir holds %v", ents)
	}
	folders, _ := os.ReadDir(filepath.Join(state, "resumes"))
	for _, f := range folders {
		if !folderRe.MatchString(f.Name()) && f.Name() != ".lock" {
			t.Errorf("unexpected entry %q under resumes", f.Name())
		}
	}
	if _, _, err := s.Get("k", 0); !errors.Is(err, ErrInvalid) {
		t.Errorf("Get v0 = %v", err)
	}
	if _, _, err := s.Get("k", MaxVersion+1); !errors.Is(err, ErrInvalid) {
		t.Errorf("Get v10000 = %v", err)
	}
}

func TestCurrentMovesOnlyWhenParentIsCurrent(t *testing.T) {
	s, _ := newStore(t)
	cur := func() int {
		t.Helper()
		m, err := s.Current("k")
		if err != nil {
			t.Fatal(err)
		}
		return m.Version
	}
	m := goodMeta()
	v1, moved := mustSave(t, s, "k", m, pdfBytes("1"))
	if v1.Version != 1 || !moved || cur() != 1 {
		t.Fatalf("first version must become current: v%d moved=%v", v1.Version, moved)
	}
	m.Parent = 1
	v2, moved := mustSave(t, s, "k", m, pdfBytes("2"))
	if v2.Version != 2 || !moved || cur() != 2 {
		t.Fatalf("child of current must become current: v%d moved=%v cur=%d", v2.Version, moved, cur())
	}
	// A build that started from v1 finishes after v2 was made: saved, current stays.
	v3, moved := mustSave(t, s, "k", m, pdfBytes("3"))
	if v3.Version != 3 || moved || cur() != 2 {
		t.Fatalf("stale parent moved current: v%d moved=%v cur=%d", v3.Version, moved, cur())
	}
	// A version with no parent does not displace an existing current either.
	m.Parent = 0
	v4, moved := mustSave(t, s, "k", m, pdfBytes("4"))
	if v4.Version != 4 || moved || cur() != 2 {
		t.Fatalf("parentless save moved current: v%d moved=%v cur=%d", v4.Version, moved, cur())
	}
	if err := s.SetCurrent(context.Background(), "k", 4); err != nil || cur() != 4 {
		t.Fatalf("SetCurrent: %v cur=%d", err, cur())
	}
	if err := s.SetCurrent(context.Background(), "k", 9); !errors.Is(err, ErrNotFound) || cur() != 4 {
		t.Fatalf("SetCurrent to a missing version: %v cur=%d", err, cur())
	}
}

func TestCrashBetweenPDFAndJSONLeavesNoVersion(t *testing.T) {
	s, state := newStore(t)
	mustSave(t, s, "k", goodMeta(), pdfBytes("1"))
	afterStage = func(stage string) error {
		if stage == "pdf" {
			return errors.New("crash")
		}
		return nil
	}
	_, _, err := s.Save(context.Background(), "k", goodMeta(), pdfBytes("2"))
	afterStage = func(string) error { return nil }
	if err == nil {
		t.Fatal("the crash was swallowed")
	}
	ms, lerr := s.List("k")
	if lerr != nil || len(ms) != 1 || ms[0].Version != 1 {
		t.Fatalf("a version without its JSON is visible: %d %v", len(ms), lerr)
	}
	folder := filepath.Join(state, "resumes", jobFolder("k"))
	if _, err := os.Stat(filepath.Join(folder, "v2.pdf")); err != nil {
		t.Fatalf("the test did not leave an orphan PDF: %v", err)
	}
	if _, _, err := s.Get("k", 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get of the orphan = %v", err)
	}
	// The next Save sweeps the orphan and reuses the number.
	got, _ := mustSave(t, s, "k", goodMeta(), pdfBytes("3"))
	if got.Version != 2 {
		t.Fatalf("next version %d", got.Version)
	}
	_, pdf, err := s.Get("k", 2)
	if err != nil || !bytes.Contains(pdf, []byte("3")) {
		t.Fatalf("v2 holds the orphan's bytes: %q %v", pdf, err)
	}
}

func TestOrphanPDFIsSweptOnSave(t *testing.T) {
	s, state := newStore(t)
	mustSave(t, s, "k", goodMeta(), pdfBytes("1"))
	folder := filepath.Join(state, "resumes", jobFolder("k"))
	orphan := filepath.Join(folder, "v7.pdf")
	if err := os.WriteFile(orphan, pdfBytes("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List("k"); err != nil {
		t.Fatalf("an orphan PDF must be ignored on read: %v", err)
	}
	mustSave(t, s, "k", goodMeta(), pdfBytes("2"))
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan survived the Save: %v", err)
	}
}

func TestCrashBetweenJSONAndPointerKeepsVersionAndCurrent(t *testing.T) {
	s, _ := newStore(t)
	mustSave(t, s, "k", goodMeta(), pdfBytes("1"))
	afterStage = func(stage string) error {
		if stage == "json" {
			return errors.New("crash")
		}
		return nil
	}
	m := goodMeta()
	m.Parent = 1
	got, cur, err := s.Save(context.Background(), "k", m, pdfBytes("2"))
	afterStage = func(string) error { return nil }
	if err == nil || got.Version != 2 || cur {
		t.Fatalf("Save = v%d current=%v err=%v; the committed version must be reported with the error", got.Version, cur, err)
	}
	ms, lerr := s.List("k")
	if lerr != nil || len(ms) != 2 {
		t.Fatalf("version not visible: %d %v", len(ms), lerr)
	}
	if c, err := s.Current("k"); err != nil || c.Version != 1 {
		t.Fatalf("current moved without its pointer write: %+v %v", c, err)
	}
	if _, pdf, err := s.Get("k", 2); err != nil || !bytes.Contains(pdf, []byte("2")) {
		t.Fatalf("committed version unreadable: %v", err)
	}
}

func TestFirstVersionCrashBeforePointerHasNoCurrent(t *testing.T) {
	s, _ := newStore(t)
	afterStage = func(stage string) error {
		if stage == "json" {
			return errors.New("crash")
		}
		return nil
	}
	_, _, err := s.Save(context.Background(), "k", goodMeta(), pdfBytes("1"))
	afterStage = func(string) error { return nil }
	if err == nil {
		t.Fatal("crash swallowed")
	}
	if _, err := s.Current("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Current = %v, want ErrNotFound", err)
	}
	sum, err := s.Summary("k")
	if err != nil || sum.Versions != 1 || sum.Current != nil {
		t.Fatalf("Summary = %+v %v", sum, err)
	}
}

func TestPruneKeepsCurrentAndUsed(t *testing.T) {
	s, state := newStore(t)
	ctx := context.Background()
	for i := 0; i < MaxVersions; i++ {
		m := goodMeta()
		m.Parent = i // each is a child of the previous current
		mustSave(t, s, "k", m, pdfBytes(strconv.Itoa(i)))
	}
	// current is v20. Pin v1 as used and v2 as current: the two oldest are now protected.
	if _, err := s.Use(ctx, "k", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCurrent(ctx, "k", 2); err != nil {
		t.Fatal(err)
	}
	m := goodMeta()
	m.Parent = 2
	got, moved := mustSave(t, s, "k", m, pdfBytes("new"))
	if got.Version != 21 || !moved {
		t.Fatalf("v%d moved=%v", got.Version, moved)
	}
	ms, err := s.List("k")
	if err != nil {
		t.Fatal(err)
	}
	var have []int
	for _, v := range ms {
		have = append(have, v.Version)
	}
	if len(have) != MaxVersions || !slices.Contains(have, 1) || !slices.Contains(have, 2) || slices.Contains(have, 3) || !slices.Contains(have, 21) {
		t.Fatalf("kept %v", have)
	}
	// The pruned version's PDF went with it.
	if _, err := os.Stat(filepath.Join(state, "resumes", jobFolder("k"), "v3.pdf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pruned PDF remains: %v", err)
	}
	if u, err := s.Used("k"); err != nil || u.Version != 1 {
		t.Fatalf("used = %+v %v", u, err)
	}
}

func TestPruneJobsKeepsUsedAndRemovesLeastRecentlySaved(t *testing.T) {
	s, state := newStore(t)
	root := filepath.Join(state, "resumes")
	if err := os.MkdirAll(filepath.Join(root, ".tmp", "build-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".tmp", "build-1", "page.html"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	name := func(i int) string { return fmt.Sprintf("%064x", i+1) }
	for i := 0; i < MaxJobs; i++ {
		d := filepath.Join(root, name(i))
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		at := t0.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(d, at, at); err != nil {
			t.Fatal(err)
		}
	}
	// The oldest holds a used version: it must survive; the next oldest goes.
	if err := os.WriteFile(filepath.Join(root, name(0), "used"), []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mustSave(t, s, "fresh", goodMeta(), pdfBytes("1"))
	if _, err := os.Stat(filepath.Join(root, name(0))); err != nil {
		t.Fatalf("a job with a used version was pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, name(1))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the least recently saved unused job survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, name(2))); err != nil {
		t.Fatalf("pruned more than one job: %v", err)
	}
	fs, _ := s.jobFolders()
	if len(fs) != MaxJobs {
		t.Fatalf("%d job folders, want %d", len(fs), MaxJobs)
	}
	if _, err := os.Stat(filepath.Join(root, ".tmp", "build-1", "page.html")); err != nil {
		t.Fatalf("the build work folder was touched: %v", err)
	}
	if _, _, err := s.Get("fresh", 1); err != nil {
		t.Fatal(err)
	}
}

func TestPruneJobsRefusesWhenEveryJobIsUsed(t *testing.T) {
	s, state := newStore(t)
	root := filepath.Join(state, "resumes")
	for i := 0; i < MaxJobs; i++ {
		d := filepath.Join(root, fmt.Sprintf("%064x", i+1))
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "used"), []byte("1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := s.Save(context.Background(), "fresh", goodMeta(), pdfBytes("1"))
	if !errors.Is(err, ErrFull) {
		t.Fatalf("err = %v, want ErrFull", err)
	}
	fs, _ := s.jobFolders()
	if len(fs) != MaxJobs {
		t.Fatalf("a used job was deleted: %d folders", len(fs))
	}
	if _, err := os.Stat(filepath.Join(root, jobFolder("fresh"))); err == nil {
		t.Fatal("a refused Save left its folder")
	}
	if err := s.AppendAttempt(context.Background(), "fresh", Attempt{Code: "canceled"}); !errors.Is(err, ErrFull) {
		t.Fatalf("AppendAttempt into a full store = %v", err)
	}
}

func TestDamagedPDFIsNeverServedOrUsed(t *testing.T) {
	s, state := newStore(t)
	mustSave(t, s, "k", goodMeta(), pdfBytes("honest"))
	p := filepath.Join(state, "resumes", jobFolder("k"), "v1.pdf")
	if err := os.WriteFile(p, pdfBytes("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	pdf, _, err := func() ([]byte, Meta, error) { m, b, err := s.Get("k", 1); return b, m, err }()
	if !errors.Is(err, ErrDamaged) || pdf != nil {
		t.Fatalf("Get served a mismatched PDF: %q %v", pdf, err)
	}
	if _, err := s.Use(context.Background(), "k", 1); !errors.Is(err, ErrDamaged) {
		t.Fatalf("Use = %v", err)
	}
	if _, err := s.Used("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused Use bound a version: %v", err)
	}
}

func TestDamageDetection(t *testing.T) {
	setup := func(t *testing.T) (*Store, string) {
		s, state := newStore(t)
		mustSave(t, s, "k", goodMeta(), pdfBytes("1"))
		return s, filepath.Join(state, "resumes", jobFolder("k"))
	}
	t.Run("json without its pdf", func(t *testing.T) {
		s, f := setup(t)
		os.Remove(filepath.Join(f, "v1.pdf"))
		if _, _, err := s.Get("k", 1); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Get = %v", err)
		}
		if _, err := s.Current("k"); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Current = %v", err)
		}
		if _, err := s.Summary("k"); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Summary = %v; a damaged job must not look like no resume", err)
		}
	})
	t.Run("key file names another posting", func(t *testing.T) {
		s, f := setup(t)
		os.WriteFile(filepath.Join(f, "key"), []byte("someone-else"), 0o600)
		for name, err := range map[string]error{
			"List":    func() error { _, e := s.List("k"); return e }(),
			"Current": func() error { _, e := s.Current("k"); return e }(),
			"Get":     func() error { _, _, e := s.Get("k", 1); return e }(),
			"Summary": func() error { _, e := s.Summary("k"); return e }(),
			"Save":    func() error { _, _, e := s.Save(context.Background(), "k", goodMeta(), pdfBytes("x")); return e }(),
			"Use":     func() error { _, e := s.Use(context.Background(), "k", 1); return e }(),
		} {
			if !errors.Is(err, ErrDamaged) {
				t.Errorf("%s = %v, want ErrDamaged", name, err)
			}
		}
	})
	t.Run("key file missing beside versions", func(t *testing.T) {
		s, f := setup(t)
		os.Remove(filepath.Join(f, "key"))
		if _, err := s.List("k"); !errors.Is(err, ErrDamaged) {
			t.Fatalf("List = %v", err)
		}
	})
	t.Run("record names another key", func(t *testing.T) {
		s, f := setup(t)
		b, _ := os.ReadFile(filepath.Join(f, "v1.json"))
		os.WriteFile(filepath.Join(f, "v1.json"), bytes.Replace(b, []byte(`"key":"k"`), []byte(`"key":"z"`), 1), 0o600)
		if _, _, err := s.Get("k", 1); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Get = %v", err)
		}
	})
	t.Run("unknown schema", func(t *testing.T) {
		s, f := setup(t)
		b, _ := os.ReadFile(filepath.Join(f, "v1.json"))
		os.WriteFile(filepath.Join(f, "v1.json"), bytes.Replace(b, []byte(`"schema":2`), []byte(`"schema":3`), 1), 0o600)
		if _, _, err := s.Get("k", 1); !errors.Is(err, ErrSchema) || errors.Is(err, ErrDamaged) {
			t.Fatalf("Get = %v, want ErrSchema", err)
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		s, f := setup(t)
		b, _ := os.ReadFile(filepath.Join(f, "v1.json"))
		os.WriteFile(filepath.Join(f, "v1.json"), bytes.Replace(b, []byte(`{"schema":2,`), []byte(`{"schema":2,"header":"x",`), 1), 0o600)
		if _, _, err := s.Get("k", 1); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Get = %v", err)
		}
	})
	t.Run("not json", func(t *testing.T) {
		s, f := setup(t)
		os.WriteFile(filepath.Join(f, "v1.json"), []byte("{"), 0o600)
		if _, _, err := s.Get("k", 1); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Get = %v", err)
		}
	})
	t.Run("record over the size cap", func(t *testing.T) {
		s, f := setup(t)
		os.WriteFile(filepath.Join(f, "v1.json"), bytes.Repeat([]byte(" "), MaxMetaBytes+1), 0o600)
		if _, _, err := s.Get("k", 1); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Get = %v", err)
		}
	})
	t.Run("pointer to a missing version", func(t *testing.T) {
		s, f := setup(t)
		os.WriteFile(filepath.Join(f, "current"), []byte("5\n"), 0o600)
		if _, err := s.Current("k"); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Current = %v", err)
		}
		if _, _, err := s.Save(context.Background(), "k", goodMeta(), pdfBytes("2")); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Save over a dangling pointer = %v", err)
		}
		// SetCurrent repairs it without reading it.
		if err := s.SetCurrent(context.Background(), "k", 1); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Current("k"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("garbage pointer", func(t *testing.T) {
		s, f := setup(t)
		os.WriteFile(filepath.Join(f, "used"), []byte("../../x"), 0o600)
		if _, err := s.Used("k"); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Used = %v", err)
		}
	})
	t.Run("one bad version does not hide the rest", func(t *testing.T) {
		s, f := setup(t)
		mustSave(t, s, "k", goodMeta(), pdfBytes("2"))
		os.WriteFile(filepath.Join(f, "v1.json"), []byte("{"), 0o600)
		ms, err := s.List("k")
		if !errors.Is(err, ErrDamaged) || len(ms) != 1 || ms[0].Version != 2 {
			t.Fatalf("List = %d versions, %v", len(ms), err)
		}
	})
	t.Run("symlinked record", func(t *testing.T) {
		s, f := setup(t)
		os.Rename(filepath.Join(f, "v1.json"), filepath.Join(f, "real.json"))
		os.Symlink("real.json", filepath.Join(f, "v1.json"))
		if _, _, err := s.Get("k", 1); !errors.Is(err, ErrDamaged) {
			t.Fatalf("Get through a link = %v", err)
		}
	})
	t.Run("names outside the version grammar are ignored", func(t *testing.T) {
		s, f := setup(t)
		for _, n := range []string{"v0.json", "v01.json", "v10000.json", "v1x.json", "vv.json", "v-1.json"} {
			os.WriteFile(filepath.Join(f, n), []byte("{"), 0o600)
		}
		ms, err := s.List("k")
		if err != nil || len(ms) != 1 {
			t.Fatalf("List = %d, %v", len(ms), err)
		}
	})
}

func TestUseRules(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	mk := func(mod func(*Meta), pdf []byte) int {
		m := goodMeta()
		mod(&m)
		got, _ := mustSave(t, s, "k", m, pdf)
		return got.Version
	}
	notRun := mk(func(m *Meta) { m.Checks.Fit, m.Pages = "not_run", 0 }, nil)
	failed := mk(func(m *Meta) { m.Checks.Fit = "failed" }, pdfBytes("f"))
	could := mk(func(m *Meta) { m.Checks.Fit = "could_not_run" }, pdfBytes("c"))
	stale := mk(func(m *Meta) { m.Checks.Eligibility = "stale" }, pdfBytes("s"))
	unknown := mk(func(m *Meta) { m.Checks.Eligibility = "unknown" }, pdfBytes("u"))
	noPDF := mk(func(m *Meta) { m.Checks.Fit = "failed" }, nil)
	soft := mk(func(m *Meta) { m.Checks.Fit = "soft" }, pdfBytes("soft"))
	passed := mk(func(m *Meta) {}, pdfBytes("ok"))
	for name, v := range map[string]int{"fit not_run": notRun, "fit failed": failed, "fit could_not_run": could, "stale": stale, "eligibility unknown": unknown, "no pdf": noPDF} {
		if _, err := s.Use(ctx, "k", v); !errors.Is(err, ErrNotUsable) {
			t.Errorf("%s: Use = %v, want ErrNotUsable", name, err)
		}
	}
	if _, err := s.Used("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused Use bound something: %v", err)
	}
	if _, err := s.Use(ctx, "k", 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("Use of a missing version = %v", err)
	}
	if _, err := s.Use(ctx, "nope", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("Use on an unknown job = %v", err)
	}
	for name, v := range map[string]int{"soft": soft, "passed": passed} {
		m, err := s.Use(ctx, "k", v)
		if err != nil || m.Version != v {
			t.Errorf("%s: Use = %+v %v", name, m, err)
		}
		if u, err := s.Used("k"); err != nil || u.Version != v {
			t.Errorf("%s: Used = %+v %v", name, u, err)
		}
	}
}

func TestNeverWrittenStore(t *testing.T) {
	s, state := newStore(t)
	ctx := context.Background()
	if _, err := s.List("k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("List = %v", err)
	}
	if _, err := s.Current("k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Current = %v", err)
	}
	if _, err := s.Used("k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Used = %v", err)
	}
	if sum, err := s.Summary("k"); err != nil || sum.Versions != 0 || sum.Current != nil || sum.Used != nil {
		t.Errorf("Summary = %+v %v", sum, err)
	}
	if err := s.SetCurrent(ctx, "k", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetCurrent = %v", err)
	}
	if _, err := s.Use(ctx, "k", 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("Use = %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "resumes")); err == nil {
		t.Errorf("a read-side call created the store")
	}
	if _, err := Open(""); !errors.Is(err, ErrInvalid) {
		t.Errorf("Open(\"\") = %v", err)
	}
}

func TestSummaryDropsSelectionAndCounts(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	mustSave(t, s, "k", goodMeta(), pdfBytes("1"))
	m := goodMeta()
	m.Parent, m.Source, m.Pages = 1, "edited", 2
	mustSave(t, s, "k", m, pdfBytes("2"))
	if _, err := s.Use(ctx, "k", 1); err != nil {
		t.Fatal(err)
	}
	sum, err := s.Summary("k")
	if err != nil {
		t.Fatal(err)
	}
	if sum.Versions != 2 || sum.Current == nil || sum.Current.Version != 2 || sum.Current.Source != "edited" || sum.Current.Pages != 2 || sum.Used == nil || sum.Used.Version != 1 {
		t.Fatalf("Summary = %+v", sum)
	}
	if sum.Current.Selection != nil || sum.Used.Selection != nil {
		t.Fatal("Summary carries the selection")
	}
}

func TestConcurrentSavesAcrossStoreInstances(t *testing.T) {
	_, state := newStore(t)
	const workers, each = 4, 4
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(state)
			if err != nil {
				errs <- err
				return
			}
			for i := 0; i < each; i++ {
				if _, _, err := s.Save(context.Background(), "k", goodMeta(), pdfBytes(fmt.Sprintf("w%d-%d", w, i))); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	checkInterleaved(t, state, workers*each)
}

// checkInterleaved asserts n versions exist, numbered 1..n once each, every
// PDF matches its record, and exactly one of them is current.
func checkInterleaved(t *testing.T, state string, n int) {
	t.Helper()
	s, _ := Open(state)
	ms, err := s.List("k")
	if err != nil || len(ms) != n {
		t.Fatalf("List = %d versions, %v; want %d", len(ms), err, n)
	}
	for i, m := range ms {
		if m.Version != i+1 {
			t.Fatalf("versions %d at index %d", m.Version, i)
		}
		if _, _, err := s.Get("k", m.Version); err != nil {
			t.Fatalf("v%d: %v", m.Version, err)
		}
	}
	if c, err := s.Current("k"); err != nil || c.Version < 1 || c.Version > n {
		t.Fatalf("Current = %+v %v", c, err)
	}
}

func TestSavesFromSeparateProcessesInterleave(t *testing.T) {
	state := t.TempDir()
	const procs, each = 3, 4
	var cmds []*exec.Cmd
	var outs []*bytes.Buffer
	for p := 0; p < procs; p++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperSaver$")
		cmd.Env = append(os.Environ(), "RESUMESTORE_HELPER_STATE="+state, "RESUMESTORE_HELPER_ID="+strconv.Itoa(p), "RESUMESTORE_HELPER_N="+strconv.Itoa(each))
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds, outs = append(cmds, cmd), append(outs, &out)
	}
	for i, c := range cmds {
		if err := c.Wait(); err != nil {
			t.Fatalf("process %d: %v\n%s", i, err, outs[i])
		}
	}
	checkInterleaved(t, state, procs*each)
}

// TestHelperSaver is the child of TestSavesFromSeparateProcessesInterleave; it
// does nothing in a normal run.
func TestHelperSaver(t *testing.T) {
	state := os.Getenv("RESUMESTORE_HELPER_STATE")
	if state == "" {
		t.Skip("helper process only")
	}
	id := os.Getenv("RESUMESTORE_HELPER_ID")
	n, _ := strconv.Atoi(os.Getenv("RESUMESTORE_HELPER_N"))
	s, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, _, err := s.Save(context.Background(), "k", goodMeta(), pdfBytes(id+"-"+strconv.Itoa(i))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSaveWaitsForTheJobLockAndHonoursContext(t *testing.T) {
	s, state := newStore(t)
	mustSave(t, s, "k", goodMeta(), pdfBytes("1"))
	// Hold the job lock as a slow writer would.
	_, release, err := s.lockJob(context.Background(), "k", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, _, err = s.Save(ctx, "k", goodMeta(), pdfBytes("2"))
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("Save under a held lock = %v, want ErrBusy", err)
	}
	release()
	if _, _, err := s.Save(context.Background(), "k", goodMeta(), pdfBytes("2")); err != nil {
		t.Fatalf("Save after release: %v", err)
	}
	_ = state
}

func TestAttemptsAreBoundedAndValidated(t *testing.T) {
	s, state := newStore(t)
	ctx := context.Background()
	s.Now = func() time.Time { return t0 }
	for i := 0; i < maxAttemptLines+30; i++ {
		if err := s.AppendAttempt(ctx, "k", Attempt{Cost: Cost{USD: float64(i), Turns: 1}, Code: "canceled"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Attempts("k")
	if err != nil || len(got) != maxAttemptLines {
		t.Fatalf("Attempts = %d, %v", len(got), err)
	}
	if got[len(got)-1].Cost.USD != float64(maxAttemptLines+29) || got[0].Cost.USD != 30 || !got[0].Created.Equal(t0) {
		t.Fatalf("window is not the most recent lines: first %+v last %+v", got[0], got[len(got)-1])
	}
	st, _ := os.Stat(filepath.Join(state, "resumes", jobFolder("k"), attemptsFile))
	if st.Size() > maxAttemptsBytes || st.Mode().Perm() != 0o600 {
		t.Fatalf("attempts file %d bytes mode %v", st.Size(), st.Mode().Perm())
	}
	for _, a := range []Attempt{{Code: ""}, {Code: "has space"}, {Code: strings.Repeat("c", 65)}, {Code: "ok", Cost: Cost{USD: math.Inf(1)}}, {Code: "ok", Cost: Cost{USD: -1}}} {
		if err := s.AppendAttempt(ctx, "k", a); !errors.Is(err, ErrInvalid) {
			t.Errorf("AppendAttempt(%+v) = %v", a, err)
		}
	}
	// Attempts do not make versions, and the folder is still a valid job.
	if _, err := s.List("k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("List = %v", err)
	}
	if sum, err := s.Summary("k"); err != nil || sum.Versions != 0 {
		t.Fatalf("Summary = %+v %v", sum, err)
	}
	mustSave(t, s, "k", goodMeta(), pdfBytes("1"))
	if got, err := s.Attempts("k"); err != nil || len(got) != maxAttemptLines {
		t.Fatalf("a Save disturbed the attempts: %d %v", len(got), err)
	}
}

func TestAttemptsLogSurvivesGarbageAndSizeCap(t *testing.T) {
	s, state := newStore(t)
	ctx := context.Background()
	if err := s.AppendAttempt(ctx, "k", Attempt{Code: "canceled"}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(state, "resumes", jobFolder("k"), attemptsFile)
	if err := os.WriteFile(p, append([]byte("not json\n"), bytes.Repeat([]byte("x"), 6*maxAttemptsBytes)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attempts("k"); !errors.Is(err, ErrDamaged) {
		t.Fatalf("Attempts over garbage = %v", err)
	}
	// A paid attempt must still be recorded: the bad log is replaced.
	if err := s.AppendAttempt(ctx, "k", Attempt{Code: "timeout"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Attempts("k")
	if err != nil || len(got) != 1 || got[0].Code != "timeout" {
		t.Fatalf("Attempts = %+v %v", got, err)
	}
	// Long codes at the line cap stay under the byte cap.
	for i := 0; i < maxAttemptLines; i++ {
		if err := s.AppendAttempt(ctx, "k", Attempt{Code: strings.Repeat("c", 64)}); err != nil {
			t.Fatal(err)
		}
	}
	if st, _ := os.Stat(p); st.Size() > maxAttemptsBytes {
		t.Fatalf("attempts file is %d bytes", st.Size())
	}
}

func TestSweepRemovesStaleTempFilesOnly(t *testing.T) {
	s, state := newStore(t)
	mustSave(t, s, "k", goodMeta(), pdfBytes("1"))
	f := filepath.Join(state, "resumes", jobFolder("k"))
	stale, fresh := filepath.Join(f, ".v9.json.tmp-1"), filepath.Join(f, ".v9.json.tmp-2")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * time.Hour)
	os.Chtimes(stale, old, old)
	mustSave(t, s, "k", goodMeta(), pdfBytes("2"))
	if _, err := os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale temp file survived: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("a fresh temp file (a live writer's) was removed: %v", err)
	}
}

func TestVersionNumbersRunOut(t *testing.T) {
	s, state := newStore(t)
	mustSave(t, s, "k", goodMeta(), pdfBytes("1"))
	f := filepath.Join(state, "resumes", jobFolder("k"))
	b, _ := os.ReadFile(filepath.Join(f, "v1.json"))
	b = bytes.Replace(b, []byte(`"version":1`), []byte(`"version":9999`), 1)
	os.WriteFile(filepath.Join(f, "v9999.json"), b, 0o600)
	os.Rename(filepath.Join(f, "v1.pdf"), filepath.Join(f, "v9999.pdf"))
	os.Remove(filepath.Join(f, "v1.json"))
	os.Remove(filepath.Join(f, "current"))
	_, _, err := s.Save(context.Background(), "k", goodMeta(), pdfBytes("2"))
	if !errors.Is(err, ErrFull) {
		t.Fatalf("Save past v9999 = %v, want ErrFull", err)
	}
}

func TestStoreIgnoresDotEntriesUnderRoot(t *testing.T) {
	s, state := newStore(t)
	root := filepath.Join(state, "resumes")
	os.MkdirAll(filepath.Join(root, ".tmp", "vani-resume-1"), 0o700)
	os.WriteFile(filepath.Join(root, ".tmp", "vani-resume-1", "x"), []byte("x"), 0o600)
	os.MkdirAll(filepath.Join(root, "not-a-hash"), 0o700)
	for i := 0; i < 5; i++ {
		mustSave(t, s, "k"+strconv.Itoa(i), goodMeta(), pdfBytes("1"))
	}
	fs, err := s.jobFolders()
	if err != nil || len(fs) != 5 {
		t.Fatalf("jobFolders = %d, %v", len(fs), err)
	}
	if _, err := os.Stat(filepath.Join(root, ".tmp", "vani-resume-1", "x")); err != nil {
		t.Fatal(err)
	}
}
