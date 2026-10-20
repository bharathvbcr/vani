package sources

import (
	"fmt"
	"slices"
	"strings"

	"github.com/bharathvbcr/Vani/internal/posting"
)

// CheckFilters refuses a board's query filters unless every key is one
// FilterKeys lists for ats and carries at least one value. Search-site
// sources call it from New, so a typo in a filter fails at construction
// instead of silently widening the query.
func CheckFilters(ats posting.ATS, f map[string][]string) error {
	allowed := FilterKeys[ats]
	for k, vs := range f {
		if !slices.Contains(allowed, k) {
			return fmt.Errorf("filter %q is not one of %s", k, strings.Join(allowed, ", "))
		}
		if len(vs) == 0 {
			return fmt.Errorf("filter %q has no value", k)
		}
	}
	return nil
}
