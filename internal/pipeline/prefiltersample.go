package pipeline

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// A listing-time drop (DropPolicy.ListedDrops, the adapters' DropListed) removes
// a new row before it reaches the ledger, so nothing downstream, the weekly
// audit included, ever sees it. The prefilter sample keeps a bounded,
// deterministic sample of those rows in a state file of its own, so the audit
// can ask the owner about them and a wrong rule shows up.
//
// What it holds is what a drop needs to be judged by a person: the board's ATS
// and company, the listed title and place, the rule that dropped the row and
// when. It never holds a description (a listed row has none), and it is not
// the ledger: a row here is not a posting, it has no status and no history.

const (
	// PrefilterSampleFile is where the sample is kept, under the state dir.
	PrefilterSampleFile = "audit/prefilter-sample.json"
	// DefaultPrefilterSamplePerWeek is how many rows one ISO week keeps.
	DefaultPrefilterSamplePerWeek = 50
	// MaxPrefilterSamplePerWeek bounds the per-week cap, so the file stays small
	// whatever a caller asks for.
	MaxPrefilterSamplePerWeek = 500
	// PrefilterWeeksKept is how many ISO weeks the file holds. The audit draws
	// from the current week; the rest let a label answered late still find its
	// posting and let a skipped week be drawn up.
	PrefilterWeeksKept = 8
	// PrefilterClassPrefix names the audit class of a pre-ledger drop:
	// "prefilter:<rule>", filed as the stratum "dropped:prefilter:<rule>".
	PrefilterClassPrefix = "prefilter:"

	prefilterSampleVersion = 1
	maxPrefilterSampleFile = 4 << 20
	maxPrefilterField      = 200
)

// PrefilterRow is one sampled listing-time drop.
type PrefilterRow struct {
	// Key names the row for the audit: "x:" and a hash of the board's ATS and
	// company with the listed title and place (PrefilterKey). It is not a
	// ledger key: no ledger record has it.
	Key      posting.Key `json:"key"`
	ATS      posting.ATS `json:"ats"`
	Company  string      `json:"company,omitempty"`
	Title    string      `json:"title"`
	Location string      `json:"location,omitempty"`
	Remote   bool        `json:"remote,omitempty"`
	// Rule is which rule dropped the row (PrefilterRule), the part of its audit
	// class after "prefilter:".
	Rule string    `json:"rule"`
	At   time.Time `json:"at"`
}

// Class is the row's audit class, "prefilter:<rule>".
func (r PrefilterRow) Class() string { return PrefilterClassPrefix + r.Rule }

// Posting is the row as a posting for the audit card: the listed fields and
// no description.
func (r PrefilterRow) Posting() posting.Posting {
	return posting.Posting{Key: r.Key, ATS: r.ATS, Company: r.Company, Title: r.Title, Location: r.Location, Remote: r.Remote}
}

// PrefilterKey is the audit key of a listed row. The sources' listing rows
// carry no id (the adapters judge a row before any detail request), so the key
// is made of what they do carry. It lives in the "x" key family under a hash of
// its own, so it can never equal a ledger FallbackKey.
func PrefilterKey(p posting.Posting) posting.Key {
	h := sha256.New()
	for _, s := range []string{"prefilter", string(p.ATS), foldField(p.Company), foldField(p.Title), foldField(p.Location)} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return posting.Key(string(posting.Other) + ":" + hex.EncodeToString(h.Sum(nil))[:24])
}

func foldField(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// PrefilterRule names the rule a drop reason (DropPolicy.EvaluatePosting)
// stands for: "exclude_<field>" for an exclusion (title, company, location,
// description or other), "read_<slot>" for a title reading. It never holds a
// rule's value: values can reveal the owner's work authorization, and a class
// reaches the label file.
func PrefilterRule(reason string) string {
	if why, ok := strings.CutPrefix(strings.TrimSpace(reason), "excluded:"); ok {
		why = strings.TrimSpace(why)
		field, _, _ := strings.Cut(why, " ")
		switch field {
		case "title", "company", "description":
			return "exclude_" + field
		case "location":
			if UnrecognisedLocationReason(reason) {
				return "exclude_location_unrecognised"
			}
			return "exclude_location"
		}
		return "exclude_other"
	}
	if rest, ok := strings.CutPrefix(strings.TrimSpace(reason), "slot "); ok {
		slot, _, _ := strings.Cut(rest, " ")
		if t := ruleToken(slot); t != "" {
			return "read_" + t
		}
	}
	return "other"
}

// LocationUnrecognisedMark is in the reason of an only_locations drop that
// rests on a place the location tables do not know (config.Exclude.Match,
// config.UnrecognisedLocationMark; a test pins the two). The pipeline does not
// import the config, so it names the marker here.
const LocationUnrecognisedMark = "unrecognised location"

// UnrecognisedLocationReason reports whether a drop reason (with or without the
// pipeline's "excluded: " prefix) is an exclusion on the location that rests on
// a place nothing recognised.
func UnrecognisedLocationReason(reason string) bool {
	why := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(reason), "excluded:"))
	// Only the only_locations reason carries it, as its last words: an
	// exclude.locations entry is the owner's own text and may hold any words.
	return strings.HasPrefix(why, "location is outside only_locations") && strings.HasSuffix(why, "("+LocationUnrecognisedMark+")")
}

// ruleToken keeps the characters of s a class name may hold (a-z, 0-9, _ and
// -), at most 40.
func ruleToken(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(strings.Trim(s, `"`))) {
		if b.Len() >= 40 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ListedDropsRecorded is ListedDrops that also tells rec each row it drops,
// with the rule that dropped it, before it answers. rec runs on the adapters'
// goroutines and must be safe for concurrent use; nil records nothing.
func (p DropPolicy) ListedDropsRecorded(rec func(post posting.Posting, rule string)) func(posting.Posting) bool {
	if !p.Prefilters() {
		return nil
	}
	return func(post posting.Posting) bool {
		_, reason, dropped := p.EvaluatePosting(post)
		if dropped && rec != nil {
			rec(post, PrefilterRule(reason))
		}
		return dropped
	}
}

type prefilterWeek struct {
	// Seen counts the drops this week's runs reported, a row listed on several
	// days once for each: the sample is drawn from rows, so Seen is a bound on
	// how many there were, not their count.
	Seen int            `json:"seen"`
	Rows []PrefilterRow `json:"rows"`
	hash map[posting.Key][sha256.Size]byte
}

type prefilterSampleFile struct {
	V       int                       `json:"v"`
	Updated time.Time                 `json:"updated"`
	PerWeek int                       `json:"per_week"`
	Weeks   map[string]*prefilterWeek `json:"weeks"`
}

// PrefilterSample is the sample: per ISO week, the PerWeek rows whose
// hash(week, key) is smallest among every row that week reported. The choice
// is a function of the rows and the week alone, so it does not depend on the
// order the boards were read in, a row seen every day takes one place, and a
// restart changes nothing. Safe for concurrent use.
type PrefilterSample struct {
	mu      sync.Mutex
	perWeek int
	weeks   map[string]*prefilterWeek
	changed bool
}

// NewPrefilterSample returns an empty sample that keeps perWeek rows a week
// (DefaultPrefilterSamplePerWeek when perWeek is below 1, at most
// MaxPrefilterSamplePerWeek).
func NewPrefilterSample(perWeek int) *PrefilterSample {
	if perWeek < 1 {
		perWeek = DefaultPrefilterSamplePerWeek
	}
	perWeek = min(perWeek, MaxPrefilterSamplePerWeek)
	return &PrefilterSample{perWeek: perWeek, weeks: map[string]*prefilterWeek{}}
}

func weekHash(week string, k posting.Key) [sha256.Size]byte {
	return sha256.Sum256([]byte(week + "\x00" + string(k)))
}

func lessWeekHash(a, b [sha256.Size]byte) bool { return bytes.Compare(a[:], b[:]) < 0 }

// Note reports a dropped listing row seen at `at`, in ISO week `week`
// (eval.WeekSeed). The row takes a place when it is among the week's PerWeek
// smallest hashes; otherwise only Seen counts it. A row whose key is already
// held changes nothing but Seen.
func (s *PrefilterSample) Note(p posting.Posting, rule, week string, at time.Time) {
	if s == nil || week == "" || rule == "" {
		return
	}
	row := PrefilterRow{
		ATS: p.ATS, Company: cleanField(p.Company), Title: cleanField(p.Title), Location: cleanField(p.Location),
		Remote: p.Remote, Rule: ruleToken(rule), At: at.UTC(),
	}
	if row.Rule == "" || row.Title == "" && row.Company == "" && row.Location == "" {
		return // nothing a person could judge
	}
	row.Key = PrefilterKey(posting.Posting{ATS: p.ATS, Company: p.Company, Title: p.Title, Location: p.Location})
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.week(week)
	w.Seen++
	s.changed = true
	if _, held := w.hash[row.Key]; held {
		return
	}
	h := weekHash(week, row.Key)
	if len(w.Rows) >= s.perWeek {
		worst := 0
		for i := range w.Rows {
			if lessWeekHash(w.hash[w.Rows[worst].Key], w.hash[w.Rows[i].Key]) {
				worst = i
			}
		}
		if !lessWeekHash(h, w.hash[w.Rows[worst].Key]) {
			return
		}
		delete(w.hash, w.Rows[worst].Key)
		w.Rows[worst] = row
	} else {
		w.Rows = append(w.Rows, row)
	}
	w.hash[row.Key] = h
}

func (s *PrefilterSample) week(week string) *prefilterWeek {
	w := s.weeks[week]
	if w == nil {
		w = &prefilterWeek{hash: map[posting.Key][sha256.Size]byte{}}
		s.weeks[week] = w
	}
	return w
}

// cleanField makes a listed field safe to store and show: control characters
// go, runs of space collapse, and it is cut to maxPrefilterField bytes at a
// rune boundary.
func cleanField(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxPrefilterField {
		s = s[:maxPrefilterField]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// Week is the rows held for an ISO week, in key order, and how many drops the
// week's runs reported (Seen).
func (s *PrefilterSample) Week(week string) (rows []PrefilterRow, seen int) {
	if s == nil {
		return nil, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w := s.weeks[week]
	if w == nil {
		return nil, 0
	}
	rows = append([]PrefilterRow(nil), w.Rows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return rows, w.Seen
}

// Rows is every row held, over all weeks kept, newest week first and by key
// within a week; a key held in two weeks appears once.
func (s *PrefilterSample) Rows() []PrefilterRow {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.weeks))
	for n := range s.weeks {
		names = append(names, n)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	seen := map[posting.Key]bool{}
	var out []PrefilterRow
	for _, n := range names {
		rows := append([]PrefilterRow(nil), s.weeks[n].Rows...)
		sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
		for _, r := range rows {
			if !seen[r.Key] {
				seen[r.Key] = true
				out = append(out, r)
			}
		}
	}
	return out
}

// Changed reports whether Note has recorded anything since the sample was
// made or loaded.
func (s *PrefilterSample) Changed() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.changed
}

// LoadPrefilterSample reads the sample at path. A missing file is an empty
// sample. A file that is oversized, malformed, of another version or holds an
// entry it cannot read is an error, so a sample that could not be read is
// never taken for an empty one and written over.
func LoadPrefilterSample(path string, perWeek int) (*PrefilterSample, error) {
	s := NewPrefilterSample(perWeek)
	f, err := atomicfile.OpenRead(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("prefilter sample: %w", err)
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, maxPrefilterSampleFile+1))
	if err != nil {
		return nil, fmt.Errorf("prefilter sample: read: %w", err)
	}
	if len(raw) > maxPrefilterSampleFile {
		return nil, fmt.Errorf("prefilter sample: %s is over %d bytes", path, maxPrefilterSampleFile)
	}
	var pf prefilterSampleFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&pf); err != nil {
		return nil, fmt.Errorf("prefilter sample: %s: %w", path, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("prefilter sample: %s: data after the object", path)
	}
	if pf.V != prefilterSampleVersion {
		return nil, fmt.Errorf("prefilter sample: %s: version %d, want %d", path, pf.V, prefilterSampleVersion)
	}
	if len(pf.Weeks) > 4*PrefilterWeeksKept {
		return nil, fmt.Errorf("prefilter sample: %s holds %d weeks, over %d", path, len(pf.Weeks), 4*PrefilterWeeksKept)
	}
	for name, w := range pf.Weeks {
		if w == nil || name == "" || len(name) > 16 || w.Seen < 0 || len(w.Rows) > MaxPrefilterSamplePerWeek {
			return nil, fmt.Errorf("prefilter sample: %s: week %q is not readable", path, name)
		}
		w.hash = make(map[posting.Key][sha256.Size]byte, len(w.Rows))
		for _, r := range w.Rows {
			if r.Key == "" || r.Key.ATS() == "" || r.Rule == "" || ruleToken(r.Rule) != r.Rule || r.At.IsZero() ||
				len(r.Title) > maxPrefilterField || len(r.Company) > maxPrefilterField || len(r.Location) > maxPrefilterField {
				return nil, fmt.Errorf("prefilter sample: %s: a row of week %q is not readable", path, name)
			}
			if _, dup := w.hash[r.Key]; dup {
				return nil, fmt.Errorf("prefilter sample: %s: week %q holds %s twice", path, name, r.Key)
			}
			w.hash[r.Key] = weekHash(name, r.Key)
		}
		s.weeks[name] = w
	}
	s.changed = false
	return s, nil
}

// Save writes the sample to path atomically: the newest PrefilterWeeksKept
// weeks, each cut to its PerWeek smallest hashes.
func (s *PrefilterSample) Save(path string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(s.weeks))
	for n := range s.weeks {
		names = append(names, n)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	if len(names) > PrefilterWeeksKept {
		for _, n := range names[PrefilterWeeksKept:] {
			delete(s.weeks, n)
		}
	}
	for n, w := range s.weeks {
		if len(w.Rows) > s.perWeek {
			sort.Slice(w.Rows, func(i, j int) bool { return lessWeekHash(w.hash[w.Rows[i].Key], w.hash[w.Rows[j].Key]) })
			for _, r := range w.Rows[s.perWeek:] {
				delete(w.hash, r.Key)
			}
			w.Rows = w.Rows[:s.perWeek]
		}
		sort.Slice(w.Rows, func(i, j int) bool { return w.Rows[i].Key < w.Rows[j].Key })
		s.weeks[n] = w
	}
	b, err := json.MarshalIndent(prefilterSampleFile{V: prefilterSampleVersion, Updated: now.UTC(), PerWeek: s.perWeek, Weeks: s.weeks}, "", " ")
	if err != nil {
		return fmt.Errorf("prefilter sample: encode: %w", err)
	}
	if len(b) > maxPrefilterSampleFile {
		return fmt.Errorf("prefilter sample: encodes to %d bytes, over %d", len(b), maxPrefilterSampleFile)
	}
	if err := atomicfile.Write(path, b); err != nil {
		return fmt.Errorf("prefilter sample: %w", err)
	}
	s.changed = false
	return nil
}
