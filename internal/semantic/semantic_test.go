package semantic

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/kbsync"
)

// The bank is synthetic. Its org, location and date strings are unique tokens
// so a leak into an embedder input is detectable.
const synthBank = `{
  "entries": {
    "e1": {"title": "Platform Engineer", "org": "Zorblax Industries", "dates": "2019 - 2022", "location": "Quuxville", "stack": "Go, Kubernetes, Postgres; 2021",
           "bullets": ["b1", "b2"]},
    "e2": {"title": "Backend Developer", "org": "Frobnitz LLC", "dates": "1987-1991", "location": "Wibbleton", "stack": "Python, kubernetes",
           "bullets": ["b3", "b4"]}
  },
  "bullets": {
    "b1": {"text": "Operated **Kubernetes** clusters serving production traffic and cut deploy time by {{clmDeploy}}"},
    "b2": {"text": "Built Go services for internal developer tooling and CI pipelines"},
    "b3": {"text": "built go services for internal developer tooling and CI pipelines"},
    "b4": {"text": "Designed Postgres schemas and query optimizations for reporting workloads"}
  }
}`

const target = "Senior platform engineer building Kubernetes and Go infrastructure services"

var synonyms = [][]string{{"kubernetes", "k8s", "kube"}, {"golang", "go"}}

func synthProfile(t testing.TB) Profile {
	t.Helper()
	b, err := kbsync.Parse([]byte(synthBank))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ProfileFromBank(b, target)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func newMatcher(t testing.TB, e Embedder) (*Matcher, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cache")
	m, err := NewMatcher(e, synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	return m, dir
}

// spy wraps an Embedder: it counts calls, records every text and kind, and
// can rewrite or replace answers.
type spy struct {
	inner Embedder
	mu    sync.Mutex
	calls int
	maxN  int
	texts []string
	hook  func(ctx context.Context, texts []string, kind Kind, call int) ([][]float32, error, bool)
	model string
}

func (s *spy) Model() string {
	if s.model != "" {
		return s.model
	}
	return s.inner.Model()
}

func (s *spy) Embed(ctx context.Context, texts []string, kind Kind) ([][]float32, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.maxN = max(s.maxN, len(texts))
	s.texts = append(s.texts, texts...)
	hook := s.hook
	s.mu.Unlock()
	if hook != nil {
		if rows, err, handled := hook(ctx, texts, kind, call); handled {
			return rows, err
		}
	}
	return s.inner.Embed(ctx, texts, kind)
}

func (s *spy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newSpy() *spy { return &spy{inner: NewFakeEmbedder(256, synonyms)} }

func k8sDoc() Doc {
	return Doc{Key: "k8s", Title: "Senior Platform Engineer", Description: "Requirements:\n- Experience operating k8s clusters in production\n- Strong golang skills building backend services"}
}

func pastryDoc() Doc {
	return Doc{Key: "pastry", Title: "Pastry Chef", Description: "Responsibilities:\n- Bake fresh pastry and croissants every morning\n- Decorate wedding cakes for customers"}
}

func mustWarm(t *testing.T, m *Matcher, docs ...Doc) Report {
	t.Helper()
	rep, err := m.Warm(context.Background(), docs, 1000)
	if err != nil {
		t.Fatalf("Warm: %v (%+v)", err, rep)
	}
	return rep
}

func lookup(t *testing.T, m *Matcher, d Doc) Match {
	t.Helper()
	got, ok := m.Lookup(d.Key, TextHash(d.Title, d.Description))
	if !ok {
		t.Fatalf("no cached match for %s", d.Key)
	}
	return got
}

// ---- profile ----

func TestProfileFromBank(t *testing.T) {
	p := synthProfile(t)
	seen := map[string]int{}
	for _, c := range p.Chunks {
		seen[strings.ToLower(c.Text)]++
		if c.Label != "skills" && c.Label != "Platform Engineer" && c.Label != "Backend Developer" {
			t.Errorf("chunk label %q is not a role title or skills", c.Label)
		}
		if strings.Contains(c.Text, "{{") || strings.Contains(c.Text, "**") {
			t.Errorf("chunk keeps markup or a claim token: %q", c.Text)
		}
	}
	if n := seen["built go services for internal developer tooling and ci pipelines"]; n != 1 {
		t.Errorf("duplicate bullet kept %d times", n)
	}
	if n := seen["skills: go, kubernetes, postgres, python"]; n != 1 {
		t.Errorf("skills chunk missing or duplicated (kubernetes/Kubernetes dedup): %v", seen)
	}
	if got := seen["operated kubernetes clusters serving production traffic and cut deploy time by"]; got != 1 {
		t.Errorf("claim token not dropped cleanly: %v", seen)
	}
	if p.Hash() != synthProfile(t).Hash() {
		t.Error("Hash is not deterministic")
	}
}

func TestProfileEmptyAndBad(t *testing.T) {
	if _, err := ProfileFromBank(kbsync.Bank{}, target); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty bank: %v", err)
	}
	b, _ := kbsync.Parse([]byte(synthBank))
	// An empty target falls back to the decision model's default comparison,
	// so semantic matching is not silently off for a config with none.
	if p, err := ProfileFromBank(b, "  "); err != nil || p.Target != DefaultTarget {
		t.Errorf("empty target: profile target %q, err %v; want DefaultTarget", p.Target, err)
	}
	for name, raw := range map[string]string{
		"malformed":      `{"entries": `,
		"duplicate key":  `{"entries": {"a": {"title": "x"}, "a": {"title": "y"}}}`,
		"dangling":       `{"entries": {"a": {"title": "x", "bullets": ["nope"]}}}`,
		"oversized":      `{"bullets": {"b": {"text": "` + strings.Repeat("x", kbsync.MaxBankBytes) + `"}}}`,
		"not an object":  `[1,2]`,
		"null entry":     `{"entries": {"a": null}}`,
		"trailing data":  `{} {}`,
		"bad bullet id":  `{"bullets": {"has space": {"text": "x"}}}`,
		"empty id":       `{"bullets": {"": {"text": "x"}}}`,
		"too many dupes": `{"entries": {"a": {"bullets": ["b","b"]}}, "bullets": {"b": {"text": "x"}}}`,
	} {
		if _, err := kbsync.Parse([]byte(raw)); err == nil {
			t.Errorf("%s: kbsync.Parse accepted it", name)
		}
	}
	// A bank with only empty fields has nothing to embed.
	empty, err := kbsync.Parse([]byte(`{"entries": {"a": {"title": "", "bullets": []}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ProfileFromBank(empty, target); !errors.Is(err, ErrEmpty) {
		t.Errorf("hollow bank: %v", err)
	}
}

func TestProfileBounds(t *testing.T) {
	var bl, ids strings.Builder
	for i := 0; i < 2000; i++ {
		if i > 0 {
			bl.WriteString(",")
			ids.WriteString(",")
		}
		fmt.Fprintf(&bl, `"b%d": {"text": "Delivered unique project number %d with several words"}`, i, i)
		fmt.Fprintf(&ids, `"b%d"`, i)
	}
	huge := strings.Repeat("kubernetes ", 2000)
	raw := fmt.Sprintf(`{"entries": {"e": {"title": "Engineer", "bullets": [%s, "big"]}}, "bullets": {%s, "big": {"text": %q}}}`, ids.String(), bl.String(), huge)
	b, err := kbsync.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ProfileFromBank(b, target)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Chunks) > MaxProfileChunks || !p.Truncated {
		t.Errorf("%d chunks, truncated=%v", len(p.Chunks), p.Truncated)
	}
	for _, c := range p.Chunks {
		if len(c.Text) > MaxProfileChunkBytes || !utf8.ValidString(c.Text) {
			t.Fatalf("chunk of %d bytes / invalid UTF-8", len(c.Text))
		}
	}
}

func TestProfileCleansURLsAndEmail(t *testing.T) {
	got := cleanBulletText("Shipped [the tool](https://example.com/x?a,b) mail me@example.org or see https://example.com now")
	if strings.Contains(got, "example") || strings.Contains(got, "@") || got != "Shipped the tool mail or see now" {
		t.Errorf("got %q", got)
	}
}

// Phone-shaped runs and work-authorization phrases are contact data. A year
// span and an organisation named in the same bullet are not.
func TestCleanBulletTextDropsPhoneAndAuthorization(t *testing.T) {
	phone := cleanBulletText("Shipped the API at 555 123 4567 for Acme")
	for _, digit := range []string{"555", "123", "4567"} {
		if strings.Contains(phone, digit) {
			t.Errorf("phone run survived in %q", phone)
		}
	}
	if !strings.Contains(phone, "Acme") {
		t.Errorf("organisation dropped from %q", phone)
	}

	for _, keep := range []string{
		"2019-2024",
		"2019\u20132024",
		"555 --- 4567",
		"555 ---- 4567",
		"100,000",
		"3.12",
		"Go 1.26",
		"2019\u20142024",
		"saw \u2460\u2461\u2462\u2463\u2464\u2465\u2466 at Acme",
		"saw \u00b9\u00b2\u00b3\u2074\u2075\u2076\u2077 at Acme",
		"Reduced cloud sponsorship costs at Acme",
		"",
		"123456",
	} {
		if got := cleanBulletText(keep); got != keep {
			t.Errorf("cleanBulletText(%q) = %q, want it unchanged", keep, got)
		}
	}

	if got := cleanBulletText("2019 - 2024"); got != "" {
		t.Errorf("spaced year run survived in %q", got)
	}
	keptYear := cleanBulletText("At Acme from 2019-2024 shipped 555 123 4567")
	if !strings.Contains(keptYear, "Acme") || !strings.Contains(keptYear, "2019-2024") {
		t.Errorf("year span or organisation dropped from %q", keptYear)
	}
	for _, digit := range []string{"555", "123", "4567"} {
		if strings.Contains(keptYear, digit) {
			t.Errorf("phone run survived beside a year span in %q", keptYear)
		}
	}

	for _, in := range []string{
		"555---4567",
		"(555) 123-4567",
		"555.123.4567",
		"555-123-4567",
		"1899-2024",
		"2099-2100",
		"2019-2024-2025",
		"0123-2024",
		"1234567",
		"\uff15\uff15\uff15 \uff11\uff12\uff13 \uff14\uff15\uff16\uff17",
		"\u0662\u0660\u0661\u0669-\u0662\u0660\u0662\u0664",
	} {
		got := cleanBulletText(in)
		if strings.ContainsAny(got, "0123456789") || got == in {
			t.Errorf("phone-shaped run survived: in %q got %q", in, got)
		}
	}

	auth := cleanBulletText("Joined Acme and am authorized to work in the United States")
	if strings.Contains(strings.ToLower(auth), "authorized to work") || !strings.Contains(auth, "Acme") {
		t.Errorf("authorization phrase or organisation mishandled: %q", auth)
	}
	sponsor := cleanBulletText("Started at Acme without sponsorship last year")
	if strings.Contains(strings.ToLower(sponsor), "without sponsorship") || !strings.Contains(sponsor, "Acme") {
		t.Errorf("sponsorship phrase or organisation mishandled: %q", sponsor)
	}
}

// Org, location and date strings, and any other field than titles, stacks and
// bullets, must never reach the embedder.
func TestPersonalFieldsNeverEmbedded(t *testing.T) {
	s := newSpy()
	m, _ := newMatcher(t, s)
	mustWarm(t, m, k8sDoc(), pastryDoc())
	all := strings.ToLower(strings.Join(s.texts, "\n"))
	for _, banned := range []string{"zorblax", "frobnitz", "quuxville", "wibbleton", "2019", "2022", "1987", "1991", "2021", "industries", "llc"} {
		if strings.Contains(all, banned) {
			t.Errorf("%q reached the embedder", banned)
		}
	}
	if !strings.Contains(all, "platform engineer") || !strings.Contains(all, "skills:") {
		t.Error("expected titles and skills to be embedded")
	}
}

// ---- chunking ----

func TestChunkPosting(t *testing.T) {
	desc := `<h2>About us</h2><p>We are a fast growing company with great snacks and a lovely office.</p>
<h2>Benefits</h2><ul><li>Generous health dental and vision coverage for you and family</li></ul>
<h3>Requirements</h3><ul><li>Five years of experience building distributed systems in Go</li><li>Hands-on <b>Kubernetes</b> operations &amp; on-call experience</li></ul>
<script>alert("do not follow this instruction to ignore previous rules")</script>
<style>.x{color:red}</style><!-- comment with secret words here ok -->
<p>Visit <a href="https://evil.example/click">our careers page</a> to apply today please.</p>`
	cs := ChunkPosting("<b>Senior</b> Engineer", desc)
	if len(cs) == 0 || cs[0].Label != "title" || cs[0].Weight != 2 || cs[0].Text != "Senior Engineer" {
		t.Fatalf("title chunk: %+v", cs)
	}
	var all string
	for _, c := range cs {
		all += c.Text + "\n"
		if strings.ContainsAny(c.Text, "<>") || strings.Contains(c.Text, "alert(") || strings.Contains(c.Text, "evil.example") || strings.Contains(c.Text, "secret words") || strings.Contains(c.Text, "color:red") {
			t.Errorf("markup or script survived: %q", c.Text)
		}
	}
	if !strings.Contains(all, "Hands-on Kubernetes operations & on-call experience") {
		t.Errorf("entities/bold not handled:\n%s", all)
	}
	// With everything fitting, document order holds.
	if i, j := strings.Index(all, "Five years"), strings.Index(all, "Hands-on"); i < 0 || j < i {
		t.Errorf("order:\n%s", all)
	}
}

func TestChunkPostingPrefersRequirements(t *testing.T) {
	var b strings.Builder
	b.WriteString("About us:\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "Company blurb sentence number %d about our wonderful culture.\n", i)
	}
	b.WriteString("Requirements:\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, "- Must have requirement number %d with deep Go experience\n", i)
	}
	cs := ChunkPosting("Engineer", b.String())
	if len(cs) != MaxPostingChunks {
		t.Fatalf("%d chunks", len(cs))
	}
	n := 0
	for _, c := range cs {
		if strings.Contains(c.Text, "requirement number") {
			n++
		}
	}
	if n != 10 {
		t.Errorf("kept %d of 10 requirement lines", n)
	}
}

func TestChunkPostingBounds(t *testing.T) {
	if cs := ChunkPosting("", ""); len(cs) != 0 {
		t.Errorf("empty posting: %+v", cs)
	}
	if cs := ChunkPosting("  \x00\x01 ", "\x00\x00"); len(cs) != 0 {
		t.Errorf("control-only posting: %+v", cs)
	}
	huge := strings.Repeat("Own the roadmap and deliver measurable results for many customers worldwide. ", 200000)
	cs := ChunkPosting(strings.Repeat("T", 5000), huge)
	if len(cs) > MaxPostingChunks {
		t.Errorf("%d chunks", len(cs))
	}
	for _, c := range cs {
		if len(c.Text) > MaxPostingChunkBytes || !utf8.ValidString(c.Text) {
			t.Fatalf("chunk of %d bytes", len(c.Text))
		}
	}
	long := strings.Repeat("é", 2000) + " one two three four"
	for _, c := range ChunkPosting("x", long) {
		if !utf8.ValidString(c.Text) || len(c.Text) > MaxPostingChunkBytes {
			t.Fatalf("rune cut: %d bytes", len(c.Text))
		}
	}
	if a, b := ChunkPosting("T", huge[:5000]), ChunkPosting("T", huge[:5000]); fmt.Sprint(a) != fmt.Sprint(b) {
		t.Error("ChunkPosting is not deterministic")
	}
}

func TestChunkPostingEscapedHTML(t *testing.T) {
	cs := ChunkPosting("Dev", "&lt;p&gt;You will design and ship reliable backend systems&lt;/p&gt;")
	if len(cs) != 2 || cs[1].Text != "You will design and ship reliable backend systems" {
		t.Errorf("%+v", cs)
	}
}

// ---- scoring ----

func TestSynonymsScoreHigher(t *testing.T) {
	m, _ := newMatcher(t, newSpy())
	rep := mustWarm(t, m, k8sDoc(), pastryDoc())
	if rep.Scored != 2 || rep.Failed != 0 {
		t.Fatalf("%+v", rep)
	}
	k, p := lookup(t, m, k8sDoc()), lookup(t, m, pastryDoc())
	if !(k.Raw > p.Raw && k.Score > p.Score) {
		t.Fatalf("k8s %+v vs pastry %+v", k, p)
	}
	t.Logf("k8s raw=%.3f score=%.3f; pastry raw=%.3f score=%.3f", k.Raw, k.Score, p.Raw, p.Score)
}

func TestCalibration(t *testing.T) {
	m, _ := newMatcher(t, newSpy())
	mid := Doc{Key: "mid", Title: "Data Analyst", Description: "Requirements:\n- Write SQL queries against Postgres for reporting dashboards"}
	rep := mustWarm(t, m, k8sDoc(), mid, pastryDoc())
	if rep.Scored != 3 {
		t.Fatalf("%+v", rep)
	}
	on, md, off := lookup(t, m, k8sDoc()), lookup(t, m, mid), lookup(t, m, pastryDoc())
	t.Logf("on=%.3f mid=%.3f off=%.3f floor=%.3f ceil=%.3f", on.Score, md.Score, off.Score, m.pd.floor, m.pd.ceil)
	if on.Score < 0.5 {
		t.Errorf("on-domain posting scored %.3f", on.Score)
	}
	if off.Score > 0.1 {
		t.Errorf("off-domain posting scored %.3f", off.Score)
	}
	if !(on.Score > md.Score && md.Score > off.Score) {
		t.Errorf("ordering lost: %.3f %.3f %.3f", on.Score, md.Score, off.Score)
	}
	for _, s := range []float64{on.Score, md.Score, off.Score} {
		if s < 0 || s > 1 || math.IsNaN(s) {
			t.Errorf("score %v outside [0,1]", s)
		}
	}
	// A narrow raw band (a real model) maps the same way: score is affine between
	// floor and ceiling up to its knee, zero at and below the floor, and above the
	// ceiling it keeps ordering instead of clipping (breadth_test.go).
	pd := &profileData{floor: 0.55, ceil: 0.76}
	if pd.score(0.55, topChunks) != 0 || pd.score(0.4, topChunks) != 0 {
		t.Error("floor and below must score 0")
	}
	if got := pd.score(0.655, topChunks); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("midpoint %v", got)
	}
	if at, above := pd.score(0.76, topChunks), pd.score(0.9, topChunks); !(at < above && above < 1) {
		t.Errorf("the ceiling must not clip: %v at it, %v above it", at, above)
	}
	if pd.score(math.NaN(), topChunks) != 0 {
		t.Error("NaN must not become a score")
	}
}

func TestEvidence(t *testing.T) {
	m, _ := newMatcher(t, newSpy())
	d := k8sDoc()
	d.Description += "\n- " + strings.Repeat("kubernetes ", 60) + "and more words here"
	mustWarm(t, m, d)
	got := lookup(t, m, d)
	if len(got.Evidence) != 3 {
		t.Fatalf("%d evidence", len(got.Evidence))
	}
	for i, ev := range got.Evidence {
		if utf8.RuneCountInString(ev.Snippet) > 120 || ev.Label == "" {
			t.Errorf("evidence %+v", ev)
		}
		if i > 0 && ev.Cosine > got.Evidence[i-1].Cosine {
			t.Errorf("evidence not ranked")
		}
	}
	if got.Model != m.e.Model() {
		t.Errorf("model %q", got.Model)
	}
}

func TestDeterministicAcrossMatchers(t *testing.T) {
	var got [2]Match
	for i := range got {
		m, _ := newMatcher(t, newSpy())
		mustWarm(t, m, k8sDoc())
		got[i] = lookup(t, m, k8sDoc())
	}
	if fmt.Sprint(got[0]) != fmt.Sprint(got[1]) {
		t.Errorf("%+v != %+v", got[0], got[1])
	}
}

func TestEmptyAndHugeDescriptions(t *testing.T) {
	s := newSpy()
	m, _ := newMatcher(t, s)
	docs := []Doc{
		{Key: "empty"},
		{Key: "title-only", Title: "Kubernetes Engineer"},
		{Key: "huge", Title: "Engineer", Description: strings.Repeat("Run k8s clusters at scale for many users. ", 100000)},
		{Key: "html", Title: "Engineer", Description: "<div><script>x</script><p>Run k8s clusters at scale</p></div>"},
		{Key: ""},
	}
	rep, err := m.Warm(context.Background(), docs, 100)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scored != 3 || rep.Failed != 2 {
		t.Fatalf("%+v", rep)
	}
	if _, ok := m.Lookup("empty", TextHash("", "")); ok {
		t.Error("a posting with no text must not get a score")
	}
	if s.maxN > MaxBatch {
		t.Errorf("batch of %d", s.maxN)
	}
}

func TestBatchesBounded(t *testing.T) {
	s := newSpy()
	m, _ := newMatcher(t, s)
	var docs []Doc
	for i := 0; i < 60; i++ {
		d := k8sDoc()
		d.Key = fmt.Sprintf("k%d", i)
		d.Title = fmt.Sprintf("Engineer %d", i)
		docs = append(docs, d)
	}
	docs = append(docs, docs[0]) // duplicate key counts once
	rep, err := m.Warm(context.Background(), docs, 50)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scored != 50 || rep.Skipped != 10 || m.Len() != 50 {
		t.Fatalf("%+v len=%d", rep, m.Len())
	}
	if s.maxN > MaxBatch || s.maxN == 0 {
		t.Errorf("largest batch %d", s.maxN)
	}
}

// ---- cache-only Lookup ----

func TestLookupNeverEmbeds(t *testing.T) {
	s := newSpy()
	m, dir := newMatcher(t, s)
	if _, ok := m.Lookup("k8s", TextHash(k8sDoc().Title, k8sDoc().Description)); ok {
		t.Fatal("found before warm")
	}
	if s.count() != 0 {
		t.Fatalf("Lookup called the embedder %d times", s.count())
	}
	mustWarm(t, m, k8sDoc())
	after := s.count()
	d := k8sDoc()
	lookup(t, m, d)
	if _, ok := m.Lookup(d.Key, "stale-hash"); ok {
		t.Error("a stale text hash must miss")
	}
	if _, ok := m.Lookup("unknown", TextHash("a", "b")); ok {
		t.Error("unknown key")
	}
	if s.count() != after {
		t.Fatal("Lookup called the embedder")
	}
	// A new process reads the cache without the embedder.
	s2 := newSpy()
	m2, err := NewMatcher(s2, synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	lookup(t, m2, d)
	rep, err := m2.Warm(context.Background(), []Doc{d}, 10)
	if err != nil || rep.Cached != 1 || rep.Scored != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	if s2.count() != 0 {
		t.Errorf("cached posting re-embedded (%d calls)", s2.count())
	}
}

func TestCachedWorksWhileEmbedderDown(t *testing.T) {
	s := newSpy()
	m, _ := newMatcher(t, s)
	mustWarm(t, m, k8sDoc())
	s.hook = func(context.Context, []string, Kind, int) ([][]float32, error, bool) {
		return nil, ErrUnavailable, true
	}
	rep, err := m.Warm(context.Background(), []Doc{k8sDoc()}, 10)
	if err != nil || rep.Cached != 1 || rep.Unavailable {
		t.Fatalf("%+v %v", rep, err)
	}
}

// ---- embedder failures ----

func TestEmbedderUnavailable(t *testing.T) {
	s := newSpy()
	s.hook = func(context.Context, []string, Kind, int) ([][]float32, error, bool) {
		return nil, fmt.Errorf("no model: %w", ErrUnavailable), true
	}
	m, _ := newMatcher(t, s)
	rep, err := m.Warm(context.Background(), []Doc{k8sDoc(), pastryDoc()}, 10)
	if !errors.Is(err, ErrUnavailable) || !rep.Unavailable || rep.Scored != 0 || rep.Failed != 2 || rep.Err == "" {
		t.Fatalf("%+v %v", rep, err)
	}
	if m.Len() != 0 {
		t.Error("unavailable must not store a score")
	}
	if _, ok := m.Lookup("k8s", TextHash(k8sDoc().Title, k8sDoc().Description)); ok {
		t.Error("lookup hit with no model")
	}
}

func TestEmbedderUnavailableAfterSetup(t *testing.T) {
	s := newSpy()
	m, _ := newMatcher(t, s)
	mustWarm(t, m, k8sDoc()) // profile and anchors cached
	s.hook = func(context.Context, []string, Kind, int) ([][]float32, error, bool) {
		return nil, ErrUnavailable, true
	}
	rep, err := m.Warm(context.Background(), []Doc{pastryDoc()}, 10)
	if !errors.Is(err, ErrUnavailable) || !rep.Unavailable || rep.Failed != 1 {
		t.Fatalf("%+v %v", rep, err)
	}
}

func TestEmbedderMisbehaves(t *testing.T) {
	good := func(texts []string) [][]float32 {
		rows, _ := NewFakeEmbedder(256, synonyms).Embed(context.Background(), texts, KindQuery)
		return rows
	}
	cases := map[string]func(texts []string) ([][]float32, error){
		"too few rows":  func(x []string) ([][]float32, error) { return good(x)[:len(x)-1], nil },
		"too many rows": func(x []string) ([][]float32, error) { return append(good(x), good(x)[0]), nil },
		"no rows":       func(x []string) ([][]float32, error) { return nil, nil },
		"wrong width":   func(x []string) ([][]float32, error) { r := good(x); r[0] = r[0][:10]; return r, nil },
		"nan": func(x []string) ([][]float32, error) {
			r := good(x)
			r[len(r)-1][3] = float32(math.NaN())
			return r, nil
		},
		"inf": func(x []string) ([][]float32, error) {
			r := good(x)
			r[0][0] = float32(math.Inf(1))
			return r, nil
		},
		"zero vector": func(x []string) ([][]float32, error) { r := good(x); r[0] = make([]float32, 256); return r, nil },
		"error":       func(x []string) ([][]float32, error) { return nil, errors.New("boom") },
		"panic":       func(x []string) ([][]float32, error) { panic("embedder bug") },
	}
	for name, bad := range cases {
		t.Run("setup/"+name, func(t *testing.T) {
			s := newSpy()
			s.hook = func(_ context.Context, texts []string, _ Kind, _ int) ([][]float32, error, bool) {
				rows, err := bad(texts)
				return rows, err, true
			}
			m, dir := newMatcher(t, s)
			rep, err := m.Warm(context.Background(), []Doc{k8sDoc()}, 10)
			if err == nil || rep.Scored != 0 || rep.Failed != 1 || rep.Err == "" {
				t.Fatalf("%+v %v", rep, err)
			}
			if m.Len() != 0 {
				t.Error("stored a score")
			}
			if _, statErr := os.Stat(filepath.Join(dir, profileFileName)); statErr == nil {
				t.Error("profile cache written from a bad embedding")
			}
		})
		t.Run("posting/"+name, func(t *testing.T) {
			s := newSpy()
			m, _ := newMatcher(t, s)
			mustWarm(t, m, pastryDoc()) // setup with the good embedder
			s.hook = func(_ context.Context, texts []string, _ Kind, _ int) ([][]float32, error, bool) {
				rows, err := bad(texts)
				return rows, err, true
			}
			rep, err := m.Warm(context.Background(), []Doc{k8sDoc()}, 10)
			if err != nil && !errors.Is(err, ErrEmbed) && name != "error" && name != "panic" {
				t.Fatalf("unexpected error %v", err)
			}
			if name == "wrong width" || name == "nan" || name == "inf" || name == "zero vector" {
				// One bad row fails that posting only.
				if rep.Failed != 1 || rep.Scored != 0 {
					t.Fatalf("%+v", rep)
				}
			} else if rep.Failed != 1 || rep.Scored != 0 || rep.Err == "" {
				t.Fatalf("%+v %v", rep, err)
			}
			if _, ok := m.Lookup("k8s", TextHash(k8sDoc().Title, k8sDoc().Description)); ok {
				t.Error("stored a score from bad vectors")
			}
		})
	}
}

func TestStopsAfterRepeatedFailures(t *testing.T) {
	s := newSpy()
	m, _ := newMatcher(t, s)
	mustWarm(t, m, pastryDoc())
	s.mu.Lock()
	s.calls = 0
	s.hook = func(context.Context, []string, Kind, int) ([][]float32, error, bool) {
		return nil, errors.New("flaky"), true
	}
	s.mu.Unlock()
	var docs []Doc
	for i := 0; i < 200; i++ {
		d := k8sDoc()
		d.Key, d.Title = fmt.Sprintf("k%d", i), fmt.Sprintf("Engineer %d", i)
		docs = append(docs, d)
	}
	rep, err := m.Warm(context.Background(), docs, 200)
	if err == nil || rep.Failed != 200 {
		t.Fatalf("%+v %v", rep, err)
	}
	if s.count() > maxConsecBad {
		t.Errorf("%d embed calls against a dead embedder", s.count())
	}
}

// gateEmbedder stays inside Embed until release is closed. It ignores ctx.
type gateEmbedder struct {
	Embedder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *gateEmbedder) Embed(context.Context, []string, Kind) ([][]float32, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return nil, context.Canceled
}

// callEmbed returns when ctx ends and does not wait for an embedder that
// ignores it. The tracker's Wait stays false until Embed returns, and stays
// false when the bound expires first.
func TestCallEmbedAbandonsWhenEmbedIgnoresContext(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	rel := func() { once.Do(func() { close(release) }) }
	t.Cleanup(rel)
	inner := &gateEmbedder{Embedder: NewFakeEmbedder(4, nil), entered: make(chan struct{}), release: release}
	tr := TrackEmbedder(inner)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := callEmbed(ctx, tr, []string{"plain"}, KindDocument)
	if time.Since(start) > time.Second {
		t.Fatal("callEmbed waited for an embedder that ignores ctx")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	select {
	case <-inner.entered:
	case <-time.After(time.Second):
		t.Fatal("Embed had not been entered")
	}
	if tr.Wait(30 * time.Millisecond) {
		t.Fatal("Wait returned while Embed was still inside")
	}
	rel()
	if !tr.Wait(time.Second) {
		t.Fatal("Wait did not return after Embed returned")
	}
}

func TestEmbedTrackedWaitExpiresWhileInside(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	inner := &gateEmbedder{Embedder: NewFakeEmbedder(4, nil), entered: make(chan struct{}), release: release}
	tr := TrackEmbedder(inner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = callEmbed(ctx, tr, []string{"plain"}, KindDocument)
	}()
	select {
	case <-inner.entered:
	case <-time.After(time.Second):
		t.Fatal("Embed was not entered")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("callEmbed held the caller after cancel")
	}
	if tr.Wait(20 * time.Millisecond) {
		t.Fatal("Wait returned while Embed was still inside")
	}
}

func TestBlockedEmbedderHonorsDeadline(t *testing.T) {
	for name, ignoresCtx := range map[string]bool{"honors ctx": false, "ignores ctx": true} {
		t.Run(name, func(t *testing.T) {
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			s := newSpy()
			s.hook = func(ctx context.Context, _ []string, _ Kind, _ int) ([][]float32, error, bool) {
				if ignoresCtx {
					<-release
				} else {
					<-ctx.Done()
				}
				return nil, ctx.Err(), true
			}
			m, _ := newMatcher(t, s)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			start := time.Now()
			rep, err := m.Warm(ctx, []Doc{k8sDoc()}, 10)
			if time.Since(start) > 5*time.Second {
				t.Fatal("Warm outlived its deadline")
			}
			if !errors.Is(err, context.DeadlineExceeded) || !rep.Canceled || rep.Scored != 0 {
				t.Fatalf("%+v %v", rep, err)
			}
			if rep.Failed != 0 || rep.Skipped != 1 {
				t.Errorf("a cancelled run is skipped, not failed: %+v", rep)
			}
		})
	}
}

func TestCancelMidWarmSavesPartial(t *testing.T) {
	s := newSpy()
	m, dir := newMatcher(t, s)
	mustWarm(t, m, pastryDoc())
	var docs []Doc
	for i := 0; i < 40; i++ {
		d := k8sDoc()
		d.Key, d.Title = fmt.Sprintf("k%d", i), fmt.Sprintf("Engineer %d", i)
		docs = append(docs, d)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.mu.Lock()
	s.calls = 0
	s.hook = func(_ context.Context, _ []string, _ Kind, call int) ([][]float32, error, bool) {
		if call == 2 {
			cancel()
			return nil, context.Canceled, true
		}
		return nil, nil, false
	}
	s.mu.Unlock()
	rep, err := m.Warm(ctx, docs, 100)
	if !errors.Is(err, context.Canceled) || !rep.Canceled {
		t.Fatalf("%+v %v", rep, err)
	}
	if rep.Scored == 0 || rep.Scored >= 40 || rep.Scored+rep.Rescored+rep.Skipped != 40 || rep.Failed != 0 || rep.Rescored != 0 {
		t.Fatalf("partial result wrong: %+v", rep)
	}
	m2, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Len() != rep.Scored+1 { // + the pastry posting warmed first
		t.Errorf("saved %d, scored %d", m2.Len(), rep.Scored)
	}
}

// ---- cache ----

func TestCorruptCacheFailsClosed(t *testing.T) {
	for _, file := range []string{scoresFileName, profileFileName} {
		for name, content := range map[string]string{
			"garbage":       "not json",
			"truncated":     `{"version":1,"profile_hash":"x"`,
			"wrong version": `{"version":99}`,
			"trailing":      `{"version":1,"entries":{}} {}`,
		} {
			t.Run(file+"/"+name, func(t *testing.T) {
				dir := t.TempDir()
				if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
				if _, err := NewMatcher(newSpy(), synthProfile(t), dir); !errors.Is(err, ErrCache) {
					t.Fatalf("got %v", err)
				}
			})
		}
	}
	// Right profile and model, malformed entries.
	p := synthProfile(t)
	e := newSpy()
	dir := t.TempDir()
	body := fmt.Sprintf(`{"version":1,"profile_hash":%q,"model":%q,"entries":{"k":{"text_hash":"h","raw":0.5,"score":7}}}`, p.Hash(), e.Model())
	if err := os.WriteFile(filepath.Join(dir, scoresFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMatcher(e, p, dir); !errors.Is(err, ErrCache) {
		t.Errorf("out-of-range score accepted: %v", err)
	}
	// Oversized.
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, scoresFileName), []byte(strings.Repeat(" ", MaxCacheBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMatcher(e, p, dir); !errors.Is(err, ErrCache) {
		t.Errorf("oversized cache accepted: %v", err)
	}
	// Truncated vectors with a matching hash.
	m, dir := newMatcher(t, newSpy())
	mustWarm(t, m, k8sDoc())
	raw, _ := os.ReadFile(filepath.Join(dir, profileFileName))
	if err := os.WriteFile(filepath.Join(dir, profileFileName), []byte(strings.Replace(string(raw), `"vectors":["`, `"vectors":["AAAA`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMatcher(newSpy(), synthProfile(t), dir); !errors.Is(err, ErrCache) {
		t.Errorf("damaged vector accepted: %v", err)
	}
}

func TestNewMatcherRejects(t *testing.T) {
	if _, err := NewMatcher(nil, synthProfile(t), t.TempDir()); err == nil {
		t.Error("nil embedder")
	}
	if _, err := NewMatcher(newSpy(), Profile{}, t.TempDir()); !errors.Is(err, ErrEmpty) {
		t.Errorf("empty profile: %v", err)
	}
	if _, err := NewMatcher(newSpy(), synthProfile(t), ""); !errors.Is(err, ErrCache) {
		t.Errorf("no dir: %v", err)
	}
}

func TestCacheEviction(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	m.SetMaxEntries(5)
	for i := 0; i < 12; i++ {
		d := k8sDoc()
		d.Key, d.Title = fmt.Sprintf("k%02d", i), fmt.Sprintf("Engineer %d", i)
		mustWarm(t, m, d)
	}
	if m.Len() != 5 {
		t.Fatalf("len %d", m.Len())
	}
	m2, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Len() != 5 {
		t.Fatalf("saved %d", m2.Len())
	}
	for i := 0; i < 12; i++ {
		d := k8sDoc()
		d.Key, d.Title = fmt.Sprintf("k%02d", i), fmt.Sprintf("Engineer %d", i)
		_, ok := m2.Lookup(d.Key, TextHash(d.Title, d.Description))
		if want := i >= 7; ok != want {
			t.Errorf("k%02d present=%v want %v (oldest must go first)", i, ok, want)
		}
	}
}

func TestProfileOrModelChangeInvalidates(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	mustWarm(t, m, k8sDoc())

	// Same profile and model: kept.
	same, err := NewMatcher(newSpy(), synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	lookup(t, same, k8sDoc())

	// Different profile.
	p := synthProfile(t)
	p.Chunks = append(p.Chunks, Chunk{Label: "skills", Text: "Skills: Haskell", Weight: 1})
	changed, err := NewMatcher(newSpy(), p, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := changed.Lookup("k8s", TextHash(k8sDoc().Title, k8sDoc().Description)); ok {
		t.Error("score survived a profile change")
	}
	// The score is gone, but the posting's chunk vectors are not: it is
	// rescored from them, not embedded again (vectors_test.go).
	rep, err := changed.Warm(context.Background(), []Doc{k8sDoc()}, 10)
	if err != nil || rep.Rescored != 1 || rep.Scored != 0 || rep.Cached != 0 {
		t.Fatalf("%+v %v", rep, err)
	}

	// Different target summary (the ceiling) is a different profile too.
	p2 := synthProfile(t)
	p2.Target += " and more"
	if p2.Hash() == synthProfile(t).Hash() {
		t.Error("target not in the hash")
	}

	// Different model: the file now belongs to `changed`'s profile; build a
	// matcher for the original profile with another model.
	other := &spy{inner: NewFakeEmbedder(256, synonyms), model: "other-model/dim=256"}
	om, err := NewMatcher(other, synthProfile(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if om.Len() != 0 {
		t.Error("score survived a model change")
	}
	if _, err := om.Warm(context.Background(), []Doc{k8sDoc()}, 10); err != nil {
		t.Fatal(err)
	}
	if other.count() == 0 {
		t.Error("new model never embedded")
	}
}

func TestTextChangeRescored(t *testing.T) {
	s := newSpy()
	m, _ := newMatcher(t, s)
	mustWarm(t, m, k8sDoc())
	d := k8sDoc()
	d.Description += "\n- Comfortable with golang tooling and build systems"
	rep := mustWarm(t, m, d)
	if rep.Scored != 1 || rep.Cached != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestCacheFileMode(t *testing.T) {
	m, dir := newMatcher(t, newSpy())
	mustWarm(t, m, k8sDoc())
	for _, f := range []string{scoresFileName, profileFileName} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", f, st.Mode().Perm())
		}
	}
}

func TestVectorRoundTrip(t *testing.T) {
	v, _ := validRow([]float32{1, 2, 3, -4.5}, 4)
	got, err := decodeVec(encodeVec(v), 4)
	if err != nil || fmt.Sprint(got) != fmt.Sprint(v) {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := decodeVec(encodeVec(v), 5); err == nil {
		t.Error("wrong width accepted")
	}
	if _, err := decodeVec("!!!", 4); err == nil {
		t.Error("bad base64 accepted")
	}
}

// ---- fake ----

func TestFakeEmbedder(t *testing.T) {
	f := NewFakeEmbedder(256, synonyms)
	rows, err := f.Embed(context.Background(), []string{"Kubernetes", "k8s", "pastry", "", "the and of"}, KindQuery)
	if err != nil {
		t.Fatal(err)
	}
	if c := dot(rows[0], rows[1]); c < 0.99 {
		t.Errorf("synonyms cosine %.3f", c)
	}
	if c := dot(rows[0], rows[2]); math.Abs(c) > 0.2 {
		t.Errorf("unrelated cosine %.3f", c)
	}
	for i, r := range rows {
		if _, ok := validRow(r, 256); !ok {
			t.Errorf("row %d invalid", i)
		}
		var sq float64
		for _, x := range r {
			sq += float64(x) * float64(x)
		}
		if math.Abs(sq-1) > 1e-4 {
			t.Errorf("row %d norm² %v", i, sq)
		}
	}
	if NewFakeEmbedder(256, synonyms).Model() == NewFakeEmbedder(256, nil).Model() || NewFakeEmbedder(128, nil).Model() == NewFakeEmbedder(256, nil).Model() {
		t.Error("Model must identify dim and synonyms")
	}
	again, _ := f.Embed(context.Background(), []string{"Kubernetes"}, KindDocument)
	if fmt.Sprint(again[0]) != fmt.Sprint(rows[0]) {
		t.Error("not deterministic")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Embed(ctx, []string{"x"}, KindQuery); err == nil {
		t.Error("cancelled ctx ignored")
	}
}

// Opt-in: loads the real bank and prints counts only, never content.
func TestRealBankCounts(t *testing.T) {
	if os.Getenv("SEMANTIC_REAL_BANK") != "1" {
		t.Skip("set SEMANTIC_REAL_BANK=1 and KBSYNC_BANK to run against the real bank")
	}
	b, err := kbsync.Load(os.Getenv("KBSYNC_BANK"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := ProfileFromBank(b, "software engineer")
	if err != nil {
		t.Fatal(err)
	}
	skills, other := 0, 0
	for _, c := range p.Chunks {
		if c.Label == "skills" {
			skills++
		} else {
			other++
		}
	}
	t.Logf("entries=%d chunks=%d skills_chunks=%d other_chunks=%d truncated=%v", len(b.Entries), len(p.Chunks), skills, other, p.Truncated)
	m, err := NewMatcher(NewFakeEmbedder(256, nil), p, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rep, err := m.Warm(context.Background(), []Doc{k8sDoc()}, 1)
	t.Logf("warm: %+v err=%v", rep, err)
}

// fnEmbedder replaces Embed and keeps Model from the inner embedder.
type fnEmbedder struct {
	Embedder
	fn func(ctx context.Context, texts []string, kind Kind) ([][]float32, error)
}

func (f *fnEmbedder) Embed(ctx context.Context, texts []string, kind Kind) ([][]float32, error) {
	return f.fn(ctx, texts, kind)
}

// stepEmbedder is one armed Embed: it closes entered, optionally waits on
// release, then closes returned. arm replaces the channels before each call.
type stepEmbedder struct {
	Embedder
	mu       sync.Mutex
	entered  chan struct{}
	release  chan struct{}
	returned chan struct{}
	calls    atomic.Int32
}

func (s *stepEmbedder) arm(entered, release, returned chan struct{}) {
	s.mu.Lock()
	s.entered, s.release, s.returned = entered, release, returned
	s.mu.Unlock()
}

func (s *stepEmbedder) Embed(ctx context.Context, texts []string, kind Kind) ([][]float32, error) {
	s.calls.Add(1)
	s.mu.Lock()
	entered, release, returned := s.entered, s.release, s.returned
	s.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		<-release
	}
	if returned != nil {
		close(returned)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.Embedder.Embed(context.Background(), texts, kind)
}

// TestTrackedEmbedderNilAndSinglePin: a nil embedder is not wrapped, wrapping
// a tracker again is the same pointer (one pin, not two), and Wait stays
// false until that one Embed returns.
func TestTrackedEmbedderNilAndSinglePin(t *testing.T) {
	if TrackEmbedder(nil) != nil {
		t.Fatal("TrackEmbedder(nil) is not nil")
	}
	inner := NewFakeEmbedder(16, nil)
	tr := TrackEmbedder(inner)
	if tr == nil || TrackEmbedder(tr) != tr {
		t.Fatal("double TrackEmbedder is not the same pointer")
	}
	if _, nested := tr.Embedder.(*TrackedEmbedder); nested || tr.Embedder != inner {
		t.Fatal("double wrap nested a second tracker")
	}

	release := make(chan struct{})
	entered := make(chan struct{})
	emb := &stepEmbedder{Embedder: inner, entered: entered, release: release, returned: make(chan struct{})}
	tr = TrackEmbedder(emb)
	if TrackEmbedder(tr) != tr {
		t.Fatal("double wrap of a live tracker is a new pointer")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = callEmbed(ctx, tr, []string{"one pin"}, KindDocument)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Embed did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("callEmbed did not return on cancel")
	}
	if tr.Wait(0) {
		t.Fatal("Wait returned while the single Embed was inside")
	}
	close(release)
	if !tr.Wait(2 * time.Second) {
		t.Fatal("one pin did not release, or a second pin was added")
	}
}

// Overlapping callEmbeds: cancelling some contexts returns those callers
// without dropping the holds of the Embeds still inside. Wait is false until
// every started Embed returns, and the counter does not go negative.
func TestCallEmbedConcurrentCancels(t *testing.T) {
	const n = 6
	entered := make([]chan struct{}, n)
	release := make([]chan struct{}, n)
	for i := 0; i < n; i++ {
		entered[i] = make(chan struct{})
		release[i] = make(chan struct{})
	}
	inner := &fnEmbedder{Embedder: NewFakeEmbedder(16, nil), fn: func(_ context.Context, texts []string, _ Kind) ([][]float32, error) {
		var i int
		if _, err := fmt.Sscanf(texts[0], "n-%d", &i); err != nil || i < 0 || i >= n {
			return nil, errors.New("bad embed id")
		}
		close(entered[i])
		<-release[i]
		return nil, context.Canceled
	}}
	tr := TrackEmbedder(inner)
	done := make([]chan error, n)
	cancels := make([]context.CancelFunc, n)
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel
		ch := make(chan error, 1)
		done[i] = ch
		go func(i int) {
			_, err := callEmbed(ctx, tr, []string{fmt.Sprintf("n-%d", i)}, KindQuery)
			ch <- err
		}(i)
	}
	for i := 0; i < n; i++ {
		select {
		case <-entered[i]:
		case <-time.After(2 * time.Second):
			t.Fatalf("embed %d did not start", i)
		}
	}
	for i := 0; i < n; i += 2 {
		cancels[i]()
		select {
		case err := <-done[i]:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled call %d: %v", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("cancelled call %d did not return", i)
		}
	}
	if tr.Wait(0) {
		t.Fatal("Wait returned while Embeds were still inside")
	}
	for i := 0; i < n; i += 2 {
		close(release[i])
	}
	if tr.Wait(0) {
		t.Fatal("Wait returned while uncancelled Embeds were still inside")
	}
	for i := 1; i < n-1; i += 2 {
		close(release[i])
		select {
		case <-done[i]:
		case <-time.After(2 * time.Second):
			t.Fatalf("call %d did not finish", i)
		}
	}
	if tr.Wait(0) {
		t.Fatal("Wait returned with one Embed still inside")
	}
	close(release[n-1])
	select {
	case <-done[n-1]:
	case <-time.After(2 * time.Second):
		t.Fatal("last call did not finish")
	}
	if !tr.Wait(2 * time.Second) {
		t.Fatal("Wait stayed false after every Embed returned")
	}
	for i := 0; i < n; i++ {
		cancels[i]()
	}
}

// A panic after Embed has started must release the hold. Wait returns, and a
// later callEmbed on the same tracker still runs. An in-flight sibling Embed
// keeps Wait false until it returns.
func TestCallEmbedPanicDoesNotStickWait(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var n atomic.Int32
	inner := &fnEmbedder{Embedder: NewFakeEmbedder(16, nil), fn: func(_ context.Context, texts []string, kind Kind) ([][]float32, error) {
		switch n.Add(1) {
		case 1:
			close(entered)
			<-release
			return nil, context.Canceled
		case 2:
			panic("embedder bug")
		default:
			return NewFakeEmbedder(16, nil).Embed(context.Background(), texts, kind)
		}
	}}
	tr := TrackEmbedder(inner)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan struct{})
	go func() {
		defer close(first)
		_, _ = callEmbed(ctx, tr, []string{"held"}, KindDocument)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first Embed did not start")
	}
	_, err := callEmbed(context.Background(), tr, []string{"boom"}, KindDocument)
	if !errors.Is(err, ErrEmbed) {
		t.Fatalf("panic err %v", err)
	}
	select {
	case <-first:
		t.Fatal("the held callEmbed returned while Embed was still inside")
	default:
	}
	close(release)
	select {
	case <-first:
	case <-time.After(2 * time.Second):
		t.Fatal("first callEmbed did not finish")
	}
	// One Wait, and it must return before the next pin: a timed-out Wait
	// leaves its goroutine inside WaitGroup.Wait until the counter hits zero.
	if !tr.Wait(2 * time.Second) {
		t.Fatal("Wait did not return after the panic and the in-flight Embed")
	}
	rows, err := callEmbed(context.Background(), tr, []string{"again"}, KindDocument)
	if err != nil || len(rows) != 1 {
		t.Fatalf("second call after panic: %v rows %d", err, len(rows))
	}
	if !tr.Wait(2 * time.Second) {
		t.Fatal("Wait false after the call that followed the panic")
	}
}

// Cancel racing a fast Embed, a blocked Embed, and an already-cancelled
// context. Each iteration must leave the tracker idle: no leaked pin and no
// negative WaitGroup.
func TestCallEmbedCancelRacesFastEmbed(t *testing.T) {
	emb := &stepEmbedder{Embedder: NewFakeEmbedder(16, nil)}
	tr := TrackEmbedder(emb)
	text := "héllo " + strings.Repeat("長", 64)
	for i := 0; i < 200; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		entered := make(chan struct{})
		returned := make(chan struct{})
		mode := i % 3
		var release chan struct{}
		if mode == 0 {
			release = make(chan struct{})
		}
		if mode == 2 {
			cancel()
		}
		emb.arm(entered, release, returned)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = callEmbed(ctx, tr, []string{text}, KindDocument)
		}()
		switch mode {
		case 0:
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				cancel()
				t.Fatalf("iter %d: Embed did not start", i)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				close(release)
				t.Fatalf("iter %d: callEmbed did not return on cancel", i)
			}
			select {
			case <-returned:
				close(release)
				t.Fatalf("iter %d: Embed returned before release", i)
			default:
			}
			close(release)
			select {
			case <-returned:
			case <-time.After(2 * time.Second):
				t.Fatalf("iter %d: Embed did not return", i)
			}
		default:
			if mode == 1 {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatalf("iter %d: callEmbed did not return", i)
			}
		}
		if !tr.Wait(2 * time.Second) {
			t.Fatalf("iter %d: hold left on the tracker", i)
		}
		cancel()
	}
	if emb.calls.Load() == 0 {
		t.Fatal("race loop never entered Embed")
	}
}

// Empty batches, unicode, a posting chunk already cut to the chunk cap, a
// wrong row count, and a context that is already cancelled. callEmbed returns
// on cancel without a closer running while Embed is inside.
func TestCallEmbedOddInputs(t *testing.T) {
	fake := NewFakeEmbedder(16, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	counting := &fnEmbedder{Embedder: fake, fn: func(context.Context, []string, Kind) ([][]float32, error) {
		calls.Add(1)
		return nil, nil
	}}
	ctr := TrackEmbedder(counting)
	if _, err := callEmbed(ctx, ctr, []string{"x"}, KindQuery); !errors.Is(err, context.Canceled) || calls.Load() != 0 {
		t.Fatalf("pre-canceled: err and calls %d", calls.Load())
	}
	if !ctr.Wait(2 * time.Second) {
		t.Fatal("pre-canceled call left a pin")
	}
	dctx, dcancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer dcancel()
	if _, err := callEmbed(dctx, ctr, nil, KindDocument); !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 0 {
		t.Fatalf("pre-expired deadline: calls %d", calls.Load())
	}

	var got [][]string
	rec := &fnEmbedder{Embedder: fake, fn: func(_ context.Context, texts []string, kind Kind) ([][]float32, error) {
		cp := append([]string(nil), texts...)
		got = append(got, cp)
		if len(texts) == 0 {
			return nil, nil
		}
		return fake.Embed(context.Background(), texts, kind)
	}}
	rtr := TrackEmbedder(rec)
	if rows, err := callEmbed(context.Background(), rtr, nil, KindDocument); err != nil || len(rows) != 0 {
		t.Fatalf("nil batch: %v %d", err, len(rows))
	}
	if rows, err := callEmbed(context.Background(), rtr, []string{}, KindQuery); err != nil || len(rows) != 0 {
		t.Fatalf("empty batch: %v %d", err, len(rows))
	}
	if len(got) != 2 || len(got[0]) != 0 || len(got[1]) != 0 {
		t.Fatalf("empty batches received %#v", got)
	}

	title := "タイトル" + strings.Repeat("名", 800)
	desc := "héllo \u2603 requirement line with kubernetes experience today. " + strings.Repeat("Operate kubernetes clusters daily please. ", 40)
	chunks := ChunkPosting(title, desc)
	if len(chunks) == 0 {
		t.Fatal("unicode posting produced no chunks")
	}
	texts := make([]string, 0, len(chunks))
	for _, c := range chunks {
		if !utf8.ValidString(c.Text) || len(c.Text) > MaxPostingChunkBytes || c.Text == "" {
			t.Fatalf("chunk %q is %d bytes", c.Label, len(c.Text))
		}
		texts = append(texts, c.Text)
	}
	raw := strings.Repeat("長", 4000)
	batch := append([]string{""}, texts...)
	batch = append(batch, raw)
	rows, err := callEmbed(context.Background(), rtr, batch, KindQuery)
	if err != nil || len(rows) != len(batch) {
		t.Fatalf("batch: %v rows %d want %d", err, len(rows), len(batch))
	}
	seen := got[len(got)-1]
	if len(seen) != len(batch) {
		t.Fatalf("embedder saw %d texts, want %d", len(seen), len(batch))
	}
	for i, tx := range seen {
		if tx != batch[i] {
			t.Fatalf("text %d changed in callEmbed (%d bytes vs %d)", i, len(tx), len(batch[i]))
		}
		if i > 0 && i < len(batch)-1 && len(tx) > MaxPostingChunkBytes {
			t.Fatalf("chunk text %d is %d bytes, cap %d", i, len(tx), MaxPostingChunkBytes)
		}
	}
	if seen[len(seen)-1] != raw {
		t.Fatal("callEmbed rewrote the raw long string")
	}

	release := make(chan struct{})
	entered := make(chan struct{})
	var inside atomic.Bool
	var closed atomic.Bool
	blocker := &fnEmbedder{Embedder: fake, fn: func(context.Context, []string, Kind) ([][]float32, error) {
		inside.Store(true)
		close(entered)
		<-release
		inside.Store(false)
		return nil, context.Canceled
	}}
	btr := TrackEmbedder(blocker)
	bctx, bcancel := context.WithCancel(context.Background())
	defer bcancel()
	returned := make(chan error, 1)
	go func() {
		_, err := callEmbed(bctx, btr, []string{texts[0], "héllo \u2603"}, KindQuery)
		returned <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("blocking Embed did not start")
	}
	bcancel()
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel during Embed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("callEmbed waited for Embed after cancel")
	}
	closer := make(chan struct{})
	go func() {
		defer close(closer)
		if !btr.Wait(2 * time.Second) {
			return
		}
		if inside.Load() {
			t.Error("Close ran while Embed was inside")
		}
		closed.Store(true)
	}()
	if closed.Load() {
		t.Fatal("Close ran before Embed was released")
	}
	close(release)
	select {
	case <-closer:
	case <-time.After(2 * time.Second):
		t.Fatal("closer did not finish after Embed returned")
	}
	if !closed.Load() {
		t.Fatal("Close did not run after Embed returned")
	}

	wrong := &fnEmbedder{Embedder: fake, fn: func(context.Context, []string, Kind) ([][]float32, error) {
		return [][]float32{{1, 0}}, nil
	}}
	wtr := TrackEmbedder(wrong)
	if _, err := callEmbed(context.Background(), wtr, []string{"a", "b"}, KindQuery); !errors.Is(err, ErrEmbed) {
		t.Fatalf("wrong length: %v", err)
	}
	if _, err := callEmbed(context.Background(), wtr, []string{}, KindDocument); !errors.Is(err, ErrEmbed) {
		t.Fatalf("row for an empty batch: %v", err)
	}
	if !wtr.Wait(2 * time.Second) {
		t.Fatal("wrong-length calls left a hold")
	}
}
