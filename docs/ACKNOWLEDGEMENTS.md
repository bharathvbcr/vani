# Acknowledgements

Vani is one reader for employer job listings that are published across many
applicant-tracking systems. The programs below are what that idea was studied
against, and what this repository actually runs. Licenses were read from each
project's public GitHub repository on 2026-11-07 (`gh api` license field, or
the license file when that field was `NOASSERTION`). A license can change.
The upstream file is the one that counts.

Nothing in this list is an endorsement by that project. Job-posting text is
not theirs to give Vani, and it is not Vani's to give away. See
[LEGAL.md](LEGAL.md).

## Programs this repository runs

These are dependencies or separate programs. They keep their own licenses.
Vani's [PolyForm Noncommercial License 1.0.0](../LICENSE) does not relicense them.

| Project | What Vani uses it for | Licence as published |
|---|---|---|
| [Manvi](https://github.com/bharathvbcr/Manvi) | LLM orchestrator Vani imports | MIT |
| [DevPrism](https://github.com/bharathvbcr/DevPrism) | Deterministic resume-gap ranking over MCP | MIT |
| [Tessl](https://github.com/bharathvbcr/tessl) | In-process embeddings on Metal, `-tags tessl` | Apache-2.0 |
| [gusset](https://github.com/bharathvbcr/gusset) | Go/Rust boundary Tessl links | MIT or Apache-2.0, either one |
| [DevCouncil](https://github.com/bharathvbcr/DevCouncil) | Pulled in through Manvi, not called by Vani directly | Apache-2.0 |
| [Lightpanda](https://github.com/lightpanda-io/browser) | Career pages. A separate process, never linked | AGPL-3.0 |

The `decide` module (OpenRouter Decisions client, Lappi client, router, budget)
is a local checkout named in `go.mod`. It was not a public GitHub repository
when this page was written, so it is not linked.

## Patterns reimplemented here

[docs/PLAN.md](PLAN.md) records these as patterns, not copied trees. Source
files in this repository do not carry an upstream copyright header. The
changelog's line that freehire's MIT adapters were "ported with notice" is
that earlier note. If a file here is a substantial copy of one of these
projects, say so in an issue. It will be marked with the upstream licence or
removed. See [LEGAL.md](LEGAL.md).

| Project | Licence | Pattern Vani reimplemented |
|---|---|---|
| [career-ops](https://github.com/career-ops-hq/career-ops) | MIT | Check liveness before spending a model call. A person presses Submit. |
| [JobSpy](https://github.com/speedyapply/JobSpy) | MIT | One adapter per source, a shared posting shape, a source that fails does not fail the run. Vani reads employer boards, not the aggregator sites JobSpy reads, and it adds cross-board dedupe. |
| [ats-scrapers](https://github.com/kalil0321/ats-scrapers) | MIT | Read the applicant-tracking system the employer publishes on, instead of a repost. |
| [freehire](https://github.com/strelov1/freehire) | MIT | `vani watchlist discover` asks freehire.me which companies have matching roles. It is not a posting source. freehire's own terms allow keyless personal, noncommercial use of the documented API, and the client stays inside that limit. |
| [Seekter](https://github.com/selfishprimate/seekter) | MIT | A daily pass with the seeker's own filters, and a record of what was skipped. |
| [Resume-Matcher](https://github.com/srbhr/Resume-Matcher) | Apache-2.0 | A deterministic match of a resume to a posting. Vani calls DevPrism for that. It does not vendor Resume-Matcher. |
| [Stagehand](https://github.com/browserbase/stagehand) | MIT | Observe a page, then replay the action. Not linked. |
| [Firecrawl](https://github.com/firecrawl/firecrawl) | AGPL-3.0 | Discover pages from `robots.txt` and a sitemap, and ask a cheaper decision before a larger model. Firecrawl itself is not run. |
| [ApplyPilot](https://github.com/Pickle-Pixel/ApplyPilot) | AGPL-3.0 | Keep work-authorization and voluntary self-identification apart from resume facts. Not linked. ApplyPilot can submit applications. Vani does not. |
| [OpenResume](https://github.com/xitanggg/open-resume) | AGPL-3.0 | Studied. Not used. Ranking stays in DevPrism. |
| [JSON Resume schema](https://github.com/jsonresume/resume-schema) | MIT, repository archived | A resume interchange shape. Not a runtime dependency. |

[docs/PLAN.md](PLAN.md) also names a small project called Formhand for per-ATS
fill adapters that never press Submit. No public repository under that name
was identified on 2026-11-07, so it is not linked.

The changelog also names Dossier and Simplify Copilot as idea sources from
the same 2026-10-27 study. No repository was matched to those names here, and
no code was taken from them.

## Listing endpoints

Greenhouse, Lever, Ashby, Workday, and the other boards in the README are the
employers' own career sites and applicant-tracking APIs. Vani reads the
listing endpoints they publish for applicants. That is not a grant of their
postings, their marks, or their terms.
