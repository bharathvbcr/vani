package apple

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// multiFixture is a live posting (2026-10-20) in Cupertino, San Diego and
// Seattle, with one Pay & Benefits footer per location: $175,000 to $308,500
// in San Diego and Seattle, $184,700 to $324,800 in Cupertino. San Diego's
// footer comes first.
func multiFixture(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/jobsdata_multi.json")
	if err != nil {
		t.Fatal(err)
	}
	var jd map[string]any
	if err := json.Unmarshal(raw, &jd); err != nil {
		t.Fatal(err)
	}
	return jd
}

func decodeFixture(t *testing.T, jd map[string]any) jobsData {
	t.Helper()
	raw, err := json.Marshal(jd)
	if err != nil {
		t.Fatal(err)
	}
	var out jobsData
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The pay line is the envelope of every location's range, not the first
// footer's; it leads, SourceText removes it (so stored postings re-render
// rather than re-triage), and jobinsight reads the full envelope.
func TestMultiLocationPay(t *testing.T) {
	jd := multiFixture(t)
	slug := jd["transformedPostingTitle"].(string)
	pages := map[string]string{
		pageURL("x", nil, 1):                 searchPage("1", [3]string{"200688031", "0836", slug}),
		detailURL("200688031", "0836", slug): detailsPage(t, jd),
	}
	s, _ := newSource(pages)
	got, err := s.List(context.Background(), board("x"))
	if err != nil {
		t.Fatal(err)
	}
	d := got[0].Description
	line, rest, _ := strings.Cut(d, "\n\n")
	if line != "Pay range: USD 175000 – 324800" {
		t.Errorf("pay line %q", line)
	}
	if srcutil.SourceText(d) != rest || !strings.HasPrefix(rest, "Level: ") {
		t.Errorf("SourceText is not the description after the pay line: %.80q", srcutil.SourceText(d))
	}
	pays := jobinsight.ParsePay(d)
	if len(pays) != 1 || pays[0].Currency != "USD" || pays[0].Min != 175000 || pays[0].Max != 324800 {
		t.Errorf("ParsePay %+v", pays)
	}
	if got[0].Location != "Cupertino, California, United States; San Diego, California, United States; Seattle, Washington, United States" {
		t.Errorf("location %q", got[0].Location)
	}
}

// Without the pay line, the description is exactly the pre-change rendering,
// so adding it changes no stored posting's board text.
func TestPayLineLeavesBoardText(t *testing.T) {
	jd := decodeFixture(t, multiFixture(t))
	with := description(jd)
	jd.PostingFooters[0].PostLocationID = "postLocation-NONE"
	jd.PostingFooters[1].PostLocationID = "postLocation-NONE"
	jd.PostingFooters[2].PostLocationID = "postLocation-NONE"
	without := description(jd)
	if strings.HasPrefix(without, srcutil.PayLinePrefix) || srcutil.SourceText(with) != without {
		t.Errorf("board text differs:\n%.120q\n%.120q", srcutil.SourceText(with), without)
	}
}

func footer(id, content string) map[string]any {
	return map[string]any{"postLocationId": id, "localizations": map[string]any{
		"en_US": []any{map[string]any{"name": "Pay & Benefits", "content": content}}}}
}

func usLoc(id string) map[string]any {
	return map[string]any{"id": id, "name": id, "city": id, "countryName": "United States", "countryID": "iso-country-USA"}
}

func TestPayLineCases(t *testing.T) {
	sentence := func(lo, hi string) string {
		return "<p>The base pay range for this role is between " + lo + " and " + hi + ", and your base pay will depend on your skills.</p>"
	}
	us := []any{usLoc("A"), usLoc("B")}
	gb := []any{map[string]any{"id": "L", "city": "London", "countryName": "United Kingdom", "countryID": "iso-country-GBR"}}
	for _, c := range []struct {
		name    string
		locs    []any
		footers []any
		want    string
	}{
		{"one", us, []any{footer("A", sentence("$100,000", "$150,000"))}, "Pay range: USD 100000 – 150000"},
		{"envelope", us, []any{footer("A", sentence("$100,000", "$150,000")), footer("B", sentence("$90,000.50", "$160,000"))}, "Pay range: USD 90000.5 – 160000"},
		// An hourly word outside the range sentence is not seen: the line
		// states no period (and ParsePay does not read $22 as a year's pay,
		// below).
		{"hourly elsewhere", us, []any{footer("A", sentence("$22.00", "$35.50")+" per hour")}, "Pay range: USD 22 – 35.5"},
		{"hourly inline", us, []any{footer("A", "base pay range for this role is between $22.00 and $35.50 per hour.")}, "Pay range: USD 22 – 35.5 per hour"},
		{"hourly and annual", us, []any{footer("A", "base pay range for this role is between $22.00 and $35.50 per hour."), footer("B", sentence("$90,000", "$100,000"))}, ""},
		{"non-US location", gb, []any{footer("L", sentence("$100,000", "$150,000"))}, ""},
		{"unknown location id", us, []any{footer("Z", sentence("$100,000", "$150,000"))}, ""},
		{"empty id", []any{usLoc("")}, []any{footer("", sentence("$100,000", "$150,000"))}, ""},
		{"inverted", us, []any{footer("A", sentence("$150,000", "$100,000"))}, ""},
		{"inverted skipped, other read", us, []any{footer("A", sentence("$150,000", "$100,000")), footer("B", sentence("$1,000", "$2,000"))}, "Pay range: USD 1000 – 2000"},
		{"zero", us, []any{footer("A", sentence("$0", "$0"))}, ""},
		{"no sentence", us, []any{footer("A", "<p>Apple employees also have the opportunity to become shareholders.</p>")}, ""},
		{"bad commas", us, []any{footer("A", sentence("$1,00", "$2,000"))}, ""},
		{"no footers", us, nil, ""},
		{"oversized amount", us, []any{footer("A", sentence("$"+strings.Repeat("9", 400), "$1"))}, ""},
	} {
		jd := decodeFixture(t, map[string]any{"locations": c.locs, "postingFooters": c.footers})
		if got := jd.payLine(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	for _, p := range jobinsight.ParsePay("Pay range: USD 22 – 35.5") {
		if p.Period == jobinsight.Year {
			t.Errorf("an unstated $22 reads as a year's pay: %+v", p)
		}
	}
	// Footers past the bound are not read.
	var many []any
	for range maxFooters {
		many = append(many, footer("Z", "x"))
	}
	many = append(many, footer("A", sentence("$100,000", "$150,000")))
	if got := decodeFixture(t, map[string]any{"locations": us, "postingFooters": many}).payLine(); got != "" {
		t.Errorf("footer past the bound read: %q", got)
	}
}

// A metro location (no city) keeps its name ahead of the state.
func TestMetroLocation(t *testing.T) {
	jd := fixture(t)
	jd = with(jd, "locations", []any{
		map[string]any{"id": "postLocation-SFMETRO", "name": "San Francisco Bay Area", "stateProvince": "California", "countryName": "United States"},
		map[string]any{"name": "Cupertino", "city": "Cupertino", "stateProvince": "California", "countryName": "United States"},
		map[string]any{"name": "", "city": "", "stateProvince": "", "countryName": ""},
		map[string]any{"name": "Remote Office"},
	})
	p, err := decodeFixture(t, jd).posting(board("x"), "https://jobs.apple.com/en-us/details/1-1/x", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	if p.Location != "San Francisco Bay Area, California, United States; Cupertino, California, United States; Remote Office" {
		t.Errorf("location %q", p.Location)
	}
}
