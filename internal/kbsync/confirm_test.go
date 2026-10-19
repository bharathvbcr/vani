package kbsync

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/resume"
)

// Synthetic DevPrism blocks: a locked bullet whose wording rkb would replace,
// which DevPrism gates (tools_career.rs:113-125). The text is made up.
func lockedBlock(id, text string) string {
	bullet := map[string]string{"alpha-job": "b1", "beta-proj": "b3"}[id]
	return fmt.Sprintf(`{"id":%q,"kind":"work","title":"E","org":"A","dateRange":{"start":"x","end":null},"personas":[],"domains":[],"skills":[],"seniorityLevel":"ic","updatedAt":"1",
	  "facts":[{"id":"f1","text":"synthetic fact","createdAt":"1"}],
	  "bullets":[{"id":"%s/%s","canonical":%q,"variants":{},"metrics":[],"evidenceRefs":[],"locked":true}]}`, id, id, bullet, text)
}

// confirmRun plans against the fake and applies with confirmer.
func confirmRun(t *testing.T, f *fakeDevPrism, st *SyncState, confirmer Confirmer) (SyncPlan, Result, error) {
	t.Helper()
	cur, err := FetchState(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	p := Plan(fixtureResolved(t), cur, *st, fixedNow)
	res, err := Apply(context.Background(), f, p, st, confirmer)
	return p, res, err
}

type shown struct {
	id   string
	loss Loss
}

func recordAll(into *[]shown, yes func(id string) bool) Confirmer {
	return func(id string, l Loss) bool {
		*into = append(*into, shown{id, l})
		return yes(id)
	}
}

func always(string) bool { return true }

// confirmCalls returns the upserts that carried a request_state.
func confirmCalls(t *testing.T, f *fakeDevPrism) []map[string]json.RawMessage {
	t.Helper()
	var out []map[string]json.RawMessage
	for _, c := range f.upserts() {
		var a map[string]json.RawMessage
		if err := json.Unmarshal(c.Args, &a); err != nil {
			t.Fatal(err)
		}
		if _, ok := a["request_state"]; ok {
			out = append(out, a)
		}
	}
	return out
}

func outcome(res Result, id string) Outcome {
	for _, o := range res.Outcomes {
		if o.ID == id {
			return o
		}
	}
	return Outcome{}
}

func TestConfirmAccepted(t *testing.T) {
	f := newFake()
	f.seed(t, lockedBlock("alpha-job", "synthetic locked wording"))
	st := NewSyncState(filepath.Join(t.TempDir(), "s.json"))
	var seen []shown
	_, res, err := confirmRun(t, f, st, recordAll(&seen, always))
	if err != nil {
		t.Fatal(err)
	}
	if res.Confirmed != 1 || res.Added != 2 || res.NeedsConfirmation != 0 {
		t.Fatalf("result %+v", res)
	}
	if o := outcome(res, "alpha-job"); o.Status != StatusConfirmed {
		t.Fatalf("alpha-job %+v", o)
	}
	// Shown first, and exactly: the one locked bullet, with its old text.
	if len(seen) != 1 || seen[0].id != "alpha-job" {
		t.Fatalf("shown %+v", seen)
	}
	want := Loss{LockedRewritten: []LostItem{{ID: "alpha-job/b1", Text: "synthetic locked wording"}}}
	if fmt.Sprint(seen[0].loss) != fmt.Sprint(want) {
		t.Fatalf("shown loss %+v, want %+v", seen[0].loss, want)
	}
	// One confirmation, for the block DevPrism asked about, with its own
	// requestState, the same block bytes, and confirm:true.
	cc := confirmCalls(t, f)
	if len(cc) != 1 {
		t.Fatalf("%d confirmations sent", len(cc))
	}
	var sentState string
	if err := json.Unmarshal(cc[0]["request_state"], &sentState); err != nil || sentState != f.issued[0] {
		t.Fatalf("request_state %s, issued %q", cc[0]["request_state"], f.issued[0])
	}
	if string(cc[0]["input_responses"]) != `{"confirm":true}` {
		t.Fatalf("input_responses %s", cc[0]["input_responses"])
	}
	var first []byte
	for _, b := range f.blockArg {
		if bytes.Contains(b, []byte(`"id":"alpha-job"`)) {
			if first == nil {
				first = b
			} else if !bytes.Equal(first, b) {
				t.Fatalf("confirmation resent a different block:\nfirst:   %s\nconfirm: %s", first, b)
			}
		}
	}
	if got := f.blocks["alpha-job"]["bullets"].([]any)[0].(map[string]any)["canonical"]; got == "synthetic locked wording" {
		t.Fatal("DevPrism still holds the old wording")
	}
	if _, ok := st.Blocks["alpha-job"]; !ok {
		t.Fatal("confirmed block's hash not recorded")
	}
	if len(f.pending) != 0 {
		t.Fatalf("%d confirmation tokens left unused", len(f.pending))
	}
}

// A nil Confirmer is the old behaviour: the block is reported and nothing is
// sent back.
func TestConfirmNilConfirmerSendsNothing(t *testing.T) {
	f := newFake()
	f.seed(t, lockedBlock("alpha-job", "synthetic locked wording"))
	st := NewSyncState(filepath.Join(t.TempDir(), "s.json"))
	_, res, err := confirmRun(t, f, st, nil)
	if err != nil || res.NeedsConfirmation != 1 || res.Confirmed != 0 {
		t.Fatalf("%v %+v", err, res)
	}
	if len(confirmCalls(t, f)) != 0 || f.profiles != 1 {
		t.Fatalf("confirmations %d, profile reads %d", len(confirmCalls(t, f)), f.profiles)
	}
}

// DevPrism refuses the requestState (here: it forgot it, as after a restart,
// elicitation.rs:71). The block is reported, nothing is recorded, and the
// sync goes on with the other blocks.
func TestConfirmRefusedByDevPrism(t *testing.T) {
	f := newFake()
	f.seed(t, lockedBlock("alpha-job", "synthetic locked wording"))
	f.beforeConfirm = func(f *fakeDevPrism) { f.forget() }
	st := NewSyncState(filepath.Join(t.TempDir(), "s.json"))
	_, res, err := confirmRun(t, f, st, func(string, Loss) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	o := outcome(res, "alpha-job")
	if o.Status != StatusConfirmRejected || !strings.Contains(o.Reason, "not issued by this server") {
		t.Fatalf("alpha-job %+v", o)
	}
	if res.ConfirmRejected != 1 || res.Added != 2 {
		t.Fatalf("the sync stopped at the refusal: %+v", res)
	}
	if _, ok := st.Blocks["alpha-job"]; ok {
		t.Fatal("refused block recorded")
	}
	if got := f.blocks["alpha-job"]["bullets"].([]any)[0].(map[string]any)["canonical"]; got != "synthetic locked wording" {
		t.Fatalf("DevPrism block changed: %v", got)
	}
	if !strings.Contains(res.Summary(), "1 confirmation refused") {
		t.Fatalf("summary %q", res.Summary())
	}
}

// A token is good once: sending the same requestState again is refused.
func TestConfirmTokenIsSingleUse(t *testing.T) {
	f := newFake()
	f.seed(t, lockedBlock("alpha-job", "synthetic locked wording"))
	st := NewSyncState(filepath.Join(t.TempDir(), "s.json"))
	p, _, err := confirmRun(t, f, st, func(string, Loss) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	// Put the old block back and replay the very same confirmation.
	f.seed(t, lockedBlock("alpha-job", "synthetic locked wording"))
	var it Item
	for _, x := range p.Items {
		if x.ID == "alpha-job" {
			it = x
		}
	}
	_, err = f.CallTool(context.Background(), "career_upsert_block", map[string]json.RawMessage{
		"block": it.Payload, "request_state": mustJSON(t, f.issued[0]), "input_responses": json.RawMessage(`{"confirm":true}`),
	})
	var te *resume.ToolError
	if !errors.As(err, &te) || te.Code != -32002 {
		t.Fatalf("replayed token: %v", err)
	}
}

// DevPrism's copy changed between the run's read and the confirmation. The
// token is bound to the payload, not to what it replaces
// (tools_career.rs:879-911), so DevPrism would accept it and drop the new
// content unseen. Vani re-reads the block and does not confirm.
func TestConfirmStaleWhenDevPrismChanged(t *testing.T) {
	f := newFake()
	f.seed(t, lockedBlock("alpha-job", "synthetic locked wording"))
	f.profileHookAfter = 1 // the plan's own read is unchanged
	f.beforeProfile = func(f *fakeDevPrism) {
		b := f.blocks["alpha-job"]
		b["bullets"] = append(b["bullets"].([]any), map[string]any{"id": "added-meanwhile", "canonical": "written in DevPrism after the read", "locked": false})
	}
	st := NewSyncState(filepath.Join(t.TempDir(), "s.json"))
	var seen []shown
	_, res, err := confirmRun(t, f, st, recordAll(&seen, always))
	if err != nil {
		t.Fatal(err)
	}
	if o := outcome(res, "alpha-job"); o.Status != StatusStale {
		t.Fatalf("alpha-job %+v", o)
	}
	if res.Stale != 1 || res.Added != 2 {
		t.Fatalf("result %+v", res)
	}
	if len(seen) != 0 || len(confirmCalls(t, f)) != 0 {
		t.Fatalf("shown %d, confirmations %d: a changed block must not be confirmed", len(seen), len(confirmCalls(t, f)))
	}
	if len(asList(f.blocks["alpha-job"]["bullets"])) != 2 {
		t.Fatal("the bullet added in DevPrism was dropped")
	}
	if _, ok := st.Blocks["alpha-job"]; ok {
		t.Fatal("stale block recorded")
	}
}

// The re-read is compared by JSON value: a copy that differs only in spacing
// and key order is the same block, and the confirmation goes out.
func TestConfirmReReadComparesByValue(t *testing.T) {
	f := newFake()
	f.seed(t, lockedBlock("alpha-job", "synthetic locked wording"))
	c := &reformatCaller{fakeDevPrism: f}
	cur, err := FetchState(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	p := Plan(fixtureResolved(t), cur, *NewSyncState(""), fixedNow)
	c.reformat = true
	res, err := Apply(context.Background(), c, p, NewSyncState(filepath.Join(t.TempDir(), "s.json")), func(string, Loss) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if o := outcome(res, "alpha-job"); o.Status != StatusConfirmed {
		t.Fatalf("alpha-job %+v: a reformatted but equal copy was taken for a change", o)
	}
}

// reformatCaller answers career_get_profile, once reformat is set, with each
// block's keys reversed and indented: the same value in different bytes.
type reformatCaller struct {
	*fakeDevPrism
	reformat bool
}

func (c *reformatCaller) CallTool(ctx context.Context, name string, args any) (json.RawMessage, error) {
	raw, err := c.fakeDevPrism.CallTool(ctx, name, args)
	if err != nil || name != "career_get_profile" || !c.reformat {
		return raw, err
	}
	var p struct {
		Blocks []map[string]json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString(`{"blocks": [`)
	for i, b := range p.Blocks {
		if i > 0 {
			out.WriteString(", ")
		}
		keys := sortedKeys(b)
		out.WriteString("{\n")
		for j := len(keys) - 1; j >= 0; j-- {
			var v bytes.Buffer
			if err := json.Indent(&v, b[keys[j]], "  ", "    "); err != nil {
				return nil, err
			}
			fmt.Fprintf(&out, "  %q: %s", keys[j], v.Bytes())
			if j > 0 {
				out.WriteString(",\n")
			}
		}
		out.WriteString("\n}")
	}
	out.WriteString("]}")
	return out.Bytes(), nil
}

// A block added since the run read DevPrism has no planned copy to match.
func TestConfirmStaleWhenBlockAppeared(t *testing.T) {
	f := newFake()
	cur, err := FetchState(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	p := Plan(fixtureResolved(t), cur, *NewSyncState(""), fixedNow)
	f.seed(t, lockedBlock("alpha-job", "synthetic wording written in DevPrism meanwhile"))
	res, err := Apply(context.Background(), f, p, NewSyncState(filepath.Join(t.TempDir(), "s.json")), func(string, Loss) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if o := outcome(res, "alpha-job"); o.Status != StatusStale {
		t.Fatalf("alpha-job %+v", o)
	}
	if len(confirmCalls(t, f)) != 0 {
		t.Fatal("confirmed a block the plan never saw")
	}
}

// Partial: of two blocks DevPrism asks about, only the one the Confirmer
// accepts is confirmed; the other's token is never sent.
func TestConfirmPartial(t *testing.T) {
	f := newFake()
	f.seed(t, lockedBlock("alpha-job", "synthetic locked wording"))
	f.seed(t, lockedBlock("beta-proj", "other synthetic locked wording"))
	st := NewSyncState(filepath.Join(t.TempDir(), "s.json"))
	var seen []shown
	_, res, err := confirmRun(t, f, st, recordAll(&seen, func(id string) bool { return id == "alpha-job" }))
	if err != nil {
		t.Fatal(err)
	}
	if outcome(res, "alpha-job").Status != StatusConfirmed {
		t.Fatalf("alpha-job %+v", outcome(res, "alpha-job"))
	}
	if o := outcome(res, "beta-proj"); o.Status != StatusNeedsConfirmation || o.Reason != "not confirmed" {
		t.Fatalf("beta-proj %+v", o)
	}
	if len(seen) != 2 || res.Confirmed != 1 || res.NeedsConfirmation != 1 || res.Added != 1 {
		t.Fatalf("shown %d, result %+v", len(seen), res)
	}
	cc := confirmCalls(t, f)
	if len(cc) != 1 || !bytes.Contains(cc[0]["block"], []byte(`"id":"alpha-job"`)) {
		t.Fatalf("confirmations %v", cc)
	}
	if _, ok := st.Blocks["beta-proj"]; ok {
		t.Fatal("declined block recorded")
	}
	if len(f.pending) != 1 {
		t.Fatalf("pending tokens %d, want beta-proj's unused one", len(f.pending))
	}
}

// DevPrism dies or times out on the confirmation: the sync stops with the
// error, the block is not recorded, and blocks accepted before it are saved.
func TestConfirmDevPrismDownOrTimingOut(t *testing.T) {
	for name, c := range map[string]struct{ sent, want error }{
		"timeout": {fmt.Errorf("%w: tools/call", resume.ErrCallTimeout), resume.ErrCallTimeout},
		"down":    {fmt.Errorf("%w: exit status 1", resume.ErrServerGone), resume.ErrServerGone},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.seed(t, lockedBlock("beta-proj", "synthetic locked wording"))
			f.confirmErr = c.sent
			path := filepath.Join(t.TempDir(), "s.json")
			st := NewSyncState(path)
			_, res, err := confirmRun(t, f, st, func(string, Loss) bool { return true })
			if !errors.Is(err, c.want) {
				t.Fatalf("err %v, want %v", err, c.want)
			}
			if res.Added != 1 || res.Confirmed != 0 {
				t.Fatalf("result %+v", res)
			}
			saved, lerr := LoadState(path)
			if lerr != nil {
				t.Fatal(lerr)
			}
			if _, ok := saved.Blocks["alpha-job"]; !ok {
				t.Fatal("the block accepted before the failure was not saved")
			}
			if _, ok := saved.Blocks["beta-proj"]; ok {
				t.Fatal("the unconfirmed block was recorded")
			}
		})
	}
}

// The re-read before confirming fails: the sync stops, nothing is confirmed.
func TestConfirmReReadFails(t *testing.T) {
	f := newFake()
	f.seed(t, lockedBlock("alpha-job", "synthetic locked wording"))
	f.profileHookAfter = 1
	f.beforeProfile = func(f *fakeDevPrism) { f.blocks["broken"] = map[string]any{} } // a block with no id
	_, _, err := confirmRun(t, f, NewSyncState(filepath.Join(t.TempDir(), "s.json")), func(string, Loss) bool { return true })
	if !errors.Is(err, ErrDevPrism) {
		t.Fatalf("err %v", err)
	}
	if len(confirmCalls(t, f)) != 0 {
		t.Fatal("confirmed without a good re-read")
	}
}

// Cancelling while the Confirmer runs sends nothing.
func TestConfirmCancelledContext(t *testing.T) {
	f := newFake()
	f.seed(t, lockedBlock("alpha-job", "synthetic locked wording"))
	cur, _ := FetchState(context.Background(), f)
	p := Plan(fixtureResolved(t), cur, *NewSyncState(""), fixedNow)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := Apply(ctx, f, p, NewSyncState(filepath.Join(t.TempDir(), "s.json")), func(string, Loss) bool { cancel(); return true })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if len(confirmCalls(t, f)) != 0 {
		t.Fatal("sent a confirmation after cancellation")
	}
}

// A requestState Vani cannot read is never confirmed: what would be lost
// could not be shown.
func TestConfirmUnreadableStateIsNotConfirmed(t *testing.T) {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	full := `"droppedBulletIds":[],"droppedFactIds":[],"modifiedLockedBullets":["x"]`
	cases := map[string]string{
		"empty":         "",
		"not base64":    "%%%",
		"not json":      enc("nope"),
		"array":         enc(`[1]`),
		"wrong tool":    enc(`{"tool":"career_delete_block",` + full + `}`),
		"missing lists": enc(`{"tool":"career_upsert_block","droppedBulletIds":[]}`),
		"names nothing": enc(`{"tool":"career_upsert_block","droppedBulletIds":[],"droppedFactIds":[],"modifiedLockedBullets":[]}`),
		"oversized":     strings.Repeat("A", maxRequestStateChars+4),
		"too many":      enc(`{"tool":"career_upsert_block","droppedBulletIds":[` + strings.TrimSuffix(strings.Repeat(`"b",`, maxLossItems+1), ",") + `],"droppedFactIds":[],"modifiedLockedBullets":[]}`),
	}
	for name, state := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeLoss(state, nil); err == nil {
				t.Fatal("decoded")
			}
			f := &stateCaller{state: state}
			it := Item{ID: "alpha-job", Action: ActionUpdate, Payload: json.RawMessage(`{"id":"alpha-job"}`), Prior: json.RawMessage(`{"id":"alpha-job"}`)}
			res, err := Apply(context.Background(), f, SyncPlan{Items: []Item{it}}, NewSyncState(filepath.Join(t.TempDir(), "s.json")), func(string, Loss) bool {
				t.Fatal("shown an unreadable loss")
				return true
			})
			if err != nil || res.NeedsConfirmation != 1 || !strings.HasPrefix(res.Outcomes[0].Reason, "not confirmed: ") {
				t.Fatalf("%v %+v", err, res)
			}
			if f.confirms != 0 {
				t.Fatal("confirmed")
			}
		})
	}
}

// stateCaller answers every upsert with inputRequired carrying state.
type stateCaller struct {
	state    string
	confirms int
}

func (c *stateCaller) CallTool(_ context.Context, name string, args any) (json.RawMessage, error) {
	if name == "career_get_profile" {
		return json.RawMessage(`{"blocks":[{"id":"alpha-job"}]}`), nil
	}
	if a, ok := args.(map[string]json.RawMessage); ok && a["request_state"] != nil {
		c.confirms++
	}
	return json.Marshal(map[string]any{"resultType": "inputRequired", "inputRequests": map[string]any{}, "requestState": c.state})
}

// decodeLoss names dropped bullets and facts with their text in DevPrism's
// copy, and an id that copy lacks with no text.
func TestDecodeLossTexts(t *testing.T) {
	prior := json.RawMessage(`{"bullets":[{"id":"b1","canonical":"synthetic one"},{"id":"b2","canonical":"synthetic two"}],"facts":[{"id":"f1","text":"synthetic fact"}]}`)
	st := base64.StdEncoding.EncodeToString([]byte(`{"tool":"career_upsert_block","droppedBulletIds":["b1","gone"],"droppedFactIds":["f1"],"modifiedLockedBullets":["b2"],"__nonce":"n"}`))
	l, err := decodeLoss(st, prior)
	if err != nil {
		t.Fatal(err)
	}
	want := Loss{
		DroppedBullets:  []LostItem{{"b1", "synthetic one"}, {"gone", ""}},
		DroppedFacts:    []LostItem{{"f1", "synthetic fact"}},
		LockedRewritten: []LostItem{{"b2", "synthetic two"}},
	}
	if fmt.Sprint(l) != fmt.Sprint(want) {
		t.Fatalf("loss %+v", l)
	}
}

// D23 (b6dd36a): a crash after DevPrism accepted a confirmed overwrite but
// before the state was saved replays the block on the next run. The replay
// must be byte-identical to the confirmed write, and since DevPrism already
// holds it, it is a plain update that asks for no second confirmation.
func TestConfirmedOverwriteReplaysByteIdentical(t *testing.T) {
	f := newFake()
	f.seed(t, lockedBlock("alpha-job", "synthetic locked wording"))
	lost := NewSyncState(filepath.Join(t.TempDir(), "never-saved.json"))
	if _, res, err := confirmRun(t, f, lost, func(string, Loss) bool { return true }); err != nil || res.Confirmed != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	var sent [][]byte
	for _, b := range f.blockArg {
		if bytes.Contains(b, []byte(`"id":"alpha-job"`)) {
			sent = append(sent, b)
		}
	}
	if len(sent) != 2 || !bytes.Equal(sent[0], sent[1]) {
		t.Fatalf("alpha-job sent %d times; the confirmation must carry the first call's bytes", len(sent))
	}
	confirmed := sent[1]
	if err := os.Remove(filepath.Join(filepath.Dir(lost.path), "never-saved.json")); err != nil {
		t.Fatal(err) // it was saved on return; the crash is that it was not
	}
	issued := len(f.issued)
	cur, err := FetchState(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	p := Plan(fixtureResolved(t), cur, *NewSyncState(""), fixedNow.Add(26*60*60*1e9))
	var replay Item
	for _, it := range p.Items {
		if it.ID == "alpha-job" {
			replay = it
		}
	}
	if replay.Action != ActionUpdate || !bytes.Equal(replay.Payload, confirmed) {
		t.Fatalf("replay %s differs from the confirmed write:\nconfirmed: %s\nreplay:    %s", replay.Action, confirmed, replay.Payload)
	}
	res, err := Apply(context.Background(), f, p, NewSyncState(filepath.Join(t.TempDir(), "s.json")), func(string, Loss) bool {
		t.Fatal("the replay asked for a second confirmation")
		return false
	})
	if err != nil || outcome(res, "alpha-job").Status != StatusUpdated || len(f.issued) != issued {
		t.Fatalf("%v %+v", err, res)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
