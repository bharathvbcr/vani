package posting

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// legacyPosting is Posting as the binary before ApplyForm declared it: what an
// older reader decodes a newer state file into.
type legacyPosting struct {
	Key         Key       `json:"key"`
	ATS         ATS       `json:"ats"`
	Company     string    `json:"company"`
	Title       string    `json:"title"`
	Location    string    `json:"location,omitempty"`
	Remote      bool      `json:"remote,omitempty"`
	Department  string    `json:"department,omitempty"`
	URL         string    `json:"url"`
	ApplyURL    string    `json:"apply_url,omitempty"`
	PostedAt    time.Time `json:"posted_at,omitzero"`
	UpdatedAt   time.Time `json:"updated_at,omitzero"`
	Description string    `json:"description,omitempty"`
	SeenAt      time.Time `json:"seen_at"`
}

func formPosting() Posting {
	return Posting{
		Key: "ab:acme:1", ATS: Ashby, Company: "Acme", Title: "T", URL: "https://x/1", Description: "d",
		SeenAt: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC),
		Form: &ApplyForm{V: ApplyFormVersion, Questions: 3, Required: 2,
			Gates: []FormGate{{Kind: GateSponsorship, Text: "Sponsorship?", YesNo: true}}},
	}
}

// An older binary reading a newer posting ignores apply_form and keeps every
// field it knows; a newer binary reading an older posting has a nil form.
func TestApplyFormIsAdditiveForOldReaders(t *testing.T) {
	raw, err := json.Marshal(formPosting())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"apply_form":{"v":1`) {
		t.Fatalf("%s", raw)
	}
	var old legacyPosting
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatalf("an older reader fails on the new key: %v", err)
	}
	if old.Key != "ab:acme:1" || old.Company != "Acme" || old.Description != "d" || old.URL != "https://x/1" {
		t.Fatalf("%+v", old)
	}
	// What the older binary writes back has no form, and reads back whole.
	back, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	var now Posting
	if err := json.Unmarshal(back, &now); err != nil || now.Form != nil || now.Key != "ab:acme:1" {
		t.Fatalf("%v %+v", err, now)
	}
}

func TestPostingWithoutAFormEncodesAsBefore(t *testing.T) {
	p := formPosting()
	p.Form = nil
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "apply_form") {
		t.Fatalf("%s", raw)
	}
	old, err := json.Marshal(legacyPosting{Key: p.Key, ATS: p.ATS, Company: p.Company, Title: p.Title, URL: p.URL, Description: p.Description, SeenAt: p.SeenAt})
	if err != nil || string(old) != string(raw) {
		t.Fatalf("%v\n new %s\n old %s", err, raw, old)
	}
}

func TestApplyFormUsableAndEqual(t *testing.T) {
	var nilForm *ApplyForm
	if nilForm.Usable() || nilForm.Flagged() || !nilForm.Equal(nil) {
		t.Fatal("nil form")
	}
	f := &ApplyForm{V: ApplyFormVersion, Questions: 2}
	g := &ApplyForm{V: ApplyFormVersion, Questions: 2}
	if !f.Usable() || !f.Equal(g) || f.Equal(nil) || nilForm.Equal(f) {
		t.Fatal("equal forms")
	}
	g.Gates = []FormGate{{Kind: GatePlace, Flag: FlagPlaceList}}
	if f.Equal(g) || !g.Flagged() || f.Flagged() {
		t.Fatal("a gate differs")
	}
	if (&ApplyForm{V: ApplyFormVersion + 1}).Usable() || (&ApplyForm{}).Usable() {
		t.Fatal("another version is not usable")
	}
}
