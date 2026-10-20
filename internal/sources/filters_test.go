package sources

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestCheckFilters(t *testing.T) {
	for ats, keys := range FilterKeys {
		if len(keys) == 0 {
			continue
		}
		if err := CheckFilters(ats, nil); err != nil {
			t.Errorf("%s: no filters: %v", ats, err)
		}
		if err := CheckFilters(ats, map[string][]string{keys[0]: {"x"}}); err != nil {
			t.Errorf("%s: allowed key %q refused: %v", ats, keys[0], err)
		}
		err := CheckFilters(ats, map[string][]string{keys[0]: nil})
		if err == nil || !strings.Contains(err.Error(), "has no value") {
			t.Errorf("%s: empty value: %v", ats, err)
		}
		err = CheckFilters(ats, map[string][]string{"not-a-filter": {"x"}})
		if err == nil || !strings.Contains(err.Error(), "is not one of") {
			t.Errorf("%s: unknown key: %v", ats, err)
		}
	}
	// A system with no verified filters refuses every key.
	if err := CheckFilters(posting.ATS("no-such-ats"), map[string][]string{"k": {"v"}}); err == nil {
		t.Error("unknown system accepted a filter")
	}
}
