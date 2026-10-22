package srcutil

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// board_name is Ashby's alone: every other adapter, handed one, refuses the
// board rather than quietly ignoring a name it would never request.
func TestCheckBoardBoardName(t *testing.T) {
	if err := CheckBoard(sources.Board{ATS: posting.Ashby, Token: "hippocratic-ai", BoardName: "Hippocratic AI"}, posting.Ashby); err != nil {
		t.Fatalf("ashby board_name refused: %v", err)
	}
	for _, c := range []struct {
		b    sources.Board
		want posting.ATS
		msg  string
	}{
		{sources.Board{ATS: posting.Greenhouse, Token: "acme", BoardName: "Acme"}, posting.Greenhouse, "ab only"},
		{sources.Board{ATS: posting.Lever, Token: "acme", BoardName: "Acme"}, posting.Lever, "ab only"},
		{sources.Board{ATS: posting.Ashby, Token: "acme", BoardName: "a/b"}, posting.Ashby, "board_name"},
		{sources.Board{ATS: posting.Ashby, Token: "acme", BoardName: " a"}, posting.Ashby, "board_name"},
		{sources.Board{ATS: posting.Ashby, Token: "acme", BoardName: strings.Repeat("a", sources.MaxBoardNameLen+1)}, posting.Ashby, "board_name"},
	} {
		err := CheckBoard(c.b, c.want)
		if err == nil || !strings.Contains(err.Error(), c.msg) {
			t.Errorf("CheckBoard(%+v) = %v, want error containing %q", c.b, err, c.msg)
		}
	}
}
