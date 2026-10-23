package ashby

import (
	"context"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/srctest"
)

// A Unicode board name is requested as its UTF-8 bytes, percent-escaped, and
// every key still sits under the token's prefix: the key never sees the name.
func TestUnicodeBoardNameIsEscapedAndKeysUseTheToken(t *testing.T) {
	const id = "11111111-2222-3333-4444-555555555555"
	const jobs = `{"jobs":[{"id":"` + id + `","title":"Engineer","location":"Zurich","isListed":true,` +
		`"publishedAt":"2026-10-01T00:00:00Z","jobUrl":"https://jobs.ashbyhq.com/x/` + id + `",` +
		`"applyUrl":"https://jobs.ashbyhq.com/x/` + id + `/application","descriptionPlain":"Build things."}]}`
	for _, c := range []struct{ name, token, escaped string }{
		{"Zürich Labs", "zurich-labs", "Z%C3%BCrich%20Labs"},
		{"东京 科技", "tokyo-tech", "%E4%B8%9C%E4%BA%AC%20%E7%A7%91%E6%8A%80"},
	} {
		want := "https://api.ashbyhq.com/posting-api/job-board/" + c.escaped + "?includeCompensation=true"
		s, d := newSource(map[string]srctest.Reply{want: {Status: 200, Body: []byte(jobs)}})
		b := sources.Board{ATS: posting.Ashby, Token: c.token, BoardName: c.name, Company: "Acme"}
		got, err := s.List(context.Background(), b)
		if err != nil {
			t.Fatalf("%q: %v", c.name, err)
		}
		if u := d.URLs(); len(u) != 1 || u[0] != want {
			t.Fatalf("%q: requested %v, want %s", c.name, u, want)
		}
		if len(got) != 1 || got[0].Key != posting.Key("ab:"+c.token+":"+id) {
			t.Fatalf("%q: postings %+v", c.name, got)
		}
		if !strings.HasPrefix(string(got[0].Key), b.KeyPrefix()) || b.KeyPrefix() != "ab:"+c.token+":" {
			t.Errorf("%q: key %q, prefix %q", c.name, got[0].Key, b.KeyPrefix())
		}
	}
}

func TestUnsafeUnicodeBoardNamesAreRefusedBeforeAnyRequest(t *testing.T) {
	for name, bn := range map[string]string{
		"RTL override":      "Acme‮Labs",
		"zero-width joiner": "Acme‍Labs",
		"combining mark":    "Café",
		"invalid UTF-8":     "Z\xc3",
		"fullwidth slash":   "a／b",
		"65 runes":          strings.Repeat("字", sources.MaxBoardNameLen+1),
		"too many bytes":    strings.Repeat("𠀀", sources.MaxBoardNameLen),
	} {
		t.Run(name, func(t *testing.T) {
			s, d := newSource(one(200, `{"jobs":[]}`))
			b := sources.Board{ATS: posting.Ashby, Token: "acme", BoardName: bn}
			if _, err := s.List(context.Background(), b); err == nil {
				t.Fatalf("board_name %q accepted", bn)
			}
			if n := len(d.URLs()); n != 0 {
				t.Errorf("refused board made %d requests", n)
			}
		})
	}
}
