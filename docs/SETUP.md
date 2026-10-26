# Setup

First-time setup for this checkout, then the commands used day to day. Checked on 2026-10-26
against the code named below. Config, state and which process holds which key are in
[`RUNTIME.md`](RUNTIME.md). Pipeline behaviour, the watchlist flags and the digest are in the
[README](../README.md). This page is the order to do them in.

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

Go 1.26.6 (`go.mod`). Vani builds against three local checkouts, not published modules:

| Module | Resolved directory (this checkout, 2026-10-25) |
|---|---|
| `github.com/bharathvbcr/Manvi/manvi` | `/Users/bharath/Code/devtools/Manvi/manvi` |
| `github.com/bharathvbcr/decide` | `/Users/bharath/Code/devtools/decide` |
| `github.com/bharathvbcr/tessl/go` | `/Users/bharath/Code/research/tessl/go` |

Manvi's own `go.mod` replaces DevCouncil and gusset with paths relative to that checkout
(`../../DevCouncil/backend/go_orchestrator`, `../../gusset`). Those replaces apply only inside a
workspace. `go list -m` from this checkout on 2026-10-25 resolved them to
`/Users/bharath/Code/devtools/DevCouncil/backend/go_orchestrator` and
`/Users/bharath/Code/devtools/gusset`.

`go.work` and `go.work.sum` are gitignored. The `go.work` in this checkout is:

```
go 1.26.6

use (
	.
	/Users/bharath/Code/devtools/Manvi/manvi
	/Users/bharath/Code/devtools/decide
)

replace github.com/bharathvbcr/Manvi/manvi v0.0.0 => /Users/bharath/Code/devtools/Manvi/manvi
```

If it is missing, recreate that file. Do not add `decide` or `tessl` replaces to it: `go.mod`
already replaces those two with paths relative to this checkout (`../../devtools/decide`,
`../../research/tessl/go`), and `go list -m` resolved them to the table above. The `use` block is
what selects Manvi and decide in the workspace. A git worktree's module directory is not this
checkout, so those relative paths miss there; that worktree's own `go.work` needs absolute
`replace` lines for `decide` and `tessl/go`. This checkout's file does not have them. Do not edit
`go.mod` or run `go mod tidy`.

Confirm the workspace before building:

```bash
go -C /Users/bharath/Code/apps/Vani list -m -f '{{.Path}} {{.Dir}}' github.com/bharathvbcr/Manvi/manvi github.com/bharathvbcr/decide github.com/bharathvbcr/tessl/go github.com/bharathvbcr/DevCouncil/backend/go_orchestrator github.com/bharathvbcr/gusset
```

Each line must name a directory that exists. An empty `Dir` means that replace did not resolve.

### 2. Build

The default build is pure Go. It runs the pipeline, `kb sync` and OpenRouter. It does not embed.

```bash
go -C /Users/bharath/Code/apps/Vani build -o /Users/bharath/Code/apps/Vani/build/vani ./cmd/vani
go -C /Users/bharath/Code/apps/Vani vet ./...
go -C /Users/bharath/Code/apps/Vani test ./...
```

`test -race ./...` is the stricter form in the README. Embeddings need the Tessl build in step 6.
`vani` below means `/Users/bharath/Code/apps/Vani/build/vani`.

### 3. Config and state

```bash
/Users/bharath/Code/apps/Vani/build/vani init
/Users/bharath/Code/apps/Vani/build/vani config check
/Users/bharath/Code/apps/Vani/build/vani status
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
| `llm` | absent (no Claude until you add it) |
| `devprism.binary` | empty (ranking skipped until you set it) |
| watchlist | empty |

A model set with `decide.daily_usd.openrouter` of 0 is refused
(`decide.daily_usd.openrouter must be above 0 when decide.openrouter_model is set`). The cap is at
most 50 (`MaxDailyUSD`); above that the error is `decide.daily_usd.openrouter <value> must be within 0..50`.
While the model is set, `allowed_hosts` must include `openrouter.ai`. `vani init` already does.

### 4. OpenRouter

The decision model is Liquid. `vani init` writes `liquid/d1-ultra`. The client is
`decide/jev` (`/Users/bharath/Code/devtools/decide/jev`). Vani always passes
`decide.openrouter_model`; it does not send jev's own default `typesafe/jev-1.13`. The model
answers under the name derived from the id (`liquid/d1-ultra` → `d1-ultra`) in routing, the
ledger, drop reasons and `vani eval`.

The call is `POST https://openrouter.ai/api/alpha/decisions` with `Content-Type: application/json`
and `Authorization: Bearer <key>`. The body is the Decisions shape: `model`, `questions` (each
`type: choice`, with `instructions` and `criteria`) and `state`. A 401, 402 or 403 comes back as
`decide: backend credentials rejected: HTTP 401` (same shape for 402 and 403) and disables that
client for the rest of the run. The next run builds a new one. Retries cover transport errors,
429 and 5xx. Vani does not set jev's timeout or attempt count, so the defaults apply: 3 attempts,
20s for the whole call. The day's triage spend is appended to `<state dir>/budget/jev.json` under
the spend key `jev`. That file name is kept on purpose: a rename would let a still-running process
start the day at zero. The cap is `decide.daily_usd.openrouter`. Notes spend is a second file,
`<state dir>/budget/jev-notes.json`, same spend key, capped by `funnel.notes_daily_usd` (default
0.15). Past either cap that call is not made.

**Where the key comes from.** `decide.openrouter_key_env` (default `OPENROUTER_API_KEY`):

1. A non-empty environment variable wins. The Keychain is not read.
2. Otherwise, on macOS, `vani` runs `security find-generic-password -s openrouter-api-key -a <login name> -w`. The account is `user.Current().Username`, the same short name the Mac app passes (`NSUserName()`, what `id -un` prints). One read waits at most 60 seconds. A success is kept for the life of the process. A failure is kept for that run only and is read again on the next run (`startRun` bumps the run counter). It is not read twice in one run.
3. `VANI_NO_KEYCHAIN` set to any value skips step 2. The phrase is `the Keychain was not read because VANI_NO_KEYCHAIN is set`.
4. Off macOS there is no Keychain and no warning.

The Mac app reads the same item and puts it in the environment of the `vani` processes it starts,
with `VANI_NO_KEYCHAIN=1` so `vani` does not prompt a second time. The app does not write the
item. A plist from `vani launchd plist` pins only `VANI_CONFIG` and `VANI_STATE_DIR`; that daemon
reads the Keychain itself at start.

Create or replace the item without putting the secret on the command line (`-w` last makes
`security` prompt). `LOGINNAME` is the short user name. `-T` names the `vani` binary allowed to
read the item; repeat it for each binary. An unsigned rebuild is a different binary and prompts
again ([`RUNTIME.md`](RUNTIME.md)). Do not pass `-A`.

```bash
security add-generic-password -U -a LOGINNAME -s openrouter-api-key -T /Users/bharath/Code/apps/Vani/build/vani -w
```

Check that the item exists without printing it. `find-generic-password` without a redirect writes
the item, secret included, to stdout. Exit 44 means there is no such item.

```bash
security find-generic-password -s openrouter-api-key -a LOGINNAME >/dev/null
```

**When the key is missing.** `jev.New` returns `jev: no API key for the OpenRouter endpoint: environment variable OPENROUTER_API_KEY is not set` (the variable name, never the value). The run warns `openrouter (liquid/d1-ultra) disabled:` plus that error. A run that advances and has no other backend (no `llm` either) stops before it lists a board, with `no decision backend is available (configure decide.openrouter_model or llm)`. `run-once --list-only` still builds the client, so the disabled warning can appear, then lists and reconciles. A daemon still starts. On stderr: `vani: daemon: ` plus `OPENROUTER_API_KEY is not set in the daemon's environment and <why the Keychain gave none>, so the decision model (liquid/d1-ultra) is disabled and the daemon triages no posting`. It keeps the schedule. Text `vani status` prints `daemon: model disabled:` and `fix: add the key to the login Keychain (service openrouter-api-key) …`. `vani status --json` has `daemon.model.enabled` and, when this daemon read the item itself, `daemon.model.source` of `keychain`. `enabled: true` means a key was present. It does not mean OpenRouter accepted it.

Leave `decide.openrouter_model` empty only when you mean to turn the decision model off. The
config then does not require a key, a positive OpenRouter cap, or `openrouter.ai` in
`allowed_hosts`. Something else (`llm`, or a list-only run) has to carry triage.

### 5. Knowledge base

rkb is a separate program. Vani does not build `bank.json` or `Claims.tex`. It calls the rkb
binary you point at. The canonical bank is rkb's; DevPrism's `career.db` is a one-way copy of the
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

`kb sync` runs `rkb claims export --out <temp>/claims.json` with an environment of `PATH`,
`HOME`, `TMPDIR` and, when `kb.code_root` is set, `RKB_CODE_ROOT`. On Windows it also passes
`SystemRoot`, `TEMP`, `TMP` and `USERPROFILE`. No API key is passed. The process waits at most 60 seconds (`rkbexec`
clamps a longer timeout to 5 minutes; 60 seconds is under that). Each of stdout and stderr is
kept up to 64 KiB. The claims file itself is refused above 4 MiB. A missing binary, a non-zero
exit, no claims file, a file with no claims, or a claim with an empty value fails the command.
Nothing is guessed.

A bullet leaves the bank only when `kbsync.Eligible` passes (`internal/kbsync/eligible.go`): a
listed selection contains it, every `{{clm…}}` token resolves, no CAUTION claim lacks
`caution_ok`, and both the HR and hiring-manager readers passed that exact wording. Round 2
cannot be the bullet's author or the reviewer id `drafter`, and the two readers cannot be the
same person. The dry run prints what is withheld and why.

```bash
/Users/bharath/Code/apps/Vani/build/vani kb sync --dry-run --bank /abs/bank.json --rkb /abs/rkb --code-root /abs/code
```

`--bank`, `--rkb` and `--code-root` override the config for that run. There is no flag for
`kb.selections`. `--dry-run` without `--replace` still runs `rkb claims export`. It reads no
DevPrism state and sends nothing. It plans against an empty DevPrism, so a block that would be
sent shows as an add; a variant or a blocked block is still reported as that. It prints
`note: planned against an empty DevPrism; live DevPrism state was not read`. It still needs
`kb.selections` in the config.

Then, once `devprism.binary` is set (step 7):

```bash
/Users/bharath/Code/apps/Vani/build/vani kb sync
```

A real sync reads DevPrism's blocks, upserts the approved pool, and does not delete. An overwrite
that would drop bullets or facts, or rewrite a locked bullet, is reported as needing confirmation
and skipped. `--confirm` prints each dropped or rewritten bullet and fact, then confirms those
blocks for that run. There is no prompt; the flag is the consent. With `--dry-run` it exits 2:
`kb sync: --confirm cannot be used with --dry-run, which sends nothing`.

`--replace` also deletes DevPrism blocks the pool does not produce (`work`, `experience`,
`project`, `education`; other kinds stay unless `--delete-all-kinds`). It first copies
`career.db` with SQLite's online backup (`sqlite3 .backup`). The binary is the first executable
among `/usr/bin/sqlite3`, `/opt/homebrew/bin/sqlite3` and `/usr/local/bin/sqlite3`. The copy is
`<state dir>/kbsync/backups/career-YYYYMMDD-HHMMSS.db` (UTC; newest 10 kept). Then it syncs the
pool and deletes only if every pool block landed. No `sqlite3` stops the replace before DevPrism
starts: `kbsync: career.db backup: no sqlite3 binary found`. It refuses to delete while the approved pool has no block to sync
(`the approved pool has no block to sync, so replace would only delete`). `--replace --dry-run`
reads the block list and prints what it would delete; it makes no copy. `--career-db` overrides
the default, which on macOS is `~/Library/Application Support/DevPrism/career.db`
(`os.UserConfigDir`). `--keep ID,ID` spares named blocks. Each id has to be an orphan DevPrism
holds and the pool does not, or the replace is refused:
`kbsync: replace refused: --keep "<id>" is not an orphan in DevPrism (no such block, or the approved pool owns it)`.
`--keep`, `--delete-all-kinds`, `--max-delete` and `--career-db` are accepted only with
`--replace`. Without it the command exits 2: `kb sync: --<name> needs --replace`. With
`--replace`, `--max-delete` is 1..500 (default 100); outside that,
`kb sync: --max-delete <n> is outside 1..500`. `--career-db` must be an absolute path.

Hash state is `<state dir>/kbsync/state.json`. A second sync of an unchanged pool sends nothing.

This repo does not choose where rkb itself is built, and it does not name a checkout that builds
it. `KBSYNC_REAL_BANK=1` is the opt-in test that runs `claims export` against a real bank and logs
counts only (`internal/kbsync/realbank_test.go`). It requires `KBSYNC_BANK` and `KBSYNC_RKB`.
`KBSYNC_CODE_ROOT` is passed through and may be empty. It is not a setup step.

### 6. Embeddings

Semantic matching orders postings against the eligible profile. It never drops one. It is on only
when both `kb.bank` and `semantic.snapshot` are set. The snapshot is the absolute path of a
Hugging Face snapshot directory of `google/embeddinggemma-2`: the `snapshots/<revision>` folder,
not the repository root. The model id Vani records looks like
`tessl/embeddinggemma-2@<revision>/f32/d<width>`. This repo does not contain a download command
for that snapshot. The tests that load a real model read `VANI_TESSL_SNAPSHOT`, or, in one test,
`~/.cache/huggingface/hub/models--google--embeddinggemma-2/snapshots/*`.

`target_summary` is the calibration ceiling. Write several concrete sentences (what you build,
with what, at what scale), at most 1,024 bytes after whitespace is collapsed to single spaces.
No name, contact, location, work authorization, or names of people, employers or schools. The
config loader does not scan the main file for those terms. `vani track` checks a track file's
summary against a built-in list of contact and work-authorization terms, plus
`identifying-words.txt` in the tracks directory. An empty summary is valid JSON and a coarser
ceiling.

The embedder is in-process on Metal, darwin/arm64, cgo on, Xcode command line tools, build tag
`tessl`. The binding links
`/Users/bharath/Code/research/tessl/go/engine/target/release/libgusset.a`. That archive was not
present on this Mac on 2026-10-25. The `engine` target runs `cargo build --release --locked` in
`tessl/go/engine`. This repo does not install Rust. Build the archive, then build `vani`:

```bash
make -C /Users/bharath/Code/research/tessl/go engine
go -C /Users/bharath/Code/apps/Vani build -tags tessl -trimpath -o /Users/bharath/Code/apps/Vani/build/vani ./cmd/vani
/Users/bharath/Code/apps/Vani/build/vani semantic status
```

`semantic status` prints `this binary has the Tessl engine: yes` or `no`. `no` means the binary
was not built with `-tags tessl` on darwin/arm64 (the stub build is everything else). A snapshot
that will not load (missing files, no `query` or `document` prompt, engine archive that does not
match the binding) is `semantic: embedding model unavailable`. The daily run still writes the
digest; the run record says `semantic matching not run: <reason>`. That is a skipped check, not
a success.

Go's build cache keys the cgo package on the flag text, not the archive bytes. After rebuilding
the archive, prefix the `go build` the way `tessl/go/Makefile` does (`CGO_CFLAGS` with
`-DTESSL_ENGINE_SHA=` and the first 16 hex digits of `shasum -a 256` of `libgusset.a`). The
README has the full line. `ld: warning` lines about a duplicate `-lgusset` are harmless.

`semantic.warm_per_run` defaults to 1000 and is at most 5000. Scores land in
`<state dir>/semantic/`. The first backlog:

```bash
/Users/bharath/Code/apps/Vani/build/vani semantic warm --max 5000
```

`--max` omitted, or `--max 0`, uses `semantic.warm_per_run`. A negative value or one above 5000
exits 2: `semantic warm: --max must be 1-5000`. The command reads the ledger without its lock, so
it runs while the daemon is up. It embeds postings. The profile those scores are compared with
is eligible bullets only. With no selections, no rkb, or an rkb that fails, status prints
`profile: unavailable:` and a warm exits with `semantic matching not run: the knowledge base profile could not be built:`.
The command prints counts, paths and reasons, not bank text or posting text.

`vani jobs` and the digest read cached scores and do not load the embedding model. They still
build the eligible profile, which runs `rkb claims export` when semantic matching is configured.
A changed bank, selection set, target summary or
snapshot makes the cache stale; `semantic status` says so and tells you to warm again. The
daemon's idle warm, between runs, is in the README. `VANI_NO_IDLE_WARM=1` turns it off. There is
no config key for that.

Near-duplicate postings and `vani answers similar` use the same binary and snapshot
([`SIMILARITY.md`](SIMILARITY.md)).

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
/Users/bharath/Code/apps/Vani/build/vani config check
/Users/bharath/Code/apps/Vani/build/vani kb sync --dry-run
/Users/bharath/Code/apps/Vani/build/vani kb sync
/Users/bharath/Code/apps/Vani/build/vani rescore --dry-run
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

### 8. Watchlist, summary, first run

Add boards with `watchlist add`. The README's Use section has Greenhouse, Lever, Ashby, Workday
and the search-site flags. One board:

```bash
/Users/bharath/Code/apps/Vani/build/vani watchlist add --ats gh --token BOARD --company 'Name' --allow-host
```

`--allow-host` is a boolean. It does not take a host. It appends the board's API host (Greenhouse
is `boards-api.greenhouse.io`), and `--domain` and the `--icon` host when those flags are set.
Without it, save fails with `allowed_hosts is missing: boards-api.greenhouse.io` and tells you to
pass `--allow-host`. `config check` fails the same way while a board's host is missing.

Put `target_summary` in the config (step 6). Then list without spending:

```bash
VANI_NO_KEYCHAIN=1 /Users/bharath/Code/apps/Vani/build/vani run-once --list-only
```

A run that triages needs the OpenRouter key (or an `llm` block) and spends from
`decide.daily_usd.openrouter`. Drop `VANI_NO_KEYCHAIN` only for that run, in a terminal you mean
to spend from. Cap the first one:

```bash
/Users/bharath/Code/apps/Vani/build/vani run-once --board gh:BOARD --advance 25 --no-digest
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

`exclude` drops on the posting's own fields and spends nothing. `vani config exclude` prints the
lists as one JSON object. `--set` replaces them from one JSON object on stdin, keys `titles`,
`title_unless`, `companies`, `locations`, `only_locations` and `descriptions`. An unknown key or
trailing data is refused. Each list holds at most 128 entries, `descriptions` at most 32, and each
entry at most 100 bytes. Input over 256 KiB is refused:
`config exclude: input larger than 262144 bytes`. Saving leaves stored postings unchanged; the
next run applies the lists. What each list matches is in the README.

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
`/`. The label defaults to `dev.vani.daemon`. The README's Use section has KeepAlive, the
5-minute throttle, and the `launchctl bootout` / `launchctl bootstrap` lines.

`vani lca import` and `vani employer import` add badges. They never filter
([`LCA.md`](LCA.md), [`EMPLOYER.md`](EMPLOYER.md)). `vani track use` swaps the target summary and
the track's drop rules ([`TRACKS.md`](TRACKS.md)).

Language-model KB match (`vani kbmatch run`) is a second read, after triage, of whether the
posting's duties are the work in the bank. Default backend is `llm`. `"openrouter"` asks
`decide.openrouter_model` and spends from the OpenRouter cap. `--dry-run` sends nothing. It is
not required for embeddings or for `kb sync`.

## Day to day

```bash
go -C /Users/bharath/Code/apps/Vani vet ./...
go -C /Users/bharath/Code/apps/Vani test ./...
VANI_NO_KEYCHAIN=1 /Users/bharath/Code/apps/Vani/build/vani status
VANI_NO_KEYCHAIN=1 /Users/bharath/Code/apps/Vani/build/vani config check
```

After an rkb edit you stand behind (selection still lists the bullet, readers still pass it):

```bash
VANI_NO_KEYCHAIN=1 /Users/bharath/Code/apps/Vani/build/vani kb sync --dry-run
VANI_NO_KEYCHAIN=1 /Users/bharath/Code/apps/Vani/build/vani kb sync
VANI_NO_KEYCHAIN=1 /Users/bharath/Code/apps/Vani/build/vani semantic status
VANI_NO_KEYCHAIN=1 /Users/bharath/Code/apps/Vani/build/vani semantic warm
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
| Boards, triage, digest, rescore, `kbmatch`, launchd | [README](../README.md) |
| DevPrism `--mcp` and `career.db` | [`PHASE0-devprism.md`](PHASE0-devprism.md) |
| Similar wordings | [`SIMILARITY.md`](SIMILARITY.md) |
| Tailored PDF and Resume Studio | [`RUNTIME.md`](RUNTIME.md), "Building a resume" and "Resume Studio" |
| Mac app and the schedule switch | [`macos/README.md`](../macos/README.md) |
| Open questions | [`FOLLOWUPS.md`](FOLLOWUPS.md) |
