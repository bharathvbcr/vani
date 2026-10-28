package draft

import (
	"errors"
	"strings"
	"testing"
)

// The refusal carries what was unbacked as a structured kind, so a surface
// (the extension, the Mac app) can say it without reading the message's prose.
func TestUngroundedRefusalCarriesItsKind(t *testing.T) {
	for _, tc := range []struct{ reply, kind string }{
		{"I cut latency 73%.", KindFigure},
		{"I served 10,000 users.", KindFigure},
		{"I cut latency 42% and hold a PhD.", KindCredential}, // the 42% is backed
		{"I hold a PhD.", KindCredential},
		{"I am AWS Certified.", KindCredential},
		{"I used AWS.", KindAcronym},
		{"I have five years of experience.", KindYears},
		{"I have 5+ years of experience.", KindYears},
	} {
		_, err := draftReply(t, tc.reply, latencyRequest(), Config{})
		de := ungroundedErr(t, err)
		if de.Kind != tc.kind {
			t.Errorf("%q: kind %q, want %q (%v)", tc.reply, de.Kind, tc.kind, err)
		}
		if got := KindOf(err); got != tc.kind {
			t.Errorf("%q: KindOf = %q, want %q", tc.reply, got, tc.kind)
		}
	}
}

// A failure that is not a refusal for an unbacked claim has no kind, however
// it is wrapped.
func TestOnlyUngroundedRefusalsHaveAKind(t *testing.T) {
	if got := KindOf(nil); got != "" {
		t.Errorf("KindOf(nil) = %q", got)
	}
	if got := KindOf(errors.New("plain")); got != "" {
		t.Errorf("KindOf(plain) = %q", got)
	}
	if got := KindOf(fail(CodeEmpty, "no text")); got != "" {
		t.Errorf("KindOf(empty_reply) = %q", got)
	}
	_, err := draftReply(t, "I cut latency 73%.", latencyRequest(), Config{})
	if got := KindOf(errors.Join(errors.New("context"), err)); got != KindFigure {
		t.Errorf("a wrapped refusal lost its kind: %q", got)
	}
	// The message still names the kind in words, for a reader without the field.
	if !strings.Contains(err.Error(), "refusing a figure") {
		t.Errorf("message = %v", err)
	}
}
