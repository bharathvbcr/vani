package main

import (
	"strings"
	"testing"
)

// The Mac app tells a stale save from any other refusal by the reply's code
// (VaniProcess reads it from the same JSON as the error text), never by the
// text: a refusal echoes the owner's entry, so matching text lets an entry
// that spells the stale phrase turn an unrelated refusal into a stale one.
const w5badvStalePhrase = "the exclusions changed since they were read"

// A stale save carries code stale, the code the app matches on.
func TestW5badvStaleRefusalCarriesTheCode(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	setExcludeInput(t, `{"titles": ["intern"]}`)
	mustCode(t, 0, "config", "exclude", "--set")
	pane := w5bRead(t)
	w5bPatch(t, 0, `{"add": {"companies": ["Acme"]}}`)
	setExcludeInput(t, `{"titles": ["intern", "co-op"]}`)
	o, _ := mustCode(t, exitRuntime, "config", "exclude", "--set", "--revision", pane.Revision)
	if r := excludeOut(t, o); r.Code != codeStale {
		t.Fatalf("stale save code = %q, want %q: %+v", r.Code, codeStale, r)
	}
}

// A refusal that is NOT stale must not carry code stale, or the app reads it as
// stale, re-reads, and tells the owner a save "keeps changing". Validation's
// duplicate message echoes the entry with %q, so an entry that is the stale
// phrase itself plants the phrase in an unrelated refusal; the code stays
// runtime whatever the text says.
func TestW5badvOtherRefusalsDoNotCarryTheStaleCode(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	setExcludeInput(t, `{"titles": ["`+w5badvStalePhrase+`", "`+w5badvStalePhrase+`"]}`)
	o, _ := mustCode(t, exitRuntime, "config", "exclude", "--set")
	r := excludeOut(t, o)
	if r.Code == codeStale {
		t.Fatalf("code = stale for a duplicate entry: %+v", r)
	}
	if r.Code != codeRuntime {
		t.Errorf("duplicate entry code = %q, want %q", r.Code, codeRuntime)
	}
}

// Remove and add of the same entry in one patch: the add wins (remove runs
// first), the entry stays, and the entry is not duplicated.
func TestW5badvPatchRemoveAndAddSameEntry(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	setExcludeInput(t, `{"titles": ["intern", "co-op"]}`)
	mustCode(t, 0, "config", "exclude", "--set")
	r := w5bPatch(t, 0, `{"remove": {"titles": ["INTERN"]}, "add": {"titles": ["Intern"]}}`)
	if strings.Join(r.Exclude.Titles, ",") != "co-op,Intern" {
		t.Errorf("titles = %q", r.Exclude.Titles)
	}
}

// Revision is the same for the reply of a save and for a later read, including
// when the file held untrimmed entries (written by hand) that Load keeps.
func TestW5badvRevisionReadEqualsSaveReply(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	setExcludeInput(t, `{"titles": ["  intern  ", "Co-Op"], "locations": ["New  York"]}`)
	o, _ := mustCode(t, 0, "config", "exclude", "--set")
	saved := excludeOut(t, o)
	if now := w5bRead(t); now.Revision != saved.Revision {
		t.Errorf("save reply revision %s != read revision %s", saved.Revision, now.Revision)
	}
}
