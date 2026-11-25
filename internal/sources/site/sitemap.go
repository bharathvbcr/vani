package site

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bharathvbcr/Vani/internal/egress"
)

const maxSitemapIndexChildren = 1000

var sitemapPriorityKeywords = []string{
	"job", "career", "position", "opening", "recruit", "talent", "work",
}

type sitemapResult struct {
	urls         []discoveredURL
	sitemapsRead int
	capped       bool
	notes        []string
}

type queuedSitemap struct {
	url   string
	depth int
}

func readSitemaps(
	ctx context.Context,
	d egress.Doer,
	domain string,
	timeout time.Duration,
	maxSitemaps int,
	policy robotsPolicy,
	recordDisallowed func(string),
) (sitemapResult, error) {
	var out sitemapResult
	var initial []string
	if len(policy.sitemaps) > 0 {
		initial = prioritizeSitemapURLs(policy.sitemaps)
	} else {
		initial = []string{(&url.URL{Scheme: "https", Host: domain, Path: "/sitemap.xml"}).String()}
	}

	queue := make([]queuedSitemap, 0, len(initial))
	for _, sm := range initial {
		queue = append(queue, queuedSitemap{url: sm, depth: 0})
	}

	fetched := map[string]bool{}
	seenURLs := map[string]bool{}

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return sitemapResult{}, err
		}

		item := queue[0]
		queue = queue[1:]

		if fetched[item.url] {
			continue
		}
		u, err := url.Parse(item.url)
		if err != nil {
			continue
		}
		reqPath := requestPath(u)
		if !policy.allowed(reqPath) {
			recordDisallowed(reqPath)
			continue
		}
		if len(out.urls) >= MaxSitemapEntries {
			out.capped = true
			break
		}
		if out.sitemapsRead >= maxSitemaps {
			out.capped = true
			out.notes = append(out.notes, fmt.Sprintf("sitemap reads capped at %d", maxSitemaps))
			break
		}

		fetched[item.url] = true
		resp, err := fetchSameHost(
			ctx,
			d,
			item.url,
			domain,
			"application/xml, text/xml;q=0.9, */*;q=0.1",
			timeout,
			MaxSitemapBytes,
		)
		if err != nil {
			if ctx.Err() != nil {
				return sitemapResult{}, ctx.Err()
			}
			out.notes = append(out.notes, fmt.Sprintf("sitemap %s refused: %v", item.url, err))
			continue
		}
		if resp.status < 200 || resp.status >= 300 {
			out.notes = append(out.notes, fmt.Sprintf("sitemap %s returned HTTP %d", item.url, resp.status))
			continue
		}
		if len(resp.body) >= 2 && resp.body[0] == 0x1f && resp.body[1] == 0x8b {
			out.notes = append(out.notes, fmt.Sprintf("sitemap %s refused: gzip-compressed", item.url))
			continue
		}

		parsed, err := parseSitemapXML(resp.body, domain, item.depth, MaxSitemapEntries-len(out.urls))
		if err != nil {
			out.notes = append(out.notes, fmt.Sprintf("sitemap %s refused: %v", item.url, err))
			continue
		}
		out.sitemapsRead++
		out.notes = append(out.notes, parsed.notes...)
		if parsed.entriesCapped {
			out.capped = true
		}

		for _, rawLoc := range parsed.urls {
			if len(out.urls) >= MaxSitemapEntries {
				out.capped = true
				break
			}
			if du, ok := newDiscoveredURL(rawLoc, "sitemap", ""); ok && !seenURLs[du.raw] {
				seenURLs[du.raw] = true
				out.urls = append(out.urls, du)
			}
		}

		if len(parsed.childSitemaps) > 0 && item.depth == 0 {
			prioritized := prioritizeSitemapURLs(parsed.childSitemaps)
			childQueue := make([]queuedSitemap, 0, len(prioritized))
			for _, child := range prioritized {
				if !fetched[child] {
					childQueue = append(childQueue, queuedSitemap{url: child, depth: 1})
				}
			}
			// Explore prioritized children ahead of remaining lower-priority entries.
			queue = append(childQueue, queue...)
		}
	}

	return out, nil
}

type parsedSitemap struct {
	urls          []string
	childSitemaps []string
	entriesCapped bool
	notes         []string
}

func parseSitemapXML(raw []byte, domain string, depth int, remaining int) (parsedSitemap, error) {
	dec := xml.NewDecoder(bytes.NewReader(raw))
	var root xml.StartElement
	for {
		tok, err := dec.Token()
		if err != nil {
			return parsedSitemap{}, fmt.Errorf("malformed XML: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			root = se
			break
		}
	}

	switch root.Name.Local {
	case "sitemapindex":
		if depth > 0 {
			return parsedSitemap{}, errors.New("nested sitemapindex exceeds 1-level depth limit")
		}
		return decodeSitemapIndex(dec, domain)
	case "urlset":
		return decodeURLSet(dec, remaining)
	default:
		return parsedSitemap{}, fmt.Errorf("unexpected root element <%s>", root.Name.Local)
	}
}

func decodeSitemapIndex(dec *xml.Decoder, domain string) (parsedSitemap, error) {
	var out parsedSitemap
	seen := map[string]bool{}
	closed := false
	entries := 0

	for !closed {
		tok, err := dec.Token()
		if err != nil {
			return parsedSitemap{}, fmt.Errorf("malformed sitemapindex: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "sitemap" {
				if err := dec.Skip(); err != nil {
					return parsedSitemap{}, fmt.Errorf("malformed sitemapindex: %w", err)
				}
				continue
			}
			var e struct {
				Loc string `xml:"loc"`
			}
			if err := dec.DecodeElement(&e, &t); err != nil {
				return parsedSitemap{}, fmt.Errorf("malformed sitemap entry: %w", err)
			}
			entries++
			if entries > maxSitemapIndexChildren {
				out.entriesCapped = true
				out.notes = append(out.notes, fmt.Sprintf("sitemapindex capped at %d entries", maxSitemapIndexChildren))
				return out, nil
			}
			loc := strings.TrimSpace(e.Loc)
			if loc == "" {
				continue
			}
			if clean, ok := cleanSameDomainSitemapURL(loc, domain); ok {
				if !seen[clean] {
					seen[clean] = true
					out.childSitemaps = append(out.childSitemaps, clean)
				}
			} else {
				out.notes = append(out.notes, fmt.Sprintf("ignored non-same-domain child sitemap %q", truncNote(loc)))
			}
		case xml.EndElement:
			closed = true
		}
	}

	if err := checkTrailingXML(dec); err != nil {
		return parsedSitemap{}, err
	}
	return out, nil
}

func decodeURLSet(dec *xml.Decoder, remaining int) (parsedSitemap, error) {
	var out parsedSitemap
	closed := false

	for !closed {
		tok, err := dec.Token()
		if err != nil {
			return parsedSitemap{}, fmt.Errorf("malformed urlset: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local != "url" {
				if err := dec.Skip(); err != nil {
					return parsedSitemap{}, fmt.Errorf("malformed urlset: %w", err)
				}
				continue
			}
			var e struct {
				Loc string `xml:"loc"`
			}
			if err := dec.DecodeElement(&e, &t); err != nil {
				return parsedSitemap{}, fmt.Errorf("malformed url entry: %w", err)
			}
			loc := strings.TrimSpace(e.Loc)
			if loc == "" {
				continue
			}
			if len(out.urls) >= remaining {
				out.entriesCapped = true
				out.notes = append(out.notes, fmt.Sprintf("sitemap entries capped at %d", MaxSitemapEntries))
				return out, nil
			}
			out.urls = append(out.urls, loc)
		case xml.EndElement:
			closed = true
		}
	}

	if err := checkTrailingXML(dec); err != nil {
		return parsedSitemap{}, err
	}
	return out, nil
}

func checkTrailingXML(dec *xml.Decoder) error {
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("malformed trailing XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.Comment, xml.ProcInst:
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return errors.New("unexpected text after closing sitemap element")
			}
		default:
			return errors.New("unexpected content after closing sitemap element")
		}
	}
}

func prioritizeSitemapURLs(urls []string) []string {
	out := slices.Clone(urls)
	slices.SortStableFunc(out, func(a, b string) int {
		pa := isCareerKeywordURL(a)
		pb := isCareerKeywordURL(b)
		switch {
		case pa && !pb:
			return -1
		case !pa && pb:
			return 1
		default:
			return 0
		}
	})
	return out
}

func isCareerKeywordURL(raw string) bool {
	low := strings.ToLower(raw)
	for _, kw := range sitemapPriorityKeywords {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}

func newDiscoveredURL(raw, source, listingURL string) (discoveredURL, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxURLLen || !utf8.ValidString(raw) ||
		strings.ContainsAny(raw, " \t\r\n") || strings.ContainsFunc(raw, unicode.IsControl) {
		return discoveredURL{}, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || u.User != nil || u.Host == "" {
		return discoveredURL{}, false
	}
	if strings.EqualFold(u.Scheme, "http") {
		switch u.Port() {
		case "":
		case "80":
			u.Host = strings.TrimSuffix(u.Host, ":80")
		default:
			return discoveredURL{}, false
		}
		u.Scheme = "https"
	} else if !strings.EqualFold(u.Scheme, "https") {
		return discoveredURL{}, false
	}
	if u.Port() != "" {
		return discoveredURL{}, false
	}
	u.Scheme = "https"
	u.Host = strings.ToLower(u.Hostname())
	u.Fragment, u.RawFragment = "", ""
	if u.RawQuery != "" {
		q := u.Query()
		changed := false
		for k := range q {
			if strings.HasPrefix(strings.ToLower(k), "utm_") {
				q.Del(k)
				changed = true
			}
		}
		if changed {
			u.RawQuery = q.Encode()
		}
	}
	if u.Host == "" {
		return discoveredURL{}, false
	}
	return discoveredURL{
		raw:        u.String(),
		parsed:     u,
		source:     source,
		listingURL: listingURL,
	}, true
}
