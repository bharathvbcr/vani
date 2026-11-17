package srcutil

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/bharathvbcr/Vani/internal/ledger"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

func storableBoard() sources.Board {
	return sources.Board{ATS: posting.Greenhouse, Token: "acme", Company: "Acme"}
}

func storableRow() posting.Posting {
	return posting.Posting{Key: "gh:acme:1", ATS: posting.Greenhouse, Company: "Acme", Title: "Engineer",
		URL: "https://boards.acme.test/1", Description: "about the role"}
}

// Each text field and key part is accepted at its bound and refused one byte
// over. The error names the field and never carries the value.
func TestStorableByteBounds(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	url := func(n int) string { return "https://h.test/" + long(n-len("https://h.test/")) }
	cases := []struct {
		name  string
		set   func(p *posting.Posting, n int)
		bound int
		field string
	}{
		{"company", func(p *posting.Posting, n int) { p.Company = long(n) }, MaxCompany, "company"},
		{"location", func(p *posting.Posting, n int) { p.Location = long(n) }, MaxLocation, "location"},
		{"department", func(p *posting.Posting, n int) { p.Department = long(n) }, MaxDepartment, "department"},
		{"url", func(p *posting.Posting, n int) { p.URL = url(n) }, MaxURL, "url"},
		{"apply url", func(p *posting.Posting, n int) { p.ApplyURL = url(n) }, MaxURL, "apply_url"},
		{"description", func(p *posting.Posting, n int) { p.Description = long(n) }, MaxDescription, "description"},
		{"key part", func(p *posting.Posting, n int) { p.Key = posting.Key("gh:" + long(n) + ":1") }, MaxKeyPart, "key part 1"},
		{"second key part", func(p *posting.Posting, n int) { p.Key = posting.Key("gh:acme:" + long(n)) }, MaxKeyPart, "key part 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			at := storableRow()
			tc.set(&at, tc.bound)
			if _, err := Storable(storableBoard(), at); err != nil {
				t.Fatalf("at the bound %d: %v", tc.bound, err)
			}
			over := storableRow()
			tc.set(&over, tc.bound+1)
			_, err := Storable(storableBoard(), over)
			if err == nil {
				t.Fatalf("one byte over %d: accepted", tc.bound)
			}
			if !strings.Contains(err.Error(), tc.field) || len(err.Error()) > 200 || strings.Contains(err.Error(), "aaaaaaaa") {
				t.Errorf("error %q: want it to name %q, stay short and not echo the value", err, tc.field)
			}
		})
	}
}

// A whole key past MaxKey is refused even when every part is within its bound.
func TestStorableKeyBounds(t *testing.T) {
	p := storableRow()
	p.Key = posting.Key("wd:" + strings.Repeat("a", MaxKeyPart) + ":" + strings.Repeat("b", MaxKeyPart) + ":" + strings.Repeat("c", MaxKeyPart))
	if len(p.Key) <= MaxKey {
		t.Fatalf("test key is %d bytes, want over %d", len(p.Key), MaxKey)
	}
	if _, err := Storable(storableBoard(), p); err == nil || !strings.Contains(err.Error(), "key is") {
		t.Errorf("err %v", err)
	}
	for _, k := range []posting.Key{"", "nocolon", "zz:acme:1", "gh::1", "gh:acme:"} {
		p.Key = k
		if _, err := Storable(storableBoard(), p); err == nil {
			t.Errorf("key %q accepted", k)
		}
	}
	// A fallback key is x:{hash}, one part.
	p.Key = "x:0123456789abcdef01234567"
	if _, err := Storable(storableBoard(), p); err != nil {
		t.Errorf("fallback key: %v", err)
	}
}

// A multi-byte field at the cap is accepted whole and over it refused whole:
// nothing is cut, so no rune is split.
func TestStorableDoesNotCutMultiByteFields(t *testing.T) {
	p := storableRow()
	p.Location = strings.Repeat("é", MaxLocation/2) // exactly MaxLocation bytes
	got, err := Storable(storableBoard(), p)
	if err != nil || got.Location != p.Location {
		t.Fatalf("at the cap: err %v, location changed %v", err, got.Location != p.Location)
	}
	p.Location += "é"
	if _, err := Storable(storableBoard(), p); err == nil {
		t.Errorf("over the cap by one rune: accepted")
	}
}

// A title is normalised by Title; one that is empty afterwards is refused.
func TestStorableTitle(t *testing.T) {
	p := storableRow()
	p.Title = "  Senior Engineer \n "
	got, err := Storable(storableBoard(), p)
	if err != nil || got.Title != "Senior Engineer" {
		t.Errorf("got %q, err %v", got.Title, err)
	}
	for _, empty := range []string{"", " ", " \t\n ", "&nbsp;"} {
		p.Title = empty
		if _, err := Storable(storableBoard(), p); err == nil || !strings.Contains(err.Error(), "title") {
			t.Errorf("title %q: err %v", empty, err)
		}
	}
}

// A time outside years 1..9999 becomes the zero time, in every time field; a
// time inside, including the bounds, is kept.
func TestStorableTimes(t *testing.T) {
	bounds := []struct {
		name string
		at   time.Time
		kept bool
	}{
		{"year 0", time.Date(0, 12, 31, 23, 59, 59, 0, time.UTC), false},
		{"year 1", time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), true},
		{"year 9999 last second", time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), true},
		{"year 10000", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"negative unix", time.Unix(-1<<45, 0).UTC(), false},
		{"unixmilli overflow", time.UnixMilli(1 << 62).UTC(), false},
		// 9999-12-31T23:59:59-05:00 is year 10000 in UTC.
		{"zone pushes UTC past 9999", time.Date(9999, 12, 31, 23, 59, 59, 0, time.FixedZone("x", -5*3600)), false},
		{"zero stays zero", time.Time{}, true},
	}
	for _, tc := range bounds {
		t.Run(tc.name, func(t *testing.T) {
			p := storableRow()
			p.PostedAt, p.UpdatedAt, p.SeenAt = tc.at, tc.at, tc.at
			got, err := Storable(storableBoard(), p)
			if err != nil {
				t.Fatal(err)
			}
			for field, v := range map[string]time.Time{"PostedAt": got.PostedAt, "UpdatedAt": got.UpdatedAt, "SeenAt": got.SeenAt} {
				if tc.kept && !v.Equal(tc.at) || !tc.kept && !v.IsZero() {
					t.Errorf("%s = %v, kept=%v for %v", field, v, tc.kept, tc.at)
				}
			}
			if _, err := json.Marshal(got); err != nil {
				t.Errorf("the returned row still cannot be encoded: %v", err)
			}
		})
	}
}

// URLs are held to CheckURL: other schemes and userinfo are refused for both
// the listing URL and the apply URL.
func TestStorableURLs(t *testing.T) {
	for _, bad := range []string{"javascript:alert(1)", "file:///etc/passwd", "https://jobs.acme.test@evil.test/", "/relative"} {
		p := storableRow()
		p.URL = bad
		if _, err := Storable(storableBoard(), p); err == nil || !strings.Contains(err.Error(), "url") {
			t.Errorf("url %q: err %v", bad, err)
		}
		p = storableRow()
		p.ApplyURL = bad
		if _, err := Storable(storableBoard(), p); err == nil || !strings.Contains(err.Error(), "apply_url") {
			t.Errorf("apply url %q: err %v", bad, err)
		}
	}
}

// The declared caps keep one ledger line under the ledger's own limit: every
// text field at its cap, made entirely of the bytes JSON escapes most
// expensively (a control character is six bytes), still encodes as an event
// the ledger accepts. This is the check that the bounds, taken together, do
// what they are for.
func TestStorableCapsKeepAWorstCaseEventUnderTheLedgerLimit(t *testing.T) {
	ctl := func(n int) string { return strings.Repeat("\x01", n) }
	p := posting.Posting{
		Key:         posting.Key("wd:" + strings.Repeat("a", MaxKeyPart) + ":" + strings.Repeat("b", MaxKeyPart)),
		ATS:         posting.Workday,
		Company:     ctl(MaxCompany),
		Title:       strings.Repeat("<", MaxTitle),
		Location:    ctl(MaxLocation),
		Department:  ctl(MaxDepartment),
		URL:         "https://h.test/" + strings.Repeat("a", MaxURL-len("https://h.test/")),
		ApplyURL:    "https://h.test/" + strings.Repeat("a", MaxURL-len("https://h.test/")),
		Description: ctl(MaxDescription),
	}
	ev := ledger.Event{V: ledger.SchemaVersion, At: time.Now(), Key: p.Key, Status: posting.Discovered, Posting: &p}
	line, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if len(line)+1 > ledger.MaxLineBytes {
		t.Errorf("worst-case event is %d bytes, over the ledger's %d", len(line)+1, ledger.MaxLineBytes)
	}
	t.Logf("worst-case event: %d of %d bytes", len(line)+1, ledger.MaxLineBytes)
	// Storable itself accepts it if its encoded size is within MaxRecordBytes;
	// the worst case above is the control characters' six-fold cost.
	if _, err := Storable(storableBoard(), p); err != nil && len(line) <= MaxRecordBytes {
		t.Errorf("a row inside both limits was refused: %v", err)
	}
}

// A row whose encoded form is past MaxRecordBytes is refused even when every
// capped field is within its cap (the uncapped form summary).
func TestStorableRefusesAnOversizedEncodedRow(t *testing.T) {
	p := storableRow()
	p.Form = &posting.ApplyForm{V: 1}
	for i := 0; i < 40000; i++ {
		p.Form.Uploads = append(p.Form.Uploads, strings.Repeat("u", 20))
	}
	if _, err := Storable(storableBoard(), p); err == nil || !strings.Contains(err.Error(), "encodes to") {
		t.Errorf("err %v", err)
	}
}

// A valid row comes back unchanged.
func TestStorableLeavesAValidRowAlone(t *testing.T) {
	p := storableRow()
	p.Location, p.Department = "Remote, US", "Platform"
	p.PostedAt = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	got, err := Storable(storableBoard(), p)
	if err != nil || got != p {
		t.Errorf("changed or refused: %v", err)
	}
}
