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
- `VANI_NO_KEYCHAIN=1` (any value) skips the Keychain; set it in agent shells;
- one read is bounded by 60 s (time to answer the Keychain's access prompt); a success is kept
  for the life of the process (a daemon asks once), a failure is retried at most once per run;
- a failure (no item, timed out, refused) prints one warning naming the item and why, never
  the value, and the run then ends as a run with no backend: it lists the boards and fails
  with advance stop `no_backend` (below). Off macOS there is no Keychain and no warning.

Security impact: before, only the Mac app read this key and handed it to a `vani` it started.
Now any `vani` in the user's login session can read it once the user allows the `vani` binary at
the Keychain prompt; an unsigned rebuild is a new binary and prompts again. Spend stays capped by
`decide.daily_usd.openrouter`.

### Claude (Anthropic)

`vani` reads Anthropic keys from the environment only, through Manvi's resolver
(`manvi/credentials/credentials.go`, `Resolve`). The Mac app fills it through the
`anthropic-account` launcher, which picks the Console account and sets `ANTHROPIC_WORKSPACE_ID`
(`Drafting.swift`, `DraftRunner.KeySource`). Reading `anthropic-api-key` from the Keychain
directly would bypass that choice, so `vani` does not.

### By entry point

| Entry point | Started by | Decision model key | Claude key |
|---|---|---|---|
| Pipeline tab, Run now (`vani run-once`) | the app | from the app (Keychain), else `vani`'s own Keychain read | through `anthropic-account` |
| Extension "suggest" (`vani suggest`) | the app's agent | the same | the same |
| Extension "draft" (`vani draft`) | the app's agent or Chrome host | not used | the same |
| `vani run-request` | a terminal or the app | whatever the running daemon has | the same |
| `vani daemon` (scheduled runs) | launchd, from `vani launchd plist` | `vani`'s own Keychain read (the plist pins only `VANI_CONFIG` and `VANI_STATE_DIR`) | none |
| `vani` in a terminal | you | environment, else Keychain | environment only |
| `vani` in an agent shell | an agent | none with `VANI_NO_KEYCHAIN=1` | environment only |

Keychain items present for this user on 2026-10-20: `openrouter-api-key` and
`anthropic-api-key`. There is no Gemini item.

No `vani daemon` is installed (`launchctl list` shows only the app and its agent; `vani status`
says no daemon has announced itself), so the config's 08:30 schedule does not fire; runs happen
when Run now is pressed.

## Running without a decision backend

A run with no decision backend (no key anywhere) lists its boards, applies the drop rules and
the near-duplicate stage, records the postings live, then fails with advance stop `no_backend`;
the app's notice says the postings are in Jobs. `--list-only` asks for no triage and succeeds.
Untriaged postings are counted in the Jobs tab's "awaiting triage" banner, not listed as rows.
The inbox poll (advance only) still refuses without a backend.

## Model ids

Neither `liquid/d1-ultra` nor `typesafe/jev-1.13` appears in OpenRouter's public
`GET /api/v1/models` on 2026-10-20 (468 models; the only `typesafe/` entry is
`typesafe/jev-router`); both are presumably visible to the account's key only. d1-ultra has
answered live calls (FOLLOWUPS, 2026-10-19). `typesafe/jev-1.13` remains the shared decide
module's `jev.DefaultModel`, used only when a caller passes no model; Vani always passes one.

## Open

- Signing `vani` so the Keychain grant survives rebuilds: not done, and whether a stable
  signature keeps the grant is unverified (it needs two consecutive signed builds to check).
- Claude for scheduled runs: a daemon has no Anthropic key. Wrapping the plist's program in
  `anthropic-account exec … -- vani daemon` (as the app does for its runs) is not built.
- No daemon is installed; scheduled runs need `vani launchd plist` and a bootstrap.
