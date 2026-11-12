package rkbresume

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// Figure kinds of a bullet, matching rkb/numbers.go:245-270 and rkb/lint.go:293-313.
const (
	FigureImpact = "impact"
	FigureCount  = "count"
	FigureNone   = "none"

	minQuantShareDefault  = 0.85
	minQuantShareFourPage = 0.65
	minImpactShare        = 0.75
)

var (
	reFigFrac = regexp.MustCompile(`^\s*\d+/\d+`)
	reFigRate = regexp.MustCompile(`(?i)/s\b|per second|tok/s|tokens/s|GFLOP/s|GB/s|MFU`)
	reFigPct  = regexp.MustCompile(`%|\bpp\b|percentage points?|\bpoints?\b`)
	reFigMult = regexp.MustCompile(`×|\d\s?x\b|\bfaster\b|\bslower\b`)
	reFigTime = regexp.MustCompile(`(?i)\d\s?(ms|µs|us|ns|s|sec|seconds?|min|minutes?|h|hours?|days?|weeks?)\b`)

	squashWSRe      = regexp.MustCompile(`\s+`)
	vmVerdictLineRe = regexp.MustCompile(`^\[(PRIMARY|LEDGER|SKIP|FAIL|UNBACKED)\s*\]\s+(\S+)`)
	vmTallyLineRe   = regexp.MustCompile(`^\s*(PRIMARY|LEDGER|SKIP|FAIL|UNBACKED)(?:\s*\([^)]*\))?\s*:\s*(\d+)\s*$`)
)

// claimFigureKind classifies one claim's resolved value, matching rkb/numbers.go:256-270.
func claimFigureKind(v string) string {
	switch {
	case reFigFrac.MatchString(v):
		return "pct"
	case reFigRate.MatchString(v):
		return "rate"
	case reFigPct.MatchString(v):
		return "pct"
	case reFigMult.MatchString(v):
		return "mult"
	case reFigTime.MatchString(v):
		return "time"
	default:
		return "count"
	}
}

// bulletFigureKind classifies a bullet's raw text against resolved claim values,
// matching rkb/lint.go:293-308 byte-for-byte.
func bulletFigureKind(rawText string, claims map[string]string) string {
	locs := kbsync.TokenRe.FindAllStringSubmatchIndex(rawText, -1)
	if len(locs) == 0 {
		return FigureNone
	}
	hasImpact := false
	for _, loc := range locs {
		id := rawText[loc[2]:loc[3]]
		after := strings.TrimSpace(rawText[loc[1]:min(len(rawText), loc[1]+4)])
		if v, ok := claims[id]; ok && (claimFigureKind(v) != "count" || strings.HasPrefix(after, "×") || strings.HasPrefix(after, "%")) {
			hasImpact = true
		}
	}
	if !hasImpact {
		plain := kbsync.RKBPlain(rawText, claims)
		if strings.Contains(plain, "×") || strings.Contains(plain, "%") || strings.Contains(plain, "→") || strings.Contains(plain, "p=") || strings.Contains(plain, "p =") {
			hasImpact = true
		}
	}
	if hasImpact {
		return FigureImpact
	}
	return FigureCount
}

func (b *builder) figureKind(id string) string {
	bl, ok := b.bank.Bullets[id]
	if !ok {
		return FigureNone
	}
	return bulletFigureKind(bl.Text, b.claims.Values)
}

func (b *builder) minQuantShare() float64 {
	if b.base != nil && b.base.pages == 4 {
		return minQuantShareFourPage
	}
	return minQuantShareDefault
}

// CheckVMReport validates that a validator report is non-empty, did not crash
// or truncate mid-run, and did not omit passing PRIMARY/LEDGER verdict lines
// (which rkb's parseVM needs for Pass 1). Plain synthetic test stubs with no
// validator lines or tallies pass through unchanged. It returns "" when usable,
// or a human-readable reason why it is not.
func CheckVMReport(raw []byte) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "the validator report is empty"
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if strings.HasPrefix(text, "panic: ") || strings.Contains(text, "\npanic: ") ||
		strings.Contains(text, "fatal error: ") || strings.Contains(text, "Traceback (most recent call last):") {
		return "the validator report contains a crash or traceback"
	}

	segs := strings.Split(text, "\n# --- merged partial run (")
	type segStats struct {
		isValidator   bool
		hasResult     bool
		hasTally      bool
		passVerdicts  int
		totalVerdicts int
		talliedPass   int
		talliedTotal  int
	}
	stats := make([]segStats, len(segs))
	for i, seg := range segs {
		var st segStats
		for _, line := range strings.Split(seg, "\n") {
			if m := vmVerdictLineRe.FindStringSubmatch(line); m != nil {
				st.isValidator = true
				st.totalVerdicts++
				if m[1] == "PRIMARY" || m[1] == "LEDGER" {
					st.passVerdicts++
				}
				continue
			}
			if m := vmTallyLineRe.FindStringSubmatch(line); m != nil {
				n, err := strconv.Atoi(m[2])
				if err == nil {
					st.isValidator = true
					st.hasTally = true
					st.talliedTotal += n
					if m[1] == "PRIMARY" || m[1] == "LEDGER" {
						st.talliedPass += n
					}
				}
				continue
			}
			if strings.HasPrefix(strings.TrimSpace(line), "RESULT:") {
				st.hasResult = true
			}
		}
		if st.isValidator && !st.hasResult {
			return "the validator report is truncated (missing RESULT line)"
		}
		stats[i] = st
	}

	verdictsAfter := 0
	for i := len(stats) - 1; i >= 0; i-- {
		st := stats[i]
		if st.talliedPass > 0 && st.passVerdicts+verdictsAfter < st.talliedPass {
			return fmt.Sprintf("the validator report tallied %d passing checks (PRIMARY/LEDGER) but holds only %d passing verdict lines (regenerate it with `rkb claims vm`, not quiet/problems-only output)", st.talliedPass, st.passVerdicts)
		}
		verdictsAfter += st.totalVerdicts
	}
	if last := stats[len(stats)-1]; last.hasTally && last.totalVerdicts < last.talliedTotal {
		return fmt.Sprintf("the validator report tallied %d checks in its latest run, but holds only %d verdict lines", last.talliedTotal, last.totalVerdicts)
	}
	return ""
}

// sentenceHash computes rkb's 12-hex sentence hash for Pass 2 blind verification
// (rkb/claims.go:306-312, rkb/util.go:215-218).
func sentenceHash(rawText, claimID string) string {
	s := strings.ReplaceAll(rawText, "{{"+claimID+"}}", "[MASKED]")
	s = kbsync.TokenRe.ReplaceAllString(s, "<other figure>")
	s = kbsync.RKBPlain(s, nil)
	s = strings.TrimSpace(squashWSRe.ReplaceAllString(s, " "))
	return kbsync.WordingHash(s)
}

// hasPass1Problem reports whether rkb's claim problems include a Pass 1 or
// validator failure (rkb/claims.go:371-381). Per-sentence Pass 2 problems on
// sibling sentences and old voided-pass notices leave Pass 1 valid.
func hasPass1Problem(problems []string) bool {
	for _, p := range problems {
		if strings.HasPrefix(p, "pass 1 ") || strings.HasPrefix(p, "validator ") {
			return true
		}
	}
	return false
}

// sentenceVerified reports whether rawText's use of claimID is double-verified:
// either the claim is double_verified across the whole selection, or Pass 1
// passed and this exact sentence's hash is in ConfirmedSentences.
func sentenceVerified(rawText, claimID string, c exportedClaim) bool {
	if c.DoubleVerified {
		return true
	}
	if len(c.ConfirmedSentences) == 0 || hasPass1Problem(c.Problems) {
		return false
	}
	return slices.Contains(c.ConfirmedSentences, sentenceHash(rawText, claimID))
}

func (b *builder) choiceShares(ch choice) (total, quant, impact int) {
	for _, id := range ch.bulletIDs() {
		total++
		switch b.figureKind(id) {
		case FigureImpact:
			quant++
			impact++
		case FigureCount:
			quant++
		}
	}
	return total, quant, impact
}

func (b *builder) placeProtected(secIdx int, entry string) bool {
	if b.rd.sectionKept(secIdx) {
		return true
	}
	if b.rd.entryScoped() && (secIdx != b.rd.scopeSec || entry != b.rd.scopeEnt) {
		return true
	}
	return false
}

// baseChoice filters the base selection to eligible bullets and rebalances any
// excess count-only or unquantified_ok bullets left behind by ineligible drops.
func (b *builder) baseChoice() choice {
	ch := b.base.baseChoice(b.el.Allowed, b.isCont)
	rebalanced, _ := b.rebalanceShares(ch)
	return rebalanced
}

// rebalanceShares trims excess count-only bullets and unquantified_ok bullets
// when filtering or unverified-figure dropping pushes a choice below rkb lint's
// >=75% impact share or >=85% (65% on 4-page) quantified share. It scans from
// the bottom of the page upward, preferring entries with >=2 bullets so entries
// and sections stay populated, never touches redraft-locked sections/entries,
// and never drops a lead whose continuation would worsen the target share.
func (b *builder) rebalanceShares(ch choice) (choice, []string) {
	origOrder := ch.bulletIDs()
	total, quant, impact := b.choiceShares(ch)
	if total == 0 || quant == 0 {
		return ch, nil
	}
	droppedSet := map[string]bool{}

	for quant > 0 && impact > 0 && float64(impact)/float64(quant) < minImpactShare {
		cand := b.pickTrimCandidate(ch,
			func(id string) bool { return b.figureKind(id) == FigureCount },
			func(next choice) bool {
				nt, nq, ni := b.choiceShares(next)
				return nt > 0 && ni == impact && nq < quant
			},
		)
		if cand == "" {
			break
		}
		next, cas := ch.without(map[string]bool{cand: true}, b.isCont)
		droppedSet[cand] = true
		for _, id := range cas {
			droppedSet[id] = true
		}
		ch = next
		total, quant, impact = b.choiceShares(ch)
	}

	minQ := b.minQuantShare()
	for total > 0 && quant > 0 && float64(quant)/float64(total) < minQ {
		cand := b.pickTrimCandidate(ch,
			func(id string) bool {
				return b.figureKind(id) == FigureNone && b.bank.Bullets[id].UnquantifiedOK != ""
			},
			func(next choice) bool {
				nt, nq, _ := b.choiceShares(next)
				return nt > 0 && nq == quant && nt < total
			},
		)
		if cand == "" {
			break
		}
		next, cas := ch.without(map[string]bool{cand: true}, b.isCont)
		droppedSet[cand] = true
		for _, id := range cas {
			droppedSet[id] = true
		}
		ch = next
		total, quant, impact = b.choiceShares(ch)
	}

	if len(droppedSet) == 0 {
		return ch, nil
	}
	var trimmed []string
	for _, id := range origOrder {
		if droppedSet[id] {
			trimmed = append(trimmed, id)
		}
	}
	return ch, trimmed
}

func (b *builder) pickTrimCandidate(ch choice, match func(string) bool, improves func(choice) bool) string {
	seq := b.base.sequence(ch)
	tryBullet := func(id string) bool {
		if !match(id) {
			return false
		}
		next, _ := ch.without(map[string]bool{id: true}, b.isCont)
		return improves(next)
	}

	// Tier 1: multi-bullet entries from bottom of page upward.
	for si := len(seq) - 1; si >= 0; si-- {
		secIdx := seq[si]
		if !b.base.sections[secIdx].choosable || b.base.sections[secIdx].kind != "entries" {
			continue
		}
		ents := ch.secs[secIdx].entries
		for ei := len(ents) - 1; ei >= 0; ei-- {
			e := ents[ei]
			if b.placeProtected(secIdx, e.id) || len(e.bullets) < 2 {
				continue
			}
			for bi := len(e.bullets) - 1; bi >= 0; bi-- {
				if id := e.bullets[bi]; tryBullet(id) {
					return id
				}
			}
		}
	}
	// Tier 2: multi-bullet summary sections from bottom upward.
	for si := len(seq) - 1; si >= 0; si-- {
		secIdx := seq[si]
		if !b.base.sections[secIdx].choosable || b.base.sections[secIdx].kind != "bullets" || b.placeProtected(secIdx, "") {
			continue
		}
		bs := ch.secs[secIdx].bullets
		if len(bs) < 2 {
			continue
		}
		for bi := len(bs) - 1; bi >= 0; bi-- {
			if id := bs[bi]; tryBullet(id) {
				return id
			}
		}
	}
	// Tier 3: single-bullet entries from bottom of page upward.
	for si := len(seq) - 1; si >= 0; si-- {
		secIdx := seq[si]
		if !b.base.sections[secIdx].choosable || b.base.sections[secIdx].kind != "entries" {
			continue
		}
		ents := ch.secs[secIdx].entries
		for ei := len(ents) - 1; ei >= 0; ei-- {
			e := ents[ei]
			if b.placeProtected(secIdx, e.id) || len(e.bullets) != 1 {
				continue
			}
			if id := e.bullets[0]; tryBullet(id) {
				return id
			}
		}
	}
	// Tier 4: single-bullet summary sections.
	for si := len(seq) - 1; si >= 0; si-- {
		secIdx := seq[si]
		if !b.base.sections[secIdx].choosable || b.base.sections[secIdx].kind != "bullets" || b.placeProtected(secIdx, "") {
			continue
		}
		bs := ch.secs[secIdx].bullets
		if len(bs) == 1 && tryBullet(bs[0]) {
			return bs[0]
		}
	}
	return ""
}

// enforcesFigureShares reports whether pool p comes from an rkb bank that holds
// bullets to figure-share rules: at least one impact bullet is offered, and
// every unquantified bullet in p carries an explicit unquantified_ok reason.
func (b *builder) enforcesFigureShares(p pool) bool {
	impact, bareNone := 0, 0
	check := func(id string) {
		switch b.figureKind(id) {
		case FigureImpact:
			impact++
		case FigureNone:
			if b.bank.Bullets[id].UnquantifiedOK == "" {
				bareNone++
			}
		}
	}
	for _, pb := range p.summary {
		check(pb.id)
	}
	for _, pe := range p.entries {
		for _, pb := range pe.bullets {
			check(pb.id)
		}
	}
	return impact > 0 && bareNone == 0
}

// poolCanMeetShares checks that p offers enough figured and impact bullets (given
// mandatory sections/entries, per-place caps, and any kept redraft parts) that
// >= minQuantShare and >= minImpactShare are satisfiable without asking the
// model for an impossible ratio.
func (b *builder) poolCanMeetShares(p pool) (canQuant, canImpact bool) {
	checkPool := p
	if b.rd != nil && len(b.rd.full.entries) > 0 {
		checkPool = b.rd.full
	}
	if !b.enforcesFigureShares(checkPool) {
		return false, false
	}

	keptTotal, keptQuant, keptImpact := 0, 0, 0
	if b.rd.keeps() {
		for id := range b.keptBullets() {
			keptTotal++
			switch b.figureKind(id) {
			case FigureImpact:
				keptQuant++
				keptImpact++
			case FigureCount:
				keptQuant++
			}
		}
	}

	countList := func(bs []poolBullet) (imp, fig int) {
		for _, pb := range bs {
			switch b.figureKind(pb.id) {
			case FigureImpact:
				imp++
				fig++
			case FigureCount:
				fig++
			}
		}
		return imp, fig
	}

	maxOpenImpact, maxOpenQuant := 0, 0
	forcedNone, forcedCount := 0, 0

	openSumSecs := 0
	if !b.rd.entryScoped() {
		for i, s := range b.base.sections {
			if s.choosable && s.kind == "bullets" && !b.rd.sectionKept(i) && b.fillable(i, p) {
				openSumSecs++
			}
		}
	}
	if openSumSecs > 0 {
		imp, fig := countList(p.summary)
		sumCap := openSumSecs * MaxSummaryBullets
		maxOpenImpact += min(imp, sumCap)
		maxOpenQuant += min(fig, sumCap)
		if fig == 0 {
			forcedNone += openSumSecs
		} else if imp == 0 {
			forcedCount += openSumSecs
		}
	}

	byEntry := map[string]poolEntry{}
	for _, pe := range p.entries {
		byEntry[pe.id] = pe
		imp, fig := countList(pe.bullets)
		maxOpenImpact += min(imp, MaxEntryBullets)
		maxOpenQuant += min(fig, MaxEntryBullets)
	}

	if b.rd.entryScoped() {
		if pe, ok := byEntry[b.rd.scopeEnt]; ok && len(pe.bullets) > 0 {
			imp, fig := countList(pe.bullets)
			if fig == 0 {
				forcedNone++
			} else if imp == 0 {
				forcedCount++
			}
		}
	} else {
		for i, s := range b.base.sections {
			if !s.choosable || s.kind != "entries" || b.rd.sectionKept(i) {
				continue
			}
			if i == b.base.expIdx {
				for _, be := range s.entries {
					if pe, ok := byEntry[be.id]; ok && len(pe.bullets) > 0 {
						imp, fig := countList(pe.bullets)
						if fig == 0 {
							forcedNone++
						} else if imp == 0 {
							forcedCount++
						}
					}
				}
			} else if b.fillable(i, p) {
				secImp, secFig := 0, 0
				for _, pe := range p.entries {
					if b.canHold(i, pe.id) {
						imp, fig := countList(pe.bullets)
						secImp += imp
						secFig += fig
					}
				}
				if secFig == 0 {
					forcedNone++
				} else if secImp == 0 {
					forcedCount++
				}
			}
		}
	}

	room := max(0, MaxTotalBullets-keptTotal)
	minQ := b.minQuantShare()
	if room >= forcedNone {
		addQ := min(maxOpenQuant, room-forcedNone)
		tot := keptTotal + addQ + forcedNone
		q := keptQuant + addQ
		canQuant = tot > 0 && float64(q)/float64(tot) >= minQ
	}
	if room >= forcedCount {
		addI := min(maxOpenImpact, room-forcedCount)
		q := keptQuant + addI + forcedCount
		imp := keptImpact + addI
		canImpact = q > 0 && float64(imp)/float64(q) >= minImpactShare
	}
	return canQuant, canImpact
}
