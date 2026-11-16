# Sample knowledge base

A complete, invented knowledge base for a fictional "Sample Person", in rkb's default layout. Every
name, project and figure in it is made up. Copy it as the starting point for your own, or point
rkb and Vani at it to see the whole path work before you write anything.

```
examples/                       <- the code root (RKB_CODE_ROOT, Vani's kb.code_root)
  career/CurriculumVitae/
    core/Claims.tex             <- the only place a figure's value lives (\newcommand{\clmName}{value})
    resume/bank.json            <- every bullet and entry, written once
    resume/resumes/sample.json  <- one selection: which entries and bullets a resume uses
    Claims.verify.json          <- rkb's verification log (manual pass 1 for two figures)
  projects/                     <- the artifacts the figures come from (sources, claims add)
```

How it was built, with rkb's own commands (no hand-edited reviews):

1. `Claims.tex`, `bank.json` (header and entries) and `resumes/sample.json` were written by hand.
2. `rkb bank set <id> --text … --need … --sources … --by sample-person` created each bullet; the
   entry's `bullets` list in `bank.json` was then edited to attach it.
3. `rkb claims add --claim clmShelfSearch --found "…" --source-path projects/shelfwise/bench.txt`
   (and the same for `clmSkyCalls`) recorded a manual pass 1, checked against the artifact.
4. `rkb claims export --out claims.json`, then four reader rounds:
   `rkb lint resumes/sample.json --claims claims.json --sheet r1-hr.json --round 1`, a reader filled
   only the `hr` verdicts and set `reviewer`, and `rkb review resumes/sample.json r1-hr.json --claims
   claims.json` recorded it. The same for round 1 HM, round 2 HR and round 2 HM, each by a
   different reviewer, none of them the author.

What it shows:

| Bullet | Vani (`kbsync.Eligible`) | Why |
|---|---|---|
| `shelf-title-index` | eligible | figure resolves; HR and HM each passed round 1 and round 2 on this wording, independent reviewers |
| `sky-batched-calls` | eligible | the same |
| `shelf-loan-reminders` | withheld | no reader has reviewed it: "HR reader has not passed this wording in both rounds" |

`rkb bank check` and `rkb lint` pass with no errors. `rkb lint --final` (the release gate for a
finished PDF) still fails, by design: it also needs blind pass 2 for every figure and statement
(`rkb claims worksheet` / `claims ingest`) and a validator report (`rkb claims vm`), and the sample
ships no validator. Vani's eligibility rule is a subset of that gate and does not need them.

Try it (from the Vani repo root, after `scripts/bootstrap.sh` built `build/rkb`):

```bash
cp -R rkb/examples /tmp/my-kb            # rkb writes lock files next to bank.json; work on a copy
RKB_CODE_ROOT=/tmp/my-kb build/rkb bank check
RKB_CODE_ROOT=/tmp/my-kb build/rkb claims export --out /tmp/my-kb/claims.json
RKB_CODE_ROOT=/tmp/my-kb build/rkb lint /tmp/my-kb/career/CurriculumVitae/resume/resumes/sample.json --claims /tmp/my-kb/claims.json
```

Then wire it into Vani (`docs/KNOWLEDGE_BASE_GUIDE.md`, "Try the sample knowledge base").
