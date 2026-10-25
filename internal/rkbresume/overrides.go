package rkbresume

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// entryOverride is the owner's per-resume override of one entry's own fields,
// under rkb's own selection keys (rkb bank.go SelEntry). An empty field
// overrides nothing; a Lead of "-" suppresses the entry's lead line. rkb's draft
// lint does not check these fields and rkb's render applies markdown and claim
// tokens to them, so checkOverrides is what keeps them plain text.
type entryOverride struct {
	Title string `json:"title"`
	Stack string `json:"stack"`
	Lead  string `json:"lead"`
	Org   string `json:"org"`
	Dates string `json:"dates"`
}

// Bounds on an override, in runes.
const (
	maxOverrideRunes = 120 // title, org, stack
	maxLeadRunes     = 300
	maxDatesRunes    = 40
	maxEditBytes     = 64 << 10
	leadSuppress     = "-"
)

// editKeys are the keys of an owner's edit: a reply's, and the five override
// keys on each entry.
var editKeys = keySet{
	top:     replyKeys.top,
	section: replyKeys.section,
	entry:   append(append([]string{}, replyKeys.entry...), "title", "stack", "lead", "org", "dates"),
}

// zero reports whether nothing is overridden.
func (o entryOverride) zero() bool { return o == entryOverride{} }

// apply writes the overridden fields over the entry m, a selection entry's
// fields as rkb reads them.
func (o entryOverride) apply(m map[string]json.RawMessage) {
	for key, v := range map[string]string{"title": o.Title, "stack": o.Stack, "lead": o.Lead, "org": o.Org, "dates": o.Dates} {
		if v != "" {
			m[key], _ = json.Marshal(v)
		}
	}
}

// forbiddenInOverride are the forms rkb's render would act on: a claim token,
// a markdown link, emphasis, and markup. None may appear in an override.
var forbiddenInOverride = []string{"{{", "}}", "](", "*", "`", "<", ">"}

// checkOverrides holds each override to one plain printable line of its field's
// length: no control, format (bidi) or unassigned character, no newline, no
// padding, no markup rkb would render, dates only letters, digits, spaces and
// "–-/,.", no figure in a lead, and a lead of exactly "-" meaning none. An empty field is not an
// override; a whitespace-only one is refused rather than read as empty. Entries
// are checked in id order so the refusal is the same every time. Messages name
// the field, never its text.
func checkOverrides(ov map[string]entryOverride) error {
	ids := make([]string, 0, len(ov))
	for id := range ov {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		o := ov[id]
		for _, f := range []struct {
			name, v string
			max     int
		}{
			{"title", o.Title, maxOverrideRunes}, {"org", o.Org, maxOverrideRunes}, {"stack", o.Stack, maxOverrideRunes},
			{"lead", o.Lead, maxLeadRunes}, {"dates", o.Dates, maxDatesRunes},
		} {
			if err := checkOverride(f.name, f.v, f.max); err != nil {
				return fmt.Errorf("entry %q: %w", clip(id, 60), err)
			}
		}
	}
	return nil
}

func checkOverride(name, v string, max int) error {
	if v == "" || (name == "lead" && v == leadSuppress) {
		return nil
	}
	switch {
	case !utf8.ValidString(v):
		return fmt.Errorf("%s is not valid text", name)
	case strings.TrimSpace(v) == "":
		return fmt.Errorf("%s is only whitespace; leave it empty to override nothing", name)
	case strings.TrimSpace(v) != v:
		return fmt.Errorf("%s has space around it", name)
	case utf8.RuneCountInString(v) > max:
		return fmt.Errorf("%s is over %d characters", name, max)
	}
	for _, r := range v {
		if !unicode.IsPrint(r) { // not control (newline, tab), format (bidi marks), a space but " ", or unassigned
			return fmt.Errorf("%s must be one printable line: it has a control, formatting or invisible character", name)
		}
		if name == "dates" && !datesRune(r) {
			return fmt.Errorf("dates may hold only letters, digits, spaces and – - / , .")
		}
		// A figure on the page comes only from a claim rkb verified. A lead is a
		// sentence, so a number in it would be an unverified figure; a title, an
		// org, a stack and dates hold names and versions ("CUDA 12", "3M").
		if name == "lead" && unicode.IsNumber(r) {
			return errors.New("lead may not hold a figure: figures on the page come only from verified claims")
		}
	}
	for _, bad := range forbiddenInOverride {
		if strings.Contains(v, bad) {
			return fmt.Errorf("%s may not contain %q: rkb would render it", name, bad)
		}
	}
	return nil
}

func datesRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" –-/,.", r)
}

// editEntries reads the five override keys off every entry of an owner's edit.
type editEntries struct {
	Sections []struct {
		Entries []struct {
			Entry string `json:"entry"`
			entryOverride
		} `json:"entries"`
	} `json:"sections"`
}

// decodeEdit reads an owner's edit: a reply's shape, plus the five override keys
// on each entry. Bounded (maxEditBytes) before anything else, then as strict as
// the model's reply (one JSON object, nothing after it, no repeated key, ids in
// the bank's alphabet, keys spelled exactly as editKeys) with the override keys
// the one addition. It returns the reply and the overrides by entry id; an entry
// that overrides nothing has none. The model's decodeReply still refuses these
// keys.
func decodeEdit(raw []byte) (reply, map[string]entryOverride, error) {
	if len(raw) > maxEditBytes {
		return reply{}, nil, fmt.Errorf("the edit is over %d bytes", maxEditBytes)
	}
	r, err := decodeKeyed(raw, editKeys, false)
	if err != nil {
		return reply{}, nil, err
	}
	var e editEntries
	if err := json.Unmarshal(raw, &e); err != nil {
		return reply{}, nil, errors.New("the edit is not the JSON object asked for")
	}
	ov := map[string]entryOverride{}
	for _, s := range e.Sections {
		for _, en := range s.Entries {
			if en.zero() {
				continue
			}
			if _, dup := ov[en.Entry]; dup {
				return reply{}, nil, fmt.Errorf("entry %q is used twice", clip(en.Entry, 60))
			}
			ov[en.Entry] = en.entryOverride
		}
	}
	return r, ov, nil
}

// attachOverrides puts each override on the entry of ch it names. An override for
// an entry that is not on the page is refused, whole: nothing is attached.
func attachOverrides(ch *choice, ov map[string]entryOverride) error {
	left := make(map[string]bool, len(ov))
	for id := range ov {
		left[id] = true
	}
	for i := range ch.secs {
		for j := range ch.secs[i].entries {
			e := &ch.secs[i].entries[j]
			if o, ok := ov[e.id]; ok {
				e.over = o
				delete(left, e.id)
			}
		}
	}
	if len(left) > 0 {
		ids := make([]string, 0, len(left))
		for id := range left {
			ids = append(ids, clip(id, 60))
		}
		sort.Strings(ids)
		return fmt.Errorf("an override names %q, which is not on the page", ids[0])
	}
	return nil
}

// ownerChoice turns the owner's edit into a choice: decodeEdit, the page-safety
// rules every choice obeys (checkSafe), the override rules (checkOverrides),
// then the overrides on their entries. The rules that steer the model
// (checkSteering) do not bind the owner.
func (b *builder) ownerChoice(raw []byte, p pool, jd string) (choice, error) {
	r, ov, err := decodeEdit(raw)
	if err != nil {
		return choice{}, err
	}
	ch, err := b.checkSafe(r, p, jd)
	if err != nil {
		return choice{}, err
	}
	if err := checkOverrides(ov); err != nil {
		return choice{}, err
	}
	if err := attachOverrides(&ch, ov); err != nil {
		return choice{}, err
	}
	return ch, nil
}
