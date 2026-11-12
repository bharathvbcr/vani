package eval

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/decide/jdtriage"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/decide"
)

// A teacher row names the raw title and location the question rendered beside
// the description, as title_sha256 and location_sha256 (HashText, the hash
// text_sha256 already uses), so a reader can tell whether two readings saw the
// same rendered posting. text_sha256 stays the description's hash, and the new
// fields are not part of a reading's identity: a stored reading stays current,
// so nothing is asked again because of them.
func TestTeacherRowBindsTitleAndLocation(t *testing.T) {
	p := posting.Posting{Key: "gh:acme:1", Title: "  ML Engineer (Inference) ", Location: "Remote — EU", Description: "Build things.\n"}
	task := JDTriageTask("ML systems roles")
	q := jdtriage.Question(p)
	row := teacherRow(p, "claude-sonnet-5-5", task, q, decide.Answer{}, time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"text_sha256":     HashText(p.Description),
		"title_sha256":    HashText(p.Title),
		"location_sha256": HashText(p.Location),
		"fixture_sha":     FixtureSHA(),
		"prompt_hash":     PromptHash("ML systems roles"),
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %s", k, m[k], v)
		}
	}
	// Another title over the same description is the same identity.
	other := p
	other.Title, other.Location = "Platform Engineer", "Austin, TX"
	if row.ident() != task.Ident(other, "claude-sonnet-5-5") {
		t.Errorf("the title or location became part of the identity: %+v", row.ident())
	}
}

// A row written before the fields existed still reads as a valid, current row.
func TestTeacherRowWithoutTitleAndLocationStillReads(t *testing.T) {
	old := `{"v":1,"key":"gh:acme:1","text_sha256":"` + HashText("Build things.") + `","model":"m","fixture_sha":"f","prompt_hash":"p","slots":{"role_fit":{"reading":"answered","value":"strong_fit"}},"at":"2026-11-01T00:00:00Z"}`
	var r TeacherRow
	if err := json.Unmarshal([]byte(old), &r); err != nil || r.validate() != nil {
		t.Fatalf("old row: %v %v", err, r.validate())
	}
}
