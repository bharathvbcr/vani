# Follow-ups

```mermaid
flowchart TD
    subgraph CoreVani["Vani Core (apps/Vani)"]
        VANI_RUN["Pipeline Runner"]
        VANI_EGRESS["internal/egress\n(SSRF Guard & CONNECT Proxy)"]
        VANI_DECIDE["internal/decide/jdtriage\n(4-slot JD Triage Fixture)"]
        VANI_KB["cmd/vani kb sync\n(rkb bank -> DevPrism)"]
        VANI_APPLY["Vani Apply (Extension & Mac App)\n(CAS Store & Native Messaging)"]
    end

    subgraph ManviIntegration["Manvi Ecosystem (devtools/Manvi)"]
        MANVI_EGRESS["Shared Egress Policy\n[Follow-up: Export Vetting + Dialer]"]
        MANVI_LLM["LLM Adapters (Gemini/Claude)\n[Follow-up: Route through egress]"]
        MANVI_PIN["go.work Local Checkout Pin\n[Follow-up: Version lockfile]"]
    end

    subgraph DecideEcosystem["Decide Module (devtools/decide)"]
        DECIDE_ROUTER["Decision Router & Spend Caps"]
        DECIDE_JEV["decide/jev\n[Follow-up: Multi-slot & n8n support]"]
        DECIDE_LAPPI["decide/lappi\n[Follow-up: Unify DevCouncil copy]"]
    end

    subgraph DevCouncil["DevCouncil (devtools/devcouncil)"]
        DC_LAPPI["devcouncil/lappi\n[Follow-up: Replace with decide/lappi]"]
    end

    subgraph DevPrism["DevPrism Service"]
        DP_MCP["DevPrism --mcp\n(resume_gap_analysis & career_upsert_block)"]
    end

    subgraph ExternalServices["External Automation & Agents"]
        N8N["n8n-email-engine\n[Follow-up: Ledger event feed]"]
        LAPPI_AGENT["Lappi Daemon (qd.sock)\n[Follow-up: Train 4 triage families]"]
    end

    VANI_EGRESS -.->|Proposed Convergence| MANVI_EGRESS
    VANI_RUN --> MANVI_LLM
    VANI_DECIDE --> DECIDE_ROUTER
    DECIDE_ROUTER --> DECIDE_JEV & DECIDE_LAPPI
    DECIDE_LAPPI -.->|Unify Dependency| DC_LAPPI
    DECIDE_LAPPI --> LAPPI_AGENT
    VANI_KB --> DP_MCP
    N8N -.->|vani.event lines| VANI_RUN
```

## Needs the owner's go-ahead

- **Converge egress with Manvi.** [`internal/egress`](file:///Users/bharath/Code/apps/Vani/internal/egress) re-implements manvi/fetch's rules because
  manvi/fetch is GET-only, reduces bodies to prose and caps them for model context, so it cannot
  carry ATS JSON or Workday POSTs. Fix: Manvi exports its vetting + pinned dialer as a reusable
  policy; Vani wraps it and deletes its copy. A Manvi change plus a Jarvis pin bump.
- **LLM traffic bypasses Vani's egress.** Manvi's Gemini/Anthropic adapters open their own
  connections to their fixed vendor endpoints. The model cannot choose the destination, so this
  is not an SSRF path, but it is a second outbound path. Resolved by the same Manvi egress export.
- **n8n-email-engine `vani.event` lines** feeding the ledger (alerts from email). A change in
  another repo.
- **DevCouncil's `devcouncil/lappi` onto `decide/lappi`.** The client and request code now live in
  `~/Code/devtools/decide/lappi` (moved, made generic: `NewRequest`, no defect-class constants);
  DevCouncil still has its own copy, so there are two until this lands;
  `decide/lappi/drift_test.go` fails if their code (comments aside) diverges, and skips with a
  message when DevCouncil is not checked out beside `decide`. The change: DevCouncil
  requires `github.com/bharathvbcr/decide` and keeps only its defect-class request, `asker.go`,
  `record.go` and `diffsplit.go`. Blast radius: replace directives in a dependency are ignored, so
  every module that builds DevCouncil through a `replace` (Manvi, Jarvis, Vani's workspace) needs
  its own `require` + `replace` for `decide`, or `decide` must be published. That touches Manvi's
  `go.mod` and `module-pins.txt` and Jarvis's `go.mod` and `upstream.lock.json`.
- **n8n-email-engine onto `decide/jev`.** The email engine's inline Jev client has no context, no
  cancellation and no spend cap. Moving it needs two additions to `decide/jev`: a configurable
  endpoint (with the email engine's key-host and loopback-http rules) and a caller-shaped `state`
  (it sends `{from, subject, snippet, bulk_mail}`, not `{"text": ...}`).
- **Lappi training (approved, deferred).** Lappi-decision's decision pool carries one choice slot
  per example, so the triage question is split for Lappi into four single-slot families
  (`career.jd_role_fit`, `career.jd_sponsorship`, `career.jd_work_mode`, `career.jd_seniority`;
  fixture v2). Vani's Lappi backend routes each slot to its family; Jev and the LLM still get all
  four slots in one request. The generators are being written in Lappi-decision; the training run
  waits for the owner.

## Shadow evaluation (applied 2026-10-19, not yet measured on the live ledger)

- **Three gaps in what `vani eval` could tell you, fixed on the owner's go-ahead ("apply all",
  T = 0.4).** Found by a read-only audit and re-verified in code. Use `vani bench local` to compare
  candidate models offline; this section is about the live shadow's record. Context and the 382/382
  reproduction: "d1-3B against your own labels" under Unverified.
  - **Run output.** `runOnce` prints `vani: shadow "<name>": N call(s) or slot(s) failed, timed out
    or came back unusable this run` after the advance phase when `Router.ShadowErrors()` is nonzero
    (`cmd/vani/shadowerrors.go`). The count is per run and includes slots the shadow left out.
  - **1. A failed shadow is recorded.** `decide/router.go` `compareShadow` now sets every slot of
    `RouterAnswer.Shadow` to `Unavailable` (detail `auth`, `deadline` or `error`, tagged
    `;backend=<shadow>`) when the shadow errors, panics or times out, so `Shadow` non-nil means
    "was asked". `OnDisagree` fires only for a shadow that read the slot (`Answered` or
    `Abstained`): an outage, a refusal or a `Failed` reading is not a disagreement (the last is a
    narrowing: a shadow value outside the slot's options used to fire it). No new annotation keys:
    `triage.<slot>.shadow_reading=unavailable` and `shadow_backend` are the existing ones,
    `pipeline.Unread` ignores them, and nothing validates the value set. `vani eval` now counts
    those postings as asked, shows them in a "shadow unavailable" column, and the existing
    no-worse-coverage rule applies to them. Tests: `decide/router_test.go`
    (`TestFailedShadowIsRecordedUnavailable`: error, auth, panic, timeout; `OnDisagree` silent, one
    `ShadowErrors` per failed call; `TestOnDisagreeNeedsAShadowThatRead`; the two old assertions
    that `Shadow` stays nil were rewritten), `pipeline/triage_annotations_test.go`,
    `eval/paired_test.go`. Failures from runs before this change are not in the ledger and cannot
    be recovered; the report says so.
  - **2. The shadow is named after its model.** `config.Local.BackendName()` gives
    `local-<model>-<combined|per_slot>`, where `<model>` is the GGUF file name without `.gguf` when
    Vani starts the server (the file is what differs between quantisations) or `decide.local.model`
    otherwise, normalised like `config.BackendName`. It is `shadow_backend` in the ledger and keys
    the eval, so two models, or one model asked two ways, are separate Promotion and Shadow rows,
    each needing its own 30 labels (the Shadow table was keyed by slot only and pooled them too).
    **Limit: 32 bytes** (`jev.MaxNameBytes`); `config.Validate` rejects a longer name instead of
    truncating. `local-vllm-d3-mini-q8_0-combined` is exactly 32, so a longer GGUF file name
    (a version suffix, say) fails `vani config check`; shorten the file name. An OpenRouter model
    named `local-...` is refused. `vani bench` names its backend `local-bench`. Rows recorded
    before this stay `local` (append-only): the 382 d1-3B Q8_0 combined postings; the report labels
    that row as recorded before the model was named. `benchCases` skips any primary that is a local
    shadow (`config.IsLocalShadow`). Alternative not taken: separate `shadow_model` /
    `shadow_questions` keys (no length limit, two more contract keys).
  - **3. The verdict is paired.** `eval/report.go` `Promotion.Verdict`: the 30-label floor and the
    no-worse-coverage rule are unchanged, then a comparison on the postings both answered (at least
    30), counting those where exactly one matched the label (`PrimaryOnly`, `ShadowOnly`). It needs
    at least 10 such postings (`MinDiscordant`) and a one-sided 95% exact (Clopper-Pearson) lower
    bound on `ShadowOnly / (PrimaryOnly + ShadowOnly)` of at least `T = 0.4`
    (`PromotionBound`), computed with stdlib `math`. Reference bounds, computed independently of
    the code and checked in `TestLowerBound`: 15 of 20 gives 0.544 (promotable), 12 of 20 gives
    0.394, 10 of 20 gives 0.302, 25 of 50 gives 0.376 (all not), 10 of 10 gives 0.741. Consequence
    to know: a shadow that matches the primary on nearly everything has few disagreements and
    reads "insufficient disagreements", never "promotable"; and a tie on 20 disagreements no
    longer promotes.
  - **Verification (2026-10-19).** `go build ./...`, `go vet ./...` and
    `go test -count=1 ./cmd/... ./internal/...` in Vani pass, and
    `go test -count=1 github.com/bharathvbcr/decide/...` passes (decide, jev, lappi; the router
    tests also under `-race`). Not done: a live `vani eval` over the real ledger, a live run with a
    failing shadow, and `vani config check` against the live config (its GGUF file name must fit
    the 32-byte limit above).

## Structural

- **Decide module location.** `decide` is local-only (no remote, nothing committed); Vani reaches
  it by `replace => ../../devtools/decide`.
- **Manvi pin.** `go.work` uses the local Manvi checkout (main at ccbe4ed on 2026-10-18), while
  Jarvis pins 5dbf47c. Add a lock file and a check.
- **`vani status --json` re-reads decide's spend file.** `decide.DailyCap` has no reader (only
  `Allow` and `Charge`), so `readOpenRouterSpend` (`cmd/vani/statuscmd.go`) parses the
  `{"version":1,"days":{day:{key:usd}}}` format itself. A test writes the file with the real
  `DailyCap`, so a format change fails there. The fix is a `DailyCap.Spent(key)` accessor in
  `decide`, then delete the parser.
- **Skipped daily runs are not in the run record.** `daemon.Run` logs a run it skips for starting
  more than 6 h late (and one that runs late) but does not record it, so Vani Apply's Pipeline tab
  shows "No run in over a day" rather than why. A hook from `daemon.Run` into the run record
  (`cmd/vani/runrecord.go`) would say "skipped: 7 h late".
- **`JobsClient.call` and `PipelineClient.status` are the same twelve lines** (run vani, bound,
  decode `{"error"}`). Extract one `VaniJSONCall` in `VaniApplyKit` when `Jobs.swift` is next
  touched.

## Unverified (Phase 0)

- **Jobs tab controls reach the model: not driven by automation (2026-10-19).** An in-process
  harness (the Jobs tab in a plain window over the live state, no Login Items agent) verified the
  Insights toolbar button through its AppKit control. Setting the model as each control does,
  round-tripped through the real `vani`, verified: the fit filter (20 `strong_fit` shown, matching
  `jobs stats`), sort by freshness and by priority (ordered), and selection loading the detail.
  Not verified: that the Sort picker, the Filter menu items, a fit-row tap and a scatter click
  call those setters. SwiftUI's toolbar menus came back empty to `NSMenu`. The Insights panel was
  absent from the in-process accessibility tree (603 elements, none with these labels). Synthetic
  `NSEvent` clicks did not reach SwiftUI tap gestures. Screen control was declined. The scatter's
  nearest-dot rule is unit-tested as `ChartHit.nearest`. Closing this needs an XCUITest target,
  or a 30-second manual check: Sort > Newest first, click a fit row, click a dot.
- **Picked notes have not run live (2026-10-18).** Notes are now reasons that d1-ultra picks from
  `<state dir>/reasons.json` (`internal/decide/jdreasons`), replacing a Claude turn per posting.
  The live catalog (14 fit, 14 gap, 13 check) was drafted in a Claude Code session on 2026-10-18
  and installed with `vani reasons import`, because `vani reasons draft` needs Claude and the
  Keychain key is refused until `ANTHROPIC_WORKSPACE_ID` is set. Its `drafted_by` says so. Not
  measured: how well d1-ultra picks among up to 16 options per category, and what
  a three-question call costs. The design gives one pick per category; a yes/no question per reason
  (several scored picks per category, about 15 times the questions per call) was not built.
- **`typesafe/jev-1.13` not tested, and not in use (owner's call, 2026-10-19).** The owner runs
  `liquid/d1-ultra` (below), so no four-question request was ever sent to jev-1.13. It stays
  `decide/jev`'s `DefaultModel` and the model in `TestRequestShapeGolden`
  (`decide/jev/jev_test.go:86`). A config that leaves `decide.jev_model` empty, or sets jev-1.13,
  runs untested against the four-slot question.
- **Lappi does not serve the triage families; nothing to calibrate yet.** Checked 2026-10-18,
  read-only. Lappi-decision has no `career.jd_triage` family. It registers the four single-slot
  families (`crates/qd-prep/src/synth_career.rs:57-66`), and its generator is written. All four are
  `task_not_trained` in `docs/caller-contract.md` (rows at lines 85-88) and stated zero-train in
  `data/decisions/v6-allocation.json` (`stated_zero_train_families`: "no pool is built or trained. No
  R2 cap has been ruled"). There is no train cap, so no calibration entry exists. The export
  calibration gate refuses a family without one (`crates/qd-export/tests/calibration_gate.rs`).
  Both copies of the client require and parse `score` (the margin) and `conformal_set`
  (`decide/lappi/client.go:125, 442-487`; DevCouncil `devcouncil/lappi/client.go:124-125, 441-476`), so
  the calibration fields are supported on the client side. The fixture copies match
  (sha256 `0cf04ad7...`, both sides). Rechecked: no `qd` on PATH, `~/.cargo/bin` or
  `/opt/homebrew/bin`; no `~/Library/Caches/qd/` (the default socket dir); no launchd agent; no `qd`
  process. Nothing reads Lappi until an R2 cap is ruled and the four families are pooled, trained,
  calibrated and served.
- **d1-3B against your own labels.** `.state/config.json` runs d1 (`decide.local.server`) as the
  shadow. The ledger holds d1 shadow readings for 382 postings that d1-ultra read (all four
  slots answered by both). The verdict is `vani label` then `vani eval`, and it needs at least 30
  labeled postings that d1 was asked about. Neither the 12-slot synthetic check below nor the
  agreement numbers that follow are evidence for promotion.
  - **Agreement with d1-ultra on real postings (2026-10-19, `vani bench local`, 382 postings, no
    labels).** `vani bench local` replays postings the primary read through a local model with the
    run's exact request. It reproduced the recorded shadow 382/382 on every slot, so the request
    matches production. d1-3B Q8_0 agreed with d1-ultra on:

    | slot | agreement |
    |---|---|
    | sponsorship | 346/382 (90.6%) |
    | role_fit | 236/382 (61.8%) |
    | work_mode | 233/382 (61.0%) |
    | seniority | 202/382 (52.9%) |

    Latency was p50 735 ms and p95 956 ms per posting (four combined questions, Metal), and the
    server started in 5.0 s. The synthetic 0.3 s does not hold on real posting lengths.
    - Where d1-3B disagrees:
      - It reads senior roles as mid (88 times).
      - On role_fit, the costly direction is mismatch_seniority → strong_fit (21 times).
      - It answers not_stated where d1-ultra read onsite (78) or a sponsorship stance (35). Those
        misses are conservative.
    - Where d1-ultra was confident (p ≥ 0.9), d1-3B agreed on role_fit 77/78 and on seniority
      43/46. Most disagreement is on postings d1-ultra itself was unsure of (role_fit p < 0.5:
      58/172 agree).
    - Agreement is not accuracy. Either side can be wrong.
    - The JSON report is `build/bench-d1-3b.json` (gitignored).
  - **vllm-d3-mini against the same 382 postings (2026-10-19, `vani bench local --batch 8192
    --parallel 1`).** The model is `ggml-org/vllm-d3-mini-GGUF` `vllm-d3-mini-Q8_0.gguf`
    (5,157,835,200 bytes, sha256 `214d44cc…793f93d`, matching Hugging Face). It is 4B, built on
    Qwen3.5-4B, and Apache-2.0. The owner approved the download. It is at
    `build/models/ggml-org-vllm-d3-mini-GGUF/` (gitignored) because the workspace gate refuses
    writes outside the repo. llama.cpp 0.7.0 served it over `/v1/systemone`, with no failed
    posting.

    | slot | d1-3B | d3-mini |
    |---|---|---|
    | role_fit | 236 (61.8%) | 296 (77.5%) |
    | seniority | 202 (52.9%) | 303 (79.3%) |
    | sponsorship | 346 (90.6%) | 336 (88.0%) |
    | work_mode | 233 (61.0%) | 218 (57.1%) |

    Each cell is agreement with d1-ultra out of 382.

    - **Latency:** d3-mini runs at p50 3.2 s and p95 4.5 s per posting, against d1-3B's
      0.74 s and 0.96 s, so about 4.4× slower. The flags also differ: d1-3B ran with `-np` auto
      and the default batch, d3-mini with `-np 1 -b/-ub 8192`.
    - **The costly error mostly goes:**
      - role_fit mismatch_seniority → strong_fit falls from 21 to 6.
      - seniority senior → mid falls from 88 to 27.
    - **Its own probability separates its errors from cloud agreement.** With local p ≥ 0.5:
      - role_fit agreed 148/148;
      - sponsorship 298/299;
      - seniority 176/185;
      - work_mode 108/112.

      Below 0.5, role_fit agreed 148/234.
    - **Where it trails:** its losses on work_mode and sponsorship are not_stated answers where
      d1-ultra read a stance: onsite → not_stated 92, hybrid → not_stated 45, sponsors_visa →
      not_stated 28. These misses are conservative.
    - Report: `build/bench-d3-mini.json`. Agreement is not accuracy. `vani label` and
      `vani eval` decide, and d3 is a new candidate under Lappi-decision's pre-registration,
      which names d1-3B only.
    - **d3-mini is the run's shadow since 2026-10-19 (owner's go-ahead).**
      - `decide.local.server` gained `batch_size` and `parallel`, and the run passes them to
        llama-server (`TestLocalServerGetsBatchAndParallel`).
      - `.state/config.json` now runs `vllm-d3-mini-Q8_0.gguf` with `batch_size` 8192 and
        `parallel` 1, and passes `vani config check`. Its deprecated `jev_*` keys were renamed to
        `openrouter_*` in the same edit.
      - A bare `vani bench local -n 20` started exactly that server, on Metal, and answered 20 of
        20 postings across four companies (p50 2.9 s).
      - Shadow readings recorded before the switch are d1-3B's. Until the per-model shadow id
        lands, `vani eval` pools them with d3's.
      - The model file lives in the gitignored `build/models/`. Moving it to
        `~/Code/research/models/` means updating `model_path`.
    - `vani bench local -n N` with N below the population now takes postings round-robin across
      companies (`TestBenchCasesSpreadAcrossCompanies`). It used to take the first N in key
      order, which is one company's.
    - Insight cost: one `vani jobs list` (keywords for every browsable posting, blurb learning,
      scores, sort) took 0.48 s for 5,000 postings of 6 KiB (`BenchmarkJobsInsight5000`,
      `TestJobsInsightScalesTo5000Postings`).
    - `ggml-org/vllm-d3-flash-GGUF` (9B, about 9.5 GB at Q8_0) is the stretch candidate. It
      has not been downloaded.
  - The owner decided on 2026-10-18 to port d1 natively to tessl only if it beats Jev. Jev runs
    only with `OPENROUTER_API_KEY` exported, and its `daily_usd` cap is 0.5. The promotion table has
    one row per primary backend (d1 vs `jev`, d1 vs `llm`), each needing its own 30 labels. A day
    where the LLM answered after Jev's cap therefore never counts as Jev's baseline.
  - The primary is now `liquid/d1-ultra` (`decide.openrouter_model`, the owner's choice). Since
    2026-10-19 it answers as `d1-ultra` in annotations and eval rows. The 382 earlier rows name it
    `jev`; `vani eval` reads those as `d1-ultra` (primary only, never a shadow), so the numbers do
    not move (`internal/eval/names_test.go`). "Beats Jev" reads as local d1-3B against cloud
    d1-ultra. Lappi-decision's pre-registration amendment 2 records this rename (draft, for the
    owner's review).
  - **The first live runs read nothing (2026-10-18, about 22:10).** `OPENROUTER_API_KEY` was unset,
    so d1-ultra was off. Anthropic refused every call with HTTP 400, "This API key is not scoped to
    a workspace", because `ANTHROPIC_WORKSPACE_ID` was unset; Vani sends `anthropic-workspace-id`
    only when it is (`wire.go`, as Manvi's CLI does). All 300 postings per run stayed Live, and no
    shadow reading was recorded. Since then, the advance phase stops after 20 unread postings in a
    row, or when none of the postings it tried were read, and the run fails. A shell loop then
    stops, and no empty digest is written. Unread postings are summarised in one line, and a
    provider error that differs only in its request id is printed once.
  - `vani label` queues, in order: disagreements, then postings d1 was asked about, then postings
    triaged before the shadow existed. The 604 postings triaged on 2026-10-18 have no d1 reading and
    are not re-asked: `vani retriage` only returns postings that got no reading at all.
  - The qd.sock scoping session recommends about 80 labels: 50 to fit calibration and 30 held out.
    Labelling disagreements first means precision is measured on harder cases than a random sample,
    but the shadow and the primary are scored on the same postings.
  - **Held-out verdict in Vani (2026-10-19, needs the owner's approval before merge).**
    `vani eval --held-out d1-vani-jdtriage-2026-10-18` prints the usual report unchanged, then a
    Promotion table over the pre-registration's held-out postings only. The split ranks every
    labelled key by sha256(salt || 0x00 || key) ascending and holds out the first 30
    (`internal/eval/heldout.go`, `Splits`). It uses the same Promotion arithmetic and the same
    `jev` alias as the all-labels table. Salt and size come from the registered name and are not
    flags. The rule is tested against a digest computed with `shasum`, and the code is tested to
    reproduce the all-labels rows when every posting is held out (`heldout_test.go`). If any label
    line is unreadable, the held-out run refuses, because a missing key could move the split.
    - **Owner decision: what counts as "labelled".** The flag ranks every distinct key in
      `labels.jsonl` (latest line per key). That includes a key whose labels are all `skip` and a
      key with no ledger record. This keeps the split a function of the label file alone. If such a
      key ranks in the first 30, it takes a held-out place but adds nothing to the asked count. The
      registration's rule text does not say whether such keys count. Excluding all-skip keys is a
      one-line filter in `BuildHeldOut`, and it must be decided before any label is read.
    - The flag does not print the labels.jsonl sha256 that the registration's ledger row needs. Run
      `shasum -a 256` on the file at the verdict run.
- **whisper.cpp after the llama.cpp 0.7.0 upgrade:** `whisper-cli --help` runs; no transcription
  was made, because no whisper model is on this Mac.

## Verified (Phase 0)

- **Lever EU host (2026-10-19).** Vani's real `lever` source and `egress` client
  (`build/phase0-devprism/leverprobe`), free unauthenticated GETs:
  - `quantinuum`, `olx` and `kwalee` (all listed on `jobs.eu.lever.co`) on `api.eu.lever.co`
    returned 121, 55 and 17 postings, all with `jobs.eu.lever.co` posting URLs. On `api.lever.co`
    every one was `board not found`.
  - `spotify` is the reverse: 82 postings on the US host, `board not found` on the EU host.
  - So a board's `region` must match where the company is hosted; the wrong host fails loudly as
    not-found, never empty. Egress's DNS vetting and pinned TLS accept the EU host.
- **DevPrism `--mcp` (2026-10-18, [PHASE0-devprism.md](PHASE0-devprism.md)).** `devprism.binary`
  can be set to `/Applications/DevPrism.app/Contents/MacOS/claude-prism-desktop` with
  `devprism.tools_hash` `1349173b7e4784bf59a8f478d6a4d63fe4f3479e332dba946ad5f09767ecab39`.
  - Every `--mcp` start opens the real `career.db` and runs schema init plus two idempotent
    migrations. On today's db they change nothing: a copy was byte-identical after a session.
  - Only `HOME` selects the data dir.
  - The three tools Vani calls match its shapes on the installed build, and a real `kb sync` against
    a copy added 81 blocks, then reran as 81 unchanged.
  - Ranking input is bounded: every source caps a description at 64 KiB (`srcutil.MaxDescription`;
    ashby, greenhouse, lever, workday, page and its JSON-LD path), far under DevPrism's 1 MiB
    `jd_text` refusal.
  - The installed build (2026-08-22 13:14) is older than the DevPrism checkout. Inferred from
    `git log`: it was built from `24ecbc8` (2026-08-21), the last commit before it. Four later commits
    touch `career_db` or the MCP tools: `777f94b`, `52869d3`, `51b213f`, `90b910c`. Recompute the
    hash after any DevPrism update.
  - **Owner decision before the first real sync: the duplicated work history.** Back up
    `career.db` first. 5 of rkb's 8 `work` blocks duplicate 4 existing DevPrism `experience` blocks
    (same title and org, different ids). Those 4 blocks hold 8 bullets and 4 facts that rkb does
    not carry. Another 5 title matches are at a different org and are not duplicates. The id pairs
    are in `build/phase0-devprism/duplicate-work-blocks.tsv` (gitignored; ids only, plus
    bullet/fact counts). Either delete the 4 old blocks in DevPrism after moving anything worth
    keeping into rkb, or keep both and accept that `resume_gap_analysis` counts evidence from
    either copy.
- **Decisions API, four slots in one request (2026-10-19 02:18 UTC, `liquid/d1-ultra`).** The owner
  approved one paid call, sent by `build/phase0-devprism/jevone`: Vani's real `jdtriage.Question` for a
  synthetic posting, `decide/jev` with `MaxAttempts: 1`, through `egress`.
  - Result: 1 HTTP request, status 200, 755 ms. All four slots answered (`role_fit`, `seniority`,
    `sponsorship`, `work_mode`), each `{"type": "choice", "choice", "probabilities", "confidence"}`.
    `state: {"text": ...}` was accepted. The response names the resolved model
    (`liquid/d1-ultra-20261015`) and `"provider": "Liquid"`.
  - **`usage.cost` is USD**: `{"input_tokens": 664, "output_tokens": 0, "cost": 0.000027888}`, which
    is exactly 664 tokens at the published $0.042 per million input tokens. `decide/jev` charged that
    value as `cost_usd`.
  - Readings: senior, sponsors_visa and remote, each correct for the posting. `role_fit` was
    `mismatch_seniority` (0.93), correct against the default early-career target.
  - `decide/jev` records `confidence` (for example 0.913) as the slot probability, not the chosen
    option's entry in `probabilities` (0.931). That is by design: it prefers `confidence` and falls
    back to `probabilities[choice]` (`decide/jev/jev.go:454-456`), as the email engine does.
  - Set as Vani's Jev model on 2026-10-19: `.state/config.json` `decide.jev_model` is now
    `liquid/d1-ultra` (was `typesafe/jev-1.13`). `vani config check` passes. Key env
    `OPENROUTER_API_KEY`; cap `decide.daily_usd.jev` 0.5, about 17,900 postings a day at this call's
    cost.
- **Local d1-3B (Q8_0) on llama.cpp 0.7.0 (2026-10-18).** llama.cpp 0.6.0 refuses the model
  (`unsupported decision model type: lfm2-d1`). The model is at
  `~/Code/research/models/LiquidAI-d1-3B-GGUF/`, sha256 `2f0942d5…eaba77d`.
  - `localserver` starts it in about 2 s and stops it cleanly. A real `vani run-once`, run without
    keys, started and stopped it before failing at "no decision backend". No process was left behind.
  - The check used three synthetic postings and 12 slots, through `decide/jev` (`Name: local`) over
    `egress.Loopback`, with the configured target summary rendered as in a real run.
    - The `combined` question scored 10/12, at about 0.3 s per posting on Metal.
    - Both misses had low probability: a `role_fit` at p 0.22, and a `seniority` at p 0.37 on a mid
      sales role.
    - Without the target summary, `per_slot` scored 9/12 and `combined` 10/12.
  - **CPU (`-ngl 0`) and Metal gave the same choice on all 12 slots.** Probabilities differed by at
    most 0.03, and CPU took about 4 s per posting.
  - A low-probability answer is still read as `answered` (the Jev wire shape has no abstain). Only a
    drop rule's `min_openrouter_prob` turns probability into action, and only for the configured
    OpenRouter model's name (`DropPolicy.calibrated`, `internal/pipeline/runner.go`); `local` is
    only ever the shadow and never drops.
  - **Prompt variant measured:** each option goes out with its fixture description
    (`jdtriage.Descriptions()`), and the target summary is in the state. The options are in
    alphabetical order, not fixture order: `decide/jev` builds `criteria` as a Go map, and
    encoding/json sorts map keys (`decide/jev/jev.go`, `body`). Cloud and local get the same order,
    so the comparison stays fair. The
    qd.sock scoping session found that the bare option strings change 3 of 12 choices and that a
    reordering can move a probability by up to 0.36. A shadow verdict therefore holds only for this
    variant; an adapter that serves d1 has to send the same descriptions in the same order.
  - **Metal only.** `localserver` passes `--device MTL0 -ngl all`, following the owner's rule
    (2026-10-18) that on-device decider runtimes use Metal by default and never fall back to CPU.
    Measured: an absent device makes llama-server exit 1 at startup ("invalid device: MTL9"), so
    without Metal the run carries on without the shadow. The test fake reports unhealthy unless it
    was started Metal-only. A real start took 3.0 s and gave the same 10/12.
  - **What the verdict covers.** The served runtime for d1 is to be a native tessl/qd-metal port
    (Lappi-decision `docs/third-party-decider-runtime-2026-10-18.md` §10/§13), not llama.cpp.
    llama.cpp is the eval path and the port's parity oracle on the same GGUF weights. So a
    promotable verdict from `vani eval` holds for the port only once the port passes parity with
    llama.cpp on the same prompt variant.
  - **Slots and context:** `localserver` leaves `-np` on auto. On 0.7.0 that gives
    `n_slots = 4, n_ctx_slot = 8192, kv_unified = true`, so the four combined questions run in one
    pass over a shared 8192-token pool. A maximum-size posting (past the 24 KiB cap, with the
    summary) answered in 2.1 s without a context error. Do not add `-np 4` alone. Measured with
    `-c 8192 -np 4`, the log shows `n_ctx_slot = 2048, kv_unified = 'false'`, and the same posting
    (5073 tokens) was refused with HTTP 400 ("exceeds the available context size (2048 tokens)").

## Known limits

- **Pay is read from text, by fixed rules (`internal/jobinsight/pay.go`).** Display and browse
  filters only; no engine rule reads it.
  - Lever and Ashby now carry their pay into the description (owner's call, 2026-10-19). That
    changed every Lever and Ashby posting's description hash. On the first run after it,
    `Reconcile` (`internal/pipeline/reconcile.go`, the `DescriptionHash() != hash` case) marks each
    one Live with reason `changed` and clears its triage, score, rank and explanation. Each is then
    re-triaged, re-scored and re-explained (decision and LLM spend, under the daily caps), and every
    one not hidden in Jobs is listed in the next digest again. Marks and notes are kept (they live
    in `internal/marks`). A posting dropped before is re-triaged too, and may come back.
  - Greenhouse's job-board API returns structured pay (`pay_input_ranges`) only from the
    single-job endpoint with `pay_transparency=true`, not from the list Vani reads
    (docs.greenhouse.io/job-board.html, checked 2026-10-19). Fetching it would be one request per
    posting, so Greenhouse pay is whatever the posting's own text says. Workday and page boards are
    the same.
  - A bare "$" reads as USD. A Canadian or Australian posting that writes "$" without "CAD"/"AUD"
    near the amount is read as USD and compared as USD.
  - No exchange rates: `--min-salary` leaves out a job that states pay only in another currency.
  - Several ranges in one currency (location bands) become one envelope, so `--min-salary` keeps a
    role if any band reaches the floor. A range that would stretch the envelope past 3× (an intern
    band, a variable-pay range) is left out and the earlier ranges stand.
  - Bounds: 128 KiB of text, 8 KiB of text around currency signs, 4,000 amounts. Measured on this
    Mac (`BenchmarkParsePay`, min of 3): about 0.12 ms for a 64 KiB posting with one range, and
    about 2.5 ms for 64 KiB dense with currency signs (the window cap's worst case).

- **llama-server on a Unix socket leaves the socket file behind** (llama.cpp 0.7.0, `--host
  <path>.sock`). It does so even after a clean SIGTERM (exit 0), and the next start on that path
  fails with `couldn't bind HTTP server socket`. Vani's `localserver` uses a TCP loopback port, so it
  is unaffected. Any socket adapter (the Lappi-decision qd.sock plan) has to remove a stale socket
  itself after checking that nothing is listening. Serving over the socket works: health, then
  `/v1/systemone`. The socket was created `srwxr-xr-x` under the default umask. On macOS, connecting
  needs write permission, so only the owner should be able to connect. That is inferred, not tested
  as another user; keep the socket in a 0700 directory anyway.

- **Lightpanda proxy token is in argv.** The per-run token rides in `--http-proxy` and
  `--proxy-bearer-token`, so another process of the same user can read it with `ps` while a fetch
  runs. Bounded: the proxy listens on loopback only, the token is fresh per run and dies with the
  proxy, and it only forwards to allowlisted hosts on 443. `lightpanda help` lists proxy
  credentials as flags only (`--http-proxy`, `--proxy-bearer-token`; checked 2026-10-18).
- **Lightpanda reads robots.txt at the start of every run** (`--obey-robots`; measured
  2026-10-19, 1.1.0-nightly.10455): a board with N postings asks for its host's robots.txt N+2
  times per run (Vani's own Crawl-delay read, then once per lightpanda process). Each process's
  read sits inside that run, so it is paced with the run, but it is not deduplicated: lightpanda's
  RobotStore is in-memory per process and there is no flag to hand it a robots.txt. Dropping
  `--obey-robots` would lose robots enforcement on subresources, which `--block-urls` cannot
  reproduce (no Allow exceptions). Deduplicating needs one long-lived lightpanda per run so its
  RobotStore is shared: `lightpanda mcp` (JSON-RPC on stdio, no WebSocket) or `serve` (CDP). That
  replaces `fetch`'s exit-code and stdout contract that ExecRunner and `--fail-on-http-error` rely
  on, so it waits for the owner. `TestLightpandaRobotsIntegration` pins the current count.
- **Lightpanda has no `structured_data` dump mode;** JSON-LD is parsed from `--dump html`.
  Telemetry is on by default and is disabled through `LIGHTPANDA_DISABLE_TELEMETRY=1`.
- **kbsync shared bullets: fixed in code, not yet run against the real career.db.** 14 rkb
  bullets are listed by more than one entry. Before the fix, every copy kept the bare rkb bullet
  id. DevPrism keys embeddings by `(owner_id, model)` with the bullet id as `owner_id`, so the
  copies shared one embedding row. A search hit then resolved to an arbitrary parent block
  (`career_db/vectors.rs:629-635`, `LIMIT 1`). Deleting one parent also dropped the embedding the
  other still used (`career_db/mod.rs:526-556`).

  Now every bullet id, shared or not, is `<entry id>/<bullet id>` (`scopedBulletID` in
  `internal/kbsync/blocks.go`).
  - Why every bullet: an id does not change when a second entry starts listing a bullet.
  - Why `/`: it is outside rkb's id alphabet `[A-Za-z0-9._:-]`, so a scoped id never collides
    with a bare id.
  - DevPrism accepts it: `Bullet.id` is an unvalidated `String` (`career_db/mod.rs:49-60`).
    `career_upsert_block` caps only the whole block, at 1 MiB, and every DevPrism lookup compares
    the whole id.
  - Refused: an entry with an empty id, or any id containing `/`, is blocked, never sent.
  - Hashes: `hashVersion` is now 2, so every hash changed. State written before the fix reads as
    "update", never "unchanged".

  **Limit left: a block already synced with bare ids.**
  - `buildUpdate` renames each bare-id bullet to its scoped id. That keeps DevPrism's fields on the
    bullet and does not duplicate its text.
  - DevPrism's overwrite gate counts the vanished bare id as a dropped bullet. It answers
    `inputRequired`, which `kb sync` reports as `needs_confirmation`.
  - The sync never confirms. It records no hash, and it asks again on every run.
  - Way out: the owner deletes that block in DevPrism. The next sync then adds it fresh with
    scoped ids, because `Plan` adds any block DevPrism lacks.
  - No real sync has run, so only the Phase 0 copy of career.db has bare ids.
  - Tests: `internal/kbsync/scoped_test.go`.
- **Employer lists need a yearly re-import** (IRS eo1-eo4 and IPEDS HD2024 imported 2026-10-18 into
  `.state/state`, 1.6M names). Recheck the IPEDS codes in each year's dictionary (docs/EMPLOYER.md).
  Runs read the derived `index.view` (0.09 s / 69 MB a lookup, was 3 s / 490 MB). Import still loads
  the full index: re-importing eo3 into it takes about 6.3 s and 770 MB at peak (was 6.6 s / 1.0 GB).
- **LCA imports are one file per fiscal year.** Quarterly files are cumulative, so importing Q2
  and Q4 of one year double-counts. Re-importing identical bytes replaces the earlier import
  (keyed by the file's sha256).
- `decide.DailyCap` is safe within one process only; the daemon and a manual `run-once` at the
  same moment could under-count a Jev charge.
- DevPrism reports no aggregate score; the rank is must-have coverage until a better definition
  exists.
- `launchd plist` does not embed `VANI_CONFIG` / `VANI_STATE_DIR`.
- **Vani Apply Mac app, unverified on a live system** (`macos/README.md`):
  - Safari loading the hand-assembled appex: `pluginkit -m -p com.apple.Safari.web-extension` after
    the app is first opened.
  - Whether a development-signed extension needs "Allow unsigned extensions".
  - Signed host/appex opening the App Group container without a prompt.
  - A live put/get from each browser.
  - Safari's native message size limit (undocumented; the bank is capped at 900 KB for Chrome's
    1 MB).
- **Safari and promise-returning `chrome.*`:** `background.ts` awaits `chrome.tabs.query`,
  `chrome.tabs.sendMessage`, `chrome.scripting.executeScript` and `chrome.storage.local.*`. Apple
  documents the `chrome.*` alias but not that these return promises. If the live Safari test shows
  them returning undefined, add a `dist-safari` prelude in `build.ts` that aliases `chrome` to
  `browser`. The native transport already prefers `browser.runtime.sendNativeMessage`.
- **Safari booleans:** the store requires a real boolean for `captureSelfId` and `review`. If Safari's
  bridge delivers JS `true` as a plain NSNumber, every Safari write turns self-ID capture off. The live
  check: a Safari put with `captureSelfId: true` must read back true.
- **Mac app deletions do not win over a browser offline copy:** answers written to
  `chrome.storage.local` during a host outage are merged back by `updatedAt`, so an answer deleted
  in the app meanwhile can return. There are no tombstones yet.
- **Claude wiring (2026-10-18), not yet exercised live:**
  - **No real Opus call yet.** Agent shells cannot read the Keychain item `anthropic-api-key`, so
    no paid call has been made. The first one is a Draft click in Vani Apply, after setting Settings ▸
    Claude drafting to `.state/config.json` and `.state/state`. Its acceptance check: a draft returns
    and `.state/state/budget/llm-<day>.json` records the charge.
  - **The pipeline still needs the key in its environment.** `run-once` and `daemon` have no
    launcher that injects it (the `manvi` shell function covers only `manvi`). Running them needs
    `ANTHROPIC_API_KEY` exported for that command, or a `vani` shell function like the `manvi` one.
    A launchd agent would need its own injection.
  - **First-run cost.** With no Jev and no title prefilter, every live posting goes to Opus for
    triage, at roughly $0.01 to $0.03 each. On 9 boards the $10/day cap runs out on day one. Postings
    the cap refuses now stay `live` and are retried the next day (`ErrTriageUnavailable`). Before this
    change they became `unsure` and were never triaged. A title prefilter, from the track deciders'
    `title_signals`, would stretch the budget.
  - **First `run-once` (2026-10-18 21:15) used a stale `build/vani`,** built before the
    pricedProvider fix. Every LLM call was refused locally, so $0 was spent. 300 postings went
    live → unsure → scored on that non-answer. `vani retriage` (new; `--dry-run` lists) returned
    them to live. It is the repair for any future outage that predates `ErrTriageUnavailable`.
    The pipeline also caps each run at 300 postings, so one run is at most 300 triages plus their
    notes.
  - **Second `run-once` (21:20): Anthropic refused every request with a 400** (300 refused
    attempts, $0). `vani draft` showed why: the owner's key is not scoped to a workspace, so each
    request must carry `anthropic-workspace-id`. Vani built the adapter with `anthropic.New`,
    ignoring Manvi's `ANTHROPIC_WORKSPACE_ID` (and `ANTHROPIC_BASE_URL`). `provider()` now uses
    `NewForWorkspace` as Manvi's CLI does, and Vani Apply has a Settings ▸ Claude drafting
    workspace field that it passes to `vani draft`. Either export `ANTHROPIC_WORKSPACE_ID` with
    the key, or replace the Keychain key with one created inside a workspace.
  - **Postings awaiting triage were invisible (2026-10-19).** The app's ledger held 3,027 postings,
    but only the 283 that reached a digest were listed. All 656 Anthropic postings and 1,983
    others were still `live`, because each run triages at most 300 and no view lists untriaged
    postings. The LLM budget file showed 1,098 refused attempts, but no record of why: only the LLM
    backend's errors were printed (to stderr), and the run kept none of them. Now:
    `advance_per_run` sets the cap (default 300, at most 5000). The run record and
    `status --json` carry the `advance` report and `backend_errors` from every decision backend
    (OpenRouter, LLM, Lappi, local, and the notes picker). `jobs list`/`stats` report `pending`,
    and the digest and the Jobs tab footer show the count. Draining 2,639 postings at 300 a run
    takes 9 runs. Raising the cap is the owner's spend decision.
  - **Safari can't draft.** Its sandboxed appex cannot start `vani`, so the draft op answers
    `unsupported`. Fixing that means XPC to the app, or a request queue in the group folder that the
    app serves.
  - **Mark Applied from the extension (2026-10-19).** The extension offers to mark a tracked job
    Applied after a final Submit, or from the popup on any page (`jobFind`/`jobMark` native ops,
    `vani jobs find --url`; `extension/README.md`), in Chrome (native host) and Safari (agent XPC).
    Done: gh_jid and other job-naming parameters, Greenhouse embeds, apply sub-pages, frames,
    offers kept in `chrome.storage.session`, a toolbar badge, the Jobs tab reloading when the app
    comes to the front. Checked end to end with the built `vani-apply-host` and a real `vani` against
    a seeded ledger (finds, mark, refusals, no shell). Not done or not checked:
    - **Not run in a live browser.** Loading the unpacked extension in Chrome, the Safari appex, and
      the agent over a real launchd Mach service (Login Items) were not exercised; the XPC calls were
      tested over an in-process connection and the extension through `bun test` and `test/dom.html`.
    - **Submit is not watched on hosts the manifest does not list** (a company careers page). Widening
      the content-script hosts widens what the extension can read, which is the owner's call; the popup
      row covers those pages on demand.
    - **A fixed set of job-naming parameters** (`internal/joburl.IDParams`, mirrored in
      `extension/src/applied.ts`). A board that tells jobs apart by another parameter is "ambiguous" or
      unmatched, never guessed; add the name to both lists and the shared vectors.
    - **Form submitted by script with no Submit button** gives no offer; the popup row still works.
    - **A frame the top frame cannot reach** (no content script there) shows the question inside the
      frame, which may be small.
  - **Vani Apply launches anthropic drafts through `anthropic-account` (2026-10-19).** The app and
    the Chrome host no longer read the Keychain or keep a workspace id. When the config's
    `llm.provider` is anthropic they start `anthropic-account exec [--account NAME] -- vani …`, and
    the account comes from a Settings picker fed by `anthropic-account list --json`. That replaces
    the workspace field above, and it means a Keychain prompt is now the launcher's (`security`, up
    to 2 min). Two things are open: it has not been run live against the API, and it is not
    installed in `~/.local/bin` yet. Vani still ignores `ANTHROPIC_API_KEY_FALLBACK`, because
    `cmd/vani/wire.go` builds the adapter without arming the fallback.
  - **The host's Keychain read has no timeout.** An unanswered macOS prompt leaves a Chrome draft
    waiting until the extension's own timeout fires. On the anthropic path the read is now the
    launcher's, inside the draft's own 150 s ceiling.
