package kbsync

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/bharathvbcr/Vani/internal/resume"
)

// DevPrism's MRTR confirmation for career_upsert_block, as Vani answers it.
//
// The first call sends {block} alone. An overwrite that would drop bullets or
// facts, or rewrite a locked bullet, is answered with
// {resultType:"inputRequired", inputRequests:{confirm}, requestState}
// (protocol.rs:276-280, tools_career.rs:926-959). requestState is base64 of a
// JSON object naming droppedBulletIds, droppedFactIds and modifiedLockedBullets
// (tools_career.rs:945-952, protocol.rs:305-330). The confirming call sends the
// same block, the requestState string unchanged and input_responses
// {confirm:true} (tools_career.rs:292-313, 884-917). DevPrism accepts the
// state once, within five minutes, for the exact block payload only: its nonce
// is held in the DevPrism process's memory and bound to blockId:sha1(block)
// (elicitation.rs:39, 145-166; tools_career.rs:144-148). So the confirmation
// goes out through the same client, in the same run, with the plan item's
// Payload bytes. A refused state is a JSON-RPC error -32002 whose message is
// one of elicitation.rs:71-74 (protocol.rs:24).

// errElicitationFailed is DevPrism's ERR_ELICITATION_FAILED (protocol.rs:24).
const errElicitationFailed = -32002

// maxRequestStateChars mirrors DevPrism's own bound on requestState
// (protocol.rs:295).
const maxRequestStateChars = 64 * 1024

// maxLossItems bounds each list read out of a requestState.
const maxLossItems = 4096

// LostItem is one bullet or fact an overwrite would discard or rewrite. Text
// is from DevPrism's current copy of the block; it is empty when that copy
// does not hold the id.
type LostItem struct {
	ID   string
	Text string
}

// Loss is what DevPrism says one overwrite would destroy.
type Loss struct {
	DroppedBullets  []LostItem
	DroppedFacts    []LostItem
	LockedRewritten []LostItem
}

// Empty reports whether the loss names nothing.
func (l Loss) Empty() bool {
	return len(l.DroppedBullets) == 0 && len(l.DroppedFacts) == 0 && len(l.LockedRewritten) == 0
}

// Confirmer is shown each block DevPrism asked about, with exactly what the
// overwrite would lose, before any confirmation is sent. It returns true to
// confirm that block. A nil Confirmer confirms nothing.
type Confirmer func(id string, loss Loss) bool

// decodeLoss reads the lists DevPrism put in requestState. Any doubt is an
// error: a block whose loss cannot be shown exactly is not confirmed.
func decodeLoss(state string, prior json.RawMessage) (Loss, error) {
	s := strings.TrimSpace(state)
	if s == "" {
		return Loss{}, errors.New("DevPrism sent no requestState")
	}
	if len(s) > maxRequestStateChars {
		return Loss{}, fmt.Errorf("requestState is over %d bytes", maxRequestStateChars)
	}
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return Loss{}, errors.New("requestState is not base64")
	}
	var st struct {
		Tool           string    `json:"tool"`
		DroppedBullets *[]string `json:"droppedBulletIds"`
		DroppedFacts   *[]string `json:"droppedFactIds"`
		LockedBullets  *[]string `json:"modifiedLockedBullets"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		return Loss{}, errors.New("requestState is not a JSON object")
	}
	if st.Tool != "career_upsert_block" || st.DroppedBullets == nil || st.DroppedFacts == nil || st.LockedBullets == nil {
		return Loss{}, errors.New("requestState does not list what the overwrite would lose")
	}
	if len(*st.DroppedBullets) > maxLossItems || len(*st.DroppedFacts) > maxLossItems || len(*st.LockedBullets) > maxLossItems {
		return Loss{}, fmt.Errorf("requestState lists more than %d items", maxLossItems)
	}
	bullets, facts := priorTexts(prior)
	pick := func(ids []string, texts map[string]string) []LostItem {
		var out []LostItem
		for _, id := range ids {
			out = append(out, LostItem{ID: id, Text: texts[id]})
		}
		return out
	}
	l := Loss{
		DroppedBullets:  pick(*st.DroppedBullets, bullets),
		DroppedFacts:    pick(*st.DroppedFacts, facts),
		LockedRewritten: pick(*st.LockedBullets, bullets),
	}
	if l.Empty() {
		return Loss{}, errors.New("requestState names nothing the overwrite would lose")
	}
	return l, nil
}

// priorTexts maps bullet ids to their canonical text and fact ids to their
// text (career_db/mod.rs:49-74) in DevPrism's copy of a block.
func priorTexts(prior json.RawMessage) (bullets, facts map[string]string) {
	var b struct {
		Bullets []struct {
			ID        string `json:"id"`
			Canonical string `json:"canonical"`
		} `json:"bullets"`
		Facts []struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		} `json:"facts"`
	}
	bullets, facts = map[string]string{}, map[string]string{}
	if len(prior) == 0 || json.Unmarshal(prior, &b) != nil {
		return bullets, facts
	}
	for _, x := range b.Bullets {
		bullets[x.ID] = x.Canonical
	}
	for _, x := range b.Facts {
		facts[x.ID] = x.Text
	}
	return bullets, facts
}

// sameValue compares two copies of a DevPrism block by JSON value, as
// unchangedStamp does: key order and spacing in DevPrism's serialization are
// not a change. A copy that does not parse is never the same.
func sameValue(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// confirmAnswer is what happened to one confirmation.
type confirmAnswer struct {
	status string // StatusConfirmed, StatusStale, StatusConfirmRejected or StatusNeedsConfirmation
	reason string
}

// confirm runs one block's confirmation round trip. It returns an error only
// for a failure that stops the sync (DevPrism down, a timeout, an answer it
// cannot map); a refusal or a change in DevPrism is a per-block outcome.
func confirm(ctx context.Context, caller ToolCaller, it Item, state string, ok Confirmer) (confirmAnswer, error) {
	loss, err := decodeLoss(state, it.Prior)
	if err != nil {
		return confirmAnswer{status: StatusNeedsConfirmation, reason: "not confirmed: " + err.Error()}, nil
	}
	// DevPrism checks the token against the payload, not against the block it
	// is about to replace (tools_career.rs:879-911), so a block that changed
	// since the plan would lose more than is shown here. Re-read it.
	now, err := FetchState(ctx, caller)
	if err != nil {
		return confirmAnswer{}, err
	}
	if cur, has := now.Blocks[it.ID]; has != (it.Prior != nil) || (has && !sameValue(cur, it.Prior)) {
		return confirmAnswer{status: StatusStale, reason: "DevPrism's copy changed after this run read it; not confirmed, run kb sync again"}, nil
	}
	if !ok(it.ID, loss) {
		return confirmAnswer{status: StatusNeedsConfirmation, reason: "not confirmed"}, nil
	}
	if err := ctx.Err(); err != nil {
		return confirmAnswer{}, err
	}
	st, err := json.Marshal(state)
	if err != nil {
		return confirmAnswer{}, err
	}
	args := map[string]json.RawMessage{
		"block":           it.Payload,
		"request_state":   st,
		"input_responses": json.RawMessage(`{"confirm":true}`),
	}
	raw, err := caller.CallTool(ctx, "career_upsert_block", args)
	var te *resume.ToolError
	if errors.As(err, &te) && te.Code == errElicitationFailed {
		return confirmAnswer{status: StatusConfirmRejected, reason: "DevPrism refused the confirmation: " + truncate(te.Message, 300)}, nil
	}
	if err != nil {
		return confirmAnswer{}, fmt.Errorf("kbsync: confirming career_upsert_block %s: %w", it.ID, err)
	}
	var ans struct {
		Success   *bool  `json:"success"`
		Cancelled bool   `json:"cancelled"`
		BlockID   string `json:"blockId"`
	}
	if err := json.Unmarshal(raw, &ans); err != nil {
		return confirmAnswer{}, fmt.Errorf("%w: confirming career_upsert_block %s: answer is not an object", ErrDevPrism, it.ID)
	}
	switch {
	case ans.Success != nil && *ans.Success && ans.BlockID == it.ID:
		return confirmAnswer{status: StatusConfirmed}, nil
	case ans.Success != nil && !*ans.Success && ans.Cancelled:
		return confirmAnswer{status: StatusNeedsConfirmation, reason: "DevPrism reported the overwrite cancelled"}, nil
	}
	return confirmAnswer{}, fmt.Errorf("%w: confirming career_upsert_block %s: unexpected answer %s", ErrDevPrism, it.ID, truncate(string(raw), 200))
}
