# Similar wordings and near-duplicate postings (embeddings)

One embedder serves both: `internal/semantic` (Tessl EmbeddingGemma 2, in-process on Metal, behind
`-tags tessl`; no sidecar). Both ask it for `KindSimilarity` (the snapshot's STS prompt) and share
one vector cache, `<state dir>/semantic/semantic-vectors.json` (key: model id + kind + sha256 of the
text; versioned, 10,000 entries and 64 MiB at most, oldest evicted, written atomically).

An embedding only **annotates** (Rule 7). Nothing here merges an answer or drops a posting.

## `vani answers similar --json` (the Mac app's merge sheet)

stdin `{"texts":[...], "min_cos"?: number}`, stdout
`{"available":bool, "reason"?, "model"?, "pairs":[{"a","b","cos","tier"}], "truncated"?, "note"?}`.
`a` and `b` index `texts` (`a < b`); the reply never repeats a wording. Send question keys and
labels only, never answer values: the texts are embedded and cached on disk.

- `available:false` always carries a reason and is never "no pairs": a vani built without
  `-tags tessl`, no `semantic.snapshot` (the config also needs `kb.bank`), a model that will not
  load or answers badly, a bound exceeded, or the run out of time (`--timeout`, default 12 s,
  under the app's 15 s). A run that times out keeps its finished batches cached, so the retry is fast.
- Bounds: 4,000 texts; a text over 512 bytes is cut there at a rune boundary (`truncated` counts
  them); 100,000 pairs (raise `min_cos` past that); stdin 8 MiB. Malformed input is
  `{"error":...}` with exit 1.
- A missing `min_cos` is `PossibleCos`.

### Tiers

| tier | rule | measured on the 106-pair fixture |
|---|---|---|
| `likely` | cos >= `LikelyCos` (0.95) and no lexical veto | 15 of 52 same-question pairs, 0 false merges |
| `possible` | cos >= `PossibleCos` (0.88), or `likely` vetoed | 50 of 52 same-question pairs; 30 of 54 different-question pairs also flagged (62% precision) |

`possible` is a list for a person to review, never a merge. `likely` means "no counter-example in
the fixture yet", not a proof: the veto's words were read off the same fixture, so nothing was held
out (`internal/semantic/similar.go` has the numbers and the reasoning).

**Lexical veto** (`semantic.LexicalVeto`, `TierPair`): short labels sit on a high cosine floor and
one-word differences score above many true rewordings ("First name" ~ "Last name" 0.9646). A pair is
held at `possible` when, over the content tokens the extension builds (`extension/src/normalize.ts`
`tokens()`: stopwords including url/link/profile/number, phrase folding, light stemmer):

1. either label has no content token; or
2. one label has a contrast word the other lacks: first/last, current/previous/former,
   now/future, home/work, primary/secondary, legal/preferred, residence/citizenship; or
3. the token sets have the same size and differ by exactly one token on each side.

Two wordings that normalize to the same text are never vetoed. The veto only lowers a tier.
Without it `LikelyCos` would have to be 0.97 (9 of 52). Cost of the veto: 16 true pairs
(Given name ~ First name, Phone number ~ Telephone number, ...) are held at `possible`.

`extension/test/vectors/tokens.json` is generated from the TypeScript and replayed by both
`bun test extension/test/tokens.test.ts` and `go test ./internal/semantic` (TestTokensMatchTheExtensionVectors),
so the Go port cannot drift. After changing `tokens()` or `normalizeLabel`, regenerate with
`UPDATE_VECTORS=1 bun test extension/test/tokens.test.ts`. The Go port approximates JavaScript's NFKC
for full-width forms, no-break/ideographic spaces, f-ligatures and superscript digits only; that gap
can only move a pair between `likely` and `possible`.

### Recalibrating

`extension/test/vectors/label-pairs.json` holds the labelled pairs. With the snapshot present,

```bash
VANI_EMBED_REQUIRE=1 go -C /Users/bharath/Code/apps/Vani test -tags tessl ./internal/semantic -run TestRealLabelCalibration -v
```

prints the precision/recall curve, the zero-false-merge cutoff with and without the veto, and the
holdout split. The test skips visibly without the snapshot (it looks in `VANI_TESSL_SNAPSHOT`, then
`~/.cache/huggingface/hub/models--google--embeddinggemma-2/snapshots/*`) and fails under
`VANI_EMBED_REQUIRE=1`. It fails if any pair at `likely` is a false merge or the recorded maxima drift.

## Near-duplicate postings: the cosine tiebreaker

`pipeline.NearDupOptions.Similarity` is `func(ctx, a, b string) (cos float64, ok bool, err error)`.
The stage calls it only for candidate pairs (shingle overlap >= `NearDupCandidate`) and records the
reading as `dup.cosine`; no status depends on it. `a` and `b` are
`srcutil.SourceText(posting.Description)`, the descriptions alone.

`semantic.NewSimilarity(emb, cache)` embeds the first 1 KB of each description.
`semantic.NewTitledSimilarity(emb, cache, titles)` embeds `title + "\n" + first 1 KB` per
`semantic.DupText`, taking each title from a `*semantic.TitleIndex` that maps a description to its
title (a description two postings carry under different titles is ambiguous and gets none; an
unknown one is embedded without a title). Both are `NearDupOptions.Similarity`-shaped.

### Constructing it in `cmd/vani/wire.go` (after this branch and the near-dup stage are merged)

Config: `semantic.snapshot` and `kb.bank` must both be set (config validation requires the pair),
and the binary must be built with `-tags tessl` (see README, "Knowledge-base matching").

```go
// nearDupSimilarity returns the stage's Similarity option and a finish func.
// With no snapshot, an unloadable model or an unusable cache it returns a nil
// option: the stage then runs on shingles alone (NearDupReport.SimilarityConfigured
// is false and no dup.cosine annotation is written).
func (w *wired) nearDupSimilarity(ctx context.Context) (sim func(context.Context, string, string) (float64, bool, error), finish func()) {
	finish = func() {}
	if w.cfg.Semantic.Snapshot == "" {
		return nil, finish
	}
	emb, closeEmb, err := openEmbedder(ctx, w.cfg) // semanticwire.go; ErrUnavailable without -tags tessl
	if err != nil {
		w.warnf("near-duplicate cosine off: %s", oneLine(err.Error(), 200))
		return nil, finish
	}
	dir := semanticDir(w.stateDir)
	cache, err := semantic.OpenVectorCache(dir, 0)
	if errors.Is(err, semantic.ErrCache) { // a cache holds nothing the model cannot recompute
		if semantic.ResetVectorCache(dir) == nil {
			cache, err = semantic.OpenVectorCache(dir, 0)
		}
	}
	if err != nil {
		cache = nil // no caching this run; the cosine still works
		w.warnf("near-duplicate vector cache off: %s", oneLine(err.Error(), 200))
	}
	titles := semantic.NewTitleIndex()
	for _, r := range w.led.All() {
		titles.Add(r.Posting.Title, srcutil.SourceText(r.Posting.Description)) // the same text the stage compares
	}
	return semantic.NewTitledSimilarity(emb, cache, titles), func() {
		if cache != nil {
			if err := cache.Save(); err != nil {
				w.warnf("near-duplicate vector cache not saved: %s", oneLine(err.Error(), 200))
			}
		}
		_ = closeEmb()
	}
}
```

and at the existing call (`wire.go`, `pipeline.AnnotateNearDuplicates(... NearDupOptions{Protected: protect})`):

```go
sim, finish := w.nearDupSimilarity(ctx)
nd, err := pipeline.AnnotateNearDuplicates(ctx, w.led, time.Now(), pipeline.NearDupOptions{Protected: protect, Similarity: sim})
finish() // saves the vector cache once; never inside the stage
```

What to expect: the model loads once per run (1-2.5 s, outside the stage's 2 s deadline); a cold
cache embeds inside that deadline, so many new candidate pairs can end the stage `Partial` and show
`SimilarityErrors` (timeouts), and the next run, finding the finished vectors cached, goes further. When the
embedder is unavailable the stage is exactly the shingle stage it was: the unavailability is a
warning line, never a cosine of 0 and never a failed run. Descriptions are never logged.
