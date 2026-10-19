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
const hashVersion = 2

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
// the fields rkb is the source of truth for. Everything else on a DevPrism
// block (facts, skills, personas, locked flags, variants...) is DevPrism's and
// is preserved by Plan.
type Block struct {
	ID       string
	Kind     string
	Title    string
	Org      string
	Start    string
	End      *string // nil means ongoing
	Location string
	Bullets  []BlockBullet
	Hash     string // sha256 over the canonical form of the fields above
	// Blocked, when non-empty, is why this block cannot be synced. A blocked
	// block is reported and skipped, never sent with a guess.
	Blocked string
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
// "SEP 2025 to Present", "MAY 2022") into DevPrism's dateRange. Empty dates
// give an empty start and an open end (how rkb project entries read). Text it
// cannot split is reported, not guessed.
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
	switch len(parts) {
	case 1:
		p := parts[0]
		return p, &p, nil
	case 2:
		switch strings.ToLower(parts[1]) {
		case "present", "current", "now":
			return parts[0], nil, nil
		}
		e := parts[1]
		return parts[0], &e, nil
	}
	return "", nil, fmt.Errorf("unparseable dates")
}

// ResolveBank turns a bank and the claim values into blocks. Unresolved claim
// tokens, missing titles and unparseable dates mark a block Blocked; nothing
// is sent with "[clmX?]" left in it.
func ResolveBank(b Bank, c Claims) Resolved {
	var r Resolved
	unresolved := map[string]bool{}
	caution := map[string]bool{}
	r.Unattached = b.Unattached()
	for _, id := range b.EntryIDs() {
		e := b.Entries[id]
		blk := Block{ID: id, Kind: KindOf(e), Title: strings.TrimSpace(e.Title), Org: strings.TrimSpace(e.Org), Location: strings.TrimSpace(e.Location)}
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
			for _, m := range tokenRe.FindAllStringSubmatch(raw, -1) {
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
	return r
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
		V        int     `json:"v"`
		ID       string  `json:"id"`
		Kind     string  `json:"kind"`
		Title    string  `json:"title"`
		Org      string  `json:"org"`
		Start    string  `json:"start"`
		End      *string `json:"end"`
		Location string  `json:"location"`
		Bullets  []hb    `json:"bullets"`
	}{V: hashVersion, ID: b.ID, Kind: b.Kind, Title: b.Title, Org: b.Org, Start: b.Start, End: b.End, Location: b.Location, Bullets: []hb{}}
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
