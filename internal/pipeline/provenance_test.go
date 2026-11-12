package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// Provenance a later training export reads: which raw text a triage reading was
// asked about, and which model made an escalation's shadow reading. The keys are
// spelled out here, not taken from the ledger constants, so a rename fails the
// test: Lappi-decision's job_pairs.rs parses these names.

func rawSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// postingTriager answers ans and keeps every posting it was asked about.
type postingTriager struct {
	ans decide.Answer
	got []posting.Posting
}

func (p *postingTriager) Triage(_ context.Context, q posting.Posting) (decide.Answer, error) {
	p.got = append(p.got, q)
	return p.ans, nil
}

// A triage that asked the backends records the sha256 of the raw title,
// location and description it asked about, unnormalised (whitespace and case
// count), on the triage event itself, whether the reading keeps or drops it.
func TestFreshTriageBindsTheRawTextItRead(t *testing.T) {
	for name, ans := range map[string]decide.Answer{
		"kept":    okAns("remote", 0.99),
		"dropped": okAns("onsite", 0.99),
	} {
		t.Run(name, func(t *testing.T) {
			l := openLedger(t)
			p := ghPost(t, "1", "  Build   inference\tsystems.\n\nFully REMOTE.  ")
			p.Title, p.Location = "ML Engineer  (Inference)", "Remote — EU"
			reconcileFunnel(t, l, p)
			tr := &postingTriager{ans: ans}
			r := newRunner(l, tr, Funnel{ModelID: "m1"})
			if err := r.Advance(ctx, p.Key); err != nil {
				t.Fatal(err)
			}
			if len(tr.got) != 1 {
				t.Fatalf("%d triage calls, want 1", len(tr.got))
			}
			asked := tr.got[0]
			got := rec(t, l, p.Key)
			if name == "dropped" && got.Status != posting.Dropped {
				t.Fatalf("status %s, want dropped", got.Status)
			}
			want := map[string]string{
				"triage.desc_sha256":     rawSHA(asked.Description),
				"triage.title_sha256":    rawSHA(asked.Title),
				"triage.location_sha256": rawSHA(asked.Location),
			}
			for k, v := range want {
				if got.Annotations[k] != v {
					t.Errorf("%s = %q, want %q", k, got.Annotations[k], v)
				}
			}
			if got.Annotations["triage.desc_sha256"] != rawSHA(p.Description) || got.Annotations["triage.desc_sha256"] == p.DescriptionHash() {
				t.Errorf("triage.desc_sha256 is not the raw hash of the listed description")
			}
		})
	}
}

// A reading copied from a matching posting was not asked about this posting's
// text (the match is on the normalised description), so the copy carries no
// binding of its own; the asked posting carries its own.
func TestInheritedCopyCarriesNoTextBinding(t *testing.T) {
	l := openLedger(t)
	a := dupPost(t, "1", "Remote", longDesc)
	b := dupPost(t, "2", "Remote", strings.ReplaceAll(longDesc, " ", "  "))
	if a.DescriptionHash() != b.DescriptionHash() || a.Description == b.Description {
		t.Fatal("fixture: the two descriptions must match only after normalisation")
	}
	reconcileFunnel(t, l, a, b)
	tr := &postingTriager{ans: okAns("remote", 0.99)}
	r := newRunner(l, tr, Funnel{Cache: NewReadCache(), ModelID: "m1"})
	for _, k := range []posting.Key{a.Key, b.Key} {
		if err := r.Advance(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if len(tr.got) != 1 {
		t.Fatalf("%d triage calls, want 1 (b inherits)", len(tr.got))
	}
	ra, rb := rec(t, l, a.Key), rec(t, l, b.Key)
	if ra.Annotations["triage.desc_sha256"] != rawSHA(a.Description) {
		t.Errorf("the asked posting's binding = %q", ra.Annotations["triage.desc_sha256"])
	}
	if rb.Annotations[AnnTriageInheritedFrom] != string(a.Key) {
		t.Fatalf("b did not inherit: %v", rb.Annotations)
	}
	for _, k := range []string{"triage.desc_sha256", "triage.title_sha256", "triage.location_sha256"} {
		if v := rb.Annotations[k]; v != "" {
			t.Errorf("the copy carries %s = %q", k, v)
		}
	}
}

// No backend read the description when the posting was dropped on its title or
// skipped as too short, so neither event carries a text binding.
func TestNoBindingWhereNoBackendRead(t *testing.T) {
	l := openLedger(t)
	short := ghPost(t, "1", "  ")
	titled := ghPost(t, "2", longDesc)
	titled.Title = "Senior Staff Engineer"
	reconcileFunnel(t, l, short, titled)
	tr := &postingTriager{ans: okAns("remote", 0.99)}
	r := newRunner(l, tr, Funnel{ModelID: "m1"})
	r.Policy.ReadTitle = func(title string) []TitleReading {
		if strings.HasPrefix(title, "Senior Staff") {
			return []TitleReading{{Slot: "remote", Value: "onsite"}}
		}
		return nil
	}
	for _, k := range []posting.Key{short.Key, titled.Key} {
		if err := r.Advance(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if len(tr.got) != 0 {
		t.Fatalf("a backend was asked %d time(s)", len(tr.got))
	}
	for _, k := range []posting.Key{short.Key, titled.Key} {
		got := rec(t, l, k)
		for _, key := range []string{"triage.desc_sha256", "triage.title_sha256", "triage.location_sha256"} {
			if v, has := got.Annotations[key]; has && v != "" {
				t.Errorf("%s (%s): %s = %q", k, got.Status, key, v)
			}
		}
	}
}

// The escalation tier tags its readings with the model that made them
// (TagModel); a triage records it as triage.<slot>.shadow_model. A shadow with
// no such tag (another shadow backend) records none.
func TestShadowModelIsRecordedFromTheTiersTag(t *testing.T) {
	primary := map[string]decide.SlotReading{"remote": tagged(jev("remote", 0.3), "d1-ultra"), "level": tagged(jev("senior", 0.3), "d1-ultra")}
	ann := triageAnnotations(decide.Answer{Backend: "d1-ultra", Slots: primary, Shadow: map[string]decide.SlotReading{
		"remote": {Reading: decide.Answered, Value: "hybrid", Detail: "backend=escalate;model=claude-sonnet-5-5"},
		"level":  {Reading: decide.Answered, Value: "junior", Detail: "backend=lappi"},
	}})
	if ann["triage.remote.shadow_model"] != "claude-sonnet-5-5" {
		t.Errorf("triage.remote.shadow_model = %q (all: %v)", ann["triage.remote.shadow_model"], ann)
	}
	if v, has := ann["triage.level.shadow_model"]; has {
		t.Errorf("an untagged shadow recorded a model %q", v)
	}
	// An outage reading from the tier still names the model that was asked.
	out := triageAnnotations(decide.Answer{Backend: "d1-ultra", Slots: primary, Shadow: map[string]decide.SlotReading{
		"remote": {Reading: decide.Unavailable, Detail: "error;backend=escalate;model=claude-haiku-5-5"},
	}})
	if out["triage.remote.shadow_reading"] != "unavailable" || out["triage.remote.shadow_model"] != "claude-haiku-5-5" {
		t.Errorf("outage: %v", out)
	}
}
