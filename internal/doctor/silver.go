package doctor

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/atomicfile"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// The doctor's silver log (second Fable review, third pass): each drop
// verdict of a drops part that ran, pointwise, so the doctor's judgment of a
// drop can be set against the owner's own answers on the same postings. It is
// silver, never gold: no row is a label, a mark or a known outcome, and
// neither the rescue rule nor the silver prior reads it. Only
// BuildSilverAgreement does, against owner labels.
const (
	SilverFile = "silver_doctor.jsonl"
	// SilverSchema 2 adds the summary and rules hashes to a row. Schema 1
	// rows are still read: they carry neither hash, so the comparison keeps
	// them in groups of their own and never pools them with a schema 2 run.
	SilverSchema       = 2
	silverLegacySchema = 1
	// maxSilverLine bounds one row as read back.
	maxSilverLine = 4 << 10
)

// SilverRow is one drop verdict. It holds no free text: the model's why stays
// in the report it came from. TextSHA256 hashes the posting text the doctor
// was shown (title, company and description as sent). PromptSHA256 hashes the
// drops part's system prompt (a compile-time constant), SummarySHA256 the
// target summary the review judged against and RulesSHA256 the drop rules it
// was shown, so a comparison stays within one configuration: prompt, summary,
// rules and model. Schema 1 rows hold no summary or rules hash (both empty).
type SilverRow struct {
	V             int         `json:"v"`
	RunID         string      `json:"run_id"`
	At            time.Time   `json:"at"`
	Key           posting.Key `json:"key"`
	Stratum       string      `json:"stratum"`
	PSelect       float64     `json:"p_select"`
	Verdict       string      `json:"verdict"`
	Cause         string      `json:"cause"`
	Model         string      `json:"model"`
	PromptSHA256  string      `json:"prompt_sha256"`
	SummarySHA256 string      `json:"summary_sha256,omitempty"`
	RulesSHA256   string      `json:"rules_sha256,omitempty"`
	TextSHA256    string      `json:"text_sha256"`
}

func (r SilverRow) validate() error {
	switch {
	case r.V != SilverSchema && r.V != silverLegacySchema:
		return fmt.Errorf("schema %d, want %d (or the legacy %d)", r.V, SilverSchema, silverLegacySchema)
	case r.RunID == "" || r.Key == "" || r.Stratum == "" || r.Model == "":
		return errors.New("run id, key, stratum and model are required")
	case !(r.PSelect >= eval.MinInclusionProbability && r.PSelect <= 1):
		return fmt.Errorf("p_select %v out of range", r.PSelect)
	case !slices.Contains(dropVerdicts, r.Verdict):
		return fmt.Errorf("verdict %q is not a drop verdict", r.Verdict)
	case !slices.Contains(dropCauses, r.Cause):
		return fmt.Errorf("cause %q is not a drop cause", r.Cause)
	case len(r.PromptSHA256) != 64 || len(r.TextSHA256) != 64:
		return errors.New("prompt and text hashes are sha256 hex")
	case r.V == SilverSchema && (len(r.SummarySHA256) != 64 || len(r.RulesSHA256) != 64):
		return errors.New("summary and rules hashes are sha256 hex")
	case r.V == silverLegacySchema && (r.SummarySHA256 != "" || r.RulesSHA256 != ""):
		return errors.New("a schema 1 row holds no summary or rules hash")
	}
	return nil
}

// TextSHA256 is the hash a row records for a case: the text the doctor saw.
func TextSHA256(c DropCase) string {
	h := sha256.Sum256([]byte(c.Title + "\x00" + c.Company + "\x00" + c.Description))
	return hex.EncodeToString(h[:])
}

// PromptSHA256 is the hash a row records for a drops request's system prompt.
func PromptSHA256(system string) string {
	h := sha256.Sum256([]byte(system))
	return hex.EncodeToString(h[:])
}

// SilverRun is the configuration a drops review ran under, as the hashes a row
// records: the system prompt, the target summary and the drop rules. The model
// is the report's own.
type SilverRun struct {
	PromptSHA256  string
	SummarySHA256 string
	RulesSHA256   string
}

// NewSilverRun hashes a drops review's configuration. The rules are hashed
// as a set (sorted, each length-prefixed), so their order does not split runs
// and two different rule lists never collide on a joining character.
func NewSilverRun(system, summary string, rules []string) SilverRun {
	h := sha256.New()
	for _, r := range slices.Sorted(slices.Values(rules)) {
		fmt.Fprintf(h, "%d:%s", len(r), r)
	}
	return SilverRun{PromptSHA256: PromptSHA256(system), SummarySHA256: PromptSHA256(summary), RulesSHA256: hex.EncodeToString(h.Sum(nil))}
}

// SilverStore is the doctor's silver log. Rows are appended whole and
// fsynced; a torn last line is repaired with a newline before the next row
// and skipped on read, as are lines that are not valid rows (Skipped).
type SilverStore struct {
	path    string
	mu      sync.Mutex
	seen    map[[2]string]bool // (run id, key)
	rows    []SilverRow
	Skipped int
}

// OpenSilverStore reads path; a missing file is an empty log.
func OpenSilverStore(path string) (*SilverStore, error) {
	s := &SilverStore{path: path, seen: map[[2]string]bool{}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("doctor silver: %w", err)
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := br.ReadSlice('\n')
		switch {
		case errors.Is(err, bufio.ErrBufferFull) || len(line) > maxSilverLine:
			s.Skipped++
			for errors.Is(err, bufio.ErrBufferFull) { // drain the rest of the long line
				_, err = br.ReadSlice('\n')
			}
		case len(strings.TrimSpace(string(line))) > 0:
			var r SilverRow
			if json.Unmarshal(line, &r) != nil || r.validate() != nil {
				s.Skipped++
			} else if !s.seen[[2]string{r.RunID, string(r.Key)}] {
				s.seen[[2]string{r.RunID, string(r.Key)}] = true
				s.rows = append(s.rows, r)
			}
		}
		if errors.Is(err, io.EOF) {
			return s, nil
		}
		if err != nil {
			return nil, fmt.Errorf("doctor silver: %w", err)
		}
	}
}

// Rows is a copy of every valid row, first of each (run id, key), in file order.
func (s *SilverStore) Rows() []SilverRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.rows)
}

// Append durably adds the rows not already in the log under their (run id,
// key), so a retried part never doubles a verdict, and returns how many it
// added. Every row is validated before any is written: one bad row writes
// nothing.
func (s *SilverStore) Append(rows []SilverRow) (int, error) {
	var buf []byte
	s.mu.Lock()
	defer s.mu.Unlock()
	batch := map[[2]string]bool{}
	var add []SilverRow
	for _, r := range rows {
		if r.V == 0 {
			r.V = SilverSchema
		}
		if r.V == silverLegacySchema {
			return 0, fmt.Errorf("doctor silver: %s: schema %d rows are read, never written", r.Key, r.V)
		}
		if err := r.validate(); err != nil {
			return 0, fmt.Errorf("doctor silver: %s: %w", r.Key, err)
		}
		id := [2]string{r.RunID, string(r.Key)}
		if s.seen[id] || batch[id] {
			continue
		}
		batch[id] = true
		line, err := json.Marshal(r)
		if err != nil {
			return 0, err
		}
		buf = append(append(buf, line...), '\n')
		add = append(add, r)
	}
	if len(add) == 0 {
		return 0, nil
	}
	f, created, err := atomicfile.OpenAppend(s.path, os.O_APPEND)
	if err != nil {
		return 0, fmt.Errorf("doctor silver: open: %w", err)
	}
	defer f.Close()
	if created {
		if err := atomicfile.SyncNew(s.path); err != nil {
			return 0, fmt.Errorf("doctor silver: %w", err)
		}
	}
	torn, err := atomicfile.TornTail(f)
	if err != nil {
		return 0, fmt.Errorf("doctor silver: %w", err)
	}
	if torn {
		buf = append([]byte{'\n'}, buf...)
	}
	if _, err := f.Write(buf); err != nil {
		return 0, fmt.Errorf("doctor silver: write: %w", err)
	}
	if err := atomicfile.Sync(f); err != nil {
		return 0, fmt.Errorf("doctor silver: sync: %w", err)
	}
	for _, r := range add {
		s.seen[[2]string{r.RunID, string(r.Key)}] = true
		s.rows = append(s.rows, r)
	}
	return len(add), nil
}

// SilverRows turns a run's drop findings into rows: one per finding whose
// posting was sent (a finding on a key the run never sent is not the
// doctor's judgment of a sampled drop) and whose stratum keep allows. withheld
// counts the findings keep refused.
func SilverRows(rep Report, cases []DropCase, runID string, run SilverRun, keep func(stratum string) bool) (rows []SilverRow, withheld int) {
	byKey := make(map[string]DropCase, len(cases))
	for _, c := range cases {
		byKey[c.Key] = c
	}
	for _, f := range rep.DropFinds {
		c, ok := byKey[f.Key]
		if !ok {
			continue
		}
		if !keep(c.Stratum) {
			withheld++
			continue
		}
		rows = append(rows, SilverRow{V: SilverSchema, RunID: runID, At: rep.At, Key: posting.Key(c.Key), Stratum: c.Stratum, PSelect: c.PSelect,
			Verdict: f.Verdict, Cause: f.Cause, Model: rep.Model, PromptSHA256: run.PromptSHA256, SummarySHA256: run.SummarySHA256, RulesSHA256: run.RulesSHA256, TextSHA256: TextSHA256(c)})
	}
	return rows, withheld
}

// The comparison's floors: the auditor's own (eval: 10 yes and 30 no owner
// labels). Below them the comparison says "insufficient" and shows no figure.
const (
	SilverMinYes = 10
	SilverMinNo  = 30
)

// SilverAgreement is one configuration's doctor verdicts set against the
// owner's would_apply answers on the same postings: the newest verdict per
// posting under that configuration (prompt, summary, rules and model: runs
// that differ in any one are separate rows). A row with no SummarySHA256 and
// RulesSHA256 is a schema 1 group, whose summary and rules were not recorded. Wrong-drop agreement is the share of owner-yes postings
// the doctor called a wrong drop; correct-drop agreement the share of
// owner-no postings it called a correct one. Unclear verdicts and maybe
// answers count in neither. Labels drawn because a model reading disagreed or
// sat near a yes (eval.SelectedOnReading) are not independent of the verdicts
// being scored, so they are left out and counted in LabelsLeftOut.
type SilverAgreement struct {
	PromptSHA256  string `json:"prompt_sha256"`
	Model         string `json:"model"`
	SummarySHA256 string `json:"summary_sha256,omitempty"`
	RulesSHA256   string `json:"rules_sha256,omitempty"`
	Verdicts      int    `json:"verdicts"`
	OwnerYes      int    `json:"owner_yes"`
	OwnerNo       int    `json:"owner_no"`
	Unclear       int    `json:"unclear_on_owner_labels"`
	// LabelsLeftOut counts the judged postings whose would_apply answer was
	// selected on a reading and so was not used.
	LabelsLeftOut int    `json:"labels_left_out_selected_on_reading"`
	Status        string `json:"status"` // "ok" or "insufficient"
	// Set only when Status is "ok".
	WrongOnYes  int     `json:"wrong_drop_on_owner_yes,omitempty"`
	CorrectOnNo int     `json:"correct_drop_on_owner_no,omitempty"`
	WrongRate   float64 `json:"wrong_drop_rate_on_yes,omitempty"`
	WrongLo     float64 `json:"wrong_drop_rate_lo,omitempty"`
	WrongHi     float64 `json:"wrong_drop_rate_hi,omitempty"`
	CorrectRate float64 `json:"correct_drop_rate_on_no,omitempty"`
	CorrectLo   float64 `json:"correct_drop_rate_lo,omitempty"`
	CorrectHi   float64 `json:"correct_drop_rate_hi,omitempty"`
}

// silverGroup is the comparison's grouping key.
type silverGroup struct{ prompt, model, summary, rules string }

func (g silverGroup) String() string {
	return g.prompt + "\x00" + g.model + "\x00" + g.summary + "\x00" + g.rules
}

// BuildSilverAgreement compares the log with the owner's labels, per
// configuration (prompt, model, summary and rules hashes).
// A label whose source is a reading-selected cell is not used.
func BuildSilverAgreement(rows []SilverRow, labels map[posting.Key]eval.Entry) []SilverAgreement {
	newest := map[silverGroup]map[posting.Key]SilverRow{}
	for _, r := range rows {
		g := silverGroup{r.PromptSHA256, r.Model, r.SummarySHA256, r.RulesSHA256}
		m := newest[g]
		if m == nil {
			m = map[posting.Key]SilverRow{}
			newest[g] = m
		}
		if prev, ok := m[r.Key]; !ok || !r.At.Before(prev.At) {
			m[r.Key] = r
		}
	}
	out := []SilverAgreement{}
	for _, g := range slices.SortedFunc(maps.Keys(newest), func(a, b silverGroup) int { return strings.Compare(a.String(), b.String()) }) {
		a := SilverAgreement{PromptSHA256: g.prompt, Model: g.model, SummarySHA256: g.summary, RulesSHA256: g.rules, Verdicts: len(newest[g])}
		var wrongYes, correctNo int
		for k, r := range newest[g] {
			l := labels[k]
			v := l.Labels[eval.WouldApply]
			if v != "yes" && v != "no" {
				continue
			}
			if eval.SelectedOnReading(l.Source) {
				a.LabelsLeftOut++
				continue
			}
			if r.Verdict == DropUnclear {
				a.Unclear++
				continue
			}
			if v == "yes" {
				a.OwnerYes++
				if r.Verdict == DropWrong {
					wrongYes++
				}
			} else {
				a.OwnerNo++
				if r.Verdict == DropCorrect {
					correctNo++
				}
			}
		}
		a.Status = "insufficient"
		if a.OwnerYes >= SilverMinYes && a.OwnerNo >= SilverMinNo {
			a.Status = "ok"
			a.WrongOnYes, a.CorrectOnNo = wrongYes, correctNo
			a.WrongRate = float64(wrongYes) / float64(a.OwnerYes)
			a.WrongLo, a.WrongHi, _ = eval.Wilson(wrongYes, a.OwnerYes)
			a.CorrectRate = float64(correctNo) / float64(a.OwnerNo)
			a.CorrectLo, a.CorrectHi, _ = eval.Wilson(correctNo, a.OwnerNo)
		}
		out = append(out, a)
	}
	return out
}

// SilverMarkdown renders the comparison.
func SilverMarkdown(as []SilverAgreement, skipped int) string {
	var b strings.Builder
	b.WriteString("\n## Doctor drop verdicts against the owner\n\nThe doctor's verdicts (" + SilverFile + ") on postings the owner also answered, per prompt. Silver only: a verdict is never a label, a mark or a known outcome.\n\n")
	if len(as) == 0 {
		b.WriteString("no data: the doctor's silver log is empty\n")
	}
	for _, a := range as {
		cfgNote := "summary " + short(a.SummarySHA256) + ", rules " + short(a.RulesSHA256)
		if a.SummarySHA256 == "" && a.RulesSHA256 == "" {
			cfgNote = "summary and rules not recorded (rows from before schema 2)"
		}
		fmt.Fprintf(&b, "- prompt %s, model %s, %s: %d postings judged; on %d owner labels (%d yes, %d no; %d unclear verdicts left out)\n", short(a.PromptSHA256), a.Model, cfgNote, a.Verdicts, a.OwnerYes+a.OwnerNo, a.OwnerYes, a.OwnerNo, a.Unclear)
		if a.LabelsLeftOut > 0 {
			fmt.Fprintf(&b, "  - %d owner labels left out: they were chosen because a model reading disagreed or sat near a yes, so they are not independent of the verdicts scored\n", a.LabelsLeftOut)
		}
		if a.Status != "ok" {
			fmt.Fprintf(&b, "  - insufficient: the comparison needs %d yes and %d no owner labels on postings the doctor judged\n", SilverMinYes, SilverMinNo)
			continue
		}
		fmt.Fprintf(&b, "  - owner yes called a wrong drop: %d of %d, %.2f (%.2f-%.2f)\n", a.WrongOnYes, a.OwnerYes, a.WrongRate, a.WrongLo, a.WrongHi)
		fmt.Fprintf(&b, "  - owner no called a correct drop: %d of %d, %.2f (%.2f-%.2f)\n", a.CorrectOnNo, a.OwnerNo, a.CorrectRate, a.CorrectLo, a.CorrectHi)
	}
	if skipped > 0 {
		fmt.Fprintf(&b, "- %d lines of the log were not valid rows and were skipped\n", skipped)
	}
	return b.String()
}

// short is the first 12 hex digits of a hash, or "none" for no hash.
func short(h string) string {
	if h == "" {
		return "none"
	}
	return h[:min(12, len(h))]
}
