# Setup

First-time setup for a local checkout laid out as in [`go.work.example`](../go.work.example) (`$CODE_ROOT/apps/Vani`, `$CODE_ROOT/devtools/…`; replace `$CODE_ROOT` with your absolute path, including inside `go.work`, which does not expand variables), then day-to-day commands. For a **portable new-user setup** (fresh machine, minimal config without `rkb`/`DevPrism`/`Tessl`), start with [`ONBOARDING.md`](ONBOARDING.md). Config, state and key ownership live in [`RUNTIME.md`](RUNTIME.md), and the overview is in the [README](../README.md).

Two audiences: the owner at a terminal, and an agent shell. An agent shell sets
`VANI_NO_KEYCHAIN=1` before any `vani` that looks up the OpenRouter key. That variable is any
non-empty value. Without it, a command that builds an OpenRouter client reads the login Keychain
item `openrouter-api-key` and can spend from it. Those commands are `run-once` (including
`--list-only`), `daemon`, `suggest`, `kbmatch run` when the backend is `openrouter`, and the notes
picker inside a run. `config check`, `init`, `kb sync`, `semantic warm`, `semantic status` and
`kbmatch run --dry-run` do not build that client. `--list-only` does build it when
`decide.openrouter_model` is set (the triage client, and the notes picker when a reasons catalog
exists and loads), so it reads the Keychain unless the variable is set. It does not ask the decision
model, so it does not spend, and it does not stop when no backend can be built. The advance-phase
semantic warm is skipped with it. When semantic matching is configured, the near-duplicate pass on
that run still loads the embedding model. `run-request` does not build a client. The running daemon
performs the request and spends from the daemon's key when the request advances (the default, or
`--advance`). `VANI_NO_KEYCHAIN` on the `run-request` process does not apply to that daemon. An
agent shell sends `run-request` only with `--list-only`.

Personal data (name, contact, work authorization) does not go in `target_summary`,
`motivation_notes`, a model prompt, a log line or a fixture. The key itself is never written into
the config, the launchd plist, a doc or a command that prints it.

## First-time setup

### 1. Checkouts and `go.work`

Go 1.27.4 (`go.mod`). Two modules live in this repo as nested modules: the decision client
`decide/` (`go.mod` replaces it with `./decide`) and the knowledge-base tool `rkb/` (stdlib only,
built with `GOWORK=off`). Vani builds against four public sibling checkouts, not published
modules:

| Module | Repository |
|---|---|
| `github.com/bharathvbcr/Manvi/manvi` | `github.com/bharathvbcr/Manvi` |
| `github.com/bharathvbcr/DevCouncil/backend/go_orchestrator` | `github.com/bharathvbcr/DevCouncil` (Manvi needs it) |
| `github.com/bharathvbcr/gusset` | `github.com/bharathvbcr/gusset` (Manvi and tessl need it) |
| `github.com/bharathvbcr/tessl/go` | `github.com/bharathvbcr/tessl` (`go.mod` requires it; only `-tags tessl` compiles it) |

**The short way.** `scripts/bootstrap.sh` clones the four into `.deps/` (gitignored; `--deps DIR`
for elsewhere) at the commits in `scripts/deps.lock`, writes `go.work` with absolute paths, checks
that each module resolves, builds `build/vani` and `build/rkb`, runs `vani init` (only when no
config exists) and `vani config check`, and reports each optional layer of steps 4–7 as found or
missing. It never changes an existing `go.work`, checkout or config: with a `go.work` present it
clones nothing and only checks what that file names. Then continue at step 3.

**The manual way.** Manvi's own `go.mod` replaces DevCouncil and gusset with paths relative to
that checkout (`../../DevCouncil/backend/go_orchestrator`, `../../gusset`), so the three must be
siblings. Those replaces apply only inside a workspace. `go.mod` replaces `tessl/go` with
`../../research/tessl/go`. [`go.work.example`](../go.work.example) is the workspace for the
layout `$CODE_ROOT/apps/Vani`, `$CODE_ROOT/devtools/{Manvi,DevCouncil,gusset}`,
`$CODE_ROOT/research/tessl`:

```
go 1.27.4

use (
	.
	./decide
	$CODE_ROOT/devtools/Manvi/manvi
)

replace github.com/bharathvbcr/Manvi/manvi v0.0.0 => $CODE_ROOT/devtools/Manvi/manvi
```

`go.work` and `go.work.sum` are gitignored. Do not add a `decide` or `tessl` replace to it in
that layout: `go.mod` already replaces both. A git worktree outside that layout misses the
relative `tessl` path; its `go.work` needs an absolute `replace` line for `tessl/go` (the
bootstrap writes one). Do not edit `go.mod` or run `go mod tidy`.

**Moving a pin.** `scripts/deps.lock` records sibling commits that were verified together. To move
one: clone Vani and the four siblings at the new commits into an empty directory, run
`scripts/bootstrap.sh` there with an empty `GOMODCACHE`, then `go vet ./...`, `go test ./...` and a
`-tags tessl` build, and only then edit the lock.

Confirm the workspace before building:

```bash
go -C $CODE_ROOT/apps/Vani list -m -f '{{.Path}} {{.Dir}}' github.com/bharathvbcr/Manvi/manvi github.com/bharathvbcr/decide github.com/bharathvbcr/tessl/go github.com/bharathvbcr/DevCouncil/backend/go_orchestrator github.com/bharathvbcr/gusset
```

Each line must name a directory that exists. An empty `Dir` means that replace did not resolve.

### 2. Build

The default build is pure Go. It runs the pipeline, `kb sync` and OpenRouter. It does not embed.

```bash
go -C $CODE_ROOT/apps/Vani build -o $CODE_ROOT/apps/Vani/build/vani ./cmd/vani
go -C $CODE_ROOT/apps/Vani vet ./...
go -C $CODE_ROOT/apps/Vani test ./...
```

Run the full race suite with `VANI_NO_KEYCHAIN=1 go -C $CODE_ROOT/apps/Vani test -race ./...`.
Embeddings need the Tessl build in step 6. `vani` below means `$CODE_ROOT/apps/Vani/build/vani`.

### 3. Config and state

```bash
$CODE_ROOT/apps/Vani/build/vani init
$CODE_ROOT/apps/Vani/build/vani config check
$CODE_ROOT/apps/Vani/build/vani status
```

`init` writes `config.Default()` and refuses to overwrite a file that exists. On macOS the default
pair is `~/Library/Application Support/Vani/config.json` and
`~/Library/Application Support/Vani/state` (mode 0600 on the config). `VANI_CONFIG` and
`VANI_STATE_DIR` override them, each on its own. `status` prints where each path came from and
whether the Mac app's settings agree. Read that before trusting a count. The full resolution
rules, the 2026-10-20 move, and the per-entry-point key table are in
[`RUNTIME.md`](RUNTIME.md).

There is no general `vani config set`. `config` accepts `check`, `motivation` and `exclude`.
Everything else is an edit of the JSON file. Unknown fields are refused. `config check` prints
`ok: <path> (N boards, M allowed hosts)` or the validation errors.

`Default()` already sets:

| Key | Value |
|---|---|
| `decide.openrouter_model` | `liquid/d1-ultra` |
| `decide.openrouter_key_env` | `OPENROUTER_API_KEY` |
| `decide.daily_usd.openrouter` | `0.25` |
| `decide.daily_usd.llm` | `1.0` |
| `allowed_hosts` | `openrouter.ai` |
| `schedule` | `08:30` `America/Los_Angeles` |
| `llm` | `provider` and `model` empty (no Claude until you fill them, step 9) |
| `devprism.binary` | empty (ranking skipped until you set it) |
| watchlist | empty |

A model set with `decide.daily_usd.openrouter` of 0 is refused
(`decide.daily_usd.openrouter must be above 0 when decide.openrouter_model is set`). The cap is at
most 50 (`MaxDailyUSD`); above that the error is `decide.daily_usd.openrouter <value> must be within 0..50`.
While the model is set, `allowed_hosts` must include `openrouter.ai`. `vani init` already does.

### 4. OpenRouter

`vani init` defaults `decide.openrouter_model` to `liquid/d1-ultra` (`allowed_hosts` already includes
`openrouter.ai`), answered under the short name `d1-ultra`. Spend is logged to `<state dir>/budget/jev.json`
against `decide.daily_usd.openrouter`, and notes spend to `budget/jev-notes.json` against
`funnel.notes_daily_usd` (default `0.15`). Leave `decide.openrouter_model` empty only when turning off
OpenRouter triage (`llm` or `--list-only` must carry the run). Full retry/timeout/key resolution rules live in [`RUNTIME.md`](RUNTIME.md).

**Key lookup (`OPENROUTER_API_KEY` / `decide.openrouter_key_env`):**
1. A non-empty env var wins (Keychain is skipped).
2. On macOS (unless `VANI_NO_KEYCHAIN=1`), `vani` reads login Keychain service `openrouter-api-key` for your short user name once per run (`security find-generic-password`). The Mac app reads the same item and passes `OPENROUTER_API_KEY` + `VANI_NO_KEYCHAIN=1` to child processes.

Create or check the Keychain item without putting the secret in shell history (`-T` grants a specific binary; do not pass `-A`):

```bash
security add-generic-password -U -a LOGINNAME -s openrouter-api-key -T $CODE_ROOT/apps/Vani/build/vani -w
security find-generic-password -s openrouter-api-key -a LOGINNAME >/dev/null  # exit 44 if absent
```

If the key is missing, `run-once` stops before listing when no `llm` fallback is configured, while `daemon` starts with the decision model disabled (`vani status --json` sets `daemon.model.enabled: false`).

### 5. Knowledge base

rkb is a separate program whose source is the nested module [`rkb/`](../rkb/README.md); the
bootstrap builds it to `build/rkb` (by hand: `GOWORK=off go -C rkb build -o ../build/rkb .`).
Vani does not build `bank.json` or `Claims.tex`. It calls the rkb binary `kb.rkb` points at. That
rkb starts from its embedded generic config and layers one user file over it: the skill's
`<skill>/rkb/assets/config.json` when the binary sits in a skill folder, else
`~/.config/rkb/config.json` (Vani passes rkb only `PATH`, `HOME`, `TMPDIR` and `RKB_CODE_ROOT`, so
`RKB_SKILL_DIR` does not reach it). Put your own codenames, jargon and phrase lists there
([`rkb/README.md`](../rkb/README.md)); `rkb doctor` names the file in use. A knowledge base kept in
an rkb skill folder keeps `kb.rkb` on that skill's `bin/rkb`, so its config applies. To start from
nothing, copy [`rkb/examples`](../rkb/examples) ([`KNOWLEDGE_BASE_GUIDE.md`](KNOWLEDGE_BASE_GUIDE.md)). The canonical bank is rkb's; DevPrism's `career.db` is a one-way copy of the
approved bullets, used for ranking (`resume_gap_analysis`). Building a tailored PDF is a later
step ([`RUNTIME.md`](RUNTIME.md), "Building a resume") and is not required to rank postings.

**Config keys** (every path absolute; `config check` refuses a relative one):

| Key | What it is |
|---|---|
| `kb.bank` | rkb `bank.json` |
| `kb.rkb` | the rkb binary |
| `kb.code_root` | the folder that holds `career/`. Passed to rkb as `RKB_CODE_ROOT`. Empty lets rkb search. |
| `kb.selections` | at most 32 absolute paths of rkb `resumes/*.json` files you currently use. Empty is valid JSON. Sync, draft, suggest (when the bank is configured), `kbmatch`, resume and the semantic profile all refuse it. None of them falls back to the whole bank. The strings are in the failure table. |
| `kb.base_selection` | one of `kb.selections`. Required for `vani resume`, not for sync or embeddings. |
| `kb.vm_report` | optional absolute path of the rkb validator report. Resume builds only. |

`kb sync` runs `rkb claims export` and copies only `kbsync.Eligible` bullets into DevPrism's
`career.db` (hash state in `<state dir>/kbsync/state.json`; eligibility rules live in
[`RUNTIME.md`](RUNTIME.md)).

```bash
# Preview eligible bullets and what is withheld (no DevPrism writes)
$CODE_ROOT/apps/Vani/build/vani kb sync --dry-run --bank /abs/bank.json --rkb /abs/rkb --code-root /abs/code

# Apply upserts once devprism.binary is set (step 7)
$CODE_ROOT/apps/Vani/build/vani kb sync
```

- `--bank`, `--rkb`, `--code-root` override config per run (`kb.selections` has no flag).
- `--confirm` consents to overwriting locked bullets or dropping bullets/facts (cannot combine with `--dry-run`).
- `--replace` backs up `career.db` via `sqlite3 .backup` to `<state dir>/kbsync/backups/career-YYYYMMDD-HHMMSS.db` (keeps newest 10) and deletes non-pool `work`/`experience`/`project`/`education` blocks (unless the pool is empty). Accepts `--keep ID,ID` (must be orphans), `--delete-all-kinds`, `--max-delete N` (1..500, default 100), and `--career-db PATH`.

(`KBSYNC_REAL_BANK=1` with `KBSYNC_BANK`/`KBSYNC_RKB` runs the opt-in `internal/kbsync/realbank_test.go` check.)

### 6. Embeddings

Semantic matching orders postings against the eligible profile without dropping any. It runs only
when both `kb.bank` and `semantic.snapshot` (the `snapshots/<revision>` directory of a
`google/embeddinggemma-2` Hugging Face snapshot, not the repo root) are set; Vani has no download
command (`VANI_TESSL_SNAPSHOT` sets the snapshot for tests, or `~/.cache/huggingface/hub/models--google--embeddinggemma-2/snapshots/*`). Recorded ids look like `tessl/embeddinggemma-2@<revision>/f32/d<width>`.

- **`target_summary`, `identifying-words.txt`, `motivation_notes`, drop floors:** Keep
  `target_summary` under 1,024 collapsed-whitespace bytes with no names, contact details,
  employers, schools, or work-authorization terms. While `target_summary` is set and
  `<state dir>/tracks/identifying-words.txt` (or `tracks_dir` when set) is missing, empty, or
  matches the summary, `vani run-once` (`--list-only` included), `vani daemon`, `vani digest`,
  a real `retriage` or `rescore` (not their `--dry-run`), and every command that builds a model
  prompt from the config (`reasons draft`, `draft`, `suggest` and others) refuse (`vani teacher` /
  `vani kbmatch run` take `--allow-missing-identifying-words`). `config check` does not test it;
  `vani status` does, and names the file it expects.
  `motivation_notes` (`vani config motivation --set`) refuses `@`/handles, `linkedin`, `github`, and
  visa/citizenship wording. Drop floors: `min_openrouter_prob` is `0` or `>= 0.9`; `min_lappi_score`
  is `0` or `>= 0.8`. See
  [`RUNTIME.md#the-identifying-words-file-gates-target_summary`](RUNTIME.md#the-identifying-words-file-gates-target_summary).
- **Tessl Metal embedder (`darwin/arm64`, cgo, Xcode CLT, `-tags tessl`):** Links
  `$CODE_ROOT/research/tessl/go/engine/target/release/libgusset.a` (`cargo build --release --locked`
  via `tessl/go/Makefile`):

  ```bash
  make -C $CODE_ROOT/research/tessl/go engine
  CGO_CFLAGS="-DTESSL_ENGINE_SHA=$(shasum -a 256 $CODE_ROOT/research/tessl/go/engine/target/release/libgusset.a | cut -c1-16)" \
    go -C $CODE_ROOT/apps/Vani build -tags tessl -trimpath -o $CODE_ROOT/apps/Vani/build/vani ./cmd/vani
  $CODE_ROOT/apps/Vani/build/vani semantic status
  ```

  `CGO_CFLAGS` forces Go to re-link when `libgusset.a` changes (`ld: warning` about duplicate
  `-lgusset` is harmless). `semantic status` reports `this binary has the Tessl engine: yes|no`; if
  the snapshot or archive fails to load (`semantic: embedding model unavailable`), runs still write
  the digest with `semantic matching not run: <reason>`.
- **Warming (`semantic.warm_per_run`, default 1000, max 5000):** Run `$CODE_ROOT/apps/Vani/build/vani semantic warm --max 5000`
  (reads the ledger without locking, safe while `daemon` runs; `--max 0`/omitted uses config;
  negative or `> 5000` exits 2). Scores cache under `<state dir>/semantic/`; `vani jobs` and digests
  read the cache without loading the model, though they still run `rkb claims export` to build the
  eligible profile (`semantic warm` exits with `semantic matching not run: the knowledge base profile could not be built:` if selections or `rkb` fail). Changing the bank, selections, summary, or
  snapshot invalidates the cache. Between runs, `daemon` warms idle capacity
  ([`RUNTIME.md#idle-warm`](RUNTIME.md#idle-warm); set `VANI_NO_IDLE_WARM=1` to disable). See
  [`SIMILARITY.md`](SIMILARITY.md) for near-duplicate postings and `vani answers similar`.

### 7. DevPrism, so ranking and `kb sync` have somewhere to write

Ranking is DevPrism `resume_gap_analysis` over `--mcp`. It is skipped while `devprism.binary` is
empty, and postings scored in that state are not revisited until `vani rescore`. The installed
binary named in `config.Default`'s comment and in [`PHASE0-devprism.md`](PHASE0-devprism.md) is
`/Applications/DevPrism.app/Contents/MacOS/claude-prism-desktop`. It was present on this Mac on
2026-10-25, and `CFBundleShortVersionString` read that day is 1.4.0, the same string Phase 0
recorded on 2026-10-18.

Set `devprism.binary` to that absolute path. `devprism.tools_hash` is optional; a pin that does
not match the handshake fails startup with `resume: DevPrism tool set changed`. Starting `--mcp` opens
`career.db` and runs idempotent migrations before the first request. Read Phase 0 before the
first real `kb sync`. Back up `career.db` first. `--replace` copies it itself; a plain sync does
not.

```bash
$CODE_ROOT/apps/Vani/build/vani config check
$CODE_ROOT/apps/Vani/build/vani kb sync --dry-run
$CODE_ROOT/apps/Vani/build/vani kb sync
$CODE_ROOT/apps/Vani/build/vani rescore --dry-run
```

A real `rescore` opens the ledger, so stop the daemon first. While the daemon holds it the
error is `ledger: directory is locked by another process`. `--dry-run` reads a snapshot without
that lock and writes nothing. `rescore` calls DevPrism only and spends nothing. With
`devprism.binary` empty it refuses: `devprism.binary is empty: nothing to score with (set it, see docs/PHASE0-devprism.md)`.
Digested postings are left alone. A posting with no description is left unscored.

`kb sync` without `devprism.binary` exits 1 with
`kb sync: devprism.binary is not configured (use --dry-run to plan without DevPrism)` and does not
start DevPrism. `--dry-run` without `--replace` is the plan that needs no binary.
`--replace --dry-run` needs the binary because it reads the block list.

### 8. Watchlist, exclusions, summary, first run

Add boards with `vani watchlist add --ats CODE --token T --company 'Name' --allow-host` (`vani watchlist remove --ats CODE --token T` writes a tombstone in `<state>/watchlist/removed/` so the next run closes open postings; pass `--keep-postings` to keep them open or `--close-postings` for an already-removed board):

| Code(s) | Source | Key flags & notes |
|---|---|---|
| `gh`, `lv`, `ab`, `gm` | Greenhouse, Lever, Ashby, Gem | `--token` board slug; `lv` takes optional `--region eu`; `ab` takes optional `--board-name` when literal name differs from slug |
| `wd`, `oc` | Workday, Oracle HCM | `--token tenant/site` (`jpmc/CX_1001`), `--host` board host (`acme.wd5.myworkdayjobs.com` / `wd1.myworkdaysite.com` or `jpmc.fa.oraclecloud.com`), optional repeatable `--search` |
| `az`, `gg`, `ap`, `ib`, `tt` | Amazon, Google, Apple, IBM, TikTok | `ib` accepts `--token ibm` or `confluent`; `tt` requires `--token tiktok`; all take optional `--search`; `az`/`gg`/`ap`/`tt` take repeatable `--filter key=value` |
| `ef`, `jb`, `av`, `tb` | Eightfold, Jibe, Avature, TalentBrew | Require `--host`; `ef` token is company domain + optional `--search`/`--filter`; `jb` token is client code + optional `--search`/`--filter`; `av` also requires `--url` portal listing URL |
| `sr`, `ds`, `at`, `rp`, `wk` | SmartRecruiters, D. E. Shaw, Atlassian, Rippling, Workable | Slug `--token`; `sr` accepts repeatable `--filter country=us` (2-letter ISO code) |
| `rc`, `pp`, `bh`, `ps`, `bz`, `tm`, `hv`, `hb` | Recruitee, Pinpoint, BambooHR, Personio, Breezy HR, Teamtailor, HireHive, HiBob | Lower-case subdomain `--token` (e.g. `acme` for `acme.recruitee.com`) |
| `fd`, `pg` | XML feed, generic careers page | Short slug `--token` + `--url`; `pg` also requires `--link-pattern RE` |

```bash
$CODE_ROOT/apps/Vani/build/vani watchlist add --ats gh --token BOARD --company 'Name' --allow-host
```

`--allow-host` is a boolean. It does not take a host. It appends the board's API host (Greenhouse
is `boards-api.greenhouse.io`), and `--domain` and the `--icon` host when those flags are set.
Without it, save fails with `allowed_hosts is missing: boards-api.greenhouse.io` and tells you to
pass `--allow-host`. `config check` fails the same way while a board's host is missing.

Company icons are optional. Give a board the host of the company's own website (`--domain
www.acme.com` on `watchlist add`, or `vani watchlist domain --ats gh --token acme --domain
www.acme.com --allow-host` for a board you already have) and each run asks that host for
`/apple-touch-icon.png`, `/favicon.ico`, then `/favicon.png`. The first reply that is a PNG, ICO or
JPEG of at most 1 MiB is kept in `<state dir>/logos/`; an SVG or an HTML page is refused, the
site's pages are never read, and a redirect to any other host is refused, so give the host that
serves the icon (often the `www.` one). A site that names its icon only in its HTML needs the URL set
by hand: `vani watchlist icon --ats gh --token acme --url https://www.acme.com/assets/icon.png
--allow-host` (or `watchlist add … --icon URL`). That URL is asked for first. It must be https,
its host must be in `allowed_hosts`, the board needs a domain (the icon is cached under it), and
boards sharing a domain may not name two different icons. Changing it refetches at once, and
`--url ""` clears it. Icons are refetched after 30 days and a failed domain waits 7; a run fetches
at most 50 domains. `vani logos refresh [--force]` fetches them now.

To find boards worth adding, `vani watchlist discover --search "ml engineer" --country US --allow-host`
asks freehire.me which companies have matching roles and prints a `watchlist add` command for each
new board; it edits nothing. freehire.me is a third-party site, so the command refuses until it is in
`allowed_hosts` (`--allow-host` adds it, once). Terms: keyless personal use only, no scraping beyond
its API, so one request of 100 results by default and at most 3 pages with a pause between them.
Run the printed commands you want, then `run-once --board gh:BOARD --list-only` to see the board read.
`vani watchlist discover --profile --dry-run` shows the searches your `title_signals.include` and
knowledge base would make; drop `--dry-run` to run them, and add `--add 10` to put the ten
best-ranked new boards on the watchlist.

Put `target_summary` in the config (step 6). Then list without spending:

```bash
VANI_NO_KEYCHAIN=1 $CODE_ROOT/apps/Vani/build/vani run-once --list-only
```

A run that triages needs the OpenRouter key (or an `llm` block) and spends from
`decide.daily_usd.openrouter`. Drop `VANI_NO_KEYCHAIN` only for that run, in a terminal you mean
to spend from. Cap the first one:

```bash
$CODE_ROOT/apps/Vani/build/vani run-once --board gh:BOARD --advance 25 --no-digest
```

`--advance` sets that run's cap, from 1 to 5000. Omitting it, or passing 0, uses `advance_per_run`
(default 300, at most 5000). Together with `--list-only` it exits 2:
`--list-only skips the advance phase; --advance has nothing to cap`. A value outside 0..5000 exits 2:
`--advance <n> must be within 0..5000 (0 means advance_per_run)`. `--no-digest` still triages. The
warm before advance still runs, up to `semantic.warm_per_run` minus a quarter of it. The digest, and
the warm after the pipeline that uses what that budget has left, are skipped, and the roles wait
for the next digest. The digest file is `<state dir>/digests/YYYY-MM-DD.md`. A later run the same
day that still has new roles writes `YYYY-MM-DD-2.md` and does not overwrite the first file. One
day holds at most 50 of these files; past that, `digest: more than 50 digests for YYYY-MM-DD`.
While the daemon holds the ledger, `vani run-request` takes the same flags and the daemon makes
the run (a request that advances spends; see the audience note above). `run-once` while the
daemon holds the ledger fails with `ledger: directory is locked by another process`.

#### Exclusions

`exclude` drops on the posting's own fields before triage and spends nothing. `vani config exclude` prints the
lists as one JSON object. `--set` replaces them from one JSON object on stdin, keys `titles`,
`title_unless`, `companies`, `locations`, `only_locations` and `descriptions`. An unknown key or
trailing data is refused. Each list holds at most 128 entries, `descriptions` at most 32, and each
entry at most 100 bytes. Input over 256 KiB is refused:
`config exclude: input larger than 262144 bytes`. Saving leaves stored postings unchanged; the
next run applies the lists (`vani prefilter` previews what drops or reopens):
- `titles`: whole-word case-insensitive phrase match against posting title (whitespace collapsed; list singular/plural forms explicitly).
- `title_unless`: exempts a title matched by `titles` from the title exclusion only.
- `companies`: exact company name match (case- and whitespace-folded).
- `locations`: drops only when *every* location/remote option listed on the posting matches an entry (`"TX"`, `"Texas"`, `"US"`/`"USA"`/`"United States"`, `"remote"`, or area/region words).
- `only_locations`: keeps only if *at least one* listed location option matches an entry (empty/unscoped remote postings stay kept).
- `descriptions`: whole-word phrase match against posting description (ledger records only that descriptions matched, never the phrase).

### 9. Optional: Claude, notes, schedule, badges

Claude is the fallback and the drafter, through Manvi. It is off until the config has
`llm.provider` (`anthropic` or `gemini`), `llm.model`, and for Anthropic both
`input_nano_usd_per_token` and `output_nano_usd_per_token`. The key is an environment variable
only (`ANTHROPIC_API_KEY`, or `GEMINI_API_KEY` / `GOOGLE_API_KEY`). `vani` does not read the
Anthropic Keychain item. The Mac app fills it through `anthropic-account`. Details:
[`RUNTIME.md`](RUNTIME.md). A workspace-scoped Anthropic key also needs `ANTHROPIC_WORKSPACE_ID`.

`vani reasons draft` makes one LLM call, spent from the LLM cap, and writes
`<state dir>/reasons.json`. It refuses an empty `target_summary`. The OpenRouter model then
picks one catalog line per scored posting. Without a catalog, scored postings reach the digest
with no note.

The daily daemon is the Mac app's Pipeline ▸ Run on schedule
([`macos/README.md`](../macos/README.md), Use, the schedule agent), or `vani launchd plist` plus
`launchctl bootstrap`. The plist installs nothing by itself. `launchd` without `plist` exits 2:
`launchd: expected subcommand "plist"`. `--binary` must be absolute (the default is this
executable). A relative config or state path is refused, because launchd starts the agent from
`/`, and the generated plist pins `VANI_CONFIG` and `VANI_STATE_DIR` in `EnvironmentVariables`.
The label defaults to `dev.vani.daemon`. It sets `KeepAlive` (`SuccessfulExit: false`) and
`ThrottleInterval` `300` (5 minutes), so failed exits restart after backoff while clean stops stay
down. Reload a changed plist at `~/Library/LaunchAgents/<label>.plist` with:

```bash
launchctl bootout gui/$UID ~/Library/LaunchAgents/<label>.plist
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/<label>.plist
```

`vani lca import` and `vani employer import` add badges. They never filter
([`LCA.md`](LCA.md), [`EMPLOYER.md`](EMPLOYER.md)). `vani track use` swaps the target summary and
the track's drop rules ([`TRACKS.md`](TRACKS.md)).

Language-model KB match (`vani kbmatch run`) is a second read, after triage, of whether the
posting's duties are the work in the bank. Default backend is `llm`. `"openrouter"` asks
`decide.openrouter_model` and spends from the OpenRouter cap. `--dry-run` sends nothing. It is
not required for embeddings or for `kb sync`.

## Day to day

```bash
go -C $CODE_ROOT/apps/Vani vet ./...
go -C $CODE_ROOT/apps/Vani test ./...
VANI_NO_KEYCHAIN=1 $CODE_ROOT/apps/Vani/build/vani status
VANI_NO_KEYCHAIN=1 $CODE_ROOT/apps/Vani/build/vani config check
```

After an rkb edit you stand behind (selection still lists the bullet, readers still pass it):

```bash
VANI_NO_KEYCHAIN=1 $CODE_ROOT/apps/Vani/build/vani kb sync --dry-run
VANI_NO_KEYCHAIN=1 $CODE_ROOT/apps/Vani/build/vani kb sync
VANI_NO_KEYCHAIN=1 $CODE_ROOT/apps/Vani/build/vani semantic status
VANI_NO_KEYCHAIN=1 $CODE_ROOT/apps/Vani/build/vani semantic warm
```

`kbmatch` answers go stale when the bank or the target summary changes. `vani kbmatch status`
counts them and calls no model. Asking again (`vani kbmatch run`) spends. An agent shell uses
`--dry-run`, which builds no OpenRouter client.

After a Tessl or gusset change, rebuild the archive, then rebuild `vani` with `-tags tessl` and
the archive hash in `CGO_CFLAGS` (step 6). `semantic status` must say the engine is present
before a warm is worth running.

A scheduled run that should triage needs the Keychain item and must not have `VANI_NO_KEYCHAIN`
in the daemon's environment. Confirm with `vani status --json`: `daemon.model.enabled` true, and
`daemon.model.source` `keychain` when that daemon read the item itself. Text `vani status` only
prints the model line when it is disabled.

## When something fails

| What you see | What it means |
|---|---|
| `go: cannot find module` / empty `Dir` from `go list -m` | `go.work` is missing or a checkout moved. Step 1. |
| `<config path> already exists; refusing to overwrite` | `init` will not overwrite. Edit the file or point `VANI_CONFIG` elsewhere. |
| `kb sync: kb.selections is empty; list the rkb selection files in use (resumes/*.json) so the sync knows which bullets are approved. It never falls back to the whole bank` (exit 2) | Sync's own refusal, before rkb runs. `vani draft` and `vani suggest`, when `kb.bank` and `kb.rkb` are set, exit 1 with `draft:` in front of `kb.selections is empty: list the rkb resumes you currently use (resumes/*.json); nothing is built from the whole bank` (the text is on the JSON reply). `vani kbmatch` puts `kbmatch: the knowledge base:` in front of that same sentence (exit 1). `semantic status` prints `profile: unavailable:` and exits 0. `semantic warm` exits 1 with `semantic matching not run: the knowledge base profile could not be built:`. `vani resume` says `kb.selections names no rkb selection, so no bullet is eligible for a resume`. |
| `kb sync: --confirm cannot be used with --dry-run, which sends nothing` | Exit 2. `--keep`, `--delete-all-kinds`, `--max-delete` or `--career-db` without `--replace` exits 2 as `kb sync: --<name> needs --replace`. |
| `semantic warm: --max must be 1-5000` | Exit 2. `--max` omitted, or 0, uses `semantic.warm_per_run`. |
| `kb sync: devprism.binary is not configured (use --dry-run to plan without DevPrism)` | A live sync. Exit 1. |
| `kb sync: --replace --dry-run reads DevPrism's block list, so devprism.binary must be configured` | `--replace --dry-run` without the binary. A plain `--dry-run` does not need it. |
| `kbsync: resolve claims: rkb exited` | `rkb claims export` failed. The stderr snippet after the exit code is the reason. Fix rkb; Vani does not substitute claims. |
| `semantic matching is not configured: set semantic.snapshot and kb.bank` | Warm was asked with one of the two unset. |
| `this binary has the Tessl engine: no` | Rebuild with `-tags tessl` on darwin/arm64. |
| `semantic matching not run:` | Profile, snapshot or model failed. The digest is still written. The reason is the rest of the line. |
| `openrouter (liquid/d1-ultra) disabled:` and `no decision backend is available (configure decide.openrouter_model or llm)` | No key (environment and Keychain) and no `llm`, on a run that advances. `--list-only` prints the disabled warning and continues. |
| `daemon: model disabled:` | Text status: the daemon has no key. stderr is `vani: daemon:` plus the same reason. The `fix:` line names the Keychain item. |
| `decide.daily_usd.openrouter must be above 0 when decide.openrouter_model is set` | The model is on and the cap is 0. A zero cap would refuse every call. |
| `allowed_hosts is missing: openrouter.ai` | The model is on and that host is not allowed. Other missing hosts are joined on the same line. |
| `the Keychain did not answer within 1m0s (an access prompt nobody answered?)` | First read of a new unsigned binary, or nobody answered. `VANI_NO_KEYCHAIN=1` skips the read. |
| `decide: backend credentials rejected: HTTP 401` (or 402, 403) | OpenRouter rejected the key. That client is off for the rest of the run. |

A check that could not run is written as unavailable or `not run`. It is not a score of zero and
not a clean bill.

## Elsewhere

| Topic | Doc |
|---|---|
| Which process has the OpenRouter key, the Anthropic key, the live paths | [`RUNTIME.md`](RUNTIME.md) |
| CLI overview and command cheat sheet | [README](../README.md) |
| Cloud shadow, idle warm, semantic matching, `jobs export`, `doctor` | [`RUNTIME.md`](RUNTIME.md) |
| DevPrism `--mcp` and `career.db` | [`PHASE0-devprism.md`](PHASE0-devprism.md) |
| Similar wordings | [`SIMILARITY.md`](SIMILARITY.md) |
| Tailored PDF and Resume Studio | [`RUNTIME.md`](RUNTIME.md), "Building a resume" and "Resume Studio" |
| Mac app and the schedule switch | [`macos/README.md`](../macos/README.md) |
| New-user progressive setup (`go.work` + minimal layers) | [`ONBOARDING.md`](ONBOARDING.md) |
| Agent guide: building a new ATS adapter | [`ADAPTER_GUIDE.md`](ADAPTER_GUIDE.md) |
| Agent guide: building/wiring a knowledge base (`bank.json`, `rkb`, `Eligible`) | [`KNOWLEDGE_BASE_GUIDE.md`](KNOWLEDGE_BASE_GUIDE.md) |
| Open questions | [`FOLLOWUPS.md`](FOLLOWUPS.md) |
