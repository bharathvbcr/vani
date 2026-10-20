package config

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/location"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const (
	// MaxExcludeEntries bounds each of exclude's title, title_unless, company
	// and location lists.
	MaxExcludeEntries = 128
	// MaxExcludeDescriptions bounds exclude.descriptions, which is matched
	// against the whole description of every posting each run.
	MaxExcludeDescriptions = 32
	// MaxExcludeEntryLen bounds one entry, in bytes.
	MaxExcludeEntryLen = 100
)

// remoteEntry is the location entry that excludes the remote option itself.
const remoteEntry = "remote"

// Exclude is the owner's hard filter: a posting it matches is dropped before
// any decision backend is asked, and a kept posting it comes to match is
// dropped at the next run (pipeline.ApplyExclusions). It is an engine rule on
// the posting's own fields, never on a model's reading. Tracks leave it alone.
//
// Phrases match as Rank's title signals do: case and runs of whitespace are
// ignored, and a phrase matches only as whole words ("intern" does not match
// "Internal" or "Internship", so list each form you mean).
type Exclude struct {
	// Titles drops a posting whose title contains any phrase ("intern",
	// "co-op", "internship").
	Titles []string `json:"titles,omitempty"`
	// TitleUnless keeps a title Titles matched when it also contains one of
	// these phrases ("intern" with unless "internal tools"). It lifts only the
	// title exclusion.
	TitleUnless []string `json:"title_unless,omitempty"`
	// Companies drops a posting whose company is one of these names, ignoring
	// case and runs of whitespace (exact names, not phrases).
	Companies []string `json:"companies,omitempty"`
	// Locations drops a posting only when every option it lists matches an
	// entry, so a role listed in an excluded place and an acceptable one is
	// kept. The options are its places, and remote work when the location
	// says where ("Remote (US)") or names no place ("Remote"); "Remote -
	// India" is remote within India, judged by the place. An entry matches a
	// place's area or region as whole words ("London" matches "New London,
	// CT"), a US place's state by code or name ("TX", "Texas"; "Washington"
	// is the state, not Washington, DC), and "United States", "USA" or "US"
	// any US place. "remote" matches the remote option; any other entry
	// matches a remote option whose scope it names. A posting whose location
	// names nothing is kept.
	Locations []string `json:"locations,omitempty"`
	// OnlyLocations, when set, keeps a posting only if some option its
	// location lists matches an entry (entries match as in Locations; a
	// remote scope of several places needs only one), so ["United States",
	// "Canada"] drops a role listed only in London. "remote" keeps every role
	// whose remote option stands on its own. A posting whose location names
	// nothing, or is remote with no place and no scope, is kept.
	OnlyLocations []string `json:"only_locations,omitempty"`
	// Descriptions drops a posting whose description contains any phrase.
	// The ledger records only that a description phrase matched, never which:
	// a phrase can say what the owner may not do.
	Descriptions []string `json:"descriptions,omitempty"`
}

// Empty reports whether nothing is excluded. TitleUnless alone excludes
// nothing.
func (x Exclude) Empty() bool {
	return len(x.Titles) == 0 && len(x.Companies) == 0 && len(x.Locations) == 0 && len(x.OnlyLocations) == 0 &&
		len(x.Descriptions) == 0
}

// IsZero reports whether every list is empty, so the config omits exclude
// whether its lists are nil or empty.
func (x Exclude) IsZero() bool { return x.Empty() && len(x.TitleUnless) == 0 }

// Match reports why x drops p: which field matched and, but for a description,
// the entry that did. ok is false when x keeps p.
func (x Exclude) Match(p posting.Posting) (why string, ok bool) {
	if t := fold(p.Title); t != "" {
		if m := firstWord(t, x.Titles); m != "" && firstWord(t, x.TitleUnless) == "" {
			return fmt.Sprintf("title matches %q", m), true
		}
	}
	if c := fold(p.Company); c != "" {
		for _, n := range x.Companies {
			if fold(n) == c {
				return fmt.Sprintf("company is %q", n), true
			}
		}
	}
	if m := x.locationMatch(p); m != "" {
		return fmt.Sprintf("location matches %q", m), true
	}
	if x.outsideOnly(p) {
		return "location is outside only_locations", true
	}
	if len(x.Descriptions) > 0 {
		if d := fold(p.Description); d != "" && firstWord(d, x.Descriptions) != "" {
			return "description matches an excluded phrase", true
		}
	}
	return "", false
}

// locationOptions are the options a posting's location gives: its places,
// and remote work as an option of its own when it says where ("Remote (US)")
// or the location names no place. "Remote - India" and "Germany · Remote" are
// remote within the place, which the place entries judge. read is false when
// the location names nothing Vani can read.
type locationOptions struct {
	places []location.Place
	remote bool   // remote work is an option of its own
	scope  string // its scope, "" when unstated
	read   bool
}

func optionsOf(p posting.Posting) locationOptions {
	loc := location.Parse(p.Location, p.Remote)
	o := locationOptions{places: loc.Places, read: len(loc.Places) > 0 || loc.Remote}
	if loc.Remote && (loc.RemoteScope != "" || len(loc.Places) == 0) {
		o.remote, o.scope = true, loc.RemoteScope
	}
	return o
}

// locationMatch returns the first exclude.locations entry that matched when
// every option the posting's location gives matches one, else "".
func (x Exclude) locationMatch(p posting.Posting) string {
	if len(x.Locations) == 0 {
		return ""
	}
	o := optionsOf(p)
	if !o.read {
		return ""
	}
	first := ""
	for _, pl := range o.places {
		m := placeMatch(x.Locations, pl)
		if m == "" {
			return ""
		}
		if first == "" {
			first = m
		}
	}
	if o.remote {
		m := remoteMatch(x.Locations, o.scope, true)
		if m == "" {
			return ""
		}
		if first == "" {
			first = m
		}
	}
	return first
}

// outsideOnly reports whether a posting's location is outside
// exclude.only_locations: it names something readable and no option matches
// an entry. Remote work with no scope and no place is unknown, so it is kept.
func (x Exclude) outsideOnly(p posting.Posting) bool {
	if len(x.OnlyLocations) == 0 {
		return false
	}
	o := optionsOf(p)
	if !o.read || (o.remote && o.scope == "" && len(o.places) == 0) {
		return false
	}
	for _, pl := range o.places {
		if placeMatch(x.OnlyLocations, pl) != "" {
			return false
		}
	}
	return !o.remote || remoteMatch(x.OnlyLocations, o.scope, false) == ""
}

// usPlace is matched by an entry naming the country of a place with a US
// state, which the board need not have written.
const usPlace = "united states usa us"

// placeMatch returns the first entry that matches pl, or "". A state entry
// ("TX", "Washington") compares with a US place's state code only, so
// "Washington" is not Washington, DC; on a place with no state (a foreign
// one) it is read as words like any other entry. "remote" never matches a
// place.
func placeMatch(entries []string, pl location.Place) string {
	parts := []string{pl.Area, pl.Region}
	if pl.State != "" {
		parts = append(parts, usPlace)
	}
	text := location.NormArea(strings.Join(parts, " "))
	for _, e := range entries {
		n := location.NormArea(e)
		if n == remoteEntry {
			continue
		}
		if code, ok := location.StateCode(e); ok && pl.State != "" {
			if code == pl.State {
				return e
			}
			continue
		}
		if containsWord(text, n) {
			return e
		}
	}
	return ""
}

// remoteMatch returns the first entry that matches a remote option with this
// scope, or "": "remote", or entries matching the places the scope lists,
// every one of them when all is set ("US, Canada" is excluded only when both
// are) and any one otherwise (it is kept when either is).
func remoteMatch(entries []string, scope string, all bool) string {
	for _, e := range entries {
		if location.NormArea(e) == remoteEntry {
			return e
		}
	}
	if strings.TrimSpace(scope) == "" {
		return ""
	}
	first := ""
	for _, part := range strings.Split(scope, ",") {
		s := location.NormArea(part)
		if s == "us" { // location.Parse's name for every US scope
			s = usPlace
		}
		m := ""
		for _, e := range entries {
			if n := location.NormArea(e); s != "" && containsWord(s, n) {
				m = e
				break
			}
		}
		switch {
		case m == "" && all:
			return ""
		case m != "" && !all:
			return m
		}
		if first == "" {
			first = m
		}
	}
	return first
}

// validate reports each bad list or entry through add.
func (x Exclude) validate(add func(string, ...any)) {
	for _, l := range []struct {
		name    string
		entries []string
		max     int
		fold    func(string) string
	}{
		{"exclude.titles", x.Titles, MaxExcludeEntries, fold},
		{"exclude.title_unless", x.TitleUnless, MaxExcludeEntries, fold},
		{"exclude.companies", x.Companies, MaxExcludeEntries, fold},
		{"exclude.locations", x.Locations, MaxExcludeEntries, location.NormArea},
		{"exclude.only_locations", x.OnlyLocations, MaxExcludeEntries, location.NormArea},
		{"exclude.descriptions", x.Descriptions, MaxExcludeDescriptions, fold},
	} {
		if len(l.entries) > l.max {
			add("%s has %d entries; at most %d", l.name, len(l.entries), l.max)
		}
		seen := map[string]bool{}
		for i, v := range l.entries {
			k := l.fold(v)
			switch {
			case !utf8.ValidString(v):
				add("%s[%d] is not valid UTF-8", l.name, i)
			case strings.ContainsFunc(v, unicode.IsControl):
				add("%s[%d] contains a control character", l.name, i)
			case len(v) > MaxExcludeEntryLen:
				add("%s[%d] is over %d bytes", l.name, i, MaxExcludeEntryLen)
			case k == "":
				// A location entry is folded to its letters and digits; one with
				// none would match nothing.
				add("%s[%d] is empty", l.name, i)
			case seen[k]:
				add("%s[%d]: duplicate %q", l.name, i, v)
			}
			seen[k] = true
		}
	}
	if len(x.TitleUnless) > 0 && len(x.Titles) == 0 {
		add("exclude.title_unless needs exclude.titles: it only lifts a title exclusion")
	}
}

// Clone returns a deep copy, so a copied config shares no slice with x.
func (x Exclude) Clone() Exclude {
	return Exclude{
		Titles: slices.Clone(x.Titles), TitleUnless: slices.Clone(x.TitleUnless),
		Companies: slices.Clone(x.Companies), Locations: slices.Clone(x.Locations),
		OnlyLocations: slices.Clone(x.OnlyLocations), Descriptions: slices.Clone(x.Descriptions),
	}
}
