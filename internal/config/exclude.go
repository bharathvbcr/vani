package config

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
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

// ReadsDescriptions reports whether Match reads a posting's description. When
// it does not, a posting held without its description (ledger.Record's
// BodyEvicted) is matched exactly as it would be with it.
func (x Exclude) ReadsDescriptions() bool { return len(x.Descriptions) > 0 }

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
		if x.unrecognisedOutside(p) {
			return "location is outside only_locations (" + UnrecognisedLocationMark + ")", true
		}
		return "location is outside only_locations", true
	}
	if len(x.Descriptions) > 0 {
		if d := fold(p.Description); d != "" && firstWord(d, x.Descriptions) != "" {
			return "description matches an excluded phrase", true
		}
	}
	return "", false
}

// LocationUnreadable reports whether x has a location rule it cannot apply to
// p: the location names no place and no remote work, or, with only_locations
// set, is remote with no place and no scope, or names only options that are
// ambiguous (location.VerdictAmbiguous: "Cambridge", "San Jose") besides ones
// outside the list. Match keeps such a posting, but it has not judged it, so
// the caller counts it as kept unchecked. A rule on another field never makes
// a posting unreadable here: a blank title or description is already counted
// by the stage that reads it (title_unread, empty_description).
func (x Exclude) LocationUnreadable(p posting.Posting) bool {
	if len(x.Locations) == 0 && len(x.OnlyLocations) == 0 {
		return false
	}
	o := optionsOf(p)
	if !o.read {
		return true
	}
	if len(x.OnlyLocations) > 0 && o.remote && o.scope == "" && len(o.opts) == 0 {
		return true
	}
	return x.onlyUnjudged(o)
}

// locationOptions are the options a posting's location gives: each option of
// each place (location.PlaceOptions: "Toronto, Austin" is two), and remote
// work as an option of its own when it says where ("Remote (US)") or the
// location names no place. "Remote - India" and "Germany · Remote" are remote
// within the place, which the place entries judge. read is false when the
// location names nothing Vani can read.
type locationOptions struct {
	opts   []location.Option
	remote bool   // remote work is an option of its own
	scope  string // its scope, "" when unstated
	read   bool
}

func optionsOf(p posting.Posting) locationOptions {
	loc := location.Parse(p.Location, p.Remote)
	o := locationOptions{read: len(loc.Places) > 0 || loc.Remote}
	for _, pl := range loc.Places {
		o.opts = append(o.opts, location.PlaceOptions(pl)...)
	}
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
	for _, opt := range o.opts {
		m := optionMatch(x.Locations, opt)
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
	if !o.read || (o.remote && o.scope == "" && len(o.opts) == 0) {
		return false
	}
	for _, opt := range o.opts {
		// An option as likely a US place as a foreign one ("Cambridge", "San
		// Jose") is not judged: the posting is kept.
		if optionMatch(x.OnlyLocations, opt) != "" || opt.Verdict == location.VerdictAmbiguous || stateUnread(x.OnlyLocations, opt) {
			return false
		}
	}
	return !o.remote || remoteMatch(x.OnlyLocations, o.scope, false) == ""
}

// UnrecognisedLocationMark is in the reason Match gives when only_locations
// dropped a posting at least one of whose places the location tables do not
// know: such a place reads as outside the list, but it may be a US town the
// tables lack (usCityNames in internal/location/us.go), so the drop is
// counted, listed and audited as a class of its own. The ledger keeps the
// reason, the audit class ("exclude:location_unrecognised") and the count
// derive from it, and a test pins the three together.
const UnrecognisedLocationMark = "unrecognised location"

// unrecognisedOutside reports whether a posting outsideOnly drops has a place
// the tables do not know among its options: the drop rests on a name nothing
// placed rather than on a place known to be elsewhere.
func (x Exclude) unrecognisedOutside(p posting.Posting) bool {
	for _, opt := range optionsOf(p).opts {
		if opt.Unrecognised {
			return true
		}
	}
	return false
}

// stateUnread reports whether an option is a US place whose state was not
// read ("Austin", "Seattle") while entries name a US state: the entry cannot
// be compared with it, so the option is neither inside nor outside the list.
// An entry naming the country matches such an option in optionMatch before
// this is asked.
func stateUnread(entries []string, o location.Option) bool {
	if o.Verdict != location.VerdictUS || o.State != "" {
		return false
	}
	for _, e := range entries {
		if _, ok := location.StateCode(e); ok {
			return true
		}
	}
	return false
}

// onlyUnjudged reports whether only_locations keeps a location only because
// an option it names is ambiguous (location.VerdictAmbiguous) or a US place
// with no state read (stateUnread) and no option matched an entry: kept, but
// not checked.
func (x Exclude) onlyUnjudged(o locationOptions) bool {
	if len(x.OnlyLocations) == 0 {
		return false
	}
	ambiguous := false
	for _, opt := range o.opts {
		if optionMatch(x.OnlyLocations, opt) != "" {
			return false
		}
		ambiguous = ambiguous || opt.Verdict == location.VerdictAmbiguous || stateUnread(x.OnlyLocations, opt)
	}
	if !ambiguous {
		return false
	}
	return !o.remote || remoteMatch(x.OnlyLocations, o.scope, false) == ""
}

// usPlace is matched by an entry naming the country of a place in the US,
// which the board need not have written.
const usPlace = "united states usa us"

// optionMatch returns the first entry that matches one option of a place, or
// "". A state entry ("TX", "Washington") compares with a US option's state
// code only, so "Washington" is not Washington, DC; on an option with no
// state (a foreign one) it is read as words like any other entry. "remote"
// never matches a place. An entry naming a country matches an option of that
// country whether or not the board wrote it: "United States" matches every
// option location.PlaceOptions reads as the US ("San Francisco", "US, CA,
// Santa Clara"), "Canada" matches "Toronto, Ontario". An option that is not
// the US never matches the US, whatever words it holds ("Remote - outside
// US" is not the US).
func optionMatch(entries []string, o location.Option) string {
	text := location.NormArea(o.Text)
	switch {
	case o.Verdict == location.VerdictUS:
		text += " " + usPlace
	case o.Country != "":
		text += " " + location.NormArea(o.Country)
	}
	for _, e := range entries {
		n := location.NormArea(e)
		if n == remoteEntry {
			continue
		}
		if code, ok := location.StateCode(e); ok && o.State != "" {
			if code == o.State {
				return e
			}
			continue
		}
		if namesUS(n) && o.Verdict != location.VerdictUS {
			continue
		}
		if containsWord(text, n) {
			return e
		}
	}
	return ""
}

// namesUS reports whether a folded entry names the United States.
func namesUS(n string) bool {
	switch n {
	case "united states", "usa", "us", "u s", "u s a", "united states of america", "america":
		return true
	}
	return false
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
		switch {
		case s == "us": // location.Parse's name for every US scope
			s = usPlace
		case !all && (s == "anywhere" || s == "north america" || s == "americas"):
			// A remote role open anywhere, or across the Americas, can be
			// done from the US: only_locations naming the US keeps it, as
			// location.InUS reads it. exclude.locations (all) does not widen
			// this way, so excluding the US never drops a worldwide role.
			s += " " + usPlace
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

// excludeList is one of Exclude's lists with its bound and the key its entries
// compare by.
type excludeList struct {
	name    string
	entries *[]string
	max     int
	fold    func(string) string
}

// lists names every list of x: the one table validate, Revision and Merge walk.
func (x *Exclude) lists() []excludeList {
	return []excludeList{
		{"exclude.titles", &x.Titles, MaxExcludeEntries, fold},
		{"exclude.title_unless", &x.TitleUnless, MaxExcludeEntries, fold},
		{"exclude.companies", &x.Companies, MaxExcludeEntries, fold},
		{"exclude.locations", &x.Locations, MaxExcludeEntries, location.NormArea},
		{"exclude.only_locations", &x.OnlyLocations, MaxExcludeEntries, location.NormArea},
		{"exclude.descriptions", &x.Descriptions, MaxExcludeDescriptions, fold},
	}
}

// ErrStale means the exclusions changed since the caller read them (the
// revision it holds no longer matches), so nothing was written. The caller
// reads again.
var ErrStale = errors.New("config: the exclusions changed since they were read; read them again")

// Revision identifies the exclusions' content: equal revisions mean equal
// lists, entries in order. A save that names the revision it read is refused
// with ErrStale when another writer changed them in between.
func (x Exclude) Revision() string {
	h := sha256.New()
	var n [8]byte
	for _, l := range x.lists() {
		binary.BigEndian.PutUint64(n[:], uint64(len(*l.entries)))
		h.Write(n[:])
		for _, e := range *l.entries {
			binary.BigEndian.PutUint64(n[:], uint64(len(e)))
			h.Write(n[:])
			h.Write([]byte(e))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Merge returns x without the entries of remove, then with the entries of add
// that it does not already hold. Each list compares entries by its own key
// (case and whitespace for phrases, the normalized area for locations); an
// entry with an empty key is never added. It is the additive form of a save:
// what the caller did not name is kept, so an entry another writer added in
// between survives. The result is not validated.
func (x Exclude) Merge(add, remove Exclude) Exclude {
	out := x.Clone()
	adds, removes := add.Clone(), remove.Clone()
	as, rs := adds.lists(), removes.lists()
	for i, l := range out.lists() {
		drop := map[string]bool{}
		for _, e := range *rs[i].entries {
			drop[l.fold(e)] = true
		}
		var kept []string
		have := map[string]bool{}
		for _, e := range *l.entries {
			if k := l.fold(e); !drop[k] {
				kept = append(kept, e)
				have[k] = true
			}
		}
		for _, e := range *as[i].entries {
			if k := l.fold(e); k != "" && !have[k] {
				have[k] = true
				kept = append(kept, e)
			}
		}
		*l.entries = kept
	}
	return out
}

// validate reports each bad list or entry through add.
func (x Exclude) validate(add func(string, ...any)) {
	for _, l := range x.lists() {
		if len(*l.entries) > l.max {
			add("%s has %d entries; at most %d", l.name, len(*l.entries), l.max)
		}
		seen := map[string]bool{}
		for i, v := range *l.entries {
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
