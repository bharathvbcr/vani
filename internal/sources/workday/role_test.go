package workday

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/sources/srctest"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// withInfo is the fixture's first detail with jobPostingInfo field k set to
// v, as live details carry it.
func withInfo(t *testing.T, k string, v any) func(srctest.Request) (srctest.Reply, bool) {
	t.Helper()
	var d map[string]map[string]any
	if err := json.Unmarshal([]byte(read(t, "detail1.json")), &d); err != nil {
		t.Fatal(err)
	}
	d["jobPostingInfo"][k] = v
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

// The detail's timeType (live: "Full time", "Part time") leads as a role line
// in the shared wording; SourceText leaves the board's text.
func TestTimeTypeLine(t *testing.T) {
	for in, want := range map[any]string{"Part time": "Role: part-time\n\n", "Full time": "Role: full-time\n\n", "": "", nil: ""} {
		s, _ := newSource(withInfo(t, "timeType", in))
		got, err := s.List(context.Background(), board)
		if err != nil {
			t.Fatal(err)
		}
		d := got[0].Description
		if want == "" {
			if strings.HasPrefix(d, srcutil.RolePrefix) {
				t.Errorf("%v: %.40q", in, d)
			}
			continue
		}
		if !strings.HasPrefix(d, want) || srcutil.SourceText(d) != d[len(want):] {
			t.Errorf("%v: %.60q", in, d)
		}
	}
}

// Titles are one clean line, from the detail or else the listed row.
func TestTitleIsCleaned(t *testing.T) {
	s, _ := newSource(withInfo(t, "title", " NVIDIA 2027  Ignite\tInternships &amp; More "))
	got, err := s.List(context.Background(), board)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Title != "NVIDIA 2027 Ignite Internships & More" {
		t.Errorf("title %q", got[0].Title)
	}
}
