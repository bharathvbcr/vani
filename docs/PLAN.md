# Vani — a listings-first career engine (plan)

*Name decided 2026-10-18: Vani (Vāṇī, "voice"). Location still yours to confirm (see §10). Drafted 2026-10-18 from a code audit of
Manvi, Jarvis, DevPrism, Lappi-decision, n8n-email-engine and career, plus primary-source checks of
the open-source projects named below. Evidence labels: **[V]** verified in code/docs/CLI,
**[I]** inferred, **[U]** unverified — a Phase 0 spike.*

*Note 2026-10-20: this is the plan as drafted, kept as history. Where it says "Jev" as the cloud
decision model, read the decision model: the owner chose Liquid (`liquid/d1-ultra` over
OpenRouter) on 2026-10-19, and `decide/jev` is only the name of the OpenRouter client package.
Embeddings run through Tessl; Claude through Manvi. Current behaviour: [`RUNTIME.md`](RUNTIME.md),
the README and [`FOLLOWUPS.md`](FOLLOWUPS.md).*

## 0. What this is

A personal Go daemon that, every day, finds open roles at companies you care about, drops the
dead/duplicate/ineligible ones without spending tokens, ranks the rest against your career
knowledge base with DevPrism's deterministic scorers, and hands you a short digest with an
explanation per role and — on request — a tailored 2-page PDF. Application form filling is
**out of scope for now** (§9 keeps the design door open).

Manvi is the orchestrator: its Claude/Gemini agent loop plans searches, handles boards it has never
seen, writes the "why this role" notes, and triages anything the pipeline can't classify. Routine
steps are deterministic Go, so they are cheap, repeatable and auditable.

## 1. What already exists (and what doesn't)

| Need | Existing piece | State |
|---|---|---|
| LLM orchestration | Manvi `agent.Loop` + `tools.Registry`; providers anthropic, gemini, local (Ollama/MLX), xai (`cmd/manvi/providers.go:41-85,276-278`) | [V] — but `buildProvider` is in `package main` |
| Spend control | `manvi/llm/budget` ledger, reserve-before-send, `budget.Provider` wrapper (`llm/budget/provider.go:28`) | [V] — CLI wires it for anthropic only; Jarvis wraps gemini itself (`Jarvis/internal/app/assist.go:128`) |
| Safe outbound HTTP | `manvi/fetch`: https-only, allowlist (`MANVI_FETCH_HOSTS`), DNS-pinned dialer, private-IP block on every hop | [V] — `vetURL` unexported (`fetch.go:279`) |
| Untrusted-content framing | `BEGIN/END UNTRUSTED WEB CONTENT` (`manvi/devcouncil/web_tools.go:117,123`) | [V] |
| Secret inputs never seen by the model | `workflow.Ref{Source:"input"}`, `Parameter.Sensitive`, `PrivacyPolicy.Watched`, credential scrubber | [V] |
| Resume + ATS | DevPrism headless MCP (`--mcp`, `--mcp-http`): `resume_analyze_jd`, `resume_gap_analysis`, `resume_score_and_select`, `resume_synthesize`, `resume_ats_check`, career KB tools; **no model calls inside** | [V] in source; installed binary's `--mcp` is [U] |
| Career facts | rkb `bank.json` + `Claims.tex` + lint (claims, pronouns, 2 pages); DevPrism `career.db` (separate, no link to rkb) | [V] — two KBs, see D1 |
| Decision model (local) | Lappi `qd-metal-serve`, socket contract, Go client in DevCouncil (`devcouncil/lappi/`) | [V] — **serves none of the caller families today** (`caller-contract.md:10-14`) |
| Decision model (cloud) | Jev `typesafe/jev-1.13` via OpenRouter, inline in n8n-email-engine (`jev_target.go:17-18`) | [V] — no ctx, no spend cap, `package main` |
| Email classification | n8n-email-engine (Go): Gmail labels incl. `02_Applications`, `01_Action/Interviews` | [V] — writes no tracker |
| Headless browser | Lightpanda 1.1.0-nightly at `/opt/homebrew/bin/lightpanda`: `fetch --dump markdown/structured_data`, `mcp`, `--http-proxy`, `--block-private-networks` (after DNS), `--block-cidrs`, `--block-urls`, `--obey-robots` | [V] CLI help |
| Daemon / scheduler / triggers / notifications | — | **none** in Manvi or Jarvis [V for files read] |
| Listing sources, dedupe, liveness, ledger, LCA data | — | none |

## 2. Architecture

```
                 ┌──────────────────────── vanid (Go daemon, launchd) ───────────────────────────┐
  schedule ─────▶│ scheduler ─▶ pipeline (per posting state machine, deterministic)              │
  CLI/TUI ──────▶│                │                                                              │
  email events ─▶│                ├─ sources/   greenhouse · lever · ashby · workday · lightpanda │──▶ egress proxy ─▶ internet
                 │                ├─ decide/    router: Lappi ▸ Jev ▸ LLM  (typed readings)       │
                 │                ├─ resume/    typed DevPrism MCP tools (stdio child process)   │
                 │                ├─ ledger/    JSONL canonical + derived index                  │
                 │                └─ notify/    digest, macOS notification, CLI inbox            │
                 │ orchestrator/  Manvi agent.Loop (Claude or Gemini, budget-wrapped)            │
                 │                bounded turns; restricted tool registry on untrusted content  │
                 │ vault/         profile values in Keychain; never model-visible               │
                 └───────────────────────────────────────────────────────────────────────────────┘
```

Language: **Go** for the whole engine (daemon, orchestration, network concurrency; matches Manvi
and Jarvis, and Manvi is imported as a library). No Rust in v1: nothing here is CPU-bound, and the
heavy pieces (DevPrism scoring, Lappi) are already Rust behind process contracts. No Python:
`intake_jd.py`'s job (keyword extraction, bullet ranking) is retired in favour of DevPrism's
`resume_analyze_jd` / `resume_score_and_select`.

Process boundaries (each versioned, none is an in-process import except Manvi):

| Peer | How | Contract |
|---|---|---|
| Manvi | Go library import (pinned in `go.mod`) | Go API |
| DevPrism | child process `claude-prism-desktop --mcp`, JSON-RPC over stdio | MCP tool schemas, pinned by tool-list hash at startup |
| Lappi | unix socket `$HOME/Library/Caches/qd/qd.sock` | `docs/schema-api.md` v1 |
| Jev | HTTPS via egress proxy | OpenRouter decisions API |
| Lightpanda | child process (`fetch` one-shots; `mcp --port` for sessions) | CLI flags + MCP |
| n8n-email-engine | event file it appends (new, §8) | `vani.event` v1 JSON lines |
| Jarvis | not used in v1 (§9) | `jarvis.*` serve ops when needed |

## 3. Where the LLM orchestrator sits

| Stage | Deterministic Go | Decision router (§4) | Manvi LLM (Claude/Gemini) |
|---|---|---|---|
| Plan the day | run the saved watchlist | — | propose new companies/queries from your targets and recent outcomes; you approve additions |
| Find postings | ATS JSON APIs, sitemaps, JSON-LD | `browser.tier_route` when a career page's type is unknown | handle a board it has never seen: read Lightpanda's tree, write a **source adapter spec** (selectors + pagination), which is saved and replayed without the LLM next time |
| Filter | liveness, dedupe, location/remote, seniority keywords | `career.jd_triage` slots: role_fit, sponsorship, work_mode, seniority | only when every decision backend abstains |
| Rank | DevPrism `resume_gap_analysis` + `resume_score_and_select` (deterministic scores) | — | — |
| Explain | — | — | 3-line "why this role / what's missing" grounded in the gap report |
| Tailor (on request) | DevPrism `resume_synthesize` (page_budget 2), `resume_ats_check` | — | draft bullet rewrites → DevPrism `resume_rewrite_bullets` verifies metrics survive |
| Exceptions | — | — | triage failures, propose adapter fixes |

The LLM is the brain for anything open-ended; it is not consulted for things a parser or a
classifier can answer. This keeps daily cost near zero and makes each digest reproducible from
the ledger.

## 4. The decision layer

**Backends, in order:** Lappi (local) → Jev (cloud; the "cloud decision model" — correct me if you
meant another) → Manvi LLM with a structured-output schema. One Go interface:

```go
type Question struct {
    Task     string            // e.g. "career.jd_triage"
    Context  []byte            // untrusted; caller-capped
    Slots    []Slot            // name + options (≤16) supplied by the caller
    Facts    map[string]string // structure-only narrowing record (caller-contract §4)
}
type Answer struct {
    Slots   map[string]SlotReading
    Backend string            // "lappi" | "jev" | "llm"
}
type SlotReading struct {
    Reading Reading           // Answered | Abstained | Refused | Failed | Unavailable | NotAsked
    Value   string            // always one of the caller's options, or empty
    Lappi   *LappiCalib       // Score (margin) + ConformalSet
    Jev     *JevCalib         // Probability
    LLM     *LLMCalib         // none; LLM answers are never auto-accepted below a human check on hard filters
}
```

Invariants (each gets a test):

1. **Rule 7 by construction.** `Value` must be one of the caller's options or empty; the parser
   rejects anything else. `decide` has no type that means "allow"; the policy/approval packages
   never import `decide` (import-boundary test). Decisions narrow and rank; Go code decides.
2. **No mixed calibration.** There is no `Confidence float64`. Thresholds are per backend and per
   slot, stored in config, chosen from the eval set (below).
3. **Don't ask Lappi what it can't answer.** At startup the router reads Lappi's served
   `trained_families`; untrained families skip the socket entirely (no refusal round-trip). Today
   that is every family, so Jev answers everything until v6 ships. A "list served families" socket
   op is [U]; the fallback is to read the release manifest the server loaded, or probe each family
   once at startup and cache the `task_not_trained` refusal.
4. **Byte-identical prompts.** Lappi only answers questions rendered exactly as trained. The
   router's renderer and the `synth_career.rs` generator share one fixture file (task, question,
   slot names, option strings). Lappi-decision owns it; the engine keeps a pinned copy with a
   checksum test (the way GitPulse vendors DevCouncil contracts), and each repo asserts byte
   equality against it.
5. **Jev is wrapped before it's trusted in a daemon.** New `decide/jev` client: context
   cancellation, one whole-call deadline, bounded retries (429/5xx only), `usage.cost` charged to a
   `jev` key in the engine's budget ledger with a daily cap. Extract from n8n-email-engine into a
   shared module (§8) so the email engine uses the same client.
6. **Hard filters are conservative.** A slot that would *drop* a posting (sponsorship =
   no_sponsorship / citizenship_required, role_fit = mismatch) only drops on an Answered reading
   above threshold. Abstained/low-confidence keeps the posting and marks it "unsure" in the
   digest. A wrong keep costs you ten seconds; a wrong drop is invisible. The drop itself is the
   engine's own rule keyed on the annotation, so the decision model only annotates (Rule 7).

**Training vs evaluation (your "Synthetic only" ruling stands).** Lappi trains on generated data
only (`synth_career.rs`, alongside `synth_jarvis.rs`). Real postings flow through as **evaluation**:

- A hand-labelled eval set of **150 real postings**, labelled per slot by you in a small CLI review
  loop. Git holds canonical keys, labels and JD text hashes under `eval/jd_triage/`; the JD text
  itself (employer-copyrighted, sometimes with recruiter contacts) lives in the gitignored state dir
  and is re-fetched on demand.
- **Disagreement sampling grows it for free:** whenever two backends disagree on a slot (Jev vs LLM
  now; Lappi vs Jev later), the posting goes to a "label me" queue in the digest. Ten seconds per
  label, and the eval set concentrates on the hard cases.
- **Per-family promotion:** Lappi takes over a family only when, on the eval set, its
  Answered-reading precision meets the threshold Jev meets, at no worse coverage. Until then it runs
  in shadow (asked, recorded, not used).
- Whether public job postings may also become *training* data is a change to your ruling — see D3.

**Families to add to Lappi v6** (in `crates/qd-prep`): `career.jd_triage` (role_fit, sponsorship,
work_mode, seniority — options exactly as in the shared fixture), `browser.tier_route`. Template
file names are **not** options (they change); "base persona" uses DevPrism persona ids, which are
stable.

## 5. The per-posting pipeline

```
discovered ─▶ canonicalized ─▶ live? ─▶ new? ─▶ triaged ─▶ scored ─▶ explained ─▶ in digest
                    │             │        │        │          │
                    └─dead────────┴─dup────┴─dropped/unsure───┴─(tailor on request)─▶ pdf
```

- **Canonical key:** `ats:board:job_id` (Greenhouse `gh:{token}:{id}`, Lever `lv:{company}:{id}`,
  Ashby `ab:{org}:{id}`, Workday `wd:{tenant}:{site}:{jobReqId}`); fallback
  `sha256(norm(company)|norm(title)|norm(location))`. Cross-source duplicates collapse onto the ATS
  key. (JobSpy has no dedupe at all [V], so this is ours.)
- **Liveness before any token:** ATS API 404/closed, JSON-LD `validThrough` in the past, or the
  posting missing from its board's list two runs in a row → dead.
- **Change detection:** store a hash of the normalized JD; a changed hash re-triages, an unchanged
  one never costs anything again.
- **Sponsorship:** the JD-text slot is the filter. A local DOL LCA table (FY disclosure files,
  employer name per row [V]) adds a **badge** ("~N LCA filings, last 3 FY"), never a filter —
  employer-name matching is fuzzy. DOL ships `.xlsx`: either a Go xlsx reader (a new dependency,
  ask first) or a one-off throwaway conversion to CSV per fiscal year (allowed under the Python
  policy's throwaway role) that Go then reads. Recommend the CSV conversion; it runs four times a
  year.
- **Ledger:** append-only JSONL per posting event under the engine's state dir (canonical,
  diffable, survives index loss) plus a derived index rebuilt on start. No dcstore, no devmap
  schema: nothing here is subject to `check:schema-parity`. (SQLite as the index is D4.)

## 6. Design problems and how each is handled

| # | Problem | Evidence | Mitigation |
|---|---|---|---|
| P1 | Lappi serves no caller family; the "~120 ms, $0" premise is false today | `caller-contract.md:10-14` [V] | Router (§4): Jev answers now; Lappi shadow → per-family promotion after v6 |
| P2 | Lappi trains synthetic-only, so live traffic can't train it | `caller-contract.md:107` [V] | Synthetic generators train; real postings evaluate; D3 if you want to widen |
| P3 | Lappi needs byte-identical prompt shapes | decision-client survey [V] | Shared fixture + equality test in both repos (§4 inv. 4) |
| P4 | Jev score (probability) ≠ Lappi score (margin + conformal set) | `main.go:609-615`, `schema-api.md:347-354` [V] | Per-backend calibration fields, per-backend thresholds (§4 inv. 2) |
| P5 | Jev client has no ctx, no spend cap, lives in `package main` | `main.go:547,778`; no cap [V] | Shared `decide/jev` module with cancellation + budget ledger (§4 inv. 5) |
| P6 | Rule 7: a decision must never authorize | `caller-contract.md:55-57` [V] | Options-only answers, no "allow" type, import-boundary test; hard filters conservative (§4 inv. 1, 6) |
| P7 | Out-of-process fetchers escape Manvi's allowlist | `fetch.go:13-17` [V] | Engine egress proxy (CONNECT allowlist + private-IP block using Manvi's pinned dialer); Lightpanda runs with `--http-proxy` **and** `--block-private-networks`; Lightpanda's own blocking is a second wall, not the only one |
| P8 | Lightpanda's proxy handling of HTTPS (CONNECT) and subresources | not documented in help [U] | Phase 0 spike: point it at a logging proxy, load a page with third-party subresources, confirm every request appears |
| P9 | `vetURL` unexported; `buildProvider` in `package main` | `fetch.go:279`, `providers.go` [V] | Phase 1 doesn't need either (`fetch.New` and the provider adapters are exported). Phase 2: export an `egress.Policy` (vet + pinned dial) for the proxy (§8) |
| P10 | Manvi wires the budget ledger only for anthropic | `cmd/manvi/budget.go:76-94` [V] | Engine wraps whichever provider it builds (Claude or Gemini) in `budget.Provider`, plus a `jev` key; daily caps per key |
| P11 | MCP tools reach the model as generic `mcp_call_tool` | `devcouncil/mcp_tools.go:14-52` [V] | Engine registers **typed** Go tools (fixed schemas) that call DevPrism over MCP underneath; the model never gets raw `mcp_call_tool` |
| P12 | DevPrism tool calls carry PII (`header_name`, `contact_lines`) | `tools_resume.rs` [V] | Those parameters are not in the typed tool's model-visible schema; the wrapper fills them from the vault after the model decides to call |
| P13 | Two career KBs (rkb vs DevPrism `career.db`) | no references between them [V] | D1 decided: rkb canonical → one-way idempotent sync into `career.db`; rkb lint runs upstream |
| P14 | Two page rules (rkb: exactly 2; DevPrism `page_budget` 1–4) | `lint.go:215`, tool schema [V] | Typed wrapper pins `page_budget: 2`; verify PDF page count after render |
| P15 | Fabricated metrics in tailored text | DevPrism `metrics.rs`, `resume_rewrite_bullets` [V] | LLM drafts only; DevPrism accepts a draft only if every metric survives and none is new; rkb `{{clm}}` claims remain the source |
| P16 | Prompt injection through JD/page text | general | Turns whose context holds fetched content run with a restricted registry (`ReadOnly`, no ledger-approve, no notify-send, no fetch) and the UNTRUSTED framing; outputs are parsed into typed fields, never executed |
| P17 | Manvi has no daemon, scheduler, triggers or notifications | survey [V] | Engine owns them: launchd agent (memguard's pattern), in-process scheduler, event-file trigger, macOS notification + CLI inbox |
| P18 | Embedding Manvi transitively pins DevCouncil; Manvi changes bump Jarvis's pin | `go.mod`, cross-app rule | Engine pins Manvi like Jarvis does; every Manvi export (P9) is reported as a delta and asked before Jarvis bumps; engine reads no DevCouncil-owned schema |
| P19 | Scraping aggregators (LinkedIn/Indeed/Glassdoor) risks account bans and ToS | JobSpy's board list [V] | ATS-direct and company career pages only; `--obey-robots`; per-host rate limits (`--http-nav-burst`, own token bucket) |
| P20 | Workday listing endpoint is undocumented, caps `limit` at 20, `postedOn` is a phrase | community sources [U-ish] | Isolated adapter with paging by 20, `Accept: application/json` + `Accept-Language`, parse relative dates, contract test against a saved fixture; fails soft |
| P21 | Lever/Ashby expose no question schema; Ashby no form definition | postings-api README, Ashby docs [V/U] | Irrelevant for listings v1; matters only for §9 |
| P22 | CAPTCHAs / bot walls | — | Never solved. Posting marked "needs browser", link goes in the digest for you |
| P23 | Lappi 16-option cap | `schema-api.md:156-183` [V] | Callers prefilter and record the narrowing in `facts` |
| P24 | DevPrism's installed binary may not expose `--mcp`; source build fails locally (tectonic) | DevPrism survey, memory [U] | Phase 0 smoke test; the resume engine is Typst (no tectonic needed on the MCP path) but the binary still links it |
| P25 | Stale DevMap indexes in DevPrism (schema 26 vs 27) and n8n-email-engine (23 vs 27) | `devmap_status` [V] | Rebuild before Phase 1 edits in those repos |

## 7. Borrowed from open source (patterns, not code)

| Project | Licence [V] | Pattern we re-implement | Lands in |
|---|---|---|---|
| Career-Ops | MIT | zero-token liveness check before spend; human keeps final submit | `pipeline/live` |
| JobSpy | MIT | one adapter per source, fan-out with non-fatal per-source errors, normalized posting model | `sources/*` (we add the dedupe it lacks) |
| Resume-Matcher | Apache-2.0 | deterministic weighted ATS score; render-then-parse check | already in DevPrism (`gap.rs`, `ats_sim.rs`) — use, don't rebuild |
| Stagehand | MIT | observe-then-act with cached actions | §3 "source adapter spec": LLM writes it once, Go replays it |
| Firecrawl | AGPL-3.0 | `/map` site discovery; "decision model before LLM" retry (#5180) | sitemap/robots discovery in `sources/site`; router order in `decide` (we don't run Firecrawl) |
| Lightpanda | AGPL-3.0 | run as a separate process only | `sources/lightpanda` |
| JSON Resume | MIT (repo archived 2026-06-12; schema moved) | interchange format for KB sync | `resume/sync` if needed; pin the version |
| OpenResume | AGPL-3.0, stale since 2024 | feature-scored resume parser | not needed (DevPrism `ats_sim`) |
| ApplyPilot | AGPL-3.0 | `work_authorization` / `eeo_voluntary` / `resume_facts` profile split | `vault` schema (§9) |
| Formhand | MIT, 0 stars | per-ATS fill adapters, never presses Submit, saved Q&A bank | §9 only |

Running AGPL tools as separate local processes for personal use is fine. Copying their code into
your repos is not, so nothing is copied.

## 8. Changes needed in other repos (each needs your OK)

**Phase 1 touches no other repo and bumps no pin.** It uses what Manvi already exports:
`fetch.New(allowed, limits)` / `(*Client).Fetch(ctx, url)` (`fetch.go:125,204`) for the ATS JSON
APIs, and `gemini.New` / `anthropic.New` (`llm/gemini/adapter.go:30`, `llm/anthropic/adapter.go:105`)
wrapped in `budget.Provider`, the way Jarvis does it. Whether `fetch.Document` carries the raw JSON
body or only extracted text is [U] — Phase 0 checks it; if it only carries text, Phase 1 needs a
small Manvi addition after all and it moves into this table's Phase 1 row.

| Phase | Repo | Change | Why |
|---|---|---|---|
| 2 | Manvi | export `manvi/fetch` vetting + pinned dialer as an `egress.Policy`, usable by a CONNECT proxy | P7, P9 — only needed once Lightpanda runs |
| 2 | Manvi | (nice to have) move `buildProvider` out of `cmd/manvi` | the engine can construct adapters directly without it |
| 2 | Jarvis | Manvi pin bump after the above (deltas reported first) | cross-app rule |
| 2 | n8n-email-engine | append `vani.event` lines (`{v:1, kind:"application_email", label, thread_id, sender_domain, received_at}`, with no body and no address) when it labels `02_Applications*` / `01_Action/*` | ledger learns about replies without reading your mail |
| 3 | new shared module (e.g. `devtools/decide`, Go) | Jev client (extracted from n8n-email-engine) + Lappi client (moved from DevCouncil `devcouncil/lappi/`) behind §4's interface | one owner per client; DevCouncil, n8n-email-engine and the engine all use it. Until then the engine's Jev client lives in the engine and moves here |
| 3 | Lappi-decision | `synth_career.rs` + the shared prompt fixture; register `career.jd_triage`, `browser.tier_route` for v6 | P1–P3 |
| 0 | DevPrism, n8n-email-engine | rebuild DevMap indexes (no code change) | P25 |

## 9. Later, if needed: application filling

Kept out of v1. When you want it, the design that avoids the audit's problems:

- **Application packet** as the handoff object: the fields to fill (keys, not values), answers
  chosen from your approved Q&A bank (a decision picks *among saved answers*, which Rule 7 allows),
  the PDF hash, and a digest. Values come from the vault only at fill time.
- **Greenhouse needs no LLM:** its job-board API returns the form (`?questions=true`: field name,
  type, required, select values [V]). A Go template can generate a Jarvis Capability per board
  directly; Gemini Discovery is only for Lever/Ashby/Workday, where Lightpanda's `detectForms`
  reads the form.
- **Filling happens in your real browser,** with you watching and pressing Submit yourself. Gate
  spike: can Jarvis resolve a Greenhouse `<input>` by role/name in a Chrome window over AX? [U]
  If not, the fallback is a small MIT-pattern extension reading the packet from localhost.
- The browser stays out of `computer.Desktop`, so `Node.NativePath` / `Window` never change and no
  cross-app schema moves.

## 10. Decisions for you

| # | Decision | Recommendation |
|---|---|---|
| D1 | Canonical career KB: rkb `bank.json` or DevPrism `career.db` | **Decided 2026-10-18: rkb `bank.json` is canonical.** One-way, idempotent sync into `career.db` (via `career_upsert_block` / `career_add_facts`, keyed by a content hash so re-runs are no-ops); rkb lint runs before the sync; DevPrism scores and renders but never writes back. Edits made in DevPrism are overwritten on the next sync |
| D2 | Name and location of the new repo | **Name decided: Vani.** Location: `~/Code/apps/Vani` (it is something you run, per your folder taxonomy) |
| D3 | May public job-posting text (not personal data) become Lappi *training* data, or stay eval-only? | Eval-only for now; revisit after the first v6 family measures |
| D4 | Ledger index: JSONL only, or JSONL + SQLite (`modernc.org/sqlite`, pure Go — a new dependency) | JSONL only in Phase 1; add SQLite if queries get slow |
| D5 | Orchestrator default provider | Gemini (`gemini-3.8-flash`, already Jarvis's) for daily planning/notes; Claude for tailoring drafts — both behind the budget ledger. *Decided: Claude through Manvi (live config `claude-opus-5-5`).* |
| D6 | Daily spend caps | e.g. $1/day LLM, $0.25/day Jev to start. *Live 2026-10-20: $10/day LLM, $20/day decision model.* |
| D7 | Is "cloud decision model" Jev (`typesafe/jev-1.13` via OpenRouter)? | assumed yes. *Decided 2026-10-19: no — Liquid `liquid/d1-ultra` via OpenRouter.* |

## 11. Phases

**Phase 0 — spikes (one session, read-mostly).** Exit: each answered yes/no with evidence.
1. DevPrism installed binary: `tools/list` over `--mcp`; `resume_gap_analysis` on one public JD;
   `career_get_profile` / `career_list_personas` → how many blocks `career.db` holds today. If it
   is thin, either the rkb → `career.db` sync (D1) moves into Phase 1, or Phase 1 ranks on triage
   slots alone and DevPrism scoring joins in Phase 2.
6. `manvi/fetch`: does `Document` expose the raw response body (needed for JSON APIs)?
2. Lightpanda: HTTPS through `--http-proxy` (every subresource visible at the proxy) plus
   `--block-private-networks`.
3. Jev: key works, cost per triage call, latency.
4. Lappi: read served `trained_families` from the running server.
5. One public posting through each of Greenhouse, Lever, Ashby and one Workday tenant.

**Phase 1 — the listings engine.** Exit: a daily digest you actually read for a week.
- The engine repo only (no other repo changes, §8): daemon, scheduler, watchlist,
  Greenhouse/Lever (incl. EU host `api.eu.lever.co`)/Ashby adapters through `manvi/fetch`,
  canonical key + liveness + change detection, JSONL ledger.
- Decision router with Jev + LLM backends (Lappi joins in Phase 3: every family is refused today,
  so shadow-asking it now would record nothing); `career.jd_triage` slots; conservative hard
  filters; disagreement queue.
- DevPrism typed tools for gap analysis + score-and-select ranking (subject to Phase 0 spike 1);
  digest with explanations.
- Eval set seeded to 50 labels.

**Phase 2 — reach and quality.**
- Manvi `egress.Policy` export + engine egress proxy; then Lightpanda adapter for custom boards
  with LLM-written, replayable source specs; Workday adapter.
- On-request tailored 2-page PDFs (D1 sync in place; page count verified).
- LCA badge; n8n-email-engine events update the ledger (applied / interview / rejected).
- Eval set to 150.

**Phase 3 — the decision model earns its keep.**
- Shared `decide` module (Jev + Lappi clients); Lappi wired, shadow-asked.
- `synth_career.rs` + shared fixture; v6 training run including `career.jd_triage`.
- Shadow comparison on the eval set; per-family promotion; email engine moves to the shared router.

**Phase 4 — application filling (§9), only if you want it.**

## 12. Still unverified

- DevPrism `--mcp` on the installed binary (P24).
- Lightpanda HTTPS/CONNECT and subresource behaviour through a proxy (P8).
- Lever's lack of a question schema and Ashby's lack of a form endpoint come from README/docs
  summaries.
- Workday endpoint details come from community sources.
- Jarvis driving web content in Chrome over AX (only matters for §9).
- Coverage note: the code surveys used rg with default ignores (no `-uu`) and DevMap mostly for
  index status; "not found" above means not in tracked source.
