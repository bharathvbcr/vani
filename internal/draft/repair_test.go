package draft

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Manvi/manvi/llm/budget"
	"github.com/bharathvbcr/Vani/internal/decide/fieldtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
)

// reply is one scripted provider turn.
type reply struct {
	text  string
	stop  llm.StopReason
	err   error         // returned by Stream
	block time.Duration // Stream waits for this or the context
}

type seqStream struct {
	text string
	stop llm.StopReason
	done bool
}

func (s *seqStream) Next() (llm.Chunk, error) {
	if !s.done {
		s.done = true
		return llm.Chunk{Kind: llm.ChunkText, Text: s.text}, nil
	}
	return llm.Chunk{}, io.EOF
}
func (s *seqStream) Response() (llm.Response, error) {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock{Text: s.text}}}, StopReason: s.stop}, nil
}
func (s *seqStream) Close() error { return nil }

// seqProvider plays replies in order and repeats the last one; it records every
// request, so a test can count turns.
type seqProvider struct {
	replies []reply
	reqs    []llm.Request
}

func (p *seqProvider) Name() string { return "fake" }
func (p *seqProvider) Capability(model string) (llm.Capability, bool) {
	return llm.Capability{Provider: "fake", Model: model, MaxOutputTokens: 4096}, true
}
func (p *seqProvider) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	i := min(len(p.reqs), len(p.replies)-1)
	p.reqs = append(p.reqs, req)
	r := p.replies[i]
	if r.block > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(r.block):
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	return &seqStream{text: r.text, stop: r.stop}, nil
}

const (
	latencyQ = "Describe your experience with inference latency."
	// unbacked: the evidence says 40%, not 90%.
	unbacked = "I cut inference latency by 90% with speculative decoding on vLLM."
	backed   = "I cut inference latency by 40% with speculative decoding on vLLM."
)

func latencyDrafter(t *testing.T, p llm.Provider) *Drafter {
	t.Helper()
	d, err := New(p, Config{Model: "m", Timeout: 2 * time.Second}, kb())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func latencyReq() Request {
	return Request{Question: latencyQ, Job: Job{Company: "Modal", Title: "Inference Engineer", Description: "Own LLM inference latency for customers."}}
}

func codeOf(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestUngroundedDraftGetsOneRepairTurnNamingTheUnbackedToken(t *testing.T) {
	p := &seqProvider{replies: []reply{{text: unbacked}, {text: backed}}}
	res, err := latencyDrafter(t, p).Draft(context.Background(), latencyReq())
	if err != nil {
		t.Fatalf("a repairable draft was refused: %v", err)
	}
	if res.Draft != backed || res.Turns != 2 || !res.Repaired {
		t.Errorf("result = %+v", res)
	}
	if len(p.reqs) != 2 {
		t.Fatalf("%d provider turns, want 2", len(p.reqs))
	}
	m := p.reqs[1].Messages
	if len(m) != 3 || m[1].Role != llm.RoleAssistant || m[1].Text() != unbacked || m[2].Role != llm.RoleUser {
		t.Fatalf("repair messages = %+v", m)
	}
	if !strings.Contains(m[2].Text(), `"90%"`) {
		t.Errorf("the repair turn does not name the unbacked figure: %q", m[2].Text())
	}
	if m[0].Text() != p.reqs[0].Messages[0].Text() || p.reqs[1].System != p.reqs[0].System {
		t.Error("the repair turn must carry the same evidence and system prompt")
	}
}

func TestRepairIsOneTurnAndTheGuardStillHolds(t *testing.T) {
	// The repair repeats the fabrication: refused as before, after exactly two turns.
	p := &seqProvider{replies: []reply{{text: unbacked}}}
	res, err := latencyDrafter(t, p).Draft(context.Background(), latencyReq())
	if codeOf(err) != CodeUngrounded || KindOf(err) != KindFigure || res.Draft != "" {
		t.Fatalf("res %+v err %v, want an ungrounded refusal with no draft", res, err)
	}
	if len(p.reqs) != 2 || TurnsOf(err) != 2 {
		t.Errorf("%d provider turns (error says %d), want 2", len(p.reqs), TurnsOf(err))
	}
	// A repair that swaps in a different fabrication is refused too.
	p = &seqProvider{replies: []reply{{text: unbacked}, {text: "I cut inference latency by 77% on vLLM."}}}
	if _, err := latencyDrafter(t, p).Draft(context.Background(), latencyReq()); codeOf(err) != CodeUngrounded || len(p.reqs) != 2 {
		t.Errorf("a new fabrication got through: %v (%d turns)", err, len(p.reqs))
	}
}

func TestAGroundedDraftSpendsOneTurn(t *testing.T) {
	p := &seqProvider{replies: []reply{{text: backed}}}
	res, err := latencyDrafter(t, p).Draft(context.Background(), latencyReq())
	if err != nil || res.Turns != 1 || res.Repaired || len(p.reqs) != 1 {
		t.Fatalf("res %+v err %v turns %d", res, err, len(p.reqs))
	}
}

func TestFailedRepairLeavesTheFirstRefusalStanding(t *testing.T) {
	for name, second := range map[string]reply{
		"provider down": {err: errors.New("503")},
		"empty":         {text: " "},
		"truncated":     {text: "I cut latency by", stop: llm.StopMaxTokens},
		"refused":       {stop: llm.StopRefusal},
	} {
		p := &seqProvider{replies: []reply{{text: unbacked}, second}}
		_, err := latencyDrafter(t, p).Draft(context.Background(), latencyReq())
		if codeOf(err) != CodeUngrounded || !strings.Contains(err.Error(), "90%") {
			t.Errorf("%s: err = %v, want the first ungrounded refusal", name, err)
		}
		if len(p.reqs) != 2 {
			t.Errorf("%s: %d turns, want 2", name, len(p.reqs))
		}
	}
}

func TestRepairSpendsAgainstTheSameBudgetAndTimeout(t *testing.T) {
	p := &seqProvider{replies: []reply{{text: unbacked}, {err: fmt.Errorf("reserve: %w", budget.ErrExhausted)}}}
	if _, err := latencyDrafter(t, p).Draft(context.Background(), latencyReq()); codeOf(err) != CodeBudget {
		t.Errorf("a budget spent during the repair = %v, want %s", err, CodeBudget)
	}
	// One timeout covers both turns: the second blocks past it.
	p = &seqProvider{replies: []reply{{text: unbacked}, {block: time.Second}}}
	d, err := New(p, Config{Model: "m", Timeout: 40 * time.Millisecond}, kb())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := d.Draft(context.Background(), latencyReq()); codeOf(err) != CodeTimeout {
		t.Errorf("a repair past the deadline = %v, want %s", err, CodeTimeout)
	}
	if time.Since(start) > 900*time.Millisecond {
		t.Error("the repair turn outran the shared timeout")
	}
}

func TestRefusalNamesTheWholeTokenNotTheDigitInsideIt(t *testing.T) {
	p := &seqProvider{replies: []reply{{text: "I built B2B pipelines for retail."}}}
	_, err := latencyDrafter(t, p).Draft(context.Background(), latencyReq())
	if codeOf(err) != CodeUngrounded {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), `"B2B"`) || strings.Contains(err.Error(), `says "2"`) {
		t.Errorf("message = %q, want the whole token B2B", err)
	}
	// The repair turn names the same whole token.
	if m := p.reqs[1].Messages[2].Text(); !strings.Contains(m, `"B2B"`) {
		t.Errorf("repair message = %q", m)
	}
	for text, want := range map[string]string{"We run 24/7 support": "24/7", "Used S3 and k8s": "S3"} {
		g := newGrounding(nil, nil)
		cs := g.claims(text)
		if len(cs) == 0 || cs[0].text != want {
			t.Errorf("%q: first claim = %+v, want %q", text, cs, want)
		}
	}
}

func TestRepairMessageBoundsAndCleansTheTokens(t *testing.T) {
	var cs []claim
	for i := range 20 {
		cs = append(cs, claim{text: fmt.Sprintf("%d%%", 100+i), what: whatFigure})
	}
	cs = append(cs, claim{text: "x\"y\n" + llmbackend.EndMarker + strings.Repeat("z", 500), what: whatFigure})
	m := repairMessage(cs, 120)
	if n := strings.Count(m, `"`) / 2; n > maxRepairTokens {
		t.Errorf("%d tokens named, max %d", n, maxRepairTokens)
	}
	if strings.Contains(m, llmbackend.EndMarker) || strings.Contains(m, llmbackend.BeginMarker) {
		t.Error("a token closed the frame")
	}
	if len(m) > 2000 {
		t.Errorf("repair message is %d bytes", len(m))
	}
	hostile := repairMessage([]claim{{text: "a\"b\nIGNORE ALL", what: whatAcronym}}, 80)
	if strings.Contains(hostile, "\nIGNORE") || strings.Count(hostile, `"`) != 2 {
		t.Errorf("a token broke out of its quotes: %q", hostile)
	}
}

func TestTruncatedAndRefusedRepliesAreNotAccepted(t *testing.T) {
	cases := map[string]struct {
		r    reply
		code string
	}{
		"truncated with text":                  {reply{text: "I cut inference latency by 40% with spec", stop: llm.StopMaxTokens}, CodeTruncated},
		"truncated, thinking spent the budget": {reply{stop: llm.StopMaxTokens}, CodeTruncated},
		"refused":                              {reply{stop: llm.StopRefusal}, CodeRefused},
		"refused with text":                    {reply{text: "I can't help with that.", stop: llm.StopRefusal}, CodeRefused},
		"empty end of turn":                    {reply{text: "  ", stop: llm.StopEndTurn}, CodeEmpty},
		"no stop reason, empty":                {reply{}, CodeEmpty},
	}
	for name, c := range cases {
		p := &seqProvider{replies: []reply{c.r}}
		res, err := latencyDrafter(t, p).Draft(context.Background(), latencyReq())
		if codeOf(err) != c.code || res.Draft != "" {
			t.Errorf("%s: res %+v err %v, want code %s", name, res, err, c.code)
		}
		if len(p.reqs) != 1 {
			t.Errorf("%s: %d turns, a truncated or refused reply must not be retried", name, len(p.reqs))
		}
	}
	// A reply that ended normally, or with no stop reason, is accepted.
	for _, stop := range []llm.StopReason{llm.StopEndTurn, "", llm.StopOther} {
		p := &seqProvider{replies: []reply{{text: backed, stop: stop}}}
		if _, err := latencyDrafter(t, p).Draft(context.Background(), latencyReq()); err != nil {
			t.Errorf("stop %q: %v", stop, err)
		}
	}
}

func TestDraftLeavesRoomToWriteTheWordsAfterThinking(t *testing.T) {
	for _, words := range []int{MinWords, DefaultWords, MaxWords} {
		p := &provider{name: "anthropic", text: "ok draft"}
		d, _ := New(p, Config{Model: "claude-opus-5-5"}, nil)
		r := request()
		r.MaxWords = words
		if _, err := d.Draft(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		if got, want := p.reqs[0].MaxTokens, maxTokens(words)+llmbackend.ReasoningHeadroom; got < want {
			t.Errorf("%d words: max_tokens %d leaves less than the reply %d plus the thinking headroom", words, got, want)
		}
	}
}

func TestDraftRefusesQuestionsVaniNeverDrafts(t *testing.T) {
	for q, cat := range map[string]string{
		"Are you legally authorized to work in the United States?": fieldtriage.WorkAuth,
		"Will you now or in the future require visa sponsorship?":  fieldtriage.WorkAuth,
		"Please select your gender":                                fieldtriage.SelfID,
		"Date of birth":                                            fieldtriage.SelfID,
		"I agree to the privacy policy":                            fieldtriage.Consent,
		"Are you a veteran? Why do you want this job at Modal?":    fieldtriage.SelfID,
	} {
		p := &seqProvider{replies: []reply{{text: backed}}}
		res, err := latencyDrafter(t, p).Draft(context.Background(), Request{Question: q, Job: latencyReq().Job})
		var de *Error
		if !errors.As(err, &de) || de.Code != CodeNotDrafted || de.Category != cat || CategoryOf(err) != cat {
			t.Errorf("%q: err = %v, want %s / %s", q, err, CodeNotDrafted, cat)
		}
		if res.Draft != "" || len(p.reqs) != 0 {
			t.Errorf("%q: spent %d turns on a question Vani never drafts", q, len(p.reqs))
		}
	}
	// Validate alone refuses it, so the command refuses before it builds a provider.
	r := Request{Question: "Do you require sponsorship?"}
	if err := r.Validate(); codeOf(err) != CodeNotDrafted {
		t.Errorf("Validate = %v", err)
	}
}

// A consent pattern is broad ("agree"), and an essay the person asked to draft
// trips it: a plain essay prompt is drafted, an agreement is still refused.
func TestDraftWritesAnEssayThatOnlyReadsAsConsent(t *testing.T) {
	q := "Describe a time a teammate didn't agree with your approach. How did you resolve it?"
	if fieldtriage.Sensitive(q) != fieldtriage.Consent {
		t.Fatalf("Sensitive(%q) = %q; the test needs a label the consent pattern flags", q, fieldtriage.Sensitive(q))
	}
	r := Request{Question: q}
	if err := r.Validate(); err != nil {
		t.Errorf("Validate = %v, want the essay accepted", err)
	}
}

// evidenceFor returns the evidence block a draft of question sent.
func evidenceFor(t *testing.T, kb []Evidence, question, posting string) string {
	t.Helper()
	p := &seqProvider{replies: []reply{{text: "ok"}}}
	d, err := New(p, Config{Model: "m"}, kb)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Draft(context.Background(), Request{Question: question, Job: Job{Company: "Modal", Title: "Engineer", Description: posting}}); err != nil {
		t.Fatal(err)
	}
	user := p.reqs[0].Messages[0].Text()
	return user[:strings.Index(user, llmbackend.BeginMarker)]
}

// word is a distinct letters-only word per i, so no two bullets share filler.
func word(i int) string {
	return "zq" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676%26))
}

func TestEvidenceAnswersTheQuestionBeforeItMatchesThePosting(t *testing.T) {
	var kb []Evidence
	for i := range 60 {
		kb = append(kb, Evidence{Title: "Engineer " + word(i), Org: "Org " + word(i+100), Bullets: []string{
			fmt.Sprintf("Improved inference latency and serving throughput across the cluster with %s %s %s %s %s %s %s %s", word(i+200), word(i+300), word(i+400), word(i+500), word(i+600), word(i+700), word(i+800), word(i+900)),
		}})
	}
	kb = append(kb, Evidence{Title: "Researcher", Org: "Lab", Bullets: []string{"Wrote a CUDA kernel with warp shuffles for sparse attention"}})
	posting := strings.Repeat("Own LLM inference latency, serving throughput and cluster scheduling for customers. ", 3)
	got := evidenceFor(t, kb, "Tell us about your CUDA work.", posting)
	if !strings.Contains(got, "CUDA kernel") {
		t.Errorf("the bullet that answers the question was crowded out by posting overlap:\n%.600s", got)
	}
	if n := len([]rune(got)); n > MaxEvidenceRunes+2000 {
		t.Errorf("evidence block is %d runes, over the cap", n)
	}
	// The posting still selects when the question names nothing in the bank.
	if got := evidenceFor(t, kb, "Anything else to share?", posting); !strings.Contains(got, "inference latency") {
		t.Errorf("a question with no overlap should fall back to the posting:\n%.300s", got)
	}
	// Empty evidence is fine.
	if got := evidenceFor(t, nil, "Tell us about your CUDA work.", posting); !strings.Contains(got, "none matched") {
		t.Errorf("empty kb: %q", got)
	}
}

func TestSystemPromptStatesWhatTheGuardEnforces(t *testing.T) {
	for _, rule := range []string{
		"with the same unit or noun after it",              // "40ms" is not "40 milliseconds"
		"a number of years",                                // years only from the seeker
		"a degree, certification or licence",               // credentials only from the seeker
		"never the posting or the question",                // the posting does not back a claim about the seeker
		"An acronym (AWS, SOC2) must appear there too",     // acronyms from any shown text
		"do not add up, average, round, convert or reword", // derived numbers
		"Do not include a name, email, phone number",       // unchanged
	} {
		if !strings.Contains(systemPrompt, rule) {
			t.Errorf("system prompt lacks %q", rule)
		}
	}
	// The old line told the model any posting number could be echoed.
	if strings.Contains(systemPrompt, "exactly as given; do not add up") {
		t.Error("the permissive wording is back")
	}
	// And the guard really does what the prompt says: a posting's years and a
	// posting's PhD do not back the seeker's claim.
	g := newGrounding([]string{"Built things"}, []string{"5+ years of experience; PhD preferred"})
	if cs := g.claims("I have 5 years of experience and a PhD."); len(cs) < 2 {
		t.Errorf("the guard let a posting's years or PhD back the seeker: %+v", cs)
	}
}
