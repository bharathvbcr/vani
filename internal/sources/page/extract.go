package page

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/bharathvbcr/Vani/internal/sources"
)

type structuredDataPayload struct {
	JSONLD    []json.RawMessage `json:"jsonLd"`
	Microdata []any             `json:"microdata"`
}

// buildExtractSchema builds the JSON schema string passed to Lightpanda's
// extract tool from the owner's configured CSS selectors, or "" when none are
// set.
func buildExtractSchema(ext *sources.PageExtract) string {
	if ext == nil {
		return ""
	}
	m := map[string]string{}
	if s := strings.TrimSpace(ext.Title); s != "" {
		m["title"] = s
	}
	if s := strings.TrimSpace(ext.Description); s != "" {
		m["description"] = s
	}
	if s := strings.TrimSpace(ext.Location); s != "" {
		m["location"] = s
	}
	if s := strings.TrimSpace(ext.Company); s != "" {
		m["company"] = s
	}
	if len(m) == 0 {
		return ""
	}
	b, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(b)
}

// findStructuredJobPosting parses Lightpanda's structuredData JSON payload,
// trying jsonLd first and then microdata JobPosting items.
func findStructuredJobPosting(pageURL string, raw []byte, now time.Time) (jobPosting, bool) {
	if len(raw) == 0 {
		return jobPosting{}, false
	}
	var p structuredDataPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return jobPosting{}, false
	}
	if len(p.JSONLD) > 0 {
		blocks := make([]string, 0, len(p.JSONLD))
		for _, item := range p.JSONLD {
			var s string
			if err := json.Unmarshal(item, &s); err == nil {
				blocks = append(blocks, s)
			} else if len(item) > 0 {
				blocks = append(blocks, string(item))
			}
		}
		if jp, ok := findJobPosting(pageURL, blocks, now); ok {
			return jp, true
		}
	}
	if len(p.Microdata) == 0 {
		return jobPosting{}, false
	}
	var nodes []map[string]any
	for _, item := range p.Microdata {
		collectMicrodataJobPostings(item, 0, &nodes)
	}
	return selectJobPosting(pageURL, nodes, now)
}

func collectMicrodataJobPostings(v any, depth int, out *[]map[string]any) {
	if depth > maxJSONLDDepth || out == nil {
		return
	}
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	node := microdataItemToMap(m, depth)
	if hasType(node["@type"], "JobPosting") {
		*out = append(*out, node)
	}
	props, _ := m["properties"].(map[string]any)
	for _, rawVal := range props {
		switch vals := rawVal.(type) {
		case []any:
			for _, el := range vals {
				collectMicrodataJobPostings(el, depth+1, out)
			}
		case map[string]any:
			collectMicrodataJobPostings(vals, depth+1, out)
		}
	}
}

func microdataItemToMap(m map[string]any, depth int) map[string]any {
	out := map[string]any{}
	if t, ok := m["type"]; ok {
		out["@type"] = t
	}
	if depth > maxJSONLDParseDepth {
		return out
	}
	props, _ := m["properties"].(map[string]any)
	for k, rawVal := range props {
		out[k] = microdataPropValue(k, rawVal, depth)
	}
	if len(postingRefs(out)) == 0 {
		if id, ok := m["id"].(string); ok {
			id = strings.TrimSpace(id)
			lc := strings.ToLower(id)
			if strings.HasPrefix(lc, "http://") || strings.HasPrefix(lc, "https://") {
				out["url"] = id
			}
		}
	}
	return out
}

func microdataPropValue(key string, rawVal any, depth int) any {
	var items []any
	switch t := rawVal.(type) {
	case []any:
		items = t
	case nil:
		return nil
	default:
		items = []any{t}
	}
	converted := make([]any, 0, len(items))
	for _, el := range items {
		if sub, ok := el.(map[string]any); ok {
			if _, hasProps := sub["properties"]; hasProps || sub["type"] != nil {
				converted = append(converted, microdataItemToMap(sub, depth+1))
				continue
			}
		}
		converted = append(converted, el)
	}
	switch key {
	case "jobLocation", "jobLocationType", "url", "sameAs":
		return converted
	}
	for _, el := range converted {
		if s, ok := el.(string); ok {
			if strings.TrimSpace(s) != "" {
				return s
			}
			continue
		}
		if el != nil {
			return el
		}
	}
	return nil
}

// decodeExtracted decodes a flat JSON object returned by Lightpanda's extract
// tool, ignoring null or empty values.
func decodeExtracted(raw []byte) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			out[k] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
