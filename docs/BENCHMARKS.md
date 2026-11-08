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
