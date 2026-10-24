# Where a run gets its config, state and keys

Checked on 2026-10-20 against the code and this Mac. Each claim names its evidence; re-check
it before relying on it after the code changes.

## Models, one role each

- **Decision model:** Liquid. The live config's `decide.openrouter_model` is `liquid/d1-ultra`
  over OpenRouter (chosen 2026-10-19 after live readings; FOLLOWUPS). `vani init` writes the
  same model (`internal/config` `Default()`). The OpenRouter client is the shared decide
  module's `decide/jev` package, and the day's spend is kept under the budget key `jev`: those
  are library and file names kept from before the model was Liquid, not the model. The local
  Liquid d1-3B can run beside it through llama.cpp (`decide.local`, shadow only).
- **Embeddings:** Tessl, in-process on Metal (EmbeddingGemma; `internal/semantic`, a
  `-tags tessl` darwin/arm64 build; `vani semantic status` says whether the binary has it).
- **Claude:** through Manvi, which Vani imports as its LLM orchestrator (`llm.provider`
  `anthropic`; the live config uses `claude-opus-5-5`). Jarvis also runs on Manvi; it has no
  code link to Vani (no reference either way, checked 2026-10-20).

## Config and state: one pair

`vani` resolves one config file and one state directory per process
(`internal/config/appsettings.go`, `Resolve`):

1. `VANI_CONFIG` and `VANI_STATE_DIR` win, each on its own.
2. Otherwise the platform default: on macOS `~/Library/Application Support/Vani/config.json`
   and `~/Library/Application Support/Vani/state`.
3. On macOS, when either path came from the default, `vani` reads the Mac app's settings,
   `~/Library/Group Containers/HAUHVD8M37.dev.vbcr.vani/apply/draft-settings.json` (`vaniConfig`,
   `vaniStateDir`; at most 64 KiB). If they name a non-empty config or state folder other than
   the defaulted one, `vani` refuses to run (`ErrAppPairMismatch`): the message names both pairs
   and the `export VANI_CONFIG=… VANI_STATE_DIR=…` that would run it on the app's pair. A missing
   settings file is no check; a malformed, oversized or unreadable one prints a warning and the
   command goes on.

The app passes both variables whenever its settings name paths, so the app itself is never
refused. `vani status` prints where each path came from:

```
config: /Users/bharath/Library/Application Support/Vani/config.json (default)
state dir: /Users/bharath/Library/Application Support/Vani/state (default)
app settings: agrees
```

`app settings` is `agrees`, `absent`, `not checked` (both paths from the environment, or not
macOS) or `unreadable: …`; `vani status --json` carries the same as `paths`.

### The live pair today

The live setup (19 boards, Liquid d1-ultra at $20/day, Claude Opus at $10/day, a target
summary, DevPrism, `advance_per_run` 3000, a 3461-posting ledger) is the default pair,
`~/Library/Application Support/Vani/config.json` and `.../Vani/state`, and the app's settings
name no paths. The app, a bare `vani` in a terminal and an agent shell all use it; the verified
`vani status` above is from right after the move.

### The move (done 2026-10-20)

Until 2026-10-20 the live pair was this repo's `.state/config.json` and `.state/state`, named by
the app's settings, while the default location held a near-default config nothing read (a
list-only AMD run that day put 1286 postings there). The owner ran
`tmp/amd/migrate-vani-state.sh` (gitignored): it refused while any Vani process ran, moved the
old default pair aside to `~/Library/Application Support/Vani.pre-migration-2026-10-20`
(nothing deleted), moved `.state/config.json` (mode 600) and `.state/state` to the default
location, left `.state/MOVED.txt`, cleared `vaniConfig` and `vaniStateDir` in the app's
settings, and verified with a bare `vani status` (`(default)` twice, `app settings: agrees`)
and `vani config check` (19 boards, 31 allowed hosts). The live config names no path inside
`.state`, so nothing else moved. The backup can be deleted once the app's Jobs tab looks
right.

## Keys

### Decision model (`OPENROUTER_API_KEY`)

Every `vani` that builds the OpenRouter client (`run-once`, `daemon`, `suggest`, `kbmatch`,
`reasons`) reads the variable named by `decide.openrouter_key_env`; when it is unset or empty,
`vani` reads the login Keychain item the app keeps it in (`cmd/vani/keychain.go`,
`internal/vault/modelkey.go`):

- service `openrouter-api-key`, account = the macOS user (the app's
  `Keychain.openRouterService` in `Drafting.swift`; a test holds the two names together);
- the environment wins: a non-empty variable is used and the Keychain is never read;
- `VANI_NO_KEYCHAIN=1` (any value) skips the Keychain; set it in agent shells. The Mac app sets
  it in every `vani` it starts with the decision key (Run now, the schedule agent's daemon,
  suggest): the app is the one reader for those, and a key it could not read (missing, refused,
  not answered) is not asked for again by `vani`, which would be a second prompt from another
  binary after a refusal;
- one read is bounded by 60 s (time to answer the Keychain's access prompt); a success is kept
  for the life of the process (a daemon asks once, at its start, so `daemon.model` can say
  whether it has a key), a failure is retried at most once per run;
- a failure (no item, timed out, refused) prints one warning naming the item and why, never
  the value, and no OpenRouter client is built: without another backend a run that advances is
  refused as a run with no decision backend (below). Off macOS there is no Keychain and no
  warning.

`vani status` and `status --json` (`daemon.model`) say what a running daemon actually has:
`enabled` with `source: "keychain"` when it read the key itself, nothing for the environment,
or `enabled: false` with the reason, including why the Keychain gave none (`keychain`).

Security impact: before, only the Mac app read this key and handed it to a `vani` it started.
Now any `vani` in the user's login session that has neither the variable nor `VANI_NO_KEYCHAIN`
can read it, once the user allows the `vani` binary at the Keychain prompt; an unsigned rebuild
is a new binary and prompts again. The read is bounded (60 s), `VANI_NO_KEYCHAIN` disables it
and the environment overrides it. The value is never logged or put in an error, and it is only
ever sent to `openrouter.ai` (decide's jev client calls the lookup only for that endpoint).
Spend stays capped by `decide.daily_usd.openrouter`. Nothing else is widened: only the
`openrouter-api-key` service is readable (the Anthropic key is not, and `vani` has no
Keychain write).

### Claude (Anthropic)

`vani` reads Anthropic keys from the environment only, through Manvi's resolver
(`manvi/credentials/credentials.go`, `Resolve`). The Mac app fills it through the
`anthropic-account` launcher, which picks the Console account and sets `ANTHROPIC_WORKSPACE_ID`
(`Drafting.swift`, `DraftRunner.KeySource`). Reading `anthropic-api-key` from the Keychain
directly would bypass that choice, so `vani` does not.

### By entry point

| Entry point | Started by | Decision model key | Claude key |
|---|---|---|---|
| Pipeline tab, Run now (`vani run-once`) | the app | from the app (Keychain), in the environment; `VANI_NO_KEYCHAIN=1` so `vani` never reads it again | through `anthropic-account` |
| Pipeline tab, Run on schedule (`vani daemon`, a login item) | the app's schedule agent | the same, read once per start; `daemon.model` reports it | none |
| Extension "suggest" (`vani suggest`) | the app's agent | the same | the same |
| Extension "draft" (`vani draft`) | the app's agent or Chrome host | not used | the same |
| `vani run-request` | a terminal or the app | whatever the running daemon has | the same |
| `vani daemon` (scheduled runs) | launchd, from `vani launchd plist` | `vani`'s own Keychain read at start (the plist pins only `VANI_CONFIG` and `VANI_STATE_DIR`) | none |
| `vani` in a terminal | you | environment, else Keychain | environment only |
| `vani` in an agent shell | an agent | none with `VANI_NO_KEYCHAIN=1` | environment only |

Keychain items present for this user on 2026-10-20: `openrouter-api-key` and
`anthropic-api-key`. There is no Gemini item.

On 2026-10-20 no `vani daemon` was installed (`launchctl list` showed only the app and its
agent), so the config's 08:30 schedule did not fire. Since 2026-10-23 the app can keep it:
Pipeline ▸ Run on schedule (`macos/README.md`, the schedule agent); `vani status` says
"schedule not kept" until a daemon announces itself. Whether the owner has turned it on is not
checked here.

## The knowledge base: only eligible bullets leave

rkb's bank (`kb.bank`) holds superseded wordings, bullets that cite figures `Claims.tex` has
withdrawn (CAUTION) and drafts nobody reviewed. Nothing built from it goes to a model, an
embedding or an answer unless the owner's current resumes list the bullet and it passes
`kbsync.Eligible` (`internal/kbsync/eligible.go`, the one rule): a listed bullet whose claim
tokens all resolve, with no CAUTION claim unless its `caution_ok` names it, that passes rkb's
reader gate on this exact wording (rkb's 12-hex `text_hash`): for HR and for the hiring manager,
the latest round-2 verdict is a pass and there is a second pass (round 1, or round 2 by another
reviewer), the round-2 reviewer is named, is not "drafter" and is not the bullet's author, and HR
and hiring manager are different reviewers. Selections are read with rkb's defaults (a section
without `kind` is inferred; an entry without `bullets` shows all its bank bullets). The real-bank
oracle (`TestRealBankFinalizedResumeIsEligible`) holds the rule to rkb's: every bullet of the last
resume `rkb finalize` passed must be eligible.

So these need `kb.selections` (absolute paths of rkb `resumes/*.json` you currently use) and
`kb.rkb` (for claim values and CAUTION flags), and fail closed without them, never reading
the whole bank:

| Consumer | Without `kb.selections` or `kb.rkb` |
|---|---|
| `vani draft`, `vani suggest` (`cmd/vani/eligiblebank.go`, `draft.FromResolved`) | refused, code `kb` |
| The semantic profile: embedding matching and the digest `vani kbmatch` sends to OpenRouter (`semanticProfile`, `Eligibility.Restrict`) | "semantic matching not run" / "language-model reads not used"; ranking falls back to its prior |

An entry with no eligible bullet contributes no title and no stack terms to the profile.
Changing which bullets are eligible changes the profile hash: the semantic score cache is
discarded and re-scored locally (Tessl, no spend, bounded by `semantic.warm_per_run`), and
stored `kbmatch` answers no longer match (`kbmatch.json` is keyed by the digest hash) until
`vani kbmatch run` asks again, which is the only thing that spends on them. `vani draft` also
refuses a draft that cites a figure none of its sources contain (code `ungrounded`,
`internal/draft/ground.go`).

## Building a resume

`vani resume` (`cmd/vani/resumecmd.go`, `internal/rkbresume`) builds one job's resume with rkb, in
the owner's template; DevPrism only ranks postings (the DevPrism `resume_synthesize` path was
removed 2026-10-23: its `career.db` held stale blocks and its layout is not the owner's).

| Config | Use |
|---|---|
| `kb.bank`, `kb.rkb`, `kb.code_root` | as above; `code_root` is rkb's `RKB_CODE_ROOT` |
| `kb.selections`, `kb.base_selection` | eligibility (above), and the selection tailored: its header, meta, layout, `lines` sections and per-entry overrides are kept as written; it must be one of `kb.selections`, 1 to 2 pages, every entry listing its bullets |
| `kb.vm_report` | the rkb validator report the claims export reads (which figures count as verified). Set but missing is code `config`; unset, `vm_report_full.txt` else `vm_report.txt` under `<code_root>/career/CurriculumVitae` is used and the reply's `notes` names it and its age |
| `llm.provider`, `llm.model` | the tailoring turn, at high effort (`llmbackend.ShapeHigh`); spends from the same daily `decide.daily_usd.llm` ledger as `vani draft`. Two live runs on 2026-10-24 cost $0.22 and $0.13 (each one retry) |

Steps: claims export, `kbsync.Eligible`, one `claims export --used-in` of the base (proves rkb runs
and names figures rkb has not double-verified), one tool-less model turn, then in a 0700 temp dir
`claims export --used-in`, `lint` (draft, not `--final`), `render`, `fit --pages N --strict`. It does
not run `rkb build`, which writes LaTeX into the career tree.

- The model sees the posting inside the untrusted markers, the base skeleton (section titles, entry
  ids and titles) and the eligible pool (bullet id, resolved text, entry). Never the bank's headers,
  the name, contact or locations. It answers with ids, the order of the project sections, and 8 to
  15 keywords from the posting; every bullet must be `Allowed` under its entry, unique, within
  bounds, and every entry must sit in a section that may hold it (a job only in Professional
  Experience, a project only in a project section), else the choice is refused. The project sections
  between Professional Experience and the first non-entries section may be reordered for the role;
  Summary, Experience, Skills, Education and Publications keep their places. Work-history entries
  the base lists stay, even when every bullet of one is left off (it prints as a heading).
- Say each thing once: bank entries that list a bullet in common or name a common origin are one
  family (two framings of one project, e.g. "Custom LLM Inference Runtime" and "GPU Performance:
  gemma-metal Runtime" share six bullets); a tailored page shows at most one entry of a family
  unless the base itself pairs them (`internal/rkbresume/family.go`).
- Continuations: a bullet that reads on from the one before it ("beneath those features", "the same
  lessons", a summary line opening "Also") is offered only with, and only directly after, the bullet
  it follows in the owner's selections (`internal/rkbresume/continuation.go`).
- No provider, no key, budget spent, an invalid answer, or a choice rkb rejects twice (one retry
  that tells the model the lint error or fit advice, ids only): the base selection is built,
  filtered to eligible bullets. The reply says `"selection":"tailored"` or `"base"` and why.
- Figures: a bullet using a figure rkb has not double-verified would print highlighted yellow
  (the template's `print-color-adjust: exact`), so it is not offered, and one the model's own
  export flags is left off and listed in `unverified_dropped`; the rendered page is checked for any
  `unverified` mark before the PDF is accepted.
- Fit: OVERFLOW, and a SHORT that ends a page early, fail. Soft flaws with every page in use (a SHORT
  last page, GAP, STRICT orphan lines) give the model one retry with rkb's advice, then the tailored
  PDF is kept with a `notes` entry rather than replaced by the untailored base, the owner's choice
  (2026-10-24). A retry that runs out of time keeps a soft-fit tailored PDF already built. rkb's
  `die()` exits 2 like OVERFLOW; a verdict counts only when `fit --report` exists and carries the exit
  code, so a missing Chrome or `pdftotext` is code `render`, never a pass.
- Text rkb prints (lint lines, stderr) is scrubbed of the base header's name and contact strings
  before it reaches an error, the reply or a model prompt (`internal/rkbresume/scrub.go`).
- Codes: `config`, `kb`, `render`, `lint`, `fit`, `figures`, `too_large`, `pages`, `timeout`,
  `cancelled`. The whole build is bounded at 360 s (`rkbresume.DefaultDeadline`; the app's agent
  allows a resume job 375 s, the extension waits 390 s); steps are bounded (claims 30 s, lint 30 s,
  render 30 s, fit 100 s, model turn 90 s). A model turn starts only with 195 s left, the base build
  only with 45 s left. Typical runs take about a minute. Under heavy machine load (a load average
  of 119 on 2026-10-24, other sessions compiling) rkb's Chrome fit outlived its 100 s and the build
  failed with code `timeout`: an honest failure, not a wrong PDF; rerun when the machine is quieter.
- `pdftotext` is looked up on the child's PATH, then in `/opt/homebrew/bin` and `/usr/local/bin`
  (the Mac app's agent runs with the system folders only).
- Checked on 2026-10-23 with the live bank and `ai-agent-systems.json`, no model: two pages, the
  owner's sections, a base build in about 25 s of CPU (FOLLOWUPS, "vani resume on rkb"). Checked
  on 2026-10-24 with Opus 5.5 on a real Amazon SageMaker Inference posting: a tailored two-page
  PDF with GPU & ML Systems moved ahead of the agent sections, every page full, nothing retired.

## Running without a decision backend

A run that advances and has no decision backend (no key anywhere, no llm) is refused at its
start, before it folds the spool or lists any board, with `no decision backend is available`
(`TestFullRunWithoutBackendStillRefusedAfterListOnly`). `vani run-once --list-only` asks for no
triage and needs none: it lists, reconciles and applies the drop rules (`TestRunOnceCLIListOnlyWithoutBackend`).
Untriaged postings are counted in the Jobs tab's "awaiting triage" banner, not listed as rows.
The inbox poll (advance only) still refuses without a backend. (A 2026-10-20 change that made a
full run list its boards first and then fail with advance stop `no_backend` was not kept when
the later list-only behaviour and its tests landed: see FOLLOWUPS.)

## Model ids

Neither `liquid/d1-ultra` nor `typesafe/jev-1.13` appears in OpenRouter's public
`GET /api/v1/models` on 2026-10-20 (468 models; the only `typesafe/` entry is
`typesafe/jev-router`); both are presumably visible to the account's key only. d1-ultra has
answered live calls (FOLLOWUPS, 2026-10-19). `typesafe/jev-1.13` remains the shared decide
module's `jev.DefaultModel`, used only when a caller passes no model; Vani always passes one.

## Open

- Signing `vani` so the Keychain grant survives rebuilds: not done, and whether a stable
  signature keeps the grant is unverified (it needs two consecutive signed builds to check).
- Claude for scheduled runs: a plist-started daemon has no Anthropic key. Wrapping the plist's
  program in `anthropic-account exec … -- vani daemon` (as the app does for its runs) is not
  built. The app's schedule agent starts the daemon through `anthropic-account` when
  `llm.provider` is `anthropic`.
- Scheduled runs need the app's Run on schedule switch, or `vani launchd plist` and a bootstrap.
- The Keychain path of a plist-started daemon (`vani`'s own read, with the access prompt) has
  not been run live: the tests replace the read.
