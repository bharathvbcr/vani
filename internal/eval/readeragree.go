package eval

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// The reader agreement sets every reader of a triage slot against the owner's
// would_apply: the engine's backends as the ledger recorded them, and each
// teacher model in the readings log. It is the measurement that decides
// whether any model's reading may stand in for the owner (docs: the stage-0
// step of the adaptive-ranking plan): until a reader's sensitivity and
// specificity are known on the owner's labels, its verdicts are not labels.
// Counts only, no weighting: like BuildRelevance, it describes the judged
// postings, not the pool.

// ReaderSlots are the slots the agreement reads, in display order.
var ReaderSlots = []string{"role_fit", "seniority"}

const (
	ReaderEngine  = "engine"  // a backend recorded in the ledger
	ReaderTeacher = "teacher" // a model in the teacher readings log
	// NoReading is the row of owner-judged postings a reader gave no answer for.
	NoReading = "(no reading)"
	// OtherReading folds the values past maxReaderValues, so a reader that
	// answers free text cannot grow the table without bound.
	OtherReading = "(other)"
	// maxReaderValues bounds the distinct values listed per slot and reader.
	maxReaderValues = 32
)

// ReaderCell is one value a reader gave, by the owner's answer.
type ReaderCell struct {
	Value string `json:"value"`
	Yes   int    `json:"yes"`
	Maybe int    `json:"maybe"`
	No    int    `json:"no"`
}

// ReaderSlotTable is one slot's confusion table for one reader. Its rows add
// up to every owner-judged posting: the NoReading row holds the ones the
// reader did not answer.
type ReaderSlotTable struct {
	Slot string       `json:"slot"`
	Rows []ReaderCell `json:"rows"`
}

// ReaderRow is one reader against the owner.
type ReaderRow struct {
	Reader string `json:"reader"`
	Kind   string `json:"kind"`
	// Read counts the owner-judged postings the reader answered any slot for.
	Read int `json:"read"`
	// Prompts counts the distinct prompt hashes among the teacher rows used: a
	// value above 1 mixes readings made under different questions.
	Prompts int               `json:"prompts,omitempty"`
	Slots   []ReaderSlotTable `json:"slots"`
	// Sensitivity is how many of the wanted postings (yes or maybe) the reader
	// read as a PositiveReadings role_fit (Wanted) of those it read (N).
	// Specificity is how many of the unwanted ones it did not (Wanted), of N.
	Sensitivity Count `json:"sensitivity"`
	Specificity Count `json:"specificity"`
}

// ReaderAgreement is every reader against the owner's would_apply.
type ReaderAgreement struct {
	Judged int `json:"judged"`
	Yes    int `json:"yes"`
	Maybe  int `json:"maybe"`
	No     int `json:"no"`
	// TeacherStatus is TeacherProxyAvailable when the log was read; any other
	// value says why no teacher reader is listed.
	TeacherStatus string      `json:"teacher_status"`
	TeacherDetail string      `json:"teacher_detail,omitempty"`
	Readers       []ReaderRow `json:"readers"`
}

// readerTally builds one reader's tables.
type readerTally struct {
	row      ReaderRow
	read     map[posting.Key]bool
	prompts  map[string]bool
	cells    map[string]map[string]*ReaderCell // slot -> value -> cell
	slotRead map[string]map[posting.Key]bool
}

func newTally(reader, kind string) *readerTally {
	t := &readerTally{row: ReaderRow{Reader: reader, Kind: kind}, read: map[posting.Key]bool{}, prompts: map[string]bool{},
		cells: map[string]map[string]*ReaderCell{}, slotRead: map[string]map[posting.Key]bool{}}
	for _, s := range ReaderSlots {
		t.cells[s], t.slotRead[s] = map[string]*ReaderCell{}, map[posting.Key]bool{}
	}
	return t
}

func (c *ReaderCell) add(v string) {
	switch v {
	case "yes":
		c.Yes++
	case "maybe":
		c.Maybe++
	default:
		c.No++
	}
}

// note records that the reader gave value for slot on k, owner answer v.
func (t *readerTally) note(k posting.Key, slot, value, v string) {
	if t.slotRead[slot][k] {
		return // one reading per posting, slot and reader
	}
	t.slotRead[slot][k], t.read[k] = true, true
	c := t.cells[slot][value]
	if c == nil {
		c = &ReaderCell{Value: value}
		t.cells[slot][value] = c
	}
	c.add(v)
	if slot != "role_fit" {
		return
	}
	pos := PositiveReadings[value]
	if h, _ := wanted(v); h {
		t.row.Sensitivity.N++
		if pos {
			t.row.Sensitivity.Wanted++
		}
	} else {
		t.row.Specificity.N++
		if !pos {
			t.row.Specificity.Wanted++
		}
	}
}

// finish fills the NoReading rows and bounds each table.
func (t *readerTally) finish(owner map[posting.Key]string) ReaderRow {
	t.row.Read, t.row.Prompts = len(t.read), len(t.prompts)
	for _, slot := range ReaderSlots {
		none := ReaderCell{Value: NoReading}
		for k, v := range owner {
			if !t.slotRead[slot][k] {
				none.add(v)
			}
		}
		cells := make([]ReaderCell, 0, len(t.cells[slot]))
		for _, c := range t.cells[slot] {
			cells = append(cells, *c)
		}
		total := func(c ReaderCell) int { return c.Yes + c.Maybe + c.No }
		slices.SortFunc(cells, func(a, b ReaderCell) int {
			return cmp.Or(cmp.Compare(total(b), total(a)), cmp.Compare(a.Value, b.Value))
		})
		if len(cells) > maxReaderValues {
			other := ReaderCell{Value: OtherReading}
			for _, c := range cells[maxReaderValues:] {
				other.Yes, other.Maybe, other.No = other.Yes+c.Yes, other.Maybe+c.Maybe, other.No+c.No
			}
			cells = append(cells[:maxReaderValues:maxReaderValues], other)
		}
		if total(none) > 0 {
			cells = append(cells, none)
		}
		t.row.Slots = append(t.row.Slots, ReaderSlotTable{Slot: slot, Rows: cells})
	}
	return t.row
}

// BuildReaderAgreement sets each reader's slot values against the owner's
// would_apply labels. Engine readers come from recs: a slot answered by its
// recorded backend, the first record of a key counting, a reading copied from
// another posting (AnnTriageInheritedFrom) left out as every evaluation does.
// Teacher readers come from teacher's jd_triage rows: each model's newest row
// for the key, whatever prompt it was made under (Prompts says how many there
// were). teacherStatus says why teacher is nil; a nil store is never
// "available".
func BuildReaderAgreement(labels map[posting.Key]Entry, recs []ledger.Record, teacher *TeacherStore, teacherStatus string, names Names) ReaderAgreement {
	out := ReaderAgreement{TeacherStatus: teacherStatus, Readers: []ReaderRow{}}
	if teacher == nil && out.TeacherStatus == TeacherProxyAvailable {
		out.TeacherStatus = TeacherProxyMissing
	}
	owner := map[posting.Key]string{}
	for k, e := range labels {
		v, ok := e.Labels[WouldApply]
		if !ok {
			continue
		}
		if _, j := wanted(v); !j {
			continue
		}
		owner[k] = v
		out.Judged++
		switch v {
		case "yes":
			out.Yes++
		case "maybe":
			out.Maybe++
		default:
			out.No++
		}
	}
	if len(owner) == 0 {
		return out
	}
	tallies := map[string]*readerTally{}
	get := func(reader, kind string) *readerTally {
		id := kind + "\x00" + reader
		t := tallies[id]
		if t == nil {
			t = newTally(reader, kind)
			tallies[id] = t
		}
		return t
	}
	seen := map[posting.Key]bool{}
	for _, r := range recs {
		v, ok := owner[r.Key]
		if !ok || seen[r.Key] {
			continue
		}
		seen[r.Key] = true
		if r.Annotations[ledger.AnnTriageInheritedFrom] != "" {
			continue
		}
		for _, slot := range ReaderSlots {
			pre := ledger.AnnTriagePrefix + slot
			value := r.Annotations[pre]
			if r.Annotations[pre+ledger.AnnTriageSuffixReading] != string(decide.Answered) || value == "" {
				continue
			}
			backend, _ := names.primary(r.Annotations[pre+ledger.AnnTriageSuffixBackend])
			if backend == "" {
				backend = UnknownBackend
			}
			get(backend, ReaderEngine).note(r.Key, slot, value, v)
		}
	}
	if teacher != nil {
		for _, k := range slices.Sorted(maps.Keys(owner)) {
			for model, row := range teacher.LatestByModel("", k) {
				t := get(model, ReaderTeacher)
				answered := false
				for _, slot := range ReaderSlots {
					sl, ok := row.Slots[slot]
					if !ok || sl.Reading != string(decide.Answered) || sl.Value == "" {
						continue
					}
					t.note(k, slot, sl.Value, owner[k])
					answered = true
				}
				if answered {
					t.prompts[row.PromptHash] = true
				}
			}
		}
	}
	for _, t := range tallies {
		if len(t.read) == 0 {
			continue
		}
		out.Readers = append(out.Readers, t.finish(owner))
	}
	slices.SortFunc(out.Readers, func(a, b ReaderRow) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Reader, b.Reader))
	})
	return out
}

// Markdown renders the agreement: per reader, its sensitivity and specificity
// on role_fit with Wilson intervals, then each slot's table. With nothing
// judged it says "no data".
func (a ReaderAgreement) Markdown() string {
	var b strings.Builder
	b.WriteString("\n## Readers against the owner\n\nEach reader's slot values set against the owner's would_apply (wanted = yes or maybe). Sensitivity: wanted postings read strong_fit or partial_fit; specificity: unwanted ones read otherwise. Raw counts with 95% Wilson intervals. A reader whose interval sits low is not a stand-in for the owner.\n\n")
	if a.Judged == 0 {
		b.WriteString("no data: no judged would_apply labels yet\n")
		return b.String()
	}
	fmt.Fprintf(&b, "- owner-judged postings: %d (yes %d, maybe %d, no %d)\n", a.Judged, a.Yes, a.Maybe, a.No)
	fmt.Fprintf(&b, "- teacher readings: %s", a.TeacherStatus)
	if a.TeacherDetail != "" {
		fmt.Fprintf(&b, " (%s)", oneLineDetail(a.TeacherDetail))
	}
	b.WriteString("\n")
	if len(a.Readers) == 0 {
		b.WriteString("\nno reader answered a slot for any judged posting\n")
		return b.String()
	}
	for _, r := range a.Readers {
		fmt.Fprintf(&b, "\n### %s %s\n\n- read %d of %d judged postings", r.Kind, r.Reader, r.Read, a.Judged)
		if r.Prompts > 1 {
			fmt.Fprintf(&b, "; readings span %d prompts", r.Prompts)
		}
		fmt.Fprintf(&b, "\n- sensitivity (wanted read as a fit): %s\n- specificity (unwanted not read as a fit): %s\n", r.Sensitivity, r.Specificity)
		for _, s := range r.Slots {
			fmt.Fprintf(&b, "\n%s | yes | maybe | no\n---|---|---|---\n", s.Slot)
			for _, c := range s.Rows {
				fmt.Fprintf(&b, "%s | %d | %d | %d\n", c.Value, c.Yes, c.Maybe, c.No)
			}
		}
	}
	return b.String()
}

// oneLineDetail keeps a status detail to one bounded line.
func oneLineDetail(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}
