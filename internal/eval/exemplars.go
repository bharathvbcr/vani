package eval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/decide/wouldapplypair"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The frozen exemplar set of the pairwise would_apply auditor: the owner's own
// labelled job titles (with their companies), written once from labels.jsonl by
// an explicit command and read by every pair question. Freezing is what makes
// the evaluation honest. A label made after FrozenAt was not in what the model
// read, so it is the evaluation set; a label made before it is not evaluation
// data however the auditor scores it (the leakage rule of the 2026-11-12
// ranking plan, build/fable-plan-2026-11-12-ranking.md).

const (
	// ExemplarsFile is the frozen set inside the state directory.
	ExemplarsFile = "silver_exemplars.json"
	// ExemplarsSchema is the file's version.
	ExemplarsSchema = 1
	// MinExemplarsPerClass is the fewest yes and the fewest no labels a freeze
	// is made from: below it the question has no contrast to show the model.
	MinExemplarsPerClass = 3

	maxExemplarsFile = 1 << 20
)

// ErrNotFrozen means no exemplar set has been written yet.
var ErrNotFrozen = errors.New("eval: no frozen exemplar set (run `vani silver freeze`)")

// ExemplarEntry is one frozen exemplar. Key is kept so the evaluation can leave
// the exemplar postings out of every pair and out of the evaluation set.
type ExemplarEntry struct {
	Key     posting.Key `json:"key"`
	Title   string      `json:"title"`
	Company string      `json:"company"`
}

// ExemplarSet is the frozen file. SHA256 is the hash of its content (the entries,
// not the freeze time): every pair row records it, so a row can always be tied
// to the exact lists the model read.
type ExemplarSet struct {
	V        int             `json:"v"`
	FrozenAt time.Time       `json:"frozen_at"`
	Yes      []ExemplarEntry `json:"yes"`
	No       []ExemplarEntry `json:"no"`
	SHA256   string          `json:"sha256"`
}

// contentSHA256 is the hash of the entries, length-prefixed so no two lists
// hash alike.
func (s ExemplarSet) contentSHA256() string {
	h := sha256.New()
	for _, l := range [][]ExemplarEntry{s.Yes, s.No} {
		fmt.Fprintf(h, "%d:", len(l))
		for _, e := range l {
			for _, f := range []string{string(e.Key), e.Title, e.Company} {
				fmt.Fprintf(h, "%d:%s\x00", len(f), f)
			}
		}
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Validate refuses a set that is not what a freeze writes: a wrong version, a
// class under MinExemplarsPerClass or over the question's bound, an entry with
// no key or title, or a hash that is not the content's (a hand edit).
func (s ExemplarSet) Validate() error {
	if s.V != ExemplarsSchema {
		return fmt.Errorf("eval: exemplar set version %d, want %d", s.V, ExemplarsSchema)
	}
	if s.FrozenAt.IsZero() {
		return errors.New("eval: exemplar set has no freeze time")
	}
	for name, l := range map[string][]ExemplarEntry{"yes": s.Yes, "no": s.No} {
		if len(l) < MinExemplarsPerClass || len(l) > wouldapplypair.MaxExemplarsPerClass {
			return fmt.Errorf("eval: %d %q exemplars, want %d..%d", len(l), name, MinExemplarsPerClass, wouldapplypair.MaxExemplarsPerClass)
		}
		for _, e := range l {
			if e.Key == "" || strings.TrimSpace(e.Title) == "" {
				return fmt.Errorf("eval: a %q exemplar has no key or title", name)
			}
		}
	}
	if s.SHA256 != s.contentSHA256() {
		return errors.New("eval: exemplar set does not match its recorded hash (edited by hand?)")
	}
	return nil
}

// Exemplars is the set as the question shows it: titles and companies only.
func (s ExemplarSet) Exemplars() wouldapplypair.Exemplars {
	conv := func(l []ExemplarEntry) []wouldapplypair.Exemplar {
		out := make([]wouldapplypair.Exemplar, 0, len(l))
		for _, e := range l {
			out = append(out, wouldapplypair.Exemplar{Title: e.Title, Company: e.Company})
		}
		return out
	}
	return wouldapplypair.Exemplars{Yes: conv(s.Yes), No: conv(s.No)}
}

// Keys are the frozen postings' keys.
func (s ExemplarSet) Keys() map[posting.Key]bool {
	out := make(map[posting.Key]bool, len(s.Yes)+len(s.No))
	for _, l := range [][]ExemplarEntry{s.Yes, s.No} {
		for _, e := range l {
			out[e.Key] = true
		}
	}
	return out
}

// Reposts are the RepostKeys of the frozen postings: a later posting with the
// same title and company is the same job again, and its label is no fresher
// evidence than the exemplar the model already read.
func (s ExemplarSet) Reposts() map[string]bool {
	out := map[string]bool{}
	for _, l := range [][]ExemplarEntry{s.Yes, s.No} {
		for _, e := range l {
			out[RepostKey(e.Title, e.Company)] = true
		}
	}
	return out
}

// RepostKey identifies a job by its title and company, case and spacing folded.
func RepostKey(title, company string) string {
	fold := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	return fold(title) + "\x00" + fold(company)
}

// FreezeStats says what a freeze rested on, so a small set is visible as small.
type FreezeStats struct {
	// YesLabels and NoLabels are the owner's usable would_apply labels (closed
	// strata, maybes and skips left out), before the ledger join and the cap.
	YesLabels, NoLabels int
	// Maybe, Skipped and Closed count the labels left out for those reasons;
	// Withheld counts the labels the caller's withhold function left out (labels
	// drawn from strata whose postings the auditor never sends).
	Maybe, Skipped, Closed, Withheld int
	// NotInLedger counts labelled postings with no ledger record (no title to
	// show); Repeats counts those whose title and company a kept one has.
	NotInLedger, Repeats int
	// YesKept and NoKept are what the set holds.
	YesKept, NoKept int
	// YesFromCells and NoFromCells are the kept exemplars drawn from the
	// audit's disagreement cells (SelectedOnReading). A yes there is a posting
	// the teacher already wanted, so it echoes the auditor's own prior; the
	// count makes that tilt visible at the freeze.
	YesFromCells, NoFromCells int
}

// FreezeExemplars builds the exemplar set from the owner's labels: each yes and
// no would_apply label (maybe and skip excluded, as are the closed strata, which
// measure liveness and not taste) joined to its ledger record's title and
// company. A job whose title and company repeat is shown once. A class over the
// question's bound keeps a deterministic hash-ordered subset, never "the first
// ones", so the set does not lean on whichever postings sort first. Fewer than
// MinExemplarsPerClass of either class is an error, and the stats still say
// what was found. withhold, when set, is asked of each label's Source (its
// audit stratum): one it names is left out, so a title from a stratum the
// auditor must not send (the work-authorization drops) is not shown to the model
// as an example of what the owner would not apply to.
func FreezeExemplars(labels map[posting.Key]Entry, recs []ledger.Record, at time.Time, withhold func(source string) bool) (ExemplarSet, FreezeStats, error) {
	var st FreezeStats
	byKey := make(map[posting.Key]posting.Posting, len(recs))
	for _, r := range recs {
		byKey[r.Key] = r.Posting
	}
	var yes, no []posting.Key
	for k, l := range labels {
		v, ok := l.Labels[WouldApply]
		switch {
		case !ok:
			continue
		case strings.HasPrefix(l.Source, ClosedPrefix):
			st.Closed++
		case withhold != nil && withhold(l.Source):
			st.Withheld++
		case v == Skip:
			st.Skipped++
		case v == "maybe":
			st.Maybe++
		case v == "yes":
			yes = append(yes, k)
		case v == "no":
			no = append(no, k)
		}
	}
	st.YesLabels, st.NoLabels = len(yes), len(no)
	seen := map[string]bool{}
	pick := func(keys []posting.Key) []ExemplarEntry {
		slices.SortFunc(keys, func(a, b posting.Key) int {
			ha, hb := hashOf("exemplars", "pick", string(a)), hashOf("exemplars", "pick", string(b))
			return bytes.Compare(ha[:], hb[:])
		})
		var out []ExemplarEntry
		for _, k := range keys {
			p, ok := byKey[k]
			if !ok || strings.TrimSpace(p.Title) == "" {
				st.NotInLedger++
				continue
			}
			rk := RepostKey(p.Title, p.Company)
			if seen[rk] {
				st.Repeats++
				continue
			}
			if len(out) == wouldapplypair.MaxExemplarsPerClass {
				continue
			}
			seen[rk] = true
			out = append(out, ExemplarEntry{Key: k, Title: oneLine(p.Title), Company: oneLine(p.Company)})
		}
		return out
	}
	set := ExemplarSet{V: ExemplarsSchema, FrozenAt: at.UTC(), Yes: pick(yes), No: pick(no)}
	st.YesKept, st.NoKept = len(set.Yes), len(set.No)
	fromCells := func(es []ExemplarEntry) (n int) {
		for _, e := range es {
			if SelectedOnReading(labels[e.Key].Source) {
				n++
			}
		}
		return n
	}
	st.YesFromCells, st.NoFromCells = fromCells(set.Yes), fromCells(set.No)
	set.SHA256 = set.contentSHA256()
	if st.YesKept < MinExemplarsPerClass || st.NoKept < MinExemplarsPerClass {
		return ExemplarSet{}, st, fmt.Errorf("eval: %d yes and %d no exemplars; a freeze needs at least %d of each (maybe, skip and closed labels do not count)",
			st.YesKept, st.NoKept, MinExemplarsPerClass)
	}
	return set, st, nil
}

// EvalLabelStats says which labels were left out of the evaluation set and why.
type EvalLabelStats struct {
	// Kept are the yes and no labels the evaluation rests on.
	Kept int
	// BeforeFreeze: made at or before the freeze (the exemplars were chosen from
	// them or the model could have read them). Exemplar and Repost: made after
	// it but of a frozen posting (a relabel; labels are latest-wins, so a
	// relabel looks new) or of the same job again. NotInLedger: no record to
	// compare against the exemplars with. Maybe, Skipped and Closed are classes
	// that are not part of the yes-against-no question.
	BeforeFreeze, Exemplar, Repost, NotInLedger, Maybe, Skipped, Closed int
	// Selected are labels drawn from the audit's disagreement cells
	// (SelectedOnReading): chosen on a model's reading, they would tilt the
	// auditor's verdict, so they are left out.
	Selected int
	// Engineering is how many of Kept came from the audit's engineering group
	// (EngineeringStratum): random within the engineering postings and chosen
	// on no reading, so they count, and the report says how many.
	Engineering int
}

// EvaluationLabels is the set the auditor is scored on: the owner's yes and no
// would_apply labels made strictly after set.FrozenAt, of postings that are not
// in the set and are not the same job (title and company) as one in it. Maybe,
// skip and closed-stratum labels are not in it. byKey gives each labelled
// posting's title and company; a labelled posting it does not hold cannot be
// shown to be new and is left out. The value is "yes" or "no".
func EvaluationLabels(labels map[posting.Key]Entry, set ExemplarSet, byKey map[posting.Key]posting.Posting) (map[posting.Key]string, EvalLabelStats) {
	var st EvalLabelStats
	out := map[posting.Key]string{}
	frozen, reposts := set.Keys(), set.Reposts()
	for k, l := range labels {
		v, ok := l.Labels[WouldApply]
		switch {
		case !ok:
			continue
		case strings.HasPrefix(l.Source, ClosedPrefix):
			st.Closed++
		case v == Skip:
			st.Skipped++ // a skip is a skip whatever drew it
		case SelectedOnReading(l.Source):
			st.Selected++
		case v == "maybe":
			st.Maybe++
		case !l.LabeledAt.After(set.FrozenAt):
			st.BeforeFreeze++
		case frozen[k]:
			st.Exemplar++
		default:
			p, ok := byKey[k]
			switch {
			case !ok:
				st.NotInLedger++
			case reposts[RepostKey(p.Title, p.Company)]:
				st.Repost++
			default:
				out[k] = v
				st.Kept++
				if EngineeringStratum(l.Source) {
					st.Engineering++
				}
			}
		}
	}
	return out, st
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// ReadExemplars reads and checks the frozen set at path. A missing file is
// ErrNotFrozen; a file that is corrupt, of another version or edited since it
// was written is an error, never a smaller set.
func ReadExemplars(path string) (ExemplarSet, error) {
	f, err := atomicfile.OpenRead(path)
	if errors.Is(err, os.ErrNotExist) {
		return ExemplarSet{}, ErrNotFrozen
	}
	if err != nil {
		return ExemplarSet{}, fmt.Errorf("eval: exemplars: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxExemplarsFile+1))
	if err != nil {
		return ExemplarSet{}, fmt.Errorf("eval: exemplars: %w", err)
	}
	if len(raw) > maxExemplarsFile {
		return ExemplarSet{}, fmt.Errorf("eval: exemplar set exceeds %d bytes", maxExemplarsFile)
	}
	var s ExemplarSet
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return ExemplarSet{}, fmt.Errorf("eval: exemplar set is corrupt: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return ExemplarSet{}, errors.New("eval: exemplar set has trailing data")
	}
	if err := s.Validate(); err != nil {
		return ExemplarSet{}, err
	}
	return s, nil
}

// WriteExemplars writes s at path. A set already there is never overwritten:
// without replace that is an error, and with it the old file is kept beside the
// new one under its own freeze time, so a refreeze never loses the set the
// earlier pair rows (which carry its hash) were asked under.
func WriteExemplars(path string, s ExemplarSet, replace bool) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		if !replace {
			return fmt.Errorf("eval: %s already holds a frozen set; a freeze is not repeated without --replace (the old set is kept beside it)", filepath.Base(path))
		}
		stamp := time.Now().UTC()
		if old, rerr := ReadExemplars(path); rerr == nil {
			stamp = old.FrozenAt
		}
		keep := strings.TrimSuffix(path, ".json") + "." + stamp.UTC().Format("20060102T150405Z") + ".json"
		if _, err := os.Lstat(keep); err == nil {
			return fmt.Errorf("eval: %s already exists; not overwriting an archived set", filepath.Base(keep))
		}
		if err := os.Rename(path, keep); err != nil {
			return fmt.Errorf("eval: keeping the old set: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("eval: exemplars: %w", err)
	}
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, append(raw, '\n'))
}
