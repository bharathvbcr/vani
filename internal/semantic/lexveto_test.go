package semantic

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const tokenVectorsPath = "../../extension/test/vectors/tokens.json"

func sortedTokens(s string) []string {
	out := []string{}
	for t := range labelTokens(s) {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// The Go tokenizer returns what extension/src/normalize.ts tokens() returns, on
// every case in the vectors the TypeScript test also replays.
func TestTokensMatchTheExtensionVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(tokenVectorsPath))
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []struct {
			Label  string   `json:"label"`
			Tokens []string `json:"tokens"`
		} `json:"cases"`
	}
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(&f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) < 200 {
		t.Fatalf("%d vectors, want at least 200", len(f.Cases))
	}
	bad := 0
	for _, c := range f.Cases {
		if got := sortedTokens(c.Label); !reflect.DeepEqual(got, c.Tokens) {
			bad++
			if bad <= 10 {
				t.Errorf("tokens(%q) = %q, extension says %q", c.Label, got, c.Tokens)
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d vectors differ", bad, len(f.Cases))
	}
}

func TestLexicalVeto(t *testing.T) {
	vetoed := [][2]string{
		{"First name", "Last name"},
		{"Do you currently require sponsorship?", "Will you require sponsorship in the future?"},
		{"Country of residence", "Country of citizenship"},
		{"Emergency contact name", "Emergency contact phone"}, // one-for-one
		{"Current employer", "Previous employer"},
		{"Current employer", "Former employer"},
		{"Home address", "Work address"},
		{"Primary email", "Secondary email"},
		{"Legal name", "Preferred name"},
		{"Years of experience with Python", "Years of experience with Java"},
		{"Last name", "Surname"}, // a contrast word the other lacks: a person decides
		{"Current city", "City"}, // likewise
		{"Link", "URL"},          // nothing left to read
		{"Phone number", ""},     // nothing left to read
	}
	for _, p := range vetoed {
		if why := LexicalVeto(p[0], p[1]); why == "" {
			t.Errorf("%q ~ %q: not vetoed", p[0], p[1])
		}
		if LexicalVeto(p[1], p[0]) == "" {
			t.Errorf("%q ~ %q: veto is not symmetric", p[1], p[0])
		}
	}
	kept := [][2]string{
		{"LinkedIn", "LinkedIn profile URL"}, // url, profile are stopwords
		{"Phone number", "Telephone number"}, // number is a stopword, but phone vs telephone are two tokens...
		{"LINKEDIN PROFILE", "LinkedIn profile:"},
		{"Are you legally authorized to work in the United States?", "Are you authorized to work in the US?"},
		{"Email address", "E-mail"},
		{"Cover letter", "Upload your cover letter"},
		{"Highest education level", "Highest level of education completed"},
		{"First name", "First name"},
		{"First name", "FIRST NAME *"},
		{"", ""},
	}
	for _, p := range kept {
		got := LexicalVeto(p[0], p[1])
		if p[0] == "Phone number" && p[1] == "Telephone number" {
			// {phone} vs {telephon}: one-for-one on a true pair. The veto is
			// conservative; this documents that it demotes it.
			if got != "one-word substitution" {
				t.Errorf("%q ~ %q: %q", p[0], p[1], got)
			}
			continue
		}
		if got != "" {
			t.Errorf("%q ~ %q: vetoed (%s), should stay mergeable", p[0], p[1], got)
		}
	}
}

func TestTierPair(t *testing.T) {
	if got := TierPair("First name", "Last name", 0.99); got != "possible" {
		t.Errorf("First/Last at 0.99: %q", got)
	}
	if got := TierPair("LinkedIn", "LinkedIn profile URL", 0.99); got != "likely" {
		t.Errorf("true pair at 0.99: %q", got)
	}
	if got := TierPair("First name", "Last name", PossibleCos+0.001); got != "possible" {
		t.Errorf("possible stays possible: %q", got)
	}
	if got := TierPair("First name", "Last name", PossibleCos-0.01); got != "" {
		t.Errorf("below the floor stays below it: %q", got)
	}
	if got := TierPair("LinkedIn", "LinkedIn profile URL", LikelyCos-0.001); got != "possible" {
		t.Errorf("just under likely: %q", got)
	}
}

// The veto against the fixture: which labelled pairs it demotes. It must demote
// every hard negative that differs by one content word, and the pairs it takes
// from the true ones are listed so the cost is visible.
func TestLexicalVetoOnTheFixture(t *testing.T) {
	pairs := loadLabelPairs(t)
	var negVetoed, negKept, posVetoed, posKept int
	var lostPos, keptNeg []string
	for _, p := range pairs {
		v := LexicalVeto(p.A, p.B) != ""
		switch {
		case !p.Same && v:
			negVetoed++
		case !p.Same:
			negKept++
			keptNeg = append(keptNeg, p.A+" ~ "+p.B)
		case v:
			posVetoed++
			lostPos = append(lostPos, p.A+" ~ "+p.B)
		default:
			posKept++
		}
	}
	t.Logf("different-question pairs: %d vetoed, %d not (%s)", negVetoed, negKept, strings.Join(keptNeg, "; "))
	t.Logf("same-question pairs: %d vetoed, %d kept mergeable (vetoed: %s)", posVetoed, posKept, strings.Join(lostPos, "; "))
	for _, k := range []string{"First name ~ Last name", "Do you currently require sponsorship? ~ Will you require sponsorship in the future?",
		"Country of residence ~ Country of citizenship", "Emergency contact name ~ Emergency contact phone", "Current employer ~ Previous employer"} {
		for _, n := range keptNeg {
			if n == k {
				t.Errorf("near-miss %q is not vetoed", k)
			}
		}
	}
	if negVetoed < 30 {
		t.Errorf("only %d of the different-question pairs are vetoed", negVetoed)
	}
}
