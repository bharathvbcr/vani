package eval

import "testing"

// An only_locations drop that rests on a place the tables do not know is its
// own class, apart from one on a place they know to be elsewhere, so a wrong
// drop of a US town they lack has a stratum to surface in.
func TestDropClassSplitsUnrecognisedLocations(t *testing.T) {
	for name, c := range map[string]struct{ why, want string }{
		"unrecognised":          {"location is outside only_locations (unrecognised location)", "exclude:location_unrecognised"},
		"with the prefix":       {"excluded: location is outside only_locations (unrecognised location)", "exclude:location_unrecognised"},
		"recognised elsewhere":  {"location is outside only_locations", "exclude:location"},
		"an exclude entry":      {`location matches "unrecognised location"`, "exclude:location"},
		"the mark on a title":   {`title matches "x (unrecognised location)"`, "exclude:title"},
		"the mark not at close": {"location is outside only_locations (unrecognised location) extra", "exclude:location"},
	} {
		ann := map[string]string{"triage.backend": "exclude", AnnExcludeMatched: c.why}
		got := DropClass(droppedWith(ann), classRules)
		if got != c.want {
			t.Errorf("%s: DropClass = %q, want %q", name, got, c.want)
		}
		if src := "dropped:" + got; !validSource(src) {
			t.Errorf("%s: %q is not a valid label source", name, src)
		}
		if AuditCell(AuditCandidate{Dropped: true, Class: got}) != "dropped:"+c.want {
			t.Errorf("%s: cell %q", name, AuditCell(AuditCandidate{Dropped: true, Class: got}))
		}
	}
}
