# Benchmarks

These are measurements of Vani's own code on synthetic inputs, plus older
model comparisons that were not re-run. They are not a ranking of job boards,
and they are not a claim about how fast a live daily run is. A live run waits
on the network, on `robots.txt`, and on model calls. None of the 2026-11-07
runs made a network call.

Agreement with a model is not accuracy. A number from a single day is one
sample.

## 2026-11-07, this machine

Apple M5 Pro, 64 GB, macOS 27.2, `go1.27.3 darwin/arm64`. `go.mod` still says
`go 1.26.6`. Each row is `go test -run '^$' -benchmem -count=5` with the
default one-second bench time, and `VANI_NO_KEYCHAIN=1`. The figure is the
minimum of the five `ns/op` results. The range is the other four.

| Benchmark | What it measures | Minimum | Range across 5 |
|---|---|---|---|
| `BenchmarkParsePay` | One pay range at the end of a 64 KiB description | 0.141 ms (445–464 MB/s, 24 allocs) | 0.141–0.151 ms |
| `BenchmarkParsePayDense` | 64 KiB with a currency sign every few words, and no pay range | 4.30 ms | 4.30–4.64 ms |
| `BenchmarkJobsInsight5000` | Keywords, scores, and a sort of 5,000 synthetic postings of about 6 KiB | 0.468 s | 0.468–0.511 s |
| `BenchmarkGetJSON20MiB` | `srcutil.GetJSON` of a synthetic 20 MiB body through a fake egress client. The cap is 64 MiB. | 18.5 ms, 76.8 MB/op, 22,119–22,122 allocs | 18.5–21.2 ms |

Commands, from this repository:

```bash
go test -run '^$' -bench 'BenchmarkParsePay$|BenchmarkParsePayDense$' -benchmem -count=5 ./internal/jobinsight/
go test -run '^$' -bench BenchmarkJobsInsight5000 -benchmem -count=5 ./cmd/vani/
go test -run '^$' -bench BenchmarkGetJSON20MiB -benchmem -count=5 ./internal/sources/srcutil/
```

Earlier notes of the same benchmarks, not re-run as a paired comparison, so
this page does not call the difference a speedup or a regression:

- Pay, recorded in [FOLLOWUPS.md](FOLLOWUPS.md) as a minimum of 3 on "this
  Mac": about 0.12 ms for the one-range case and about 2.5 ms for the dense
  case.
- Insight, same note: 0.48 s for 5,000 postings.
- `BenchmarkGetJSON20MiB`, 2026-10-21, minimum of an interleaved run: 26.3 ms,
  76.8 MB/op, 22.1k allocs. The byte and allocation counts match the
  2026-11-07 run. The time does not. The Go version of that run was not
  recorded beside the number.

## 2026-11-11, the Jobs read path at the live scale

Same machine, `go1.27.3`, `VANI_NO_KEYCHAIN=1`. The state is a copy of the
owner's live state: a 629 MB ledger of about 62k records, with a ledger
checkpoint and a text facts store saved by an earlier run. Other sessions kept
the load average between 59 and 230 throughout. Wall time for one command
varied from 1.2 to 36 s, so the tables use CPU time. In the CLI table the
CPU spread within an arm was at most 15%; in the serve table it reached 23%
(`--view all --sort company`). The plan, [JOBS_READ_MODEL_PLAN.md](JOBS_READ_MODEL_PLAN.md),
has each phase's own A/B.

**Cold CLI, before and after.** `old` is `vani` built from `17ce448^` (before
Phase 1b) with `go build -overlay` over the same tree. `new` is `a9f754c`. The
runs were interleaved, 5 per arm, with the order alternating each round. Each
row is `/usr/bin/time -l vani jobs list --json <args>`, and the figure is the
minimum user CPU. In every row the slowest `new` run used less than a third of
the CPU of the fastest `old` run.

| `jobs list` | old user CPU (min, range) | new user CPU (min, range) | change | max RSS old / new |
|---|---|---|---|---|
| `--view new` | 8.37 s (8.37–8.75) | 2.08 s (2.08–2.27) | −75% | 1.28–1.53 / 0.57–0.66 GB |
| `--view all --sort company` | 8.22 s (8.22–8.90) | 1.65 s (1.65–1.88) | −80% | 1.29–1.52 / 0.57–0.62 GB |
| `--view new --sort salary` | 8.26 s (8.26–8.88) | 1.81 s (1.81–1.93) | −78% | 1.28–1.31 / 0.57–0.70 GB |
| `--view saved` | 5.32 s (5.32–5.69) | 1.06 s (1.06–1.12) | −80% | 0.78–1.38 / 0.56–0.62 GB |

**Resident serve.** `BenchmarkLiveServe`, `-benchtime=5x -count=3`, CPU per
request from getrusage, user plus system. This is not the same measure as the
user-only CPU in the table above. Not interleaved with the CLI.

| Request | CPU per request (range of 3) | Allocated |
|---|---|---|
| first list, `--view new` (opens the checkpoint and the facts store) | 2.39–2.52 s | 1.60 GB |
| warm `--view new` | 1.54–1.58 s | 702 MB |
| warm `--view all --sort company` | 1.16–1.42 s | 554 MB |
| warm `--view new --sort salary` | 1.23–1.45 s | 587 MB |
| warm `--view saved` | 0.34–0.41 s | 176 MB |
| warm `--view new --q engineer` | 1.34–1.52 s | 600 MB |
| warm `stats --view new` | 1.80–1.92 s | 760 MB |

Heap in use after a list and a forced GC, with the Reader and the facts store
held: 360 MiB. That is a floor, not serve's resident size. Each warm request
allocates 176–760 MB, so the process's RSS between requests is larger. RSS
was not measured.

These numbers miss two of the plan's targets. A warm view change takes about
1.2–1.6 s of CPU, against a target under 200 ms. Resident memory is at least
360 MiB of heap, against under 300 MB. The cold CLI target was under 1 s. Only `--view saved`
comes near it.

What remains is the ranking itself, run per request over every posting:
`jobview.Select`, same-company grouping, location parsing, and the semantic
scores decoded again on each request. The Phase 4 section of the plan gives
the profile.

**Serve keeps what did not change (later the same day).** Serve now keeps the
company-history groups while the ledger and the watchlist domains are
unchanged, the decoded semantic scores while their file is unchanged, and
each title's rank and keyword hits and each location's places. `jobview.Sort`
sorts a permutation instead of swapping whole jobs, and `Select` sizes its
slice once. `old` is `3b1e221` (the serve table above), built with
`go build -overlay`; `new` is the working tree. Same `BenchmarkLiveServe`
warm requests, `-benchtime=5x`, three runs per arm, interleaved with the
order alternating, load average 42 to 69. CPU per request, user plus system.
In every row the slowest `new` run used less CPU than the fastest `old` run.

| Warm request | old (3 runs) | new (3 runs) | change, min to min | allocated old / new |
|---|---|---|---|---|
| `--view new` | 1488–1553 ms | 764–808 ms | −49% | 702 / 323 MB |
| `--view all --sort company` | 1107–1146 ms | 700–900 ms | −37% | 553 / 288 MB |
| `--view new --sort salary` | 1249–1336 ms | 739–847 ms | −41% | 584 / 288 MB |
| `--view saved` | 347–352 ms | 189–213 ms | −46% | 176 / 81 MB |
| `--view new --q engineer` | 1344–1371 ms | 703–737 ms | −48% | 599 / 292 MB |
| `stats --view new` | 1793–1883 ms | 1056–1069 ms | −41% | 760 / 339 MB |

Maximum RSS of the benchmark process (`/usr/bin/time -l`; one first list,
then the warm requests): 1.06–1.07 GB old, 1.01–1.06 GB new. That is the
in-process request path, not a `vani jobs serve` fed over stdin, which was not
measured. It is three times the plan's 300 MB target, and these changes did
not lower it.

The replies did not change: the live-copy parity dump (`TestLiveJobsParityDump`,
18 lists, 2 stats, gaps and 5 shows at a pinned clock) is byte-identical for
the old command, the new command and the new serve's warm second pass
(`VANI_PARITY_SERVE=1`).

Still about 0.7–1.1 s of CPU per warm view, against a 200 ms target. What is
left is per request over every posting in the view: `ToJob`, the score
(`InputsFor`, `ScoreWith`), the stable sort, and the garbage they make.

```bash
scripts/bench-jobs-ab.sh 17ce448^ <copy-of-state> <config.json> 5
VANI_NO_KEYCHAIN=1 VANI_BENCH_STATE=<state> VANI_BENCH_CONFIG=<config.json> \
  go test -run '^$' -bench BenchmarkLiveServe -benchtime=5x -count=3 ./cmd/vani/
```

## Not re-run on 2026-11-07

- **Advance** (`BenchmarkAdvance` in `cmd/vani/advance_bench_test.go`). The
  benchmark sleeps at 1/100 of latencies measured on 2026-10-18/19
  (d1-ultra triage 0.65 s, d3-mini shadow 3.2 s, notes 0.35 s, an assumed
  0.02 s score) over 96 synthetic postings, then multiplies back. The table
  in [FOLLOWUPS.md](FOLLOWUPS.md) (4 workers, shadow one posting in 12, 2
  slots: about 0.34–0.35 s of scaled time per posting) is that run. It was
  not repeated.
- **Employer index.** [EMPLOYER.md](EMPLOYER.md) records a 2026-10-18
  measurement on the real IRS and IPEDS index, warm page cache, minimum of 5
  interleaved runs: a lookup about 0.09 s and 69 MB, against about 3 s and
  490 MB when the full index was loaded. Not re-measured.
- **Local model agreement.** On 2026-10-19, `vani bench local` compared
  on-device models with `liquid/d1-ultra` on one private ledger of 382
  postings. Agreement is not accuracy. The run was not repeated. Cells are
  the share of answers that matched d1-ultra.

  | Model | Answered | role_fit | seniority | sponsorship | work_mode | p50 / p95 |
  |---|---|---|---|---|---|---|
  | d3-mini | 382/382 | 77.5% | 79.3% | 88.0% | 57.1% | 3.2 / 4.5 s |
  | d1-3B | 382/382 | 61.8% | 52.9% | 90.6% | 61.0% | 0.74 / 0.96 s |
  | RSI-Jev v6.0-VL 4B, partial | 192 of 212 tried | 69.8% | 59.4% | 97.9% | 42.7% | 26.9 / 60.0 s |
  | decider-2b v11 | 381/382 | 16.5% | 62.5% | 86.4% | 29.4% | 7.8 / 16.3 s |

  A separate `vani bench local -n 20` of the d3-mini server on Metal answered
  20 of 20, p50 2.9 s. That is a latency sample, not the agreement table.

## Other benchmarks in the tree

`go test -bench .` runs these as well. They have no 2026-11-07 number on this
page: `BenchmarkAppendBatch1000`, `BenchmarkAppend1000`, `BenchmarkOpen20k`,
`BenchmarkLimitedBody`, `BenchmarkSetMark`, `BenchmarkCompact`,
`BenchmarkHashIndex`, `BenchmarkReadViewOnly`, `BenchmarkLoadView`,
`BenchmarkReconcileRun`, `BenchmarkLoad` (LCA), `BenchmarkLoad` (employer),
`BenchmarkDescriptionHash`, `BenchmarkSummaryPerJob500Jobs50Folders`,
`BenchmarkSummariesWalk500Jobs50Folders`, `BenchmarkScanHTML`,
`BenchmarkJobsExportAskPageBound`, `BenchmarkJobsMark10k`,
`BenchmarkJobsFind10k`, `BenchmarkAdvance`, `BenchmarkText`,
`BenchmarkTextLargeDocument`, `BenchmarkPlain`.

A benchmark that was not run is not a pass, and it is not a result.
