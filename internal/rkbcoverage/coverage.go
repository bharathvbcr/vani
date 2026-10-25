// Package rkbcoverage says which of a posting's keywords the bullets of a resume
// version cover, and which no eligible bullet in the whole pool could cover.
//
// It is pure: the caller reads the pool (rkbresume.Pool: every eligible bullet
// with its wording as rkb prints it) and the posting's keywords, and passes
// them in. Nothing here reads a file, asks a model or holds a word of the
// header; the reply carries terms and bullet ids, never bullet text.
//
// Matching is jobinsight's own, so a term "occurs" in a bullet exactly where
// `vani jobs show` found it in the posting:
//
//   - a posting keyword is a term of jobinsight's vocabulary (vocab.txt); a
//     bullet covers it when jobinsight.Default().Terms(text) names it (whole
//     token sequences, case-insensitive, aliases and stop phrases and
//     case-sensitive terms like "Go" included);
//   - a version's jd_keywords are free words or short phrases the model took
//     from the posting. They are matched by the same tokenizer and rules: each
//     one becomes a one-term vocabulary entry (jobinsight.ParseVocab), so
//     "scikit-learn" and "C++" tokenize as they do for the posting. A phrase
//     the vocabulary format cannot hold (over 4 tokens, over 64 bytes, a "|")
//     is reported as unjudged, never as a miss.
package rkbcoverage

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
)

// Bounds. A reply never lists more than MaxTerms terms or MaxBulletsPerTerm
// bullet ids for one term; Hit.N and Report.TermsTotal carry the full counts so
// a capped list never reads as the whole.
const (
	MaxTerms          = 200
	MaxBulletsPerTerm = 40
	// MaxSelectionBytes bounds the selection read (the store's own bound).
	MaxSelectionBytes = 64 << 10
	// MaxSelected and MaxPool bound the bullets read.
	MaxSelected = 1000
	MaxPool     = 5000
	maxTermLen  = 64 // bytes, jobinsight's own limit for a spelling
)

// Bullet is one eligible pool bullet: its id and its wording as rkb prints it.
type Bullet struct {
	ID, Text string
}

// Input is everything a coverage needs.
type Input struct {
	// Selection is a version's choice in the owner-edit shape (only the bullet
	// ids are read).
	Selection json.RawMessage
	// Pool is every eligible bullet, as the bank stands now. A selected id that is
	// not in it has no wording to match.
	Pool []Bullet
	// Posting are the posting's keywords: jobinsight vocabulary term names, as
	// jobinsight.Keyword.Term gives them, most relevant first.
	Posting []string
	// JD are the version's jd_keywords (free words or phrases).
	JD []string
}

// Hit is a term some chosen bullet covers. Bullets lists at most
// MaxBulletsPerTerm ids, in page order; N is how many chosen bullets cover it.
type Hit struct {
	Term    string   `json:"term"`
	Bullets []string `json:"bullets"`
	N       int      `json:"n"`
}

// Report is the verdict. Hit and Miss together are every judged term: Miss is
// each one no chosen bullet covers, and Uncovered is the part of Miss that no
// eligible bullet in the whole pool covers either (a swap cannot fix those; they
// are what the owner's bank lacks). Unjudged terms could not be matched at all
// and are in none of the three. Unresolved are chosen bullet ids with no
// eligible wording now (gone, or no longer offered): their terms are not counted
// as covered, and a miss may be theirs.
type Report struct {
	Hit        []Hit    `json:"hit"`
	Miss       []string `json:"miss"`
	Uncovered  []string `json:"uncovered"`
	Unjudged   []string `json:"unjudged"`
	Unresolved []string `json:"unresolved"`
	// TermsTotal is how many distinct terms were asked about; TermsTruncated says
	// only the first MaxTerms were judged.
	TermsTotal     int  `json:"terms_total"`
	TermsTruncated bool `json:"terms_truncated,omitempty"`
}

// SelectedIDs reads the bullet ids a selection chooses, in page order and
// without repeats: the top-level bullets of a bullets section, then each entry's.
func SelectedIDs(sel json.RawMessage) ([]string, error) {
	if len(sel) == 0 {
		return nil, errors.New("rkbcoverage: no selection")
	}
	if len(sel) > MaxSelectionBytes {
		return nil, fmt.Errorf("rkbcoverage: the selection is over %d bytes", MaxSelectionBytes)
	}
	var doc struct {
		Sections []struct {
			Bullets []string `json:"bullets"`
			Entries []struct {
				Bullets []string `json:"bullets"`
			} `json:"entries"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(sel, &doc); err != nil {
		return nil, errors.New("rkbcoverage: the selection is not the owner-edit JSON")
	}
	var ids []string
	seen := map[string]bool{}
	add := func(list []string) error {
		for _, id := range list {
			if id == "" || seen[id] {
				continue
			}
			if len(ids) >= MaxSelected {
				return fmt.Errorf("rkbcoverage: the selection chooses more than %d bullets", MaxSelected)
			}
			seen[id] = true
			ids = append(ids, id)
		}
		return nil
	}
	for _, s := range doc.Sections {
		if err := add(s.Bullets); err != nil {
			return nil, err
		}
		for _, e := range s.Entries {
			if err := add(e.Bullets); err != nil {
				return nil, err
			}
		}
	}
	return ids, nil
}

// PoolUncovered returns, in order, the vocabulary terms no bullet of pool covers.
// It is Compute's uncovered list for terms asked of the pool alone, with no
// version chosen.
func PoolUncovered(pool []Bullet, terms []string) ([]string, error) {
	rep, err := Compute(Input{Selection: json.RawMessage(`{"sections":[]}`), Pool: pool, Posting: terms})
	if err != nil {
		return nil, err
	}
	return rep.Uncovered, nil
}

// term is one thing to look for: shown is how the reply spells it, and exactly
// one of the two vocabularies names it.
type term struct {
	shown  string
	posted bool // a jobinsight vocabulary name (matched by the default vocabulary)
}

// Compute judges the input.
func Compute(in Input) (Report, error) {
	rep := Report{Hit: []Hit{}, Miss: []string{}, Uncovered: []string{}, Unjudged: []string{}, Unresolved: []string{}}
	if len(in.Pool) > MaxPool {
		return rep, fmt.Errorf("rkbcoverage: the pool has more than %d bullets", MaxPool)
	}
	ids, err := SelectedIDs(in.Selection)
	if err != nil {
		return rep, err
	}
	def, err := jobinsight.Default()
	if err != nil {
		return rep, fmt.Errorf("rkbcoverage: the keyword vocabulary is broken: %w", err)
	}

	// The terms: the posting's first, then the version's, each once whatever its
	// case; the first spelling is the one shown.
	var terms []term
	seen := map[string]bool{}
	for _, t := range in.Posting {
		t = strings.TrimSpace(t)
		if k := strings.ToLower(t); t != "" && !seen[k] {
			seen[k] = true
			terms = append(terms, term{shown: t, posted: true})
		}
	}
	for _, t := range in.JD {
		t = strings.Join(strings.Fields(t), " ")
		if k := strings.ToLower(t); t != "" && !seen[k] {
			seen[k] = true
			terms = append(terms, term{shown: t})
		}
	}
	rep.TermsTotal = len(terms)
	if len(terms) > MaxTerms {
		terms, rep.TermsTruncated = terms[:MaxTerms], true
	}

	// The version's own phrases, as a vocabulary of one-term entries. One that the
	// format refuses (or that spells the same tokens as an earlier one) is
	// unjudged.
	var judged []term
	var lines []string
	var custom *jobinsight.Vocab
	for _, t := range terms {
		if t.posted {
			judged = append(judged, t)
			continue
		}
		// A leading "=" would mean "case-sensitive" and a "|" starts an alias.
		if len(t.shown) > maxTermLen || strings.HasPrefix(t.shown, "=") || strings.Contains(t.shown, "|") {
			rep.Unjudged = append(rep.Unjudged, t.shown)
			continue
		}
		next := append(lines[:len(lines):len(lines)], "jd "+t.shown)
		v, err := jobinsight.ParseVocab(strings.Join(next, "\n"))
		if err != nil {
			rep.Unjudged = append(rep.Unjudged, t.shown)
			continue
		}
		lines, custom = next, v
		judged = append(judged, t)
	}
	postingSet := map[string]bool{}
	for _, t := range judged {
		if t.posted {
			postingSet[t.shown] = true
		}
	}

	// mentions: the judged terms a wording covers.
	mentions := func(text string) map[string]bool {
		out := map[string]bool{}
		for t := range def.Terms(text) {
			if postingSet[t] {
				out[t] = true
			}
		}
		if custom != nil {
			for t := range custom.Terms(text) {
				out[t] = true
			}
		}
		return out
	}

	byID := make(map[string]map[string]bool, len(in.Pool))
	inPool := map[string]bool{}
	for _, b := range in.Pool {
		if b.ID == "" || inPool[b.ID] {
			continue
		}
		inPool[b.ID] = true
		byID[b.ID] = mentions(b.Text)
	}
	poolCovers := map[string]bool{}
	for _, m := range byID {
		for t := range m {
			poolCovers[t] = true
		}
	}

	hits := map[string][]string{}
	for _, id := range ids {
		m, ok := byID[id]
		if !ok {
			rep.Unresolved = append(rep.Unresolved, id)
			continue
		}
		for t := range m {
			hits[t] = append(hits[t], id)
		}
	}
	for _, t := range judged {
		switch ids := hits[t.shown]; {
		case len(ids) > 0:
			h := Hit{Term: t.shown, N: len(ids), Bullets: ids[:min(len(ids), MaxBulletsPerTerm)]}
			rep.Hit = append(rep.Hit, h)
		default:
			rep.Miss = append(rep.Miss, t.shown)
			if !poolCovers[t.shown] {
				rep.Uncovered = append(rep.Uncovered, t.shown)
			}
		}
	}
	sort.Strings(rep.Unjudged)
	return rep, nil
}
