package eval

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// Split is a pre-registered held-out split: labelled postings ranked by
// sha256(Salt || 0x00 || posting.Key), ascending, of which the first Size are
// held out and the rest are fit. Neither the salt nor the size is a free
// parameter: a split is chosen by its registration name from Splits, so the
// held-out set cannot be re-drawn until a result reads well.
type Split struct {
	Name string
	Salt string
	Size int
}

// Splits are the registered held-out splits, by registration name.
var Splits = map[string]Split{
	// Lappi-decision campaign/d1-vani-jdtriage-eval-preregistered.json,
	// "split": rank labelled postings by
	// sha256('d1-vani-jdtriage-2026-10-18' || 0x00 || posting.Key), ascending;
	// the first 30 are held out, all others are fit.
	"d1-vani-jdtriage-2026-10-18": {Name: "d1-vani-jdtriage-2026-10-18", Salt: "d1-vani-jdtriage-2026-10-18", Size: 30},
}

// SplitNames lists the registered split names, sorted.
func SplitNames() []string {
	out := make([]string, 0, len(Splits))
	for n := range Splits {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Rank is the split's ranking value for k: sha256 over the salt's bytes, one
// 0x00 byte, then the key's bytes, with no length prefix or terminator.
func (sp Split) Rank(k posting.Key) [sha256.Size]byte {
	b := make([]byte, 0, len(sp.Salt)+1+len(k))
	b = append(b, sp.Salt...)
	b = append(b, 0)
	b = append(b, k...)
	return sha256.Sum256(b)
}

// Assign ranks the given keys and returns the held-out keys (the first Size,
// in rank order) and the fit keys (the rest, in rank order). Duplicate keys
// are ranked once. Equal digests, which sha256 makes impossible in practice,
// fall back to key order so the result never depends on input order.
func (sp Split) Assign(keys []posting.Key) (heldOut, fit []posting.Key) {
	type ranked struct {
		key  posting.Key
		rank [sha256.Size]byte
	}
	seen := make(map[posting.Key]bool, len(keys))
	rs := make([]ranked, 0, len(keys))
	for _, k := range keys {
		if seen[k] {
			continue
		}
		seen[k] = true
		rs = append(rs, ranked{k, sp.Rank(k)})
	}
	sort.Slice(rs, func(i, j int) bool {
		if c := bytes.Compare(rs[i].rank[:], rs[j].rank[:]); c != 0 {
			return c < 0
		}
		return rs[i].key < rs[j].key
	})
	for i, r := range rs {
		if i < sp.Size {
			heldOut = append(heldOut, r.key)
		} else {
			fit = append(fit, r.key)
		}
	}
	return heldOut, fit
}

// HeldOut is the Promotion table over a registered split's held-out postings.
type HeldOut struct {
	Split   Split
	Ranked  int           // labelled postings ranked: every distinct key in the label file
	HeldOut []posting.Key // in rank order
	Fit     int
	// Promotions is BuildReport's Promotion table computed over the held-out
	// postings' labels only, with the same arithmetic and the same Names.
	Promotions []Promotion
}

// BuildHeldOut reads the label set in s once and returns both the all-labels
// report (what BuildReport returns) and sp's split with the Promotion rows
// over its held-out postings. Every distinct key in the label file
// (latest line per key) is a labelled posting and is ranked, whether or not
// it has a ledger record or any non-skip label, so the split is a function of
// the label file alone: no ledger or shadow state can move a posting between
// the halves.
//
// It refuses, rather than reporting, when the label file cannot be read or
// has any line it skipped as unreadable: a skipped line may be a key missing
// from the ranking, which would move which postings are held out.
func BuildHeldOut(recs []ledger.Record, s *Store, names Names, sp Split) (Report, HeldOut, error) {
	if sp.Size < 1 {
		return Report{}, HeldOut{}, fmt.Errorf("eval: split %q has no held-out size", sp.Name)
	}
	labels, bad, err := s.Labels()
	if err != nil {
		return Report{}, HeldOut{}, fmt.Errorf("eval: held-out split needs the labels: %w", err)
	}
	if bad > 0 {
		return Report{}, HeldOut{}, fmt.Errorf("eval: %d label lines are unreadable; a held-out split over a partial label file could hold out the wrong postings (plain `vani eval` reports them)", bad)
	}
	all := buildReport(recs, labels, bad, nil, names)
	keys := make([]posting.Key, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	held, fit := sp.Assign(keys)
	sub := make(map[posting.Key]Entry, len(held))
	for _, k := range held {
		sub[k] = labels[k]
	}
	rep := buildReport(recs, sub, 0, nil, names)
	return all, HeldOut{Split: sp, Ranked: len(keys), HeldOut: held, Fit: len(fit), Promotions: rep.Promotions}, nil
}

// LookupSplit returns the registered split named name.
func LookupSplit(name string) (Split, error) {
	sp, ok := Splits[name]
	if !ok {
		return Split{}, fmt.Errorf("unknown held-out split %q; registered: %s", name, strings.Join(SplitNames(), ", "))
	}
	return sp, nil
}

// Markdown renders the held-out section: the split's facts, then the
// Promotion rows over the held-out postings, with the same columns and
// verdicts as the all-labels table.
func (h HeldOut) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n## Held-out promotion: %s\n\n", h.Split.Name)
	fmt.Fprintf(&b, "- split: every distinct key in the label file, ranked by sha256(%q || 0x00 || posting key) ascending; the first %d are held out\n",
		h.Split.Salt, h.Split.Size)
	fmt.Fprintf(&b, "- labelled postings ranked: %d, held out: %d, fit: %d\n", h.Ranked, len(h.HeldOut), h.Fit)
	b.WriteString("- the Promotion table above is over all labelled postings and is in-sample; this table is over the held-out postings only\n\n")
	if len(h.Promotions) == 0 {
		b.WriteString("No held-out posting has a shadow reading on a labelled slot: n/a\n")
	} else {
		writePromotions(&b, h.Promotions)
	}
	// Keys come from the label file, which a hand edit can fill with anything,
	// so each is quoted: a newline in one cannot forge a line of this report.
	b.WriteString("\nHeld-out keys, in rank order:\n\n")
	for i, k := range h.HeldOut {
		fmt.Fprintf(&b, "%d. %q\n", i+1, string(k))
	}
	return b.String()
}
