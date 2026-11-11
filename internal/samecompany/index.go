package samecompany

import (
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/marks"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

const (
	// ApplyWindowDays is how far back an application counts as "lately". Some
	// application systems silently reject a second application to one company
	// inside a window (Ashby's limit is reported at 3 per company per 60 days).
	// A constant, not a config key: it is a fact about those systems.
	ApplyWindowDays = 60
	applyWindow     = ApplyWindowDays * 24 * time.Hour

	// RepostMinGapDays is how long after the earlier listing a posting must first
	// appear to be a relisting. Roles listed together (one per city) are first
	// seen within this span of each other and are siblings, not reposts.
	RepostMinGapDays = 14
	// RepostMaxGapDays is how long after the earlier listing a relisting is still
	// recognised; a role back after a longer gap is treated as a new opening.
	RepostMaxGapDays = 90

	repostMinGap = RepostMinGapDays * 24 * time.Hour
	repostMaxGap = RepostMaxGapDays * 24 * time.Hour
)

// AppliedAt says whether the owner applied to the posting e belongs to, and
// when. It is the one place that decides what counts as "applied", so counting
// the stages that follow an application (screening, interview, offer, ...)
// later is a change to this function alone.
func AppliedAt(e marks.Entry) (time.Time, bool) {
	return e.MarkedAt, e.Mark == marks.Applied && !e.MarkedAt.IsZero()
}

// Prior is an earlier posting an answer points at: the one applied to, or the
// one a role was first listed as.
type Prior struct {
	Key   posting.Key
	Title string
	At    time.Time // when it was applied to, or first seen
}

// Applied is RepeatApply's answer: how many other postings at the same company
// were applied to inside the window, and the most recent of them.
type Applied struct {
	Count  int
	Latest Prior
}

// Groups is the half of an Index that reads only the records and the
// watchlist's domains: each posting's company identity, worked out in one pass,
// and a company's role groups, built the first time a row at that company is
// asked about. It reads nothing from the marks or the clock, so a long-lived
// reader (`vani jobs serve`) keeps one for as long as the ledger and the
// domains are unchanged and pairs it with each request's marks and clock
// (Index). It keeps a copy of the few fields it reads, never the records,
// whose descriptions a caller may have loaded. It is safe for concurrent use.
type Groups struct {
	co      *Companies
	all     []member           // every posting with a company identity, in record order
	members map[string][]int32 // company id -> indexes into all

	mu     sync.Mutex
	names  map[string]string // company text -> Name
	groups map[string]map[string]*roleGroup
	built  int // role groups built, for tests
}

// member is what Groups keeps of one record.
type member struct {
	key      posting.Key
	id       string
	title    string
	location string
	remote   bool
	first    time.Time
}

// Index answers both questions over one ledger snapshot: its Groups, and the
// owner's applications as of now. It is safe for concurrent use.
type Index struct {
	g           *Groups
	now         time.Time
	applied     map[string][]Prior
	appliedKeys map[posting.Key]bool // every applied key, in or out of the window
}

// New indexes recs against the owner's marks. now is the clock the window is
// measured from. A nil co identifies companies by name alone. Records with no
// company (and on no domain board) have no identity and match nothing.
func New(recs []ledger.Record, m map[posting.Key]marks.Entry, co *Companies, now time.Time) *Index {
	return NewGroups(recs, co).Index(m, now)
}

// NewGroups works out the company identity of every record in recs.
func NewGroups(recs []ledger.Record, co *Companies) *Groups {
	g := &Groups{co: co, members: map[string][]int32{}, names: map[string]string{},
		groups: map[string]map[string]*roleGroup{}}
	for _, r := range recs {
		id := g.id(r.Key, r.Posting.Company)
		if id == "" {
			continue
		}
		g.members[id] = append(g.members[id], int32(len(g.all)))
		g.all = append(g.all, member{key: r.Key, id: id, title: r.Posting.Title,
			location: r.Posting.Location, remote: r.Posting.Remote, first: r.FirstSeen})
	}
	return g
}

// Index pairs g with the owner's marks and the clock the apply window is
// measured from.
func (g *Groups) Index(m map[posting.Key]marks.Entry, now time.Time) *Index {
	ix := &Index{g: g, now: now, applied: map[string][]Prior{}, appliedKeys: map[posting.Key]bool{}}
	for _, mb := range g.all {
		if at, ok := AppliedAt(m[mb.key]); ok && !ix.appliedKeys[mb.key] {
			ix.appliedKeys[mb.key] = true
			ix.applied[mb.id] = append(ix.applied[mb.id], Prior{Key: mb.key, Title: mb.title, At: at})
		}
	}
	return ix
}

// Built is how many companies' role groups g has built: a test's evidence
// that a kept Groups was reused.
func (g *Groups) Built() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.built
}

// id is the company identity of a posting, "" for none. The caller holds mu
// (or is NewGroups, before g is shared).
func (g *Groups) id(k posting.Key, company string) string {
	if d := g.co.domainOfBoard(k); d != "" {
		return "d:" + d
	}
	n, ok := g.names[company]
	if !ok {
		n = Name(company)
		g.names[company] = n
	}
	if n == "" {
		return ""
	}
	if d := g.co.domainOfName(n); d != "" {
		return "d:" + d
	}
	return "n:" + n
}

// RepeatApply reports the postings the owner applied to at k's company within
// the window before now, other than k itself. ok is false when there are none,
// when the company is unknown, or when k is itself applied to (there is no
// application left to warn about).
func (ix *Index) RepeatApply(k posting.Key, company string) (a Applied, ok bool) {
	if ix == nil {
		return Applied{}, false
	}
	if ix.appliedKeys[k] {
		return Applied{}, false
	}
	ix.g.mu.Lock()
	id := ix.g.id(k, company)
	ix.g.mu.Unlock()
	if id == "" {
		return Applied{}, false
	}
	for _, p := range ix.applied[id] {
		age := ix.now.Sub(p.At)
		if p.Key == k || age < 0 || age > applyWindow {
			continue
		}
		a.Count++
		if a.Count == 1 || p.At.After(a.Latest.At) || p.At.Equal(a.Latest.At) && p.Key > a.Latest.Key {
			a.Latest = p
		}
	}
	return a, a.Count > 0
}

// relisted is one posting in a role group.
type relisted struct {
	key   posting.Key
	title string
	first time.Time
}

// roleGroup holds the postings of one company with one role title, sorted by
// first seen (then key): all of them, and by location, with "" holding those
// that name no place.
type roleGroup struct {
	all   []relisted
	byLoc map[string][]relisted
}

func (a relisted) cmp(b relisted) int {
	if c := a.first.Compare(b.first); c != 0 {
		return c
	}
	return strings.Compare(string(a.key), string(b.key))
}

// groupsOf builds (once) the role groups of company id. The caller holds mu.
func (g *Groups) groupsOf(id string) map[string]*roleGroup {
	if out, ok := g.groups[id]; ok {
		return out
	}
	out := map[string]*roleGroup{}
	seen := map[posting.Key]bool{}
	for _, i := range g.members[id] {
		mb := g.all[i]
		role := roleTitle(mb.title)
		if role == "" || mb.first.IsZero() || seen[mb.key] {
			continue
		}
		seen[mb.key] = true
		e := relisted{key: mb.key, title: mb.title, first: mb.first}
		rg := out[role]
		if rg == nil {
			rg = &roleGroup{byLoc: map[string][]relisted{}}
			out[role] = rg
		}
		rg.all = append(rg.all, e)
		loc := locationOf(mb.location, mb.remote)
		rg.byLoc[loc] = append(rg.byLoc[loc], e)
	}
	for _, rg := range out {
		slices.SortFunc(rg.all, relisted.cmp)
		for _, l := range rg.byLoc {
			slices.SortFunc(l, relisted.cmp)
		}
	}
	g.groups[id] = out
	g.built++
	return out
}

// Repost reports the earlier posting r is likely a relisting of: one at the
// same company with the same role title (pipeline.RoleTitle), at a compatible
// place (the same, or either side naming none), under another key, first seen
// at least RepostMinGapDays and at most RepostMaxGapDays before r. Of several
// such postings the most recently first seen is named. A posting first seen
// within the minimum gap of r is a sibling (one role listed per city), and the
// same key coming back is one posting, which the ledger's own identity rules
// already fold; neither is a repost. ok is false when there is none or r has no
// first-seen date.
func (ix *Index) Repost(r ledger.Record) (p Prior, ok bool) {
	if ix == nil || r.FirstSeen.IsZero() {
		return Prior{}, false
	}
	role := roleTitle(r.Posting.Title)
	if role == "" {
		return Prior{}, false
	}
	ix.g.mu.Lock()
	defer ix.g.mu.Unlock()
	id := ix.g.id(r.Key, r.Posting.Company)
	if id == "" {
		return Prior{}, false
	}
	rg := ix.g.groupsOf(id)[role]
	if rg == nil {
		return Prior{}, false
	}
	hi, lo := r.FirstSeen.Add(-repostMinGap), r.FirstSeen.Add(-repostMaxGap)
	var best relisted
	var found bool
	consider := func(list []relisted) {
		if q, ok := latestIn(list, r.Key, lo, hi); ok && (!found || q.cmp(best) > 0) {
			best, found = q, true
		}
	}
	if loc := locationOf(r.Posting.Location, r.Posting.Remote); loc == "" {
		consider(rg.all) // names no place: compatible with every listing
	} else {
		consider(rg.byLoc[loc])
		consider(rg.byLoc[""])
	}
	if !found {
		return Prior{}, false
	}
	return Prior{Key: best.key, Title: best.title, At: best.first}, true
}

// latestIn is the last entry of list (sorted by first seen) first seen in
// [lo, hi] and not under key skip.
func latestIn(list []relisted, skip posting.Key, lo, hi time.Time) (relisted, bool) {
	// The first entry first seen after hi; everything before it is a candidate.
	i := sort.Search(len(list), func(i int) bool { return list[i].first.After(hi) })
	for i--; i >= 0 && !list[i].first.Before(lo); i-- {
		if list[i].key != skip {
			return list[i], true
		}
	}
	return relisted{}, false
}

// roleTitle is the title a role is grouped by.
func roleTitle(title string) string { return pipeline.RoleTitle(title) }

// locationOf is a posting's place (its location text and whether the board
// lists it remote, the only fields pipeline.LooseLocation reads) folded for
// comparison, or "" when it names none (no location text and not remote),
// which is compatible with any place.
func locationOf(location string, remote bool) string {
	if strings.TrimSpace(location) == "" && !remote {
		return ""
	}
	return pipeline.LooseLocation(posting.Posting{Location: location, Remote: remote})
}
