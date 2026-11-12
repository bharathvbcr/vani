package pipeline

import (
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// TagModel appends the tag the shadow annotations read back; a model id that
// could not be read back whole (empty, or holding ';', '=', a space or a
// control character) is not tagged, so no wrong model is ever recorded.
func TestTagModelRoundTripsAndRefusesUnreadableIDs(t *testing.T) {
	cases := map[string]string{
		"backend=escalate":       "backend=escalate;model=claude-haiku-5-5",
		"":                       "model=claude-haiku-5-5",
		"error;backend=escalate": "error;backend=escalate;model=claude-haiku-5-5",
	}
	for detail, want := range cases {
		got := TagModel(detail, "claude-haiku-5-5")
		if got != want {
			t.Errorf("TagModel(%q) = %q, want %q", detail, got, want)
		}
		if m := slotModel(decide.SlotReading{Detail: got}); m != "claude-haiku-5-5" {
			t.Errorf("slotModel(%q) = %q", got, m)
		}
		if b := slotBackend(decide.Answer{}, decide.SlotReading{Detail: got}); strings.Contains(detail, "backend=") && b != "escalate" {
			t.Errorf("the tag broke the backend tag: %q", b)
		}
	}
	for _, bad := range []string{"", "a;b", "a b", "a\tb", "a\x00b", "model=x"} {
		if got := TagModel("backend=escalate", bad); got != "backend=escalate" {
			t.Errorf("TagModel(%q) = %q, want it untagged", bad, got)
		}
	}
	if m := slotModel(decide.SlotReading{Detail: "backend=escalate"}); m != "" {
		t.Errorf("an untagged detail read as model %q", m)
	}
}

// A backfill records the tier's model and the raw text it asked the tier
// about, under the shadow's own keys, and leaves the primary triage's binding
// alone: the tier may read a later version of the posting than the primary did.
func TestRecordShadowsBindsTheModelAndTheTextTheTierRead(t *testing.T) {
	l := openLedger(t)
	r, key := triagedPosting(t, l, "1", posting.Triaged, 0.3)
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t0.Add(2 * time.Minute), Key: key, Status: posting.Triaged,
		Annotations: map[string]string{"triage.desc_sha256": "primary-binding"}}); err != nil {
		t.Fatal(err)
	}
	read := rec(t, l, key).Posting
	read.Title, read.Location = "Engineer 1 (as the tier read it)", "Remote, US"
	read.Description += " The tier read this version."
	sh := escalated("hybrid")
	sh.Detail = TagModel(sh.Detail, "claude-haiku-5-5")
	n, err := r.RecordShadows(ctx, read, escBackend, map[string]string{"remote": "remote"}, map[string]decide.SlotReading{"remote": sh})
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got := rec(t, l, key).Annotations
	want := map[string]string{
		"triage.remote.shadow_model":    "claude-haiku-5-5",
		"triage.shadow_desc_sha256":     rawSHA(read.Description),
		"triage.shadow_title_sha256":    rawSHA(read.Title),
		"triage.shadow_location_sha256": rawSHA(read.Location),
		"triage.desc_sha256":            "primary-binding",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	// Nothing recorded (a repeat): no event, so no binding is written either.
	before := rec(t, l, key)
	if n, err := r.RecordShadows(ctx, read, escBackend, map[string]string{"remote": "remote"}, map[string]decide.SlotReading{"remote": sh}); n != 0 || err != nil {
		t.Fatalf("repeat: n=%d err=%v", n, err)
	}
	if !rec(t, l, key).LastEvent.Equal(before.LastEvent) {
		t.Fatal("a repeat that recorded nothing wrote an event")
	}
}

// A posting triaged again forgets the shadow binding a backfill wrote, with
// the rest of the earlier triage.
func TestRetriageForgetsTheBackfillsBinding(t *testing.T) {
	l := openLedger(t)
	r, key := triagedPosting(t, l, "1", posting.Triaged, 0.3)
	sh := escalated("hybrid")
	sh.Detail = TagModel(sh.Detail, "claude-haiku-5-5")
	if n, err := r.RecordShadows(ctx, rec(t, l, key).Posting, escBackend, map[string]string{"remote": "remote"}, map[string]decide.SlotReading{"remote": sh}); n != 1 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	// The description changes: the earlier triage is stale and the posting is
	// live again.
	changed := ghPost(t, "1", "Lead the payments platform team in Berlin, on site five days a week.")
	if _, err := Reconcile(ctx, l, board, []posting.Posting{changed}, t0.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if st := rec(t, l, key).Status; st != posting.Live {
		t.Fatalf("status %s after a description change, want live", st)
	}
	tr := &postingTriager{ans: okAns("remote", 0.99)}
	if err := newRunner(l, tr, Funnel{ModelID: "m1"}).Advance(ctx, key); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, key).Annotations
	for _, k := range []string{"triage.shadow_desc_sha256", "triage.shadow_title_sha256", "triage.shadow_location_sha256", "triage.remote.shadow_model"} {
		if v := got[k]; v != "" {
			t.Errorf("%s survived a new triage: %q", k, v)
		}
	}
	if got["triage.desc_sha256"] != rawSHA(rec(t, l, key).Posting.Description) {
		t.Errorf("the new triage is not bound: %q", got["triage.desc_sha256"])
	}
}
