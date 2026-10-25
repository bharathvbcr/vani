package kbsync

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// hashVersion is mixed into every block hash. Bump it when the bank-to-block
// mapping below changes, so a changed mapping re-syncs every block.
// 2: bullet ids are block-scoped (scopedBulletID).
// 3: skills from the stack, personas, markdown removed, ISO dates.
const hashVersion = 3

// bulletIDSep joins an entry id and a bullet id into a DevPrism bullet id. It
// is outside rkb's id alphabet (idRe), so a scoped id never equals an rkb id
// and splits back unambiguously. DevPrism stores Bullet.id as a plain string
// (career_db/mod.rs:49-60) and only compares it whole.
const bulletIDSep = "/"

// scopedBulletID is the DevPrism id of rkb bullet bulletID inside block
// entryID. rkb lets several entries list one bullet; DevPrism keys bullet
// embeddings by bullet id alone (career_db/vectors.rs) and resolves a bullet
// hit to its parent block by that id, so each block's copy needs its own id.
// Every bullet is scoped, shared or not, so an id does not change when a
// second entry starts listing the bullet.
func scopedBulletID(entryID, bulletID string) (string, error) {
	switch {
	case entryID == "":
		return "", fmt.Errorf("entry has no id")
	case bulletID == "":
		return "", fmt.Errorf("a bullet has no id")
	case strings.Contains(entryID, bulletIDSep) || strings.Contains(bulletID, bulletIDSep):
		return "", fmt.Errorf("an id contains %q, which bullet ids use as a separator", bulletIDSep)
	}
	return entryID + bulletIDSep + bulletID, nil
}

// DevPrism block kinds the mapping produces (src/lib/career, CANONICAL_BLOCK_KINDS).
const (
	KindWork      = "work"
	KindProject   = "project"
	KindEducation = "education"
)

// defaultSeniority is the seniorityLevel a NEW block gets: DevPrism requires
// the field (career_db/mod.rs:94), rkb has no such concept, and the TS enum
// (types.ts:12) starts at "ic". Existing blocks keep their own value.
const defaultSeniority = "ic"

// Block is the rkb-owned content of one DevPrism experience block: exactly
// the fields rkb is the source of truth for. Skills and Personas are what rkb
// adds; an update keeps DevPrism's own and adds these (plan.go). Everything
// else on a DevPrism block (facts, locked flags, variants...) is DevPrism's
// and is preserved by Plan.
type Block struct {
	ID       string
	Kind     string
	Title    string  // markdown removed
	Org      string  // markdown removed
	Start    string  // ISO "YYYY-MM" or "YYYY"; empty for an undated project
	End      *string // nil means ongoing
	Location string
	Skills   []string // from the stack, in stack order
	Personas []string
	Bullets  []BlockBullet
	Hash     string // sha256 over the canonical form of the fields above
	// Blocked, when non-empty, is why this block cannot be synced. A blocked
	// block is reported and skipped, never sent with a guess.
	Blocked string
	// VariantOf, when non-empty, is the entry this one is an rkb variant of.
	// A variant is reported and not synced: it mostly rephrases its base, and
	// DevPrism would print both.
	VariantOf string
}

// BlockBullet is one bullet with its claim tokens resolved. Metrics are the
// resolved claim values the sentence uses (DevPrism's "must survive
// rewriting" list).
type BlockBullet struct {
	ID        string // DevPrism bullet id: scopedBulletID(block id, RkbID)
	RkbID     string // rkb bullet id; the bare id hashVersion 1 sent as ID
	Canonical string
	Metrics   []string
}

// Resolved is a bank turned into DevPrism-shaped blocks, sorted by id.
type Resolved struct {
	Blocks     []Block
	Bullets    int      // bullets across all blocks (a shared bullet counts per block)
	Unresolved int      // distinct claim tokens with no value
	Unattached int      // bank bullets no entry lists; not synced
	Cautioned  []string // claim ids rkb marks CAUTION that a synced bullet uses
	// VariantOnly counts bullets that only variant entries list: with the
	// variants unsynced, DevPrism does not get them.
	VariantOnly int
	// BankBulletIDs is every bullet id in the bank, whether or not a block lists
	// it. An update drops a bullet DevPrism holds under one of these bare ids
	// (a hashVersion 1 sync stored them so) that the block no longer lists; an
	// id that is not a bank bullet id is DevPrism's own bullet and stays.
	BankBulletIDs map[string]bool
}

var degreeRe = regexp.MustCompile(`(?i)^(bachelor|master|doctor|ph\.?\s?d)\b`)
var datesSplitRe = regexp.MustCompile(`\s+to\s+|\s*[–—]\s*|\s+-\s+`)

// KindOf maps an rkb entry to a DevPrism kind. rkb entries carry no kind (the
// section of a resume decides), so the rule is deterministic and stated: a
// degree title is education, an entry with an org and dates is work, anything
// else is a project.
func KindOf(e Entry) string {
	switch {
	case degreeRe.MatchString(strings.TrimSpace(e.Title)):
		return KindEducation
	case strings.TrimSpace(e.Org) != "" && strings.TrimSpace(e.Dates) != "":
		return KindWork
	default:
		return KindProject
	}
}

// ParseDates splits rkb's free-text dates ("AUG 2023 – APR 2025",
// "SEP 2025 to Present", "MAY 2022") into DevPrism's dateRange, each date in
// ISO form ("2023-08", or "2024" for a bare year): DevPrism's recency score
// reads a leading four-digit year (career_match/scoring.rs parse_year_month)
// and scores anything else as if undated. Empty dates give an empty start and
// an open end (how rkb project entries read). Text it cannot split or read is
// reported, not guessed: so is a range with no start ("Present" alone,
// "Present – 2024"), which would otherwise store "Present" as a date.
func ParseDates(s string) (start string, end *string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil, nil
	}
	parts := datesSplitRe.Split(s, -1)
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
		if parts[i] == "" {
			return "", nil, fmt.Errorf("unparseable dates")
		}
	}
	if openEnd(parts[0]) {
		return "", nil, fmt.Errorf("dates have no start")
	}
	if len(parts) > 2 {
		return "", nil, fmt.Errorf("unparseable dates")
	}
	start, err = isoDate(parts[0])
	if err != nil {
		return "", nil, err
	}
	if len(parts) == 1 {
		return start, &start, nil
	}
	if openEnd(parts[1]) {
		return start, nil, nil
	}
	e, err := isoDate(parts[1])
	if err != nil {
		return "", nil, err
	}
	return start, &e, nil
}

var (
	monthYearRe = regexp.MustCompile(`^([A-Za-z]+)\.?\s+(\d{4})$`)
	isoMonthRe  = regexp.MustCompile(`^(\d{4})-(0[1-9]|1[0-2])$`)
	yearOnlyRe  = regexp.MustCompile(`^\d{4}$`)
)

var monthNumbers = map[string]string{
	"jan": "01", "january": "01", "feb": "02", "february": "02", "mar": "03", "march": "03",
	"apr": "04", "april": "04", "may": "05", "jun": "06", "june": "06",
	"jul": "07", "july": "07", "aug": "08", "august": "08", "sep": "09", "sept": "09", "september": "09",
	"oct": "10", "october": "10", "nov": "11", "november": "11", "dec": "12", "december": "12",
}

// isoDate reads one date part: "AUG 2023" or "August 2023" (any case),
// "2023-08", or "2023". Anything else ("Fall 2023", "08/2023") is an error.
func isoDate(p string) (string, error) {
	year := ""
	out := ""
	switch {
	case isoMonthRe.MatchString(p):
		year, out = p[:4], p
	case yearOnlyRe.MatchString(p):
		year, out = p, p
	default:
		m := monthYearRe.FindStringSubmatch(p)
		if m == nil {
			return "", fmt.Errorf("a date is not in a recognised form")
		}
		mm, ok := monthNumbers[strings.ToLower(m[1])]
		if !ok {
			return "", fmt.Errorf("a date is not in a recognised form")
		}
		year, out = m[2], m[2]+"-"+mm
	}
	if year < "1900" || year > "2100" {
		return "", fmt.Errorf("a date's year is out of range")
	}
	return out, nil
}

// openEnd reports whether a date part means "still ongoing".
func openEnd(p string) bool {
	switch strings.ToLower(p) {
	case "present", "current", "now":
		return true
	}
	return false
}

// ResolveBank turns a bank and the claim values into blocks. Unresolved claim
// tokens, missing titles, unparseable dates and an unusable stack mark a block
// Blocked; nothing is sent with "[clmX?]" left in it. Titles, orgs and
// stacks lose their markdown (PlainText); bullet text keeps ResolveText's
// rendering, which mirrors rkb's own text renderer.
func ResolveBank(b Bank, c Claims) Resolved {
	var r Resolved
	unresolved := map[string]bool{}
	caution := map[string]bool{}
	r.Unattached = b.Unattached()
	r.BankBulletIDs = make(map[string]bool, len(b.Bullets))
	for bid := range b.Bullets {
		r.BankBulletIDs[bid] = true
	}
	baseBullets := map[string]bool{}    // listed by an entry that is synced
	variantBullets := map[string]bool{} // listed by a variant
	for _, id := range b.EntryIDs() {
		e := b.Entries[id]
		blk := Block{ID: id, Kind: KindOf(e), Title: PlainText(e.Title), Org: PlainText(e.Org), Location: PlainText(e.Location),
			Personas: append([]string(nil), syncPersonas...), VariantOf: variantOf(id, e, b.Entries)}
		for _, bid := range e.Bullets {
			if blk.VariantOf != "" {
				variantBullets[bid] = true
			} else {
				baseBullets[bid] = true
			}
		}
		var reasons []string
		if id == "" {
			reasons = append(reasons, "entry has no id")
		}
		if blk.Title == "" {
			reasons = append(reasons, "entry has no title")
		}
		if s, en, err := ParseDates(e.Dates); err != nil {
			reasons = append(reasons, "dates are not in a form DevPrism's dateRange can hold")
		} else {
			blk.Start, blk.End = s, en
		}
		if skills, err := ParseStack(e.Stack); err != nil {
			reasons = append(reasons, err.Error())
		} else {
			blk.Skills = skills
		}
		var miss []string
		for _, bid := range e.Bullets {
			raw := b.Bullets[bid].Text
			text, m := ResolveText(raw, c.Values)
			miss = append(miss, m...)
			for _, t := range tokenIDs(raw) {
				if c.Caution[t] {
					caution[t] = true
				}
			}
			sid, err := scopedBulletID(id, bid)
			if err != nil {
				reasons = append(reasons, err.Error())
			}
			bb := BlockBullet{ID: sid, RkbID: bid, Canonical: text}
			seen := map[string]bool{}
			for _, m := range TokenRe.FindAllStringSubmatch(raw, -1) {
				if v, ok := c.Values[m[1]]; ok && !seen[v] {
					seen[v] = true
					bb.Metrics = append(bb.Metrics, v)
				}
			}
			if strings.TrimSpace(text) == "" {
				reasons = append(reasons, fmt.Sprintf("bullet %s is empty", bid))
			}
			blk.Bullets = append(blk.Bullets, bb)
			r.Bullets++
		}
		if len(miss) > 0 {
			for _, m := range miss {
				unresolved[m] = true
			}
			reasons = append(reasons, fmt.Sprintf("%d claim token(s) have no value in rkb's claims export", len(dedupe(miss))))
		}
		if len(reasons) > 0 {
			blk.Blocked = strings.Join(dedupe(reasons), "; ")
		} else {
			blk.Hash = hashBlock(blk)
		}
		r.Blocks = append(r.Blocks, blk)
	}
	r.Unresolved = len(unresolved)
	r.Cautioned = sortedKeys(caution)
	for bid := range variantBullets {
		if !baseBullets[bid] {
			r.VariantOnly++
		}
	}
	return r
}

// variantOf names the entry e is an rkb variant of: the first entry in its
// origin that is not e itself and is in the bank. An entry that lists itself
// as an origin is a base; one whose origins are all gone stands alone.
func variantOf(id string, e Entry, entries map[string]Entry) string {
	for _, o := range e.Origin {
		if o == id {
			return ""
		}
	}
	for _, o := range e.Origin {
		if _, ok := entries[o]; ok {
			return o
		}
	}
	return ""
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func hashBlock(b Block) string {
	type hb struct {
		ID        string   `json:"id"`
		Canonical string   `json:"canonical"`
		Metrics   []string `json:"metrics"`
	}
	canon := struct {
		V        int      `json:"v"`
		ID       string   `json:"id"`
		Kind     string   `json:"kind"`
		Title    string   `json:"title"`
		Org      string   `json:"org"`
		Start    string   `json:"start"`
		End      *string  `json:"end"`
		Location string   `json:"location"`
		Skills   []string `json:"skills"`
		Personas []string `json:"personas"`
		Bullets  []hb     `json:"bullets"`
	}{V: hashVersion, ID: b.ID, Kind: b.Kind, Title: b.Title, Org: b.Org, Start: b.Start, End: b.End, Location: b.Location,
		Skills: nonNilStrings(b.Skills), Personas: nonNilStrings(b.Personas), Bullets: []hb{}}
	for _, x := range b.Bullets {
		m := x.Metrics
		if m == nil {
			m = []string{}
		}
		canon.Bullets = append(canon.Bullets, hb{x.ID, x.Canonical, m})
	}
	raw, _ := json.Marshal(canon) // struct marshal is deterministic; cannot fail on these types
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
