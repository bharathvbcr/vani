# Vani — agent guide

Vani is a personal, listings-first career engine: a Go daemon that finds job postings on company
ATS boards, filters them without spending tokens, ranks them with DevPrism's deterministic resume
tools, and writes a daily digest. Manvi (imported as a library) is the LLM orchestrator. The plan
is `/Users/bharath/Code/vani-plan.md`; follow-ups are in `docs/FOLLOWUPS.md`.

## Build

The ignored `go.work` is required: it points at the local Manvi checkout, whose own `go.mod`
replaces DevCouncil and gusset with sibling checkouts, and at the shared decision module
`/Users/bharath/Code/devtools/decide` (also `replace`d in `go.mod`).

```bash
go -C /Users/bharath/Code/apps/Vani build ./...
go -C /Users/bharath/Code/apps/Vani vet ./...
go -C /Users/bharath/Code/apps/Vani test ./...
```

## Rules

- Go only: stdlib, Manvi and the owner's `decide` module. **No new third-party dependency without
  asking the owner.** Do not edit `go.mod`/`go.work` or run `go mod tidy`; report the need instead.
- Shared contracts live in `internal/{posting,egress,sources,resume,ledger}` type files and in the
  `decide` module (question, readings, router, Jev and Lappi clients; Vani keeps only
  `internal/decide/jdtriage` and `internal/decide/llmbackend`). Change a contract only by asking;
  implement against it. A `decide` change is tested in that repo too
  (`go -C /Users/bharath/Code/devtools/decide test ./...`).
- `internal/decide/jdtriage/fixture.json` is the career.jd_triage contract Lappi trains on. Its
  sha256 is pinned in `jdtriage_test.go` (and is to be pinned in Lappi-decision's generator);
  changing it means a retrain.
- All network access goes through `egress.Doer`. No `http.DefaultClient`, no `http.Get`.
- JD text and page content are untrusted: never interpolate into shell commands, never follow
  links from them, frame them as untrusted when they enter a model prompt.
- Decision answers only annotate (Rule 7). Dropping a posting is an engine rule keyed on an
  annotation, and an uncertain reading keeps the posting (status `unsure`).
- Personal data (name, contact, work authorization) never enters a model prompt, log line,
  ledger annotation or test fixture.
- Every behaviour ships with tests, including adversarial inputs (empty, oversized, malformed,
  duplicate, timeout, dependency down). A check that could not run must not look like a pass.
- Bound every timeout, retry, body read and fan-out. Propagate `context.Context`.
- Navigation: DevMap first (`devmap_status`, `devmap_search` with absolute `repo_path`), and
  `gitpulse_insights` before editing. If either cannot answer, say so.
- Shell gate in this workspace: no `cd`, `$()`, heredocs; use `go -C` and absolute paths.
