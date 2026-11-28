# Vani Specifications

7-layer specifications in the DevCouncil format (`devcouncil spec`), describing Vani as it is plus the open work that `docs/FOLLOWUPS.md` records. Vani has no repo-local constitution; each `plan.md` judges its spec against the applicable articles of DevCouncil's `constitution.md` and marks the rest N/A. Vani's own rules are the Rules in `CLAUDE.md` and Rule 7 (`decide/README.md`).

## Active Specifications

| ID | Title | Scope | State | Entry |
|---|---|---|---|---|
| `001` | Listings Pipeline and Decision Rules | Egress, board adapters, the append-only ledger and inbox spool, the daemon, the decision layer and Rule 7, pipeline stages and digest, the `scripts/test.sh` gate | Shipped; live-board, Adzuna, Swift-compile and runner follow-ups open | [`001-listings-pipeline-and-decision-rules/spec.md`](001-listings-pipeline-and-decision-rules/spec.md) |
| `002` | Apply Surfaces: Browser Extension and Mac App | Extension fill, the revision-checked answer bank and native-messaging protocol, job ingest, the Mac app as a CLI client, the Bun/types/Swift suites | Shipped; submit-action reconciliation, Safari run and Swift compile open | [`002-apply-surfaces-extension-and-mac-app/spec.md`](002-apply-surfaces-extension-and-mac-app/spec.md) |
| `003` | rkb Knowledge-Base Tool and Eligibility Gate | The nested `rkb/` module (config layering, safe persistence, verification, fail-closed `finalize`, lint) and Vani's `kbsync.Eligible`, `rkbexec` and `rkbresume` | Shipped; fit-bound re-budget, reader-gate decision and an observed real-bank oracle run open | [`003-rkb-knowledge-base-tool/spec.md`](003-rkb-knowledge-base-tool/spec.md) |

## Validation

The gate is the `specs` suite of `scripts/test.sh`, the one local entry point (`scripts/release.sh` calls it). It runs, in the repository root:

```bash
devcouncil spec validate --all --strict
```

It needs the `devcouncil` binary on `PATH`; without it the suite fails, it never passes unrun. Hosted CI (`.github/workflows/fresh-install.yml`) runs only the Go suite, so the spec gate is local only. To run just this suite, or one spec with machine-readable output:

```bash
scripts/test.sh --suite specs
devcouncil spec validate 003-rkb-knowledge-base-tool --strict --json --project-root "$PWD"
```
