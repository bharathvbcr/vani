# Do clustered posting embeddings give better audit strata? (stage 5, offline)

Question (the Fable plan of 2026-11-12, `build/fable-plan-2026-11-12-ranking.md` in the main
checkout, which is untracked: decision 5, build-order stage 5, and item 5 of its second review): if
postings are stratified by clustering their embeddings, is the within-stratum variance of a binary
target lower than with the strata Vani uses today? The plan's rule: adopt clusters for audit strata
if the variance falls 25% or more against `internal/jobcategory` at the same cell count; if Lloyd
matches e-means, use a pure-Go Lloyd rather than port e-means. (`docs/RANKING_PLAN.md` does not
mention this experiment.)

This is a measurement. It changes no audit draw, writes no label, spends nothing, and adds no
dependency, no `go.mod` change and no binding. Vani and the Rust clustering engine
(`ojas-cluster`) exchange plain files, so they stay separately versioned.

## Result (live data, one machine, 2026-11-12)

**Reject: clusters do not clear the 25% bar at equal cell count.** Against `internal/jobcategory`
at its 8 cells, spherical e-means at k = 8 lowers the within-stratum variance of Sonnet's
adjacent-or-better reading by a median of **18.6%** over 5 seeds (range 12.2% to 19.2%; n = 2,805
postings). The bar is 25%.

What was measured, and on what:

| | |
|---|---|
| State read | a copy-on-write copy of the live state dir, taken 2026-11-13T03:05:08Z (the live files were only read) |
| Ledger | 71,093 records |
| Chunk-vector log | 7,621 distinct posting texts, model `tessl/embeddinggemma-2@914f7f89...f32/d768`, 768 dims, no torn tail |
| Exported | **8,420 postings** with vectors (7,549 distinct texts: some postings share a text); 62,673 ledger records have none; stale 0, corrupt 0, degenerate 0, text unreadable 0 |
| Target | Sonnet's (`claude-sonnet-5-5`) newest stored `jd_triage` `role_fit` reading is adjacent, partial_fit or strong_fit: **2,883** exported postings have such a reading, 1,538 of them (53.3%) positive |
| Baseline (a) | `internal/jobcategory`: 2,805 of those have a category, 8 cells |
| Baseline (b) | the audit's current strata (score decile of the kept pool, drop class, closed status): 2,856 of those have one, 13 cells |
| Fits | k = 8, 13, 16, 32, 64; 5 seeds each (1 to 5); spherical; same Forgy init per (k, seed) for every algorithm; e-means `nepochs` 2.6; Lloyd 25 iterations, `tol` 0 |

Within-stratum variance of the target (the size-weighted mean of p(1-p) over strata, "plug-in"; the
total variance with no strata is 0.2492 / 0.2491):

| Baseline | n | cells | within variance |
|---|---|---|---|
| jobcategory | 2,805 | 8 | 0.2002 |
| audit strata | 2,856 | 13 | 0.2000 |

Reduction against each baseline, **median over 5 seeds [min, max]**, plug-in figure. The
degrees-of-freedom corrected figure (`pooled`) is within 0.5 points of it at k up to 32 and about 1
point lower at k = 64; `vani strata compare` prints both.

| Against | k | e-means CPU | e-means Metal (F32) | Lloyd | Cells |
|---|---|---|---|---|---|
| jobcategory (8 cells): **the plan's test** | 8 | 18.6% [12.2, 19.2] | 18.6% [12.2, 20.8] | 13.7% [10.8, 15.2] | clusters 7 to 8 non-empty on these postings |
| audit strata (13 cells) | 13 | 20.1% [14.3, 25.4] | 20.9% [13.9, 23.4] | 23.5% [18.0, 28.0] | clusters 10 to 13 non-empty |
| jobcategory, not equal cells | 16 | 22.8% [22.5, 24.7] | 23.2% [21.9, 25.0] | 23.4% [22.6, 25.1] | |
| jobcategory, not equal cells | 32 | 27.9% [25.6, 32.6] | 27.9% [26.7, 32.6] | 28.2% [23.5, 31.2] | |
| jobcategory, not equal cells | 64 | 33.5% [33.1, 34.4] | 33.3% [31.8, 33.6] | 33.3% [32.3, 34.6] | |

Reading it:

- **The decision rests on the first row only.** Rows below it have more cells than the baseline,
  and within-stratum variance falls as cells are added whatever they are; they show the curve, not
  an advantage. The 25% bar is reached only by single seeds at k = 13 (against the categories,
  25.7% for e-means CPU and 28.3% for Lloyd at their best seed) and by medians at k = 32 and above,
  at 4x the categories' cell count.
- **A permutation null sets the noise floor.** Shuffling the target across postings (cell sizes
  kept) puts the reduction in about [-0.4%, +0.3%] at k = 8 and about [+0.6%, +2.3%] at k = 64, so
  the 18.6% is signal, not chance: it is just not enough. Per-run 95% bootstrap intervals over
  the postings are about plus or minus 4 points (for example seed 1, e-means CPU, k = 8:
  19.0% [15.1, 22.8]); the seed-to-seed spread is of the same size.
- **The baselines are weak.** Categories explain 19.7% of the target's variance (1 - 0.2002 /
  0.2492) and 73% of the postings sit in one cell (`other`: 2,053), with a cell of 1 and two of 15.
  Clusters at k = 8 (e-means CPU) explain 29% to 35% across the five seeds (1 - cluster within
  variance / total variance, from `compare.json`). The audit's strata explain 19.7% too, with 13
  cells of which nine hold 29 or fewer postings.
- **Lloyd against e-means is not separated by this data.** At k = 8 the medians are 13.7% (Lloyd)
  and 18.6% (e-means) but the seed ranges overlap ([10.8, 15.2] and [12.2, 19.2]); at k = 13 and
  above Lloyd is level or ahead; the final MSEs are within seed spread at every k (below). The
  tool's operationalisation of "Lloyd matches" (the Lloyd median is no lower than the e-means
  median less the e-means seed range, a tolerance chosen here, not stated in the plan) prints
  `true`. Clusters are not adopted, so nothing follows; if they ever
  were, this data gives no reason to port e-means rather than write a pure-Go Lloyd.

### Fit quality and time, same runs

Spherical objective: mean squared Euclidean distance of each row to its nearest normalised
centroid, in f64, computed by the fit binary itself from the final centroids the same way for every
algorithm. The engines' own labels differ from the nearest-final-centroid labels on 0.0% to 6.7% of
rows (the count is in the summary; most for e-means, 0 to 73 rows for Lloyd). Probably because the
engines label during their last pass, before the final centroids exist (inferred from
`ojas-cluster/README.md`; not checked in its code), which is why the labels used are recomputed.
Mean (min, max) over 5 seeds:

| k | e-means CPU | e-means Metal F32 | Lloyd 25 |
|---|---|---|---|
| 8 | 0.06844 (0.06798, 0.06958) | 0.06844 (0.06798, 0.06958) | 0.06809 (0.06767, 0.06872) |
| 13 | 0.06398 (0.06281, 0.06477) | 0.06378 (0.06283, 0.06431) | 0.06443 (0.06366, 0.06545) |
| 16 | 0.06153 (0.06102, 0.06236) | 0.06145 (0.06093, 0.06236) | 0.06157 (0.06084, 0.06224) |
| 32 | 0.05410 (0.05335, 0.05476) | 0.05442 (0.05361, 0.05499) | 0.05500 (0.05407, 0.05646) |
| 64 | 0.04761 (0.04733, 0.04796) | 0.04771 (0.04713, 0.04819) | 0.04809 (0.04725, 0.04882) |

Fit time in ms, n = 8,420, d = 768, per (k, seed): one untimed warmup, then 5 rounds with the three
algorithms interleaved cyclically; the table is the minimum and median of the 25 timed fits per
algorithm and k. Every timed repeat reproduced the first fit's centroid bits (`repeat_identical`
true in all 75 runs).

| k | e-means CPU min / median | e-means Metal F32 min / median | Lloyd 25 min / median |
|---|---|---|---|
| 8 | 29.4 / 37.8 | 19.7 / 22.2 | 92.8 / 117.3 |
| 13 | 35.2 / 43.0 | 20.4 / 26.3 | 115.3 / 190.2 |
| 16 | 30.5 / 68.9 | 21.5 / 35.3 | 111.0 / 253.5 |
| 32 | 34.4 / 61.3 | 22.2 / 27.4 | 107.4 / 173.3 |
| 64 | 37.7 / 42.1 | 16.5 / 19.5 | 104.6 / 129.8 |

**Not a speed claim.** The machine's load average was 59 to 79 (1 minute) and 95 to 107 (5 and 15
minutes) around the runs (other sessions),
n = 8,420 is far below the committed benchmark's scale (100k x 128 and 65.5k x 512, k = 1,024:
`ojas/bench/results/2026-11-10-emeans`). These
are the numbers the run produced; the repository's benchmark remains the only speedup evidence, and
it was not re-run. No Lloyd/e-means MSE claim beyond "within seed spread at this scale" is made.

### Not measured, and what could change the answer

- **Sonnet's readings are not a random sample.** They exist for 2,883 of the 8,420 exported
  postings, and of the 2,856 with an audit stratum, 2,781 sit in score deciles 7 to 10 (deciles 1
  to 6 hold 41 between them; 34 are closed or dropped). The audit's decile strata are therefore
  nearly empty at the bottom in this population, which handicaps that baseline and is not the
  audit's own pool. A rerun after the teacher has read a wider draw is the fair test of (b).
- **The target is Sonnet's role_fit, not the owner's would_apply.** Adjacent counts as a positive
  (53% positive). The plan itself notes the role_fit rubric rarely says fit and misread the owner's
  yes postings; a stratification good at sorting Sonnet's reading may not sort the owner's labels.
  Nothing here says clusters help the owner's would_apply yield.
- **Equal cells is approximate**: at k = 8 one e-means seed (two Lloyd seeds) leaves one cluster
  without a measured posting, so that arm has 7 cells against the categories' 8.
- **One embedding model, one pooling rule, one scale** (n = 8,420 exported, 2,805 measured). No
  other k grid, spherical off, `nepochs` other than 2.6, or Metal precision lanes other than F32
  were tried.
- Duplicate texts (8,420 postings from 7,549 texts) weigh a repeated text more than once in the
  fit and in the strata counts, as the audit's own pool does.

## How to reproduce

Run against a **copy** of the state dir (`cp -c -R` is a free copy-on-write copy on APFS):
`strata compare` opens the ledger as `vani audit` does, which can save the read-model checkpoint.
`export pooled-vectors` alone writes nothing in the state dir (tested), and refuses an `--out` inside it.

```bash
export VANI_NO_KEYCHAIN=1
export VANI_STATE_DIR=/path/to/copy-of/state     # a copy, not the live dir
go -C <vani repo> build -o /tmp/vani ./cmd/vani

# 1. one pooled, L2-normalised vector per posting that has vectors (mean of its chunk vectors,
#    then normalised), a keys file, and the funnel
vani export pooled-vectors --out OUT

# 2. the baselines and the k to fit (each baseline's cell count, plus a sweep)
vani strata compare --keys OUT/pooled.keys

# 3. fit (Ojas side; see "The fit binary") and keep the JSON summary
vani_cluster --vectors OUT/pooled.vectors --out FITS --k 8,13,16,32,64 --seeds 5 --rounds 5 > FITS/summary.json

# 4. measure
vani strata compare --keys OUT/pooled.keys --summary FITS/summary.json [--json]
```

The reading of the vector log is `semantic.OpenVectorReader` (`internal/semantic/vectorexport.go`):
read-only, no lock, no append, no truncation of a torn tail, no rebuild on another model or
corrupt log. It reuses the log's own header parser, record scanner and record decoder and finds a
posting by `TextHash(title, description)` with the chunk count `ChunkPosting` cuts now, exactly as
`Warm` does. A dropped, dead or duplicate record's description is not in the folded ledger, so it
is read back with `ledger.Reader.Body` (counted separately if it cannot be). The pooled vector is
the mean of the posting's chunk vectors (the unit vectors the log stores, not renormalised before
averaging) normalised to unit length; a mean shorter than 1e-6 is refused as degenerate.

## File formats (the process contract)

Little-endian. The package comment of `internal/clusterstudy/format.go` is the authority; every
reader checks that a file is exactly the size its header implies and refuses a header over the
bounds before allocating (rows <= 200,000, width <= 8,192, rows <= 512 MiB, key <= 512 bytes).

```
pooled.vectors  "VANIPVEC" | version u32=1 | n u32 | dim u32 | keys_sha256 [32] | n*dim f32 (unit rows)
pooled.keys     n lines "<posting key>\n", the same order
*.assign        "VANIASGN" | version u32=1 | n u32 | k u32 | keys_sha256 [32] | n u32 labels (< k)
*.cent          "VANICENT" | version u32=1 | k u32 | dim u32 | k*dim f32
summary.json    format "vani-cluster-fit/1": one object per fit with algo, k, seed, files,
                inertia/mse (f64), non-empty clusters, engine-label mismatches, repeat identity,
                per-round fit milliseconds; the Metal status ("ok" or "not_run" with the reason)
```

`strata compare` refuses a summary or assignments made for another keys file (digest and row
count), a label at or over k, a truncated or oversized file, an unknown JSON field, a run with a
file name that leaves the summary's directory, and a missing e-means run at the category cell count.

## The fit binary

`scripts/cluster-study/vani_cluster.rs` is an example for the `ojas-cluster` crate
(`ojas-cluster/examples/vani_cluster.rs`; the crate's `examples/` holds the benchmark and has no
`src/bin`). It uses only `ojas-cluster` and `ojas-core` (no new dependency). It fits e-means on CPU,
e-means on Metal (F32) when a Metal 4 GPU is present (otherwise the summary says `not_run` and why;
there is no silent fallback), and `lloyd_fit`, from the same Forgy initial centroids per (k, seed),
then writes the labels (the nearest normalised centroid of the final centroids, in f64, the same
for every algorithm), the centroids, and the summary on stdout. Its unit tests cover the file
parser's refusals (truncation, trailing bytes, NaN, infinity, zero and non-unit rows, bounds), the
output layouts, the init and the f64 assignment.

On the Ojas side this belongs on a branch of its own (`emeans/vani-cli`). `ojas` and `tessl` cannot
build with Cargo from a linked worktree (`ojas-cluster` depends on `../../tessl`), so the build
for these results used a scratch crate outside both repositories with path dependencies on the
`ojas` checkout:

```toml
[package]
name = "vani-cluster-scratch"
version = "0.0.0"
edition = "2021"
publish = false
[workspace]
[dependencies]
ojas-cluster = { path = "/Users/bharath/Code/research/ojas/ojas-cluster" }
ojas-core = { path = "/Users/bharath/Code/research/ojas/ojas-core" }
```

with `Cargo.lock` copied from `ojas`, `examples/vani_cluster.rs` copied from the file above, and
`CARGO_TARGET_DIR` outside both repositories.
