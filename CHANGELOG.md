# Changelog

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
