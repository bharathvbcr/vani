package page

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/posting"
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

// maxJSONLDParseDepth bounds the decoder's recursion. The walk cap is lower:
// a chosen node's location tree sits inside the node, so parsing stops later.
const maxJSONLDParseDepth = 64

// findJobPosting chooses the JobPosting for pageURL, which is already in
// posting.CanonicalURL form (empty matches nothing). A block that is not valid
// JSON is skipped: the page still loaded, so the caller falls back to the page
// title rather than failing the board.
//
// Among nodes whose url or sameAs is this page, the first unexpired one in
// document order is used. When every such node is expired, that expired
// posting is returned so the caller can omit the page. When none name this
// page, a single JobPosting that has neither url nor sameAs is used, and
// anything else is left unread so a sibling's title or expiry cannot apply.
func findJobPosting(pageURL string, blocks []string, now time.Time) (jobPosting, bool) {
	var matched, bare []map[string]any
	others := 0
	for _, raw := range blocks {
		v, err := unmarshalOrdered(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		var nodes []map[string]any
		collectJobPostings(v, 0, &nodes)
		for _, n := range nodes {
			refs := postingRefs(n)
			if len(refs) == 0 {
				bare = append(bare, n)
				continue
			}
			if refsMatch(pageURL, refs) {
				matched = append(matched, n)
				continue
			}
			others++
		}
	}
	if len(matched) > 0 {
		first := readJobPosting(matched[0])
		for _, n := range matched {
			jp := readJobPosting(n)
			if !jp.expired(now) {
				return jp, true
			}
		}
		return first, true
	}
	if others == 0 && len(bare) == 1 {
		return readJobPosting(bare[0]), true
	}
	return jobPosting{}, false
}

// ordMap is a JSON object whose keys stay in document order. encoding/json's
// map[string]any does not.
type ordMap struct {
	keys []string
	vals map[string]any
}

func unmarshalOrdered(raw string) (any, error) {
	if raw == "" {
		return nil, errors.New("page: json-ld: empty")
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	v, err := parseJSONValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		if err == nil {
			return nil, errors.New("page: json-ld: trailing value")
		}
		return nil, err
	}
	return v, nil
}

func parseJSONValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxJSONLDParseDepth {
		return nil, errors.New("page: json-ld: nested too deep")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch d {
	case '{':
		return parseJSONObject(dec, depth)
	case '[':
		return parseJSONArray(dec, depth)
	default:
		return nil, errors.New("page: json-ld: bad delimiter")
	}
}

func parseJSONObject(dec *json.Decoder, depth int) (*ordMap, error) {
	m := &ordMap{vals: make(map[string]any)}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("page: json-ld: object key")
		}
		val, err := parseJSONValue(dec, depth+1)
		if err != nil {
			return nil, err
		}
		if _, exists := m.vals[key]; !exists {
			m.keys = append(m.keys, key)
		}
		m.vals[key] = val
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim('}') {
		return nil, errors.New("page: json-ld: object end")
	}
	return m, nil
}

func parseJSONArray(dec *json.Decoder, depth int) ([]any, error) {
	var arr []any
	for dec.More() {
		val, err := parseJSONValue(dec, depth+1)
		if err != nil {
			return nil, err
		}
		arr = append(arr, val)
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if tok != json.Delim(']') {
		return nil, errors.New("page: json-ld: array end")
	}
	return arr, nil
}

// collectJobPostings appends every JobPosting no deeper than the walk cap,
// in document order. Arrays, @graph, mainEntity and other nested objects are
// walked; @context is not. Depth 0 is the root, and a node deeper than
// maxJSONLDDepth is unread.
func collectJobPostings(v any, depth int, out *[]map[string]any) {
	if depth > maxJSONLDDepth || out == nil {
		return
	}
	switch n := v.(type) {
	case []any:
		for _, e := range n {
			collectJobPostings(e, depth+1, out)
		}
	case *ordMap:
		if hasType(n.vals["@type"], "JobPosting") {
			*out = append(*out, n.plain())
		}
		for _, k := range n.keys {
			if k == "@context" {
				continue
			}
			switch n.vals[k].(type) {
			case *ordMap, []any:
				collectJobPostings(n.vals[k], depth+1, out)
			}
		}
	}
}

func (m *ordMap) plain() map[string]any {
	out := make(map[string]any, len(m.vals))
	for k, v := range m.vals {
		out[k] = plainJSON(v)
	}
	return out
}

func plainJSON(v any) any {
	switch t := v.(type) {
	case *ordMap:
		return t.plain()
	case []any:
		arr := make([]any, len(t))
		for i, e := range t {
			arr[i] = plainJSON(e)
		}
		return arr
	default:
		return v
	}
}

// postingRefs is the non-empty url and sameAs strings on a JobPosting.
// A list contributes its strings. Anything else is not a page name.
func postingRefs(m map[string]any) []string {
	var out []string
	for _, key := range []string{"url", "sameAs"} {
		out = appendStringRefs(out, m[key])
	}
	return out
}

func appendStringRefs(out []string, v any) []string {
	switch t := v.(type) {
	case string:
		if s := strings.TrimSpace(t); s != "" {
			out = append(out, s)
		}
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func refsMatch(pageURL string, refs []string) bool {
	for _, ref := range refs {
		if samePage(pageURL, ref) {
			return true
		}
	}
	return false
}

// samePage reports whether raw names pageCanon. pageCanon is the canonical
// page URL. raw is compared through upgradeHTTP and posting.CanonicalURL, so
// the differences that package already folds (http on the default port, a
// trailing slash, a fragment, utm query keys, host case) match. A path prefix
// does not: the canonical strings must be equal.
func samePage(pageCanon, raw string) bool {
	if pageCanon == "" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	upgradeHTTP(u)
	canon, err := posting.CanonicalURL(u.String())
	if err != nil {
		return false
	}
	return canon == pageCanon
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
