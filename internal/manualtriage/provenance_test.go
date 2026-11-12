package manualtriage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withProvenance is reading() carrying the teacher store's provenance fields.
func withProvenance(k string) Reading {
	r := reading(k, t0)
	r.TextSHA256, r.FixtureSHA, r.PromptHash = hash('c'), hash('f'), hash('0')
	return r
}

// The fields are written under the teacher store's names (text_sha256,
// fixture_sha, prompt_hash), read back, and carried through a compaction; the
// store's own text and prompt are unchanged.
func TestProvenanceRoundTripsUnderTheTeacherNames(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	want := withProvenance("gh:acme:1")
	must(t, Set(dir, want))
	raw, err := os.ReadFile(file(dir))
	if err != nil {
		t.Fatal(err)
	}
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &line); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"text": want.Text, "prompt": want.Prompt,
		"text_sha256": want.TextSHA256, "fixture_sha": want.FixtureSHA, "prompt_hash": want.PromptHash,
	} {
		if line[k] != v {
			t.Errorf("stored %s = %v, want %s", k, line[k], v)
		}
	}
	check := func(stage string) {
		m, err := Read(dir)
		if err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		got := m["gh:acme:1"]
		if got.TextSHA256 != want.TextSHA256 || got.FixtureSHA != want.FixtureSHA || got.PromptHash != want.PromptHash ||
			got.Text != want.Text || got.Prompt != want.Prompt {
			t.Fatalf("%s: read back %+v", stage, got)
		}
	}
	check("append")
	m, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compact(file(dir), m); err != nil {
		t.Fatal(err)
	}
	check("compaction")
}

// A line written before the fields existed reads, and compacts, without them:
// nothing is made up for an old reading.
func TestOldLinesWithoutProvenanceStillRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	must(t, Set(dir, reading("gh:acme:1", t0)))
	m, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compact(file(dir), m); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(file(dir))
	for _, k := range []string{"text_sha256", "fixture_sha", "prompt_hash"} {
		if strings.Contains(string(raw), `"`+k+`"`) {
			t.Errorf("an old reading gained %s: %s", k, raw)
		}
	}
}

// A field that is present must be a sha256; a clear line carries none of them.
func TestProvenanceFieldsAreCheckedWhenPresent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "retriage")
	for name, mut := range map[string]func(*Reading){
		"text_sha256 short":    func(r *Reading) { r.TextSHA256 = "abc" },
		"text_sha256 long":     func(r *Reading) { r.TextSHA256 = hash('a') + "a" },
		"fixture_sha not hex":  func(r *Reading) { r.FixtureSHA = strings.Repeat("g", 64) },
		"prompt_hash with\nnl": func(r *Reading) { r.PromptHash = hash('a')[:63] + "\n" },
	} {
		r := withProvenance("gh:acme:1")
		mut(&r)
		if err := Set(dir, r); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", name, err)
		}
	}
	for _, k := range []string{"text_sha256", "fixture_sha", "prompt_hash"} {
		d := filepath.Join(t.TempDir(), "retriage")
		must(t, os.MkdirAll(d, 0o700))
		line := fmt.Sprintf(`{"v":1,"at":"2026-10-24T10:00:00Z","key":"gh:acme:1","op":"clear","%s":"%s"}`+"\n", k, hash('a'))
		must(t, os.WriteFile(file(d), []byte(line+line), 0o600))
		if _, err := Read(d); !errors.Is(err, ErrCorrupt) {
			t.Errorf("a clear line carrying %s: %v", k, err)
		}
	}
}

// A full store (MaxEntries readings, each with every field and a long key)
// still fits the file bound with room to spare, so the fields do not make a
// store fill by bytes before it reaches its entry cap: a reading refused then
// would already have been paid for.
func TestFullStoreWithProvenanceFitsTheFileBound(t *testing.T) {
	var buf bytes.Buffer
	for i := range MaxEntries {
		// 96-byte keys: longer than a board key usually is.
		r := withProvenance(fmt.Sprintf("wd:acme-corporation.wd5.myworkdayjobs.com/External_Careers_Site:JR-%027d", i))
		r.Model = "claude-sonnet-5-5"
		buf.Write(lineOf(t, setEvent(r)))
	}
	if buf.Len() > MaxFileBytes {
		t.Fatalf("%d readings with provenance take %d bytes, over the %d bound", MaxEntries, buf.Len(), MaxFileBytes)
	}
	t.Logf("%d readings with provenance: %d bytes (%d per line) of %d", MaxEntries, buf.Len(), buf.Len()/MaxEntries, MaxFileBytes)
}
