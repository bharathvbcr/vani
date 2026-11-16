# New-User Onboarding & Progressive Setup

How to set up Vani on a fresh machine without pre-existing local tools, and adopt optional modules one layer at a time. Checked against current code (`go.mod`, `internal/config`, `cmd/vani`).

> [!NOTE]
> If you already have local checkouts of `Manvi`, `tessl` and `DevPrism` on `$CODE_ROOT/...`, follow [SETUP.md](SETUP.md) for the exact step-by-step bring-up of that full stack.

---

## 1. Choose what you need first

Vani is modular. The core pipeline needs git, Go and an OpenRouter API key; [`scripts/bootstrap.sh`](../scripts/bootstrap.sh) fetches the public sibling checkouts it builds against (section 2). The decision client (`decide/`) and the knowledge-base tool (`rkb/`) are part of this repository. Every other external tool adds a specific optional capability, and the bootstrap lists which of them it found:

| Layer | Capability | External requirement | What runs without it |
|---|---|---|---|
| **Core pipeline (Minimal)** | Watchlists, exclusions, triage, daily Markdown digests | git + Go 1.27.4 + OpenRouter key (or local `llm`); the bootstrap fetches the public `Manvi`, `DevCouncil`, `gusset` and `tessl` checkouts | Everything except resume tailoring, gap ranking, Metal embeddings, and Chrome-rendered careers pages |
| **Desktop & browser** | Autofill job forms, crawl career pages into inbox, Submit -> Applied tracking | Chromium or Safari + [macos/README.md](../macos/README.md) app | CLI-only discovery, triage, and digest workflow |
| **Knowledge base** | Grounded drafts (`draft`/`suggest`), tailored PDFs (`resume`), resume gap ranking | `rkb` (built from [`rkb/`](../rkb/README.md) by the bootstrap) + your `bank.json` + DevPrism (`--mcp`) + Anthropic/Gemini key via Manvi; PDFs also need Chrome and poppler | Jobs still fetch, triage, filter, and appear in digests (ranked by fit without KB coverage) |
| **Semantic ordering** | Embedding similarity against your approved bullets | `tessl/go` Metal archive (Rust `cargo`) + Hugging Face `google/embeddinggemma-2` snapshot | Pure-Go build skips embeddings (`-tags tessl` omitted). The `tessl` checkout itself is still needed at build time because [`go.mod`](../go.mod) requires and replaces `tessl/go`; the bootstrap fetches it |
| **Employer badges** | H-1B LCA history & cap-exempt/non-profit badges | Public DOL OFLC `.xlsx`/CSV, IRS EO BMF, IPEDS `HDyyyy.csv` ([LCA.md](LCA.md), [EMPLOYER.md](EMPLOYER.md)) | Postings omit badges; ranking/dropping unaffected |
| **Careers-page crawler** | Scrape JS-rendered company career pages (`pg` boards) | `lightpanda` binary (`lightpanda_binary`) ([PHASE0-lightpanda.md](PHASE0-lightpanda.md)) | Use the 28 built-in ATS adapters, XML feeds (`fd`), or the extension's **Crawl all pages → Vani** |

---

## 2. Clone dependencies & build

```bash
git clone https://github.com/bharathvbcr/vani.git /abs/path/to/Vani
/abs/path/to/Vani/scripts/bootstrap.sh
```

The script clones `Manvi`, `DevCouncil`, `gusset` and `tessl` into `/abs/path/to/Vani/.deps/` (gitignored; `--deps DIR` puts them elsewhere) at the commits pinned in [`scripts/deps.lock`](../scripts/deps.lock), writes `go.work`, builds `build/vani` and `build/rkb`, runs `vani init` when no config exists and `vani config check`, and ends with a found/missing table for the optional layers above. It honours `VANI_CONFIG`/`VANI_STATE_DIR` (section 3), so set them first for an isolated config. It never edits an existing `go.work`, checkout or config, and running it again is safe.

To have a coding agent do this for you, paste the prompt in the README's [Set up with a coding agent](../README.md#set-up-with-a-coding-agent) section; it runs this script, spends nothing and asks before installing anything.

To lay the checkouts out by hand instead, use [`go.work.example`](../go.work.example) and [SETUP.md](SETUP.md) step 1. Either way, the baseline checks are:

```bash
go -C /abs/path/to/Vani build -o /abs/path/to/Vani/build/vani ./cmd/vani
VANI_NO_KEYCHAIN=1 go -C /abs/path/to/Vani vet ./...
```

---

## 3. Minimal working configuration

Initialize an isolated config/state directory (or omit `VANI_CONFIG`/`VANI_STATE_DIR` to use the OS default under `~/Library/Application Support/Vani/` on macOS):

```bash
VANI_CONFIG=/abs/path/config.json VANI_STATE_DIR=/abs/path/state \
  /abs/path/to/Vani/build/vani init
VANI_CONFIG=/abs/path/config.json VANI_STATE_DIR=/abs/path/state \
  /abs/path/to/Vani/build/vani config check
```

What `vani init` writes (`config.Default()`, mode `0600`):
- `decide.openrouter_model`: `liquid/d1-ultra`
- `decide.openrouter_key_env`: `OPENROUTER_API_KEY`
- `decide.daily_usd.openrouter`: `0.25` (must be in `(0, 50]` while model is set)
- `allowed_hosts`: `["openrouter.ai"]`
- `schedule`: `08:30` `America/Los_Angeles`
- `watchlist`: empty; `llm`, `devprism`, `kb`, `semantic`: unset

Provide your OpenRouter API key via login Keychain (recommended on macOS) or env var for a shell test:

```bash
# Option A: macOS login Keychain (bound to your built binary)
security add-generic-password -U -a "$USER" -s openrouter-api-key \
  -T /abs/path/to/Vani/build/vani -w

# Option B: Agent or CI shell (never read Keychain)
export VANI_NO_KEYCHAIN=1 OPENROUTER_API_KEY=...
```

> [!IMPORTANT]
> Agent shells should always set `VANI_NO_KEYCHAIN=1` before running `vani` commands, and only trigger `run-once --list-only` (which fetches listings without calling or spending from the decision backend).

---

## 4. Populate boards without writing code

Before writing a custom Go adapter, check whether Vani already covers your target company:

1. **28 direct ATS/company adapters** (`gh`, `lv`, `ab`, `wd`, `oc`, `az`, `gg`, `ap`, `ef`, `jb`, `gm`, `sr`, `ib`, `av`, `ds`, `at`, `rp`, `wk`, `rc`, `pp`, `bh`, `ps`, `bz`, `tm`, `hv`, `hb`, `tt`, `tb`):

   ```bash
   /abs/path/to/Vani/build/vani watchlist add \
     --ats gh --token BOARD_SLUG --company "Company Name" --allow-host
   ```

2. **Public XML job feeds (`fd`)**: If the company publishes an XML `<source>/<job>` feed:

   ```bash
   /abs/path/to/Vani/build/vani watchlist add \
     --ats fd --token shopify --company "Shopify" \
     --url "https://..." --allow-host
   ```

3. **Schema.org `JobPosting` careers pages (`pg` + Lightpanda)**: Any HTML listing whose job detail pages expose `application/ld+json` `JobPosting` metadata works via `--url` and a Go `--link-pattern` regexp (capped at 50 matching links):

   ```bash
   /abs/path/to/Vani/build/vani watchlist add \
     --ats pg --token acme --company "Acme" \
     --url "https://acme.com/careers" --link-pattern '/careers/jobs/[0-9]+' --allow-host
   ```

4. **Interactive / JS-heavy sites (`extension` + `vani jobs ingest`)**: Install [extension/README.md](../extension/README.md) in Chrome or Safari and click **Crawl all pages → Vani**. It paginates in the browser, extracts jobs, and optionally saves supported ATS searches to your watchlist ([EXTENSION_INGEST.md](EXTENSION_INGEST.md)).

Test fetching without spending model budget:

```bash
/abs/path/to/Vani/build/vani run-once --board gh:BOARD_SLUG --list-only
```

---

## 5. Set your target profile, exclusions, and track

1. **Target summary (non-PII ceiling):** Edit `target_summary` in `config.json` to describe what roles/systems you target (under 1,024 collapsed-whitespace bytes; **no** name, contact info, employers, schools, locations, or visa status).
2. **Identifying-words guard:** Create `<state dir>/tracks/identifying-words.txt` (one forbidden term per line, `#` comments). `vani run-once` and `vani daemon` refuse to run while `target_summary` is non-empty and this file is missing, empty, or matches `target_summary`.
3. **Hard exclusions (zero spend):** Set `titles`, `title_unless`, `companies`, `locations`, `only_locations`, and `descriptions` via `vani config exclude --set` ([SETUP.md#exclusions](SETUP.md#exclusions)), then verify with `vani prefilter`.
4. **Role tracks:** Organize different target focuses (drop rules, title signals, employer preferences) with `vani track` ([TRACKS.md](TRACKS.md)).

Run your first bounded triage pass:

```bash
/abs/path/to/Vani/build/vani run-once --advance 25 --no-digest
/abs/path/to/Vani/build/vani jobs list --limit 20
```

---

## 6. Next steps (adding optional layers)

- **Build or connect a knowledge base & DevPrism ranking:** [KNOWLEDGE_BASE_GUIDE.md](KNOWLEDGE_BASE_GUIDE.md)
- **Set up the macOS app, Safari extension, and scheduled notifications:** [macos/README.md](../macos/README.md) & [extension/README.md](../extension/README.md)
- **Add a new ATS adapter in Go:** [ADAPTER_GUIDE.md](ADAPTER_GUIDE.md)
- **Reference config/state/budget architecture:** [RUNTIME.md](RUNTIME.md)
