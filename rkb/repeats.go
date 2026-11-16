package main

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Nothing on a page should be said twice: a figure appears once, a point is
// made once, and the Summary carries points the bullets below don't. These
// checks run on every lint.

var (
	figureRe = regexp.MustCompile(`\d[\d,]*(?:\.\d+)?(?:\s?(?:–|-|to|→)\s?\d[\d,]*(?:\.\d+)?)?\s?(?:%|×|x\b|M\b|k\b|ms\b|µs\b|GB\b|GB/s|tok/s|tokens/s|(?:percentage )?points\b|pp\b)`)
	urlRe    = regexp.MustCompile(`\(?https?://\S+\)?`)
	wordRe   = regexp.MustCompile(`[a-z][a-z0-9'’\-]+`)
	stopW    = map[string]bool{}
)

func init() {
	for _, w := range strings.Fields("the a an of and to in for on with that its it by from at as is are was were which their this than be or into over per each one two his her so") {
		stopW[w] = true
	}
}

type repItem struct {
	Key  string
	Raw  string
	Text string
}

func repeatItems(c *Composed, cf *ClaimsFile) []repItem {
	var out []repItem
	for _, r := range c.allBullets() {
		out = append(out, repItem{r.B.ID, r.B.Text, resolvePlain(r.B.Text, cf.Claims)})
	}
	for _, l := range c.leads() {
		out = append(out, repItem{l.Key, l.Text, resolvePlain(l.Text, cf.Claims)})
	}
	for _, s := range c.Sections {
		for i, l := range s.Lines {
			key := "line:" + slug(s.Title, 20) + ":" + l.Label
			if l.Label == "" {
				key = "line:" + slug(s.Title, 20) + ":" + strconv.Itoa(i+1)
			}
			out = append(out, repItem{key, l.Text, resolvePlain(urlRe.ReplaceAllString(l.Text, ""), cf.Claims)})
		}
		for _, e := range s.Entries {
			out = append(out, repItem{"title:" + e.ID, e.Title, resolvePlain(urlRe.ReplaceAllString(e.Title, ""), cf.Claims)})
		}
	}
	return out
}

func normFigure(s string) string {
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "–")
	return strings.ToLower(s)
}

func checkRepeats(c *Composed, cf *ClaimsFile, R *lintResult) {
	items := repeatItems(c, cf)

	// 1. the same registered figure used twice
	where := map[string][]string{}
	for _, it := range items {
		for _, t := range tokenRe.FindAllStringSubmatch(it.Raw, -1) {
			where[t[1]] = append(where[t[1]], it.Key)
		}
	}
	// 2. the same number reached by two tokens or written into a line
	fig := map[string][]string{}
	for _, it := range items {
		seen := map[string]bool{}
		addFig := func(rawFig string) {
			n := normFigure(rawFig)
			if n != "" && !seen[n] {
				seen[n] = true
				fig[n] = append(fig[n], it.Key)
			}
		}
		for _, t := range tokenRe.FindAllStringSubmatch(it.Raw, -1) {
			if cv := cf.Claims[t[1]]; cv != nil && figureRe.MatchString(cv.Value) {
				addFig(cv.Value)
			}
		}
		nonTok := plainText(tokenRe.ReplaceAllString(it.Raw, " "))
		for _, f := range figureRe.FindAllString(nonTok, -1) {
			addFig(f)
		}
	}
	var keys []string
	for k := range where {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(where[k]) > 1 {
			R.err("%s appears %d times (%s); each figure goes on the page once — give the other spot a different point", k, len(where[k]), strings.Join(where[k], ", "))
		}
	}
	keys = keys[:0]
	for k := range fig {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(fig[k]) > 1 {
			R.err("figure %q is on the page %d times (%s); say it once", k, len(fig[k]), strings.Join(fig[k], ", "))
		}
	}

	// 3. the same phrase (4 content-bearing words) in two places
	ng := map[string]map[string]bool{}
	for _, it := range items {
		w := wordRe.FindAllString(strings.ToLower(it.Text), -1)
		for i := 0; i+4 <= len(w); i++ {
			g := w[i : i+4]
			stops := 0
			for _, x := range g {
				if stopW[x] {
					stops++
				}
			}
			if stops >= 2 {
				continue
			}
			p := strings.Join(g, " ")
			if ng[p] == nil {
				ng[p] = map[string]bool{}
			}
			ng[p][it.Key] = true
		}
	}
	type rep struct {
		p     string
		where []string
	}
	var reps []rep
	pairSeen := map[string]bool{}
	for p, m := range ng {
		if len(m) < 2 {
			continue
		}
		var ws []string
		for k := range m {
			ws = append(ws, k)
		}
		sort.Strings(ws)
		pair := strings.Join(ws, "|")
		if pairSeen[pair] {
			continue // one warning per pair of places
		}
		pairSeen[pair] = true
		reps = append(reps, rep{p, ws})
	}
	sort.Slice(reps, func(i, j int) bool { return reps[i].p < reps[j].p })
	for _, r := range reps {
		R.warn("phrase %q repeats in %s; make the point once", r.p, strings.Join(r.where, ", "))
	}

	// 4. the same opening verb on several work bullets
	verbsAt := map[string][]string{}
	for _, r := range c.allBullets() {
		if r.Ent == nil {
			continue
		}
		f := strings.Fields(resolvePlain(r.B.Text, cf.Claims))
		if len(f) == 0 {
			continue
		}
		v := strings.ToLower(regexp.MustCompile(`[^A-Za-z\-]`).ReplaceAllString(f[0], ""))
		verbsAt[v] = append(verbsAt[v], r.B.ID)
	}
	keys = keys[:0]
	for k := range verbsAt {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, v := range keys {
		if len(verbsAt[v]) > 1 {
			R.warn("%d bullets open with %q (%s); vary the verb to match what each did", len(verbsAt[v]), v, strings.Join(verbsAt[v], ", "))
		}
	}
}
