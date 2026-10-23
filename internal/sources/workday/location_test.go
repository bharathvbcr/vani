package workday

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/location"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// withAdditional is the fixture's first detail with additionalLocations set
// to more (raw JSON), as live details carry it.
func withAdditional(t *testing.T, more string) func(srctest.Request) (srctest.Reply, bool) {
	t.Helper()
	var d map[string]map[string]any
	if err := json.Unmarshal([]byte(read(t, "detail1.json")), &d); err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal([]byte(more), &v); err != nil {
		t.Fatal(err)
	}
	d["jobPostingInfo"]["additionalLocations"] = v
	raw, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	base := fixtureHandler(t)
	return func(r srctest.Request) (srctest.Reply, bool) {
		if strings.HasSuffix(r.URL, "_JR2027047") {
			return ok(string(raw)), true
		}
		return base(r)
	}
}

// NVIDIA JR2021784 (live, 2026-10-20) lists its remote place only as an
// additional location; the posting must carry it.
func TestAdditionalLocations(t *testing.T) {
	s, _ := newSource(withAdditional(t, `["US, CA, Remote"]`))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	p := got[0]
	if p.Location != "US, CA, Santa Clara; US, CA, Remote" {
		t.Errorf("location %q", p.Location)
	}
	if !location.Parse(p.Location, p.Remote).Remote {
		t.Errorf("%q does not read as remote", p.Location)
	}
	if got[1].Location == "" || strings.Contains(got[1].Location, ";") {
		t.Errorf("a detail without additional locations changed: %q", got[1].Location)
	}
}

// A non-string entry is a detail of another shape: it fails loudly, like any
// other misread field, rather than dropping places.
func TestAdditionalLocationsWrongShape(t *testing.T) {
	for _, more := range []string{`[1]`, `"US, CA, Remote"`, `[{"name":"x"}]`} {
		s, _ := newSource(withAdditional(t, more))
		if _, err := s.List(context.Background(), board); err == nil {
			t.Errorf("%s: no error", more)
		}
	}
	for _, more := range []string{`null`, `[]`} {
		s, _ := newSource(withAdditional(t, more))
		got, err := s.List(context.Background(), board)
		if err != nil || got[0].Location != "US, CA, Santa Clara" {
			t.Errorf("%s: %v %+v", more, err, got)
		}
	}
}

func TestDetailLocation(t *testing.T) {
	many := make([]string, 1000)
	for i := range many {
		many[i] = "Place " + strings.Repeat("x", i%7) + string(rune('A'+i%26))
	}
	for _, c := range []struct {
		name, primary string
		more          []string
		want          string
	}{
		// Intel JR0288049, live 2026-10-20.
		{"intel", "US, Arizona, Phoenix", []string{"US, California, Santa Clara", "US, Oregon, Hillsboro"}, "US, Arizona, Phoenix; US, California, Santa Clara; US, Oregon, Hillsboro"},
		{"none", "US, CA, Santa Clara", nil, "US, CA, Santa Clara"},
		{"empty everything", "", nil, ""},
		{"blank entries", "  ", []string{"", " ", "US, CA, Remote"}, "US, CA, Remote"},
		{"repeat of primary, any case", "US, CA, Santa Clara", []string{"us, ca, santa clara", "US, CA, Santa Clara "}, "US, CA, Santa Clara"},
		{"duplicates among more", "A", []string{"B", "B", "b"}, "A; B"},
	} {
		if got := detailLocation(c.primary, c.more); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	if n := strings.Count(detailLocation("Primary", many), "; ") + 1; n != maxLocations {
		t.Errorf("joined %d places, want %d", n, maxLocations)
	}
}
