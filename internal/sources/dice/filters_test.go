package dice

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources"
)

func TestFilterMapping(t *testing.T) {
	base := map[string]any{"keyword": "ml", "jobs_per_page": float64(100), "page_number": float64(1), "sort": "datePosted"}
	for name, c := range map[string]struct {
		filters  map[string][]string
		add      map[string]any
		windowed bool
	}{
		"location":          {map[string][]string{"location": {"  Austin,   TX "}}, map[string]any{"location": "Austin, TX"}, false},
		"radius":            {map[string][]string{"location": {"Austin, TX"}, "radius_miles": {"10"}}, map[string]any{"location": "Austin, TX", "radius": float64(10), "radius_unit": "mi"}, false},
		"posted 1":          {map[string][]string{"posted_within_days": {"1"}}, map[string]any{"posted_date": "ONE"}, true},
		"posted 3":          {map[string][]string{"posted_within_days": {"3"}}, map[string]any{"posted_date": "THREE"}, true},
		"posted 7":          {map[string][]string{"posted_within_days": {" 7 "}}, map[string]any{"posted_date": "SEVEN"}, true},
		"workplace":         {map[string][]string{"workplace_type": {"Hybrid", "REMOTE", "remote", "on-site"}}, map[string]any{"workplace_types": []any{"Remote", "On-Site", "Hybrid"}}, false},
		"employment":        {map[string][]string{"employment_type": {"contracts", "FullTime", "parttime", "third_party", "internship"}}, map[string]any{"employment_types": []any{"FULLTIME", "CONTRACTS", "PARTTIME", "THIRD_PARTY", "INTERNSHIP"}}, false},
		"employer":          {map[string][]string{"employer_type": {"other", "direct_hire", "Recruiter"}}, map[string]any{"employer_types": []any{"Direct Hire", "Recruiter", "Other"}}, false},
		"sponsorship":       {map[string][]string{"sponsorship": {"TRUE"}}, map[string]any{"willing_to_sponsor": true}, false},
		"windowed and more": {map[string][]string{"posted_within_days": {"3"}, "workplace_type": {"remote"}}, map[string]any{"posted_date": "THREE", "workplace_types": []any{"Remote"}}, true},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, map[string][]string{"ml": guids(0, 2)})
			b := diceBoard("ml")
			b.Filters = c.filters
			var st Stats
			if _, err := src(f.doer(), &Options{Report: func(_ sources.Board, x Stats) { st = x }}).List(context.Background(), b); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{}
			for k, v := range base {
				want[k] = v
			}
			for k, v := range c.add {
				want[k] = v
			}
			if got := f.toolCalls()[0].Args; !reflect.DeepEqual(got, want) {
				t.Fatalf("arguments\n%v\nwant\n%v", got, want)
			}
			if st.Windowed != c.windowed {
				t.Fatalf("windowed = %v, want %v", st.Windowed, c.windowed)
			}
		})
	}
}

func TestInvalidFilters(t *testing.T) {
	many := make([]string, sources.MaxFilterValues+1)
	for i := range many {
		many[i] = "remote"
	}
	for name, c := range map[string]struct {
		filters map[string][]string
		says    string
	}{
		"unknown key":          {map[string][]string{"country": {"us"}}, "not one of"},
		"no value":             {map[string][]string{"location": {}}, "no value"},
		"too many values":      {map[string][]string{"workplace_type": many}, "more than"},
		"two locations":        {map[string][]string{"location": {"Austin", "Dallas"}}, "exactly one"},
		"blank location":       {map[string][]string{"location": {"  "}}, "exactly one"},
		"remote location":      {map[string][]string{"location": {" remote "}}, "workplace_type remote"},
		"long location":        {map[string][]string{"location": {strings.Repeat("a", 101)}}, "at most"},
		"control in location":  {map[string][]string{"location": {"Austin\x00TX"}}, "place name"},
		"radius no location":   {map[string][]string{"radius_miles": {"10"}}, "needs a location"},
		"radius zero":          {map[string][]string{"location": {"Austin"}, "radius_miles": {"0"}}, "1 to 500"},
		"radius big":           {map[string][]string{"location": {"Austin"}, "radius_miles": {"501"}}, "1 to 500"},
		"radius not a number":  {map[string][]string{"location": {"Austin"}, "radius_miles": {"10mi"}}, "1 to 500"},
		"radius two":           {map[string][]string{"location": {"Austin"}, "radius_miles": {"10", "20"}}, "exactly one"},
		"posted 2":             {map[string][]string{"posted_within_days": {"2"}}, "1, 3 or 7"},
		"posted word":          {map[string][]string{"posted_within_days": {"SEVEN"}}, "1, 3 or 7"},
		"posted two":           {map[string][]string{"posted_within_days": {"1", "3"}}, "exactly one"},
		"workplace bad":        {map[string][]string{"workplace_type": {"remote", "office"}}, "not one of"},
		"employment bad":       {map[string][]string{"employment_type": {"full-time"}}, "not one of"},
		"employer bad":         {map[string][]string{"employer_type": {"Direct Hire"}}, "not one of"},
		"sponsorship false":    {map[string][]string{"sponsorship": {"false"}}, "must be true"},
		"sponsorship two":      {map[string][]string{"sponsorship": {"true", "true"}}, "exactly one"},
		"workplace blank item": {map[string][]string{"workplace_type": {""}}, "not one of"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, map[string][]string{"ml": guids(0, 2)})
			d := f.doer()
			b := diceBoard("ml")
			b.Filters = c.filters
			_, err := src(d, nil).List(context.Background(), b)
			if err == nil || !strings.Contains(err.Error(), c.says) {
				t.Fatalf("err = %v, want one saying %q", err, c.says)
			}
			if len(d.Requests()) != 0 {
				t.Fatalf("%d requests sent before the filters were checked", len(d.Requests()))
			}
		})
	}
}

func TestFilterKeysExported(t *testing.T) {
	want := []string{"location", "radius_miles", "posted_within_days", "workplace_type", "employment_type", "employer_type", "sponsorship"}
	if !reflect.DeepEqual(FilterKeys, want) {
		t.Fatalf("FilterKeys = %v", FilterKeys)
	}
}

func TestPayLine(t *testing.T) {
	for in, want := range map[string]string{
		"$120,000 - $150,000":                  "Pay range: USD 120000 – 150000",
		"USD 105,050.00 - 161,800.00 per year": "Pay range: USD 105050 – 161800 per year",
		"$55-$65/hr":                           "Pay range: USD 55 – 65 per hour",
		"$55 - 65 an hour":                     "Pay range: USD 55 – 65 per hour",
		"$120k - $150k":                        "Pay range: USD 120000 – 150000",
		"120,000 - 150,000 USD annually":       "Pay range: USD 120000 – 150000 per year",
		"CAD 90,000 to 110,000 a year":         "Pay range: CAD 90000 – 110000 per year",
		"$150,000 - $120,000":                  "",
		"Depends on Experience":                "",
		"$150,000":                             "",
		"120,000 - 150,000":                    "",
		"USD 100 - CAD 200":                    "",
		"$0 - $0":                              "",
		"":                                     "",
		"$100,000 - $150,000 plus bonus":       "",
		"Up to $150,000":                       "",
	} {
		if got := payLine(in); got != want {
			t.Errorf("payLine(%q) = %q, want %q", in, got, want)
		}
	}
}
