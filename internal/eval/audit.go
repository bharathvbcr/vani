package eval

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// Default weekly audit quotas (owner-approved: 15 postings, 5 scored and 10
// dropped). AuditClosed adds two postings the engine closed (dead or
// duplicate), which no other stratum ever draws.
const (
	AuditScored  = 5  // by score decile across the scored pool
	AuditDropped = 10 // across drop classes
	AuditClosed  = 2  // across closed statuses (dead, duplicate)
)

// AuditQuotas are the places of one draw per group. A group's places are
// never lent to another (that would change the inclusion probabilities).
// Disagree is CellTeacherWants' places and DisagreeRejects CellTeacherRejects';
// each cell is a group of its own, so a short cell lends nothing. At 0 a cell
// is not drawn. Fable's second review (2026-11-13) kept the rejects cell at 0
// unless asked for: a teacher that rejects most of what it reads makes it a
// large slice of the top decile rather than a rare disagreement, and its
// labels are kept out of every estimate. The owner turned every overlay on by
// default (2026-11-13), so `vani audit` gives it a small default of its own.
//
// Neighbour is CellYesNeighbour's places. Engineering is the engineering
// group's (EngineeringPrefix): a random draw from the engineering-titled
// postings the other random groups did not take, laid over them like the
// cells, to gather the engineering gold the auditor's go/no-go is judged on.
type AuditQuotas struct {
	Scored, Dropped, Closed, Disagree, DisagreeRejects, Neighbour, Engineering int
}

// DefaultAuditQuotas are the weekly draw's random quotas. The overlays are on
// by default too, but their defaults live with `vani audit`
// (cmd/vani/auditcmd.go): whether an overlay can run depends on state the
// command reads (a teacher log, a vector store, the kill rule), so a default
// overlay is skipped by name there when it cannot.
var DefaultAuditQuotas = AuditQuotas{Scored: AuditScored, Dropped: AuditDropped, Closed: AuditClosed}

// Cells is the places drawn because of a model's reading or a posting's
// nearness to the owner's yeses: the disagreement cells and the yes-neighbour
// cell.
func (q AuditQuotas) Cells() int { return q.Disagree + q.DisagreeRejects + q.Neighbour }

// RandomShareOK reports whether at least a third of q's places are drawn from
// the random strata the estimates use (scored and dropped) rather than the
// cells (Cells), as the plan keeps them. Closed and engineering places count
// on neither side: their labels are in no ranking or recall estimate, and
// they are not chosen on a reading either. It checks quotas; a random group
// drawn short can still leave the draw under a third (AuditPlan.RandomShare
// says what was drawn).
func (q AuditQuotas) RandomShareOK() bool {
	return q.Cells() <= 2*(q.Scored+q.Dropped)
}

// TeacherView is a teacher model's role_fit reading of a candidate, as the
// disagreement cells read it.
type TeacherView int8

const (
	// TeacherUnread is no usable reading (or one the caller keeps out).
	TeacherUnread TeacherView = iota
	// TeacherWants is a reading among PositiveReadings.
	TeacherWants
	// TeacherRejects is any other answered reading except adjacent.
	TeacherRejects
)

// TeacherViewOf is how the disagreement cells read one teacher row's role_fit
// slot: TeacherWants for an answered reading among PositiveReadings,
// TeacherRejects for any other answered reading but adjacent (a near miss is
// no disagreement), TeacherUnread for a row without the slot, an unanswered
// reading or an empty value.
func TeacherViewOf(row TeacherRow) TeacherView {
	sl, ok := row.Slots["role_fit"]
	if !ok || sl.Reading != string(decide.Answered) || sl.Value == "" {
		return TeacherUnread
	}
	switch {
	case PositiveReadings[sl.Value]:
		return TeacherWants
	case sl.Value == "adjacent":
		return TeacherUnread
	}
	return TeacherRejects
}

// AuditCandidate is a posting the weekly audit may draw: a kept one with its
// priority score, a dropped one with its DropClass, or a closed one (Closed,
// with Class its status: "dead", or "duplicate:<method>" for a duplicate).
// Teacher is the teacher's reading the disagreement cells use; a closed
// posting's is never read.
type AuditCandidate struct {
	Key     posting.Key
	Score   float64
	Dropped bool
	Closed  bool
	Class   string
	Teacher TeacherView
	// NearYes marks a member of the yes-neighbour pool (CellYesNeighbour): the
	// caller's choice of the unlabelled postings nearest the owner's yes
	// postings. Never set on a closed posting.
	NearYes bool
	// Engineering marks a posting the engineering group may draw: an
	// engineering title, not closed, in no stratum the auditor leaves out
	// (the caller's test).
	Engineering bool
}

// The engineering group's strata: engineering-titled kept and dropped
// postings. A label drawn there is a random sample of the engineering
// postings, not of the pool, so it is kept out of the relevance strata, drop
// recall, the reader table and the weight fit (EngineeringStratum), and it is
// counted in the auditor's evaluation set, whose go/no-go is on engineering
// gold (second Fable review, third pass).
const (
	EngineeringPrefix  = "engineering:"
	EngineeringKept    = EngineeringPrefix + "kept"
	EngineeringDropped = EngineeringPrefix + "dropped"
)

// EngineeringStratum reports whether a label was drawn by the engineering group.
func EngineeringStratum(source string) bool { return strings.HasPrefix(source, EngineeringPrefix) }

// The yes-neighbour cell (second Fable plan, enhancement 6; built to its
// second review's terms): unlabelled postings nearest the owner's yes
// postings, an overlay like the disagreement cells. Its labels were drawn
// for their nearness to yeses, so SelectedOnReading keeps them out of every
// estimate as it does the cells'.
const (
	NeighbourPrefix  = "neighbour:"
	CellYesNeighbour = NeighbourPrefix + "yes"
)

// The disagreement cells (stage 5 of build/fable-plan-2026-11-12-ranking.md):
// where the teacher's reading and the engine disagree, which is where a yes the
// engine missed is likeliest. A posting the teacher wants that was dropped or
// sits in the lowest three score deciles is in CellTeacherWants; one the
// teacher rejects in the top decile is in CellTeacherRejects. The cells are an
// overlay: their members stay in their random stratum, which draws as if the
// cells did not exist (AuditDraw), so no random stratum loses the postings
// likeliest to be a missed yes.
//
// A label drawn from them was chosen because of a model's reading, so it is
// kept out of every estimate that takes the audit's strata as a random sample
// (SelectedOnReading); drop recall counts it as a known outcome, as a mark.
const (
	DisagreePrefix     = "disagree:"
	CellTeacherWants   = DisagreePrefix + "teacher_wants_dropped_or_low"
	CellTeacherRejects = DisagreePrefix + "teacher_rejects_top"
	// lowDeciles is how many of the lowest score deciles CellTeacherWants takes.
	lowDeciles = 3
)

// SelectedOnReading reports whether a label's source is a disagreement cell:
// drawn because of a model's reading, so it says nothing unbiased about that
// model or the score. The relevance report, drop recall, the reader table, the
// auditor gates and the weight fit leave such labels out and count them apart.
func SelectedOnReading(source string) bool {
	return strings.HasPrefix(source, DisagreePrefix) || strings.HasPrefix(source, NeighbourPrefix)
}

// Audit cell and stratum prefixes. A kept posting's stratum is its score
// decile ("decile:N"); for the recall estimate all kept postings are one cell.
const (
	CellKept       = "kept"
	DroppedPrefix  = "dropped:"
	ClosedPrefix   = "closed:"
	unknownDropped = DroppedPrefix + "unknown:other"
	// PrefilterStratumPrefix starts the stratum of a row dropped at listing
	// time, before the ledger: "dropped:prefilter:<rule>" (the class
	// pipeline.PrefilterClassPrefix names, filed as every drop class is).
	PrefilterStratumPrefix = DroppedPrefix + "prefilter:"
)

// droppedSource is the stratum of a dropped posting of class c.
func droppedSource(c string) string {
	if src := DroppedPrefix + c; c != "" && validSource(src) {
		return src
	}
	return unknownDropped
}

// closedSource is the stratum of a closed posting whose class is c: its status
// ("dead", "duplicate"), and for a duplicate the method that found it
// ("duplicate:shingle", from the dup.method annotation), so that a wrongly
// folded duplicate is told apart from a wrongly dead posting and one fold
// method from another. Each part is made a safe token; a bare status is a
// stratum of its own (labels written before the split carry it).
func closedSource(c string) string {
	var parts []string
	for _, p := range strings.Split(c, ":") {
		if t := classToken(p); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) == 0 {
		return ClosedPrefix + "other"
	}
	if src := ClosedPrefix + strings.Join(parts, ":"); validSource(src) {
		return src
	}
	// Too long to be a label source (a runaway dup.method): keep the status.
	if t := classToken(parts[0]); t != "" && validSource(ClosedPrefix+t) {
		return ClosedPrefix + t + ":other"
	}
	return ClosedPrefix + "other"
}

// AuditCell is the cell c stands in for when labels are weighted up to the
// pool: CellKept, its dropped stratum, or its closed stratum. Unlike the draw's
// strata, kept postings are one cell: deciles are re-cut every week, so a
// label's decile is not where its posting sits now.
func AuditCell(c AuditCandidate) string {
	switch {
	case c.Closed:
		return closedSource(c.Class)
	case c.Dropped:
		return droppedSource(c.Class)
	}
	return CellKept
}

// AuditPick is a drawn posting, the stratum it stands for, and its inclusion
// probability in this draw (see AuditSample).
type AuditPick struct {
	Key     posting.Key
	Source  string
	PSelect float64
}

// AuditStratum is one non-empty stratum of the draw: how many unlabelled
// postings it held and how many were drawn.
type AuditStratum struct {
	Source string
	Pool   int
	Taken  int
}

// AuditPlan is the draw: the picks, and every non-empty stratum with its counts,
// so a short draw is visible as one.
type AuditPlan struct {
	Picks  []AuditPick
	Strata []AuditStratum
}

// AuditSample draws the week's audit. nScored postings are spread over the ten
// score deciles of the scored pool ("decile:10" is the highest) and nDropped over
// the drop classes ("dropped:<class>"). Postings that already have a label (any
// entry in labeled) are never drawn, a key listed twice counts once, and a
// quota is never lent from one group to the other (that would change the
// inclusion probabilities below). A group with fewer unlabelled postings than
// its quota is drawn short, not padded.
//
// Within a group, places go round-robin to the non-empty strata; when the
// places run out inside a round, the strata served are a window that walks
// round the strata in name order, `places` further each ISO week (every
// stratum is served within ceil(strata/places) weeks while the set of strata
// holds still), or, for a seed that is not a week, the first in a hash of seed
// and stratum name. The scored deciles always use the hash order. Within a stratum the draw is uniform: the first members
// in a hash of seed and key. Everything is a function of (candidates, labels,
// seed), so the same week draws the same postings in any input order.
//
// PSelect is the posting's probability of being drawn over the seed: the
// expected places its stratum gets, over the stratum's unlabelled size. It is
// relative to the unlabelled pool at the time of the draw. Deciles are cut over
// the whole scored pool, labelled or not, so a stratum keeps its meaning as
// labels accumulate.
func AuditSample(cands []AuditCandidate, labeled map[posting.Key]Entry, seed string, nScored, nDropped int) AuditPlan {
	return AuditDraw(cands, labeled, seed, AuditQuotas{Scored: nScored, Dropped: nDropped})
}

// AuditDraw is AuditSample with a third group: q.Closed places over the closed
// statuses ("closed:dead", "closed:duplicate:<method>"), drawn as the drop
// classes are.
// The scored and dropped groups draw exactly what AuditSample draws for the
// same seed, whatever q.Closed is: each group spends only its own places.
//
// With cell places (q.Disagree for CellTeacherWants, q.DisagreeRejects for
// CellTeacherRejects) the disagreement cells are laid over the others: the
// random groups are bucketed and drawn exactly as without them, so their picks
// and every p_select they carry are the same whatever the cell quotas are, and
// each cell then draws its own places from its members the random groups did
// not take. A cell pick's p_select is its cell's share (taken over members
// left), descriptive only: those labels are kept out of every estimate
// (SelectedOnReading). A short or empty cell lends its places to nothing.
func AuditDraw(cands []AuditCandidate, labeled map[posting.Key]Entry, seed string, q AuditQuotas) AuditPlan {
	deciles, classes, shut, disagree := auditBuckets(cands, labeled)
	var plan AuditPlan
	drawGroup(&plan, deciles, q.Scored, seed, false)
	drawGroup(&plan, classes, q.Dropped, seed, true)
	if q.Closed > 0 {
		drawGroup(&plan, shut, q.Closed, seed, true)
	}
	if q.Engineering+q.Cells() == 0 {
		return plan
	}
	// The overlays draw in a fixed order, each from what no earlier group
	// took: engineering (random, so its pool stays uniform), then the
	// disagreement cells, then the yes neighbours.
	picked := map[posting.Key]bool{}
	notPicked := func(ks []posting.Key) []posting.Key {
		for _, p := range plan.Picks {
			picked[p.Key] = true
		}
		return slices.DeleteFunc(slices.Clone(ks), func(k posting.Key) bool { return picked[k] })
	}
	if q.Engineering > 0 {
		eng := map[string][]posting.Key{}
		for _, c := range overlayMembers(cands, labeled, func(c AuditCandidate) bool { return c.Engineering }) {
			src := EngineeringKept
			if c.Dropped {
				src = EngineeringDropped
			}
			eng[src] = append(eng[src], c.Key)
		}
		for name, ks := range eng {
			if eng[name] = notPicked(ks); len(eng[name]) == 0 {
				delete(eng, name)
			}
		}
		drawGroup(&plan, eng, q.Engineering, seed, false)
	}
	near := overlayMembers(cands, labeled, func(c AuditCandidate) bool { return c.NearYes })
	for _, cell := range []struct {
		name    string
		places  int
		members []posting.Key
	}{
		{CellTeacherWants, q.Disagree, disagree[CellTeacherWants]},
		{CellTeacherRejects, q.DisagreeRejects, disagree[CellTeacherRejects]},
		{CellYesNeighbour, q.Neighbour, candidateKeys(near)},
	} {
		if cell.places == 0 {
			continue
		}
		if left := notPicked(cell.members); len(left) > 0 {
			drawGroup(&plan, map[string][]posting.Key{cell.name: left}, cell.places, seed, false)
		}
	}
	return plan
}

// overlayMembers is the unlabelled, open (not closed) candidates in, each key
// once.
func overlayMembers(cands []AuditCandidate, labeled map[posting.Key]Entry, in func(AuditCandidate) bool) []AuditCandidate {
	var out []AuditCandidate
	seen := map[posting.Key]bool{}
	for _, c := range cands {
		if c.Key == "" || seen[c.Key] || c.Closed || !in(c) {
			continue
		}
		seen[c.Key] = true
		if _, done := labeled[c.Key]; !done {
			out = append(out, c)
		}
	}
	return out
}

func candidateKeys(cs []AuditCandidate) []posting.Key {
	out := make([]posting.Key, len(cs))
	for i, c := range cs {
		out[i] = c.Key
	}
	return out
}

// RandomShare is the drawn picks from the random strata the estimates use
// (deciles and drop classes) and all the picks, for a caller that reports
// how much of a short draw stayed random.
func (p AuditPlan) RandomShare() (random, all int) {
	for _, pk := range p.Picks {
		if strings.HasPrefix(pk.Source, "decile:") || strings.HasPrefix(pk.Source, DroppedPrefix) {
			random++
		}
	}
	return random, len(p.Picks)
}

// AuditStrata is every stratum the audit draws from, with its members: the
// score deciles ("decile:N", cut over the whole scored pool), the drop classes
// ("dropped:<class>") and the closed statuses ("closed:<status>"), as AuditDraw
// buckets them, for a caller that pairs postings within a stratum. A posting
// with an entry in labeled is left out, so pass nil to keep the labelled ones.
// Members are in no particular order; a key listed twice counts once.
func AuditStrata(cands []AuditCandidate, labeled map[posting.Key]Entry) map[string][]posting.Key {
	deciles, classes, shut, _ := auditBuckets(cands, labeled)
	out := make(map[string][]posting.Key, len(deciles)+len(classes)+len(shut))
	for _, m := range []map[string][]posting.Key{deciles, classes, shut} {
		for name, ks := range m {
			out[name] = ks
		}
	}
	return out
}

// AuditStratumOf is AuditStrata turned around: the stratum each candidate is
// filed under, labelled or not (deciles are cut over the whole scored pool).
// It draws nothing; the strata comparison (internal/clusterstudy) reads the
// audit's current cells from it.
func AuditStratumOf(cands []AuditCandidate) map[posting.Key]string {
	out := make(map[posting.Key]string, len(cands))
	for name, ks := range AuditStrata(cands, nil) {
		for _, k := range ks {
			out[k] = name
		}
	}
	return out
}

// auditBuckets sorts the unlabelled candidates into the audit's groups of
// strata: score deciles, drop classes and closed statuses, which partition
// them, and the disagreement cells, whose members are also in a decile or a
// drop class (an overlay; AuditDraw keeps the two from drawing one key twice).
// Deciles are cut over the whole scored pool.
func auditBuckets(cands []AuditCandidate, labeled map[posting.Key]Entry) (deciles, classes, shut, disagree map[string][]posting.Key) {
	var scored, dropped, closed []AuditCandidate
	seen := map[posting.Key]bool{}
	for _, c := range cands {
		if c.Key == "" || seen[c.Key] {
			continue
		}
		seen[c.Key] = true
		switch {
		case c.Closed:
			closed = append(closed, c)
		case c.Dropped:
			dropped = append(dropped, c)
		default:
			scored = append(scored, c)
		}
	}

	// Deciles over the whole scored pool, ties broken by key.
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score < scored[j].Score
		}
		return scored[i].Key < scored[j].Key
	})
	deciles, disagree = map[string][]posting.Key{}, map[string][]posting.Key{}
	for i, c := range scored {
		if _, done := labeled[c.Key]; done {
			continue
		}
		src := decileSource(i, len(scored))
		deciles[src] = append(deciles[src], c.Key)
		switch d := i*10/len(scored) + 1; {
		case c.Teacher == TeacherWants && d <= lowDeciles:
			disagree[CellTeacherWants] = append(disagree[CellTeacherWants], c.Key)
		case c.Teacher == TeacherRejects && d == 10:
			disagree[CellTeacherRejects] = append(disagree[CellTeacherRejects], c.Key)
		}
	}
	classes = map[string][]posting.Key{}
	for _, c := range dropped {
		if _, done := labeled[c.Key]; done {
			continue
		}
		if c.Teacher == TeacherWants {
			disagree[CellTeacherWants] = append(disagree[CellTeacherWants], c.Key)
		}
		src := droppedSource(c.Class)
		classes[src] = append(classes[src], c.Key)
	}
	shut = map[string][]posting.Key{}
	for _, c := range closed {
		if _, done := labeled[c.Key]; done {
			continue
		}
		src := closedSource(c.Class)
		shut[src] = append(shut[src], c.Key)
	}
	return deciles, classes, shut, disagree
}

// decileSource is the stratum of the i'th (from 0, in score order) of n scored
// postings: "decile:1" is the lowest tenth, "decile:10" the highest.
func decileSource(i, n int) string { return fmt.Sprintf("decile:%d", i*10/n+1) }

// WeekSeed is the ISO week of t ("2026-W43"): a draw stays the same all week.
func WeekSeed(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", y, w)
}

// weekIndex is the number of the ISO week a WeekSeed names, counted in whole
// weeks (consecutive weeks differ by one, across year ends, 52- and 53-week
// years alike); ok is false for any other seed.
func weekIndex(seed string) (n int64, ok bool) {
	monday, ok := WeekStart(seed)
	if !ok {
		return 0, false
	}
	return monday.Unix() / (7 * 24 * 3600), true
}

// WeekStart is the Monday 00:00 UTC that opens the ISO week a WeekSeed names;
// ok is false for any other seed (a free-form --seed, a week 53 the year does
// not have).
func WeekStart(seed string) (monday time.Time, ok bool) {
	var y, w int
	if c, err := fmt.Sscanf(seed, "%d-W%d", &y, &w); err != nil || c != 2 || w < 1 || w > 53 || y < 1970 || y > 9999 {
		return time.Time{}, false
	}
	if seed != fmt.Sprintf("%d-W%02d", y, w) {
		return time.Time{}, false
	}
	jan4 := time.Date(y, 1, 4, 0, 0, 0, 0, time.UTC)
	monday = jan4.AddDate(0, 0, -int((jan4.Weekday()+6)%7)+7*(w-1))
	if gy, gw := monday.ISOWeek(); gy != y || gw != w {
		return time.Time{}, false
	}
	return monday, true
}

// Interleaved is the picks in a hash order of seed and key, so the owner does
// not meet every scored posting before every dropped one and cannot tell the
// stratum from the position.
func (p AuditPlan) Interleaved(seed string) []AuditPick {
	out := append([]AuditPick(nil), p.Picks...)
	sort.Slice(out, func(i, j int) bool {
		return lessHash(hashOf(seed, "order", string(out[i].Key)), hashOf(seed, "order", string(out[j].Key)))
	})
	return out
}

func hashOf(seed, purpose, name string) [sha256.Size]byte {
	return sha256.Sum256([]byte(seed + "\x00" + purpose + "\x00" + name))
}

func lessHash(a, b [sha256.Size]byte) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// drawGroup spends total places over the non-empty strata of one group and
// appends the picks and strata to plan, in stratum-name order.
func drawGroup(plan *AuditPlan, strata map[string][]posting.Key, total int, seed string, rotate bool) {
	names := make([]string, 0, len(strata))
	for n := range strata {
		names = append(names, n)
	}
	sort.Strings(names)
	// Stratum order for partial rounds, and member order within each stratum.
	order := append([]string(nil), names...)
	sort.Slice(order, func(i, j int) bool {
		return lessHash(hashOf(seed, "stratum", order[i]), hashOf(seed, "stratum", order[j]))
	})
	for _, n := range names {
		OrderMembers(seed, strata[n])
	}

	taken := map[string]int{}
	expected := map[string]float64{} // expected places over the seed
	week, byWeek := weekIndex(seed)
	byWeek = byWeek && rotate
	for left := total; left > 0; {
		var active []string
		for _, n := range order {
			if taken[n] < len(strata[n]) {
				active = append(active, n)
			}
		}
		if len(active) == 0 {
			break
		}
		if len(active) <= left {
			for _, n := range active {
				taken[n]++
				expected[n]++
			}
			left -= len(active)
			continue
		}
		// A partial round: `left` of the active strata are served, so some go
		// unserved this draw. Over the seed each is served left/len of the time.
		// For a week seed the served window walks round the strata in name
		// order, `left` further each week, so every non-empty stratum is
		// served within ceil(len/left) successive weeks; for any other seed it
		// is the first `left` in hash order.
		if byWeek {
			active = append([]string(nil), names...)
			active = slices.DeleteFunc(active, func(n string) bool { return taken[n] >= len(strata[n]) })
		}
		start := 0
		if n := int64(len(active)); byWeek {
			start = int(week % n * int64(left) % n)
		}
		for i, n := range active {
			expected[n] += float64(left) / float64(len(active))
			if byWeek && (i-start+len(active))%len(active) < left || !byWeek && i < left {
				taken[n]++
			}
		}
		left = 0
	}

	for _, n := range names {
		ks := strata[n]
		plan.Strata = append(plan.Strata, AuditStratum{Source: n, Pool: len(ks), Taken: taken[n]})
		p := min(expected[n]/float64(len(ks)), 1)
		for _, k := range ks[:taken[n]] {
			plan.Picks = append(plan.Picks, AuditPick{Key: k, Source: n, PSelect: p})
		}
	}
}
