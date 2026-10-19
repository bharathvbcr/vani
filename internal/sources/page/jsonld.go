package page

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources/htmltext"
	"github.com/bharathvbcr/Vani/internal/sources/srcutil"
)

// jobPosting is the subset of a schema.org JobPosting Vani reads. Every field
// is optional: sites omit them freely and write null for what they lack.
type jobPosting struct {
	title        string
	description  string
	company      string
	location     string
	remote       bool
	postedAt     time.Time
	validThrough time.Time // first instant it is no longer valid; zero when absent or unparseable
}

const maxJSONLDDepth = 6

// findJobPosting returns the first JobPosting in any of the JSON-LD blocks. A
// block that is not valid JSON is skipped: the page still loaded, so the caller
// falls back to the page title rather than failing the board.
func findJobPosting(blocks []string) (jobPosting, bool) {
	for _, raw := range blocks {
		var v any
		if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &v); err != nil {
			continue
		}
		if m, ok := findTyped(v, "JobPosting", 0); ok {
			return readJobPosting(m), true
		}
	}
	return jobPosting{}, false
}

// findTyped walks arrays, @graph and nested objects for a node of the given
// @type, depth-first in document order.
func findTyped(v any, typ string, depth int) (map[string]any, bool) {
	if depth > maxJSONLDDepth {
		return nil, false
	}
	switch n := v.(type) {
	case []any:
		for _, e := range n {
			if m, ok := findTyped(e, typ, depth+1); ok {
				return m, true
			}
		}
	case map[string]any:
		if hasType(n["@type"], typ) {
			return n, true
		}
		if g, ok := n["@graph"]; ok {
			return findTyped(g, typ, depth+1)
		}
	}
	return nil, false
}

// schemaPrefixes are the forms a schema.org type is written in besides its
// bare name: "https://schema.org/JobPosting", "schema:JobPosting" and so on.
var schemaPrefixes = []string{"https://schema.org/", "http://schema.org/", "https://www.schema.org/", "http://www.schema.org/", "schema:"}

func hasType(v any, typ string) bool {
	switch t := v.(type) {
	case string:
		t = strings.TrimSpace(t)
		for _, p := range schemaPrefixes {
			if len(t) > len(p) && strings.EqualFold(t[:len(p)], p) {
				t = t[len(p):]
				break
			}
		}
		return strings.EqualFold(t, typ)
	case []any:
		for _, e := range t {
			if hasType(e, typ) {
				return true
			}
		}
	}
	return false
}

func readJobPosting(m map[string]any) jobPosting {
	var jp jobPosting
	jp.title = htmltext.Text(str(m["title"]), 300)
	jp.description = description(str(m["description"]))
	jp.company = htmltext.Text(nameOf(m["hiringOrganization"]), 200)
	jp.location = locations(m["jobLocation"])
	for _, t := range stringList(m["jobLocationType"]) {
		if strings.EqualFold(t, "TELECOMMUTE") {
			jp.remote = true
		}
	}
	jp.postedAt, _ = parseDate(str(m["datePosted"]))
	vt, dateOnly := parseDate(str(m["validThrough"]))
	if dateOnly {
		vt = vt.AddDate(0, 0, 1) // a bare date is valid through the end of that day
	}
	jp.validThrough = vt
	return jp
}

// description reduces the posting's HTML to text. Some sites escape the markup
// a second time ("&lt;p&gt;"), which would otherwise surface as literal tags.
func description(s string) string {
	if !strings.Contains(s, "<") && strings.Contains(s, "&lt;") {
		s = unescapeOnce(s)
	}
	return htmltext.Text(s, srcutil.MaxDescription)
}

func unescapeOnce(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&", "&quot;", `"`, "&#39;", "'").Replace(s)
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// nameOf reads a string or an object's name (Organization, Country).
func nameOf(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	if m, ok := v.(map[string]any); ok {
		return str(m["name"])
	}
	return ""
}

func stringList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

const maxPlaces = 10

// locations renders jobLocation (one Place, a list of them, or a string) as
// "Locality, Region, Country" per place, joined with "; ".
func locations(v any) string {
	var places []any
	switch t := v.(type) {
	case []any:
		places = t
	case nil:
	default:
		places = []any{t}
	}
	var out []string
	seen := map[string]bool{}
	for i, p := range places {
		if i >= maxPlaces {
			break
		}
		s := place(p)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	return htmltext.Plain(strings.Join(out, "; "), 400)
}

func place(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	addr, ok := m["address"].(map[string]any)
	if !ok {
		if s := str(m["address"]); s != "" {
			return s
		}
		// Some sites give the PostalAddress itself as the jobLocation.
		if s := address(m); s != "" {
			return s
		}
		return str(m["name"])
	}
	return address(addr)
}

// address renders a PostalAddress as "Locality, Region, Country".
func address(addr map[string]any) string {
	var parts []string
	have := map[string]bool{}
	for _, part := range []string{nameOf(addr["addressLocality"]), nameOf(addr["addressRegion"]), nameOf(addr["addressCountry"])} {
		// Many sites already put the country inside the locality ("London,
		// United Kingdom"); do not repeat a whole comma-separated segment.
		if part == "" || have[strings.ToLower(part)] {
			continue
		}
		parts = append(parts, part)
		for _, seg := range strings.Split(part, ",") {
			have[strings.ToLower(strings.TrimSpace(seg))] = true
		}
		have[strings.ToLower(part)] = true
	}
	return strings.Join(parts, ", ")
}

// parseDate accepts RFC 3339, a zoneless date-time and a bare date. dateOnly
// reports the last, whose instant is the start of the day.
func parseDate(s string) (t time.Time, dateOnly bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, layout == "2006-01-02"
		}
	}
	return time.Time{}, false
}

// expired reports whether validThrough is known and past at now. An absent or
// unparseable value keeps the posting.
func (jp jobPosting) expired(now time.Time) bool {
	return !jp.validThrough.IsZero() && jp.validThrough.Before(now)
}
