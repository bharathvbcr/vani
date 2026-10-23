package config

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// An Ashby board whose name has letters outside ASCII is a valid board; its
// token stays the ASCII slug every key is built from.
func TestValidBoardTakesAUnicodeBoardName(t *testing.T) {
	for _, name := range []string{"Zürich Labs", "东京 科技"} {
		b := sources.Board{ATS: posting.Ashby, Token: "acme-labs", BoardName: name, Company: "Acme"}
		if err := ValidBoard(b); err != nil {
			t.Errorf("board_name %q refused: %v", name, err)
		}
	}
	for name, bn := range map[string]string{
		"RTL override":      "Acme‮Labs",
		"zero-width joiner": "Acme‍Labs",
		"combining mark":    "Café",
		"invalid UTF-8":     "Z\xc3",
		"65 runes":          strings.Repeat("字", sources.MaxBoardNameLen+1),
	} {
		b := sources.Board{ATS: posting.Ashby, Token: "acme-labs", BoardName: bn, Company: "Acme"}
		if err := ValidBoard(b); err == nil || !strings.Contains(err.Error(), "board_name") {
			t.Errorf("%s: board_name %q gave %v, want a board_name refusal", name, bn, err)
		}
	}
}
