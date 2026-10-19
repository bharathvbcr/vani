<p align="center">
  <img src="assets/logo.jpg" alt="Vani Logo" width="160" height="160" style="border-radius: 28px;" />
</p>

# Vani

A personal, listings-first career engine. Each day it reads the job boards of companies you
watch, drops dead and duplicate postings without spending anything, triages the rest with a
decision model, ranks them against your career knowledge base with DevPrism's deterministic
resume tools, and writes a Markdown digest.

Plan: [`docs/PLAN.md`](docs/PLAN.md). Follow-ups: [`docs/FOLLOWUPS.md`](docs/FOLLOWUPS.md).

## Ingestion & Pipeline Architecture

```mermaid
flowchart TD
    subgraph Sources["1. Ingestion Sources"]
        GH["Greenhouse API"]
        LV["Lever US & EU API"]
        AB["Ashby API"]
        WD["Workday CXS API"]
        SS["Search sites: Amazon, Google,\nApple, Eightfold (PCSX)"]
        PG["Custom Pages (Lightpanda)"]
    end

    subgraph Egress["Egress Security Boundary"]
        EP["internal/egress.Proxy (Loopback CONNECT)"]
        AL["Host Allowlist & Pinned Dialing"]
        PB["Private Address Block"]
    end

    subgraph Pipeline["2. Pipeline Core"]
        RAW["Raw Board Postings"]
        DEDUP{"Deduplicate & Liveness"}
        DEAD["Dead (absent twice)"]
        DUP["Duplicate (cross-board mirror)"]
        LIVE["Live Unique Posting"]

        subgraph Triage["3. Decision & Triage"]
            TR["career.jd_triage\n(role_fit, sponsorship, work_mode, seniority)"]
            JEV["OpenRouter model (d1-ultra)"]
            LLM["LLM via Manvi (Gemini / Claude)"]
            LAP["Lappi (Local socket, free)"]
            SHADOW["Shadow & Eval (vani label / eval)"]
            DROP_RULE{"Drop Rule Triggered?"}
            DROP["Dropped Posting"]
            UNSURE["Kept (Unsure / Fallback)"]
            TRIAGED["Triaged Posting"]
        end

        subgraph Enrich["4. Badges & Ranking"]
            LCA["DOL LCA History Badge\n(Certified H-1B Counts)"]
            DEVPRISM["DevPrism MCP\n(resume_gap_analysis)"]
        end

        subgraph Output["5. Synthesis & Export"]
            EXP["Picked Note\n(OpenRouter model picks from reasons catalog)"]
            DIGEST["Daily Markdown Digest"]
        end
    end

    GH & LV & AB & WD & SS --> EP
    PG --> EP
    EP --> AL & PB --> RAW
    RAW --> DEDUP
    DEDUP -->|Missing twice| DEAD
    DEDUP -->|Same posting| DUP
    DEDUP -->|Active| LIVE

    LIVE --> TR
    TR --> JEV & LLM & LAP
    TR -.-> SHADOW
    JEV & LLM & LAP --> DROP_RULE
    DROP_RULE -->|Calibrated drop| DROP
    DROP_RULE -->|Uncertain fit| UNSURE
    DROP_RULE -->|Clear fit| TRIAGED

    TRIAGED & UNSURE --> LCA
    LCA --> DEVPRISM
    DEVPRISM --> EXP
    EXP --> DIGEST
```

## Posting Lifecycle State Machine

```mermaid
stateDiagram-v2
    [*] --> Discovered: Board Scrape / ATS API / Lightpanda
    Discovered --> Duplicate: Identical payload or weak key match
    Discovered --> Live: Unique posting verified
    
    Live --> Dead: Absent across 2 consecutive runs
    Live --> Triage: Advance per-run queue
    
    state Triage {
        [*] --> DecisionRouter
        DecisionRouter --> Lappi: 1. Local unix socket (free)
        DecisionRouter --> OpenRouter: 2. d1-ultra Decision API
        DecisionRouter --> ManviLLM: 3. Structured Claude/Gemini
        
        Lappi --> CalibrateDrop: Slot probabilities
        OpenRouter --> CalibrateDrop: Slot probabilities
        ManviLLM --> CalibrateDrop: Slot readings
    }
    
    CalibrateDrop --> Dropped: Hard rule matched (e.g. no sponsorship, bad fit)
    CalibrateDrop --> Unsure: Low confidence or backend abstention
    CalibrateDrop --> Triaged: Role fit confirmed
    
    Triaged --> Enriched: Add DOL LCA certified counts & IRS employer badges
    Unsure --> Enriched: Add DOL LCA certified counts & IRS employer badges
    
    Enriched --> Ranked: DevPrism MCP (resume_gap_analysis must-haves)
    Ranked --> Explained: OpenRouter picks reason from catalog
    Explained --> Digested: Daily Markdown report & Mac UI
```

- **Sources:** Greenhouse, Lever (US and EU), Ashby and Workday (cxs search, paged by 20)
  public job-board APIs, and the big-employer search sites: amazon.jobs (`az`, its search.json),
  Google Careers (`gg`, page 1 of results only, as its robots.txt allows), jobs.apple.com (`ap`)
  and Eightfold PCSX sites such as Microsoft's and Qualcomm's (`ef`). All go through [`internal/egress`](internal/egress) (https only, host allowlist, private-address
  block, pinned dialing). Custom careers pages (`pg` boards) are read by Lightpanda: one
  `lightpanda mcp` process per board, driven over stdio, whose traffic is forced through a per-run
  authenticated loopback CONNECT proxy that applies the same egress rules ([`docs/PHASE0-lightpanda.md`](docs/PHASE0-lightpanda.md)). JSON-LD `JobPosting`
  is read from each page's HTML. Weak keys (`pg`, `x`) dedupe onto the ATS posting they mirror.
- **Triage:** `career.jd_triage` (role_fit, sponsorship, work_mode, seniority) through a router
  over the OpenRouter decision model (`decide.openrouter_model`, e.g. `liquid/d1-ultra`, asked
  through the Decisions API) then the LLM. The model answers under a name derived from its id
  (`liquid/d1-ultra` → `d1-ultra`) in routing, ledger annotations, drop reasons and the eval.
  Answers only annotate; a posting is dropped only by a configured `drop_rules` entry, on a
  calibrated answer (`min_openrouter_prob` for the OpenRouter model, `min_lappi_score` for
  Lappi). Anything uncertain is kept and marked unsure.
- **Renamed keys (2026-10-19):** `jev_model`, `jev_key_env`, `daily_usd.jev`, `min_jev_prob` and
  `decide.shadow` `jev` are now `openrouter_model`, `openrouter_key_env`, `daily_usd.openrouter`,
  `min_openrouter_prob` and `openrouter`. Old configs and track files still load, every command
  says which keys to rename, a file setting an old and a new key to different values is refused,
  and saving writes the new names. Ledger rows written before the rename name the model `jev`;
  `vani eval` and `vani label` read those as the configured model's name and say how many they
  mapped. The daily cap file stays `<state dir>/budget/jev.json`, so the rename kept the day's spend.
- **Ranking:** DevPrism `resume_gap_analysis` must-have coverage (DevPrism started lazily over
  `--mcp`). Skipped while `devprism.binary` is empty.
- **Note ("why this role"):** picked, not written. `vani reasons draft` has the LLM draft, once, a
  catalog of short reasons for the target summary in three categories (why it fits, the biggest
  gap, what to check first), written to `<state dir>/reasons.json` for review and editing (`vani reasons import FILE` installs
  one drafted elsewhere, and `vani reasons check` validates a hand edit). For each
  scored posting the OpenRouter model picks one reason per category, or none, in one call ([`internal/decide/jdreasons`](internal/decide/jdreasons/jdreasons.go)),
  and its probability is shown as the relevance score. No model writes text per posting. Without
  a catalog, or with one drafted for another target summary, notes are off and scored postings
  reach the digest without one; with a catalog but no OpenRouter model this run, notes pause and the postings
  wait for a run that can pick.
- **Digest:** `<state dir>/digests/YYYY-MM-DD.md`, ranked roles then unsure ones. Each role shows
  where it is, its stated pay (`- Pay: USD 120k–160k/yr`, one short range per currency, the form
  `vani jobs list` prints, read from the description by fixed rules and left out when the posting
  states none), its link, must-have coverage, triage answers and the note. Every posting field is
  escaped so it cannot add links, headings or lines.
- **Decision clients** live in the shared module `~/Code/devtools/decide` (types, router, budget
  caps, the Jev and Lappi clients). The question itself is [`internal/decide/jdtriage/fixture.json`](internal/decide/jdtriage/fixture.json):
  task, exact question text, context layout and options, plus one single-slot Lappi family per
  slot with its own exact question (Lappi trains one slot per example). The OpenRouter model and the LLM get all
  slots in one request; Lappi gets one request per slot, and an absent or hung agent costs one
  timeout per posting. The target-roles summary is rendered into the context, so question text
  never varies.

## Decision Tier & Shadow Evaluation

```mermaid
flowchart TD
    subgraph Input["Input Posting"]
        P["Triaged Posting Context\n(Target summary + Job description)"]
    end

    subgraph PrimaryChain["Primary Decision Chain"]
        direction TB
        LAP["1. Lappi (Local Unix Socket)\nq/d-metal-serve, zero-cost"]
        OR["2. OpenRouter Decisions API\nliquid/d1-ultra (calibrated probabilities)"]
        LLM["3. Manvi LLM Loop\nClaude / Gemini (Structured output)"]
        
        LAP -->|Socket missing / untrained| OR
        OR -->|Cap reached / timeout| LLM
    end

    subgraph ShadowSystem["Shadow & Evaluation Pipeline"]
        SHADOW["Shadow Backend (decide.shadow)\nSampled 1-in-N postings"]
        DISAGREE{"Disagreement\nDetected?"}
        LABEL["vani label\n(Human ground truth labels)"]
        EVAL["vani eval\n(Precision & promotion gate)"]
        PROMOTE["Config Promotion\n(e.g. decide.lappi.primary: true)"]
        
        SHADOW --> DISAGREE
        DISAGREE -->|Disagreements prioritized| LABEL
        LABEL --> EVAL
        EVAL -->|Lower bound >= 0.4 on N >= 30| PROMOTE
    end

    subgraph Outcomes["Triage Outcome"]
        DROP_RULE{"Drop Rule Triggered?"}
        DROP["Dropped (calibrated probability)"]
        UNSURE["Unsure (kept, highlighted in digest)"]
        FIT["Fit (advanced to ranking)"]
    end

    P --> PrimaryChain
    P -.->|Sampled| ShadowSystem
    PrimaryChain --> DROP_RULE
    DROP_RULE --> DROP & UNSURE & FIT
```

- **Shadow, eval and promotion:** `decide.shadow` (`openrouter`, `llm`, `lappi` or `local`) asks a second backend
  alongside the chain and records its reading, backend and any disagreement; it never decides.
  `vani label` labels postings (disagreements first) and `vani eval` reports per-backend precision
  and coverage, plus a promotion verdict per slot and primary. A shadow that errored or timed out
  is recorded as `unavailable` (so it still counts as asked, and shows in a "shadow unavailable"
  column), and each shadow backend, which for a local model includes the model and question
  mode, is its own row. The shadow is promotable when, on at least 30 labeled postings it was
  asked about, it answers at least as often as the primary, and then, on the postings both
  answered (at least 30), there are at least 10 where exactly one was right and a one-sided 95%
  exact lower bound on the shadow's share of those is at least 0.4. A shadow that agrees with the
  primary almost everywhere reads "insufficient disagreements", never "promotable".
  Promotion is a config change (`decide.lappi.primary: true` puts Lappi first), never automatic.
- **Lappi** (`decide.lappi`, socket default `~/Library/Caches/qd/qd.sock`): local and free. With no
  agent running every ask reads `unavailable/socket_not_found` and the chain carries on; the first
  `task_not_trained` refusal stops Vani asking for the rest of the process.
- **Local decision model** (`decide.local`, shadow only; config refuses it anywhere else): a
  llama.cpp server's `/v1/systemone` (for example LiquidAI d1-3B, llama.cpp ≥ 0.7.0), asked
  through the Jev client under the name `local-<model>-<combined|per_slot>` (the GGUF file name
  without `.gguf` when Vani starts the server; at most 32 bytes in all, which `vani config
  check` enforces; rows recorded earlier are filed as plain `local`) with no key, over `egress.Loopback`, which dials
  only loopback addresses. Either `endpoint` (a server you keep running) or `server` (`binary`,
  `model_path`, optional `context_size`, `start_timeout_ms`, and `batch_size`/`parallel` for -b/-ub
  and -np, which a vllm-d3 model needs at 8192 and 1): Vani then starts `llama-server` on
  a free 127.0.0.1 port for each run, Metal only (`--device MTL0 -ngl all`; no CPU fallback), with
  only `PATH`/`HOME`/`TMPDIR`, waits for `/health`, and
  stops it when the run ends. A server that does not come up costs the run its shadow, never the
  run. `questions: per_slot` sends each slot its Lappi family's question; the default `combined`
  measured better on d1. Measuring d1 is `vani run-once` (with the chain's keys), then
  `vani label -n 30` and `vani eval`.
- **Pipeline health (`vani status --json`, the Mac app's Pipeline tab):** every run, from the
  daemon or `run-once`, writes `<state dir>/runs/last.json` when it starts and when it ends:
  outcome (`ok`, `failed`, `cancelled`), the stage it stopped at (pipeline or digest), a one-line
  error, and the ledger's counts by status before and after. `status --json` adds the ledger, the
  schedule, today's spend and remaining cap per paid backend (UTC day), and the decision-model
  setup. It reads without the ledger lock, so it answers while the daemon runs. Each section has
  its own `ok` and `reason`: a section that could not be read is reported as unknown with the
  reason, never as zero or fine. A record that started but never ended is `running` only while its
  process lives and is under a day old, otherwise `interrupted`. The app flags a failed or
  interrupted run, an unreadable section, a spent budget and a run older than 36 hours in its
  sidebar. A run the daemon skips for starting too late is logged but not recorded.
- **Browsing aids (`vani jobs`, the Mac app's Jobs tab):** every role carries a freshness date
  and a relevance score with its basis. Display-only: they sort and plot, and never drop a role.
  - Freshness is the board's posted date. Greenhouse's comes from `first_published`. A board with
    no date falls back to when Vani first saw the role, and that fallback is marked approximate.
  - Relevance (0-100) comes from DevPrism's rank, or triage's role_fit when there is no rank,
    plus overlap with the target summary's terms.
  - Priority is relevance at most halved by age (`--half-life`, default 14 days).
  - Keywords come from a deterministic, bounded extractor ([`internal/jobinsight`](internal/jobinsight)).
    It uses an embedded vocabulary with stop phrases ("Spark Capital" is not Spark). A term at
    least 90% of one company's postings repeat is that company's blurb, and is left out.
  - Each keyword is marked when the target summary names it, or when DevPrism found it missing.
  - `jobs list` adds `--sort priority|relevance|fresh|company|salary` and filters for fit, work
    mode, sponsorship, seniority, age and closed roles, plus `--company` / `--exclude-company`
    (exact names), `--exclude` (title words) and `--min-relevance`. `jobs stats` returns the
    distributions the app's Insights charts draw.
  - Pay ([`internal/jobinsight/pay.go`](internal/jobinsight/pay.go)): each job's description is
    read for stated pay by fixed rules (currency, range, period, a pay word, plausible yearly
    bounds; "$100/month budget" or "$50M raised" is not pay), at most one range per currency, as
    `pay` with yearly figures. `--min-salary 150k` compares the top of the range in `--currency`
    (default USD, no exchange rates): a job stating no pay stays unless `--salary-listed`, and a
    job stating pay only in another currency is left out. `--sort salary` and the stats' `salary`
    buckets use the same currency. A bare "$" reads as USD. The Lever adapter adds the board's
    closing text (`additionalPlain`, where Lever boards state pay), `salaryDescriptionPlain` and
    `salaryRange`; Ashby is fetched with `includeCompensation=true` and its salary component leads
    the description unless the company hides pay on its postings.
  - Location ([`internal/location`](internal/location)): each job's board location is read into
    `places` (area plus US state code, or area plus the region as written) and `remote_listed` /
    `remote_scope` ("Remote (US)"). That is what the board says, not triage's `work_mode` reading.
    `--state TX,new mexico|none`, `--area "El Paso"|none` and `--remote` filter on it
    (`--or-remote` with a state or area keeps remote roles too: "remote, or in Texas"), `jobs stats`
    counts states, areas and remote listings, and the digest's "Where:" line uses it, falling back
    to the board's text. There is no home area or city table: an area is the place as written.
- **Local-model bench:** `vani bench local` replays postings the primary already read through
  one or more local GGUF models. It uses the run's exact request and reproduced the recorded
  shadow 382/382. It reports per-slot agreement with the primary, the confusion between them,
  agreement by probability, and latency. Metal only, no key, never writes the ledger. Agreement
  is not accuracy; that is still `vani label` and `vani eval` ([`docs/FOLLOWUPS.md`](docs/FOLLOWUPS.md)).
- **Sponsorship badge:** `vani lca import` loads DOL LCA disclosure CSVs ([`docs/LCA.md`](docs/LCA.md)); each
  posting gets a certified-H-1B-history badge. A badge, never a filter.
- **Employer badge:** `vani employer import` loads the IRS exempt-organizations list and the IPEDS
  directory ([`docs/EMPLOYER.md`](docs/EMPLOYER.md)); each posting gets a "likely cap-exempt" /
  "likely non-profit" badge. It never filters; a track's employer preference only adds to rank.
- **Tracks:** `vani track list|show|use` applies a track decider's target summary and drop rules,
  keeping sponsorship and other track-independent rules ([`docs/TRACKS.md`](docs/TRACKS.md)).
- **Knowledge base:** `vani kb sync` copies rkb's `bank.json` (claims resolved by
  `rkb claims export`) one way into DevPrism with `career_upsert_block`. Hash-tracked, never
  deletes, never confirms an overwrite DevPrism asks about. `--dry-run` reads no live DevPrism
  state and sends nothing.

- **Applying (browser extension):** [`extension/`](extension/) saves your answers when you submit an
  application and fills the next form's known questions, marked green or yellow. It never submits
  ([`extension/README.md`](extension/README.md)). Chrome and Safari share one answer bank through the Vani Apply Mac app
  ([`macos/`](macos/): SwiftUI, a Chrome native messaging host and a Safari app extension), which is also
  where answers are viewed, added, edited and deleted ([`macos/README.md`](macos/README.md)). Without the app,
  answers stay in the browser.
- **Drafting:** `vani draft` reads one JSON request on stdin (question, posting, word limit) and
  writes one JSON reply. It drafts a free-text answer with one bounded LLM turn from the posting and
  the most relevant knowledge-base bullets, never personal data, and charges the same daily LLM
  ledger as the pipeline. The posting is split at its headings ("About <company>", "About the role",
  "Responsibilities", "Logistics", ...): the excerpt sent leads with the company and role sections
  wherever they sit and leaves out pay, benefits, logistics and legal text. A motivation question
  ("Why <company>?", "What excites you about this role?") ranks evidence against the role's own work
  first and the target summary second, and the model is told to tie it to this company's mission and
  this role, never in an answer another company could receive unchanged. What the seeker values is
  rarely in resume bullets, so the optional `motivation_notes` in the config (the owner's own words
  on what they care about, at most 2,048 bytes after whitespace collapse) goes to motivation
  questions only, as trusted text labelled as the seeker's, and the model may use those reasons
  where they meet the posting but add none. It enters a prompt, so like `target_summary` it must be
  non-identifying: no name, contact details, location, work authorization, or names of people,
  employers or schools. It does not affect which evidence is picked. Every evidence list skips
  rewordings of a bullet already sent and takes at most three bullets per role. Failures carry a code: `bad_request`, `budget_exhausted`, `timeout`,
  `provider`, `empty_reply`, `config` or `kb`. The Mac app and the Chrome host run it.
- **Anthropic models:** current Claude models reject `temperature` and always reason.
  `llmbackend.Shape` therefore drops temperature for Anthropic and asks for low effort, with
  1,024 tokens of reasoning headroom. Every single-turn caller (triage, the reasons-catalog draft, draft) goes
  through it.

```mermaid
flowchart LR
    subgraph Browsers["Browser Layer"]
        CR["Chrome Extension\n(MV3 Content Script & Background)"]
        SF["Safari Web Extension\n(Content Script & Background)"]
    end

    subgraph NativeBridge["Native Host & Plugin Layer"]
        HOST["vani-apply-host\n(Chrome Native Messaging)"]
        APPEX["Safari App Extension\n(VaniApplyExtension.appex)"]
    end

    subgraph MacApp["macOS Control Layer"]
        APP["Vani Apply.app\n(SwiftUI Management UI)"]
        STORE["VaniApplyKit BankStore\n(Advisory Lock + Atomic CAS)"]
        BANK[("bank.json\n(App Group Container)")]
        LOG[("activity.jsonl\n(Event Names & Counts Only)")]
    end

    CR -->|Native Messaging JSON| HOST
    SF -->|sendNativeMessage JSON| APPEX
    HOST --> STORE
    APPEX --> STORE
    APP --> STORE
    STORE --> BANK
    STORE --> LOG
```

## Use

```bash
go -C /Users/bharath/Code/apps/Vani build -o build/vani ./cmd/vani
build/vani init
build/vani watchlist add --ats gh --token <board> --company <Name> --allow-host boards-api.greenhouse.io
build/vani config check
build/vani run-once
build/vani status
build/vani lca import /absolute/path/LCA_FY2025.csv
build/vani employer import /absolute/path/HD2024.csv
build/vani track use ml-research
build/vani label -n 20
build/vani eval
build/vani jobs list --sort priority --fit strong_fit,partial_fit --max-age 14
build/vani jobs list --min-salary 150k --seniority mid,senior --exclude manager --sort salary
build/vani jobs stats --view all
build/vani bench local --model /abs/candidate.gguf --model /abs/d1-3B-Q8_0.gguf
build/vani kb sync --dry-run --bank /abs/bank.json --rkb /abs/rkb --code-root /abs/Code
build/vani launchd plist --binary /absolute/path/to/vani
```

`launchd plist` prints an agent definition and installs nothing. It runs `vani daemon` at load and
restarts it only after a failed exit, at most once every 5 minutes (`KeepAlive` with
`SuccessfulExit` false, `ThrottleInterval` 300). A ledger that can no longer be written safely (a
failed append that could not be cut back, a failed fsync) ends the daemon with a non-zero exit and
a line in `vani.err.log`; the restarted daemon reopens the ledger. A clean stop stays down. An
agent installed from an older plist (`KeepAlive` false) keeps the old behaviour until it is
replaced: write the new output to `~/Library/LaunchAgents/<label>.plist`, then
`launchctl bootout gui/$UID ~/Library/LaunchAgents/<label>.plist` and
`launchctl bootstrap gui/$UID ~/Library/LaunchAgents/<label>.plist`. launchd gives an agent no
shell environment, so the plist pins the config file and state folder this command resolved: its
`EnvironmentVariables` holds exactly `VANI_CONFIG` and `VANI_STATE_DIR` (the variables' values when
set, else the defaults below), and the logs go to that state folder's `logs/`. Run it with the
variables you want the daemon to use, e.g. `VANI_CONFIG=/abs/config.json VANI_STATE_DIR=/abs/state
build/vani launchd plist --binary /abs/vani`; a relative value is refused (exit 2). Moving the config
or state means regenerating the plist and bootstrapping it again. Nothing else from the environment
is written into the plist, API keys included.

Workday boards: `watchlist add --ats wd --token tenant/site --host tenant.wd5.myworkdayjobs.com
--search "query"`. Search sites take repeated `--search` (up to 10 terms; the board lists their union)
and, where verified, `--filter name=value`: `az --token amazon` (`normalized_country_code[]`,
`category[]`), `gg --token google` (`target_level`, `location`), `ap --token apple` (`location`),
`ef --token microsoft.com --host apply.careers.microsoft.com` (`location`). A board over its
posting cap fails rather than being truncated, so pick searches narrow enough. Careers pages: `--ats pg --url https://... --link-pattern '<regexp for posting
URLs>'`, with `lightpanda_binary` set or `lightpanda` on PATH. Each careers-page load waits until
a host's interval has passed since the previous load from that host ended. The interval is the longer
of `page_interval_ms` (0..10000, default 0) and the `Crawl-delay` the host's `robots.txt` sets for
`Vani` (or `*`), read once per host per run; that read counts as a visit, so the first load waits
after it too. A `Crawl-delay` above 10 s, or a `robots.txt` that cannot be read (5xx, timeout,
refused host, over 512 KiB, malformed `Crawl-delay`), fails that board with a warning; a 4xx means
no `robots.txt` and no delay. lightpanda runs as `Vani/0.1 (+personal job-listing reader)`, so its
`--obey-robots` (Allow/Disallow, subresources included) applies the same `Vani` group; one process
serves a whole board, so it reads the host's `robots.txt` once per board. The wait happens before
each navigation, since lightpanda gives up on a first response after 5 s and the egress proxy's own
per-host pacing must stay under that.

Config and state default to `~/Library/Application Support/Vani/` on macOS, on Linux to
`$XDG_CONFIG_HOME/vani/config.json` and `$XDG_STATE_HOME/vani` (`~/.config`, `~/.local/state`
when unset), and on Windows to `%AppData%\Vani`; override either with `VANI_CONFIG` and
`VANI_STATE_DIR`.

Vani runs on macOS. The Go engine also compiles under `GOOS=windows` and `GOOS=linux` (both are
vetted), but **Windows is untested and not supported**, and Linux is untested beyond that. The
Mac app, its native messaging host and its helpers are macOS only and have no Windows path. Keys come from the environment: `OPENROUTER_API_KEY` for the OpenRouter model,
`GEMINI_API_KEY` / `GOOGLE_API_KEY` or `ANTHROPIC_API_KEY` for the LLM. An Anthropic key not
scoped to a workspace also needs `ANTHROPIC_WORKSPACE_ID` (`wrkspc_…`), or Anthropic refuses every
request with a 400; `ANTHROPIC_BASE_URL` overrides the API host. Both are Manvi's variables.

Each run triages, scores and explains at most `advance_per_run` postings (default 300, at most
5000), which bounds its spend and duration. They go highest title rank first, then the freshest:
the posted date when the board gives one, else when Vani first saw a posting that appeared after
its board's first crawl; undated postings from a board's first crawl go last. `advance_concurrency`
postings advance at a time (default 4, at most 16; 1 is strictly one by one). With a shadow
configured, `decide.shadow_every` asks it about one posting in that many (default 12, picked by a
hash of the posting key; 1 asks it about every posting), and a local shadow takes at most its
server's `parallel` calls plus one waiting; the run's warnings say how many postings it read and
how many sampled ones it skipped while busy. The rest stay `live` for the next run. Only triaged
postings are listed in Jobs, so `jobs list`/`jobs stats` (`pending` in `--json`), the digest
footer and the Jobs tab footer say how many are still awaiting triage. `status --json` shows
the last run's `advance` report (eligible, attempted, unread, deferred, why it stopped) and
`backend_errors`: every decision-backend call that failed, counted by cause, with one example.

Before setting `devprism.binary` (which enables ranking and a real `kb sync`), run the Phase 0
check that the installed DevPrism answers `--mcp` without migrating `career.db`.

## Build and test

The `go.work` file is ignored but required: Manvi's own `go.mod` replaces DevCouncil and gusset
with sibling checkouts, and Go only applies those replaces inside a workspace. On a fresh clone,
create it as:

```
go 1.26.6

use (
	.
	/Users/bharath/Code/devtools/Manvi/manvi
	/Users/bharath/Code/devtools/decide
)

replace github.com/bharathvbcr/Manvi/manvi v0.0.0 => /Users/bharath/Code/devtools/Manvi/manvi
```

```bash
go -C /Users/bharath/Code/apps/Vani vet ./...
go -C /Users/bharath/Code/apps/Vani test -race ./...
```
