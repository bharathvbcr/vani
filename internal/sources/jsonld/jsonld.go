// Package jsonld reads schema.org JobPosting blocks from a career site's job
// page: the <script type="application/ld+json"> blocks of the page, and the
// parts of a JobPosting that more than one source reads (its type, places,
// datePosted and baseSalary). It was TalentBrew's own reader; Meta's job pages
// carry the same kind of block. What a block must say to be a given site's
// job (its url, its company) stays with that site's source.
//
// The page is untrusted: nothing in it is executed, the scan is bounded
// (MaxBlocks, MaxTagLen), and a block that is not a JobPosting is passed over.
package jsonld

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	// MaxBlocks bounds the JSON-LD blocks read from one page, MaxTagLen the
	// opening tag of a script.
	MaxBlocks = 8
	MaxTagLen = 1024
)

// Blocks returns the bodies of the <script type="application/ld+json"> blocks
// of a page, at most MaxBlocks of them, in document order. It is a small
// tokenizer, not a parser (page.scanHTML is the same idea; that one is private
// to the page board and gathers every anchor of the page too): it finds each
// <script> opening tag, reads at most MaxTagLen bytes of it, takes the text up
// to the next </script>, and resumes after it, so a "<script" inside another
// script's text is never read as a tag. An opening tag or script that never
// ends stops the scan rather than being guessed at.
func Blocks(doc string) []string {
	var out []string
	for i := 0; i < len(doc) && len(out) < MaxBlocks; {
		j := htmltext.IndexFold(doc[i:], "<script")
		if j < 0 {
			break
		}
		k := i + j + len("<script")
		if k >= len(doc) {
			break
		}
		if c := doc[k]; c != '>' && c != '/' && c != ' ' && c != '\t' && c != '\n' && c != '\r' && c != '\f' {
			i = k // "<scripts", not a script tag
			continue
		}
		gt := strings.IndexByte(doc[k:min(len(doc), k+MaxTagLen)], '>')
		if gt < 0 {
			break
		}
		tag := htmltext.LowerASCII(doc[k : k+gt])
		bodyAt := k + gt + 1
		end := htmltext.IndexFold(doc[bodyAt:], "</script")
		if end < 0 {
			break
		}
		if strings.Contains(tag, "application/ld+json") {
			out = append(out, doc[bodyAt:bodyAt+end])
		}
		i = bodyAt + end
	}
	return out
}

// StringList is a JSON string or an array of strings, as schema.org allows
// for @type and employmentType.
type StringList []string

func (l *StringList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*l = StringList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("not a string or an array of strings")
	}
	*l = StringList(many)
	return nil
}

// Place is a schema.org Place of a JobPosting's jobLocation.
type Place struct {
	Address struct {
		Locality string `json:"addressLocality"`
		Region   string `json:"addressRegion"`
		Country  string `json:"addressCountry"`
	} `json:"address"`
}

// ControlToSpace replaces the raw control characters (U+0000 to U+001F) of a
// JSON-LD block with spaces. Citi's pages write a raw tab inside the
// description string ("<p>●\tDesign and configure API proxies…", job
// 102455422752, 2026-10-28), which is not JSON, so the block was passed over and
// the board failed every run. Outside strings these characters are whitespace
// or invalid, and inside them the text is HTML, where a space reads the same.
func ControlToSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 {
			return ' '
		}
		return r
	}, s)
}

// Decode decodes one block into v after ControlToSpace.
func Decode(block string, v any) error {
	return json.Unmarshal([]byte(ControlToSpace(strings.TrimSpace(block))), v)
}

// IsJobPosting reports whether an @type names JobPosting.
func IsJobPosting(t StringList) bool {
	for _, s := range t {
		if s == "JobPosting" {
			return true
		}
	}
	return false
}

// ParseDate reads datePosted: a date ("2026-10-27", taken as midnight UTC) or
// an RFC 3339 time ("2026-10-01T17:10:09-07:00", Meta); empty is the zero time.
func ParseDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("datePosted %q is neither a date nor an RFC 3339 time", s)
	}
	return t.UTC(), nil
}

// amount is a JSON number or a numeric string.
type amount struct {
	v  float64
	ok bool
}

func (a *amount) UnmarshalJSON(b []byte) error {
	s := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	a.v, a.ok = v, true
	return nil
}

// PayLine is the pay line for a schema.org baseSalary (MonetaryAmount: a
// currency and a QuantitativeValue with minValue, maxValue or value and a
// unitText), or "" when there is none or it is not well formed. Every
// TalentBrew page sampled carried the empty template (currency "", no amount,
// unitText ""), and Meta's pages carry no baseSalary, so a range was never seen
// in it live: the amount fields are schema.org's, read as unverified here.
// srcutil.PayLine refuses a currency that is not three letters, a non-positive
// amount and a bottom above the top.
func PayLine(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s struct {
		Currency string `json:"currency"`
		Value    struct {
			Min      amount `json:"minValue"`
			Max      amount `json:"maxValue"`
			Value    amount `json:"value"`
			UnitText string `json:"unitText"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	v := s.Value
	// srcutil.PayLine reads a bad bottom as a missing one and keeps the top;
	// here an amount that is given and unusable makes the whole range unwell
	// formed.
	for _, a := range []amount{v.Min, v.Max, v.Value} {
		if a.ok && (a.v <= 0 || math.IsInf(a.v, 0) || math.IsNaN(a.v)) {
			return ""
		}
	}
	var lo, hi *float64
	pick := func(a amount) *float64 {
		if a.ok {
			return &a.v
		}
		return nil
	}
	if v.Min.ok || v.Max.ok {
		lo, hi = pick(v.Min), pick(v.Max)
	} else {
		lo = pick(v.Value)
	}
	return srcutil.PayLine(s.Currency, lo, hi, v.UnitText)
}
