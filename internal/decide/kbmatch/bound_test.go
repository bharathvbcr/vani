package kbmatch

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestQuestionBoundsTitleLocationAndDigest(t *testing.T) {
	digest, err := DigestOf(testProfile())
	if err != nil {
		t.Fatal(err)
	}
	huge := strings.Repeat("é", 1<<19) // 1 MB of two-byte runes
	cases := map[string]struct {
		p posting.Posting
		d Digest
	}{
		"title":    {posting.Posting{Title: huge, Description: "Build things."}, digest},
		"location": {posting.Posting{Title: "Engineer", Location: huge, Description: "Build things."}, digest},
		"digest":   {posting.Posting{Title: "Engineer", Description: "Build things."}, Digest{Text: huge, SHA256: "x"}},
	}
	for name, c := range cases {
		q, truncated := Question(c.p, "target roles", c.d, nil)
		if len(q.Context) > jdtriage.MaxContextBytes {
			t.Errorf("%s: context is %d bytes, over %d", name, len(q.Context), jdtriage.MaxContextBytes)
		}
		if !truncated || q.Facts[jdtriage.FactContextTruncated] != "true" {
			t.Errorf("%s: truncated = %v, fact %q", name, truncated, q.Facts[jdtriage.FactContextTruncated])
		}
		if !utf8.Valid(q.Context) {
			t.Errorf("%s: the cut split a UTF-8 sequence", name)
		}
	}
}

func TestQuestionCutsTitleAndLocationToRuneCaps(t *testing.T) {
	digest, _ := DigestOf(testProfile())
	p := posting.Posting{
		Title:       strings.Repeat("世", draft.MaxTitleRunes+50),
		Location:    strings.Repeat("界", MaxLocationRunes+50),
		Description: "Build things.",
	}
	q, truncated := Question(p, "t", digest, nil)
	if !truncated {
		t.Error("cutting a field must set truncated")
	}
	if n := strings.Count(string(q.Context), "世"); n != draft.MaxTitleRunes {
		t.Errorf("title kept %d runes, want %d", n, draft.MaxTitleRunes)
	}
	if n := strings.Count(string(q.Context), "界"); n != MaxLocationRunes {
		t.Errorf("location kept %d runes, want %d", n, MaxLocationRunes)
	}
	// Within the caps nothing is cut and truncated stays false.
	_, truncated = Question(posting.Posting{Title: "Engineer", Location: "Remote", Description: "Build things."}, "t", digest, nil)
	if truncated {
		t.Error("a small posting was reported truncated")
	}
}
