package jobinsight

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

// The Mac app offers a currency picker from its own list
// (macos/Sources/VaniApplyKit/Jobs.swift, JobsList.payCurrencies). It must
// name exactly the currencies ParsePay reads: one it lacks cannot be picked,
// and one ParsePay does not read filters every posting out.
func TestSwiftPayCurrenciesMatch(t *testing.T) {
	src, err := os.ReadFile("../../macos/Sources/VaniApplyKit/Jobs.swift")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)static let payCurrencies = \[(.*?)\]`).FindSubmatch(src)
	if m == nil {
		t.Fatal("payCurrencies not found in Jobs.swift")
	}
	var swift []string
	for _, c := range regexp.MustCompile(`"([A-Z]{3})"`).FindAllSubmatch(m[1], -1) {
		swift = append(swift, string(c[1]))
	}
	if len(swift) != len(slices.Compact(slices.Sorted(slices.Values(swift)))) {
		t.Errorf("Jobs.swift repeats a currency: %v", swift)
	}
	slices.Sort(swift)
	if go_ := PayCurrencies(); !slices.Equal(swift, go_) {
		t.Errorf("Jobs.swift payCurrencies %v, jobinsight.PayCurrencies %v", swift, go_)
	}
}
