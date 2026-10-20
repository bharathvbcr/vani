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
// stack terms and bullet text with organisation names, dates, locations, URLs and
// e-mail addresses already removed. The digest is bounded and its hash is part
// of each stored answer, so an edit to the knowledge base re-asks. Nothing in the
// question names the owner. Both sections and the posting sit inside the
// backend's untrusted-content frame: none carries instructions to the model, and
// the reply is parsed strictly to one listed option.
package kbmatch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
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

// Digest is the knowledge base as the model sees it.
type Digest struct {
	Text   string
	SHA256 string
}

// DigestOf cuts p into a bounded, deterministic digest: stack terms first, then
// each entry's title with up to MaxBulletsPerEntry of its bullets, whole lines
// only, up to MaxDigestBytes. A profile with nothing to say is an error.
func DigestOf(p semantic.Profile) (Digest, error) {
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
				bullets[label] = append(bullets[label], cutRunes(text, maxBulletBytes))
			}
		}
	}
	var sb strings.Builder
	add := func(line string) bool {
		if sb.Len()+len(line)+1 > MaxDigestBytes {
			return false
		}
		sb.WriteString(line)
		sb.WriteByte('\n')
		return true
	}
	for _, s := range skills {
		if !add(s) {
			break
		}
	}
	for _, label := range order {
		if !add("- " + label) {
			break
		}
		for _, b := range bullets[label] {
			if !add("    * " + b) {
				break
			}
		}
	}
	if sb.Len() == 0 {
		return Digest{}, errors.New("kbmatch: the knowledge base has no titles, skills or bullets to describe")
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return Digest{Text: sb.String(), SHA256: hex.EncodeToString(sum[:])}, nil
}

// cutRunes cuts s to at most n bytes on a rune boundary.
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
	h := sha256.New()
	for _, s := range []string{Task, p.Title, p.Location, p.Description, summary, d.SHA256} {
		fmt.Fprintf(h, "%d:%s\x00", len(s), s)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Question renders the question for p. The context stays within
// jdtriage.MaxContextBytes: the posting body is cut to what the target summary
// and digest leave, and truncated says whether it was.
func Question(p posting.Posting, summary string, d Digest) (q decide.Question, truncated bool) {
	var head strings.Builder
	head.WriteString("JOB SEEKER'S TARGET ROLES:\n")
	head.WriteString(summary)
	head.WriteString("\n\nJOB SEEKER'S EXPERIENCE:\n")
	head.WriteString(d.Text)
	head.WriteString("\nJOB POSTING:\nTitle: ")
	head.WriteString(strings.Join(strings.Fields(p.Title), " "))
	head.WriteString("\nLocation: ")
	head.WriteString(strings.Join(strings.Fields(p.Location), " "))
	head.WriteString("\n\n")
	body := p.Description
	room := max(jdtriage.MaxContextBytes-head.Len(), 0)
	if len(body) > room {
		body, truncated = cutRunes(body, room), true
	}
	return decide.Question{
		Task:     Task,
		Prompt:   Prompt,
		Context:  []byte(head.String() + body),
		Slots:    []decide.Slot{{Name: Slot, Options: append([]string(nil), jobinsight.KBMatchOrder[:]...)}},
		Facts:    map[string]string{jdtriage.FactContextTruncated: fmt.Sprint(truncated)},
		RecordID: string(p.Key),
	}, truncated
}

// Judge asks D the question.
type Judge struct {
	D decide.Decider
	// Target is the owner's non-identifying target summary (jdtriage's rules).
	Target string
	Digest Digest
}

// Verdict is a usable answer: one of jobinsight.KBMatchOrder.
type Verdict struct {
	Value string
	// P is the backend's calibrated probability when it gave one.
	P    float64
	HasP bool
}

// Ask reads p. It returns ErrNotRead (wrapped, with the reading) when the model
// gave no listed option, and any decider error as it is.
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
	q, _ := Question(p, summary, j.Digest)
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
			v := Verdict{Value: o}
			if rd.Jev != nil {
				v.P, v.HasP = rd.Jev.Probability, true
			}
			return v, nil
		}
	}
	return Verdict{}, &notRead{decide.Failed, "the answer is not one of the options"}
}
