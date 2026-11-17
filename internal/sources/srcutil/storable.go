package srcutil

import (
	"encoding/json" // v1, deliberately: the ledger writes events with it, and the size check must measure the same bytes
	"fmt"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
)

// Storability. Each adapter decides alone whether a row is fit to store, and
// each forgets something: lever built PostedAt from an unbounded millisecond
// count, eightfold from an unbounded second count, jibe converted a parsed
// stamp to UTC. A time outside years 0..9999 cannot be marshalled and an
// oversized row passes the ledger's line limit, and the ledger then refuses
// the whole batch of its board (and, in the run, ends the run). Storable is the
// one check every listed row passes before it can reach the ledger.
//
// The byte caps below are declared by the seam, derived from what the adapters
// can emit and not copied from any one of them. A cap under an adapter's own
// bound refuses rows that adapter lists, and a board whose adapter is in
// failsBoardOnBadRow then fails whole on one such row. So each cap is the
// largest legitimate adapter output, with headroom:
//   - MaxLocation: the largest place count any adapter allows is 50 (ibm
//     maxCities, talentbrew maxPlaces; workday and oraclehcm allow 20), each
//     place up to RealisticPlaceBytes, joined by "; ", rounded up to 32 KiB.
//     Adapters whose join has no count bound (apple, google, amazon, deshaw,
//     eightfold, jibe, rippling, atlassian, smartrecruiters, workable) are
//     limited only by the response body cap (srcutil.MaxBody); no realistic
//     listing reaches 32 KiB of places, and one that does is refused rather
//     than stored cut.
//   - MaxDepartment: up to six RealisticPlaceBytes elements (jibe and tiktok
//     join several categories), 4 KiB.
//   - MaxCompany: a tenant or hiring name, a few elements, 1 KiB.
//   - MaxKeyPart, MaxKey: an id is a short token; page and other keys hash
//     their URL to 24 hex bytes, so a URL's length never reaches the key. A
//     key has at most three parts after its prefix (workday, oraclehcm): three
//     parts at MaxKeyPart is 3 KiB, so MaxKey is 4 KiB.
//
// Worst case, one JSON-escaped byte costs six (a control character), so the
// caps stay under the ledger's line limit with room for the event envelope,
// annotations and the form summary: 6 * (MaxCompany + MaxLocation +
// MaxDepartment + MaxURL + MaxApplyURL + MaxTitle + 2*MaxKey (the event's key
// and the posting's) + MaxDescription) is about 0.66 MiB against the ledger's
// 1 MiB line. MaxRecordBytes then checks the real encoded size, which also
// covers fields that have no cap of their own (the form summary), and refuses a
// row of all-control-character fields before it nears the line limit.
const (
	// RealisticPlaceBytes is the longest single place (or other list element)
	// a listing realistically carries: 200 runes of a three-byte script. The
	// field caps below are derived from it and from the largest element count
	// any adapter allows; each adapter's package tests assert its own maximum
	// output at this element size passes Storable.
	RealisticPlaceBytes = 600

	// MaxCompany is the byte cap on Posting.Company.
	MaxCompany = 1 << 10
	// MaxLocation is the byte cap on Posting.Location.
	MaxLocation = 32 << 10
	// MaxDepartment is the byte cap on Posting.Department.
	MaxDepartment = 4 << 10
	// MaxURL is the byte cap on Posting.URL and on Posting.ApplyURL.
	MaxURL = 2048
	// MaxKeyPart is the byte cap on each ":"-separated part of a posting key
	// after the ATS prefix.
	MaxKeyPart = 1 << 10
	// MaxKey is the byte cap on a whole posting key.
	MaxKey = 4 << 10
	// MaxRecordBytes is the cap on the JSON encoding of one posting. The ledger
	// refuses a line past its own limit (1 MiB, event envelope included); this
	// leaves half of that for the envelope, annotations and reason.
	MaxRecordBytes = 512 << 10

	// minYear and maxYear bound a time a posting may carry (RFC 3339 and the
	// ledger's JSON write years 0..9999; year 0 is excluded as a sentinel).
	minYear = 1
	maxYear = 9999
)

// fieldCaps is the table Storable enforces on the text fields.
func fieldCaps(p posting.Posting) []struct {
	name  string
	value string
	max   int
} {
	return []struct {
		name  string
		value string
		max   int
	}{
		{"company", p.Company, MaxCompany},
		{"location", p.Location, MaxLocation},
		{"department", p.Department, MaxDepartment},
		{"url", p.URL, MaxURL},
		{"apply_url", p.ApplyURL, MaxURL},
		{"description", p.Description, MaxDescription},
	}
}

// clampTime returns t, or the zero time (the date is unknown) when t is
// outside years 1..9999 in UTC or in its own zone.
func clampTime(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	for _, y := range [2]int{t.Year(), t.UTC().Year()} {
		if y < minYear || y > maxYear {
			return time.Time{}
		}
	}
	return t
}

// Storable returns p made fit to store, or an error naming the first field
// that cannot be made so. It changes only what has one obvious safe value:
// the title is normalised with Title, and a time outside years 1..9999 becomes
// the zero time. It never truncates any other field; an oversized field
// refuses the row, because a cut URL or location is a wrong one. The error
// names the field and its length, never the value.
func Storable(b sources.Board, p posting.Posting) (posting.Posting, error) {
	p.Title = Title(p.Title)
	if p.Title == "" {
		return p, fmt.Errorf("sources: title is empty")
	}
	p.PostedAt = clampTime(p.PostedAt)
	p.UpdatedAt = clampTime(p.UpdatedAt)
	p.SeenAt = clampTime(p.SeenAt)

	if err := checkKey(p.Key); err != nil {
		return p, err
	}
	for _, f := range fieldCaps(p) {
		if len(f.value) > f.max {
			return p, fmt.Errorf("sources: %s is %d bytes, over the %d allowed", f.name, len(f.value), f.max)
		}
	}
	if err := CheckURL("url", p.URL); err != nil {
		return p, err
	}
	if err := CheckURL("apply_url", p.ApplyURL); err != nil {
		return p, err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return p, fmt.Errorf("sources: row cannot be encoded: %s", boundedText(err.Error()))
	}
	if len(raw) > MaxRecordBytes {
		return p, fmt.Errorf("sources: row encodes to %d bytes, over the %d allowed", len(raw), MaxRecordBytes)
	}
	return p, nil
}

// checkKey is the ledger's own key test (a non-empty key whose prefix names a
// known ATS) plus the bounds: no empty part and no part or key over its cap.
func checkKey(k posting.Key) error {
	if k == "" || k.ATS() == "" {
		return fmt.Errorf("sources: key has no known ATS prefix")
	}
	if len(k) > MaxKey {
		return fmt.Errorf("sources: key is %d bytes, over the %d allowed", len(k), MaxKey)
	}
	_, rest, _ := strings.Cut(string(k), ":")
	for i, part := range strings.Split(rest, ":") {
		if part == "" {
			return fmt.Errorf("sources: key part %d is empty", i+1)
		}
		if len(part) > MaxKeyPart {
			return fmt.Errorf("sources: key part %d is %d bytes, over the %d allowed", i+1, len(part), MaxKeyPart)
		}
	}
	return nil
}

// maxStorableErrText bounds an error text that came from an encoder.
const maxStorableErrText = 200

func boundedText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= maxStorableErrText {
		return s
	}
	return s[:maxStorableErrText] + "..."
}
