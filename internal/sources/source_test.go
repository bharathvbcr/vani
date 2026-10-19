package sources

import (
	"slices"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestSearchTerms(t *testing.T) {
	for name, tc := range map[string]struct {
		b    Board
		want []string
	}{
		"none":            {Board{}, nil},
		"search only":     {Board{Search: " TensorRT "}, []string{"TensorRT"}},
		"union in order":  {Board{Search: "a", Searches: []string{"b", "c"}}, []string{"a", "b", "c"}},
		"case duplicates": {Board{Search: "LLM", Searches: []string{"llm", " Llm ", "CUDA"}}, []string{"LLM", "CUDA"}},
		"empty terms":     {Board{Searches: []string{"", "  ", "x"}}, []string{"x"}},
	} {
		if got := tc.b.SearchTerms(); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}

func TestFilterKeysOnlyForSearchSites(t *testing.T) {
	for ats := range FilterKeys {
		switch ats {
		case posting.Google, posting.Amazon, posting.Apple:
		default:
			t.Errorf("filters listed for %q, which has no verified filters", ats)
		}
	}
}

func TestKeyPrefixForSearchSites(t *testing.T) {
	for _, tc := range []struct {
		b    Board
		want string
	}{
		{Board{ATS: posting.Amazon, Token: "Amazon"}, "az:amazon:"},
		{Board{ATS: posting.Eightfold, Token: "microsoft.com"}, "ef:microsoft.com:"},
		{Board{ATS: posting.Workday, Token: "nvidia/Site"}, "wd:nvidia:site:"},
	} {
		if got := tc.b.KeyPrefix(); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.b, got, tc.want)
		}
	}
}
