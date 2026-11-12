// Package kbmatch defines the career.kb_match question: a language model's read
// of whether the work a job posting describes is the work in the owner's
// knowledge base. The answer is one of four ordered options
// (jobinsight.KBMatchOrder) and only orders and explains (Rule 7): it is the
// ranker's seventh additive part, never a gate, and a posting whose question
// could not be answered is simply not judged.
//
// Why a model on top of the embedding: the embedding sees which words a posting
// sits near, so a company's domain ("post-training") lifts every role it lists
// and a team-practice line lifts any job. A model can say the main duties are
// marketing and the posting is unrelated whatever the company does.
//
// What leaves the machine, per posting: the posting itself, the owner's
// target-role summary (already part of every triage question) and a digest of
// the knowledge base built from semantic.Profile, which holds only role titles,
// stack terms and bullet text. semantic.ProfileFromBank never reads an entry's
// organisation, dates, location or contact fields, and its cleanBulletText
// removes markdown, claim tokens, URLs, e-mail addresses, phone-shaped runs and
// work-authorization phrases from bullets; it does not scrub names, so an
// organisation named inside a bullet's own words stays (organisation names are
// not personal data here, and the drafter sends them on purpose). The digest
// is bounded and its hash is part
// of each stored answer, so an edit to the knowledge base re-asks. Nothing in the
// question names the owner. On top of that, every knowledge-base line (the
// digest, the compact digest and the embedding-picked focus lines) passes
// through a Redactor (the owner's identifying-words list plus the built-in one,
// track.Words) before it is put in a question, and Prepare re-checks the
// assembled owner text and refuses to send when a term survived. Both sections and the posting sit inside the
// backend's untrusted-content frame: none carries instructions to the model, and
// the reply is parsed strictly to one listed option.
package kbmatch

import (
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/draft"
	"github.com/bharathvbcr/Vani/internal/jobinsight"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/semantic"
	"github.com/bharathvbcr/decide"
)

const (
	Task = "career.kb_match"
	Slot = "kb_match"

	// MaxDigestBytes bounds the knowledge-base digest in a question.
	MaxDigestBytes = 6 << 10
	// MaxBulletsPerEntry and maxBulletBytes bound how much of one entry the
	// digest keeps.
	MaxBulletsPerEntry = 3
	maxBulletBytes     = 240
	// MaxLocationRunes bounds the posting location in a question.
	MaxLocationRunes = 120
)

// Prompt is the question, outside the untrusted frame. The options are the
// ranker's (jobinsight.KBMatchOrder).
const Prompt = `Below, in this order, are the job seeker's target roles, a digest of the job seeker's experience, and a job posting.
Decide how close the work this job does day to day is to the work in the job seeker's experience and target roles. Judge the duties the posting describes, not the company's industry, not its boilerplate (benefits, degree and years requirements, equal-opportunity text), and not words the posting merely shares with the experience.
Pick one option for the slot:
- core: the main duties are work the job seeker has done or is aiming for.
- partial: some of the main duties are, others are not.
- adjacent: the same field, tools or industry but different work (an AI company's sales, marketing, operations or program role is adjacent or unrelated, never core).
- unrelated: different work.
Answer "none" if the posting is too thin to tell.`

// ErrNotRead means the question went unanswered: the backend failed, was
// unavailable, was refused, or abstained. The posting is then not judged.
var ErrNotRead = errors.New("kbmatch: the model gave no usable answer")

// notRead is ErrNotRead with the reading that explains it.
type notRead struct {
	reading decide.Reading
	why     string
}

func (e *notRead) Error() string        { return "kbmatch: the model gave no usable answer: " + e.why }
func (e *notRead) Is(target error) bool { return target == ErrNotRead }

// ReadingOf returns the reading behind a not-read error (Unavailable when the
// backend could not be reached, Abstained when the model said it could not
// tell, and so on); ok is false for any other error.
func ReadingOf(err error) (r decide.Reading, ok bool) {
	var n *notRead
	if errors.As(err, &n) {
		return n.reading, true
	}
	return "", false
}

// MaxCompactBytes bounds the compact digest (stack terms and entry titles only),
// which is what a question carries when an embedding model has picked the
// closest lines (Focus) to go beside it.
const MaxCompactBytes = 2 << 10

// MaxFocus is the most lines the embedding model may add to a question.
const MaxFocus = 8

// Digest is the knowledge base as the model sees it. Only DigestOf makes a
// usable one: it redacts the owner's identifying terms (Redactor) out of every
// line, and keeps the redactor so Prepare redacts the focus lines too and
// Prepare can check the assembled bytes. A Digest built any other way (a
// literal) has no redactor, and Prepare refuses it.
type Digest struct {
	// Text is the full digest, redacted. SHA256 is its hash and the one identity
	// of the knowledge base: an answer is current while it matches, whichever of
	// Text or Compact + Focus the question carried. It hashes what is sent.
	Text   string
	SHA256 string
	// Compact is the stack terms and entry titles alone, redacted.
	Compact string
	// Redactions counts the identifying terms DigestOf replaced, once per
	// knowledge-base line it read (a line in both Text and Compact counts once).
	Redactions int

	r Redactor
}

// Redactor removes the owner's identifying terms from knowledge-base text
// before it leaves the machine (track.Words). Found is the check Prepare runs
// over the assembled owner text: it must find nothing Redact left.
type Redactor interface {
	Redact(s string) (string, int)
	Found(s string) []string
}

// ErrNoRedactor is Prepare's refusal of a Digest DigestOf did not make.
var ErrNoRedactor = errors.New("kbmatch: the knowledge-base digest was not built through the redactor; nothing is sent without it")

// IdentifyingError is Prepare's refusal: identifying terms survived in the
// owner's text a question would carry. It counts them by section and never
// names them, so the error can be printed and logged.
type IdentifyingError struct {
	Summary, Digest, Focus int
}

func (e *IdentifyingError) Error() string {
	return fmt.Sprintf("kbmatch: %d identifying term(s) would be sent (target summary %d, knowledge-base digest %d, closest lines %d); nothing was asked",
		e.Summary+e.Digest+e.Focus, e.Summary, e.Digest, e.Focus)
}

// Focus is one line of the knowledge base an embedding model found closest to a
// posting: the entry it belongs to and the line itself.
type Focus struct {
	Label string
	Text  string
}

// DigestOf cuts p into a bounded, deterministic digest: stack terms first, then
// each entry's title with up to MaxBulletsPerEntry of its bullets, whole lines
// only, up to MaxDigestBytes. Every line passes through r first, so no
// identifying term reaches Text or Compact and SHA256 hashes the redacted
// text; entries are still grouped by their own titles, so two entries whose
// titles redact alike stay apart. A nil r is refused, as is a profile with
// nothing to say.
func DigestOf(p semantic.Profile, r Redactor) (Digest, error) { return digestOf(p, r, true) }

// passRedactor removes nothing and finds nothing.
type passRedactor struct{}

func (passRedactor) Redact(s string) (string, int) { return s, 0 }
func (passRedactor) Found(string) []string         { return nil }

// RawDigestText is the text DigestOf would make with nothing redacted, no
// bullet cut to maxBulletBytes and no byte cap: every line the digest could keep, whole. A
// caller that checks the knowledge base for identifying terms before they are
// stripped (the auditor, which refuses instead) reads this, because a term can
// cross the cut. It is for checking only: never hashed, never sent.
func RawDigestText(p semantic.Profile) (string, error) {
	d, err := digestOf(p, passRedactor{}, false)
	return d.Text, err
}

func digestOf(p semantic.Profile, r Redactor, cut bool) (Digest, error) {
	if r == nil {
		return Digest{}, ErrNoRedactor
	}
	n := 0
	red := func(s string) string {
		out, k := r.Redact(s)
		n += k
		return out
	}
	var skills, order []string
	bullets := map[string][]string{}
	for _, c := range p.Chunks {
		text := strings.Join(strings.Fields(c.Text), " ")
		label := strings.Join(strings.Fields(c.Label), " ")
		switch {
		case text == "" || label == "":
		case c.Label == "skills":
			skills = append(skills, text)
		default:
			if _, ok := bullets[label]; !ok {
				order = append(order, label)
				bullets[label] = nil
			}
			if text != label && len(bullets[label]) < MaxBulletsPerEntry {
				bullets[label] = append(bullets[label], text)
			}
		}
	}
	var sb, compact strings.Builder
	add := func(b *strings.Builder, max int, line string) bool {
		if cut && b.Len()+len(line)+1 > max { // the raw text (cut false) has no caps at all
			return false
		}
		b.WriteString(line)
		b.WriteByte('\n')
		return true
	}
	for _, s := range skills {
		s = red(s)
		if !add(&sb, MaxDigestBytes, s) {
			break
		}
		add(&compact, MaxCompactBytes, s)
	}
	for _, label := range order {
		shown := red(label)
		if !add(&sb, MaxDigestBytes, "- "+shown) {
			break
		}
		add(&compact, MaxCompactBytes, "- "+shown)
		for _, b := range bullets[label] {
			// Redact before the cut, so the cut can never split a term into a
			// fragment the redactor no longer recognises.
			if !add(&sb, MaxDigestBytes, "    * "+cutIf(cut, red(b))) {
				break
			}
		}
	}
	if sb.Len() == 0 {
		return Digest{}, errors.New("kbmatch: the knowledge base has no titles, skills or bullets to describe")
	}
	// One more pass over each assembled text: a term can run across two lines
	// ("Quillbridge" ending one skill line, "Labs" starting the next), which no
	// per-line pass sees but the check over the assembled text does.
	text, k := r.Redact(sb.String())
	n += k
	if cut {
		text = fitLines(text, MaxDigestBytes)
	}
	compactText, _ := r.Redact(compact.String())
	compactText = fitLines(compactText, MaxCompactBytes) // (raw: Compact is unused)
	if text == "" {
		return Digest{}, errors.New("kbmatch: the knowledge base has no titles, skills or bullets to describe")
	}
	sum := sha256.Sum256([]byte(text))
	return Digest{Text: text, SHA256: hex.EncodeToString(sum[:]), Compact: compactText, Redactions: n, r: r}, nil
}

// fitLines drops whole trailing lines of s until it is at most max bytes.
func fitLines(s string, max int) string {
	for len(s) > max {
		i := strings.LastIndexByte(strings.TrimSuffix(s, "\n"), '\n')
		if i < 0 {
			return ""
		}
		s = s[:i+1]
	}
	return s
}

// Frame is the question for an empty posting and an empty digest: the frame
// every question shares (the prompt, the section headings, the options and the
// target summary), for hashing it. It carries no knowledge-base text. It
// refuses a Digest DigestOf did not make and a summary the redactor finds a
// term in, like Prepare.
func (d Digest) Frame(summary string) (decide.Question, error) {
	if d.r == nil {
		return decide.Question{}, ErrNoRedactor
	}
	empty := Digest{r: d.r}
	if err := checkOwner(summary, empty, nil); err != nil {
		return decide.Question{}, err
	}
	q, _ := question(posting.Posting{}, summary, empty, nil)
	return q, nil
}

// cutIf is a bullet cut to maxBulletBytes when cut, and the whole bullet otherwise.
func cutIf(cut bool, s string) string {
	if !cut {
		return s
	}
	return cutRunes(s, maxBulletBytes)
}

// cutRuneCount cuts s to at most n runes and reports whether it cut.
func cutRuneCount(s string, n int) (string, bool) {
	if len(s) <= n || utf8.RuneCountInString(s) <= n {
		return s, false
	}
	i := 0
	for range n {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return s[:i], true
}

// cutRunes cuts s to at most n bytes on a rune boundary (a boundary is
// found even when s is not valid UTF-8, by backing up at most to byte 0).
func cutRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// TextSHA256 identifies what a stored answer was given: the posting's text, the
// target summary and the digest. A change to any re-asks.
func TextSHA256(p posting.Posting, summary string, d Digest) string {
	sum, err := TextSHA256Of(TextPrefix(p), summary, d)
	if err != nil {
		panic("kbmatch: " + err.Error()) // TextPrefix's own state always resumes
	}
	return sum
}

// TextPrefix is TextSHA256's hash state after the posting (the task, title,
// location and description), saved: TextSHA256Of finishes it with a summary
// and digest, so a caller that kept it needs no description to find a stored
// answer. The saved form is the standard library's SHA-256 state, which only
// TextSHA256Of reads.
func TextPrefix(p posting.Posting) []byte {
	h := sha256.New()
	for _, s := range []string{Task, p.Title, p.Location, p.Description} {
		writeField(h, s)
	}
	state, err := h.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		panic("kbmatch: save a SHA-256 state: " + err.Error()) // never fails for crypto/sha256
	}
	return state
}

// TextSHA256Of is TextSHA256 from the posting's TextPrefix. A prefix that is not
// a saved SHA-256 state is an error.
func TextSHA256Of(prefix []byte, summary string, d Digest) (string, error) {
	h := sha256.New()
	if err := h.(encoding.BinaryUnmarshaler).UnmarshalBinary(prefix); err != nil {
		return "", fmt.Errorf("text prefix: %w", err)
	}
	writeField(h, summary)
	writeField(h, d.SHA256)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeField writes one length-prefixed field of TextSHA256's input.
func writeField(w io.Writer, s string) { fmt.Fprintf(w, "%d:%s\x00", len(s), s) }

// question renders the question for p. It is not exported: a question that is
// sent comes from Prepare, which refuses a digest the redactor did not make and
// checks the bytes, so no sender can skip the guard. The context stays within
// jdtriage.MaxContextBytes: the title and location are cut to draft.MaxTitleRunes
// and MaxLocationRunes, the posting body to what the target summary and
// knowledge base leave, the whole to the cap, and truncated says whether
// anything was cut.
//
// With no focus the question carries the full digest. With focus (at most
// MaxFocus lines an embedding model found closest to this posting) it carries the
// compact digest and those lines instead: shorter, and the lines are the ones
// that bear on this posting rather than the first few of each entry.
//
// The digest's redactor (DigestOf) also redacts the focus lines, before they are
// cut. question itself checks nothing: Prepare is what a sender calls.
func question(p posting.Posting, summary string, d Digest, focus []Focus) (q decide.Question, truncated bool) {
	var head strings.Builder
	owner, _ := ownerHead(summary, d, focus)
	head.WriteString(owner)
	head.WriteString("\nJOB POSTING:\nTitle: ")
	title, cutTitle := cutRuneCount(strings.Join(strings.Fields(p.Title), " "), draft.MaxTitleRunes)
	location, cutLocation := cutRuneCount(strings.Join(strings.Fields(p.Location), " "), MaxLocationRunes)
	truncated = cutTitle || cutLocation
	head.WriteString(title)
	head.WriteString("\nLocation: ")
	head.WriteString(location)
	head.WriteString("\n\n")
	body := p.Description
	room := max(jdtriage.MaxContextBytes-head.Len(), 0)
	if len(body) > room {
		body, truncated = cutRunes(body, room), true
	}
	rendered := head.String() + body
	if len(rendered) > jdtriage.MaxContextBytes { // the target or digest alone overran
		rendered, truncated = cutRunes(rendered, jdtriage.MaxContextBytes), true
	}
	return decide.Question{
		Task:     Task,
		Prompt:   Prompt,
		Context:  []byte(rendered),
		Slots:    []decide.Slot{{Name: Slot, Options: append([]string(nil), jobinsight.KBMatchOrder[:]...)}},
		Facts:    map[string]string{jdtriage.FactContextTruncated: fmt.Sprint(truncated)},
		RecordID: string(p.Key),
	}, truncated
}

// ownerSections is where each part of ownerHead's text sits, so Prepare can
// count surviving terms by section.
type ownerSections struct {
	summary, digest, focus string
}

// ownerHead renders the owner's part of a question (target summary, then the
// knowledge base: the full digest, or the compact digest and the focus lines),
// exactly the bytes Question puts before the posting.
func ownerHead(summary string, d Digest, focus []Focus) (string, ownerSections) {
	var head strings.Builder
	var sec ownerSections
	head.WriteString("JOB SEEKER'S TARGET ROLES:\n")
	head.WriteString(summary)
	sec.summary = summary
	if len(focus) == 0 || d.Compact == "" {
		head.WriteString("\n\nJOB SEEKER'S EXPERIENCE:\n")
		head.WriteString(d.Text)
		sec.digest = d.Text
	} else {
		head.WriteString("\n\nJOB SEEKER'S EXPERIENCE (stack terms and entry titles):\n")
		head.WriteString(d.Compact)
		sec.digest = d.Compact
		head.WriteString("\nTHE JOB SEEKER'S EXPERIENCE CLOSEST TO THIS POSTING (picked by an embedding model):\n")
		var fb strings.Builder
		for i, f := range focus {
			if i == MaxFocus {
				break
			}
			label := strings.Join(strings.Fields(f.Label), " ")
			text := strings.Join(strings.Fields(f.Text), " ")
			if d.r != nil {
				label, _ = d.r.Redact(label)
				text, _ = d.r.Redact(text)
			}
			fb.WriteString("- ")
			fb.WriteString(cutRunes(label, maxBulletBytes))
			fb.WriteString(": ")
			fb.WriteString(cutRunes(text, maxBulletBytes))
			fb.WriteByte('\n')
		}
		block := fb.String()
		if d.r != nil {
			// A term can run across two lines, as in DigestOf.
			block, _ = d.r.Redact(block)
		}
		head.WriteString(block)
		sec.focus = block
	}
	return head.String(), sec
}

// Prepare is Question for a question that will be sent: it refuses a Digest
// DigestOf did not make (ErrNoRedactor), and runs the redactor's check over
// the owner's text exactly as the question carries it (target summary, digest
// or compact digest, focus lines), refusing with an IdentifyingError when any
// term survived. The target summary is checked, not redacted: it also goes into
// every triage question, so a term in it is the track's to fix (vani track
// check), and silently rewriting it here would make the two disagree. The
// posting is not checked or redacted: it is the employer's text, not the
// owner's, and words such as "visa" or "sponsorship" belong there.
func Prepare(p posting.Posting, summary string, d Digest, focus []Focus) (decide.Question, bool, error) {
	if d.r == nil {
		return decide.Question{}, false, ErrNoRedactor
	}
	if err := checkOwner(summary, d, focus); err != nil {
		return decide.Question{}, false, err
	}
	q, truncated := question(p, summary, d, focus)
	return q, truncated, nil
}

// CheckOwner is Prepare's check alone, for the full-digest question every
// posting shares: a sender runs it once before asking anything, so a term in
// the summary or digest stops the run before the first call rather than
// failing every posting in turn.
func CheckOwner(summary string, d Digest) error {
	if d.r == nil {
		return ErrNoRedactor
	}
	return checkOwner(summary, d, nil)
}

func checkOwner(summary string, d Digest, focus []Focus) error {
	_, sec := ownerHead(summary, d, focus)
	e := &IdentifyingError{
		Summary: len(d.r.Found(sec.summary)),
		Digest:  len(d.r.Found(sec.digest)),
		Focus:   len(d.r.Found(sec.focus)),
	}
	if e.Summary+e.Digest+e.Focus > 0 {
		return e
	}
	return nil
}

// Judge asks D the question.
type Judge struct {
	D decide.Decider
	// Target is the owner's non-identifying target summary (jdtriage's rules).
	Target string
	Digest Digest
	// Focus, when set, picks the knowledge-base lines closest to a posting (an
	// embedding model, internal/semantic Matcher.Nearest). An error or no lines
	// fall back to the full digest: retrieval makes a question shorter, it is
	// never needed to ask one.
	Focus func(ctx context.Context, p posting.Posting) ([]Focus, error)
}

// Verdict is a usable answer: one of jobinsight.KBMatchOrder.
type Verdict struct {
	Value string
	// P is the backend's calibrated probability when it gave one.
	P    float64
	HasP bool
	// Focused is true when the question carried embedding-picked lines.
	Focused bool
}

// Ask reads p. It returns ErrNotRead (wrapped, with the reading) when the model
// gave no listed option, ErrNoRedactor or an IdentifyingError (from Prepare)
// when it would not send the question, and any decider error as it is.
func (j Judge) Ask(ctx context.Context, p posting.Posting) (Verdict, error) {
	if j.D == nil {
		return Verdict{}, errors.New("kbmatch: no Decider")
	}
	if j.Digest.Text == "" {
		return Verdict{}, errors.New("kbmatch: no knowledge-base digest")
	}
	summary, err := jdtriage.NormalizeSummary(j.Target)
	if err != nil {
		return Verdict{}, err
	}
	var focus []Focus
	if j.Focus != nil {
		if f, ferr := j.Focus(ctx, p); ferr == nil {
			focus = f
		} else if ctx.Err() != nil {
			return Verdict{}, ctx.Err()
		}
	}
	q, _, err := Prepare(p, summary, j.Digest, focus)
	if err != nil {
		// Never sent: no redactor, or a term survived. Not ErrNotRead: the caller
		// stops rather than counting it as a backend that gave no answer.
		return Verdict{}, err
	}
	ans, err := j.D.Decide(ctx, q)
	if err != nil {
		return Verdict{}, fmt.Errorf("kbmatch: %w", err)
	}
	rd, ok := ans.Slots[Slot]
	if !ok {
		return Verdict{}, &notRead{decide.Failed, "no reading for the slot"}
	}
	if rd.Reading != decide.Answered {
		return Verdict{}, &notRead{rd.Reading, string(rd.Reading)}
	}
	for _, o := range jobinsight.KBMatchOrder {
		if o == rd.Value {
			v := Verdict{Value: o, Focused: len(focus) > 0 && j.Digest.Compact != ""}
			if rd.Jev != nil {
				v.P, v.HasP = rd.Jev.Probability, true
			}
			return v, nil
		}
	}
	return Verdict{}, &notRead{decide.Failed, "the answer is not one of the options"}
}
