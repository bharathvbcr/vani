# Extension ingest: scraped jobs and saved searches

When the extension scrapes a listing page, the user can send the jobs to Vani. Vani then:

1. Queues the jobs.
2. Folds them into the ledger and analyses them (triage, then score, then explain).
3. If the user asks, saves the page as a watchlist board, so later runs search it again.

## Why there is a spool

- The daemon holds the ledger's exclusive lock (`ledger.Open`, `filelock.TryLock`) for its whole life, so no other process can write the ledger while it runs.
- The daemon loads `config.json` at start (`cmdDaemon`) and re-reads the whole file before each run and each inbox poll (`env.reloadConfig`). Only the schedule is fixed for the process's life (a change is noted on stderr and applies after a restart; so does a replaced `vani` binary, which the same reload notices and answers with exit 75). A file that cannot be read keeps the previous config and records `config_stale`.

So `vani jobs ingest` does not write the ledger. It writes a batch file to an inbox, and the ledger owner drains the inbox. A poll that leaves the ledger unusable ends the daemon (exit 1, `daemon.ErrFatal`) within one poll interval.

## Flow

```
extension popup / content drawer
  -> runtime message {type:"ingestJobs", page:{url,title}, saveSearch, jobs:[...]}
background.ts
  -> native {op:"jobsIngest", page:{url,title}, save_search, jobs:[...]}
NativeProtocol.swift (Chrome host via JobsBackend; Safari via agent XPC)
  -> `vani jobs ingest --json`, request JSON on stdin (the native request minus "op")
vani jobs ingest
  -> validates the batch and, with save_search, adds the board to config.json
  -> writes <stateDir>/inbox/<ns>-<rand>.json
  -> when the ledger lock is free (no daemon running), drains the inbox now
daemon
  -> drains the inbox every 2 minutes and at the start of each daily run
  -> folds each batch with Reconciler.ReconcilePartial (no absence pass)
  -> advances the new Live keys, bounded by advance_per_run
  -> each daily run re-reads the watchlist, so a saved board is searched from then on
```

## `vani jobs ingest --json`: input on stdin

```json
{
  "page": {"url": "https://careers.acme.com/search?q=engineer", "title": "Jobs at Acme"},
  "save_search": true,
  "jobs": [
    {"title": "...", "company": "...", "location": "...", "url": "https://...",
     "snippet": "...", "posted": "..."}
  ]
}
```

### Bounds

Anything over a bound is refused, never cut.

| Item | Bound |
|---|---|
| stdin | 1 MiB |
| jobs | 1..200 |
| `title` | 1..300 bytes, required |
| `company` | ≤200 bytes |
| `location` | ≤200 bytes |
| `url` | required, http(s), ≤2048 bytes, accepted by `posting.CanonicalURL` |
| `snippet` | ≤4000 bytes; it becomes `Posting.Description`, which is untrusted text |
| `posted` | ≤64 bytes; parsed only when it is RFC 3339, otherwise ignored |
| `page.url` | https only when `save_search` is set, ≤2048 bytes |
| `page.title` | ≤300 bytes |

- A job that breaks a rule is rejected on its own, and the reason is reported. The other jobs are still queued.
- Two jobs with the same canonical URL count once.

## Output on stdout

The exit code is 0 even when some jobs were rejected.

```json
{
  "queued": 12,
  "duplicates": 1,
  "rejected": [{"index": 3, "reason": "url is not http(s)"}],
  "search": {"status": "added|exists|not_requested|unsupported|invalid|error",
             "ats": "gh", "token": "acme", "company": "Acme", "url": "...", "reason": "..."},
  "analysis": "daemon|next_run"
}
```

`analysis` says when the jobs will be analysed:

- `daemon`: a daemon holds the ledger, and its inbox poll will analyse them within minutes.
- `next_run`: no daemon is running. The jobs were folded into the ledger as `live` and wait for the next `run-once` or daemon run.

The output never claims that analysis happened.

Exit codes:

- 2: the payload is malformed, or no job is valid and no search was saved. The reply always carries `"error"` (the Swift host shows only that on a non-zero exit); when jobs were rejected it also carries `rejected`, e.g. `"no job in the batch was valid: job 0: url is not http(s)"`.
- 1: a runtime failure.

## Saving the search (`save_search: true`)

The listing URL becomes one board. It is checked with `config.ValidBoard`, deduplicated with `sameBoard`, and written with `config.Save`. This reuses the `watchlist add` code.

| Listing URL | Board |
|---|---|
| `boards.greenhouse.io/{t}`, `job-boards.greenhouse.io/{t}` | `gh/{t}` |
| `jobs.lever.co/{s}` (`jobs.eu.lever.co` → region `eu`) | `lv/{s}` |
| `jobs.gem.com/{t}` | `gm/{t}` |
| `jobs.smartrecruiters.com/{c}`, `careers.smartrecruiters.com/{c}` | `sr/{c}` |
| `jobs.ashbyhq.com/{n}` | `ab/{n}`. A name with a space (`Hippocratic%20AI`) cannot be a token, which every posting key is built from, so the token is its lower-case slug (`hippocratic-ai`) and `board_name` holds the literal name the API is asked for. |
| `lifeattiktok.com/search[/{id}]` | `tt/tiktok` |
| `wd{N}.myworkdaysite.com/[locale/]recruiting/{tenant}/{site}` | `wd/{tenant}/{site}` on that host (it names no tenant, so the path does), with the search taken from `q` as below. |
| `{tenant}.wd{N}.myworkdayjobs.com/[locale/]{site}` | `wd/{tenant}/{site}`, with host set and the search taken from `q` when present. Only `q` is kept: the adapter applies no facets, so other URL parameters do not narrow the board. Before saving, Vani makes one limit-1 request to the board host (`workday.CheckSize`, through `egress`) for the search's total. A search over the board's cap (`workday.MaxPostings`, 5000, or the board's `max_postings`), which would fail every daily run, is not saved: status `invalid`, with a reason that says to narrow it with search terms. If the request fails, the search is not saved either: status `invalid`, with a reason that says it could not be checked and to try again. In both cases the jobs are still queued. |
| `{tenant}.fa.oraclecloud.com/hcmUI/CandidateExperience/{lang}/sites/{site}` | `oc/{tenant}/{site}` on that host, with search terms parsed from query parameters (`k` or `keyword`) when present. |
| Workable (`apply.workable.com`), Rippling (`ats.rippling.com`), and per-tenant ATS hosts (Recruitee, Pinpoint, BambooHR, Personio) | Saved as a `pg` board (below) rather than an ATS board, so specific URL search paths, department facets, and query filters are preserved for future Lightpanda scrapes. |
| A known aggregator: linkedin, indeed, glassdoor, ziprecruiter, wellfound, dice, monster, simplyhired, plus their subdomains | No board. Status is `unsupported`, with reason "{host} needs a signed-in browser; Vani cannot search it on its own". The jobs are still queued. |
| Any other https page | A `pg` board, described below |

If the config cannot be read or written (or another process holds its update lock for ten seconds), the status is `error` with a `reason`; the jobs are still queued and the exit code is 0. The saved URL drops query parameters that can carry a credential (any name with the word token, auth, key, secret, email, session or sid in it).

For a `pg` board:

- **`token`:** a slug of the host, made unique against the watchlist by adding a short hash of the path.
- **`url`:** the listing URL, scrubbed of tracking parameters. These are `utm_*`, `gclid`, `fbclid`, `msclkid`, `ref`, `refid`, `trk`, `trackingId`, `sid`, `session*` and `token`. Search parameters such as `q`, `location` and `department` are kept, because they are the search.
- **`link_pattern`:** derived from the scraped job URLs. It is `^https://{QuoteMeta(host)}/{QuoteMeta(longest common path prefix)}`.
  - Every scraped job URL must match it, and so must at least two distinct ones.
  - If the job URLs span more than one host, the status is `invalid`, with a reason.
  - The link host may differ from the listing host.
- **`allowed_hosts`:** the listing host is added. This is the same thing `watchlist add --allow-host` does.
  - **Security impact:** this widens what the daemon may fetch.
  - It happens only with `save_search`, which is sent only after the user clicks "Save search" in the extension. It never follows from anything on the page.
  - The `pg` adapter still honours `robots.txt` and pacing.

`company` is taken from the majority company of the scraped jobs. When there is none, it falls back to the page title, then to the host.

## Folding a batch (ledger owner only)

- **Board and keys:** every batch is folded under a `pg` board. Its token is the saved board's token when that board is `pg`, and otherwise the host slug. Each job is keyed `posting.PageKey(token, url)`. No new ATS value is introduced, and no `Board` field is added.
- **Duplicates:** when an ATS board later lists the same role, `twinOf`/`markWeakTwins` marks the weakly keyed record as the duplicate.
- **`Reconciler.ReconcilePartial`:** it shares the listing loop with `Reconcile` but skips the absence pass. A scrape is not a full listing and must never age out records.
- **Bad batch files:** a batch file that cannot be parsed is moved to `inbox/bad/` and reported. Neither the daemon nor the drain retries it. `bad/` keeps the newest 200 files; a drain deletes older ones.
- **Spool full:** with 1,000 batches already waiting, `jobs ingest` fails (exit 1, `error` in the JSON reply: "inbox: spool is full ...") and writes nothing. Drain the inbox (daemon or `vani run`) and send the batch again.
- **Drain bounds:** at most 50 files per drain.

## Native op `jobsIngest`

- **Request:** `{"op":"jobsIngest","page":{...},"save_search":bool,"jobs":[...]}`. Swift checks the shape and the bounds before running the CLI, then passes the CLI's JSON through.
- **Errors:** `{"error","code"}`, where `code` is one of `bad_request`, `unsupported` or `jobs`.
- **What may be logged:** the event and the counts only. Never a page URL, a title or a job.

It replaces the extension's `trackJob`/`jobTrack`. That op was sent, but no host ever handled it.

## Saved searches in the app

`vani watchlist list --json` prints `[{ats, token, company, region?, host?, search?, searches?, url?, link_pattern?}]`. The macOS Jobs tab shows these as "Saved searches".
