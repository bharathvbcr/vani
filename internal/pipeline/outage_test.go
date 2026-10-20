package pipeline

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// A backend that is down or failing (a 429, a 502, a deadline, an unusable
// body, a spent budget) has not read the posting. Only a real reading
// (Answered or Abstained) may turn into a triage verdict; an outage keeps the
// posting Live so the next run asks again. These tests characterise the five
// answer shapes: the three outage shapes keep the posting Live, and the two
// shapes that hold a real reading are unchanged.

func slots(m map[string]decide.Reading) decide.Answer {
	a := decide.Answer{Backend: "d1-ultra+llm", Slots: map[string]decide.SlotReading{}}
	for name, rd := range m {
		sr := decide.SlotReading{Reading: rd, Detail: "x"}
		if rd == decide.Answered {
			sr.Value = "remote"
		}
		a.Slots[name] = sr
	}
	return a
}

func TestOutageAnswerKeepsThePostingLive(t *testing.T) {
	cases := []struct {
		name string
		ans  decide.Answer
	}{
		{"every slot failed", slots(map[string]decide.Reading{"remote": decide.Failed, "level": decide.Failed})},
		{"every slot not asked", slots(map[string]decide.Reading{"remote": decide.NotAsked, "level": decide.NotAsked})},
		{"failed beside unavailable", slots(map[string]decide.Reading{"remote": decide.Failed, "level": decide.Unavailable})},
		{"failed, unavailable and not asked", slots(map[string]decide.Reading{"remote": decide.Failed, "level": decide.Unavailable, "cat": decide.NotAsked})},
		{"one failed slot", slots(map[string]decide.Reading{"remote": decide.Failed})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, l, k := runnerWith(t, c.ans, nil)
			before := rec(t, l, k)
			err := r.Advance(ctx, k)
			if !errors.Is(err, ErrTriageUnavailable) {
				t.Fatalf("err = %v, want ErrTriageUnavailable", err)
			}
			if got := rec(t, l, k); !reflect.DeepEqual(got, before) {
				t.Fatalf("an outage wrote to the ledger:\n got %+v\nwant %+v", got, before)
			}
		})
	}
}

// Partial answers and real abstentions are readings: they keep the existing
// Unsure-with-annotations behaviour, and a verdict is written.
func TestRealReadingsAreStillTriaged(t *testing.T) {
	cases := []struct {
		name string
		ans  decide.Answer
	}{
		{"answered beside failed", slots(map[string]decide.Reading{"remote": decide.Failed, "level": decide.Answered})},
		{"answered remote beside failed level", slots(map[string]decide.Reading{"remote": decide.Answered, "level": decide.Failed})},
		{"answered beside not asked", slots(map[string]decide.Reading{"remote": decide.NotAsked, "level": decide.Answered})},
		{"every slot abstained", slots(map[string]decide.Reading{"remote": decide.Abstained, "level": decide.Abstained})},
		{"abstained beside failed", slots(map[string]decide.Reading{"remote": decide.Abstained, "level": decide.Failed})},
		{"refused", slots(map[string]decide.Reading{"remote": decide.Refused, "level": decide.Refused})},
		{"refused beside failed", slots(map[string]decide.Reading{"remote": decide.Refused, "level": decide.Failed})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, l, k := runnerWith(t, c.ans, nil)
			r.SkipScoring, r.SkipExplain = true, true
			if err := r.Advance(ctx, k); err != nil {
				t.Fatalf("a real reading failed to advance: %v", err)
			}
			got := rec(t, l, k)
			for name, sr := range c.ans.Slots {
				if got.Annotations["triage."+name+".reading"] != string(sr.Reading) {
					t.Errorf("triage.%s.reading = %q, want %q", name, got.Annotations["triage."+name+".reading"], sr.Reading)
				}
			}
		})
	}
}

// Partial: the Answered slot is kept and the failed one is flagged unsure.
func TestAnsweredBesideFailedKeepsPartialAnswers(t *testing.T) {
	ans := slots(map[string]decide.Reading{"remote": decide.Failed, "level": decide.Answered})
	r, l, k := runnerWith(t, ans, nil)
	r.SkipScoring, r.SkipExplain = true, true
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, k)
	if got.Annotations["triage.unsure"] != `slot "remote" failed` {
		t.Fatalf("triage.unsure = %q", got.Annotations["triage.unsure"])
	}
	if got.Annotations["triage.level"] != "remote" || got.Annotations["triage.level.reading"] != "answered" {
		t.Fatalf("the answered slot was lost: %v", got.Annotations)
	}
}

// An answer with no slots at all is malformed, not an outage: the router fills
// every slot, so an empty map never means "backends were down". It stays an
// unsure triage (the rule's slot is missing from the answer).
func TestEmptyAnswerIsNotAnOutage(t *testing.T) {
	for name, ans := range map[string]decide.Answer{
		"zero answer":   {},
		"empty slots":   {Backend: "llm", Slots: map[string]decide.SlotReading{}},
		"nil slots":     {Backend: "llm", Slots: nil},
		"only a shadow": {Backend: "llm", Shadow: map[string]decide.SlotReading{"remote": {Reading: decide.Answered, Value: "remote"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if noReading(ans) {
				t.Fatal("noReading(empty) = true")
			}
			r, l, k := runnerWith(t, ans, nil)
			r.SkipScoring, r.SkipExplain = true, true
			if err := r.Advance(ctx, k); err != nil {
				t.Fatal(err)
			}
			if got := rec(t, l, k); got.Annotations["triage.unsure"] == "" {
				t.Fatalf("status %s, annotations %v: want an unsure triage", got.Status, got.Annotations)
			}
		})
	}
}

// A shadow reading is informational: a shadow that answered cannot turn an
// outage on the primary into a reading.
func TestShadowAnswerDoesNotMakeAnOutageARead(t *testing.T) {
	ans := slots(map[string]decide.Reading{"remote": decide.Failed, "level": decide.Failed})
	ans.Shadow = map[string]decide.SlotReading{"remote": {Reading: decide.Answered, Value: "remote"}}
	r, l, k := runnerWith(t, ans, nil)
	before := rec(t, l, k)
	if err := r.Advance(ctx, k); !errors.Is(err, ErrTriageUnavailable) {
		t.Fatalf("err = %v, want ErrTriageUnavailable", err)
	}
	if got := rec(t, l, k); !reflect.DeepEqual(got, before) {
		t.Fatalf("status %s: the shadow made an outage a verdict", got.Status)
	}
}

// noReading is a positive check on the three outage readings: anything else,
// an unknown value included, is a reading the engine must not guess about.
func TestNoReadingTable(t *testing.T) {
	r := func(rd decide.Reading) decide.SlotReading { return decide.SlotReading{Reading: rd} }
	cases := []struct {
		name string
		ans  decide.Answer
		want bool
	}{
		{"no slots", decide.Answer{}, false},
		{"unavailable", decide.Answer{Slots: map[string]decide.SlotReading{"a": r(decide.Unavailable)}}, true},
		{"failed", decide.Answer{Slots: map[string]decide.SlotReading{"a": r(decide.Failed)}}, true},
		{"not asked", decide.Answer{Slots: map[string]decide.SlotReading{"a": r(decide.NotAsked)}}, true},
		{"all three", decide.Answer{Slots: map[string]decide.SlotReading{"a": r(decide.Unavailable), "b": r(decide.Failed), "c": r(decide.NotAsked)}}, true},
		{"answered", decide.Answer{Slots: map[string]decide.SlotReading{"a": r(decide.Answered)}}, false},
		{"abstained", decide.Answer{Slots: map[string]decide.SlotReading{"a": r(decide.Abstained)}}, false},
		{"refused", decide.Answer{Slots: map[string]decide.SlotReading{"a": r(decide.Refused)}}, false},
		{"failed with abstained", decide.Answer{Slots: map[string]decide.SlotReading{"a": r(decide.Failed), "b": r(decide.Abstained)}}, false},
		{"empty reading", decide.Answer{Slots: map[string]decide.SlotReading{"a": r("")}}, false},
		{"unknown reading", decide.Answer{Slots: map[string]decide.SlotReading{"a": r("garbage")}}, false},
		{"failed with unknown", decide.Answer{Slots: map[string]decide.SlotReading{"a": r(decide.Failed), "b": r("garbage")}}, false},
	}
	for _, c := range cases {
		if got := noReading(c.ans); got != c.want {
			t.Errorf("%s: noReading = %v, want %v", c.name, got, c.want)
		}
	}
}

// One Advance asks the backends once. A backend that fails for good costs one
// call per posting per Advance and writes nothing, so the bound on a run is
// the per-run caps in cmd/vani (maxConsecutiveUnread stops the phase on the
// ErrTriageUnavailable this returns), not anything in here.
func TestPermanentOutageAsksOncePerAdvanceAndWritesNothing(t *testing.T) {
	ans := slots(map[string]decide.Reading{"remote": decide.Failed, "level": decide.NotAsked})
	r, l, k := runnerWith(t, ans, nil)
	ft := r.Triager.(*fakeTriager)
	before := rec(t, l, k)
	const advances = 50
	for i := range advances {
		if err := r.Advance(ctx, k); !errors.Is(err, ErrTriageUnavailable) {
			t.Fatalf("advance %d: err = %v, want ErrTriageUnavailable", i, err)
		}
	}
	if ft.calls != advances {
		t.Fatalf("%d triage calls for %d advances, want one each", ft.calls, advances)
	}
	if got := rec(t, l, k); !reflect.DeepEqual(got, before) {
		t.Fatalf("the ledger record changed across %d outage advances:\n got %+v\nwant %+v", advances, got, before)
	}
}

// cancelTriager cancels the run while it is asking, then reports every slot
// failed, as a backend whose call died with the context does.
type cancelTriager struct{ cancel context.CancelFunc }

func (c cancelTriager) Triage(context.Context, posting.Posting) (decide.Answer, error) {
	c.cancel()
	return slots(map[string]decide.Reading{"remote": decide.Failed}), nil
}

// A run cancelled mid-advance never leaves a triage verdict behind.
func TestCancelledMidAdvanceWritesNoVerdict(t *testing.T) {
	r, l, k := runnerWith(t, decide.Answer{}, nil)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.Triager = cancelTriager{cancel}
	before := rec(t, l, k)
	if err := r.Advance(cctx, k); err == nil {
		t.Fatal("Advance succeeded on a cancelled run")
	}
	if got := rec(t, l, k); !reflect.DeepEqual(got, before) {
		t.Fatalf("a cancelled advance wrote to the ledger: %+v", got)
	}
}

// An outage keeps the posting Live, so the next run, once the backend is back,
// reads it and carries it on: nothing about the earlier outage sticks.
func TestPostingReadsAfterTheOutageEnds(t *testing.T) {
	r, l, k := runnerWith(t, slots(map[string]decide.Reading{"remote": decide.Failed}), nil)
	r.SkipScoring, r.SkipExplain = true, true
	if err := r.Advance(ctx, k); !errors.Is(err, ErrTriageUnavailable) {
		t.Fatalf("err = %v", err)
	}
	r.Triager = &fakeTriager{ans: decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99)}}}
	if err := r.Advance(ctx, k); err != nil {
		t.Fatal(err)
	}
	got := rec(t, l, k)
	if got.Status != posting.Explained || got.Annotations["triage.remote.reading"] != "answered" {
		t.Fatalf("status %s, annotations %v", got.Status, got.Annotations)
	}
}

// parkAsUnsure writes what the runner used to write for ans: the verdict the
// policy gave it, then scoring skipped. It stands in for a record already in
// the live ledger.
func parkAsUnsure(t *testing.T, r *Runner, k posting.Key, ans decide.Answer) {
	t.Helper()
	st, reason := r.Policy.Evaluate(ans)
	ann := triageAnnotations(ans)
	ann[ledger.AnnLCA] = "badge kept"
	if st == posting.Unsure {
		ann["triage.unsure"] = reason
	}
	if err := r.emit(ctx, k, st, reason, ann); err != nil {
		t.Fatal(err)
	}
	if err := r.emit(ctx, k, posting.Scored, "scoring disabled", nil); err != nil {
		t.Fatal(err)
	}
}

func TestUnreadCoversEveryOutageReading(t *testing.T) {
	cases := []struct {
		name string
		ann  map[string]string
		want bool
	}{
		{"all failed", map[string]string{"triage.a.reading": "failed", "triage.b.reading": "failed"}, true},
		{"all not asked", map[string]string{"triage.a.reading": "not_asked", "triage.b.reading": "not_asked"}, true},
		{"failed, unavailable, not asked", map[string]string{"triage.a.reading": "failed", "triage.b.reading": "unavailable", "triage.c.reading": "not_asked"}, true},
		{"one failed slot", map[string]string{"triage.a.reading": "failed", "triage.backend": "llm"}, true},
		{"failed with answered", map[string]string{"triage.a.reading": "failed", "triage.b.reading": "answered"}, false},
		{"not asked with answered", map[string]string{"triage.a.reading": "not_asked", "triage.b.reading": "answered"}, false},
		{"failed with abstained", map[string]string{"triage.a.reading": "failed", "triage.b.reading": "abstained"}, false},
		{"all abstained", map[string]string{"triage.a.reading": "abstained"}, false},
		{"failed with refused", map[string]string{"triage.a.reading": "failed", "triage.b.reading": "refused"}, false},
		{"empty value", map[string]string{"triage.a.reading": ""}, false},
		{"malformed value", map[string]string{"triage.a.reading": "garbage"}, false},
		{"case differs", map[string]string{"triage.a.reading": "Failed"}, false},
		{"failed with a malformed one", map[string]string{"triage.a.reading": "failed", "triage.b.reading": "fail"}, false},
		{"failed shadow beside no primary reading", map[string]string{"triage.a.shadow_reading": "failed"}, false},
		{"answered with a failed shadow", map[string]string{"triage.a.reading": "answered", "triage.a.shadow_reading": "failed"}, false},
		{"no annotations", nil, false},
	}
	for _, c := range cases {
		if got := Unread(ledger.Record{Annotations: c.ann}); got != c.want {
			t.Errorf("%s: Unread(%v) = %v, want %v", c.name, c.ann, got, c.want)
		}
	}
}

// Records already parked as unsure on an outage answer go back to Live; the
// ones that hold a real reading, and the ones the owner has seen, do not.
func TestRetriageUnreadBackfillsFailedAndNotAsked(t *testing.T) {
	l := openLedger(t)
	t1 := t0
	mk := func(id string, ans decide.Answer, digest bool) posting.Key {
		p := ghPost(t, id, "jd "+id)
		if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t1, Key: p.Key, Status: posting.Live, Posting: &p}); err != nil {
			t.Fatal(err)
		}
		r := &Runner{Ledger: l, Policy: policy, Now: func() time.Time { return t1.Add(time.Minute) }}
		parkAsUnsure(t, r, p.Key, ans)
		if digest {
			if err := r.MarkDigested(ctx, []posting.Key{p.Key}); err != nil {
				t.Fatal(err)
			}
		}
		return p.Key
	}
	stuck := []posting.Key{
		mk("1", slots(map[string]decide.Reading{"remote": decide.Failed, "level": decide.Failed}), false),
		mk("2", slots(map[string]decide.Reading{"remote": decide.NotAsked, "level": decide.NotAsked}), false),
		mk("3", slots(map[string]decide.Reading{"remote": decide.Failed, "level": decide.Unavailable, "cat": decide.NotAsked}), false),
	}
	kept := []posting.Key{
		mk("4", slots(map[string]decide.Reading{"remote": decide.Failed, "level": decide.Answered}), false),
		mk("5", slots(map[string]decide.Reading{"remote": decide.Abstained}), false),
		mk("6", slots(map[string]decide.Reading{"remote": decide.Refused}), false),
		mk("7", slots(map[string]decide.Reading{"remote": decide.Failed}), true), // seen in a digest
	}
	// A malformed reading value is not guessed at.
	bad := ghPost(t, "8", "jd 8")
	if err := l.Append(ctx, ledger.Event{V: ledger.SchemaVersion, At: t1, Key: bad.Key, Status: posting.Unsure, Posting: &bad,
		Annotations: map[string]string{"triage.remote.reading": "fail", "triage.unsure": "x"}}); err != nil {
		t.Fatal(err)
	}
	kept = append(kept, bad.Key)

	keys, err := RetriageUnread(ctx, l, t1.Add(time.Hour), true)
	if err != nil || !reflect.DeepEqual(keys, stuck) {
		t.Fatalf("dry run = %v, %v; want %v", keys, err, stuck)
	}
	for _, k := range stuck {
		if rec(t, l, k).Status != posting.Scored {
			t.Fatalf("a dry run changed %s", k)
		}
	}
	keys, err = RetriageUnread(ctx, l, t1.Add(time.Hour), false)
	if err != nil || !reflect.DeepEqual(keys, stuck) {
		t.Fatalf("retriage = %v, %v; want %v", keys, err, stuck)
	}
	for _, k := range stuck {
		got := rec(t, l, k)
		if got.Status != posting.Live || got.Annotations[ledger.AnnLCA] == "" {
			t.Errorf("%s: status %s, annotations %v", k, got.Status, got.Annotations)
		}
		for key := range got.Annotations {
			if len(key) > 7 && key[:7] == "triage." {
				t.Errorf("%s: triage annotation %s survived", k, key)
			}
		}
	}
	for _, k := range kept {
		if got := rec(t, l, k); got.Status == posting.Live {
			t.Errorf("%s was retriaged though it holds a real reading or was digested", k)
		}
	}
	if again, _ := RetriageUnread(ctx, l, t1.Add(2*time.Hour), false); len(again) != 0 {
		t.Errorf("a second pass found %v", again)
	}

	// The reopened record is read again by the next run.
	r := &Runner{Ledger: l, Policy: policy, SkipScoring: true, SkipExplain: true,
		Triager: &fakeTriager{ans: decide.Answer{Backend: "d1-ultra", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.99)}}},
		Now:     func() time.Time { return t1.Add(3 * time.Hour) }}
	if err := r.Advance(ctx, stuck[0]); err != nil {
		t.Fatal(err)
	}
	if got := rec(t, l, stuck[0]); got.Annotations["triage.remote.reading"] != "answered" {
		t.Fatalf("not re-read: %v", got.Annotations)
	}
}

// A cancelled backfill stops and reports only what it wrote.
func TestRetriageUnreadBackfillStopsOnCancel(t *testing.T) {
	l := openLedger(t)
	for _, id := range []string{"1", "2"} {
		p := ghPost(t, id, "d")
		if err := l.Append(ctx, ledger.Event{V: 1, At: t0, Key: p.Key, Status: posting.Unsure, Posting: &p,
			Annotations: map[string]string{"triage.remote.reading": "failed"}}); err != nil {
			t.Fatal(err)
		}
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	keys, err := RetriageUnread(cctx, l, t0.Add(time.Hour), false)
	if !errors.Is(err, context.Canceled) || len(keys) != 0 {
		t.Fatalf("keys %v, err %v", keys, err)
	}
	for _, r := range l.All() {
		if r.Status != posting.Unsure {
			t.Fatalf("%s moved to %s", r.Key, r.Status)
		}
	}
}
