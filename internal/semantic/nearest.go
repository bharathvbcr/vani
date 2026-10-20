package semantic

import (
	"context"
	"errors"
	"sort"
	"strings"
)

// MaxNearest bounds how many profile lines Nearest returns.
const MaxNearest = 16

// Neighbor is one line of the knowledge base close to a posting.
type Neighbor struct {
	// Label is the entry the line belongs to (a role or project title).
	Label string
	Text  string
	// Cosine is the best cosine between this line and any chunk of the posting.
	Cosine float64
}

// Nearest returns up to k lines of the knowledge base closest to d, best first:
// each profile line scored by its best cosine over the posting's chunks. Stack
// lines and entry titles are left out (a caller that shows the stack and titles
// does not need them repeated), and so is a line nearly identical to one already
// taken. It embeds d's chunks with the Embedder (one posting, not a batch) and
// reads the profile vectors the matcher already holds, embedding the profile once
// if it has not been. It touches no score cache and fails closed: an embedder
// that is down, an unusable row or a posting with no text is an error, never an
// empty list standing in for "nothing is close".
func (m *Matcher) Nearest(ctx context.Context, d Doc, k int) ([]Neighbor, error) {
	k = min(k, MaxNearest)
	if k <= 0 {
		return nil, nil
	}
	chunks := ChunkPosting(d.Title, d.Description)
	if len(chunks) == 0 {
		return nil, errors.New("semantic: a posting has no text to embed")
	}
	m.warmMu.Lock()
	defer m.warmMu.Unlock()
	pd, err := m.ensureProfile(ctx)
	if err != nil {
		return nil, err
	}
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}
	vecs, _, err := embedStrict(ctx, m.e, texts, KindQuery, pd.dim)
	if err != nil {
		return nil, err
	}
	type scored struct {
		idx int
		cos float64
	}
	var all []scored
	for j, pv := range pd.vecs {
		c := m.p.Chunks[j]
		if c.Label == skillsLabel || c.Text == c.Label {
			continue
		}
		best := -2.0
		for _, v := range vecs {
			if x := dot(v, pv); x > best {
				best = x
			}
		}
		all = append(all, scored{j, best})
	}
	sort.SliceStable(all, func(a, b int) bool { return all[a].cos > all[b].cos })
	var out []Neighbor
	var taken []int
	for _, s := range all {
		if len(out) == k {
			break
		}
		dup := false
		for _, t := range taken {
			if dot(pd.vecs[s.idx], pd.vecs[t]) >= nearDupCos {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		taken = append(taken, s.idx)
		c := m.p.Chunks[s.idx]
		out = append(out, Neighbor{Label: strings.TrimSpace(c.Label), Text: c.Text, Cosine: s.cos})
	}
	return out, nil
}
