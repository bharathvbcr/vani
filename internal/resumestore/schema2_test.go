package resumestore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// asSchema1 rewrites version n of key as a schema 1 record: the layout older
// vani wrote, which has no instruction.
func asSchema1(t *testing.T, state, key string, n int) {
	t.Helper()
	p := versionFile(filepath.Join(Dir(state), jobFolder(key)), n, "json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"schema":2`)) {
		t.Fatalf("the record is not schema 2: %.80s", raw)
	}
	if err := os.WriteFile(p, bytes.Replace(raw, []byte(`"schema":2`), []byte(`"schema":1`), 1), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSchema2RoundTripsTheInstruction(t *testing.T) {
	s, state := newStore(t)
	m := goodMeta()
	m.Instruction = "lead with the serving work, then the compilers"
	saved, _ := mustSave(t, s, "k", m, pdfBytes("a"))
	if saved.Schema != 2 || Schema != 2 {
		t.Fatalf("schema = %d (const %d), want 2", saved.Schema, Schema)
	}
	got, _, err := s.Get("k", 1)
	if err != nil || got.Instruction != m.Instruction || got.Schema != 2 {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	listed, err := s.List("k")
	if err != nil || len(listed) != 1 || listed[0].Instruction != m.Instruction {
		t.Fatalf("List = %+v, %v", listed, err)
	}
	cur, err := s.Current("k")
	if err != nil || cur.Instruction != m.Instruction {
		t.Fatalf("Current = %+v, %v", cur, err)
	}
	// No instruction writes no key: a record without one is the same bytes as before.
	mm := goodMeta()
	mm.Reason = "x"
	mustSave(t, s, "k2", mm, pdfBytes("b"))
	raw, _ := os.ReadFile(versionFile(filepath.Join(Dir(state), jobFolder("k2")), 1, "json"))
	if bytes.Contains(raw, []byte("instruction")) {
		t.Fatalf("a record with no instruction names the key: %s", raw)
	}
}

func TestSchema1RecordsStillReadWithNoInstruction(t *testing.T) {
	s, state := newStore(t)
	mustSave(t, s, "k", goodMeta(), pdfBytes("a"))
	mustSave(t, s, "k", goodMeta(), pdfBytes("b"))
	asSchema1(t, state, "k", 1)
	m, pdf, err := s.Get("k", 1)
	if err != nil || m.Schema != 1 || m.Instruction != "" || len(pdf) == 0 {
		t.Fatalf("v1 read = %+v, %v", m, err)
	}
	all, err := s.List("k")
	if err != nil || len(all) != 2 || all[0].Schema != 1 || all[1].Schema != 2 {
		t.Fatalf("List = %+v, %v (a v1 and a v2 record live side by side)", all, err)
	}
	sum, err := s.Summary("k")
	if err != nil || sum.Versions != 2 || sum.Current == nil {
		t.Fatalf("Summary = %+v, %v", sum, err)
	}
	sums, err := s.Summaries()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := sums.Of("k"); err != nil || got.Versions != 2 {
		t.Fatalf("Summaries.Of = %+v, %v", got, err)
	}
	// The next Save writes schema 2 whatever its parent is.
	next, _ := mustSave(t, s, "k", goodMeta(), pdfBytes("c"))
	if next.Schema != 2 || next.Version != 3 {
		t.Fatalf("next = %+v", next)
	}
}

func TestSchemaOutsideTheReadRangeIsUnsupportedNotDamage(t *testing.T) {
	for _, bad := range []string{"0", "3", "99", "-1"} {
		s, state := newStore(t)
		mustSave(t, s, "k", goodMeta(), pdfBytes("a"))
		p := versionFile(filepath.Join(Dir(state), jobFolder("k")), 1, "json")
		raw, _ := os.ReadFile(p)
		if err := os.WriteFile(p, bytes.Replace(raw, []byte(`"schema":2`), []byte(`"schema":`+bad), 1), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.Get("k", 1); !errors.Is(err, ErrSchema) || errors.Is(err, ErrDamaged) {
			t.Errorf("schema %s: Get = %v, want ErrSchema", bad, err)
		}
	}
}

func TestSchema1RecordThatCarriesAnInstructionIsDamage(t *testing.T) {
	s, state := newStore(t)
	m := goodMeta()
	m.Instruction = "a preference"
	mustSave(t, s, "k", m, pdfBytes("a"))
	asSchema1(t, state, "k", 1)
	if _, _, err := s.Get("k", 1); !errors.Is(err, ErrDamaged) {
		t.Fatalf("Get = %v, want ErrDamaged: schema 1 never had an instruction", err)
	}
}

func TestSaveRefusesAnInstructionTheStoreWillNotKeep(t *testing.T) {
	s, state := newStore(t)
	long := strings.Repeat("é", MaxInstructionRunes+1) // runes, not bytes
	for name, in := range map[string]string{
		"over 500 runes":  long,
		"newline":         "one\ntwo",
		"tab":             "one\ttwo",
		"nul":             "a\x00b",
		"bidi override":   "abc‮def",
		"zero width":      "a​b",
		"invalid utf8":    "ab\xffcd",
		"leading space":   " lead with go",
		"trailing space":  "lead with go ",
		"only whitespace": "   ",
	} {
		m := goodMeta()
		m.Instruction = in
		if _, _, err := s.Save(t.Context(), "k", m, pdfBytes("a")); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Save = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(Dir(state), jobFolder("k"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused record touched the disk: %v", err)
	}
	// The bound is in runes: 500 two-byte runes (1,000 bytes) are kept.
	m := goodMeta()
	m.Instruction = strings.Repeat("é", MaxInstructionRunes)
	if got, _ := mustSave(t, s, "k", m, pdfBytes("a")); got.Instruction != m.Instruction {
		t.Fatalf("a 500 rune instruction was not kept")
	}
}

func TestReadRefusesAnInstructionWrittenByHandThatBreaksTheBound(t *testing.T) {
	s, state := newStore(t)
	m := goodMeta()
	m.Instruction = "fine"
	mustSave(t, s, "k", m, pdfBytes("a"))
	p := versionFile(filepath.Join(Dir(state), jobFolder("k")), 1, "json")
	raw, _ := os.ReadFile(p)
	os.WriteFile(p, bytes.Replace(raw, []byte(`"instruction":"fine"`), []byte(`"instruction":"`+strings.Repeat("x", MaxInstructionRunes+1)+`"`), 1), 0o600)
	if _, _, err := s.Get("k", 1); !errors.Is(err, ErrDamaged) {
		t.Fatalf("Get = %v, want ErrDamaged", err)
	}
}

func TestSchema2RoundTripsScopeAndLockAndASchema1RecordWithThemIsDamage(t *testing.T) {
	s, state := newStore(t)
	m := goodMeta()
	m.Scope = "GPU Systems"
	m.Lock = []string{"Summary", "Agent Systems"}
	mustSave(t, s, "k", m, pdfBytes("a"))
	got, _, err := s.Get("k", 1)
	if err != nil || got.Scope != "GPU Systems" || strings.Join(got.Lock, "|") != "Summary|Agent Systems" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if cur, err := s.Current("k"); err != nil || cur.Scope != "GPU Systems" || len(cur.Lock) != 2 {
		t.Fatalf("Current = %+v, %v", cur, err)
	}
	// Nothing kept writes neither key.
	mustSave(t, s, "k2", goodMeta(), pdfBytes("b"))
	raw, _ := os.ReadFile(versionFile(filepath.Join(Dir(state), jobFolder("k2")), 1, "json"))
	if bytes.Contains(raw, []byte(`"scope"`)) || bytes.Contains(raw, []byte(`"lock"`)) {
		t.Fatalf("a record that kept nothing names the keys: %s", raw)
	}
	// Schema 1 never had them.
	asSchema1(t, state, "k", 1)
	if _, _, err := s.Get("k", 1); !errors.Is(err, ErrDamaged) {
		t.Fatalf("Get = %v, want ErrDamaged: schema 1 never had a scope or lock", err)
	}
	// A rekey carries them.
	mustSave(t, s, "x:old", m, pdfBytes("c"))
	if err := s.Rekey(t.Context(), "x:old", "x:new"); err != nil {
		t.Fatal(err)
	}
	if all, err := s.List("x:new"); err != nil || len(all) != 1 || all[0].Scope != "GPU Systems" || len(all[0].Lock) != 2 {
		t.Fatalf("after Rekey: %+v, %v", all, err)
	}
}

func TestSaveRefusesAScopeOrLockTheStoreWillNotKeep(t *testing.T) {
	s, state := newStore(t)
	names := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "S" + strconv.Itoa(i)
		}
		return out
	}
	for name, edit := range map[string]func(*Meta){
		"121 rune scope":    func(m *Meta) { m.Scope = strings.Repeat("é", MaxNameRunes+1) },
		"121 rune lock":     func(m *Meta) { m.Lock = []string{strings.Repeat("é", MaxNameRunes+1)} },
		"21 locks":          func(m *Meta) { m.Lock = names(MaxLocks + 1) },
		"newline in scope":  func(m *Meta) { m.Scope = "a\nb" },
		"bidi in a lock":    func(m *Meta) { m.Lock = []string{"abc‮def"} },
		"invalid utf8":      func(m *Meta) { m.Scope = "ab\xffcd" },
		"empty lock":        func(m *Meta) { m.Lock = []string{""} },
		"space around lock": func(m *Meta) { m.Lock = []string{" Summary"} },
		"scope with space":  func(m *Meta) { m.Scope = "Summary " },
		"a lock twice":      func(m *Meta) { m.Lock = []string{"Summary", "Summary"} },
	} {
		m := goodMeta()
		edit(&m)
		if _, _, err := s.Save(t.Context(), "k", m, pdfBytes("a")); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Save = %v, want ErrInvalid", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(Dir(state), jobFolder("k"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused record touched the disk: %v", err)
	}
	// The bounds themselves are kept: 120 two-byte runes, 20 locks.
	m := goodMeta()
	m.Scope = strings.Repeat("é", MaxNameRunes)
	m.Lock = names(MaxLocks)
	if got, _ := mustSave(t, s, "k", m, pdfBytes("a")); got.Scope != m.Scope || len(got.Lock) != MaxLocks {
		t.Fatalf("a record at the bounds was not kept")
	}
}

func TestReadRefusesAScopeWrittenByHandThatBreaksTheBound(t *testing.T) {
	s, state := newStore(t)
	m := goodMeta()
	m.Scope = "fine"
	mustSave(t, s, "k", m, pdfBytes("a"))
	p := versionFile(filepath.Join(Dir(state), jobFolder("k")), 1, "json")
	raw, _ := os.ReadFile(p)
	os.WriteFile(p, bytes.Replace(raw, []byte(`"scope":"fine"`), []byte(`"scope":"`+strings.Repeat("x", MaxNameRunes+1)+`"`), 1), 0o600)
	if _, _, err := s.Get("k", 1); !errors.Is(err, ErrDamaged) {
		t.Fatalf("Get = %v, want ErrDamaged", err)
	}
}

func TestRekeyKeepsEachRecordsOwnSchemaAndInstruction(t *testing.T) {
	s, state := newStore(t)
	m := goodMeta()
	m.Instruction = "keep me"
	mustSave(t, s, "x:old", goodMeta(), pdfBytes("one"))
	mustSave(t, s, "x:old", m, pdfBytes("two"))
	asSchema1(t, state, "x:old", 1)
	if err := s.Rekey(t.Context(), "x:old", "x:new"); err != nil {
		t.Fatal(err)
	}
	all, err := s.List("x:new")
	if err != nil || len(all) != 2 {
		t.Fatalf("List = %+v, %v", all, err)
	}
	if all[0].Schema != 1 || all[0].Instruction != "" || all[1].Schema != 2 || all[1].Instruction != "keep me" {
		t.Fatalf("after Rekey: v1 = schema %d %q, v2 = schema %d %q", all[0].Schema, all[0].Instruction, all[1].Schema, all[1].Instruction)
	}
}
