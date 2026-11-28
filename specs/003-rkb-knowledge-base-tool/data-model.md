# Data Model

No career data is reproduced here; only the shapes of the files rkb reads and writes. The owner's files live in the career repository, outside Vani.

## 1. Entities & Types

| Entity | Defined in | Notes |
| :--- | :--- | :--- |
| `Bank` (`bank.json`) | `rkb/bank.go` | `headers`, `entries`, `bullets`, `lines`; unknown keys are kept (`Extra`) so a round trip does not drop them. Written atomically under a lock with a compare-and-swap. |
| `Bullet` | `rkb/bank.go` | `text` (with `{{clm}}` claim tokens), `need`, `sources`, `links`, the `*_ok` justifications (`unquantified_ok`, `testing_ok`, `comparison_ok`, `caution_ok`, `self_fix_ok`, `caveat_ok`), `point`, `attested`, `author`, `origin`, and `review` (role to a list of `Review`). |
| `Review` | `rkb/bank.go` | `round`, `verdict`, `note`, `rewrite`, `text_hash` (the wording reviewed), `by`, `at`. A verdict on an older wording is stale and refused by `rkb review`. |
| `Selection` (`resumes/*.json`) | `rkb/bank.go` | `bank`, `meta`, `header`, `sections`; a section is `bullets`, `entries` (`SelEntry` with optional per-resume overrides) or `lines`. |
| `Composed` | `rkb/bank.go` | The selection resolved against the bank; every renderer and check works on it. |
| `Claim` | `rkb/claims.go` | A figure read from `Claims.tex`: `id`, `value_tex`, `value`, `source`, `check`, `notes`, `caution` (CAUTION, DO NOT USE, WITHDRAWN), `file`. |
| `LogEntry`, `Worksheet`, `WorkItem` | `rkb/claims.go` | The verification log (`Claims.verify.json`) and the blind-pass worksheets. |
| `ClaimsFile` (`claims.json`) | `rkb/render.go` | The export Vani reads; fields in `contracts/service.yaml`. |
| `EcoReport`, `EcoLink` | `rkb/eco.go` | Ecosystem-link verification of `knowledge.json`. |
| `Finding`, `Sheet` | `rkb/lint.go` | Lint results and reader review sheets. |
| `FitReport` | `rkb/fit.go` | Page fill per page from the PDF fit. |
| `Manifest` (`manifest.json`) | `rkb/finalize.go` | Every check and the sha256 of every input and output of a release. |
| `MechanismSpec` | `rkb/guards.go` | A configured "point" (`id`, `all` regexps); a malformed entry stops rkb. |
| `Eligibility` | `internal/kbsync/eligible.go` | Vani's view: eligible bullet ids, why each listed bullet is not eligible, entry to bullets, summary bullets. |

## 2. Invariants

- A rewritten file is either the old bytes or the new bytes, never a partial write.
- A write is refused if the file changed since rkb read it.
- A bullet leaves Vani only if `kbsync.Eligible` passes it; the rule is a subset of rkb's final gate.
- No owner vocabulary or personal data is compiled into the binary or committed as a fixture (`CLAUDE.md`).

## 3. Serialization

JSON through `encoding/json/jsontext`; the exact bytes are pinned by `rkb/testdata/json-contract/*.golden.json`. `Claims.tex` is LaTeX, read by `rkb/claims.go`.
