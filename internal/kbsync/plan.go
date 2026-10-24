package kbsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
)

// Plan actions.
const (
	ActionAdd       = "add"
	ActionUpdate    = "update"
	ActionUnchanged = "unchanged"
	// ActionBlocked marks a block that cannot be synced as it stands (an
	// unresolved claim token, no title, unparseable dates, an oversized or
	// unreadable payload). It is reported and never sent.
	ActionBlocked = "blocked"
	// ActionVariant marks an rkb variant entry (Block.VariantOf). It is
	// reported and never sent.
	ActionVariant = "variant"
)

// maxPayloadBytes stays under DevPrism's 1 MiB block cap
// (tools_career.rs:56) with margin for re-serialisation.
const maxPayloadBytes = 1<<20 - 16<<10

// ErrDevPrism wraps a DevPrism answer this package cannot map.
var ErrDevPrism = errors.New("kbsync: DevPrism")

// ToolCaller is what the sync needs from DevPrism's MCP client. It matches
// (*resume.Client).CallTool (internal/resume/client.go:446).
type ToolCaller interface {
	CallTool(ctx context.Context, name string, args any) (json.RawMessage, error)
}

// DevPrismState is DevPrism's current blocks by id, kept as raw JSON so the
// fields rkb does not own survive an update untouched.
type DevPrismState struct {
	Blocks map[string]json.RawMessage
}

// FetchState reads the current blocks with career_get_profile
// (tools_career.rs:808-838, answer {blocks, personas, totalBlocks}).
func FetchState(ctx context.Context, c ToolCaller) (DevPrismState, error) {
	raw, err := c.CallTool(ctx, "career_get_profile", map[string]any{})
	if err != nil {
		return DevPrismState{}, fmt.Errorf("kbsync: career_get_profile: %w", err)
	}
	var p struct {
		Blocks *[]json.RawMessage `json:"blocks"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Blocks == nil {
		return DevPrismState{}, fmt.Errorf("%w: career_get_profile answer has no blocks array", ErrDevPrism)
	}
	st := DevPrismState{Blocks: map[string]json.RawMessage{}}
	for _, b := range *p.Blocks {
		var h struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(b, &h); err != nil || h.ID == "" {
			return DevPrismState{}, fmt.Errorf("%w: a block in career_get_profile has no id", ErrDevPrism)
		}
		if _, dup := st.Blocks[h.ID]; dup {
			return DevPrismState{}, fmt.Errorf("%w: career_get_profile lists block %q twice", ErrDevPrism, truncate(h.ID, 40))
		}
		st.Blocks[h.ID] = b
	}
	return st, nil
}

// Item is one block's planned action.
type Item struct {
	ID      string
	Action  string
	Reason  string // why blocked
	Bullets int
	// Dropped counts the bullets of DevPrism's copy an update removes (rkb
	// bullets the pool no longer lists); DevPrism asks for confirmation first.
	Dropped int
	Hash    string
	// Payload is the exact ExperienceBlock JSON career_upsert_block receives
	// (add and update only).
	Payload json.RawMessage
	// Prior is DevPrism's copy of the block the plan was built against, as
	// career_get_profile listed it; nil when DevPrism had none. A confirmation
	// is sent only while DevPrism still holds exactly this copy.
	Prior json.RawMessage
}

// SyncPlan is the full plan. Items are sorted by block id.
type SyncPlan struct {
	Items      []Item
	Orphaned   []string // in DevPrism, not in the approved pool: reported; only a --replace run deletes them (replace.go)
	Unattached int      // rkb bullets with no entry
	Unresolved int      // distinct claim tokens without a value
	Cautioned  []string // CAUTION claim ids used by synced bullets
	// VariantOnly counts bullets only variants list, which are not synced.
	VariantOnly int
}

// Count returns how many items have the action.
func (p SyncPlan) Count(action string) int {
	n := 0
	for _, it := range p.Items {
		if it.Action == action {
			n++
		}
	}
	return n
}

// Plan decides add / update / unchanged / blocked per rkb block and lists
// DevPrism-only blocks as orphaned. It never plans a deletion: ReplacePlan
// does, and only when asked. A block is
// unchanged only when DevPrism has it and the recorded hash equals the current
// one; a block DevPrism lacks is added even if the state file says it was
// synced (the user deleted it there), and a block DevPrism has but the state
// has never recorded is updated, merging into the existing block.
func Plan(res Resolved, current DevPrismState, state SyncState, now time.Time) SyncPlan {
	plan := SyncPlan{Unattached: res.Unattached, Unresolved: res.Unresolved, Cautioned: res.Cautioned, VariantOnly: res.VariantOnly}
	known := map[string]bool{}
	for _, b := range res.Blocks {
		it := Item{ID: b.ID, Bullets: len(b.Bullets), Hash: b.Hash}
		if b.VariantOf != "" {
			// A variant is never synced, so it is not "known": a copy DevPrism
			// already holds (synced before hashVersion 3) is an orphan, reported
			// by every run and deleted by --replace. The pool never re-judges its
			// bullets, so a withdrawn wording would otherwise stay in the table
			// ranking reads.
			it.Action, it.Reason = ActionVariant, "variant of "+b.VariantOf+" in rkb; not synced"
			if _, held := current.Blocks[b.ID]; held {
				it.Reason += "; DevPrism holds an older copy, orphaned (--replace deletes it)"
			}
			plan.Items = append(plan.Items, it)
			continue
		}
		known[b.ID] = true
		if b.Blocked != "" {
			it.Action, it.Reason = ActionBlocked, b.Blocked
			plan.Items = append(plan.Items, it)
			continue
		}
		existing, has := current.Blocks[b.ID]
		it.Prior = existing
		switch {
		case !has:
			it.Action = ActionAdd
			it.Payload, it.Reason = buildAdd(b, now)
		case state.Blocks[b.ID] == b.Hash:
			it.Action = ActionUnchanged
		default:
			it.Action = ActionUpdate
			it.Payload, it.Dropped, it.Reason = buildUpdate(b, existing, res.BankBulletIDs, now)
		}
		if it.Reason == "" && it.Payload != nil && len(it.Payload) > maxPayloadBytes {
			it.Reason = fmt.Sprintf("block JSON is %d bytes, over DevPrism's limit", len(it.Payload))
		}
		if it.Reason != "" {
			it.Action, it.Payload = ActionBlocked, nil
		}
		plan.Items = append(plan.Items, it)
	}
	sort.Slice(plan.Items, func(i, j int) bool { return plan.Items[i].ID < plan.Items[j].ID })
	for id := range current.Blocks {
		if !known[id] {
			plan.Orphaned = append(plan.Orphaned, id)
		}
	}
	sort.Strings(plan.Orphaned)
	return plan
}

type wireRange struct {
	Start string  `json:"start"`
	End   *string `json:"end"`
}

type wireMetric struct {
	Value string `json:"value"`
	Kind  string `json:"kind"`
}

func newWireBullet(b BlockBullet) map[string]any {
	metrics := []wireMetric{}
	for _, m := range b.Metrics {
		metrics = append(metrics, wireMetric{Value: m, Kind: "metric"})
	}
	return map[string]any{
		"id":           b.ID,
		"canonical":    b.Canonical,
		"variants":     map[string]any{},
		"metrics":      metrics,
		"evidenceRefs": []string{},
		"locked":       false,
	}
}

func stamp(now time.Time) string { return now.UTC().Format(time.RFC3339) }

// newWireSkill is a SkillTag (career_db/mod.rs SkillTag) as a map, so it
// encodes with sorted keys as the skills buildUpdate carries over do.
func newWireSkill(name string) map[string]any {
	return map[string]any{"name": name, "level": skillLevel}
}

func newWireSkills(names []string) []map[string]any {
	out := []map[string]any{}
	for _, n := range names {
		out = append(out, newWireSkill(n))
	}
	return out
}

// unionSkills keeps every skill on DevPrism's copy (its level and years
// included) and appends rkb's skills whose name it lacks, ignoring case and
// spacing. rkb never removes a skill: one dropped from the stack stays in
// DevPrism until removed there. Each skill is a map, so the result encodes
// the same bytes whatever key order DevPrism answered with: a replayed update
// must be byte for byte the first one.
func unionSkills(existing json.RawMessage, names []string) ([]any, string) {
	var old []map[string]json.RawMessage
	if len(existing) > 0 && string(existing) != "null" {
		if err := json.Unmarshal(existing, &old); err != nil {
			return nil, "the existing DevPrism block has unreadable skills"
		}
	}
	have := map[string]bool{}
	out := make([]any, 0, len(old)+len(names))
	for _, s := range old {
		var name string
		if s == nil || json.Unmarshal(s["name"], &name) != nil {
			return nil, "the existing DevPrism block has a skill without a name"
		}
		have[skillKey(name)] = true
		out = append(out, s)
	}
	for _, n := range names {
		if have[skillKey(n)] {
			continue
		}
		have[skillKey(n)] = true
		out = append(out, newWireSkill(n))
	}
	return out, ""
}

// unionPersonas keeps DevPrism's personas and appends rkb's it lacks.
func unionPersonas(existing json.RawMessage, add []string) ([]string, string) {
	var old []string
	if len(existing) > 0 && string(existing) != "null" {
		if err := json.Unmarshal(existing, &old); err != nil {
			return nil, "the existing DevPrism block has unreadable personas"
		}
	}
	out := append([]string{}, old...)
	for _, p := range add {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out, ""
}

// buildAdd renders a new ExperienceBlock (career_db/mod.rs:82-118). Every
// field DevPrism requires is present; the ones rkb has no source for take
// neutral values.
func buildAdd(b Block, now time.Time) (json.RawMessage, string) {
	bullets := []map[string]any{}
	for _, x := range b.Bullets {
		bullets = append(bullets, newWireBullet(x))
	}
	blk := map[string]any{
		"id":             b.ID,
		"kind":           b.Kind,
		"title":          b.Title,
		"org":            b.Org,
		"dateRange":      wireRange{Start: b.Start, End: b.End},
		"personas":       nonNilStrings(b.Personas),
		"domains":        []string{},
		"skills":         newWireSkills(b.Skills),
		"seniorityLevel": defaultSeniority,
		"bullets":        bullets,
		"facts":          []string{},
		"updatedAt":      stamp(now),
	}
	if b.Location != "" {
		blk["location"] = b.Location
	}
	raw, err := json.Marshal(blk)
	if err != nil {
		return nil, "cannot encode block: " + err.Error()
	}
	return raw, ""
}

// unchangedStamp returns existing's updatedAt when blk holds the same
// document apart from updatedAt and the derived embeddingText. Equality is
// by JSON value, not bytes, so key order and spacing in DevPrism's copy do not
// count as a change.
func unchangedStamp(existing json.RawMessage, blk map[string]json.RawMessage) (json.RawMessage, bool) {
	var prev map[string]json.RawMessage
	if json.Unmarshal(existing, &prev) != nil {
		return nil, false
	}
	stamp, ok := prev["updatedAt"]
	if !ok {
		return nil, false
	}
	value := func(m map[string]json.RawMessage) (any, bool) {
		c := maps.Clone(m)
		delete(c, "updatedAt")
		delete(c, "embeddingText")
		raw, err := json.Marshal(c)
		if err != nil {
			return nil, false
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, false
		}
		return v, true
	}
	a, okA := value(prev)
	b, okB := value(blk)
	if !okA || !okB || !reflect.DeepEqual(a, b) {
		return nil, false
	}
	return stamp, true
}

// buildUpdate merges rkb's fields into DevPrism's existing block. The upsert
// replaces the whole document, so everything DevPrism owns (facts, notes,
// seniority, domains, per-bullet locked/variants/metrics) is carried over,
// and DevPrism's own bullets rkb does not list are kept after rkb's. A bullet
// rkb sent earlier and no longer lists is dropped: one under this block's
// scoped id prefix, or one stored under the bare id of a bank bullet
// (hashVersion 1; bankBullets holds every bank bullet id). Skills and
// personas are a union: DevPrism's stay, rkb's are added when missing. Bullets
// match by scoped id; a bullet stored under its bare rkb id (hashVersion 1) is
// renamed to the scoped id. The only content changes left are ones DevPrism
// itself gates with a confirmation request: a locked bullet whose text
// changes, and a renamed bare id, which DevPrism counts as a dropped bullet.
func buildUpdate(b Block, existing json.RawMessage, bankBullets map[string]bool, now time.Time) (payload json.RawMessage, dropped int, reason string) {
	var blk map[string]json.RawMessage
	if err := json.Unmarshal(existing, &blk); err != nil || blk == nil {
		return nil, 0, "the existing DevPrism block is not a JSON object"
	}
	var oldBullets []json.RawMessage
	if raw, ok := blk["bullets"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &oldBullets); err != nil {
			return nil, 0, "the existing DevPrism block has unreadable bullets"
		}
	}
	byID := map[string]map[string]json.RawMessage{}
	var order []string
	for _, ob := range oldBullets {
		var m map[string]json.RawMessage
		var h struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(ob, &m) != nil || json.Unmarshal(ob, &h) != nil || h.ID == "" {
			return nil, 0, "the existing DevPrism block has a bullet without an id"
		}
		if _, dup := byID[h.ID]; dup {
			return nil, 0, "the existing DevPrism block lists a bullet id twice"
		}
		byID[h.ID] = m
		order = append(order, h.ID)
	}

	set := func(k string, v any) error {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		blk[k] = raw
		return nil
	}
	var kind string
	if raw, ok := blk["kind"]; !ok || json.Unmarshal(raw, &kind) != nil || kind == "" {
		kind = b.Kind
	}
	merged := make([]any, 0, len(b.Bullets)+len(oldBullets))
	listed := map[string]bool{}
	kept := map[string]bool{} // rkb ids the pool keeps for this entry
	for _, x := range b.Bullets {
		listed[x.ID] = true
		kept[x.RkbID] = true
		old, ok := byID[x.ID]
		if !ok && x.RkbID != "" && x.RkbID != x.ID {
			// A block synced under hashVersion 1 holds this bullet under its
			// bare rkb id. Adopt it under the scoped id so DevPrism's fields
			// on it survive and the text is not duplicated. The bare id
			// leaves the payload, so DevPrism's overwrite gate counts it as
			// dropped and asks for confirmation, which Apply reports as
			// needs_confirmation and never gives.
			if legacy, has := byID[x.RkbID]; has && !listed[x.RkbID] {
				listed[x.RkbID] = true
				idRaw, _ := json.Marshal(x.ID)
				legacy["id"] = idRaw
				old, ok = legacy, true
			}
		}
		if !ok {
			merged = append(merged, newWireBullet(x))
			continue
		}
		raw, _ := json.Marshal(x.Canonical)
		old["canonical"] = raw
		merged = append(merged, old)
	}
	for _, id := range order {
		if listed[id] {
			continue
		}
		// A bullet under this block's scoped prefix, or one stored under the bare
		// id of a bank bullet (a hashVersion 1 sync did) that is not listed
		// above, is one rkb sent earlier and no longer lists: its wording was
		// superseded or no longer passes the approval rule (pool.go). It must
		// not stay in DevPrism, where ranking would still count it. DevPrism
		// asks for confirmation before dropping it, as for any dropped bullet.
		// A bare id the pool still keeps for this entry (its scoped copy is
		// listed) and any other bullet are DevPrism's own and stay.
		if strings.HasPrefix(id, b.ID+bulletIDSep) || (bankBullets[id] && !kept[id]) {
			dropped++
			continue
		}
		merged = append(merged, byID[id])
	}
	skills, reason := unionSkills(blk["skills"], b.Skills)
	if reason != "" {
		return nil, 0, reason
	}
	personas, reason := unionPersonas(blk["personas"], b.Personas)
	if reason != "" {
		return nil, 0, reason
	}
	for _, kv := range []struct {
		k string
		v any
	}{
		{"kind", kind}, {"title", b.Title}, {"org", b.Org},
		{"dateRange", wireRange{Start: b.Start, End: b.End}},
		{"skills", skills}, {"personas", personas},
		{"bullets", merged}, {"updatedAt", stamp(now)},
	} {
		if err := set(kv.k, kv.v); err != nil {
			return nil, 0, "cannot encode block: " + err.Error()
		}
	}
	if b.Location != "" {
		if err := set("location", b.Location); err != nil {
			return nil, 0, "cannot encode block: " + err.Error()
		}
	}
	// embeddingText is derived from the content just replaced; a stale copy
	// would mislead search until the next embed backfill.
	delete(blk, "embeddingText")
	// An update that changes nothing but the stamp keeps DevPrism's stamp. That
	// is the replay after a crash between DevPrism accepting a batch and the
	// state save: the block is re-sent byte for byte as it was first sent, so
	// the replay changes nothing in DevPrism, updatedAt included.
	if prev, ok := unchangedStamp(existing, blk); ok {
		blk["updatedAt"] = prev
	}
	raw, err := json.Marshal(blk)
	if err != nil {
		return nil, 0, "cannot encode block: " + err.Error()
	}
	return raw, dropped, ""
}
