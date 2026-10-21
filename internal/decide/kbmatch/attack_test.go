package kbmatch

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/Vani/internal/posting"
)

func TestAttackTitleLocationAtCapAndOver(t *testing.T) {
	digest, _ := DigestOf(testProfile())
	for _, c := range []struct {
		name       string
		title, loc int
		want       bool
	}{
		{"both at cap", draft.MaxTitleRunes, MaxLocationRunes, false},
		{"title cap+1", draft.MaxTitleRunes + 1, 1, true},
		{"location cap+1", 1, MaxLocationRunes + 1, true},
		{"cap-1", draft.MaxTitleRunes - 1, MaxLocationRunes - 1, false},
	} {
		p := posting.Posting{Title: strings.Repeat("世", c.title), Location: strings.Repeat("界", c.loc), Description: "Build things."}
		q, truncated := Question(p, "t", digest, nil)
		if truncated != c.want || q.Facts[jdtriage.FactContextTruncated] != map[bool]string{true: "true", false: "false"}[c.want] {
			t.Errorf("%s: truncated=%v fact=%q, want %v", c.name, truncated, q.Facts[jdtriage.FactContextTruncated], c.want)
		}
		if got := strings.Count(string(q.Context), "世"); got != min(c.title, draft.MaxTitleRunes) {
			t.Errorf("%s: title runes %d", c.name, got)
		}
		if got := strings.Count(string(q.Context), "界"); got != min(c.loc, MaxLocationRunes) {
			t.Errorf("%s: location runes %d", c.name, got)
		}
	}
}

// The stated contract is a rune boundary; a combining mark at the cut is
// separated from its base. Documented here, not asserted as a break.
func TestAttackCombiningMarkAtCut(t *testing.T) {
	digest, _ := DigestOf(testProfile())
	title := strings.Repeat("a", draft.MaxTitleRunes-1) + "é" // base is rune 300, mark is rune 301
	q, truncated := Question(posting.Posting{Title: title, Description: "x"}, "t", digest, nil)
	if !truncated || !utf8.Valid(q.Context) {
		t.Fatalf("truncated=%v valid=%v", truncated, utf8.Valid(q.Context))
	}
	if strings.Contains(string(q.Context), "́") {
		t.Error("mark survived the cut")
	}
}

func TestAttackFourByteRunesAtByteCap(t *testing.T) {
	digest, _ := DigestOf(testProfile())
	for pad := range 5 {
		body := strings.Repeat("a", pad) + strings.Repeat("\U0001F600", jdtriage.MaxContextBytes)
		q, truncated := Question(posting.Posting{Title: "E", Description: body}, "t", digest, nil)
		if len(q.Context) > jdtriage.MaxContextBytes || !utf8.Valid(q.Context) || !truncated {
			t.Errorf("pad %d: len %d valid %v truncated %v", pad, len(q.Context), utf8.Valid(q.Context), truncated)
		}
	}
	// A body that exactly fills the room is not "truncated".
	_, base := Question(posting.Posting{Title: "E", Description: ""}, "t", digest, nil)
	q0, _ := Question(posting.Posting{Title: "E", Description: ""}, "t", digest, nil)
	room := jdtriage.MaxContextBytes - len(q0.Context)
	q, truncated := Question(posting.Posting{Title: "E", Description: strings.Repeat("a", room)}, "t", digest, nil)
	if base || truncated || len(q.Context) != jdtriage.MaxContextBytes {
		t.Errorf("exact fit: len %d truncated %v base %v", len(q.Context), truncated, base)
	}
	q, truncated = Question(posting.Posting{Title: "E", Description: strings.Repeat("a", room+1)}, "t", digest, nil)
	if !truncated || len(q.Context) != jdtriage.MaxContextBytes {
		t.Errorf("room+1: len %d truncated %v", len(q.Context), truncated)
	}
}

func TestAttackContextNeverExceedsCapProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(20261021))
	pool := []string{"a", "é", "世", "\U0001F600", "é", " ", "\n", "\xff"}
	gen := func(maxRunes int) string {
		var sb strings.Builder
		for range rng.Intn(maxRunes + 1) {
			sb.WriteString(pool[rng.Intn(len(pool))])
		}
		return sb.String()
	}
	digest, _ := DigestOf(testProfile())
	for i := range 300 {
		d := digest
		if rng.Intn(3) == 0 {
			d = Digest{Text: gen(40000), SHA256: "x", Compact: gen(40000)}
		}
		var focus []Focus
		for range rng.Intn(MaxFocus + 2) {
			focus = append(focus, Focus{Label: gen(400), Text: gen(400)})
		}
		p := posting.Posting{Title: gen(900), Location: gen(400), Description: gen(30000)}
		q, truncated := Question(p, gen(3000), d, focus)
		if len(q.Context) > jdtriage.MaxContextBytes {
			t.Fatalf("case %d: context %d bytes", i, len(q.Context))
		}
		if q.Facts[jdtriage.FactContextTruncated] != map[bool]string{true: "true", false: "false"}[truncated] {
			t.Fatalf("case %d: fact disagrees with flag", i)
		}
	}
}
