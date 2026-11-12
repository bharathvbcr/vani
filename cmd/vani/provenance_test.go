package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/bharathvbcr/Manvi/manvi/llm"
	"github.com/bharathvbcr/Vani/internal/config"
	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/decide/llmbackend"
	"github.com/bharathvbcr/Vani/internal/eval"
	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/manualtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// The provenance a training export reads off Vani's records (Lappi-decision's
// job_pairs.rs): the keys are spelled out, not taken from constants, so a
// rename fails here.

// testTierModel is the model id the test escalator's tier answers as.
const testTierModel = "claude-haiku-5-5"

// On the real wiring, an escalated slot's shadow reading names the model the
// tier asked (llm.triage_model, which may change between runs), and the triage
// event names the raw text the question was built from.
func TestWiredEscalationRecordsTheTierModelAndTheText(t *testing.T) {
	for _, model := range []string{"claude-haiku-5-5", "claude-sonnet-5-5"} {
		t.Run(model, func(t *testing.T) {
			rig := newEscalationRig(t, func(c *config.Config) { c.LLM.TriageModel = model },
				map[string]float64{"role_fit": 0.3}, map[string]string{"role_fit": "mismatch_domain"})
			if err := rig.r.Advance(context.Background(), "gh:acme:1"); err != nil {
				t.Fatal(err)
			}
			bodies := *rig.claudeBody
			if len(bodies) != 1 || requestModel(t, bodies[0]) != model {
				t.Fatalf("Claude requests: %d", len(bodies))
			}
			var triage *ledger.Event
			for i, e := range rig.l.events {
				if e.Annotations["triage.role_fit.reading"] != "" {
					triage = &rig.l.events[i]
				}
			}
			if triage == nil {
				t.Fatal("no triage event")
			}
			src := liveEvent("gh:acme:1").Posting
			want := map[string]string{
				"triage.role_fit.shadow_backend": "escalate",
				"triage.role_fit.shadow_model":   model,
				"triage.desc_sha256":             eval.HashText(src.Description),
				"triage.title_sha256":            eval.HashText(src.Title),
				"triage.location_sha256":         eval.HashText(src.Location),
			}
			for k, v := range want {
				if triage.Annotations[k] != v {
					t.Errorf("triage event %s = %q, want %q", k, triage.Annotations[k], v)
				}
			}
			for _, slot := range []string{"sponsorship", "work_mode", "seniority"} {
				if v, has := triage.Annotations["triage."+slot+".shadow_model"]; has {
					t.Errorf("%s was not escalated but names a shadow model %q", slot, v)
				}
			}
		})
	}
}

// The LLM backend as the drop-candidate shadow (decide.shadow "llm") answers
// under the name "llm", which does not say which Claude model it asked: its
// reading names llm.triage_model as triage.<slot>.shadow_model.
func TestWiredLLMShadowRecordsItsModel(t *testing.T) {
	for _, model := range []string{"claude-haiku-5-5", "claude-sonnet-5-5"} {
		t.Run(model, func(t *testing.T) {
			rig := cloudShadowRig(t, func(c *config.Config) { c.LLM.TriageModel = model })
			if err := rig.r.Advance(context.Background(), "gh:acme:1"); err != nil {
				t.Fatal(err)
			}
			if bodies := *rig.claudeBody; len(bodies) != 1 || requestModel(t, bodies[0]) != model {
				t.Fatalf("shadow requests: %d", len(bodies))
			}
			rec, _ := rig.l.Get("gh:acme:1")
			if rec.Annotations["triage.role_fit.shadow_backend"] != "llm" || rec.Annotations["triage.role_fit.shadow_model"] != model {
				t.Fatalf("shadow backend %q model %q, want llm / %s", rec.Annotations["triage.role_fit.shadow_backend"],
					rec.Annotations["triage.role_fit.shadow_model"], model)
			}
		})
	}
}

// modelTagged tags every slot, an error's readings too, and never writes the
// wrapped backend's own map.
func TestModelTaggedTagsEveryReadingWithoutWritingTheBackendsMap(t *testing.T) {
	own := map[string]decide.SlotReading{
		"role_fit":  {Reading: decide.Answered, Value: "strong_fit"},
		"work_mode": {Reading: decide.Unavailable, Detail: "deadline"},
	}
	inner := &escFake{reply: func(decide.Question) (decide.Answer, error) {
		return decide.Answer{Backend: "llm", Slots: own}, errors.New("partial")
	}}
	a, err := modelTagged{Backend: inner, model: "claude-haiku-5-5"}.Decide(context.Background(), triageQuestion("gh:acme:1"))
	if err == nil {
		t.Fatal("the backend's error was dropped")
	}
	if a.Slots["role_fit"].Detail != "model=claude-haiku-5-5" || a.Slots["work_mode"].Detail != "deadline;model=claude-haiku-5-5" {
		t.Fatalf("slots %+v", a.Slots)
	}
	if own["role_fit"].Detail != "" || own["work_mode"].Detail != "deadline" {
		t.Fatalf("the backend's own map was written: %+v", own)
	}
}

// The backfill records the tier's model on each reading and the raw text it
// asked the tier about, under the shadow's own keys.
func TestBackfillRecordsTheTierModelAndText(t *testing.T) {
	l := bfLedger(t)
	keys := seedUnsure(t, l, 2)
	f := &escFake{reply: answerWith(lastOption)}
	rep := backfillPass(context.Background(), bfRunner(l), escalatorFor(t, f, 0.5, 100), itemsOf(l), "", 1)
	if rep.Recorded != 2 {
		t.Fatalf("report %+v", rep)
	}
	for _, k := range keys {
		got, _ := l.Get(k)
		want := map[string]string{
			"triage.role_fit.shadow_model":  testTierModel,
			"triage.shadow_desc_sha256":     eval.HashText(got.Posting.Description),
			"triage.shadow_title_sha256":    eval.HashText(got.Posting.Title),
			"triage.shadow_location_sha256": eval.HashText(got.Posting.Location),
		}
		for key, v := range want {
			if got.Annotations[key] != v {
				t.Errorf("%s: %s = %q, want %q", k, key, got.Annotations[key], v)
			}
		}
	}
}

// A manual reading is stored with the provenance a teacher-store row carries,
// computed the teacher's way: fixture_sha (FixtureSHA), prompt_hash
// (PromptHash of the target summary), text_sha256 (HashText of the raw
// description). The store's own text (which binds the rendered title and
// location) and prompt hashes are kept as they were.
func TestJobsRetriageRecordsTheTeacherProvenance(t *testing.T) {
	p := &retriageLLM{text: rtAnswer, usage: llm.Usage{InputTokens: 1800, OutputTokens: 210}, stop: llm.StopEndTurn}
	_, state := rtSetup(t, p, nil)
	rtSeed(t, state, rtJob{key: "gh:acme:2", title: "Platform Engineer", fit: "adjacent", rank: "0.6", p: "0.9"})
	rtRun(t, 0, "gh:acme:2")

	raw, err := os.ReadFile(rtStoreFile(state))
	if err != nil {
		t.Fatal(err)
	}
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &line); err != nil {
		t.Fatalf("store %q: %v", raw, err)
	}
	post := rtPosting("gh:acme:2", "Platform Engineer")
	want := map[string]string{
		"fixture_sha": eval.FixtureSHA(),
		"prompt_hash": eval.PromptHash(rtSummary),
		"text_sha256": eval.HashText(post.Description),
		"text":        manualtriage.TextSHA256(post),
		"model":       rtSonnet,
	}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("stored %s = %v, want %s", k, line[k], v)
		}
	}
}

// Why the retriage store's prompt hash never equalled the teacher's: both
// stores hash the same question (the same fixture, system prompt and target
// summary) with different recipes. The retriage store's `prompt` is
// sha256(system || 0x00 || summary || 0x00 || fixture.json); the teacher's
// prompt_hash is sha256 over the length-prefixed system prompt, user message
// with the context left empty, and summary, and its fixture sha is separate.
// This pins that the question a manual reading sends is the one PromptHash
// fingerprints, so the prompt_hash it now stores is true of it.
func TestRetriageAsksTheQuestionPromptHashFingerprints(t *testing.T) {
	post := rtPosting("gh:acme:2", "Platform Engineer")
	q := rtQuestion(t, post, rtSummary)
	sys, _ := llmbackend.Messages(q)

	empty := jdtriage.Question(posting.Posting{})
	empty.Context = nil
	hashedSys, hashedUser := llmbackend.Messages(empty)
	if sys != hashedSys {
		t.Fatal("the retriage system prompt is not the one PromptHash hashes")
	}
	q.Context = nil
	if _, user := llmbackend.Messages(q); user != hashedUser {
		t.Fatal("the retriage user message, context left out, is not the one PromptHash hashes")
	}
	if q.Task != jdtriage.Task || q.Prompt != jdtriage.Prompt || len(q.Slots) != len(jdtriage.Slots) {
		t.Fatalf("the retriage question is not the jd_triage question: %s %d slots", q.Task, len(q.Slots))
	}
}
