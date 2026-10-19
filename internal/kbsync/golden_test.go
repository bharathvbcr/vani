package kbsync

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The golden files pin the exact career_upsert_block arguments the client
// sends. They were checked by hand against DevPrism's ExperienceBlock
// (career_db/mod.rs:24-118, serde camelCase) and the tool's input schema
// (tools_career.rs:293-313: required "block"; optional "input_responses" and
// "request_state", which Apply never sets). Regenerate with
// KBSYNC_UPDATE_GOLDEN=1 and review the diff.

func goldenCheck(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("KBSYNC_UPDATE_GOLDEN") == "1" {
		var buf bytes.Buffer
		if err := json.Indent(&buf, got, "", "  "); err != nil {
			t.Fatal(err)
		}
		buf.WriteByte('\n')
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s: %v", path, err)
	}
	jsonEq(t, got, want)
}

func upsertArgsFor(t *testing.T, f *fakeDevPrism, id string) []byte {
	t.Helper()
	for _, c := range f.upserts() {
		var a struct {
			Block struct {
				ID string `json:"id"`
			} `json:"block"`
		}
		if err := json.Unmarshal(c.Args, &a); err != nil {
			t.Fatal(err)
		}
		if a.Block.ID == id {
			return c.Args
		}
	}
	t.Fatalf("no upsert for %s", id)
	return nil
}

func TestGoldenUpsertRequests(t *testing.T) {
	f := newFake()
	f.seed(t, `{"id":"beta-proj","kind":"project","title":"Old widget","org":"","dateRange":{"start":"","end":null},"personas":["ai"],"domains":[],"skills":[{"name":"go","level":4}],"seniorityLevel":"senior","updatedAt":"1","embeddingText":"stale",
	  "bullets":[{"id":"beta-proj/b3","canonical":"old words","variants":{"ai":"spin"},"metrics":[],"evidenceRefs":["kb1"],"locked":false}],
	  "facts":[{"id":"f1","text":"a fact","skills":[],"metrics":[],"source":"manual","createdAt":"t"}]}`)
	_, res, err := applyAll(t, f, fixtureResolved(t), NewSyncState(filepath.Join(t.TempDir(), "s.json")))
	if err != nil || res.Added != 2 || res.Updated != 1 || res.NeedsConfirmation != 0 {
		t.Fatalf("%v %+v", err, res)
	}
	goldenCheck(t, "upsert_add_work.golden.json", upsertArgsFor(t, f, "alpha-job"))
	goldenCheck(t, "upsert_add_education.golden.json", upsertArgsFor(t, f, "gamma-edu"))
	goldenCheck(t, "upsert_update_project.golden.json", upsertArgsFor(t, f, "beta-proj"))
}

// rustBlock mirrors ExperienceBlock, Bullet, SkillTag and BlockFact. Decoding
// with DisallowUnknownFields proves the payload carries no field DevPrism
// lacks; the presence checks prove the required ones are there.
type rustBlock struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Org       string `json:"org"`
	DateRange struct {
		Start string  `json:"start"`
		End   *string `json:"end"`
	} `json:"dateRange"`
	Personas []string `json:"personas"`
	Domains  []string `json:"domains"`
	Skills   []struct {
		Name  string   `json:"name"`
		Level uint8    `json:"level"`
		Years *float64 `json:"years"`
	} `json:"skills"`
	SeniorityLevel string  `json:"seniorityLevel"`
	Location       *string `json:"location"`
	URL            *string `json:"url"`
	URLLabel       *string `json:"urlLabel"`
	Extra          *string `json:"extra"`
	Bullets        []struct {
		ID           string                         `json:"id"`
		Canonical    string                         `json:"canonical"`
		Variants     map[string]json.RawMessage     `json:"variants"`
		Metrics      []struct{ Value, Kind string } `json:"metrics"`
		EvidenceRefs []string                       `json:"evidenceRefs"`
		Locked       bool                           `json:"locked"`
	} `json:"bullets"`
	Facts []struct {
		ID        string                         `json:"id"`
		Text      string                         `json:"text"`
		Skills    []string                       `json:"skills"`
		Metrics   []struct{ Value, Kind string } `json:"metrics"`
		Source    string                         `json:"source"`
		CreatedAt string                         `json:"createdAt"`
	} `json:"facts"`
	Notes         *string `json:"notes"`
	EmbeddingText *string `json:"embeddingText"`
	UpdatedAt     string  `json:"updatedAt"`
}

func TestPayloadsSatisfyDevPrismSchema(t *testing.T) {
	f := newFake()
	f.seed(t, `{"id":"beta-proj","kind":"project","title":"t","org":"","dateRange":{"start":"","end":null},"personas":[],"domains":[],"skills":[],"seniorityLevel":"ic","updatedAt":"1","bullets":[],"facts":[]}`)
	if _, _, err := applyAll(t, f, fixtureResolved(t), NewSyncState(filepath.Join(t.TempDir(), "s.json"))); err != nil {
		t.Fatal(err)
	}
	if len(f.upserts()) != 3 {
		t.Fatalf("%d upserts", len(f.upserts()))
	}
	for _, c := range f.upserts() {
		var args map[string]json.RawMessage
		if err := json.Unmarshal(c.Args, &args); err != nil || len(args) != 1 {
			t.Fatalf("args must be exactly {block}: %s", c.Args)
		}
		var presence map[string]json.RawMessage
		_ = json.Unmarshal(args["block"], &presence)
		for _, req := range []string{"id", "kind", "title", "org", "dateRange", "seniorityLevel", "updatedAt"} {
			if _, ok := presence[req]; !ok {
				t.Errorf("required field %q missing: %s", req, args["block"])
			}
		}
		dec := json.NewDecoder(bytes.NewReader(args["block"]))
		dec.DisallowUnknownFields()
		var b rustBlock
		if err := dec.Decode(&b); err != nil {
			t.Fatalf("payload does not fit ExperienceBlock: %v\n%s", err, args["block"])
		}
		switch b.Kind {
		case KindWork, KindProject, KindEducation:
		default:
			t.Errorf("kind %q", b.Kind)
		}
		if b.UpdatedAt != "2026-01-02T03:04:05Z" {
			t.Errorf("updatedAt %q", b.UpdatedAt)
		}
	}
	_ = context.Background()
}
