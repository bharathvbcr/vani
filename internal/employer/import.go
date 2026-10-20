// Package employer builds a local index of employer attributes from public
// lists, and looks a posting's company up to produce a badge:
//
//   - nonprofit: the organisation is on the IRS Exempt Organizations Business
//     Master File (EO BMF) extract.
//   - cap_exempt_likely: the organisation is a degree-granting public or
//     private non-profit institution in the NCES IPEDS directory, the core of
//     the H-1B cap exemption for institutions of higher education.
//
// A badge is a hint, never a filter and never a legal determination: name
// matching is fuzzy, and cap exemption also depends on facts no list holds
// (affiliation, the job's own employer of record). Names are matched with
// the LCA normaliser so the two badges agree on what "the same employer" is.
package employer

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/indexutil"
	"github.com/bharathvbcr/Vani/internal/lca"
)

// Employer attributes. They are the values config.employer_preferences and a
// track's employer_preferences.prefer accept.
const (
	AttrCapExemptLikely = "cap_exempt_likely"
	AttrNonprofit       = "nonprofit"
)

// Attributes lists every attribute, in display order.
var Attributes = []string{AttrCapExemptLikely, AttrNonprofit}

// attr is a bit set of attributes.
type attr uint8

const (
	bitCapExempt attr = 1 << iota
	bitNonprofit
	bitsAll = bitCapExempt | bitNonprofit
)

func (a attr) names() []string {
	var out []string
	if a&bitCapExempt != 0 {
		out = append(out, AttrCapExemptLikely)
	}
	if a&bitNonprofit != 0 {
		out = append(out, AttrNonprofit)
	}
	return out
}

// Source kinds, recognised from the file's header.
const (
	KindIRSBMF = "irs_eo_bmf" // IRS EO BMF extract (eo1.csv .. eo4.csv, eo_xx.csv)
	KindIPEDS  = "ipeds_hd"   // NCES IPEDS HDyyyy directory file
)

// Skip reasons recorded in SourceMeta.Skipped.
const (
	SkipRagged       = "ragged_row"          // field count differs from the header
	SkipEmpty        = "empty_name"          // name empty after normalisation
	SkipBadEIN       = "bad_ein"             // EIN is not nine digits
	SkipNotDegree    = "not_degree_granting" // IPEDS DEGGRANT is not 1
	SkipForProfit    = "for_profit"          // IPEDS CONTROL is 3 (or unknown)
	SkipBadAliasList = "alias_dropped"       // an IPEDS alias normalised to nothing or over the cap
)

// Limits bounds an import. Exceeding one is an error, never a truncation.
type Limits struct {
	MaxRows  int
	MaxNames int
}

// DefaultLimits covers the whole BMF (about 1.9 million rows across eo1-eo4)
// with headroom.
var DefaultLimits = Limits{MaxRows: 5_000_000, MaxNames: 4_000_000}

// maxAliasesPerRow bounds how many IPEDS aliases one institution may add.
const maxAliasesPerRow = 16

// SourceMeta describes one imported file.
type SourceMeta struct {
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	SHA256      string         `json:"sha256"`
	RowsRead    int            `json:"rows_read"`
	RowsCounted int            `json:"rows_counted"`
	Skipped     map[string]int `json:"skipped,omitempty"`
}

// Partial is the result of one import: normalised names and their
// attributes, no rows.
type Partial struct {
	Source SourceMeta
	Names  map[string]attr
}

// columns of either kind; -1 when absent.
type columns struct {
	kind                      string
	name, ein                 int // BMF
	unitid, alias, ctrl, degr int // IPEDS (name holds INSTNM)
}

func resolveColumns(header []string) (columns, error) {
	idx := make(map[string]int, len(header))
	for i, h := range header {
		k := indexutil.CanonHeader(h)
		if _, dup := idx[k]; !dup {
			idx[k] = i
		}
	}
	col := func(k string) int {
		if i, ok := idx[k]; ok {
			return i
		}
		return -1
	}
	bmf := col("EIN") >= 0 && col("NAME") >= 0 && col("SUBSECTION") >= 0
	ipeds := col("UNITID") >= 0 && col("INSTNM") >= 0
	switch {
	case bmf && ipeds:
		return columns{}, errors.New("employer: header has both IRS BMF and IPEDS columns; refusing to guess the file kind")
	case bmf:
		return columns{kind: KindIRSBMF, name: col("NAME"), ein: col("EIN")}, nil
	case ipeds:
		c := columns{kind: KindIPEDS, name: col("INSTNM"), unitid: col("UNITID"), alias: col("IALIAS"),
			ctrl: col("CONTROL"), degr: col("DEGGRANT")}
		var missing []string
		if c.ctrl < 0 {
			missing = append(missing, "CONTROL")
		}
		if c.degr < 0 {
			missing = append(missing, "DEGGRANT")
		}
		if len(missing) > 0 {
			return c, fmt.Errorf("employer: IPEDS file lacks column(s) %s (use the HDyyyy directory file)", strings.Join(missing, ", "))
		}
		return c, nil
	}
	return columns{}, errors.New("employer: unrecognised file: want an IRS EO BMF extract (EIN, NAME, SUBSECTION) or an IPEDS HDyyyy file (UNITID, INSTNM, CONTROL, DEGGRANT)")
}

// field returns rec[i] trimmed, decoding Windows-1252/Latin-1 bytes when the
// cell is not valid UTF-8 (IPEDS files have shipped in either encoding).
func field(rec []string, i int) string {
	s := rec[i]
	if !utf8.ValidString(s) {
		rs := make([]rune, len(s))
		for j := 0; j < len(s); j++ {
			rs[j] = rune(s[j])
		}
		s = string(rs)
	}
	return strings.TrimSpace(s)
}

func nineDigits(s string) bool {
	if len(s) != 9 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// splitAliases splits an IPEDS IALIAS cell on "|" and ";". The HD2024
// dictionary documents no delimiter ("aliases ... submitted by the
// institution"); in HD2024.csv institutions used "|" (211 cells), ";" (25)
// and "," (138). A comma is not a delimiter here because it also sits inside
// names: "Rutgers, The State University of New Jersey", and UCSF's
// "University of California, San Francisco", which split would alias "SAN
// FRANCISCO" to UCSF. Unsplit, a comma list normalises to one long key that
// matches nothing, which only costs recall.
func splitAliases(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '|' || r == ';' })
}

// Import reads one CSV with DefaultLimits.
func Import(r io.Reader, source string) (Partial, error) {
	return ImportWithLimits(r, source, DefaultLimits)
}

// ImportWithLimits streams an IRS EO BMF extract or an IPEDS HDyyyy file,
// recognised by header, into a Partial. Memory is bounded by MaxNames.
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
		return Partial{}, errors.New("employer: empty input, no header row")
	}
	if err != nil {
		return Partial{}, fmt.Errorf("employer: reading header: %w", err)
	}
	cols, err := resolveColumns(header)
	if err != nil {
		return Partial{}, err
	}
	width := len(header)
	p := Partial{
		Source: SourceMeta{Name: source, Kind: cols.kind, Skipped: map[string]int{}},
		Names:  map[string]attr{},
	}
	skip := func(reason string) { p.Source.Skipped[reason]++ }
	add := func(key string, a attr) error {
		if _, ok := p.Names[key]; !ok && len(p.Names) >= lim.MaxNames {
			return fmt.Errorf("employer: more than %d distinct names; refusing to import", lim.MaxNames)
		}
		p.Names[key] |= a
		return nil
	}

	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Partial{}, fmt.Errorf("employer: row %d: %w", p.Source.RowsRead+2, err)
		}
		p.Source.RowsRead++
		if p.Source.RowsRead > lim.MaxRows {
			return Partial{}, fmt.Errorf("employer: more than %d rows; refusing to import", lim.MaxRows)
		}
		if len(rec) != width {
			skip(SkipRagged)
			continue
		}
		key := lca.Normalize(field(rec, cols.name))
		if key == "" {
			skip(SkipEmpty)
			continue
		}
		switch cols.kind {
		case KindIRSBMF:
			if !nineDigits(field(rec, cols.ein)) {
				skip(SkipBadEIN)
				continue
			}
			if err := add(key, bitNonprofit); err != nil {
				return Partial{}, err
			}
		case KindIPEDS:
			if field(rec, cols.degr) != "1" {
				skip(SkipNotDegree)
				continue
			}
			var a attr
			// CONTROL 1 public, 2 private not-for-profit, 3 private
			// for-profit, -3 not available; DEGGRANT 1 degree-granting. Only
			// public and non-profit institutions meet HEA 101(a)(4)
			// (20 U.S.C. 1001(a)(4)), which INA 214(g)(5)(A) adopts.
			switch field(rec, cols.ctrl) {
			case "1": // public
				a = bitCapExempt
			case "2": // private not-for-profit
				a = bitCapExempt | bitNonprofit
			default: // 3 private for-profit, -3, or a code this build does not know
				skip(SkipForProfit)
				continue
			}
			if err := add(key, a); err != nil {
				return Partial{}, err
			}
			if cols.alias >= 0 {
				n := 0
				for _, al := range splitAliases(field(rec, cols.alias)) {
					if strings.TrimSpace(al) == "" {
						continue
					}
					ak := lca.Normalize(al)
					// A one-token alias ("UC", "MIT") is too short to tell
					// employers apart; only the full name stands alone.
					if ak == "" || len(strings.Fields(ak)) < 2 || n >= maxAliasesPerRow {
						skip(SkipBadAliasList)
						continue
					}
					n++
					if err := add(ak, a); err != nil {
						return Partial{}, err
					}
				}
			}
		}
		p.Source.RowsCounted++
	}
	// Rows read but none counted means the file's codes or layout are not
	// what this build expects (an IPEDS code change, the wrong HD variant):
	// an empty index would look like "no employer matches", so refuse.
	if p.Source.RowsRead > 0 && p.Source.RowsCounted == 0 {
		return Partial{}, fmt.Errorf("employer: %d rows read and none counted (skipped: %v); the file's layout or codes are not the documented ones", p.Source.RowsRead, p.Source.Skipped)
	}
	p.Source.SHA256 = hex.EncodeToString(h.Sum(nil))
	if len(p.Source.Skipped) == 0 {
		p.Source.Skipped = nil
	}
	return p, nil
}
