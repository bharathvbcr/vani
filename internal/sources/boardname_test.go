package sources

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestBoardNameProblem(t *testing.T) {
	for _, ok := range []string{"Hippocratic AI", "ashby", "a", "A-b_c 9", "x y z", "é", strings.Repeat("a", MaxBoardNameLen)} {
		if why := BoardNameProblem(ok); why != "" {
			t.Errorf("%q refused: %s", ok, why)
		}
	}
	for _, bad := range []string{"", " ", " a", "a ", "a  b", "a\tb", "a\nb", "a/b", "..", ".", "a.b", "a%20b", "a?b", "a#b", "a:b", `a\b`, "a\x00b",
		strings.Repeat("a", MaxBoardNameLen+1)} {
		if why := BoardNameProblem(bad); why == "" {
			t.Errorf("%q accepted", bad)
		}
	}
}

// The key prefix is the token's, never the board name's.
func TestAPINameFallsBackToTheToken(t *testing.T) {
	b := Board{ATS: posting.Ashby, Token: "hippocratic-ai", BoardName: "Hippocratic AI"}
	if b.APIName() != "Hippocratic AI" || b.KeyPrefix() != "ab:hippocratic-ai:" {
		t.Errorf("APIName %q, KeyPrefix %q", b.APIName(), b.KeyPrefix())
	}
	b.BoardName = ""
	if b.APIName() != "hippocratic-ai" {
		t.Errorf("APIName %q", b.APIName())
	}
}
