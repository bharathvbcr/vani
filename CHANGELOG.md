# Changelog

## [0.2.0] - 2026-10-27

Prepared on 2026-10-27. Race-tested code is `8ca5be682f45ec41b9d4c7b275bf8fae08f51dfb` (main at HEAD).
Encompasses the 156 commits since `0.1.0`, including new ATS source adapters, native Excel workbook export,
the Resume Studio engine and macOS UI, and the complete 2026-10-26 deep audit remediation backlog.

### Highlights

- **Ten new ATS and careers board adapters:**
  - **Gem (`gm`):** Reads `https://api.gem.com/job_board/v0/{board}/job_posts/`, single-call whole listing, description pay range parsing (`2a28893`).
  - **SmartRecruiters (`sr`):** Reads `https://api.smartrecruiters.com/v1/companies/{company}/postings`, detail requests capped at 300 per run, two-letter country ISO filtering (`716cdfb`).
  - **Oracle HCM / Recruiting Cloud (`oc`):** Candidate Experience API with pagination, keyword search, and configurable `max_postings` up to 10,000 (`f813573`).
  - **IBM Careers (`ib`):** Covers IBM (`ibm`) and Confluent (`confluent`) career boards (`30773a1`).
  - **Avature (`av`):** Careers portal HTML adapter (e.g. Two Sigma), host and URL validation (`1eb4f18`).
  - **D. E. Shaw (`ds`) and Atlassian (`at`):** Single-request zero-detail career endpoints (`ab12f42`, `5e34e81`).
  - **Rippling (`rp`):** Public board API at `https://api.rippling.com/platform/api/ats/v1/board/{slug}/jobs` with detail fetches (`7718564`).
  - **Workable (`wk`):** Account widget API at `https://apply.workable.com/api/v1/widget/accounts/{slug}` with v2 shortcode detail fetches (`7718564`).
  - **XML Job Feed (`fd`):** Direct https XML job feeds (e.g. Shopify), partner job ID keys, standard library `encoding/xml` with external entity loading disabled (`7718564`).
- **Excel Workbook Export (`internal/xlsx`, `cd82d32`):**
  - `vani jobs export`: writes selected jobs or the top N (default 100, up to 5,000) sorted by priority, relevance, salary, or date to an `.xlsx` workbook.
  - Supports `--keys-stdin` to export explicit selections from the UI or pipelines.
  - Standard library only (`archive/zip` + XML), zero third-party dependencies.
  - Inline string encoding (`<is><t>`) ensures formula injection (`=`) is treated strictly as plain text.
  - macOS Jobs toolbar gains an **Export to Excel** menu and multi-row selection context menu.
- **Resume Studio & Versioned Resumes:**
  - Storage engine (`internal/resumestore`, `c136c19`): per-job versioned directory (`resumes/<sha256(key)>/`), atomic metadata, PDFs, attempts log, and locks.
  - Model pin (`4b545be`): All resume tailoring turns run exclusively on Claude Opus 5.5 (`claude-opus-5-5`) at high effort with 1,024 reasoning headroom, regardless of `llm.model`.
  - CLI surface (`cmd/vani/jobsresume.go`): `build`, `render`, `show`, `use`, `revalidate`, `diff`, `current`, `coverage`, and `gaps`.
  - Keyword coverage & gaps analysis (`internal/rkbcoverage`, `4ba14c6`): matches resume bullets against job posting keywords, identifies gaps, and appends uncovered skills to `<state>/resumes/gaps.jsonl`.
  - macOS Resume Studio (`0c30569`, `ed31bd7`): Native per-job window (`WindowGroup(for: String.self)`) with outline editing, drag-and-drop bullet reordering, bullet swapping against pool, section movement, entry overrides, PDFKit fitted preview, isolated web view unfitted preview, and redrafting sheets.
  - Browser extension dual-path resume delivery (`ea054d1`): Keyed builds (`resumeJob`) for tracked jobs with versioning vs stateless (`resume`) for untracked pages.
- **Source Updates:**
  - Workday: Support 3-digit pod numbers in tenant hostnames (`b33c049`).
  - Ashby: Fallback to job-board GraphQL query when posting API is disabled (`cc895c7`).
  - Apple: Multi-word query quoting and row parsing without team suffix (`6fbda51`).

### Fixes & Hardening (2026-10-26 Audit Backlog)

- **Symlink & non-regular file defense (`b8dca2c`, `ba1cbe2`, `e59931c`, `84dd8b2`):** `readBoundedFile` and storage layers reject symlinks, FIFOs, sockets, and character devices for `last.json`, impressions logs, marks, and manual-triage readings.
- **Daemon PID & liveness validation (`0f3ac31c`):** Checks process visibility (`EPERM` / `ERROR_ACCESS_DENIED`), binary identity, and start time tolerance to prevent stale PIDs from spoofing an active daemon.
- **Binary replacement detection (`b3448a9`):** `binarywatch` monitors the executable using OS file identity (volume serial + file index on Windows; device, inode, mtime, size on Unix) and exits 75 between runs on replacement.
- **Panic recovery (`f89d650`):** Panics following `guardedRun` finalize the run record as failed, ensuring the pipeline cursor and ledger state cleanly advance.
- **Race-free run requests and history flush (`378c663`, `6a289e4`, `3057b10`):** Mutex lock taken before state inspection; TOCTOU checks prevent concurrent request races and data loss during history flush.
- **Scrubbing & privacy bounds (`dd3d15e`, `5fcf29f`):** Spaced phone numbers, mailboxes, and work-authorization boilerplate are purged from bullet text before embeddings/kbmatch and from redraft instructions.
- **macOS app robustness (`129cdd2`, `be7a171`, `6c1fbc0`, `86e7844`):** Bank answer counters capped at `Int32.max` before SQLite binding; 50MB file size cap verified before loading; bank edits and activity log updates run asynchronously off the main actor; schedule-agent pid wait reliability improved.
- **Extension resilience (`7dc8df5`, `3894b41`, `3bc0b49`, `49a7047`):** Reuses single native messaging port even when `connectNative` appears after import; frame probe timeouts spliced cleanly without poisoning native messaging; bank cache `fresh()` drops in-flight fetches and generation cache; script-bound answer import picker.

## [0.1.0] - 2026-10-21

Prepared on 2026-10-21. Not published. There is no previous tag, so this is the first release note. It names the product surface from commits whose subjects were read for this note. It is not a line-by-line history of every commit since `cbfe2caa94a655a18cc4433c7dd87070c7f2c2cc`.

The race-tested code is `aa8ed0cd35f3d283ec02be1a7acfcc2af1893eb0`. The commit that adds this file is documentation only.

### Highlights

- Vani starts as a listings-first career engine: a Go daemon, the macOS app Vani Apply, and the browser extension (`cbfe2caa94a655a18cc4433c7dd87070c7f2c2cc`).
- Source boards search more than one query, with verified site filters and four search-site ATS kinds (`a0bc4f6052dc03be2fc53a462274bd98381fefd2`).
- Jobs ingest keeps an inbox, a near-duplicate check, cross-source identity, and a Jibe source (`cfb7b0ac7ec0b373bfd78f99e264edaba0da6e0d`).
- Jobs insight adds a location filter, an extension apply broker, and shadow eval (`365a40537ab46c57a0c2fe047ce1fe2b4e14213b`).
- The extension counts fill, save, and scan in every frame and answers every message (`c2444cfd84bd7a6dde05061009fbfafd06a78a08`). Basics and question intents fill the first form (`1bbb13ff6174f17334a179966fa111b61369cc78`).
- Jobs and the digest rank by one Score. The legacy relevance and priority fields are gone (`b71f0ebf89bd84bfb8383a7cc1b9f09d1f1d8f4a`).
- An optional embedding match compares postings with the resume knowledge base (`ce06891745cd836d28437dccbfc4f320513818ca`). It links only with `-tags tessl` on Darwin/arm64 when the Tessl archive is present. Otherwise the build still ships and semantic matching reports that it did not run (`macos/scripts/bundle.sh`).
- The daemon slices `waitUntil`, so sleep and wake cannot skip the daily run (`4ad64d001f52c60228fa260a8e203e209a94340c`). The Mac app announces that run with a native notification that opens Jobs (`fe49bf11b199d6b62f179a9a5adb14b08b4b8a8f`).

### Fixes

- Safari reads an extension script with no charset as windows-1252. A literal combining mark in the résumé normalizer then throws "range out of order" and the background script never loads. The bundler writes every non-ASCII UTF-16 unit as `\uXXXX` (`205849ff0e726b4cf3733282ede2f1af07674978`).
- `attachResume` goes to the sender's tab. The extension typechecks from a pinned compiler. A sender policy refuses the wrong caller before work runs. Scraped links that are not `http` or `https` are dropped (`ff0ba3862bc4d0322e8f31748a2d44acf8cd6610`).
- The daemon exits when the ledger is poisoned, and a knowledge-base sync replay keeps its stamp (`b6dd36a2545cd4f78609be42615576cc9a2cdc57`).
- Fallback title keys keep C++, C#, and .NET apart (`7a6ee3e2da5a0fb9f5cbdc06724bd2009e510731`).
- Test only. Rankfit's race tests now finish inside Go's default 10 minute package deadline (`42c4f64bdeb4fc8e7ddcd62234492c49dba813bd`). On their own, before this change, they took 799.947s.
- Test only. Platt sample keys use an atomic counter, so parallel tests do not share one increment (`aa8ed0cd35f3d283ec02be1a7acfcc2af1893eb0`).
- Test only. The second-facet crawl allows 30s. A quiet run is about 2.7s; Bun's 5s default failed it while a Go race suite shared the machine (`1da07b080a6d4c68838b972dd580129e9d2c3cf0`).

### Hardening

- Append-only stores fsync the directory on create, guard a torn tail, cap note lines, bound a partial knowledge-base sync, refuse rekey on Windows, and bound the spool (`317beb38867f4fb3a54807909d59f51c6d8f85df`).
- The Go tree compiles for Windows and Linux, child processes share one environment list, and the vault allowlist is sealed (`333f62abdc3d64a4221c9cb642ac05edef32407b`). That commit says `GOOS=windows` and `GOOS=linux` `go build` and `go vet` passed and that nothing ran on those systems. This release preparation did not repeat those builds. Off macOS the vault fails with `ErrNoKeychain` before it runs `security`. There is no Windows or Linux app bundle.

### Breaking changes

- OpenRouter config keys were renamed (`cf5ef63cccd4186070b4950545e4048549faf896`). `jev_model`, `jev_key_env`, `daily_usd.jev`, `min_jev_prob`, and `decide.shadow` `jev` are now `openrouter_model`, `openrouter_key_env`, `daily_usd.openrouter`, `min_openrouter_prob`, and `openrouter`. Old configs and track files still load. A file that sets an old key and a new key to different values is refused. Save writes the new names. Ledger rows written earlier keep the name `jev`; `vani eval` and `vani label` map that name and say how many they mapped. The daily cap file stays `budget/jev.json` on purpose. README "Renamed keys (2026-10-19)" matches this commit and also records that `kbmatch.backend` `"jev"` became `"openrouter"` on 2026-10-20.

### Known issues

- Nothing publishes v0.1.0. There is no release workflow, no checksum, no SBOM, and no updater. Signing in `macos/scripts/bundle.sh` defaults to Apple Development with `--timestamp=none`, then `codesign --verify --deep --strict`. It does not notarize. The decision is GitPulse `ft-52b6e7d3f0950c24195d9c20c0a20351`.
- A clean clone cannot build. `go.work` is gitignored and required, `go.mod` replaces sibling checkouts, and `go.sum` is absent. The decision is GitPulse `ft-8d8ca2ea609144eb5240c2b743487d58`.
- `atomicfile.Write` replaces a symlinked path with a regular file. That bug is GitPulse `ft-22d7da5f2735997b8bbdb7934bc1345c`, still in progress, and this release does not change it.
- `CFBundleShortVersionString` and the extension manifest are `0.1.0`. `CFBundleVersion` is `1`. `extension/package.json` has no version field.
- Marketing version `0.1.0` is the extension manifest and the two short version strings above. There is no root `package.json`, no `Cargo.toml`, no `Cargo.lock`, and no `tauri.conf.json`.
