package lca

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/indexutil"
)

// Skip reasons recorded in SourceMeta.Skipped.
const (
	SkipRagged       = "ragged_row"           // field count differs from the header
	SkipStatus       = "status_not_certified" // Denied, Withdrawn, or an unknown status
	SkipVisa         = "visa_not_tracked"     // not H-1B, H-1B1 or E-3
	SkipWithdrawnAlt = "withdrawn_non_h1b"    // Certified-Withdrawn of an H-1B1/E-3 case
	SkipDate         = "bad_date"             // no parseable decision/received date
	SkipEmployer     = "empty_employer"       // name empty after normalisation
)

// Limits bounds an import. Exceeding either is an error, never a truncation.
type Limits struct {
	MaxRows      int
	MaxEmployers int
}

// DefaultLimits are the caps Import applies.
var DefaultLimits = Limits{MaxRows: 10_000_000, MaxEmployers: 2_000_000}

// Counts is the per-employer, per-fiscal-year tally.
type Counts struct {
	H1B          int // CASE_STATUS Certified, VISA_CLASS H-1B
	H1BWithdrawn int // CASE_STATUS Certified-Withdrawn, VISA_CLASS H-1B (kept apart)
	H1B1         int // Certified, VISA_CLASS H-1B1 Chile / Singapore
	E3           int // Certified, VISA_CLASS E-3 Australian
}

// SourceMeta describes one imported file.
type SourceMeta struct {
	Name        string         `json:"name"`
	SHA256      string         `json:"sha256"`
	RowsRead    int            `json:"rows_read"`
	RowsCounted int            `json:"rows_counted"`
	Skipped     map[string]int `json:"skipped,omitempty"`
}

// Partial is the aggregate of one import: counts only, no rows.
type Partial struct {
	Source    SourceMeta
	Employers map[string]map[int]Counts // normalised employer -> fiscal year -> counts
}

// Header aliases, matched after trim, upper-casing and space/hyphen -> "_".
// The FY2020-FY2025 names are from DOL's LCA record layouts. The older names
// (FY2009-FY2019 files) are historical variants kept as aliases only.
var (
	employerAliases = []string{"EMPLOYER_NAME", "LCA_CASE_EMPLOYER_NAME", "EMPLOYER_BUSINESS_NAME"}
	statusAliases   = []string{"CASE_STATUS", "STATUS"}
	visaAliases     = []string{"VISA_CLASS"}
	// dateAliases are tried per row in order; the first parseable cell wins.
	dateAliases = []string{"DECISION_DATE", "RECEIVED_DATE", "CASE_SUBMITTED", "LCA_CASE_SUBMIT"}
)

type columns struct {
	employer, status, visa int
	dates                  []int
}

func resolveColumns(header []string) (columns, error) {
	idx := make(map[string]int, len(header))
	for i, h := range header {
		k := indexutil.CanonHeader(h)
		if _, dup := idx[k]; !dup {
			idx[k] = i
		}
	}
	find := func(aliases []string) int {
		for _, a := range aliases {
			if i, ok := idx[a]; ok {
				return i
			}
		}
		return -1
	}
	c := columns{employer: find(employerAliases), status: find(statusAliases), visa: find(visaAliases)}
	for _, a := range dateAliases {
		if i, ok := idx[a]; ok {
			c.dates = append(c.dates, i)
		}
	}
	var missing []string
	if c.employer < 0 {
		missing = append(missing, employerAliases[0])
	}
	if c.status < 0 {
		missing = append(missing, statusAliases[0])
	}
	if c.visa < 0 {
		missing = append(missing, visaAliases[0])
	}
	if len(c.dates) == 0 {
		missing = append(missing, "DECISION_DATE (or RECEIVED_DATE)")
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("lca: required column(s) missing from header: %s", strings.Join(missing, ", "))
	}
	return c, nil
}

var dateLayouts = []string{
	"2006-01-02", "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02T15:04:05Z07:00",
	"2006/01/02", "1/2/2006", "1/2/2006 15:04", "1/2/2006 15:04:05", "1/2/2006 3:04:05 PM",
	"1/2/06", "2-Jan-2006", "Jan 2, 2006",
}

func parseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil && t.Year() >= 1990 && t.Year() <= 2100 {
			return t, true
		}
	}
	return time.Time{}, false
}

// FiscalYear returns the US federal fiscal year (Oct 1 - Sep 30) of t.
func FiscalYear(t time.Time) int {
	if t.Month() >= time.October {
		return t.Year() + 1
	}
	return t.Year()
}

// Import aggregates one CSV with DefaultLimits.
func Import(r io.Reader, source string) (Partial, error) {
	return ImportWithLimits(r, source, DefaultLimits)
}

// ImportWithLimits streams a CSV export of a DOL LCA disclosure file and
// aggregates certified-case counts per normalised employer per fiscal year.
// Columns are located by header name; memory is bounded by the employer cap.
func ImportWithLimits(r io.Reader, source string, lim Limits) (Partial, error) {
	h := sha256.New()
	br := bufio.NewReaderSize(io.TeeReader(r, h), 64<<10)
	if b, err := br.Peek(3); err == nil && bytes.Equal(b, []byte{0xEF, 0xBB, 0xBF}) {
		_, _ = br.Discard(3)
	}
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = true

	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return Partial{}, errors.New("lca: empty input, no header row")
	}
	if err != nil {
		return Partial{}, fmt.Errorf("lca: reading header: %w", err)
	}
	cols, err := resolveColumns(header)
	if err != nil {
		return Partial{}, err
	}
	width := len(header)

	p := Partial{
		Source:    SourceMeta{Name: source, Skipped: map[string]int{}},
		Employers: map[string]map[int]Counts{},
	}
	skip := func(reason string) { p.Source.Skipped[reason]++ }

	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Partial{}, fmt.Errorf("lca: row %d: %w", p.Source.RowsRead+2, err)
		}
		p.Source.RowsRead++
		if p.Source.RowsRead > lim.MaxRows {
			return Partial{}, fmt.Errorf("lca: more than %d rows; refusing to import", lim.MaxRows)
		}
		if len(rec) != width {
			skip(SkipRagged)
			continue
		}
		withdrawn := false
		switch strings.ToLower(strings.Join(strings.Fields(rec[cols.status]), "")) {
		case "certified":
		case "certified-withdrawn":
			withdrawn = true
		default:
			skip(SkipStatus)
			continue
		}
		visa := strings.ToUpper(strings.TrimSpace(rec[cols.visa]))
		kind := ""
		switch {
		case visa == "H-1B":
			kind = "h1b"
		case strings.HasPrefix(visa, "H-1B1"):
			kind = "h1b1"
		case strings.HasPrefix(visa, "E-3"):
			kind = "e3"
		default:
			skip(SkipVisa)
			continue
		}
		if withdrawn && kind != "h1b" {
			skip(SkipWithdrawnAlt)
			continue
		}
		var when time.Time
		ok := false
		for _, ci := range cols.dates {
			if when, ok = parseDate(rec[ci]); ok {
				break
			}
		}
		if !ok {
			skip(SkipDate)
			continue
		}
		key := Normalize(rec[cols.employer])
		if key == "" {
			skip(SkipEmployer)
			continue
		}
		byFY, exists := p.Employers[key]
		if !exists {
			if len(p.Employers) >= lim.MaxEmployers {
				return Partial{}, fmt.Errorf("lca: more than %d distinct employers; refusing to import", lim.MaxEmployers)
			}
			byFY = map[int]Counts{}
			p.Employers[key] = byFY
		}
		fy := FiscalYear(when)
		c := byFY[fy]
		switch {
		case kind == "h1b" && withdrawn:
			c.H1BWithdrawn++
		case kind == "h1b":
			c.H1B++
		case kind == "h1b1":
			c.H1B1++
		default:
			c.E3++
		}
		byFY[fy] = c
		p.Source.RowsCounted++
	}
	p.Source.SHA256 = hex.EncodeToString(h.Sum(nil))
	if len(p.Source.Skipped) == 0 {
		p.Source.Skipped = nil
	}
	return p, nil
}

// sortedKeys returns the map's keys in ascending order.
func sortedKeys[V any](m map[int]V) []int {
	ks := make([]int, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Ints(ks)
	return ks
}
