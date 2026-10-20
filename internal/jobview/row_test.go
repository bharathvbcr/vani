package jobview

import (
	"bytes"
	"encoding/json"
	"flag"
	"math"
	"os"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/rows.golden.json")

const goldenPath = "testdata/rows.golden.json"

// golden is the file the Swift (VaniApplyKit) and extension tests replay. It
// holds synthetic postings only: no personal data.
type golden struct {
	Version      int               `json:"version"`
	FitOrder     []string          `json:"fit_order"`
	FitLabels    map[string]string `json:"fit_labels"`
	StatusLabels map[string]string `json:"status_labels"`
	Rows         []Row             `json:"rows"`
}

func ip(n int) *int { return &n }

func goldenRows() []Row {
	return []Row{
		Build(Inputs{
			Key: "greenhouse:acme:1001", Title: "Backend Engineer", Company: "Acme Robotics", Location: "Remote, US",
			Status: "scored", Relevance: 82.4, Priority: 71.3, RelevanceBasis: "rank", RoleFit: "strong_fit",
			Reasons: []Reason{{Kind: "fits", Text: "Go services", Pct: ip(91)}, {Kind: "gap", Text: "Kafka"}},
			AgeDays: ip(3), FreshSrc: "posted",
		}),
		Build(Inputs{
			Key: "lever:globex:77", Title: "Platform Engineer", Company: "Globex",
			Status: "triaged", Relevance: 41, Priority: 30.5, RelevanceBasis: "fit", RoleFit: "mismatch_seniority",
			AgeDays: ip(40), FreshSrc: "seen",
		}),
		Build(Inputs{
			Key: "ashby:initech:9", Title: "Data Engineer", Company: "Initech", Location: "Austin, TX",
			Status: "dead", Relevance: 12, Priority: 6, RelevanceBasis: "none",
			Reasons: []Reason{{Kind: "other", Text: "No listed reason applies."}},
		}),
		Build(Inputs{
			Key: "workable:umbrella:5", Title: "Systems Engineer", Company: "Umbrella",
			Status: "duplicate", Relevance: 55, Priority: 20, RoleFit: "not_in_the_fixture",
			DupLinks: []string{"greenhouse:umbrella:4", "", "lever:umbrella:6"}, AgeDays: ip(0), FreshSrc: "seen",
		}),
		Build(Inputs{
			Key: "greenhouse:hooli:3", Title: "Staff Engineer", Company: "Hooli",
			Status: "unsure", Relevance: 63.8, Priority: 63.8, RoleFit: "adjacent", AgeDays: ip(95), FreshSrc: "posted",
		}),
	}
}

func TestGolden(t *testing.T) {
	want := golden{
		Version: Version, FitOrder: FitOrder[:], FitLabels: FitLabels(), StatusLabels: StatusLabels(), Rows: goldenRows(),
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(want); err != nil {
		t.Fatal(err)
	}
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("%v (go test ./internal/jobview -update writes it)", err)
	}
	if !bytes.Equal(got, buf.Bytes()) {
		t.Fatalf("%s is stale: run go test ./internal/jobview -update and commit it", goldenPath)
	}
	var back golden
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	for _, r := range back.Rows {
		if err := Check(r.V); err != nil {
			t.Errorf("%s: %v", r.Key, err)
		}
	}
}

func TestBuildLabelsAndFit(t *testing.T) {
	r := Build(Inputs{Key: "k", Status: "dead", RoleFit: "mismatch_domain"})
	if r.V != Version || r.StatusLabel != "Closed" || r.Fit != "mismatch_domain" || r.FitLabel != "Different field" {
		t.Fatalf("%+v", r)
	}
	// A reading outside the fixture is dropped, never shown as a known fit.
	r = Build(Inputs{Key: "k", Status: "future_status", RoleFit: "good_fit"})
	if r.Fit != "" || r.FitLabel != "" {
		t.Fatalf("unknown fit leaked: %+v", r)
	}
	if r.StatusLabel != "future_status" {
		t.Fatalf("unknown status should show as itself: %q", r.StatusLabel)
	}
	for _, f := range FitOrder {
		if FitLabel(f) == "" {
			t.Errorf("no label for %s", f)
		}
	}
}

func TestBuildSimilarCountsNonEmptyLinks(t *testing.T) {
	if n := Build(Inputs{DupLinks: []string{"", "a", "", "b"}}).Similar; n != 2 {
		t.Fatalf("similar = %d", n)
	}
	if n := Build(Inputs{}).Similar; n != 0 {
		t.Fatalf("similar = %d", n)
	}
}

func TestBuildNonFiniteScoresStayEncodable(t *testing.T) {
	r := Build(Inputs{Relevance: math.NaN(), Priority: math.Inf(1)})
	if _, err := json.Marshal(r); err != nil {
		t.Fatal(err)
	}
	if r.Relevance != 0 || r.Priority != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestAgeLabel(t *testing.T) {
	cases := []struct {
		days *int
		src  string
		want string
	}{
		{nil, "posted", ""}, {ip(-1), "posted", ""}, {ip(0), "posted", "today"}, {ip(5), "posted", "5d"},
		{ip(13), "posted", "13d"}, {ip(14), "posted", "2w"}, {ip(40), "seen", "~5w"}, {ip(59), "posted", "8w"},
		{ip(60), "posted", "2mo"}, {ip(400), "seen", "~13mo"},
	}
	for _, c := range cases {
		if got := AgeLabel(c.days, c.src); got != c.want {
			t.Errorf("AgeLabel(%v,%s) = %q, want %q", c.days, c.src, got, c.want)
		}
	}
}

func TestCheckRejectsUnknownVersions(t *testing.T) {
	for _, v := range []int{0, -1, Version + 1, 99} {
		err := Check(v)
		if err == nil || !strings.Contains(err.Error(), "not supported") {
			t.Errorf("Check(%d) = %v", v, err)
		}
	}
	if err := Check(Version); err != nil {
		t.Fatal(err)
	}
}
