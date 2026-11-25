# Agent & Developer Guide: Building an ATS Adapter

How to add a new job-board adapter to Vani without duplicating shared rate limits, HTML cleaners, egress guards, or reconciliation logic. Checked against `internal/sources`, `internal/posting`, `internal/config`, `cmd/vani`, and `internal/sources/goneparity`.

---

## 1. Before writing Go code

First verify that an existing generic mechanism does not already cover the board:

1. **Does an existing adapter cover it?** Check every built-in ATS code in [`internal/posting/posting.go`](../internal/posting/posting.go) (including multi-tenant `wd`, `oc`, `ef`, `jb`, `av`, `tb`, the 8 subdomain adapters `rc`, `pp`, `bh`, `ps`, `bz`, `tm`, `hv`, `hb`, the single-site `mt`, `jv`, `mn`, and the job boards `hm`, `ro`, `zr`, `dc` and `ad`, whose rows each name their own employer).
2. **XML job feed (`fd`)?** If the site serves `<source><job>...</job></source>`, [`internal/sources/feed`](../internal/sources/feed/feed.go) already parses it with zero new code.
3. **Static or Lightpanda page with JSON-LD `JobPosting` (`pg`)?** If the company has ≤50 roles and embeds `JobPosting` JSON-LD on each role page, use `watchlist add --ats pg --url ... --link-pattern ...` ([`internal/sources/page`](../internal/sources/page/page.go)).
4. **One-off / heavily authenticated JS board?** Use the browser extension's **Crawl all pages → Vani** (`extension/src/crawler.ts` -> `vani jobs ingest`).

Write a new adapter under `internal/sources/<name>/` when an ATS exposes a public structured endpoint (JSON/XML/HTML listing) that should sync automatically on every watchlist run.

---

## 2. Core Contract & Security Rules

Every adapter implements `sources.Source` in [`internal/sources/source.go`](../internal/sources/source.go):

```go
type Source interface {
    ATS() posting.ATS
    List(ctx context.Context, b Board) ([]posting.Posting, error)
}
```

Every adapter MUST preserve these invariants:

- **Egress only via `egress.Doer`:** Never construct `http.Client` or dial sockets directly. Every request must go through `egress.Doer` (`internal/egress/doer.go`), which enforces `allowed_hosts`, HTTPS-only redirects, and private/loopback IP blocks.
- **Canonical keys (`posting.Key`):** Build keys with a dedicated `<Name>Key(...)` helper in [`internal/posting/key.go`](../internal/posting/key.go). Keys are lowercased `ats:part1:...` strings (`ErrKeyPart` refuses empty parts or `:`). Never change key structure after shipping — keys are the ledger's identity, and `Board.KeyPrefix()` ([`internal/sources/source.go`](../internal/sources/source.go)) must match every key produced so reconciliation ages absent postings accurately.
- **Fail closed, never silent truncation:**
  - `404` on a board endpoint returns `srcutil.ErrBoardNotFound`.
  - Auth/gateway failures (e.g. `401` on missing headers as noted in `internal/sources/hibob/hibob.go`, `403`, `5xx`, malformed JSON, missing expected array) MUST return an error, **never** `[]posting.Posting{}, nil` (an empty list tells the reconciler to age every open posting toward dead).
  - Exceeding `srcutil.MaxPostings` (`15000`) must fail via `srcutil.CheckCount(n)` (`srcutil.ErrTooManyPostings`), never truncate silently.
- **Untrusted payloads bounded & sanitized:** Validate board inputs at entry (`srcutil.CheckBoard` or `srcutil.CheckTenant`), validate URLs with `srcutil.CheckURL` (rejects `javascript:`, `data:`, userinfo), parse timestamps with `srcutil.ParseTime`, clean titles with `srcutil.Title(htmltext.Text(..., srcutil.MaxTitle))` (max 300 B), and build descriptions via `srcutil.Lead(...) + srcutil.Sections(room, ...)` bounded to `srcutil.MaxDescription` (`64 KiB`).
- **Tolerate isolated bad rows only when a `BadRowSink` is active:** Wrap per-row validation/mapping errors through `rows := srcutil.NewRows(ctx, b, total)` -> `rows.Bad(err)` -> `rows.Done()`. Without a sink, `Bad` fails on the first invalid row; with a run sink, up to `srcutil.MaxGone(total)` (`max(1, n/10)`) malformed rows are skipped and reported as a shortfall so the rest of the board still updates safely.

---

## 3. Choose the Right Pattern

| ATS shape | Reference implementation | Key `srcutil` helpers to reuse |
|---|---|---|
| **Single GET per-tenant subdomain (`{token}.domain.com`)** | [`internal/sources/hibob/hibob.go`](../internal/sources/hibob/hibob.go), [`recruitee`](../internal/sources/recruitee), [`hirehive`](../internal/sources/hirehive) | `srcutil.CheckTenant`, `srcutil.DoJSON`, `srcutil.CheckCount`, `srcutil.NewRows`, `srcutil.Lead`/`Sections`, add host suffix to `sources.TenantHostSuffix` |
| **Single GET hosted board (`api.vendor.com/{token}`)** | [`internal/sources/gem`](../internal/sources/gem), [`workable`](../internal/sources/workable), [`greenhouse`](../internal/sources/greenhouse) | `srcutil.CheckBoard`, `srcutil.GetJSONLimit` / `DoJSON`, explicit static host in `config.BoardHost` |
| **Paginated listing + per-posting detail requests** | [`internal/sources/smartrecruiters/smartrecruiters.go`](../internal/sources/smartrecruiters/smartrecruiters.go), [`bamboohr`](../internal/sources/bamboohr) | `NewWith(d, Options{KeepsBody, Stored, Now, Wait, Report})` + `srcutil.Pacer` + `srcutil.PlanDetails` (`RefreshDays = 30`, `MaxDetails`, `srcutil.MaxGone` for detail 404s) + `srcutil.Relocate` |
| **A job board's MCP server (Streamable HTTP)** | [`internal/sources/ziprecruiter`](../internal/sources/ziprecruiter) (search only), [`dice`](../internal/sources/dice) (search + bounded detail step) | [`internal/sources/mcphttp`](../internal/sources/mcphttp) (`New`, `Open`, `Call`; a tool's `isError` is `*mcphttp.ToolError`, a 404 is `mcphttp.ErrEndpointGone`, not `srcutil.ErrBoardNotFound`), `srcutil.PostExchange` for a reply whose headers or 202 matter |
| **An API that needs the owner's key** | [`internal/sources/adzuna`](../internal/sources/adzuna) | `Options.Credentials` injected by `cmd/vani` (environment, then the Keychain through `w.modelKey`); a wrapping `egress.Doer` adds the key to the request, so srcutil, logs and errors never see it, and scrubs every error (`url.Redacted()` hides no query parameter) |
| **Search / custom host / query filters** | [`internal/sources/workday`](../internal/sources/workday), [`eightfold`](../internal/sources/eightfold), [`oraclehcm`](../internal/sources/oraclehcm) | Register verified filter keys in `sources.FilterKeys`; validate `b.Host` strictly in `config.ValidBoard` and `config.BoardHost`; iterate `b.SearchTerms()` |

---

**Windowed listings.** A search that can never be listed whole (ZipRecruiter's 10 to 30 rows) or that a recency filter narrows (`posted_within_days`, `max_days_old`) is not the board's whole word: report `Windowed: true` in the adapter's `Stats`, carry it to `boardFetch.Windowed` (`cmd/vani/stagegaps.go`), and the run folds the board with `pipeline.ReconcileWindow` (absent postings held, then closed as expected closures, never a mass failure). A source that lists many employers also gets a `posting.SourceOf` entry, so every surface names it.

## 4. Complete Wiring Checklist (10 Places)

Look at any recently added adapter (`rg -l hibob` across the repo) to see the exact seams every new ATS touches:

1. **ATS constant:** Add `MyATS ATS = "xx"` (unique 2-letter code) in [`internal/posting/posting.go`](../internal/posting/posting.go). Never reuse or change a shipped code.
2. **Posting key & prefix:**
   - Add `MyATSKey(...) (Key, error)` in [`internal/posting/key.go`](../internal/posting/key.go) and unit tests in [`internal/posting/key_test.go`](../internal/posting/key_test.go).
   - If the key uses `ats:tenant:site:id` (like `wd`/`oc`), add the ATS to `Board.KeyPrefix()` in [`internal/sources/source.go`](../internal/sources/source.go) and `TestKeyPrefixForSearchSites` in [`internal/sources/source_test.go`](../internal/sources/source_test.go); otherwise default `ats:token:` handles it.
3. **Per-tenant host or verified filters (if applicable):**
   - If `{token}.vendor.com`, add to `sources.TenantHostSuffix` in [`internal/sources/source.go`](../internal/sources/source.go).
   - If site query filters exist and were verified live against result counts, list them in `sources.FilterKeys`.
4. **URL-to-board mapping (`BoardFromURL`):**
   - Add host/path parsing in [`internal/sources/boardurl.go`](../internal/sources/boardurl.go) and test vectors in [`internal/sources/boardurl_test.go`](../internal/sources/boardurl_test.go). This powers `vani watchlist discover` and the extension's **Save search**. (`TenantHostSuffix` entries are handled automatically.)
5. **Adapter implementation & tests:**
   - Create `internal/sources/<name>/<name>.go` implementing `New(d egress.Doer) sources.Source` (and `NewWith` if stateful/paced).
   - Write `<name>_test.go` and `badrows_test.go` using `srctest.New` / `srctest.NewFunc` (`internal/sources/srctest/srctest.go` fails any unexpected URL/method). Add `func TestMain(m *testing.M) { os.Exit(srcutil.RunWithoutWaits(m)) }` when testing retries/pacing so retry waits return immediately.
6. **Config validation & egress allowlist:**
   - Add the ATS code to the `ValidBoard` switch (and its error list), add any host/URL/token validation, and add the API host mapping to `BoardHost` in [`internal/config/config.go`](../internal/config/config.go).
   - (`TenantHostSuffix` adapters share the `CheckTenant` / `BoardHost` case automatically.) Add config validation tests in `internal/config/tenant_test.go` or `config_test.go`.
7. **Watchlist CLI help & flag validation:**
   - Update `--ats` / `--token` / `--host` / `--filter` help text in [`cmd/vani/watchlist.go`](../cmd/vani/watchlist.go) (`watchlistAdd`).
   - Add to [`cmd/vani/tenantboards_test.go`](../cmd/vani/tenantboards_test.go) if per-tenant.
8. **Pipeline registration:**
   - Register in `apiSources(d)` (and wire `Options{Stored, KeepsBody, Report}` if paginated-detail) in [`cmd/vani/runwire.go`](../cmd/vani/runwire.go).
9. **Contract & parity tests (mandatory):**
   - Add the `posting.ATS` constant to `TestEveryAcceptedBoardKindHasAnAdapter` in [`cmd/vani/sources_test.go`](../cmd/vani/sources_test.go).
   - Add the adapter to [`internal/sources/goneparity/tenantparity_test.go`](../internal/sources/goneparity/tenantparity_test.go) (per-tenant) or [`internal/sources/goneparity/goneparity_test.go`](../internal/sources/goneparity/goneparity_test.go) (paginated/detail) to prove uniform 404, 3xx redirect rejection, >`MaxGone` error, and `BadRowSink` shortfall behavior.
10. **Discovery & documentation (if applicable):**
    - If supported by Freehire discovery, add to `sourceATS` in [`internal/discover/freehire/candidates.go`](../internal/discover/freehire/candidates.go).
    - Update the ATS tables/lists in [`README.md`](../README.md), [`docs/SETUP.md`](SETUP.md), and [`skills/cli-anything-vani/SKILL.md`](../skills/cli-anything-vani/SKILL.md) (checked by `TestSkillCoversEveryCommand`).

---

## 5. Verification Commands

Run after implementing and wiring the adapter (with `VANI_NO_KEYCHAIN=1`):

```bash
VANI_NO_KEYCHAIN=1 go -C /abs/path/to/Vani vet ./...
VANI_NO_KEYCHAIN=1 go -C /abs/path/to/Vani test -race \
  ./internal/sources/<name> \
  ./internal/sources/... \
  ./internal/posting \
  ./internal/config \
  ./cmd/vani
```

Do not claim a new adapter works against a live board unless you also verified the live endpoint shape (for example via `--list-only`) and recorded the observed host/date in the package doc comment (as in `hibob.go` / `source.go`).
