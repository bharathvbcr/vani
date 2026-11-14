package pipeline

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

type parityPosting struct {
	board int
	key   posting.Key
	p     posting.Posting
}

// parityWorld is a few boards whose postings share company|title|location
// tuples across sources: ATS boards, a careers-page board, and fallback-keyed
// postings an ATS board lists, so dedupe in both directions is exercised.
func parityWorld(t *testing.T) ([]sources.Board, []parityPosting) {
	t.Helper()
	boards := []sources.Board{
		{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"},
		{ATS: posting.Page, Token: "acmepage", Company: "Acme", URL: "https://careers.acme.test/jobs"},
		{ATS: posting.Lever, Token: "beta", Company: "Beta"},
		{ATS: posting.Greenhouse, Token: "acme2", Company: "Acme"},
		// The same company on a second ATS: rule 2 of twinOf. Its links repeat
		// board 2's, a different company, so only the link ties them: rule 1.
		{ATS: posting.Lever, Token: "acmelv", Company: "Acme"},
	}
	titles := []string{"Engineer", "ML Engineer", "Data Scientist"}
	locs := []string{"Remote", "Austin"}
	var out []parityPosting
	for b, bd := range boards {
		n := 0
		for _, title := range titles {
			for _, loc := range locs {
				n++
				req := roleReq(title, loc)
				// Three digits or more, so the link names a posting. Boards 0 and 1
				// share links, as do boards 2 and 4.
				link := b
				switch b {
				case 1:
					link = 0
				case 4:
					link = 2
				}
				p := posting.Posting{Company: bd.Company, Title: title, Location: loc, URL: fmt.Sprintf("https://careers.acme.test/jobs/%d%03d?req_id=%s", link, n, req)}
				var key posting.Key
				var err error
				switch bd.ATS {
				case posting.Greenhouse:
					key, err = posting.GreenhouseKey(bd.Token, fmt.Sprint(n))
				case posting.Lever:
					key, err = posting.LeverKey(bd.Token, fmt.Sprintf("id-%d", n))
				case posting.Page:
					key, err = posting.PageKey(bd.Token, p.URL)
				}
				if err != nil {
					t.Fatal(err)
				}
				p.Key, p.ATS = key, bd.ATS
				out = append(out, parityPosting{board: b, key: key, p: p})
			}
		}
		if bd.ATS == posting.Greenhouse {
			// A fallback-keyed posting listed by an ATS board.
			fk, err := posting.FallbackKey(bd.Company, "Engineer", "Remote")
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, parityPosting{board: b, key: fk, p: posting.Posting{Key: fk, ATS: posting.Other, Company: bd.Company, Title: "Engineer", Location: "Remote", URL: "https://inbox.acme.test/j?req_id=" + roleReq("Engineer", "Remote")}})
		}
	}
	return boards, out
}

func indexEqual(t *testing.T, got, want *ledgerIndex) {
	t.Helper()
	if !reflect.DeepEqual(got.keys, want.keys) {
		t.Fatalf("index keys\ngot  %v\nwant %v", got.keys, want.keys)
	}
	if !reflect.DeepEqual(got.info, want.info) {
		t.Fatalf("index info\ngot  %v\nwant %v", got.info, want.info)
	}
	if !reflect.DeepEqual(got.ats, want.ats) || !reflect.DeepEqual(got.weak, want.weak) {
		t.Fatalf("index tuples\ngot  ats %v weak %v\nwant ats %v weak %v", got.ats, got.weak, want.ats, want.weak)
	}
	if !reflect.DeepEqual(got.byURL, want.byURL) {
		t.Fatalf("index links\ngot  %v\nwant %v", got.byURL, want.byURL)
	}
	if !slices.EqualFunc(got.events, want.events, time.Time.Equal) {
		t.Fatalf("index events\ngot  %v\nwant %v", got.events, want.events)
	}
}

// Reconciler, which reads the ledger once per run, must leave exactly the
// ledger the per-board fresh scan leaves, board after board, run after run.
func TestReconcilerMatchesAFreshScanPerBoard(t *testing.T) {
	var total Summary
	var viaURL, viaRole int // duplicates the link rule and the cross-ATS tuple rule made
	var shortReads, unreadableReads int
	mixedUnreadableDead := map[uint64]int{} // by seed%4: unreadable-cause deaths on the mixed quiet stretches
	defer func() {
		if shortReads == 0 || total.ShortDead == 0 {
			t.Errorf("the seeds did not read short listings, or none ended in a short death: %d short reads, %+v", shortReads, total)
		}
		if unreadableReads == 0 || total.UnreadableDead == 0 || mixedUnreadableDead[0] == 0 || mixedUnreadableDead[2] == 0 {
			t.Errorf("the seeds did not reach the unreadable-row cause: %d reads, %d deaths, mixed unreadable-short-clean %d, short-unreadable-clean %d",
				unreadableReads, total.UnreadableDead, mixedUnreadableDead[0], mixedUnreadableDead[2])
		}
		if viaURL == 0 || viaRole == 0 {
			t.Errorf("the seeds did not reach both identity rules: %d by link, %d by role", viaURL, viaRole)
		}
		// Every outcome reconcile can reach was reached, so parity was checked
		// on each path and not only on the easy ones.
		if total.New == 0 || total.Changed == 0 || total.Rerendered == 0 || total.Redated == 0 || total.Reappeared == 0 ||
			total.Missed == 0 || total.Dead == 0 || total.Duplicate == 0 || total.Unchanged == 0 || total.Revived == 0 ||
			total.Refreshed == 0 || total.Held == 0 || total.Deferred == 0 || total.MassMarked == 0 || total.MassDead == 0 || !total.ClockBehind ||
			total.ListedDuplicate == 0 || total.ListedDuplicate == total.Duplicate {
			t.Errorf("the seeds did not cover every outcome: %+v", total)
		}
	}()
	for seed := uint64(1); seed <= 12; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, 7))
			boards, pool := parityWorld(t)
			oracle, err := ledger.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer oracle.Close()
			subject, err := ledger.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			ctx := context.Background()
			start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
			// 24 daily runs. One board lists nothing from run 3 to run 19, a mass
			// absence held 14 days that ends in deaths; then it lists again. Some
			// runs, and some boards within a run, read under a wrong clock (both
			// ways, inside and past ClockWindow), so the freeze, the restarts and the
			// deferred deaths are compared too.
			empty := int(seed % 5)
			skews := []time.Duration{2 * time.Hour, 20 * time.Hour, 2 * 24 * time.Hour, 5 * 24 * time.Hour, 365 * 24 * time.Hour,
				-2 * time.Hour, -2 * 24 * time.Hour, -5 * 24 * time.Hour, -400 * 24 * time.Hour}
			for run := range 24 {
				runNow := start.Add(time.Duration(run) * 24 * time.Hour)
				if rng.IntN(6) == 0 {
					runNow = runNow.Add(skews[rng.IntN(len(skews))])
				}
				rc := NewReconciler(subject)
				for bi, b := range boards {
					now := runNow
					if rng.IntN(20) == 0 { // the clock jumps between two boards of one run
						now = now.Add(skews[rng.IntN(len(skews))])
					}
					var listed []posting.Posting
					quiet := bi == empty && run >= 3 && run < 20
					// On even seeds the quiet stretch is not empty but short: the
					// board lists all of its postings but its first, every run.
					shortQuiet := quiet && seed%2 == 0
					omitted := false
					for _, pp := range pool {
						if pp.board != bi {
							continue
						}
						if shortQuiet {
							if !omitted {
								omitted = true
								continue
							}
						} else if rng.IntN(4) == 0 || quiet { // a quarter missing this run
							continue
						}
						p := pp.p
						p.SeenAt = now
						// The board refiles every posting once, mid-way: a display
						// field moves under unchanged text (refreshed).
						p.Department = fmt.Sprintf("team %d", run/3)
						switch rng.IntN(4) {
						case 0:
							p.Description = "A new role description, version two."
						case 1:
							p.Description = srcutil.PayLinePrefix + " USD 100000 – 120000 per year\n\nThe role description."
						default:
							p.Description = "The role description."
						}
						// Some listings carry a posted date, some do not, so a
						// posting stored undated is re-dated on a later run.
						if rng.IntN(2) == 0 {
							p.PostedAt = start.Add(-time.Duration(rng.IntN(30)) * 24 * time.Hour)
						}
						listed = append(listed, p)
					}
					rng.Shuffle(len(listed), func(i, j int) { listed[i], listed[j] = listed[j], listed[i] })
					// A short listing (the board counted more rows than it listed):
					// on even seeds the board's quiet stretch is short, so the posting
					// it leaves out is held and dies as a short death; on odd seeds it
					// is empty, a true mass absence. Any board-run may also be short.
					//
					// Skipped unreadable rows (kind 2) are a shortfall that kills
					// nothing. Any board-run may be one, and on seeds 4k and 4k+2 the
					// quiet stretch is mixed: unreadable, then short, then a complete
					// listing (seeds 4k), or short, then unreadable, then a complete one
					// (4k+2), so the cause is compared against every listing after it.
					kind := 0
					if shortQuiet || rng.IntN(12) == 0 {
						kind = 1
					}
					if unreadDraw := rng.IntN(15) == 0; unreadDraw && !shortQuiet {
						kind = 2
					}
					if shortQuiet {
						phase := (run - 3) / 5 // runs 3-7, 8-12, 13-19
						switch {
						case seed%6 == 0: // plain short stretch: the short death stays quiet
						case seed%4 == 0:
							kind = []int{2, 1, 0}[min(phase, 2)]
						case seed%4 == 2:
							kind = []int{1, 2, 0}[min(phase, 2)]
						}
					}
					want, werr := oracleReconcile(ctx, oracle, b, listed, now, kind)
					var got Summary
					var gerr error
					switch kind {
					case 2:
						unreadableReads++
						got, gerr = rc.ReconcileUnreadable(ctx, b, listed, now)
					case 1:
						shortReads++
						got, gerr = rc.ReconcileShort(ctx, b, listed, now)
					default:
						got, gerr = rc.Reconcile(ctx, b, listed, now)
					}
					if shortQuiet && seed%6 != 0 && got.ShortDead > 0 && got.Absent > 0 && run >= 17 {
						t.Fatalf("run %d board %s: a death after unreadable rows marked the posting went quiet as a short one: %+v", run, b.Token, got)
					}
					if shortQuiet && seed%2 == 0 { // seeds 4k and 4k+2
						mixedUnreadableDead[seed%4] += got.UnreadableDead
					}
					if (werr == nil) != (gerr == nil) || got != want {
						t.Fatalf("run %d board %s: summary %+v %v, oracle %+v %v", run, b.Token, got, gerr, want, werr)
					}
					if g, w := subject.All(), oracle.All(); !reflect.DeepEqual(g, w) {
						t.Fatalf("run %d board %s: ledgers differ\ngot  %+v\nwant %+v", run, b.Token, g, w)
					}
					indexEqual(t, rc.index(), buildIndex(subject))
					total.New += got.New
					total.Changed += got.Changed
					total.Rerendered += got.Rerendered
					total.Redated += got.Redated
					total.Refreshed += got.Refreshed
					total.Reappeared += got.Reappeared
					total.Missed += got.Missed
					total.Dead += got.Dead
					total.Duplicate += got.Duplicate
					total.ListedDuplicate += got.ListedDuplicate
					total.Unchanged += got.Unchanged
					total.Revived += got.Revived
					total.Held += got.Held
					total.Deferred += got.Deferred
					total.MassMarked += got.MassMarked
					total.MassDead += got.MassDead
					total.ShortDead += got.ShortDead
					total.UnreadableDead += got.UnreadableDead
					total.ClockBehind = total.ClockBehind || got.ClockBehind
				}
			}
			for _, r := range subject.All() {
				orig, ok := subject.Get(posting.Key(r.Annotations[DuplicateOfKey]))
				if r.Status != posting.Duplicate || !ok || weakKey(r.Key) || weakKey(orig.Key) {
					continue
				}
				if slices.ContainsFunc(urlKeys(r.Posting), func(u string) bool { return slices.Contains(urlKeys(orig.Posting), u) }) {
					viaURL++
				} else {
					viaRole++
				}
			}
		})
	}
}
