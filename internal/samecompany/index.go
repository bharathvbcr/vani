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

// Index answers both questions over one ledger snapshot, built once: company
// identity is worked out in a single pass over the records, and a company's
// role groups only when a row at that company is first asked about. It is safe
// for concurrent use.
type Index struct {
	now  time.Time
	co   *Companies
	recs []ledger.Record

	mu          sync.Mutex
	names       map[string]string // company text -> Name
	applied     map[string][]Prior
	appliedKeys map[posting.Key]bool // every applied key, in or out of the window
	members     map[string][]int32   // company id -> indexes into recs
	groups      map[string]map[string]*roleGroup
}

// New indexes recs against the owner's marks. now is the clock the window is
// measured from. A nil co identifies companies by name alone. Records with no
// company (and on no domain board) have no identity and match nothing.
func New(recs []ledger.Record, m map[posting.Key]marks.Entry, co *Companies, now time.Time) *Index {
	ix := &Index{now: now, co: co, recs: recs, names: map[string]string{},
		applied: map[string][]Prior{}, appliedKeys: map[posting.Key]bool{},
		members: map[string][]int32{}, groups: map[string]map[string]*roleGroup{}}
	for i, r := range recs {
		id := ix.id(r.Key, r.Posting.Company)
		if id == "" {
			continue
		}
		ix.members[id] = append(ix.members[id], int32(i))
		if at, ok := AppliedAt(m[r.Key]); ok && !ix.appliedKeys[r.Key] {
			ix.appliedKeys[r.Key] = true
			ix.applied[id] = append(ix.applied[id], Prior{Key: r.Key, Title: r.Posting.Title, At: at})
		}
	}
	return ix
}

// id is the company identity of a posting, "" for none. The caller holds mu
// (or is New, before the index is shared).
func (ix *Index) id(k posting.Key, company string) string {
	if d := ix.co.domainOfBoard(k); d != "" {
		return "d:" + d
	}
	n, ok := ix.names[company]
	if !ok {
		n = Name(company)
		ix.names[company] = n
	}
	if n == "" {
		return ""
	}
	if d := ix.co.domainOfName(n); d != "" {
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
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.appliedKeys[k] {
		return Applied{}, false
	}
	id := ix.id(k, company)
	if id == "" {
		return Applied{}, false
	}
	for _, p := range ix.applied[id] {
		if p.Key == k || ix.now.Sub(p.At) > applyWindow {
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
func (ix *Index) groupsOf(id string) map[string]*roleGroup {
	if g, ok := ix.groups[id]; ok {
		return g
	}
	g := map[string]*roleGroup{}
	seen := map[posting.Key]bool{}
	for _, i := range ix.members[id] {
		r := ix.recs[i]
		role := roleTitle(r.Posting.Title)
		if role == "" || r.FirstSeen.IsZero() || seen[r.Key] {
			continue
		}
		seen[r.Key] = true
		e := relisted{key: r.Key, title: r.Posting.Title, first: r.FirstSeen}
		rg := g[role]
		if rg == nil {
			rg = &roleGroup{byLoc: map[string][]relisted{}}
			g[role] = rg
		}
		rg.all = append(rg.all, e)
		loc := locationOf(r.Posting)
		rg.byLoc[loc] = append(rg.byLoc[loc], e)
	}
	for _, rg := range g {
		slices.SortFunc(rg.all, relisted.cmp)
		for _, l := range rg.byLoc {
			slices.SortFunc(l, relisted.cmp)
		}
	}
	ix.groups[id] = g
	return g
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
	ix.mu.Lock()
	defer ix.mu.Unlock()
	id := ix.id(r.Key, r.Posting.Company)
	if id == "" {
		return Prior{}, false
	}
	rg := ix.groupsOf(id)[role]
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
	if loc := locationOf(r.Posting); loc == "" {
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

// locationOf is a posting's place folded for comparison, or "" when it names
// none (no location text and not remote), which is compatible with any place.
func locationOf(p posting.Posting) string {
	if strings.TrimSpace(p.Location) == "" && !p.Remote {
		return ""
	}
	return pipeline.LooseLocation(p)
}
