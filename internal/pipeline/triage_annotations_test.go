package pipeline

import (
	"testing"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

func TestTriageAnnotationsCalibrationAndShadow(t *testing.T) {
	lappi := decide.SlotReading{Reading: decide.Answered, Value: "senior", Lappi: &decide.LappiCalib{Score: 0.5}}
	a := decide.Answer{
		Backend: "jev+lappi",
		Slots: map[string]decide.SlotReading{
			"remote":    tagged(jev("onsite", 0.93456), "jev"),
			"seniority": tagged(lappi, "lappi"),
			"agree":     jev("x", 0.5),
			"noshadow":  {Reading: decide.Abstained},
			"shabst":    jev("y", 0.7),
			"primabst":  {Reading: decide.Abstained},
		},
		Shadow: map[string]decide.SlotReading{
			"remote":    {Reading: decide.Answered, Value: "remote", Detail: "backend=lappi"},
			"seniority": {Reading: decide.Answered, Value: "senior"},
			"agree":     {Reading: decide.Answered, Value: "x"},
			"shabst":    {Reading: decide.Abstained, Detail: "noul;backend=lappi"},
			"primabst":  {Reading: decide.Answered, Value: "z"},
		},
	}
	ann := triageAnnotations(a)
	want := map[string]string{
		"triage.remote.shadow_reading":    "answered",
		"triage.remote.shadow_backend":    "lappi",
		"triage.shabst.shadow_reading":    "abstained",
		"triage.shabst.shadow_backend":    "lappi",
		"triage.seniority.shadow_reading": "answered",
		"triage.remote.p":                 "0.9346",
		"triage.remote.shadow":            "remote",
		"triage.remote.disagree":          "true",
		"triage.seniority.lappi_score":    "0.5000",
		"triage.seniority.shadow":         "senior",
		"triage.agree.p":                  "0.5000",
		"triage.agree.shadow":             "x",
		"triage.shabst.p":                 "0.7000",
		"triage.primabst.shadow":          "z",
	}
	for k, v := range want {
		if ann[k] != v {
			t.Errorf("%s = %q, want %q", k, ann[k], v)
		}
	}
	for _, k := range []string{"triage.seniority.disagree", "triage.agree.disagree", "triage.shabst.shadow",
		"triage.shabst.disagree", "triage.primabst.disagree", "triage.noshadow.shadow", "triage.noshadow.p",
		"triage.remote.lappi_score", "triage.seniority.p", "triage.noshadow.shadow_reading",
		"triage.seniority.shadow_backend"} {
		if v, ok := ann[k]; ok {
			t.Errorf("unexpected %s = %q", k, v)
		}
	}
	// Without a shadow the annotations are exactly the old ones plus calibration.
	plain := triageAnnotations(decide.Answer{Backend: "jev", Slots: map[string]decide.SlotReading{"remote": jev("remote", 0.9)}})
	for k := range plain {
		if k == "triage.remote.shadow" || k == "triage.remote.disagree" {
			t.Errorf("shadow key %s without shadow", k)
		}
	}
}

func TestShadowAnswerCanNeverDropAPosting(t *testing.T) {
	// The shadow is a confident, calibrated "onsite" while the primary says
	// "remote": the policy must read only the primary.
	a := decide.Answer{
		Backend: "jev",
		Slots:   map[string]decide.SlotReading{"remote": jev("remote", 0.99)},
		Shadow:  map[string]decide.SlotReading{"remote": jev("onsite", 1)},
	}
	if st, why := policy.Evaluate(a); st != posting.Triaged {
		t.Fatalf("shadow influenced the policy: %s (%s)", st, why)
	}
	// A shadow-only answer (primary abstained) must not drop either: unsure.
	b := decide.Answer{
		Backend: "jev",
		Slots:   map[string]decide.SlotReading{"remote": {Reading: decide.Abstained}},
		Shadow:  map[string]decide.SlotReading{"remote": jev("onsite", 1)},
	}
	if st, _ := policy.Evaluate(b); st != posting.Unsure {
		t.Fatalf("shadow-only answer status = %s, want unsure", st)
	}
	// Identical decisions with and without the Shadow field.
	b.Shadow = nil
	if st, _ := policy.Evaluate(b); st != posting.Unsure {
		t.Fatalf("status changed without shadow: %s", st)
	}
}
