package semantic

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// profileData is the embedded profile and its calibration.
type profileData struct {
	vecs        [][]float32
	dim         int
	floor, ceil float64
}

// calibrate maps a raw aggregate onto [0,1] between the floor and ceiling.
func (pd *profileData) calibrate(raw float64) float64 {
	s := (raw - pd.floor) / (pd.ceil - pd.floor)
	switch {
	case s != s || s < 0: // NaN or below the floor
		return 0
	case s > 1:
		return 1
	}
	return s
}

// Matcher scores postings against a Profile. Warm embeds; Lookup only reads
// the cache. Both are safe for concurrent use; concurrent Warms run one after
// another.
type Matcher struct {
	e     Embedder
	p     Profile
	model string
	hash  string
	dir   string

	warmMu sync.Mutex   // one Warm at a time; guards pd
	pd     *profileData // nil until embedded or loaded

	mu         sync.RWMutex // guards sf and maxEntries
	sf         *scoreFile
	maxEntries int
}

// NewMatcher opens (or starts) the cache in cacheDir for this profile and
// embedder. A cache written for another profile hash or model is ignored and
// replaced on the next save; a malformed or oversized cache file is an
// ErrCache error and the caller decides what to do. It does not call the
// Embedder.
func NewMatcher(e Embedder, p Profile, cacheDir string) (*Matcher, error) {
	if e == nil {
		return nil, errors.New("semantic: nil Embedder")
	}
	if len(p.Chunks) == 0 || strings.TrimSpace(p.Target) == "" {
		return nil, fmt.Errorf("%w: use ProfileFromBank", ErrEmpty)
	}
	if cacheDir == "" {
		return nil, fmt.Errorf("%w: no cache directory", ErrCache)
	}
	model := e.Model()
	if model == "" {
		return nil, errors.New("semantic: Embedder.Model is empty")
	}
	m := &Matcher{e: e, p: p, model: model, hash: p.Hash(), dir: cacheDir, maxEntries: DefaultMaxEntries}
	pd, err := loadProfile(filepath.Join(cacheDir, profileFileName), m.hash, model, len(p.Chunks))
	if err != nil {
		return nil, err
	}
	m.pd = pd
	if m.sf, err = loadScores(filepath.Join(cacheDir, scoresFileName), m.hash, model); err != nil {
		return nil, err
	}
	return m, nil
}

// SetMaxEntries sets how many scored postings the cache keeps (oldest evicted
// first). It applies from the next save.
func (m *Matcher) SetMaxEntries(n int) {
	m.mu.Lock()
	m.maxEntries = max(n, 1)
	m.mu.Unlock()
}

// Lookup returns the cached match for key if it was scored from text with
// this textHash under the current profile and model. It never calls the
// Embedder: a posting that was never warmed is simply not found, which is a
// different fact from a score of 0.
func (m *Matcher) Lookup(key string, textHash string) (Match, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.sf.Entries[key]
	if !ok || e.TextHash != textHash {
		return Match{}, false
	}
	return Match{Score: e.Score, Raw: e.Raw, Evidence: append([]Evidence(nil), e.Evidence...), Model: m.model, Thin: e.Thin}, true
}

// Len is the number of scored postings held.
func (m *Matcher) Len() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sf.Entries)
}

type pending struct {
	doc    Doc
	hash   string
	chunks []Chunk
}

// note records the first problem of a run and classifies it.
func (r *Report) note(err error) {
	if err == nil {
		return
	}
	if r.Err == "" {
		r.Err = err.Error()
	}
	if errors.Is(err, ErrUnavailable) {
		r.Unavailable = true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		r.Canceled = true
	}
}

// Warm embeds and scores up to max postings that lack a score for the current
// (profile hash, model, text hash) and saves them. Embed calls carry at most
// MaxBatch texts. Postings already scored count as Cached and cost nothing,
// even when the embedder is down. Duplicate keys count once (the first wins);
// a posting with no text, or whose vectors are unusable, is Failed, never 0.
//
// The Report always says what happened. The error is nil only when every
// posting was scored, cached, skipped for max, or failed individually; it is
// non-nil when the run stopped early (embedder unavailable, ctx ended, setup
// or save failed), and what was scored before that is saved first.
func (m *Matcher) Warm(ctx context.Context, docs []Doc, max int) (Report, error) {
	m.warmMu.Lock()
	defer m.warmMu.Unlock()
	var rep Report
	max = min(max, MaxWarmDocs)

	var todo []pending
	seen := map[string]bool{}
	for _, d := range docs {
		if d.Key == "" {
			rep.Failed++
			rep.note(errors.New("semantic: a posting has no key"))
			continue
		}
		if seen[d.Key] {
			continue
		}
		seen[d.Key] = true
		th := TextHash(d.Title, d.Description)
		if _, ok := m.Lookup(d.Key, th); ok {
			rep.Cached++
			continue
		}
		chunks := ChunkPosting(d.Title, d.Description)
		if len(chunks) == 0 {
			rep.Failed++
			rep.note(errors.New("semantic: a posting has no text to embed"))
			continue
		}
		if len(todo) >= max {
			rep.Skipped++
			continue
		}
		todo = append(todo, pending{doc: d, hash: th, chunks: chunks})
	}
	if len(todo) == 0 {
		return rep, nil
	}

	pd, err := m.ensureProfile(ctx)
	if err != nil {
		rep.note(err)
		if rep.Canceled {
			rep.Skipped += len(todo)
		} else {
			rep.Failed += len(todo)
		}
		return rep, err
	}

	var runErr error
	groups, consecBad, i := 0, 0, 0
	for i < len(todo) && runErr == nil {
		if err := ctx.Err(); err != nil {
			runErr = err
			break
		}
		j, texts := i, []string(nil)
		for j < len(todo) && (j == i || len(texts)+len(todo[j].chunks) <= MaxBatch) {
			for _, c := range todo[j].chunks {
				texts = append(texts, c.Text)
			}
			j++
		}
		rows, err := callEmbed(ctx, m.e, texts, KindQuery)
		switch {
		case err == nil:
			consecBad = 0
		case errors.Is(err, ErrUnavailable) || ctx.Err() != nil:
			runErr = err
		default:
			rep.Failed += j - i
			rep.note(err)
			i = j
			if consecBad++; consecBad >= maxConsecBad {
				runErr = fmt.Errorf("semantic: stopped after %d failed embed calls: %w", consecBad, err)
			}
			continue
		}
		if runErr != nil {
			break
		}
		off := 0
		for _, p := range todo[i:j] {
			n := len(p.chunks)
			vecs, ok := make([][]float32, 0, n), true
			for _, r := range rows[off : off+n] {
				v, good := validRow(r, pd.dim)
				if !good {
					ok = false
					break
				}
				vecs = append(vecs, v)
			}
			off += n
			if !ok {
				rep.Failed++
				rep.note(fmt.Errorf("%w: a row is the wrong width, non-finite or zero", ErrEmbed))
				continue
			}
			raw, ev, thin := m.rawScore(p.chunks, vecs, pd)
			m.store(p.doc.Key, &entry{TextHash: p.hash, Raw: raw, Score: pd.calibrate(raw), Evidence: ev, Thin: thin})
			rep.Scored++
		}
		i = j
		if groups++; groups%8 == 0 {
			if err := m.save(); err != nil {
				rep.note(err)
				return rep, err
			}
		}
	}

	if runErr != nil {
		rep.note(runErr)
		if rep.Canceled {
			rep.Skipped += len(todo) - i
		} else {
			rep.Failed += len(todo) - i
		}
	}
	if rep.Scored > 0 {
		if err := m.save(); err != nil {
			rep.note(err)
			if runErr == nil {
				runErr = err
			}
		}
	}
	return rep, runErr
}

func (m *Matcher) store(key string, e *entry) {
	m.mu.Lock()
	e.Seq = m.sf.NextSeq
	m.sf.NextSeq++
	m.sf.Entries[key] = e
	m.mu.Unlock()
}

func (m *Matcher) save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return saveScores(filepath.Join(m.dir, scoresFileName), m.sf, m.maxEntries)
}

// ensureProfile embeds the profile chunks and the calibration anchors once
// per (profile hash, model) and caches them. Called with warmMu held.
func (m *Matcher) ensureProfile(ctx context.Context) (*profileData, error) {
	if m.pd != nil {
		return m.pd, nil
	}
	texts := make([]string, len(m.p.Chunks))
	for i, c := range m.p.Chunks {
		texts[i] = c.Text
	}
	vecs, dim, err := embedStrict(ctx, m.e, texts, KindDocument, 0)
	if err != nil {
		return nil, fmt.Errorf("semantic: embedding the profile: %w", err)
	}
	pd := &profileData{vecs: vecs, dim: dim}

	// Floor: the off-domain anchors; ceiling: the on-domain anchor.
	var anchors [][]Chunk
	for _, a := range offDomainAnchors {
		anchors = append(anchors, ChunkPosting(a.title, a.desc))
	}
	onDomain := ChunkPosting(m.p.Target, "")
	if len(onDomain) == 0 {
		onDomain = []Chunk{{Label: "line", Text: truncBytes(m.p.Target, MaxPostingChunkBytes), Weight: 1}}
	}
	anchors = append(anchors, onDomain)
	var flat []string
	for _, a := range anchors {
		for _, c := range a {
			flat = append(flat, c.Text)
		}
	}
	rows, _, err := embedStrict(ctx, m.e, flat, KindQuery, dim)
	if err != nil {
		return nil, fmt.Errorf("semantic: embedding the calibration anchors: %w", err)
	}
	var floorSum float64
	off := 0
	for i, a := range anchors {
		raw, _, _ := m.rawScore(a, rows[off:off+len(a)], pd)
		off += len(a)
		if i < len(offDomainAnchors) {
			floorSum += raw
		} else {
			pd.ceil = raw
		}
	}
	pd.floor = floorSum / float64(len(offDomainAnchors))
	if !finite(pd.floor, pd.ceil) || pd.ceil-pd.floor < minSpread {
		return nil, fmt.Errorf("%w: floor %.3f, ceiling %.3f", ErrCalibration, pd.floor, pd.ceil)
	}
	if err := saveProfile(filepath.Join(m.dir, profileFileName), m.hash, m.model, pd); err != nil {
		return nil, err
	}
	m.pd = pd
	return pd, nil
}

// rawScore is the weight-aware mean of the top m=min(8,n) chunk maxima, where
// a chunk's maximum is its best cosine over the profile chunks, and the
// evidence for the best three chunks.
func (m *Matcher) rawScore(chunks []Chunk, vecs [][]float32, pd *profileData) (float64, []Evidence, bool) {
	type best struct {
		idx   int
		cos   float64
		label string
	}
	bs := make([]best, len(chunks))
	for i := range chunks {
		b := best{idx: i, cos: -2}
		for k, pv := range pd.vecs {
			if c := dot(vecs[i], pv); c > b.cos {
				b.cos, b.label = c, m.p.Chunks[k].Label
			}
		}
		bs[i] = b
	}
	sort.SliceStable(bs, func(i, j int) bool { return bs[i].cos > bs[j].cos })
	// Take the best chunks that say something new: a chunk nearly identical
	// to one already taken (a line repeated, or reworded keyword stuffing)
	// counts once, so repetition cannot fill the top-m mean.
	var taken []best
	for _, b := range bs {
		if len(taken) == topChunks {
			break
		}
		dup := false
		for _, t := range taken {
			if dot(vecs[b.idx], vecs[t.idx]) >= nearDupCos {
				dup = true
				break
			}
		}
		if !dup {
			taken = append(taken, b)
		}
	}
	var num, den float64
	for _, b := range taken {
		w := chunks[b.idx].Weight
		num += w * b.cos
		den += w
	}
	var ev []Evidence
	for _, b := range taken[:min(evidenceN, len(taken))] {
		ev = append(ev, Evidence{Snippet: truncRunes(chunks[b.idx].Text, snippetRunes), Label: b.label, Cosine: b.cos})
	}
	return num / den, ev, len(taken) < minDistinctChunks
}
