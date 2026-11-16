package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// kbcheck: knowledge.json must not carry figures that Claims.tex doesn't.
// Every number with a unit (%, ×, ms, µs, s, GB, MB, tok/s, k) in project
// text and ecosystem.measured is compared against all Claims.tex values;
// unmatched ones are drift: register them (with source + check) or reword.

var reKBFig = regexp.MustCompile(`[-−+]?\d[\d,]*(?:\.\d+)?\s?(?:%|×|x\b|ms\b|µs\b|us\b|s\b|GB\b|MB\b|KB\b|tok/s|tokens/s|GFLOP/s|k\b|pp\b)`)

func collectStrings(v any, path string, out map[string][]string) {
	switch t := v.(type) {
	case string:
		out[path] = append(out[path], t)
	case []any:
		for _, x := range t {
			collectStrings(x, path, out)
		}
	case map[string]any:
		for _, k := range sortedKeys(t) {
			collectStrings(t[k], path, out)
		}
	}
}

func cmdKBCheck(a *args) {
	claims := parseClaims()
	var vals []string
	for _, c := range claims {
		vals = append(vals, c.Value)
	}
	var kb map[string]any
	b, err := os.ReadFile(a.get("knowledge", inRoot(cfg.KnowledgeJSON)))
	if err != nil {
		die("%v", err)
	}
	if err := json.Unmarshal(b, &kb); err != nil {
		die("knowledge.json: %v", err)
	}
	texts := map[string][]string{}
	if ps, ok := kb["projects"].([]any); ok {
		for _, p := range ps {
			pm, _ := p.(map[string]any)
			title, _ := pm["title"].(string)
			collectStrings(pm, "project "+title, texts)
		}
	}
	if eco, ok := kb["ecosystem"].(map[string]any); ok {
		collectStrings(eco["measured"], "ecosystem.measured", texts)
	}
	unmatched, total := 0, 0
	for _, where := range sortedKeys(texts) {
		var miss []string
		for _, t := range texts[where] {
			for _, loc := range reKBFig.FindAllStringIndex(t, -1) {
				f := t[loc[0]:loc[1]]
				if loc[0] > 0 && t[loc[0]-1] >= '0' && t[loc[0]-1] <= '9' {
					f = strings.TrimLeft(f, "-−+")
				}
				total++
				ok := false
				for _, v := range vals {
					if containsNumber(v, f) {
						ok = true
						break
					}
				}
				if !ok {
					miss = append(miss, strings.TrimSpace(f))
				}
			}
		}
		if len(miss) > 0 {
			unmatched += len(miss)
			fmt.Printf("%-40s %d unregistered: %s\n", truncate(where, 40), len(miss), strings.Join(uniq(miss), ", "))
		}
	}
	fmt.Printf("\n%d of %d figures in knowledge.json have no matching Claims.tex value\n", unmatched, total)
	if unmatched > 0 && a.bools["strict"] {
		os.Exit(1)
	}
}

// containsNumber: the KB figure's number appears (to its precision) in a claim value.
func containsNumber(value, fig string) bool {
	fn := numbersIn(fig)
	if len(fn) == 0 {
		return true
	}
	f0 := strings.TrimPrefix(fn[0], "-")
	for _, v := range numbersIn(value) {
		v0 := strings.TrimPrefix(v, "-")
		if numEq(fn[0], v, 1) || numEq(v, fn[0], 1) || numEq(f0, v0, 1) || numEq(v0, f0, 1) {
			return true
		}
	}
	return false
}

func uniq(xs []string) []string {
	var out []string
	for _, x := range xs {
		out = appendUniq(out, x)
	}
	return out
}
