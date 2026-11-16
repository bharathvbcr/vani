# rkb

`rkb` keeps an evidence-backed resume knowledge base: one bullet bank (`bank.json`), small
selections of it (`resumes/*.json`), and the figures behind every bullet (`Claims.tex`), each
checked against the project artifact it came from. It lints bullets, runs blind verification
passes, renders HTML/PDF/DOCX/text and LaTeX, and exports the approved claims. Go standard library
only. It is a nested module of this repository, built on its own.

## How Vani uses it

Vani never builds the bank. It runs the binary named by `kb.rkb` in its config:
`kb sync`, `draft`, `suggest`, `kbmatch`, `resume` and the semantic profile call
`rkb claims export` and read only bullets that pass `kbsync.Eligible`. `kb.code_root` is passed to
rkb as `RKB_CODE_ROOT`. The knowledge-base layout and the eligibility rules are in
[`docs/KNOWLEDGE_BASE_GUIDE.md`](../docs/KNOWLEDGE_BASE_GUIDE.md).

## Build

```bash
GOWORK=off go -C rkb build -o ../build/rkb .
GOWORK=off go -C rkb vet ./...
GOWORK=off go -C rkb test ./...
```

`GOWORK=off` because the repository's `go.work` does not include this module. Point `kb.rkb` at the
absolute path of `build/rkb`. `rkb help` lists every command; `rkb doctor` checks the setup.

External tools, each needed only by the commands that use it: Chrome or Chromium (PDF, page fill;
`RKB_CHROME` names a binary), poppler `pdftotext`/`pdfinfo`, and optionally LibreOffice and
`pdflatex`.

## Configuration

rkb starts from the generic default embedded in the binary
([`assets/config.json`](assets/config.json)) and layers one user file over it, the first that
exists:

1. `<skill>/rkb/assets/config.json`, where `<skill>` is `RKB_SKILL_DIR`, or else the nearest folder
   above the binary that holds a `SKILL.md`.
2. `~/.config/rkb/config.json`. This one also works when Vani runs rkb, because Vani passes rkb
   only `PATH`, `HOME`, `TMPDIR` and `RKB_CODE_ROOT`.

Keys the file sets replace the default's (a list replaces the whole list); keys it leaves out keep
the default, so a user file usually holds only the per-user lists below. A file that exists but
cannot be read or parsed stops rkb; it never falls back to the default. `rkb doctor` prints which
file is in use. Paths are relative to
the code root (the folder holding `career/CurriculumVitae`): `--code-root`, `RKB_CODE_ROOT`, the
working directory or one of its parents, or `~/Code`.

The per-user keys start empty in the default:

| Key | What it holds |
|---|---|
| `repos` | project name → repo folders under the code root, for figure and integration checks |
| `dependency_aliases` | project name → names its dependents use for it |
| `groups` | ecosystem groups used when `knowledge.json` is absent |
| `bullet_rules.codenames` | your project codenames; a Summary bullet naming two or more is an error. Empty turns off only that check |
| `bullet_rules.lead_jargon`, `tech_terms` | your field's words kept out of a bold lead unless glossed, and counted as jargon |
| `bullet_rules.extra_banned`, `dup_phrases`, `summary_banned` | your own filler, garbled and Summary phrasings to refuse (regexps, case-insensitive; `summary_banned` is plain text) |
| `bullet_rules.mechanisms` | points your bullets keep making in different words, `{"id": …, "all": [regexps]}`; two bullets on one page making one point are refused. A malformed entry stops rkb |
| `tex_labels` | LaTeX source stem → selection label for `bank import-tex` |
| `sibling_roots` | top-level folders under the code root that hold repos and may be unmounted in a sandbox (sources there are then skipped, not failed). Empty: a missing source is always an error |

The bank's `headers.default` (name and contact lines) is yours to fill in `bank.json`; nothing
personal is compiled into the binary.
