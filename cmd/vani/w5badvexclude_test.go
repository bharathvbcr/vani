package main

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/config"
)

// The Mac app tells a stale save from any other refusal by this phrase in the
// error text (ExclusionsClient.staleMarker); the code is dropped on the way.
const w5badvStaleMarker = "the exclusions changed since they were read"

// The Go message must keep carrying the marker the app matches on.
func TestW5badvStaleMessageCarriesTheAppsMarker(t *testing.T) {
	if !strings.Contains(config.ErrStale.Error(), w5badvStaleMarker) {
		t.Fatalf("config.ErrStale = %q no longer contains the marker the app matches", config.ErrStale)
	}
}

// A refusal that is NOT stale must not contain the marker, or the app reads it
// as stale, re-reads, and tells the owner a save "keeps changing". Validation's
// duplicate message echoes the entry with %q, so an entry that is the marker
// text itself plants it in an unrelated refusal.
func TestW5badvOtherRefusalsDoNotCarryTheStaleMarker(t *testing.T) {
	setup(t)
	mustCode(t, 0, "init")
	setExcludeInput(t, `{"titles": ["`+w5badvStaleMarker+`", "`+w5badvStaleMarker+`"]}`)
	o, _ := mustCode(t, exitRuntime, "config", "exclude", "--set")
	r := excludeOut(t, o)
	if r.Code == codeStale {
		t.Fatalf("code = stale for a duplicate entry: %+v", r)
	}
	if strings.Contains(r.Error, w5badvStaleMarker) {
		t.Errorf("a non-stale refusal (code %q) contains the stale marker, so the app misreads it as stale: %q", r.Code, r.Error)
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
