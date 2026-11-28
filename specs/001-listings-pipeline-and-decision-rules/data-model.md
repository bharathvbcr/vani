# Data Model

## 1. Entities & Types

| Entity | Defined in | Notes |
| :--- | :--- | :--- |
| `posting.Posting`, `posting.Key`, `posting.Status` | `internal/posting/posting.go`, `internal/posting/key.go` | A job posting and its identity. Statuses: `discovered`, `live`, `triaged`, `unsure`, `scored`, `explained`, `digested`, `dead`, `duplicate`, `dropped`. `unsure` means triage could not decide; the posting is kept and flagged. |
| `posting.ApplyForm` | `internal/posting/applyform.go` | Version 1 summary of an application form (`ApplyFormVersion`); bounded by `MaxFormGates` (12), `MaxFormOptions` (16), `MaxFormUploads` (4), label and option rune limits so one hostile form cannot grow a ledger line. A reader that meets another version treats the form as not read. |
| `ledger.Event` | `internal/ledger/types.go` | One immutable JSONL line: `v` (schema version, currently 1), `at`, `key`, `status`, optional `posting`, `annotations` (short strings), `reason`. |
| `ledger.Record` | `internal/ledger/types.go` | The folded current state of one posting. `Posting.Description` may be empty when `BodyEvicted` is true. |
| `decide.Question`, `decide.Slot`, `decide.Answer`, `decide.SlotReading` | `decide/types.go` | Typed multiple-choice questions to decision backends. A reading is answered, abstained, refused, failed, unavailable or not asked. |
| Decision slots for triage | `internal/decide/jdtriage/jdtriage.go` | Four slots read from a posting: `role_fit`, `seniority`, `sponsorship`, `work_mode`. The training contract is `fixture.json`, pinned by hash. |
| `decide.Router`, `decide.Spend`, `DailyCap` | `decide/router.go`, `decide/spend.go` | Ordered backends, a shadow backend that never decides, a total deadline, and per-day spend caps. |
| Inbox batch | `internal/inbox/inbox.go` | A validated request spooled as `<state>/inbox/<ns>-<rand>.json`; `Request.Validate` judges stdin, `Batch.Validate` re-judges on drain. |
| Config | `internal/config/config.go` | Includes `AllowedHosts` (`allowed_hosts`), the watchlist, exclusions, decision-model and LLM settings. Resolved per process by `appsettings.go`. |
| Run record | `<state>/runs/history/` | One record per finished run, newest 30 kept (`docs`: `vani runs list`, `cmd/vani/main.go` usage). |

## 2. Serialization

- Ledger events are JSON, one per line, schema `v` 1. A reader that does not know the version must refuse rather than guess.
- Annotation values are short strings; personal data never enters an annotation (`CLAUDE.md`).
- The ledger has one writer at a time; readers (`vani status`, the Mac app) read without taking the run's lock.

## 3. Invariants

- I1: every outbound request goes through `egress.Doer` and is https, allowlisted and not private, on every redirect hop.
- I2: the canonical ledger is append-only; every index is derived and rebuilt on start.
- I3: a decision answer is one of the caller's options; nothing in the decision layer authorises an action (Rule 7).
- I4: packages that approve or allow (`vault`, `notify`, `approve`) do not import the decision layer.
- I5: an uncertain reading keeps the posting.
- I6: `fixture.json` changes only with a retrain and a new pinned hash.
- I7: every timeout, retry, body read and fan-out is bounded, and `context.Context` is propagated (`CLAUDE.md`).
