package main

import (
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bharathvbcr/Vani/internal/jobview"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/pipeline"
	"github.com/bharathvbcr/Vani/internal/posting"
)

// prefilterReport is what the prefilter (exclusions and title readings) would
// do to a ledger: which live postings it would drop before triage, which kept
// ones the next run's exclusion pass would drop or reopen, and how the
// decision model read the postings it would drop that a model has already
// read.
type prefilterReport struct {
	Live     int            `json:"live"`
	Dropped  int            `json:"dropped"`
	ByReason map[string]int `json:"by_reason"`
	Rows     []prefilterRow `json:"rows,omitempty"` // the live postings it would drop, in ledger order
	// Kept are the triaged, scored, explained or digested postings the
	// exclusions would drop at the next run (marked ones never are); Reopen
	// are the excluded postings they no longer match, which come back to live:
	// at the next run for a posting with no full listing to wait for, and for
	// the rest when their board next lists them (a pending reopen).
	Kept   []prefilterRow `json:"kept,omitempty"`
	Reopen []prefilterRow `json:"reopen,omitempty"`
	// Read counts the postings it would drop whose role_fit a decision
	// backend (not the title) answered; RoleFit is how, and Fits lists the
	// ones read strong_fit or partial_fit: the prefilter's false drops.
	Read    int            `json:"read"`
	RoleFit map[string]int `json:"role_fit"`
	Fits    []prefilterRow `json:"fits"`
	// Unrecognised counts the postings only_locations drops (or already
	// dropped) because a place of their location is one the location tables do
	// not know: they read as outside the list, but may be US towns the tables
	// lack. UnrecognisedPlaces are the most common of those locations, as
	// listed; UnrecognisedRows (with --list) are the postings. A US town is
	// added to usCityNames in internal/location/us.go.
	Unrecognised       int               `json:"unrecognised_location"`
	UnrecognisedPlaces []unrecognisedLoc `json:"unrecognised_places,omitempty"`
	UnrecognisedRows   []prefilterRow    `json:"unrecognised,omitempty"`
}

type prefilterRow struct {
	Key      posting.Key `json:"key"`
	Title    string      `json:"title"`
	Reason   string      `json:"reason"`
	RoleFit  string      `json:"role_fit,omitempty"`
	Stage    string      `json:"stage"`
	Location string      `json:"location,omitempty"` // an unrecognised-location row's place, as the board wrote it
}

// unrecognisedLoc is a location only_locations dropped on without recognising
// it, and how many postings carry it.
type unrecognisedLoc struct {
	Location string `json:"location"`
	Postings int    `json:"postings"`
}

// maxUnrecognisedPlaces bounds the locations a prefilter report names.
const maxUnrecognisedPlaces = 20

// pointer to where the place list lives, for a US town only_locations dropped.
const usTownList = "internal/location/us.go (usCityNames)"

// prefilterOf applies p's exclusions and title reading to every record;
// protect says which postings are marked. It reads only.
func prefilterOf(recs []ledger.Record, p pipeline.DropPolicy, protect func(posting.Key) bool) prefilterReport {
	rep := prefilterReport{ByReason: map[string]int{}, RoleFit: map[string]int{}}
	ch := pipeline.ExclusionChanges(recs, p, protect)
	places := map[string]int{}
	unrecognised := func(r ledger.Record, reason string) {
		if !pipeline.UnrecognisedLocationReason(reason) {
			return
		}
		loc := jobview.CleanText(r.Posting.Location, 120, false)
		rep.Unrecognised++
		places[loc]++
		rep.UnrecognisedRows = append(rep.UnrecognisedRows, prefilterRow{Key: r.Key, Title: r.Posting.Title, Reason: reason, Stage: string(r.Status), Location: loc})
	}
	for i, r := range ch.Drop {
		if r.Status != posting.Live { // a live one is counted below, with the title drops
			rep.Kept = append(rep.Kept, prefilterRow{Key: r.Key, Title: r.Posting.Title, Reason: ch.Reasons[i], Stage: string(r.Status)})
			unrecognised(r, ch.Reasons[i])
		}
	}
	for _, r := range ch.Reopen {
		rep.Reopen = append(rep.Reopen, prefilterRow{Key: r.Key, Title: r.Posting.Title, Reason: r.Annotations[pipeline.AnnExcluded], Stage: string(r.Status)})
	}
	const rf = ledger.AnnTriagePrefix + "role_fit"
	for _, r := range recs {
		if r.Status == posting.Live {
			rep.Live++
		}
		_, reason, drop := p.EvaluatePosting(r.Posting)
		if !drop {
			continue
		}
		row := prefilterRow{Key: r.Key, Title: r.Posting.Title, Reason: reason, Stage: string(r.Status)}
		if r.Status == posting.Live || pipeline.Excluded(r) {
			// An excluded record carries the reason it was dropped under, which
			// may predate the cause: the policy's reason now is what names it.
			unrecognised(r, reason)
		}
		if r.Status == posting.Live {
			rep.Dropped++
			rep.ByReason[reason]++
			rep.Rows = append(rep.Rows, row)
		}
		a := r.Annotations
		if a[rf+ledger.AnnTriageSuffixReading] != "answered" || a[rf] == "" || a[rf+ledger.AnnTriageSuffixBackend] == pipeline.TitleBackend {
			continue
		}
		rep.Read++
		rep.RoleFit[a[rf]]++
		if v := a[rf]; v == "strong_fit" || v == "partial_fit" {
			row.RoleFit = v
			rep.Fits = append(rep.Fits, row)
		}
	}
	for loc, n := range places {
		rep.UnrecognisedPlaces = append(rep.UnrecognisedPlaces, unrecognisedLoc{Location: loc, Postings: n})
	}
	sort.Slice(rep.UnrecognisedPlaces, func(i, j int) bool {
		a, b := rep.UnrecognisedPlaces[i], rep.UnrecognisedPlaces[j]
		if a.Postings != b.Postings {
			return a.Postings > b.Postings
		}
		return a.Location < b.Location
	})
	if len(rep.UnrecognisedPlaces) > maxUnrecognisedPlaces {
		rep.UnrecognisedPlaces = rep.UnrecognisedPlaces[:maxUnrecognisedPlaces]
	}
	return rep
}

// cmdPrefilter reports what the prefilter (exclude, and title_signals.read
// with the drop rules) would drop. It never writes the ledger and calls no
// backend.
func (e *env) cmdPrefilter(args []string) error {
	fs := e.newFlags("prefilter")
	list := fs.Bool("list", false, "also list each posting it would drop or return to live")
	asJSON := e.jsonFlag(fs, "one JSON object on stdout (the lists with --list)")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := e.loadConfig()
	if err != nil {
		return err
	}
	p := dropPolicy(cfg, cfg.Decide.OpenRouterName())
	if !p.Prefilters() {
		return e.reply(*asJSON, prefilterReply{Off: true}, nil, func() {
			fmt.Fprintln(e.stdout, "prefilter is off: the config has no exclude entries and no title_signals.read entries; nothing was evaluated")
		})
	}
	snap, err := ledger.ReadSnapshot(filepath.Join(e.stateDir, "ledger"))
	if err != nil {
		return fmt.Errorf("prefilter: read the ledger: %w", err)
	}
	if snap.Torn {
		fmt.Fprintln(e.stderr, "vani: prefilter: the ledger ends in a torn line; it was left out")
	}
	protect := protectMarked(e.stateDir, func(f string, a ...any) { fmt.Fprintf(e.stderr, "vani: prefilter: "+f+"\n", a...) })
	rep := prefilterOf(snap.Records, p, protect)
	jr := rep
	jr.Fits = nonNil(jr.Fits)
	if !*list {
		jr.Rows, jr.Kept, jr.Reopen, jr.UnrecognisedRows = nil, nil, nil, nil
	}
	out := prefilterReply{Total: len(snap.Records), KeptCount: len(rep.Kept), ReopenCount: len(rep.Reopen), Report: &jr}
	return e.reply(*asJSON, out, nil, func() { writePrefilter(e.stdout, rep, len(snap.Records), *list) })
}

// prefilterReply is `vani prefilter --json`. Off: no exclusion or title
// reading is configured, and nothing was evaluated. The lists of postings are
// in the report only with --list; the counts always are.
type prefilterReply struct {
	Off         bool             `json:"off"`
	Total       int              `json:"total,omitempty"`
	KeptCount   int              `json:"kept_count"`
	ReopenCount int              `json:"reopen_count"`
	Report      *prefilterReport `json:"report,omitempty"`
}

func writePrefilter(w io.Writer, rep prefilterReport, total int, list bool) {
	fmt.Fprintf(w, "prefilter (dry run, nothing written): %d of %d live posting(s) would be dropped before triage (%d in the ledger)\n",
		rep.Dropped, rep.Live, total)
	fmt.Fprintf(w, "exclusions at the next run: %d kept posting(s) would be dropped (marked ones are kept), %d excluded posting(s) would be let back (each waits for its board to list it again; inbox and page postings come back at once)\n",
		len(rep.Kept), len(rep.Reopen))
	reasons := make([]string, 0, len(rep.ByReason))
	for r := range rep.ByReason {
		reasons = append(reasons, r)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if rep.ByReason[reasons[i]] != rep.ByReason[reasons[j]] {
			return rep.ByReason[reasons[i]] > rep.ByReason[reasons[j]]
		}
		return reasons[i] < reasons[j]
	})
	for _, r := range reasons {
		fmt.Fprintf(w, "  %5d  %s\n", rep.ByReason[r], r)
	}
	vals := make([]string, 0, len(rep.RoleFit))
	for v := range rep.RoleFit {
		vals = append(vals, fmt.Sprintf("%s %d", v, rep.RoleFit[v]))
	}
	slices.Sort(vals)
	fmt.Fprintf(w, "precision: a decision model read role_fit on %d posting(s) it would drop (any status): %s\n", rep.Read, strings.Join(vals, ", "))
	fmt.Fprintf(w, "  read strong_fit or partial_fit (false drops): %d\n", len(rep.Fits))
	for _, f := range rep.Fits {
		fmt.Fprintf(w, "    %s  %s [%s, %s] %s\n", f.Key, jobview.CleanText(f.Title, 120, false), f.RoleFit, f.Stage, f.Reason)
	}
	if rep.Read == 0 {
		fmt.Fprintln(w, "  no model reading to compare against: precision is unmeasured, not perfect")
	}
	if rep.Unrecognised > 0 {
		fmt.Fprintf(w, "unrecognised locations: %d posting(s) only_locations drops (or dropped) because a place of their location is not one Vani knows; some may be US towns.\n", rep.Unrecognised)
		for _, l := range rep.UnrecognisedPlaces {
			fmt.Fprintf(w, "  %5d  %s\n", l.Postings, l.Location)
		}
		if len(rep.UnrecognisedPlaces) == maxUnrecognisedPlaces {
			fmt.Fprintf(w, "  (the %d most common places; --list names every posting)\n", maxUnrecognisedPlaces)
		}
		fmt.Fprintf(w, "  a US town in this list is added to the place list in %s; the audit draws these drops as the class exclude:location_unrecognised\n", usTownList)
	}
	if list {
		for _, r := range rep.UnrecognisedRows {
			fmt.Fprintf(w, "%s\t%s\tunrecognised location %q (%s)\n", r.Key, jobview.CleanText(r.Title, 120, false), r.Location, r.Stage)
		}
		for _, r := range rep.Rows {
			fmt.Fprintf(w, "%s\t%s\t%s\n", r.Key, jobview.CleanText(r.Title, 120, false), r.Reason)
		}
		for _, r := range rep.Kept {
			fmt.Fprintf(w, "%s\t%s\t%s (now %s)\n", r.Key, jobview.CleanText(r.Title, 120, false), r.Reason, r.Stage)
		}
		for _, r := range rep.Reopen {
			fmt.Fprintf(w, "%s\t%s\tlet back when its board lists it (was %s)\n", r.Key, jobview.CleanText(r.Title, 120, false), r.Reason)
		}
	}
}
