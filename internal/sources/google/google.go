// Package google lists postings from Google's careers site
// (www.google.com/about/careers/applications).
//
// The results page is server-rendered and carries every result as JSON in an
// AF_initDataCallback({key: 'ds:1', ...}) block, so one plain GET per search
// term yields the postings with their descriptions; no browser and no detail
// requests. The block is positional and undocumented, so the decoder checks
// the shape of every field it reads and fails the board on any surprise
// rather than guessing.
//
// robots.txt allows the first results page and disallows every "?page=" or
// "&page=" variant (checked 2026-10-19), so each term is read from page 1
// only. A term that matches more jobs than page 1 holds fails the board:
// truncating would let the reconciler mark the unseen postings dead. Narrow
// it with a more specific term or the target_level and location filters.
package google

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/egress"
	"github.com/bharathvbcr/Vani/internal/posting"
	"github.com/bharathvbcr/Vani/internal/sources"
	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

const (
	resultsURL = "https://www.google.com/about/careers/applications/jobs/results"
	// MaxPostings bounds one board: page 1 of each term (20) times the most
	// terms a board may hold.
	MaxPostings = 20 * sources.MaxSearches
)

var (
	// ErrNoResults is a results page without the ds:1 block: a consent page,
	// an error page or a redesign. It is never read as an empty board.
	ErrNoResults = errors.New("google: results page has no ds:1 data block")
	// ErrShape is a ds:1 block whose layout is not the one this decoder knows.
	ErrShape = errors.New("google: unexpected ds:1 layout")
	// ErrTruncated is a term that matches more jobs than page 1 shows.
	ErrTruncated = errors.New("google: search matches more jobs than robots.txt lets Vani read")
)

var jobID = regexp.MustCompile(`^[0-9]{1,24}$`)

type source struct {
	d   egress.Doer
	now func() time.Time
}

// New returns the Google careers source, sending requests through d.
func New(d egress.Doer) sources.Source { return &source{d: d, now: time.Now} }

func (*source) ATS() posting.ATS { return posting.Google }

func (s *source) List(ctx context.Context, b sources.Board) ([]posting.Posting, error) {
	if err := srcutil.CheckBoard(b, posting.Google); err != nil {
		return nil, err
	}
	terms := b.SearchTerms()
	if len(terms) == 0 {
		return nil, fmt.Errorf("google %q: the board needs at least one search", b.Token)
	}
	if len(terms) > sources.MaxSearches {
		return nil, fmt.Errorf("google %q: %d searches, more than %d", b.Token, len(terms), sources.MaxSearches)
	}
	if err := sources.CheckFilters(posting.Google, b.Filters); err != nil {
		return nil, fmt.Errorf("google %q: %w", b.Token, err)
	}
	seen := s.now()
	var out []posting.Posting
	keys := map[posting.Key]bool{}
	for _, term := range terms {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := srcutil.GetBody(ctx, s.d, searchURL(term, b.Filters))
		if err != nil {
			return nil, fmt.Errorf("google %q: search %q: %w", b.Token, term, err)
		}
		jobs, err := decodeResults(page)
		if err != nil {
			return nil, fmt.Errorf("google %q: search %q: %w", b.Token, term, err)
		}
		for i, j := range jobs {
			p, err := j.posting(b, seen)
			if err != nil {
				return nil, fmt.Errorf("google %q: search %q: job %d: %w", b.Token, term, i, err)
			}
			if keys[p.Key] {
				continue
			}
			keys[p.Key] = true
			out = append(out, p)
		}
		if len(out) > MaxPostings {
			return nil, fmt.Errorf("google %q: %w: more than %d postings", b.Token, srcutil.ErrTooManyPostings, MaxPostings)
		}
	}
	return out, nil
}

// searchURL is page 1 of a term's results with the filters applied, in a
// stable order. It never carries a page parameter (robots.txt).
func searchURL(term string, filters map[string][]string) string {
	q := url.Values{}
	q.Set("q", term)
	for k, vs := range filters {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	return resultsURL + "?" + q.Encode() // Encode sorts by key
}

// job is one decoded result.
type job struct {
	id, title, team  string
	locations        []string
	about, duties, q string // HTML
	published        time.Time
	remote           bool // fLocationNote names remote locations
}

const dsMarker = "AF_initDataCallback({key: 'ds:1'"

// decodeResults finds the ds:1 block and decodes its result list. It reads
// one JSON value starting at the block's "data:", so nothing after the value
// (the rest of the script) is guessed at.
func decodeResults(page []byte) ([]job, error) {
	at := bytes.Index(page, []byte(dsMarker))
	if at < 0 {
		return nil, ErrNoResults
	}
	rest := page[at+len(dsMarker):]
	d := bytes.Index(rest, []byte("data:"))
	if d < 0 || d > 200 { // the key, then a short hash, then data
		return nil, fmt.Errorf("%w: no data after the ds:1 key", ErrShape)
	}
	dec := json.NewDecoder(bytes.NewReader(rest[d+len("data:"):]))
	dec.UseNumber()
	var top []json.RawMessage
	if err := dec.Decode(&top); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrShape, err)
	}
	if len(top) < 3 {
		return nil, fmt.Errorf("%w: data has %d elements, want at least 3", ErrShape, len(top))
	}
	var rows []json.RawMessage
	if string(top[0]) == "null" {
		rows = nil
	} else if err := json.Unmarshal(top[0], &rows); err != nil {
		return nil, fmt.Errorf("%w: results are not a list", ErrShape)
	}
	var total int
	if err := json.Unmarshal(top[2], &total); err != nil || total < 0 {
		return nil, fmt.Errorf("%w: total is not a count", ErrShape)
	}
	if total > len(rows) {
		return nil, fmt.Errorf("%w: %d jobs match, page 1 holds %d; use a narrower term or the target_level/location filters",
			ErrTruncated, total, len(rows))
	}
	jobs := make([]job, 0, len(rows))
	for i, r := range rows {
		j, err := decodeJob(r)
		if err != nil {
			return nil, fmt.Errorf("%w: result %d: %v", ErrShape, i, err)
		}
		jobs = append(jobs, j)
	}
	return jobs, nil
}

// Field positions in one ds:1 result, as observed on 2026-10-19.
const (
	fID           = 0
	fTitle        = 1
	fDuties       = 3  // [null, html]
	fQualif       = 4  // [null, html]: minimum and preferred
	fTeam         = 7  // "Google", "DeepMind", "YouTube", ...
	fLocations    = 9  // [[name, [address...], city, zip, region, country], ...]
	fAbout        = 10 // [null, html]
	minFieldCount = 11
	// fPublished is [unix seconds, nanos]: the time the site's own
	// sort_by=date orders by (2026-10-19: page 1 of "machine learning" in
	// the US sorted by date ran strictly newest-first by this field). Field
	// 12 is earlier, the creation time, and can be months older for a role
	// that went live today; field 14 trails this one by about a second.
	// Whether an edit moves this field is not known.
	fPublished = 13
	// fLocationNote is [null, html]: empty, the offices an applicant may
	// pick from, or for a remote role "<b>Remote location(s): Colorado,
	// USA.</b>" (2026-10-20: all 3 has_remote=true results in the US said
	// so, none of 92 other results did; their fLocations name only states).
	fLocationNote = 18
)

// remoteNote is the phrase fLocationNote opens a remote role's places with.
var remoteNote = regexp.MustCompile(`(?i)\bremote locations?:`)

// Bounds on a published time: anything outside is a misread field.
var (
	minPublished = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	maxPublished = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
)

func decodeJob(raw json.RawMessage) (job, error) {
	var f []json.RawMessage
	if err := json.Unmarshal(raw, &f); err != nil {
		return job{}, errors.New("not a list")
	}
	if len(f) < minFieldCount {
		return job{}, fmt.Errorf("%d fields, want at least %d", len(f), minFieldCount)
	}
	var j job
	if err := json.Unmarshal(f[fID], &j.id); err != nil || !jobID.MatchString(j.id) {
		return job{}, fmt.Errorf("field %d is not a numeric job id", fID)
	}
	var err error
	if j.title, err = plain(f[fTitle]); err != nil || j.title == "" {
		return job{}, fmt.Errorf("field %d is not a title", fTitle)
	}
	if j.team, err = plain(f[fTeam]); err != nil {
		return job{}, fmt.Errorf("field %d: %v", fTeam, err)
	}
	for _, x := range []struct {
		i   int
		dst *string
	}{{fDuties, &j.duties}, {fQualif, &j.q}, {fAbout, &j.about}} {
		if *x.dst, err = htmlPair(f[x.i]); err != nil {
			return job{}, fmt.Errorf("field %d: %v", x.i, err)
		}
	}
	if j.locations, err = locationNames(f[fLocations]); err != nil {
		return job{}, fmt.Errorf("field %d: %v", fLocations, err)
	}
	if len(f) > fPublished {
		if j.published, err = unixPair(f[fPublished]); err != nil {
			return job{}, fmt.Errorf("field %d: %v", fPublished, err)
		}
	}
	if len(f) > fLocationNote {
		note, err := htmlPair(f[fLocationNote])
		if err != nil {
			return job{}, fmt.Errorf("field %d: %v", fLocationNote, err)
		}
		j.remote = remoteNote.MatchString(htmltext.Text(note, maxNote))
	}
	return j, nil
}

// maxNote bounds the location note read for the remote phrase; live notes
// run to a few hundred bytes.
const maxNote = 4 << 10

// unixPair reads [seconds, nanos] (or null, or [seconds]) as a UTC time; null
// is no time. A time before 2000 or after 2100 is a misread field.
func unixPair(raw json.RawMessage) (time.Time, error) {
	if string(raw) == "null" {
		return time.Time{}, nil
	}
	var p []int64
	if err := json.Unmarshal(raw, &p); err != nil || len(p) == 0 || len(p) > 2 {
		return time.Time{}, errors.New("not a [seconds, nanos] pair")
	}
	var ns int64
	if len(p) == 2 {
		ns = p[1]
	}
	if ns < 0 || ns >= int64(time.Second) {
		return time.Time{}, errors.New("nanos out of range")
	}
	t := time.Unix(p[0], ns).UTC()
	if t.Before(minPublished) || !t.Before(maxPublished) {
		return time.Time{}, fmt.Errorf("time %s is out of range", t.Format(time.RFC3339))
	}
	return t, nil
}

func optString(raw json.RawMessage) (string, error) {
	if string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", errors.New("not a string")
	}
	return strings.TrimSpace(s), nil
}

// plain reads a text field. Google HTML-escapes them ("Ads Finance P&amp;L").
func plain(raw json.RawMessage) (string, error) {
	s, err := optString(raw)
	return strings.TrimSpace(html.UnescapeString(s)), err
}

// htmlPair reads [null, "<html>"] (or null).
func htmlPair(raw json.RawMessage) (string, error) {
	if string(raw) == "null" {
		return "", nil
	}
	var p []json.RawMessage
	if err := json.Unmarshal(raw, &p); err != nil || len(p) < 2 {
		return "", errors.New("not a [null, html] pair")
	}
	return optString(p[1])
}

func locationNames(raw json.RawMessage) ([]string, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	var locs [][]json.RawMessage
	if err := json.Unmarshal(raw, &locs); err != nil {
		return nil, errors.New("not a list of locations")
	}
	var out []string
	for _, l := range locs {
		if len(l) == 0 {
			return nil, errors.New("empty location")
		}
		name, err := plain(l[0])
		if err != nil {
			return nil, fmt.Errorf("location name: %v", err)
		}
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out, nil
}

func (j job) posting(b sources.Board, seen time.Time) (posting.Posting, error) {
	key, err := posting.GoogleKey(b.Token, j.id)
	if err != nil {
		return posting.Posting{}, err
	}
	company := b.Company
	if company == "" {
		company = "Google"
	}
	var desc []string
	for _, part := range []string{j.about, j.duties, j.q} {
		if strings.TrimSpace(part) != "" {
			desc = append(desc, part)
		}
	}
	p := posting.Posting{
		Key:        key,
		ATS:        posting.Google,
		Company:    company,
		Title:      srcutil.Title(j.title),
		Location:   strings.Join(j.locations, "; "),
		Department: j.team,
		// The bare id URL serves the posting (checked 2026-10-19); the slug
		// in the site's own links is cosmetic.
		URL:         resultsURL + "/" + j.id,
		Description: htmltext.Text(strings.Join(desc, "<p></p>"), srcutil.MaxDescription),
		PostedAt:    j.published,
		SeenAt:      seen,
		Remote:      j.remote,
	}
	for _, l := range j.locations {
		if strings.Contains(strings.ToLower(l), "remote") {
			p.Remote = true
		}
	}
	return p, nil
}
