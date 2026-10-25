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

Semantic state under `<state dir>/semantic/`: `semantic-scores.json` and `semantic-profile.json`
(the caches), `vectors.log` (the append-only chunk vectors a rescore reads instead of the model;
bounded at 512 MiB (about 7,600 postings at ~69 KB each), compacted evicting closed postings
first, then the lowest `jobinsight.Score` (the order a warm embeds in), then the oldest written; at
the bound a posting that ranks below everything stored is not stored (`vectors_refused`) and its
score is still saved; rebuilt by the next warm when unreadable), `semantic.lock` (held by a warm), and `semantic-idle.json` (what the daemon's idle
warm last did, its backoff and next eligible time). The daemon's idle warm is documented in
README.md ("Idle warm"); `VANI_NO_IDLE_WARM=1` turns it off, and `vani status` and `vani
semantic status` print its state.

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
| `llm.provider` | must be `anthropic`: the tailoring turn runs on `rkbresume.Model` (Opus 5.5), not on `llm.model`, at high effort (`llmbackend.ShapeHigh`), and the provider must serve and price it (Resume Studio, below); spends from the same daily `decide.daily_usd.llm` ledger as `vani draft`. Two live runs on 2026-10-24 (before the pin) cost $0.22 and $0.13 (each one retry) |

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

## Resume Studio

`vani jobs resume` keeps a job's resume as numbered versions, built by `internal/rkbresume` (above)
and stored by `internal/resumestore`. `vani resume` stays the stateless path the extension uses for
a page Vani does not track (it keeps nothing); both build through one config owner
(`resumeConfig` in `cmd/vani/resumecmd.go`), so the knowledge-base keys, the validator report, the
work and lock folders and the Opus pin are the same. The Studio window, the Resume card and the
extension's keyed path are the app's and the extension's (FOLLOWUPS, "Resume Studio").

Owner decisions (2026-10-25): **Opus only** (`rkbresume.Model`, `claude-opus-5-5`, whatever
`llm.model` says; `llm.provider` must be `anthropic` and the provider must serve and price it); the
Studio swaps **bank bullets and rkb entry overrides only** (title, org, stack, dates, lead), never
new wording; an **owner's edit obeys the page-safety rules only** (eligible, in the right entry,
continuations kept directly after their lead, one entry per family, plain one-line overrides, rkb
lint, 1 to 2 pages), not the caps that steer the model; a **lead line holds no figures**.

Every subcommand takes `--json` and writes one object: the reply, or `{"error","code"}` (exit 1;
usage errors exit 2, as the other `jobs` commands). `KEY` is a posting key that must be in the
ledger; a bad key is `bad_request`, an unknown one `unknown_key`. A version is written `v3`.
Nothing here puts the owner's name, contact lines or any resume text in a reply, a log line or an
error: a PDF or page is a 0600 file, named by path.

| Command | What it does |
|---|---|
| `build [--dry-run] [--allow-base] [--from vN] [--scope NAME] [--lock NAME ...] [--instruction TEXT] KEY` | Opus picks the bullets (the build above); the result is saved as the next version, made from the current one. Needs the posting's text (`bad_request` without it). Reply: the version record without its choice, plus `current_moved`, `current_note` and `pdf_path` (absolute). The flags make it a **redraft** (below). `--dry-run` asks no model and runs no rkb: `opus_usable` and why not (`no_model`), `budget` (cap, spent and remaining today; absent, never zero, when the spend cannot be read) and an `estimate` for at most 2 turns (`rkbresume.MaxTurns`, an upper bound on input tokens from `rkbresume.PromptBound` plus what a redraft adds (`Tailor.PromptBytes`), the shaped output ceiling, `max_usd` at Anthropic's published rates, `cap_reached`); for a redraft it also says `from`, `scope`, `lock` and `instruction_runes` (the length, never the text), and refuses what a build would refuse without rkb (`bad_instruction`, `bad_request`, `not_found`, `config`). |
| `render [--fit] [--from vN] KEY` | The owner's edit (JSON on stdin, at most 64 KiB, the shape of a version's `selection`), no model, so nothing is spent. **Without `--fit` nothing is saved**: rkb's screen HTML is copied to `<state>/resumes/.tmp/preview-N.html` (0600; ones over an hour old are swept by the next render), the build folder with the header is removed, and the reply is `{key, saved:false, html_path, checks, notes, keywords, bullets}` with `checks.fit` always `not_run` and no pages, version or PDF: the layout was not measured. **With `--fit`** the page is fitted in Chrome and saved as a version (`source` "edited") made from `--from` (default the current one); the reply is `build`'s. `--from` needs `--fit`. |
| `show [--no-check] KEY [vN]` | Every version (record, `current`, `used`), the asked-for version's full `selection` (default the current one), the editor's `pool` (`rkbresume.PoolView`: eligible bullets by entry with resolved text, `follows`, the sections an entry may go in, the base choice, `reorderable_sections`: the titles of the sections whose order may change, and `headings`: each `{section, entry}` where an entry may stay with no bullets, below) and the posting's `keywords` from `jobs show`'s data. Unless `--no-check`, each version has a fresh `eligibility` (below). `--no-check` reads stored files only (5 s), runs no rkb, gives no pool and says `eligibility_checked:false`; a version's own `checks.eligibility` is then what was true when it was built. |
| `use KEY vN` | Checks the version freshly, then binds it for applying: `{key, version, pdf_path, name, pdf_sha256}`. Stale is refused (`stale`, with the verdict), a version that could not be checked is refused (`unchecked`, or `timeout` / `cancelled`): an unchecked version is never used. The store also refuses a version whose fit did not pass or soft-pass, whose recorded eligibility is not current or that has no PDF (`not_usable`), and a PDF that does not match its hash (`damaged`). |
| `revalidate KEY vN` | Drops from vN's choice the bullets that went stale, and the continuations of a dropped lead (the pool says what each continuation follows), rebuilds it with fit (no model) and saves a version with `source` "revalidated", made from vN, whose `reason` names the dropped ids (`dropped_stale` in the reply). A version that is still vouched for is `not_stale`; one that could not be checked is `unchecked`. What is left must still build: an empty page is `bad_edit`, and nothing is saved. A dropped wording is not replaced; the owner places a swap in the editor. |
| `diff KEY vA vB` | What differs between two stored versions, from the store alone (5 s, no rkb, no model): `{key, from, to, same, bullets {added, removed, moved}, overrides, section_order?, keywords {added, removed}}`. `from` and `to` are `{version, source, parent, instruction, scope, lock, created}` (the instruction each was asked with, and the scope and locked section names its redraft kept, as recorded; each absent when the version has none). A bullet is `{id, section, entry, index}` (`entry` absent in a summary section, `index` 0-based in its entry's list). `moved` is a bullet on both pages in another section or entry, or out of order against the other bullets they share within one entry (the fewest that jumped: removing one bullet does not make its neighbours "move"): `{id, from, to}`, each a `{section, entry, index}`. `overrides` is `{entry, field, from, to}` per changed field of title, org, stack, lead and dates (an empty side is no override). `section_order` is `{from, to}`, the sections both pages have in each page's order, present only when they differ; a section only one side has is not an order change. `keywords` are the choices' `jd_keywords`. `same` is true when none of bullets, overrides, order and keywords differ (the sides can still differ in how they were made). Every list is present, empty when nothing differs. An unknown version is `not_found`, a record that does not read `damaged` or `unsupported`. |
| `current KEY vN` | Makes a stored version the one the Studio shows and the next redraft starts from: a restore (`SetCurrent`). No version is deleted and `used` is untouched (`use` alone binds a version for applying). Reply `{key, version, previous, changed}`: `previous` is the version that was current (absent for none); an already-current version is a no-op with `changed:false`; a damaged pointer is replaced. 10 s. |

**Eligibility of a stored version** (`jobsresumecheck.go`). A version records each bullet's id and
the 12-hex hash of the wording it was chosen for, and the fingerprints of every input its build read
(bank, base selection, selection set, validator report, claims, rkb binary). `show`, `use` and
`revalidate` compare them to one fresh `rkbresume.Pool` and the bank's bullet ids, and re-export each
version's own choice (`rkbresume.PoolAndVerify`, below):
`{state: current | stale | unknown, reworded, gone, ineligible, unverified, inputs_changed, why}`.
`reworded`: still offered, but the version was not judged on the new wording. `gone`: no longer in
the bank. `ineligible`: in the bank, no longer offered (including a continuation whose lead went).
`unverified`: rkb no longer double-verifies a figure in the bullet for this version's choice. A
version is stale when any bullet is in one of those lists, or when the validator report changed. The
other changed inputs are listed for information. `unknown` means the check could not run (the pool,
or a version's figure re-check) and is never read as `current`; `use` and `revalidate` refuse it as
`unchecked`.

**Figures** (gap 29). rkb's `double_verified` is not a property of a claim but of a claim and the
sentences a selection uses (`claims export --used-in`), so the pool's own export, which uses none,
judges no claim verified (measured on the owner's bank: 340 claims, 0 double-verified; 42 for the
base selection with its validator report; 13 when every bullet is "used"). A digest of that flag could
never move, so none is in the claims fingerprint or the per-bullet record. Instead `show`, `use` and
`revalidate` export each version's own choice, as the build that made it did: a bullet whose figure
no longer verifies for that choice is `unverified`, makes the version stale, and `revalidate` drops
it. A choice the live bank cannot place (a bullet no longer offered) costs no rkb run and is already
stale; a failed export is `unknown`. Up to 4 choices are exported at once (`verifyParallel`).
Measured with the real rkb (n=7, min): the pool 0.07 s; pool plus 1, 5 and 20 versions 0.15, 0.26 and
0.65 s (1.6 s one at a time). `vani jobs resume show` on a one-version job took 0.74 to 0.79 s
checked and 0.59 to 0.66 s with `--no-check`, and `vani jobs show` alone 0.38 to 0.54 s: most of
`show` is reading the job's keywords, not rkb, so the pool is not cached (a cache keyed on Vani's
fingerprints would also miss rkb's verification log and source files, and could serve a stale
"current"). `VANI_MEASURE=1 go test ./cmd/vani -run TestMeasure -v` repeats it.

**The editor's pool states two rules instead of leaving them to be inferred.** `reorderable_sections`
is the base's run of project sections (`reorderableSections`; any other section keeps its place and a
changed order of it is ignored). `headings` lists each `{section, entry}` where an entry may stay on
the page with no bullets (`keepsHeading`, the rule a build applies): every entry of the work history,
and an entry in a section whose base lists it bare, including bare entries that have no eligible
bullet and so are not in `entries`. An entry with no bullets anywhere else is refused. Both lists are
present, empty when there is nothing to say.

**Replies for versions.** The record is `resumestore.Meta`: `schema` (2; schema 1 records still read,
with no instruction, scope or lock; a record of any other schema is `unsupported`, so an older vani
refuses a v2 record: reinstall the app and the CLI together), `version`, `parent`, `source`
(tailored, base, edited, revalidated), `reason`, `instruction` (the owner's preference a tailored
version was asked with, at most 500 characters on one printable line, scrubbed of the header's
strings; never on the page; absent for a base or edited page), `scope` (the section title or entry id
a redraft re-picked alone) and `lock` (the section titles it kept), names only, each at most 120
characters on one printable line and at most 20 locks (checked when saved; rkbresume refuses longer
names before a turn), written for a page a model shaped. **A version made without a model
(`render --fit`, `revalidate`) copies nothing from its parent**: no instruction, scope or lock. The
app shows the parent's through `parent` ("from v3: ..."). `model`, `cost {usd, complete, turns}` (a turn whose
cost is unknown makes `complete` false and `usd` a lower bound), `checks {eligibility, lint, fit,
figures}`, `pages` (0: not fitted), `notes`, `unverified_dropped`, `jd_keywords`, `bullets`,
`fit_lines`, `fingerprints`, `pdf_sha256`, `created`. `render` without `--fit` names its keywords
`keywords`.

**Current and the base fallback.** A new version is made from the current one and takes its place
(the store moves `current` only when the version's parent is the current one, or there is none). A
build that fell back to the owner's base selection (a model that answered badly, or no model with
`--allow-base`) is a fallback, not a choice: it is saved with no parent so a job that already has a
current version keeps it (`current_moved:false`, `current_note` says so); the first version of a job
is current whatever it is. Without `--allow-base`, a build that cannot run Opus is refused with
`no_model` before anything is spent.

**Redraft** (`rkbresume.Tailor`, `redraft.go`). `build` with `--from vN`, `--scope`, `--lock` or
`--instruction` asks Opus again for a job that has a version. `--from vN` is the version it starts
from; with a scope, a lock or an instruction and no `--from`, the current one is (an instruction
alone on a job with no version is a plain build with a preference; a scope or lock with no version
is `bad_request`). The new version is made from the version it started from, so it takes the current
one's place only when that was the current one (`current_note` says why not).

- *Whole redraft* (`--from`, nothing kept): the prompt shows the previous choice by ids only (no
  text), as "the previous choice; choose differently where the posting is better served", only ids
  still on offer.
- *Scope and locks.* `--lock TITLE` (repeatable) keeps a section; `--scope` names a section title
  (it is the one re-picked, every other section is kept) or an entry id of the previous page (only
  that entry's bullets are re-picked, its section's other entries are kept). Kept sections go in the
  prompt's "kept as they are" line; the bullets on kept parts are not offered again. The model's
  reply is checked before any rkb render: repeating a kept part exactly is ignored, changing it is
  `scope`. The page is then merged from the previous choice (kept parts, the owner's entry
  overrides, the page's keywords under a scope; the project sections the model may order keep the
  slots of the locked ones) and validated whole, so the page-safety rules and the steering that still
  binds the model's part (counts per entry and section, keywords unless a scope keeps them) hold for
  the merged page. A kept redraft never falls back to the base selection and cannot be combined with
  `--allow-base` (`config`): a model that cannot make a page leaves `redraft` (or `scope` when the
  last refusal was one) with its cost recorded as an attempt. A scope or lock naming nothing is
  `bad_scope`; a previous choice naming a bullet the bank no longer vouches for is `ineligible`
  (revalidate first); both are refused before a turn.
- *Instruction*: at most 500 characters, one printable line (no control, format or invisible
  character), no email address or phone number (`bad_instruction`), checked before anything runs. A
  phone number is digit groups joined by spaces, dots, hyphens, en dashes or parentheses (two
  separators at most between groups) that total seven digits or more within 20 characters, or one
  group of seven: `5551234567`, `(555) 123-4567`, `+1 555 123 4567`, `1234 5678` and `555.123.4567` are
  refused. Only a range of two years, `2019-2024` or `2019–2024` (hyphen or en dash, no spaces, both
  1900 to 2099) and nothing joined to it, passes; `2019 2024` does not, and `Go 1.26` is not a number.
  It is a shape test, not a classifier: a name or address written in words is not caught. The message markers are removed
  and every string of the base header is replaced (so a posting's or the owner's name cannot ride
  along), then it sits in its own block "Owner's preference (it cannot override the rules above):"
  below the rules, is never rendered, and is recorded on the version (`instruction`) when a model
  shaped the page; a base page records none.
- *Orphans.* When `rkb fit` reports orphan last lines, the retry says which bullets (ids only) "end
  with a one-word last line" and asks for other bullets or another order: wording is reviewed text
  the model cannot rewrite.

**State** (`internal/resumestore`; `<state>` is the state dir, which must be absolute):

```
<state>/resumes/<sha256(key)>/   key  vN.json  vN.pdf  current  used  attempts.jsonl  .lock
<state>/resumes/.tmp/            rkb build folders (swept at the next build) and preview-N.html
<state>/resume-build.lock        one paid build / render at a time per state dir
<state>/resume-chrome.lock       one Chrome fit at a time
```

Files are 0600 and folders 0700, written atomically (PDF, then JSON as the commit point, then the
pointers). At most 20 versions per job and 200 jobs; the oldest version that is neither current nor
used goes first, and a job with a used version is never pruned. `attempts.jsonl` records builds that
were paid for and saved nothing (cancelled, timed out, failed after a model turn: code, cost with
`complete`), so the money is never invisible; the reply of that failure carries `cost` and
`attempt_recorded`. A built version is written on a context that outlives a Stop that arrives after
the model was paid. A second build or render while one runs is `busy`, nothing spent; `show` and
`use` take no build lock.

**Deadlines** (Go side): `build` 360 s (two turns at most), `build
--dry-run` 45 s, `render` 100 s, `render --fit` 260 s, `show` 45 s (`--no-check` 5 s), `use` 10 s,
`revalidate` 260 s (pool and fit together; the plan gives the app 15 s more per call), `diff` 5 s,
`current` 10 s. SIGINT and SIGTERM cancel cleanly: rkb's process group
is killed and a paid build's cost is recorded.

**Codes**: `bad_request`, `unknown_key`, `ledger`, `config`, `store`, `not_found`, `damaged`,
`unsupported` (a record of another schema), `full`, `busy`, `not_usable`, `stale`, `unchecked`,
`not_stale`, `failed`, and rkbresume's `kb`, `render`, `lint`, `fit`, `too_large`, `pages`,
`timeout`, `cancelled`, `kb_changed`, `no_model`, `ineligible` (the message names the bullet id),
`bad_edit`, and a redraft's `bad_instruction`, `bad_scope`, `scope`, `redraft`. Exit 1 for any of
them, 2 for a usage error.

The request header of `vani resume` is decoded and ignored: nothing requires or validates it, since
rkb prints its own, and the extension no longer sends one (step 2 of its removal; `no_name` is off
its list of worded codes). Vani can drop the decode once no older extension is installed.

**The browser's two builds.** The extension's offer asks Vani which tracked posting the page is
(`jobFind`), then starts one of two agent jobs. A tracked posting runs `resumeJob`
(`{"key"}` and nothing else): `vani jobs resume build --json KEY` (375 s), then, only when the
reply's `checks.fit` is `passed` or `soft`, `checks.eligibility` is `current` and `pdf_path` is
present, `vani jobs resume use --json KEY vN` (25 s). The agent reads the PDF at `pdf_path` (the
store's own `<state>/resumes/<sha256(key)>/vN.pdf`, a regular file, at most 512 KiB, its SHA-256
equal to `use`'s `pdf_sha256`; the login agent is not sandboxed) and replies in `vani resume`'s
shape plus `key` and `version`. A refusal is `{error, code}`, naming `version` when one was saved
and not used. A build that falls back to the base selection is used like any other (`selection`
"base" and its `reason` say so). Any other page (untracked, ambiguous, or one jobFind could not
answer, which the bar says) runs the stateless `resume` job and keeps nothing.

### Coverage

`jobs resume coverage` says which of a posting's keywords a version's bullets cover, and `jobs resume
gaps` hands the ones the bank cannot cover to the owner's career session. Neither asks a model, takes
a build lock or spends anything; each reads the pool once (the read-only rkb claims export `show`
uses) under the `show` deadline class, 45 s (`resumeCoverageDeadline`). The computation is
`internal/rkbcoverage`; a failed pool read is the failure's own code (`kb`, `timeout`, ...), never a
guessed answer.

| Command | What it does |
|---|---|
| `coverage [--json] KEY [vN]` | Default: the current version; a job with no version gets its base selection (`base:true`, no `version`). Reply: `{key, version \| base, keywords_source, keywords_error?, hit, miss, uncovered, unjudged, unresolved, terms_total, terms_truncated?, gap_missing}`. |
| `gaps [--json] [--dry-run] KEY` | Appends one line `{at, key, company, title, terms}` to `<state>/resumes/gaps.jsonl`. Terms: the `uncovered` terms of the current version (the base, when there is none) plus the `gap_missing` terms no eligible bullet covers now (DevPrism's annotation may predate a bullet the bank gained), each at most 80 runes, at most 100. The posting's keywords must be readable, else `failed` and nothing is written: an empty hand-off would read as a bank that covers everything. Reply: `{key, terms, written, file, dry_run?, unchanged?, terms_truncated?, note?}`. `--dry-run` writes nothing and takes no lock. A job with no terms writes nothing (`written:false`, `note`). |

**What the lists mean.** `hit` is `[{term, bullets, n}]`: a term some chosen bullet covers, the ids
(at most 40, page order) and how many chosen bullets cover it. `miss` is every other judged term.
`uncovered` is the part of `miss` that no *eligible* bullet in the whole pool covers either: a swap
cannot fix those, the bank lacks them. A term only an ineligible bullet says is therefore uncovered.
`unjudged` are phrases the vocabulary format cannot hold (over 4 tokens, over 64 bytes, a `|`, a leading
`=`, or the same tokens as an earlier one): they are in none of hit, miss and uncovered, so an
unmatched phrase is never read as a miss. `unresolved` are chosen bullet ids with no eligible wording
now (gone from the bank, or no longer offered): they cover nothing here, and a miss may be theirs; `show`'s
`eligibility` says why. At most 200 terms are judged (`terms_total` says how many were asked about,
`terms_truncated` that the rest were not). Replies hold terms and ids only, never bullet text. The wording is the
bank's as it stands now, so coverage of an old version reads today's text.

**Which terms.** `keywords_source` says: `posting` (the keywords `jobs show` reports, at most 30,
boilerplate removed), `jd_keywords` (the version's own, the words Opus took from the posting), both
joined by `+`, or `none`. Each term counts once whatever its case. `gap_missing` is the vocabulary
terms in the ledger's `gap.missing` annotation (DevPrism's must-haves found missing or weak), sorted;
the annotation's own text is untrusted and is never passed on.

**Matching rule.** jobinsight's, so a term occurs in a bullet exactly where `jobs show` found it in the
posting: whole token sequences, case-insensitive (`C++`, `scikit-learn` and `.NET` tokenize as there).
No substring matching: `Pythonic` does not cover `Python`. A posting keyword is matched through
`jobinsight.Default().Terms`, so the vocabulary's aliases, stop phrases and case-sensitive terms ("Go")
apply, and **a match consumes its tokens**: the longest spelling wins, so a bullet that says `CUDA
kernels` covers the term `GPU kernels` and not `CUDA`. A version's `jd_keywords` are the model's own
words, matched through a one-term vocabulary built from them with `jobinsight.ParseVocab`: they have
none of that (a `jd_keyword` `Go` matches the verb "go", and one `training` is consumed by another
`distributed training` in the same version).

**The hand-off file.** `<state>/resumes/gaps.jsonl` is 0600, written atomically under
`gaps.jsonl.lock` (waits at most 10 s or the command's deadline, then `busy`), at most 500 lines and 256
KiB (the oldest go first, the newest is kept), one line at most 16 KiB (terms are dropped, never the line).
A call whose terms equal the newest line for the same job adds nothing (`unchanged`). A file that cannot
be read whole (over its bound, not a regular file, lines that are not JSON) is replaced, since it is a
window onto recent jobs; a link at its path is replaced, never written through. The job folders of the
resume store ignore it. It holds posting data (company, title, key) and skills, nothing about the owner.
The owner's career session reads it and works the terms into the bank through `rkb review`.

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
