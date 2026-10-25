package semantic

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// profileData is the embedded profile and its calibration.
type profileData struct {
	vecs        [][]float32
	dim         int
	floor, ceil float64
}

// kneeAt is where score starts bending toward 1 instead of clipping at it.
const kneeAt = 0.8

// score maps a posting's raw aggregate over n distinct chunks onto [0,1].
//
// Above the ceiling anchor it bends smoothly toward 1 rather than clipping, so
// postings closer to the owner's work than the anchor still order (37 of 1682 live
// postings sat at exactly 1.0, the top of the list among them). Evidence counts:
// a posting that offers fewer than topChunks distinct chunks is discounted, in
// proportion to how few, so two good lines do not read as a posting that is all
// about the owner's work. Full evidence is not discounted, so a normal posting
// scores as it did.
func (pd *profileData) score(raw float64, n int) float64 {
	if n <= 0 || math.IsNaN(raw) {
		return 0
	}
	s := (raw - pd.floor) / (pd.ceil - pd.floor)
	if math.IsNaN(s) || s <= 0 {
		return 0
	}
	if s > kneeAt {
		s = kneeAt + (1-kneeAt)*math.Tanh((s-kneeAt)/(1-kneeAt))
	}
	if n < topChunks {
		const k = 2.0
		s *= (float64(n) / (float64(n) + k)) / (float64(topChunks) / (topChunks + k))
	}
	return min(max(s, 0), 1)
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

	warmMu   sync.Mutex    // one Warm at a time; guards pd
	pd       *profileData  // nil until embedded or loaded
	lockWait time.Duration // how long Warm waits for the cache lock; 0 is DefaultLockWait

	vs          *chunkStore // chunk-vector log index; guarded by warmMu and the cache lock
	vecMax      int64       // chunk-vector log bound; 0 is MaxVectorBytes
	rescoreTick func(n int) // test seam: called after each rescored posting with the running count

	mu         sync.RWMutex // guards sf, maxEntries and vecPrio
	sf         *scoreFile
	maxEntries int
	vecPrio    map[string]float64 // see SetVectorPriority
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
	h      [16]byte // hash as a vector-log key; valid when hok
	hok    bool
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
// A posting that lacks a score but whose chunk vectors are in the chunk-vector
// log (a profile change dropped its score, or another key held the same text)
// is rescored from them with no embedding. Those are Rescored, are not limited
// by max, and run before any embedding; the vectors of every posting that is
// embedded are appended to the log. Only the profile and the calibration
// anchors still need the model after a profile change.
//
// The Report always says what happened. The error is nil only when every
// posting was scored, cached, rescored, skipped for max, or failed
// individually; it is non-nil when the run stopped early (embedder
// unavailable, ctx ended, setup or save failed), and what was scored before
// that is saved first. A chunk-vector log that cannot be used never stops a
// warm: it is reported (VectorsErr) and the postings are embedded.
func (m *Matcher) Warm(ctx context.Context, docs []Doc, max int) (rep Report, runErr error) {
	m.warmMu.Lock()
	defer m.warmMu.Unlock()
	max = min(max, MaxWarmDocs)

	// The cache lock spans load, embed and save, so a warm in another process
	// neither loses these entries nor has its own overwritten. What it saved
	// since this matcher was opened is read back first.
	wait := m.lockWait
	if wait <= 0 {
		wait = DefaultLockWait
	}
	unlock, lerr := lockCache(ctx, m.dir, wait)
	if lerr == nil {
		defer unlock()
		lerr = m.reload()
	}
	if lerr != nil {
		seen := map[string]bool{}
		for _, d := range docs {
			switch {
			case d.Key == "":
				rep.Failed++
			case !seen[d.Key]:
				seen[d.Key] = true
				rep.Skipped++
			}
		}
		rep.note(lerr)
		return rep, lerr
	}

	var vs *chunkStore
	vsOpened := false
	defer func() { m.finishVectors(&rep, vs) }()
	openVecs := func() *chunkStore {
		if !vsOpened {
			vsOpened = true
			vs = m.openVectors(&rep, docs)
		}
		return vs
	}

	var todo, rescore []pending
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
		p := pending{doc: d, hash: th, chunks: chunks}
		p.h, p.hok = parseTextHash(th)
		if s := openVecs(); s != nil && p.hok {
			switch err := s.lookup(p.h, len(chunks)); {
			case err == nil:
				rescore = append(rescore, p)
				continue
			case errors.Is(err, errVecStale):
				rep.VectorsStale++
			}
		}
		if len(todo) >= max {
			rep.Skipped++
			continue
		}
		todo = append(todo, p)
	}
	if len(todo) == 0 && len(rescore) == 0 {
		return rep, nil
	}

	pd, err := m.ensureProfile(ctx)
	if err != nil {
		rep.note(err)
		if rep.Canceled {
			rep.Skipped += len(todo) + len(rescore)
		} else {
			rep.Failed += len(todo) + len(rescore)
		}
		return rep, err
	}

	// toEmbed sends a posting whose stored vectors cannot be used to the embed
	// list, within max like any other.
	toEmbed := func(p pending) {
		if len(todo) >= max {
			rep.Skipped++
			return
		}
		todo = append(todo, p)
	}

	// Vectors of another width than the model now gives belong to no usable
	// model (the id is meant to change with the width): start the log over.
	if vs != nil && vs.dim != 0 && vs.dim != pd.dim {
		rep.VectorsRebuilt = true
		rep.noteVectors(vecCorrupt("stored vectors are %d wide, the model gives %d", vs.dim, pd.dim))
		if rerr := vs.reset(); rerr != nil {
			rep.noteVectors(rerr)
			vs.close()
			vs = nil
		}
		for _, p := range rescore {
			toEmbed(p)
		}
		rescore = nil
	}

	// Rescore from stored vectors first: no embedding, so no max.
	ri := 0
	for ; ri < len(rescore); ri++ {
		if err := ctx.Err(); err != nil {
			runErr = err
			break
		}
		p := rescore[ri]
		vecs, err := vs.read(p.h, len(p.chunks), pd.dim)
		if err != nil {
			if errors.Is(err, errVecStale) {
				rep.VectorsStale++
			} else {
				rep.VectorsCorrupt++
				rep.noteVectors(err)
				vs.drop(p.h)
			}
			toEmbed(p)
			continue
		}
		raw, ev, taken := m.rawScoreN(p.chunks, vecs, pd, topChunks)
		m.store(p.doc.Key, &entry{TextHash: p.hash, Raw: raw, Score: pd.score(raw, taken), Evidence: ev, Thin: taken < minDistinctChunks})
		rep.Rescored++
		if m.rescoreTick != nil {
			m.rescoreTick(rep.Rescored)
		}
		if rep.Rescored%rescoreSaveEvery == 0 {
			if err := m.save(); err != nil {
				rep.note(err)
				rep.Skipped += len(rescore) - ri - 1 + len(todo)
				return rep, err
			}
		}
	}
	if runErr != nil {
		rep.note(runErr)
		rep.Skipped += len(rescore) - ri + len(todo)
	}
	if rep.Rescored > 0 {
		if err := m.save(); err != nil {
			rep.note(err)
			if runErr == nil {
				runErr = err
				rep.Skipped += len(todo)
			}
		}
	}
	if runErr != nil {
		return rep, runErr
	}

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
		var recs []vecRec
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
			raw, ev, taken := m.rawScoreN(p.chunks, vecs, pd, topChunks)
			m.store(p.doc.Key, &entry{TextHash: p.hash, Raw: raw, Score: pd.score(raw, taken), Evidence: ev, Thin: taken < minDistinctChunks})
			rep.Scored++
			if p.hok {
				recs = append(recs, vecRec{h: p.h, vecs: vecs})
			}
		}
		m.appendVectors(vs, recs, &rep)
		i = j
		if groups++; groups%8 == 0 {
			if err := m.save(); err != nil {
				rep.note(err)
				rep.Skipped += len(todo) - i
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

// reload replaces the in-memory scores with the file's. Called with the cache
// lock and warmMu held.
func (m *Matcher) reload() error {
	sf, err := loadScores(filepath.Join(m.dir, scoresFileName), m.hash, m.model)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.sf = sf
	m.mu.Unlock()
	return nil
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
	if err := m.calibrate(ctx, pd); err != nil {
		return nil, err
	}
	if err := saveProfile(filepath.Join(m.dir, profileFileName), m.hash, m.model, pd); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.pd = pd
	m.mu.Unlock()
	return pd, nil
}

// calibrate sets pd.floor and pd.ceil from the anchors, embedded and scored
// against pd.vecs the way a posting is. It is separate from ensureProfile so a
// check holding profile vectors it did not embed itself (a real-model run over
// a profile cache) can run the production calibration unchanged.
func (m *Matcher) calibrate(ctx context.Context, pd *profileData) error {
	// Floor: the off-domain anchors; ceiling: the on-domain anchor.
	var anchors [][]Chunk
	for _, a := range offDomainAnchors {
		anchors = append(anchors, ChunkPosting(a.title, a.desc))
	}
	anchors = append(anchors, onDomainAnchor(m.p.Target))
	var flat []string
	for _, a := range anchors {
		for _, c := range a {
			flat = append(flat, c.Text)
		}
	}
	rows, _, err := embedStrict(ctx, m.e, flat, KindQuery, pd.dim)
	if err != nil {
		return fmt.Errorf("semantic: embedding the calibration anchors: %w", err)
	}
	var floorSum float64
	off := 0
	for i, a := range anchors {
		if i < len(offDomainAnchors) {
			// Each off-domain anchor is a title and three lines, at most topChunks
			// chunks, so depth topChunks reads all of them (the floor is unchanged
			// by the ceiling's depth rule; TestOffDomainAnchorsAreReadInFull).
			raw, _, _ := m.rawScoreN(a, rows[off:off+len(a)], pd, topChunks)
			floorSum += raw
		} else {
			// The ceiling reads the same top third a posting is read at. Its raw
			// score is used as it is: score()'s evidence discount applies to
			// postings only, and a short target must not lower its own ceiling.
			pd.ceil, _, _ = m.rawScoreN(a, rows[off:off+len(a)], pd, anchorDepth(len(a)))
		}
		off += len(a)
	}
	pd.floor = floorSum / float64(len(offDomainAnchors))
	if !finite(pd.floor, pd.ceil) || pd.ceil-pd.floor < minSpread {
		return fmt.Errorf("%w: floor %.3f, ceiling %.3f", ErrCalibration, pd.floor, pd.ceil)
	}
	return nil
}

// Calibration is the mapping a profile's scores use: the raw cosines that score
// 0 and 1 before the knee, and how many chunks the on-domain ceiling anchor was
// cut into (one means the target summary is a single sentence or too short to
// split, and the ceiling is that one line's score).
type Calibration struct {
	Floor, Ceil  float64
	AnchorChunks int
}

// Calibration reports the profile's calibration. ok is false until the profile
// has been embedded (or loaded from the cache): a Matcher that has not yet
// warmed has no floor or ceiling, which is not the same as a zero one. It
// never waits for a Warm and never calls the Embedder.
func (m *Matcher) Calibration() (c Calibration, ok bool) {
	m.mu.RLock()
	pd := m.pd
	m.mu.RUnlock()
	if pd == nil {
		return Calibration{}, false
	}
	return Calibration{Floor: pd.floor, Ceil: pd.ceil, AnchorChunks: len(onDomainAnchor(m.p.Target))}, true
}

// rawScoreN is the weight-aware mean of the best depth distinct chunk maxima
// (or of all of them when there are fewer), where a chunk's maximum is its best
// cosine over the profile chunks, the evidence for the best three chunks, and
// how many distinct chunks the mean rests on. A posting is read at depth
// topChunks; the on-domain anchor at anchorDepth (anchors.go). A depth below 1
// reads one chunk, never none.
func (m *Matcher) rawScoreN(chunks []Chunk, vecs [][]float32, pd *profileData, depth int) (float64, []Evidence, int) {
	depth = max(depth, 1)
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
		if len(taken) == depth {
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
	return num / den, ev, len(taken)
}
