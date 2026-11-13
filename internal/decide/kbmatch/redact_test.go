package kbmatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
	"github.com/bharathvbcr/decide"
)

// identity redacts nothing and finds nothing: the existing tests' knowledge
// base holds no identifying term, so their digests are unchanged.
type identity struct{}

func (identity) Redact(s string) (string, int) { return s, 0 }
func (identity) Found(string) []string         { return nil }

var noTerms Redactor = identity{}

// listRedactor is a small whole-word redactor over a fixed list, the shape
// track.Words has, for this package's tests (track's own tests cover its
// matcher in depth).
type listRedactor struct {
	terms []string
	// leak, when set, makes Redact leave this term in place, as a redactor
	// that disagrees with its own check would.
	leak string
}

func (l listRedactor) Redact(s string) (string, int) {
	n := 0
	for _, t := range l.terms {
		if t == l.leak {
			continue
		}
		for {
			i := strings.Index(strings.ToLower(s), t)
			if i < 0 {
				break
			}
			s = s[:i] + "[redacted]" + s[i+len(t):]
			n++
		}
	}
	return s, n
}

func (l listRedactor) Found(s string) []string {
	var out []string
	for _, t := range l.terms {
		if strings.Contains(strings.ToLower(s), t) {
			out = append(out, t)
		}
	}
	return out
}

func secretProfile() semantic.Profile {
	return semantic.Profile{Chunks: []semantic.Chunk{
		{Label: "skills", Text: "Go, Rust, Zephyrkit", Weight: 1},
		{Label: "Zephyrkit inference runtime", Text: "Zephyrkit inference runtime", Weight: 1},
		{Label: "Zephyrkit inference runtime", Text: "Cut decode latency in Zephyrkit with fused kernels", Weight: 1},
		{Label: "Quillmark notebook", Text: "Quillmark notebook", Weight: 1},
		{Label: "Quillmark notebook", Text: "Built the Quillmark sync engine", Weight: 1},
	}}
}

var secrets = listRedactor{terms: []string{"zephyrkit", "quillmark"}}

func TestDigestOfRedactsEveryLineAndHashesWhatIsSent(t *testing.T) {
	d, err := DigestOf(secretProfile(), secrets)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{d.Text, d.Compact} {
		if found := secrets.Found(s); len(found) > 0 {
			t.Fatalf("a term survived in the digest:\n%s", s)
		}
	}
	if d.Redactions == 0 || !strings.Contains(d.Text, "[redacted]") {
		t.Fatalf("nothing was redacted: %d\n%s", d.Redactions, d.Text)
	}
	// Two entries whose titles redact alike stay two entries.
	if c := strings.Count(d.Text, "- [redacted]"); c != 2 {
		t.Fatalf("entries merged or lost: %d titles\n%s", c, d.Text)
	}
	plain, _ := DigestOf(secretProfile(), noTerms)
	if plain.SHA256 == d.SHA256 {
		t.Fatal("the hash did not move with the redacted text")
	}
}

func TestDigestOfRefusesWithoutARedactor(t *testing.T) {
	if _, err := DigestOf(testProfile(), nil); !errors.Is(err, ErrNoRedactor) {
		t.Fatalf("err %v, want ErrNoRedactor", err)
	}
}

func TestPrepareRefusesADigestNotBuiltThroughTheRedactor(t *testing.T) {
	lit := Digest{Text: "Zephyrkit\n", SHA256: "x", Compact: "Zephyrkit\n"}
	if _, _, err := Prepare(posting.Posting{Title: "t"}, "summary", lit, nil); !errors.Is(err, ErrNoRedactor) {
		t.Fatalf("err %v, want ErrNoRedactor", err)
	}
	if err := CheckOwner("summary", lit); !errors.Is(err, ErrNoRedactor) {
		t.Fatalf("CheckOwner err %v", err)
	}
}

func TestPrepareRedactsFocusLinesAndChecksTheSentBytes(t *testing.T) {
	d, _ := DigestOf(secretProfile(), secrets)
	focus := []Focus{{Label: "Quillmark notebook", Text: "Shipped Quillmark offline mode"}}
	q, _, err := Prepare(posting.Posting{Title: "Engineer", Description: "Work on our own Zephyrkit-like stack"}, "ml systems roles", d, focus)
	if err != nil {
		t.Fatal(err)
	}
	ctx := string(q.Context)
	owner, posted, _ := strings.Cut(ctx, "\nJOB POSTING:\n")
	if found := secrets.Found(owner); len(found) > 0 {
		t.Fatalf("a term reached the owner's sections:\n%s", owner)
	}
	// The posting is the employer's text: it is neither redacted nor checked.
	if !strings.Contains(posted, "Zephyrkit-like") {
		t.Fatalf("the posting was rewritten:\n%s", posted)
	}
}

func TestPrepareRefusesATermThatSurvived(t *testing.T) {
	leaky := listRedactor{terms: []string{"zephyrkit", "quillmark"}, leak: "quillmark"}
	d, _ := DigestOf(secretProfile(), leaky)
	_, _, err := Prepare(posting.Posting{Title: "t"}, "ml systems roles", d, nil)
	var ie *IdentifyingError
	if !errors.As(err, &ie) || ie.Digest == 0 || ie.Summary != 0 {
		t.Fatalf("err %v (%+v), want an IdentifyingError on the digest", err, ie)
	}
	if strings.Contains(err.Error(), "quillmark") {
		t.Fatalf("the refusal names the term: %v", err)
	}
	// A term in the target summary is checked, not redacted.
	clean, _ := DigestOf(secretProfile(), secrets)
	if err := CheckOwner("roles like zephyrkit", clean); !errors.As(err, &ie) || ie.Summary != 1 {
		t.Fatalf("summary term: err %v", err)
	}
}

// sink records what would be sent.
type sink struct{ sent []string }

func (s *sink) Decide(_ context.Context, q decide.Question) (decide.Answer, error) {
	s.sent = append(s.sent, string(q.Context))
	return decide.Answer{Slots: map[string]decide.SlotReading{Slot: {Reading: decide.Answered, Value: "core"}}}, nil
}

func TestJudgeSendsNothingItCannotClear(t *testing.T) {
	leaky := listRedactor{terms: []string{"zephyrkit"}, leak: "zephyrkit"}
	d, _ := DigestOf(secretProfile(), leaky)
	s := &sink{}
	_, err := Judge{D: s, Target: "ml systems roles", Digest: d}.Ask(context.Background(), posting.Posting{Title: "t", Description: "x"})
	var ie *IdentifyingError
	if !errors.As(err, &ie) || len(s.sent) != 0 {
		t.Fatalf("err %v, sent %d", err, len(s.sent))
	}
	clean, _ := DigestOf(secretProfile(), secrets)
	if _, err := (Judge{D: s, Target: "ml systems roles", Digest: clean}).Ask(context.Background(), posting.Posting{Title: "t", Description: "x"}); err != nil || len(s.sent) != 1 {
		t.Fatalf("clean digest: err %v, sent %d", err, len(s.sent))
	}
	if found := secrets.Found(s.sent[0]); len(found) > 0 {
		t.Fatalf("a term was sent:\n%s", s.sent[0])
	}
}
