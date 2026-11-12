package freehire

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func titled(source, id, rawURL, company, title string) Job {
	j := job(source, id, rawURL, company)
	j.Title = title
	return j
}

// merge is Merge with no evidence function: every board's Evidence is zero.
func merge(hits []Hit) Merged { return Merge(hits, nil) }

// Within one search count, a board with more evidence outranks a board with
// more postings; the evidence function sees each result under the search that
// found it, and a board keeps its best.
func TestMergeRanksEvidenceBeforePostings(t *testing.T) {
	big := []Job{
		titled("greenhouse", "big:1", "https://boards.greenhouse.io/big/jobs/1", "Big", "Agent d'accueil"),
		titled("greenhouse", "big:2", "https://boards.greenhouse.io/big/jobs/2", "Big", "Agent de sécurité"),
		titled("greenhouse", "big:3", "https://boards.greenhouse.io/big/jobs/3", "Big", "Agent polyvalent"),
	}
	small := titled("greenhouse", "small:1", "https://boards.greenhouse.io/small/jobs/1", "Small", "Agent Inference Engineer")
	var calls []string
	ev := func(search string, j Job) int {
		calls = append(calls, search+"|"+j.Title)
		if strings.Contains(j.Title, "Inference") {
			return 1
		}
		return 0
	}
	m := Merge([]Hit{{Search: "Agent", Jobs: append(big, small)}}, ev)
	if len(m.Boards) != 2 || m.Boards[0].Board.Token != "small" || m.Boards[0].Evidence != 1 || m.Boards[1].Evidence != 0 {
		t.Errorf("boards %+v", m.Boards)
	}
	if !slices.Contains(calls, "Agent|Agent Inference Engineer") || len(calls) != 4 {
		t.Errorf("evidence asked %v", calls)
	}
	// Without evidence, postings decide.
	if m := merge([]Hit{{Search: "Agent", Jobs: append(big, small)}}); m.Boards[0].Board.Token != "big" {
		t.Errorf("no evidence: first %+v", m.Boards[0])
	}
}

func mergedIn(m Merged, ats posting.ATS, token string) (MergedBoard, bool) {
	for _, b := range m.Boards {
		if b.Board.ATS == ats && strings.EqualFold(b.Board.Token, token) {
			return b, true
		}
	}
	return MergedBoard{}, false
}

func TestMergeCountsAResultOnceAndCreditsEverySearch(t *testing.T) {
	acme1 := titled("greenhouse", "acme:1", "https://boards.greenhouse.io/acme/jobs/1", "Acme", "Inference Engineer")
	acme2 := titled("greenhouse", "acme:2", "https://boards.greenhouse.io/acme/jobs/2", "Acme", "LLM Engineer")
	beta := titled("lever", "b1", "https://jobs.lever.co/beta/b1", "Beta", "ML Inference Engineer")
	gamma1 := titled("greenhouse", "gamma:1", "https://boards.greenhouse.io/gamma/jobs/1", "Gamma", "Rust Engineer")
	gamma2 := titled("greenhouse", "gamma:2", "https://boards.greenhouse.io/gamma/jobs/2", "Gamma", "Rust Engineer")
	gamma3 := titled("greenhouse", "gamma:3", "https://boards.greenhouse.io/gamma/jobs/3", "Gamma", "Systems Engineer")
	wd := titled("workday", "w1", "https://r1rcm.wd1.myworkdayjobs.com/en-US/R1RCM/job/X/Inference-Engineer_R1", "R1", "Inference Engineer")
	jazz := titled("jazzhr", "j1", "https://acme.applytojob.com/apply/j1", "Jazz", "Inference Engineer")

	m := merge([]Hit{
		{Search: "Inference", Jobs: []Job{acme1, beta, wd, jazz}},
		{Search: "Rust", Jobs: []Job{acme1, gamma1, gamma2, gamma3, wd, jazz}},
		{Search: "LLM", Jobs: []Job{acme2}},
	})
	// acme1, wd and jazz were each found twice.
	if m.Repeated != 3 || m.Distinct != 8 {
		t.Errorf("distinct %d repeated %d, want 8 and 3", m.Distinct, m.Repeated)
	}
	acme, ok := mergedIn(m, posting.Greenhouse, "acme")
	if !ok || acme.Postings != 2 || !slices.Equal(acme.Searches, []string{"Inference", "Rust", "LLM"}) {
		t.Errorf("acme %+v", acme)
	}
	if !slices.Equal(acme.Titles, []string{"Inference Engineer", "LLM Engineer"}) || acme.Board.Company != "Acme" {
		t.Errorf("acme titles %v company %q", acme.Titles, acme.Board.Company)
	}
	// Most searches first: acme (3), then the Workday board (2), then gamma
	// (1 search, 3 postings) before beta (1 search, 1 posting).
	var order []string
	for _, b := range m.Boards {
		order = append(order, b.Board.Token)
	}
	if want := []string{"acme", "r1rcm/R1RCM", "gamma", "beta"}; !slices.Equal(order, want) {
		t.Errorf("order %v, want %v", order, want)
	}
	gamma, _ := mergedIn(m, posting.Greenhouse, "gamma")
	if gamma.Postings != 3 || len(gamma.Titles) != 2 {
		t.Errorf("gamma %+v (titles are distinct)", gamma)
	}
	// A Workday board carries every search that found it, so it lists them.
	w, ok := mergedIn(m, posting.Workday, "r1rcm/R1RCM")
	if !ok || w.Board.Search != "" || !slices.Equal(w.Board.Searches, []string{"Inference", "Rust"}) || w.Postings != 1 {
		t.Errorf("workday %+v", w)
	}
	// A greenhouse board takes no search.
	if acme.Board.Search != "" || acme.Board.Searches != nil {
		t.Errorf("greenhouse board carries searches: %+v", acme.Board)
	}
	// Every distinct result is on a board or in an unreadable group.
	total := 0
	for _, b := range m.Boards {
		total += b.Postings
	}
	for _, u := range m.Unreadable {
		total += u.Count
	}
	if total != m.Distinct || !slices.Contains(m.Unreadable, Unreadable{Source: "jazzhr", Reason: ReasonNoAdapter, Count: 1}) {
		t.Errorf("accounted %d of %d; unreadable %+v", total, m.Distinct, m.Unreadable)
	}
}

// One search on a Workday board is its Search; more than sources.MaxSearches
// are cut to the first ones, and the board stays valid.
func TestMergeWorkdaySearchBounds(t *testing.T) {
	wd := titled("workday", "w1", "https://r1rcm.wd1.myworkdayjobs.com/en-US/R1RCM/job/X/Y_R1", "R1", "Inference Engineer")
	m := merge([]Hit{{Search: "Inference", Jobs: []Job{wd}}})
	if len(m.Boards) != 1 || m.Boards[0].Board.Search != "Inference" || m.Boards[0].Board.Searches != nil {
		t.Fatalf("one search: %+v", m.Boards)
	}
	var hits []Hit
	for i := range sources.MaxSearches + 4 {
		hits = append(hits, Hit{Search: "term " + string(rune('a'+i)), Jobs: []Job{wd}})
	}
	m = merge(hits)
	b := m.Boards[0]
	if len(b.Searches) != sources.MaxSearches+4 || len(b.Board.Searches) != sources.MaxSearches || b.Board.Searches[0] != "term a" {
		t.Errorf("%d found-by, %d board searches: %v", len(b.Searches), len(b.Board.Searches), b.Board.Searches)
	}
	// A search a board may not carry (over sources.MaxSearchLen) leaves the
	// board without searches rather than refused.
	m = merge([]Hit{{Search: strings.Repeat("x", sources.MaxSearchLen+1), Jobs: []Job{wd}}})
	if len(m.Boards) != 1 || m.Boards[0].Board.Search != "" || len(m.Unreadable) != 0 {
		t.Errorf("long search: boards %+v unreadable %+v", m.Boards, m.Unreadable)
	}
}

// placed is a result with a title and a location.
func placed(source, id, rawURL, company, title, location string) Job {
	j := titled(source, id, rawURL, company, title)
	j.Location = location
	return j
}

// jpmcJob is one requisition as freehire lists it on one of JPMorgan's Oracle
// career sites (live, 2026-11-11: the same requisition id on jobsearch, CX and
// CX_2001, the last under another company name).
func jpmcJob(site, req, company, title, location string) Job {
	return placed("oracle", "jpmc.fa.oraclecloud.com/"+site+":"+req,
		"https://jpmc.fa.oraclecloud.com/hcmUI/CandidateExperience/en/sites/"+site+"/job/"+req+"?utm_source=freehire.me",
		company, title, location)
}

// mirrorWorld is the two live cases: three JPMorgan Oracle sites listing the
// same requisitions, and Stone's second Greenhouse board (custom domain, its
// own ids) listing the same roles. Titles differ only in case and trailing
// space between the copies.
func mirrorWorld() []Hit {
	blr, uk := "Bengaluru, Karnataka, India", "BOURNEMOUTH, DORSET, United Kingdom"
	jpmc := []Job{
		jpmcJob("jobsearch", "210773687", "JPMorganChase", "Applied AI ML Director", blr),
		jpmcJob("jobsearch", "210788790", "JPMorganChase", "Applied AI ML Senior Associate", blr),
		jpmcJob("CX", "210773687", "JPMC", "Applied AI ML Director", blr),
		jpmcJob("CX_2001", "210773687", "Nextmind Solutions Private Limited", "Applied AI ML Director", blr),
		jpmcJob("jobsearch", "210805724", "JPMorganChase", "Applied AI ML Lead", blr),
		jpmcJob("CX", "210805724", "JPMC", "Applied AI ML Lead", blr),
		jpmcJob("CX_2001", "210805724", "Nextmind Solutions Private Limited", "Applied AI ML Lead", blr),
		jpmcJob("jobsearch", "210761189", "JPMorganChase", "Applied AI ML Lead - Senior Data Scientist", uk),
		jpmcJob("CX", "210761189", "JPMC", "Applied AI ML Lead - Senior Data Scientist", uk),
		jpmcJob("CX_2001", "210761189", "Nextmind Solutions Private Limited", "Applied AI ML Lead - Senior Data Scientist", uk),
	}
	fln, flnH := "Florianopolis, Santa Catarina, Brasil", "Florianópolis, Santa Catarina, Brasil - Híbrido"
	jornada := func(id, title, loc string) Job {
		return placed("greenhouse", "jornadatecnologiaengenharia:"+id,
			"https://jornada.stone.com.br/times/tracking-link/oportunidades/"+id+"?gh_jid="+id,
			"Jornada Stone - Tecnologia - Engenharia", title, loc)
	}
	stone := func(id, title, loc string) Job {
		return placed("greenhouse", "stone:"+id, "https://job-boards.greenhouse.io/stone/jobs/"+id, "Stone", title, loc)
	}
	genai := []Job{
		jornada("8200580003", "[Genai] Sênior AI Engineer - Híbrido Florianópolis", fln),
		stone("8040257003", "[Genai] Sênior AI Engineer - Híbrido Florianópolis", fln),
		jornada("7999588003", "[GenAI] Sênior Software Engineer - híbrido Florianópolis ", fln),
		jornada("7998364003", "[GenAI] Sênior AI Engineer - Híbrido Florianópolis", flnH),
		stone("7984857003", "[GenAI] Sênior Software Engineer - híbrido Florianópolis", fln),
		stone("8017007003", "[GENAI] sênior AI engineer -  Híbrido Florianópolis", flnH),
		stone("8041727003", "[GenAI] Sênior Analytics Engineer - híbrido Florianópolis", fln),
	}
	return []Hit{{Search: "Applied AI", Jobs: jpmc}, {Search: "GenAI", Jobs: genai}}
}

func boardKeys(bs []MergedBoard) []string {
	var out []string
	for _, b := range bs {
		out = append(out, string(b.Board.ATS)+":"+b.Board.Token)
	}
	return out
}

func mirrorKeys(mb MergedBoard) []string {
	var out []string
	for _, x := range mb.Mirrors {
		out = append(out, string(x.Board.ATS)+":"+x.Board.Token)
	}
	return out
}

// A board whose every posting (title and location) another board also lists
// is one employer's second site: it is folded into the board that lists the
// most, which keeps its searches and lists it as a mirror.
func TestMergeFoldsBoardsListingTheSamePostings(t *testing.T) {
	m := merge(mirrorWorld())
	if got := boardKeys(m.Boards); !slices.Equal(got, []string{"gh:stone", "oc:jpmc/jobsearch"}) {
		t.Fatalf("boards %v, want the two keepers only", got)
	}
	jp, _ := mergedIn(m, posting.OracleHCM, "jpmc/jobsearch")
	if jp.Postings != 4 || jp.Board.Company != "JPMorganChase" || jp.Board.Search != "Applied AI" {
		t.Errorf("jobsearch %+v", jp)
	}
	if got := mirrorKeys(jp); !slices.Equal(got, []string{"oc:jpmc/CX", "oc:jpmc/CX_2001"}) {
		t.Errorf("jpmc mirrors %v", got)
	}
	for _, x := range jp.Mirrors {
		if x.Postings != 3 || x.Shared != 3 || x.Board.Host != "jpmc.fa.oraclecloud.com" {
			t.Errorf("mirror %+v", x)
		}
	}
	if x := jp.Mirrors[1]; x.Board.Company != "Nextmind Solutions Private Limited" {
		t.Errorf("a mirror keeps its own company: %+v", x.Board)
	}
	st, _ := mergedIn(m, posting.Greenhouse, "stone")
	if got := mirrorKeys(st); st.Postings != 4 || !slices.Equal(got, []string{"gh:jornadatecnologiaengenharia"}) ||
		st.Mirrors[0].Postings != 3 || st.Mirrors[0].Shared != 3 {
		t.Errorf("stone %+v mirrors %+v", st, st.Mirrors)
	}
	// Every distinct result is still accounted for: on a board, on a mirror,
	// or in an unreadable group.
	total := 0
	for _, b := range m.Boards {
		total += b.Postings
		for _, x := range b.Mirrors {
			total += x.Postings
		}
	}
	if total != m.Distinct || m.Distinct != 17 || m.Mirrored != 3 {
		t.Errorf("accounted %d of %d, mirrored %d", total, m.Distinct, m.Mirrored)
	}
}

// A mirror's searches and evidence pass to the board it is folded into, so
// the group ranks and corroborates as one employer.
func TestMergeFoldedSearchesAndEvidenceJoinTheKeeper(t *testing.T) {
	a1 := placed("greenhouse", "a:1", "https://boards.greenhouse.io/a/jobs/1", "A", "Inference Engineer", "Remote")
	a2 := placed("greenhouse", "a:2", "https://boards.greenhouse.io/a/jobs/2", "A", "LLM Engineer", "Remote")
	a3 := placed("greenhouse", "a:3", "https://boards.greenhouse.io/a/jobs/3", "A", "GPU Engineer", "Remote")
	b1 := placed("greenhouse", "b:1", "https://boards.greenhouse.io/b/jobs/1", "A Labs", "Inference Engineer", "Remote")
	b2 := placed("greenhouse", "b:2", "https://boards.greenhouse.io/b/jobs/2", "A Labs", "LLM Engineer", "Remote")
	ev := func(search string, j Job) int {
		if search == "LLM" && j.Company == "A Labs" {
			return 2
		}
		return 0
	}
	m := Merge([]Hit{{Search: "Inference", Jobs: []Job{a1, a2, a3}}, {Search: "LLM", Jobs: []Job{b2, b1}}}, ev)
	if len(m.Boards) != 1 {
		t.Fatalf("boards %v", boardKeys(m.Boards))
	}
	k := m.Boards[0]
	if k.Board.Token != "a" || !slices.Equal(k.Searches, []string{"Inference", "LLM"}) || k.Evidence != 2 || k.Postings != 3 {
		t.Errorf("keeper %+v", k)
	}
	if len(k.Mirrors) != 1 || k.Mirrors[0].Board.Token != "b" || !slices.Equal(k.Mirrors[0].Searches, []string{"LLM"}) {
		t.Errorf("mirrors %+v", k.Mirrors)
	}
}

// What is not folded: a board with one posting (one shared title is too
// little to call two boards one employer), boards that only overlap, the same
// title in another place, and untitled results.
func TestMergeDoesNotFoldOnTooLittle(t *testing.T) {
	gh := func(token, id, title, loc string) Job {
		return placed("greenhouse", token+":"+id, "https://boards.greenhouse.io/"+token+"/jobs/"+id, token, title, loc)
	}
	cases := map[string][]Job{
		"one shared posting": {
			gh("big", "1", "Applied AI Engineer", "Remote"), gh("big", "2", "LLM Engineer", "Remote"),
			gh("solo", "1", "Applied AI Engineer", "Remote"),
		},
		"overlap, not contained": {
			gh("x", "1", "A", "Remote"), gh("x", "2", "B", "Remote"), gh("x", "3", "C", "Remote"),
			gh("y", "1", "A", "Remote"), gh("y", "2", "B", "Remote"), gh("y", "3", "D", "Remote"),
		},
		"same titles, other place": {
			gh("x", "1", "A", "Remote"), gh("x", "2", "B", "Remote"), gh("x", "3", "C", "Remote"),
			gh("y", "1", "A", "Berlin"), gh("y", "2", "B", "Berlin"),
		},
		"untitled": {
			gh("x", "1", "", "Remote"), gh("x", "2", "", "Remote"), gh("x", "3", "A", "Remote"),
			gh("y", "1", "", "Remote"), gh("y", "2", "", "Remote"),
		},
	}
	for name, jobs := range cases {
		m := merge([]Hit{{Search: "s", Jobs: jobs}})
		for _, b := range m.Boards {
			if len(b.Mirrors) != 0 {
				t.Errorf("%s: %s folded %v", name, b.Board.Token, mirrorKeys(b))
			}
		}
		if m.Mirrored != 0 || len(m.Boards) < 2 {
			t.Errorf("%s: boards %v mirrored %d", name, boardKeys(m.Boards), m.Mirrored)
		}
	}
}

// Two boards with the same postings fold the same way whichever was read
// first: the keeper is the one that ranks first (more searches, then
// evidence, postings, ATS and token).
func TestMergeFoldIsOrderIndependent(t *testing.T) {
	gh := func(token, id, title string) Job {
		return placed("greenhouse", token+":"+id, "https://boards.greenhouse.io/"+token+"/jobs/"+id, token, title, "Remote")
	}
	p := []Job{gh("pear", "1", "A"), gh("pear", "2", "B")}
	q := []Job{gh("apple", "1", "A"), gh("apple", "2", "B")}
	for _, jobs := range [][]Job{append(slices.Clone(p), q...), append(slices.Clone(q), p...)} {
		m := merge([]Hit{{Search: "s", Jobs: jobs}})
		if got := boardKeys(m.Boards); !slices.Equal(got, []string{"gh:apple"}) || !slices.Equal(mirrorKeys(m.Boards[0]), []string{"gh:pear"}) {
			t.Errorf("boards %v mirrors %v", got, mirrorKeys(m.Boards[0]))
		}
	}
}

func TestMergeOfNothing(t *testing.T) {
	for _, hits := range [][]Hit{nil, {}, {{Search: "x"}}, {{Search: "x", Jobs: []Job{}}}} {
		if m := merge(hits); len(m.Boards) != 0 || len(m.Unreadable) != 0 || m.Distinct != 0 || m.Repeated != 0 {
			t.Errorf("merge(%v) = %+v", hits, m)
		}
	}
}

// A result's title is third-party text: it is cleaned like the company name,
// so escape sequences, bidirectional overrides and newlines never reach a
// terminal.
func TestValidJobCleansTheTitle(t *testing.T) {
	raw, _ := json.Marshal(map[string]string{
		"source": "greenhouse", "external_id": "acme:1", "url": "https://boards.greenhouse.io/acme/jobs/1",
		"company": "Acme", "title": "\x1b[31mInference‮\nEngineer​ " + strings.Repeat("y", 500),
	})
	j, ok := validJob(raw)
	if !ok {
		t.Fatal("refused")
	}
	if strings.ContainsAny(j.Title, "\x1b‮\n​") || len([]rune(j.Title)) > maxName || !strings.HasPrefix(j.Title, "[31mInference Engineer") {
		t.Errorf("title %q", j.Title)
	}
	raw, _ = json.Marshal(map[string]string{"source": "greenhouse", "external_id": "acme:1", "url": "https://boards.greenhouse.io/acme/jobs/1"})
	if j, ok := validJob(raw); !ok || j.Title != "" {
		t.Errorf("no title: %+v %v", j, ok)
	}
}
