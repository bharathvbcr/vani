# Reading provenance

What Vani records about each model reading so a training export (Lappi-decision's
`crates/qd-prep/src/job_pairs.rs`) can prove which model read which exact text under
which question. Every field below is additive: no existing annotation or store field
was renamed, removed or changed. Records written before 2026-11-11 have none of them,
and nothing is back-filled.

All hashes are lowercase hex sha256 (64 characters) of the raw UTF-8 bytes of one
field, exactly as the backend was asked: no whitespace folding, no case change
(`posting.RawSHA256`, which `eval.HashText` is). The normalised `desc_hash` and
`triage.basis_hash` are a different hash and stay as they were.

## Ledger events (`triage.*` annotations)

| Key | On which events | Value |
|---|---|---|
| `triage.desc_sha256`, `triage.title_sha256`, `triage.location_sha256` | the event of a triage that asked the backends, kept or dropped (exactly the triage events the next list does not exclude) | hash of the posting's description, title, location the question was built from. The primary readings and any in-line shadow reading on that same event were all asked about this text. |
| `triage.<slot>.shadow_model` | beside `triage.<slot>.shadow_backend` = `escalate`, on a triage event or a backfill event (reason `shadow_backfilled`) | the model id the escalation tier asked that run (`llm.triage_model`, else `llm.model`), for example `claude-sonnet-5-5`. Also present when the tier's reading was an outage. |
| `triage.shadow_desc_sha256`, `triage.shadow_title_sha256`, `triage.shadow_location_sha256` | backfill events only (reason `shadow_backfilled`) | hash of the text the tier was asked about. The tier may read a later version than the primary did, so these never overwrite `triage.desc_sha256`. |

Not bound, on purpose:
- A copied reading (`triage.inherited_from` set). The copy matched on the normalised description, so the model never read this posting's raw text.
- A title or exclusion drop (`triage.backend` = `title` or `exclude`) and a too-short skip (`triage.skipped`). No backend read the description.

A later triage of the same key writes `""` for every `triage.*` key it does not set (the ledger's forget rule), so read `""` as absent. A kept-across-edit event carries no binding; the key's last binding and `triage.basis_hash` still describe what the triage read.

`triage.<slot>.shadow_model` is absent for a shadow backend that does not name its model (the Lappi shadow) and for a model id that holds `;`, `=`, whitespace or a control character (`pipeline.TagModel` refuses to tag one it could not read back whole).

`triage.model` is unchanged: its `llm=` part is still `llm.model`, not the escalation tier's model.

## Teacher store (`teacher_readings.jsonl`)

| Field | Value |
|---|---|
| `title_sha256`, `location_sha256` | hash of the raw title and location the question rendered beside the description. On every row from this change, `career.jd_triage` and `career.kb_match` alike. |

`text_sha256`, `fixture_sha`, `prompt_hash`, `kb_digest_sha` and the row identity are unchanged. The new fields are not part of the identity, so no stored reading is asked again because of them.

## Manual retriage store (`retriage/readings.jsonl`, `op: "set"` lines)

| Field | Value |
|---|---|
| `fixture_sha` | `eval.FixtureSHA()`: sha256 of `internal/decide/jdtriage/fixture.json` (the teacher's jd fixture sha) |
| `prompt_hash` | `eval.PromptHash(summary)`: the teacher's jd `prompt_hash` for the normalised target summary |
| `text_sha256`, `title_sha256`, `location_sha256` | hash of the raw description, title and location asked about |

The rows carry no `task` key; every retriage reading is `career.jd_triage`. `model` was already the model asked. The store's own `text` and `prompt` keep their meaning. A clear line carries none of the fields.

Older `vani` binaries read the retriage store with unknown fields refused, so they treat a store holding a new line as corrupt. Install the new binaries together, and do not roll back to one older than this change once a new reading is stored.

### Why the old retriage `prompt` never matched the teacher's `prompt_hash`

They are two hash recipes over the same question, not two questions. The retriage store's `prompt` is `sha256(system ‖ 0x00 ‖ summary ‖ 0x00 ‖ fixture.json)` (`manualtriage.PromptSHA256`): it folds the fixture in, so it has no separate fixture sha. The teacher's `prompt_hash` is sha256 over the length-prefixed system prompt, the user message with the context left empty, and the summary (`eval.PromptHash`); its fixture sha is separate. Both are built from `jdtriage.Triager` with the same normalised summary and `llmbackend.Messages`.

On 2026-11-11 the current code with the live target summary gives `c55fc1706e78…` for the first recipe and `b0c029e66b55…` for the second, which reproduces both stored values. So the stored retriage rows were asked the same system prompt, summary and fixture as the teacher's bound rows.

One caveat: the retriage recipe does not cover the user-message template, so that the template was also unchanged when those rows were written is inferred, not proven by their hash. `cmd/vani` `TestRetriageAsksTheQuestionPromptHashFingerprints` pins that the question a manual reading sends is the one `PromptHash` fingerprints.
