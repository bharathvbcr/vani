package rkbresume

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// The owner's header (name, contact lines) is rkb's own: it goes from the bank
// into the page and nowhere else. rkb's messages are forwarded, clipped, to the
// model on a retry and to the caller in an error, so a message that quoted a
// header value would carry it out. scrubber is the last gate on that path: it
// replaces every header string in text that came from rkb.
const (
	headerMask = "[header]"
	// A value shorter than this is left alone: replacing "US" would mangle
	// ordinary words and does not identify anyone.
	minScrubRunes = 3
	maxScrubRunes = 300
	maxScrubItems = 64
)

// scrubber's zero value replaces nothing.
type scrubber struct{ re *regexp.Regexp }

// newScrubber matches any of strs, longest first, ignoring case.
func newScrubber(strs []string) scrubber {
	seen := map[string]bool{}
	var keep []string
	for _, s := range strs {
		s = strings.TrimSpace(s)
		if n := utf8.RuneCountInString(s); n < minScrubRunes || n > maxScrubRunes || seen[s] {
			continue
		}
		seen[s] = true
		keep = append(keep, s)
	}
	if len(keep) == 0 {
		return scrubber{}
	}
	sort.Slice(keep, func(i, j int) bool {
		if len(keep[i]) != len(keep[j]) {
			return len(keep[i]) > len(keep[j])
		}
		return keep[i] < keep[j]
	})
	if len(keep) > maxScrubItems {
		keep = keep[:maxScrubItems]
	}
	parts := make([]string, len(keep))
	for i, s := range keep {
		parts[i] = regexp.QuoteMeta(s)
	}
	return scrubber{re: regexp.MustCompile(`(?i)` + strings.Join(parts, "|"))}
}

// clean returns s with every header string replaced by headerMask.
func (sc scrubber) clean(s string) string {
	if sc.re == nil || s == "" {
		return s
	}
	return sc.re.ReplaceAllLiteralString(s, headerMask)
}

// headerStrings lists every string of the header the base selection uses: its
// inline header object, or the bank header it names. kbsync.Bank does not carry
// headers, so a named one is read from the bank file here, only to be scrubbed
// from output; it is never sent anywhere.
func headerStrings(base *baseSel, bankPath string) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	raw := base.top["header"]
	var name string
	if json.Unmarshal(raw, &name) == nil {
		bank, err := readCapped(bankPath, kbsync.MaxBankBytes)
		if err != nil {
			return nil
		}
		var top struct {
			Headers map[string]json.RawMessage `json:"headers"`
		}
		if json.Unmarshal(bank, &top) != nil {
			return nil
		}
		raw = top.Headers[name]
	}
	var v any
	if json.Unmarshal(raw, &v) == nil {
		walk(v)
	}
	return out
}
