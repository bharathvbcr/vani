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
				// Three digits or more, so the link names a posting. Boards 0 and 1
				// share links, as do boards 2 and 4.
				link := b
				switch b {
				case 1:
					link = 0
				case 4:
					link = 2
				}
				p := posting.Posting{Company: bd.Company, Title: title, Location: loc, URL: fmt.Sprintf("https://careers.acme.test/jobs/%d%03d", link, n)}
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
			out = append(out, parityPosting{board: b, key: fk, p: posting.Posting{Key: fk, ATS: posting.Other, Company: bd.Company, Title: "Engineer", Location: "Remote"}})
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
}

// Reconciler, which reads the ledger once per run, must leave exactly the
// ledger the per-board fresh scan leaves, board after board, run after run.
func TestReconcilerMatchesAFreshScanPerBoard(t *testing.T) {
	var total Summary
	var viaURL, viaRole int // duplicates the link rule and the cross-ATS tuple rule made
	defer func() {
		if viaURL == 0 || viaRole == 0 {
			t.Errorf("the seeds did not reach both identity rules: %d by link, %d by role", viaURL, viaRole)
		}
		// Every outcome reconcile can reach was reached, so parity was checked
		// on each path and not only on the easy ones.
		if total.New == 0 || total.Changed == 0 || total.Rerendered == 0 || total.Redated == 0 || total.Reappeared == 0 ||
			total.Missed == 0 || total.Dead == 0 || total.Duplicate == 0 || total.Unchanged == 0 || total.Revived == 0 ||
			total.Refreshed == 0 {
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
			for run := range 6 {
				now := start.Add(time.Duration(run) * 24 * time.Hour)
				rc := NewReconciler(subject)
				for bi, b := range boards {
					var listed []posting.Posting
					for _, pp := range pool {
						if pp.board != bi || rng.IntN(4) == 0 { // a quarter missing this run
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
					want, werr := oracleReconcile(ctx, oracle, b, listed, now)
					got, gerr := rc.Reconcile(ctx, b, listed, now)
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
					total.Unchanged += got.Unchanged
					total.Revived += got.Revived
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
