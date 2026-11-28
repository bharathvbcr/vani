# Data Model

## 1. Entities & Types

| Entity | Defined in | Notes |
| :--- | :--- | :--- |
| Answer bank | `macos/Sources/VaniApplyKit/Bank.swift`, `BankStore.swift`, `SQLiteBankStore.swift`; extension side `extension/src/bank.ts` | The saved answers keyed by site and question. Writes carry `baseRev`; a stale write returns a conflict with the current bank. `droppedSecret` in the `put` reply counts values the app refused to keep. |
| Native request | `macos/Sources/VaniApplyKit/NativeProtocol.swift` | `{"op": ...}` messages: `ping` (returns `protocol` and `rev`), `get`, `put`, `log`, `draft`, `contacts` and the job ingest operation. Replies are an object, an error with a `code`, or a `conflict`. |
| Intent | `extension/src/intents.ts` | A question the extension recognises across wordings (authorization to work, sponsorship, relocation, start date, salary and others) so one answer fills each wording. A label that reads as two intents fills from neither. |
| Fill result | `extension/src/fill.ts` | Per field: confident (green), review (yellow), manual, or untouched. A value the page did not keep is `manual`. |
| Submit gate | `extension/src/submit.ts` (`SubmitGate`) | `armed`, `missed` (null when the audit did not run, which is not a pass), `finals`, `writes`. |
| Ingest request | `internal/inbox/inbox.go`, `docs/EXTENSION_INGEST.md` | `{page:{url,title}, save_search, jobs:[...]}` validated on stdin and spooled as a batch. |
| Job row | `macos/Sources/VaniApplyKit/JobRow.swift`, `Jobs.swift` | Decoded from `vani jobs ... --json`; unknown enum values and absent fields decode leniently. Stage transitions are timestamped in `<state>/marks/stages.jsonl` (`macos/README.md`). |
| Manual run | `macos/Sources/VaniApplyKit/ManualRun.swift` | Per-run options for `vani run-once`, the `manual.log` rotation and `VANI_RUN_LOG`. |
| Resume per job | App Group container | At most 512 KB PDF keyed by the page's reduced URL (per README); never written to the activity log by name. |

## 2. Serialization

- Native messaging to Chrome is length-prefixed JSON on stdio; the Safari extension uses `SFExtensionMessageKey` Foundation objects; both go through one handler.
- The Jobs tab decodes leniently and treats an absent signal as "not known", not zero (`macos/README.md`).

## 3. Invariants

- I1: Fill never writes a value longer than the field allows, and never reports a field filled that did not keep the value.
- I2: A bank write based on a stale revision does not overwrite; it returns a conflict.
- I3: A check that did not run is reported as not run (`not_run`, `missed: null`), never as a pass.
- I4: The extension does not write the ledger; the ledger owner drains the spool.
- I5: A click on a final-submit control happens only through the separate armed submit action, at most once per invocation.
- I6: Personal data does not enter a model prompt, log line, ledger annotation or fixture.
