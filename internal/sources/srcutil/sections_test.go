package srcutil

import (
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func TestSections(t *testing.T) {
	for name, tc := range map[string]struct {
		room  int
		parts []Section
		want  string
	}{
		"none":                         {100, nil, ""},
		"no room":                      {0, []Section{{"H", "<p>x</p>"}}, ""},
		"one, no heading":              {100, []Section{{"", "<p>Hello &amp; welcome</p>"}}, "Hello & welcome"},
		"headings":                     {200, []Section{{"Tasks", "<ul><li>One</li><li>Two</li></ul>"}, {"Perks", "<p>Tea</p>"}}, "Tasks\nOne\nTwo\n\nPerks\nTea"},
		"empty part drops its heading": {200, []Section{{"Tasks", "<p>Do</p>"}, {"Benefits", " <br> <p></p> "}, {"Perks", "<p>Tea</p>"}}, "Tasks\nDo\n\nPerks\nTea"},
		"heading is one line of text":  {200, []Section{{"  A &amp;\n B <b>C</b><script>x</script>", "<p>x</p>"}}, "A & B C\nx"},
		"markup is not kept":           {200, []Section{{"", `<script>alert(1)</script><p onclick="x()">Hi</p><style>p{}</style>`}}, "Hi"},
		"escaped markup stays text":    {200, []Section{{"", "&lt;script&gt;x&lt;/script&gt;"}}, "<script>x</script>"},
		"capped":                       {12, []Section{{"", "<p>abcdefghij</p>"}, {"", "<p>klmnop</p>"}}, "abcdefghij"},
		"unicode kept":                 {200, []Section{{"Über uns", "<p>日本語 — café</p>"}}, "Über uns\n日本語 — café"},
	} {
		got := Sections(tc.room, tc.parts...)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
		if len(got) > tc.room {
			t.Errorf("%s: %d bytes over the room %d", name, len(got), tc.room)
		}
	}
}

func TestSectionsCutsOnARuneBoundary(t *testing.T) {
	got := Sections(10, Section{HTML: strings.Repeat("é", 20)})
	if got != strings.Repeat("é", 5) {
		t.Fatalf("got %q (%d bytes)", got, len(got))
	}
}

func TestJoinDistinct(t *testing.T) {
	got := JoinDistinct("; ", " Berlin ", "", "berlin", "Remote\n  (EU)", "\t", "Remote (EU)", "München")
	if want := "Berlin; Remote (EU); München"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if JoinDistinct(", ") != "" {
		t.Fatal("no parts must join to nothing")
	}
}

func TestCheckTenant(t *testing.T) {
	ok := sources.Board{ATS: posting.Recruitee, Token: "syrve"}
	if err := CheckTenant(ok, posting.Recruitee); err != nil {
		t.Fatal(err)
	}
	if err := CheckTenant(ok, posting.Pinpoint); err == nil {
		t.Error("a board of another ATS was accepted")
	}
	for _, tok := range []string{"", " ", "a:b", "a.b", "evil.example", "A", "-a", "a_b", "a/b", "a@b", "a b", "a%2eb", strings.Repeat("a", 64)} {
		if err := CheckTenant(sources.Board{ATS: posting.Recruitee, Token: tok}, posting.Recruitee); err == nil {
			t.Errorf("token %q accepted", tok)
		}
	}
	if err := CheckTenant(sources.Board{ATS: posting.Recruitee, Token: "syrve", BoardName: "x"}, posting.Recruitee); err == nil {
		t.Error("board_name accepted outside Ashby")
	}
}
