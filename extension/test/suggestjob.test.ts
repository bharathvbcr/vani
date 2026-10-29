import { describe, expect, test } from "bun:test";
import {
  JOB_HELP, MAX_BACKOFF_MS, MAX_CHOICE_OPTIONS, MAX_DRAFT_CHARS, MAX_FIELDS, MAX_REQUEST_OPTION_CHARS, OLDER_VANI, buildSuggestRequest, choiceOptions, codeHelp, isOldShapeRefusal, legacyRequest,
  parseJobStep, parseSuggestReply, runJob, runSuggest,
  type JobDriver, type SuggestField,
} from "../src/suggestjob";
import { DRAFT_HELP } from "../src/draft";

const job = { company: "Acme", title: "ML Engineer", description: "We build inference systems on GPUs. ".repeat(5) };

type Req = { job: { company: string; title: string; description: string }; fields: { id: string; label: string; kind: string }[] };

describe("buildSuggestRequest", () => {
  test("sends labels, kinds and the posting only", () => {
    const r = buildSuggestRequest([
      { id: "f0", label: "  Why\n Acme? *", kind: "textarea" },
      { id: "f1", label: "GitHub URL", kind: "text" },
    ], job) as Req;
    expect(Object.keys(r).sort()).toEqual(["fields", "job"]);
    expect(r.fields).toEqual([{ id: "f0", label: "Why Acme? *", kind: "textarea" }, { id: "f1", label: "GitHub URL", kind: "text" }]);
    expect(Object.keys(r.fields[0]!).sort()).toEqual(["id", "kind", "label"]);
  });

  test("drops blank, oversized, duplicate, badly named and non-text fields, and caps the count", () => {
    const many = Array.from({ length: MAX_FIELDS + 5 }, (_, i) => ({ id: `f${i}`, label: `Q${i}`, kind: "text" as const }));
    const r = buildSuggestRequest([
      { id: "a", label: "  ", kind: "text" },
      { id: "b", label: "x".repeat(1001), kind: "text" },
      { id: "c", label: "Why?", kind: "select" as "text" },
      { id: "has space", label: "Why?", kind: "text" },
      { id: "d", label: "Why?", kind: "text" },
      { id: "d", label: "Why again?", kind: "text" },
      ...many,
    ], job) as Req;
    expect(r.fields[0]).toEqual({ id: "d", label: "Why?", kind: "text" });
    expect(r.fields.length).toBe(MAX_FIELDS);
    expect(new Set(r.fields.map(f => f.id)).size).toBe(r.fields.length);
  });

  test("sends max_chars only for a field that has a limit", () => {
    const r = buildSuggestRequest([
      { id: "a", label: "Why us?", kind: "textarea", maxChars: 2000 },
      { id: "b", label: "Anything else?", kind: "textarea" },
      { id: "c", label: "Short", kind: "text", maxChars: 0 },
      { id: "d", label: "Odd", kind: "text", maxChars: 1.5 },
      { id: "e", label: "Neg", kind: "text", maxChars: -1 },
      // "No limit" written as a huge maxlength: vani suggest refuses anything past 100,000, which would fail the page.
      { id: "f", label: "Huge", kind: "textarea", maxChars: 2_147_483_647 },
      { id: "g", label: "At the bound", kind: "textarea", maxChars: 100_000 },
    ], job) as Req & { fields: Array<{ max_chars?: number }> };
    expect(r.fields.map(f => f.max_chars)).toEqual([2000, undefined, undefined, undefined, undefined, undefined, 100_000]);
    expect(Object.keys(r.fields[1]!).sort()).toEqual(["id", "kind", "label"]); // no key at all when there is no limit
    expect(Object.keys(r.fields[0]!).sort()).toEqual(["id", "kind", "label", "max_chars"]);
  });

  test("refuses a page with nothing to ask", () => {
    expect(() => buildSuggestRequest([], job)).toThrow(/no new free-text questions/);
    expect(() => buildSuggestRequest([{ id: "a", label: " ", kind: "text" }], job)).toThrow();
  });

  test("clips the posting", () => {
    const r = buildSuggestRequest([{ id: "a", label: "Why?", kind: "text" }], { company: "c".repeat(999), title: "t", description: "d".repeat(100_000) }) as Req;
    expect(r.job.company.length).toBe(200);
    expect(r.job.description.length).toBe(60_000); // draft.MAX_DESCRIPTION_CHARS, the cap vani suggest takes
  });
});

describe("parseSuggestReply", () => {
  const asked = new Set(["f0", "f1", "f2"]);

  test("keeps asked fields, drafts only on drafted ones", () => {
    const r = parseSuggestReply({
      fields: [
        { id: "f0", status: "drafted", category: "essay", draft: "  I build inference.  ", model: "claude", evidence_items: 2 },
        { id: "f1", status: "manual", category: "work_authorization", reason: "Answer it yourself.", draft: "sneaky" },
        { id: "zz", status: "drafted", draft: "not asked" },
        { id: "f0", status: "drafted", draft: "duplicate" },
        { id: "f2", status: "approved" },
      ],
    }, asked);
    expect(r.ok).toBe(true);
    if (!r.ok) return;
    expect(r.fields.map(f => f.id)).toEqual(["f0", "f1"]);
    expect(r.fields[0]).toMatchObject({ status: "drafted", draft: "I build inference.", evidenceItems: 2 });
    expect(r.fields[1]!.draft).toBe("");
  });

  test("a drafted field with an empty or oversized draft is a visible failed row with a reason, never dropped", () => {
    const r = parseSuggestReply({ fields: [{ id: "f0", status: "drafted", draft: " " }, { id: "f1", status: "drafted", draft: "x".repeat(MAX_DRAFT_CHARS + 1) }] }, asked);
    if (!r.ok) throw new Error("not a suggestion");
    expect(r.fields.map(f => [f.id, f.status, f.draft, f.code])).toEqual([["f0", "failed", "", "bad_draft"], ["f1", "failed", "", "bad_draft"]]);
    expect(r.fields[0]!.reason).toMatch(/empty draft/);
    expect(r.fields[1]!.reason).toMatch(new RegExp(`longer than ${MAX_DRAFT_CHARS}`));
  });

  test("a duplicate of a failed bad-draft row is still a duplicate", () => {
    const r = parseSuggestReply({ fields: [{ id: "f0", status: "drafted", draft: "" }, { id: "f0", status: "drafted", draft: "real" }] }, asked);
    expect(r.ok && r.fields.map(f => f.status)).toEqual(["failed"]);
  });

  test("errors and junk are failures, never suggestions", () => {
    expect(parseSuggestReply({ error: "spent", code: "budget_exhausted" }, asked)).toEqual({ ok: false, code: "budget_exhausted", error: JOB_HELP.budget_exhausted! });
    expect(parseSuggestReply({ error: "x".repeat(900), code: "weird" }, asked)).toMatchObject({ ok: false, code: "weird" });
    expect(parseSuggestReply({ error: "draft: the draft says \"73%\"", code: "ungrounded" }, asked)).toEqual({ ok: false, code: "ungrounded", error: JOB_HELP.ungrounded! });
    // A refusal for something unbacked is worded by its kind, never as a figure when it was not one.
    const credential = parseSuggestReply({ error: "draft: x", code: "ungrounded", kind: "credential" }, asked);
    expect(!credential.ok && credential.error).toMatch(/a credential.*your own records/);
    expect(!credential.ok && credential.error).not.toMatch(/cited a figure/);
    expect(parseSuggestReply({ error: "draft: x", code: "ungrounded", kind: "nonsense" }, asked)).toEqual({ ok: false, code: "ungrounded", error: JOB_HELP.ungrounded! });
    // An unknown code keeps the host's own text, clipped, so nothing is lost.
    const weird = parseSuggestReply({ error: "x".repeat(900), code: "weird" }, asked);
    expect(!weird.ok && weird.error.length).toBe(500);
    for (const junk of [null, "text", [], { fields: "no" }, {}]) expect(parseSuggestReply(junk, asked).ok).toBe(false);
  });
});

describe("a failed field refused for an unbacked claim", () => {
  const asked = new Set(["f0", "f1", "f2"]);
  const failed = (extra: object) => ({ id: "f0", status: "failed", code: "ungrounded", reason: "draft: the draft says \"PhD\", which is not in the evidence", ...extra });

  test("says what was unbacked, by kind", () => {
    for (const [kind, re] of [["figure", /a figure/], ["years", /years of experience/], ["credential", /a credential/], ["acronym", /an acronym/]] as const) {
      const r = parseSuggestReply({ fields: [failed({ kind })] }, asked);
      if (!r.ok) throw new Error("not a suggestion");
      expect(r.fields[0]!.code).toBe("ungrounded");
      expect(r.fields[0]!.reason).toMatch(re);
      expect(r.fields[0]!.reason).not.toContain("draft says");
    }
  });

  test("a missing or unknown kind is worded generally, and another failure keeps the host's reason", () => {
    for (const kind of [undefined, "weird", 4]) {
      const r = parseSuggestReply({ fields: [failed({ kind })] }, asked);
      expect(r.ok && r.fields[0]!.reason).toBe(JOB_HELP.ungrounded!);
    }
    const t = parseSuggestReply({ fields: [{ id: "f1", status: "failed", code: "timeout", reason: "no reply within 90s", kind: "figure" }] }, asked);
    expect(t.ok && t.fields[0]!.reason).toBe("no reply within 90s");
  });
});

describe("choice questions in the request", () => {
  type Wire = { fields: Array<Record<string, unknown>> };

  test("a choice field goes out with its cleaned options, no max_chars, and nothing else", () => {
    const r = buildSuggestRequest([
      { id: "c0", label: "Notice period", kind: "choice", options: [" Immediately ", "2  weeks", "", "1 month"], maxChars: 50 },
      { id: "f0", label: "Why us?", kind: "textarea", maxChars: 900 },
    ], job) as unknown as Wire;
    expect(r.fields[0]).toEqual({ id: "c0", label: "Notice period", kind: "choice", options: ["Immediately", "2 weeks", "1 month"] });
    expect(Object.keys(r.fields[0]!).sort()).toEqual(["id", "kind", "label", "options"]);
    expect(r.fields[1]).toEqual({ id: "f0", label: "Why us?", kind: "textarea", max_chars: 900 });
  });

  test("options out of bounds make the field unaskable, never a truncated list", () => {
    expect(choiceOptions(["A", "B"])).toEqual(["A", "B"]);
    expect(choiceOptions(["A"])).toBeNull();
    expect(choiceOptions(["A", " "])).toBeNull();
    expect(choiceOptions(undefined)).toBeNull();
    expect(choiceOptions(Array.from({ length: MAX_CHOICE_OPTIONS }, (_, i) => `o${i}`))?.length).toBe(MAX_CHOICE_OPTIONS);
    expect(choiceOptions(Array.from({ length: MAX_CHOICE_OPTIONS + 1 }, (_, i) => `o${i}`))).toBeNull();
    expect(choiceOptions(["A", "x".repeat(200)])?.[1]?.length).toBe(200);
    expect(choiceOptions(["A", "x".repeat(201)])).toBeNull();
    expect(choiceOptions(["Same", "Same"])).toBeNull();
    expect(choiceOptions(["A", 3 as unknown as string])).toBeNull();
    const r = buildSuggestRequest([
      { id: "a", label: "One option", kind: "choice", options: ["Only"] },
      { id: "b", label: "No options", kind: "choice" },
      { id: "c", label: "Why?", kind: "text" },
    ], job) as unknown as Wire;
    expect(r.fields.map(f => f.id)).toEqual(["c"]);
    // A page whose only question was unaskable has nothing to ask.
    expect(() => buildSuggestRequest([{ id: "a", label: "One option", kind: "choice", options: ["Only"] }], job)).toThrow(/no new free-text questions/);
  });
});

describe("a drafted row for a choice question", () => {
  const asked = new Set(["c0", "f0"]);
  const options = new Map([["c0", ["Immediately", "2 weeks"]]]);
  const row = (extra: object) => ({ id: "c0", status: "drafted", answer_kind: "choice", choice: "2 weeks", category: "notice", model: "claude", evidence_items: 3, ...extra });

  test("carries the option it picked, and only when it is one that was sent", () => {
    const r = parseSuggestReply({ fields: [row({})] }, asked, options);
    if (!r.ok) throw new Error("not a suggestion");
    expect(r.fields[0]).toMatchObject({ status: "drafted", answerKind: "choice", choice: "2 weeks", draft: "2 weeks", evidenceItems: 3 });
  });

  test("an option the field does not have, no option, or a draft in its place is a visible failed row, never a fill", () => {
    for (const extra of [{ choice: "3 weeks" }, { choice: "2 WEEKS" }, { choice: "" }, { choice: undefined }, { choice: 7 }, { answer_kind: "essay", draft: "I can start in two weeks." }, { answer_kind: undefined, draft: "x" }]) {
      const r = parseSuggestReply({ fields: [row(extra)] }, asked, options);
      if (!r.ok) throw new Error("not a suggestion");
      expect(r.fields[0]).toMatchObject({ id: "c0", status: "failed", code: "bad_choice", draft: "" });
      expect(r.fields[0]!.choice).toBeUndefined();
      expect(r.fields[0]!.reason).toMatch(/not used/);
    }
  });

  test("a choice answer for a question that was sent as text is refused too", () => {
    const r = parseSuggestReply({ fields: [row({ id: "f0", choice: "Yes" })] }, asked, options);
    expect(r.ok && r.fields[0]).toMatchObject({ id: "f0", status: "failed", code: "bad_choice" });
  });

  test("a drafted fact and a drafted essay keep their text and say what they are", () => {
    const r = parseSuggestReply({ fields: [{ id: "f0", status: "drafted", answer_kind: "fact", draft: " 7 years ", model: "claude", evidence_items: 1 }] }, asked, options);
    expect(r.ok && r.fields[0]).toMatchObject({ status: "drafted", answerKind: "fact", draft: "7 years" });
    const e = parseSuggestReply({ fields: [{ id: "f0", status: "drafted", answer_kind: "essay", draft: "Because." }] }, asked, options);
    expect(e.ok && e.fields[0]).toMatchObject({ answerKind: "essay" });
    const unknownKind = parseSuggestReply({ fields: [{ id: "f0", status: "drafted", answer_kind: "weird", draft: "x" }] }, asked, options);
    expect(unknownKind.ok && unknownKind.fields[0]!.answerKind).toBeUndefined();
  });

  test("a manual or unsure choice row stays a row with the host's reason", () => {
    const r = parseSuggestReply({ fields: [{ id: "c0", status: "manual", category: "work_authorization", reason: "Answer it yourself.", choice: "2 weeks" }] }, asked, options);
    expect(r.ok && r.fields[0]).toMatchObject({ status: "manual", draft: "", reason: "Answer it yourself." });
    expect(r.ok && r.fields[0]!.choice).toBeUndefined();
  });
});

describe("every code vani suggest and vani draft add says something honest", () => {
  const asked = new Set(["f0"]);

  test("a job-level failure uses our sentence for draft's codes and suggest's, and the host's text for an unknown one", () => {
    for (const code of ["kb", "truncated", "refused", "not_drafted", "empty_reply", "account", "attempts_exhausted", "too_long", "no_evidence"]) {
      const r = parseSuggestReply({ error: "internal detail 0x7f", code }, asked);
      expect(r.ok).toBe(false);
      if (r.ok) continue;
      expect([code, r.error]).not.toEqual([code, "internal detail 0x7f"]);
      expect(r.error).toBe(code in DRAFT_HELP ? DRAFT_HELP[code as keyof typeof DRAFT_HELP] : codeHelp(code)!);
      expect(r.error.length).toBeGreaterThan(20);
    }
    expect(parseSuggestReply({ error: "something new", code: "brand_new" }, asked)).toEqual({ ok: false, code: "brand_new", error: "something new" });
    // A malformed request and a provider error keep the host's text: it names the field or the provider.
    expect(parseSuggestReply({ error: "field f3: too many options", code: "bad_request" }, asked)).toMatchObject({ error: "field f3: too many options" });
    expect(parseSuggestReply({ error: "529 overloaded", code: "provider" }, asked)).toMatchObject({ error: "529 overloaded" });
  });

  test("a row without a reason gets the sentence for its code; a row with one keeps it", () => {
    const rows = ["attempts_exhausted", "too_long", "kb", "truncated", "refused", "not_drafted", "no_evidence", "answer_cap", "too_large"].map((code, i) => ({ id: "f0", status: i < 2 ? "skipped" : "failed", code }));
    for (const f of rows) {
      const r = parseSuggestReply({ fields: [f] }, asked);
      if (!r.ok) throw new Error("not a suggestion");
      expect(r.fields[0]!.reason).toBe(codeHelp(f.code));
      expect(r.fields[0]!.reason.length).toBeGreaterThan(20);
    }
    const kept = parseSuggestReply({ fields: [{ id: "f0", status: "failed", code: "too_long", reason: "The draft did not fit this field's 300 characters; use Draft on this one." }] }, asked);
    expect(kept.ok && kept.fields[0]!.reason).toContain("300 characters");
  });

  test("a failed row with no reason and no known code still says it failed; a skipped one with none stays empty", () => {
    const f = parseSuggestReply({ fields: [{ id: "f0", status: "failed" }] }, asked);
    expect(f.ok && f.fields[0]!.reason).toMatch(/could not draft/);
    const s = parseSuggestReply({ fields: [{ id: "f0", status: "skipped" }] }, asked);
    expect(s.ok && s.fields[0]!.reason).toBe("");
  });
});

describe("an older vani", () => {
  const field = (p: Partial<SuggestField> & Pick<SuggestField, "id" | "label" | "kind">): SuggestField => p as SuggestField;
  const fields = [
    field({ id: "f0", label: "Why us?", kind: "textarea", maxChars: 900 }),
    field({ id: "f1", label: "GitHub", kind: "text" }),
    field({ id: "c0", label: "Notice period", kind: "choice", options: ["Immediately", "2 weeks"] }),
  ];
  const noClock = { sleep: async () => undefined };
  /** A fake host: answers each request by `reply`, and keeps every request it was sent. */
  function host(reply: (n: number, request: object) => unknown) {
    const sent: object[] = [];
    const driverFor = (request: object): JobDriver => ({
      ...noClock,
      start: async () => { sent.push(request); return { job: `j${sent.length}` }; },
      poll: async () => ({ done: true, reply: reply(sent.length, request) }),
    });
    return { sent, driverFor };
  }
  const refusal = (what: string) => ({ error: `suggest: json: unknown field "${what}"`, code: "bad_request" });
  const okReply = { fields: [{ id: "f0", status: "drafted", draft: "Because I like inference.", category: "essay", model: "claude", evidence_items: 1 }, { id: "f1", status: "manual", reason: "A link: write it yourself." }] };

  test("recognises the refusal, and only for a bad_request that names the newer shape", () => {
    // The committed vani's own words: its strict decoder (suggestcmd.go) and its field check (fieldtriage.go).
    for (const what of ["max_chars", "options"]) {
      expect(isOldShapeRefusal({ ok: false, code: "bad_request", error: `suggest: request is not a suggest request: json: unknown field "${what}"` })).toBe(true);
    }
    expect(isOldShapeRefusal({ ok: false, code: "bad_request", error: `fieldtriage: field c1 kind "choice" is not text or textarea` })).toBe(true);
    // A current vani's complaint about one field, or another unknown field, is not an older vani.
    expect(isOldShapeRefusal({ ok: false, code: "bad_request", error: "fieldtriage: choice field c3 has 1 options, want 2..50" })).toBe(false);
    expect(isOldShapeRefusal({ ok: false, code: "bad_request", error: `json: unknown field "x"` })).toBe(false);
    expect(isOldShapeRefusal({ ok: false, code: "bad_request", error: "label is empty" })).toBe(false);
    expect(isOldShapeRefusal({ ok: false, code: "provider", error: "unknown field" })).toBe(false);
    expect(isOldShapeRefusal({ ok: true, fields: [] })).toBe(false);
  });

  test("the old shape is the text and textarea fields, none with a limit", () => {
    const req = buildSuggestRequest(fields, job);
    const old = legacyRequest(req)!;
    expect(old.fields).toEqual([{ id: "f0", label: "Why us?", kind: "textarea" }, { id: "f1", label: "GitHub", kind: "text" }]);
    expect(old.job).toEqual(req.job);
    expect(legacyRequest(buildSuggestRequest([fields[2]!], job))).toBeNull();
  });

  test("a refusal naming max_chars is resent once with the old shape, and the result says the vani is older", async () => {
    const h = host(n => (n === 1 ? refusal("max_chars") : okReply));
    const run = await runSuggest(buildSuggestRequest(fields, job), h.driverFor, 60_000);
    expect(h.sent.length).toBe(2);
    const first = h.sent[0] as { fields: Array<Record<string, unknown>> };
    const second = h.sent[1] as { fields: Array<Record<string, unknown>> };
    expect(first.fields.map(f => f.kind)).toEqual(["textarea", "text", "choice"]);
    expect(second.fields).toEqual([{ id: "f0", label: "Why us?", kind: "textarea" }, { id: "f1", label: "GitHub", kind: "text" }]);
    expect(JSON.stringify(second)).not.toMatch(/max_chars|options|choice/);
    expect(run.older).toBe(true);
    expect(run.result.ok && run.result.fields.map(f => [f.id, f.status])).toEqual([["f0", "drafted"], ["f1", "manual"]]);
    expect(run.sent.fields.map(f => f.id)).toEqual(["f0", "f1"]); // c0 was not sent: the page counts it as the person's
    expect(OLDER_VANI).toMatch(/older than this extension/);
    expect(OLDER_VANI).toMatch(/[Rr]einstall/);
  });

  test("the refusal can arrive as the job's start error too, and for options or choice", async () => {
    for (const what of [refusal("options").error, `fieldtriage: field c1 kind "choice" is not text or textarea`]) {
      let n = 0;
      const sent: object[] = [];
      const driverFor = (request: object): JobDriver => ({
        ...noClock,
        start: async () => { sent.push(request); return ++n === 1 ? { error: what, code: "bad_request" } : { job: "j" }; },
        poll: async () => ({ done: true, reply: okReply }),
      });
      const run = await runSuggest(buildSuggestRequest(fields, job), driverFor, 60_000);
      expect([what, sent.length, run.older, run.result.ok]).toEqual([what, 2, true, true]);
    }
  });

  test("it is resent once, never twice: a second refusal is the result", async () => {
    const h = host(() => refusal("max_chars"));
    const run = await runSuggest(buildSuggestRequest(fields, job), h.driverFor, 60_000);
    expect(h.sent.length).toBe(2);
    expect(run.older).toBe(true);
    expect(run.result).toMatchObject({ ok: false, code: "bad_request" });
  });

  test("a page of only choice questions has nothing an older vani can be asked", async () => {
    const h = host(() => refusal("options"));
    const run = await runSuggest(buildSuggestRequest([fields[2]!], job), h.driverFor, 60_000);
    expect(h.sent.length).toBe(1);
    expect(run).toMatchObject({ older: true, result: { ok: false, code: "older", error: OLDER_VANI } });
  });

  test("a refusal that is not about the shape, or a request that used no new shape, is not resent", async () => {
    const other = host(() => ({ error: "label is empty", code: "bad_request" }));
    expect((await runSuggest(buildSuggestRequest(fields, job), other.driverFor, 60_000)).older).toBe(false);
    expect(other.sent.length).toBe(1);
    // Nothing the older vani would not know: the same request again could not do better.
    const plain = host(() => refusal("max_chars"));
    const run = await runSuggest(buildSuggestRequest([field({ id: "f1", label: "GitHub", kind: "text" })], job), plain.driverFor, 60_000);
    expect(plain.sent.length).toBe(1);
    expect(run).toMatchObject({ older: false, result: { ok: false, code: "bad_request" } });
  });

  test("a current vani is asked once and is not called older", async () => {
    const h = host(() => okReply);
    const run = await runSuggest(buildSuggestRequest(fields, job), h.driverFor, 60_000);
    expect([h.sent.length, run.older, run.result.ok]).toEqual([1, false, true]);
  });

  test("the resend shares the first attempt's bound: a refusal that took the whole of it is not followed by a second wait", async () => {
    const sent: object[] = [];
    const driverFor = (request: object): JobDriver => ({
      ...noClock,
      start: async () => { sent.push(request); await new Promise(r => setTimeout(r, 15)); return refusal("max_chars"); },
      poll: async () => ({ done: true, reply: okReply }),
    });
    const run = await runSuggest(buildSuggestRequest(fields, job), driverFor, 10);
    expect(sent.length).toBe(1);
    expect(run).toMatchObject({ older: true, result: { ok: false, code: "timeout" } });
  });
});

describe("parseJobStep", () => {
  test("reads each step strictly", () => {
    expect(parseJobStep({ job: "abc" })).toEqual({ job: "abc" });
    expect(parseJobStep({ pending: true })).toEqual({ pending: true });
    expect(parseJobStep({ done: true, reply: { fields: [] } })).toEqual({ done: true, reply: { fields: [] } });
    expect(parseJobStep({ error: "x", code: "agent" })).toMatchObject({ ok: false, code: "agent" });
    for (const junk of [{ job: "" }, { job: "x".repeat(65) }, { pending: "yes" }, { done: true }, null]) {
      expect(parseJobStep(junk)).toMatchObject({ ok: false, code: "bad_reply" });
    }
  });
});

describe("runJob", () => {
  function clock() {
    let t = 0;
    return { now: () => t, sleep: async (ms: number) => { t += ms; } };
  }

  test("starts, polls while pending and returns the reply", async () => {
    const c = clock();
    let polls = 0;
    const r = await runJob({ ...c, start: async () => ({ job: "j" }), poll: async () => (++polls < 3 ? { pending: true } : { done: true, reply: { fields: [] } }) }, 60_000);
    expect(r).toEqual({ ok: true, reply: { fields: [] } });
    expect(polls).toBe(3);
  });

  test("a refused start or a failed poll is a coded failure", async () => {
    expect(await runJob({ ...clock(), start: async () => ({ error: "busy", code: "busy" }), poll: async () => ({}) }, 1000)).toMatchObject({ ok: false, code: "busy" });
    expect(await runJob({ ...clock(), start: async () => { throw new Error("no host"); }, poll: async () => ({}) }, 1000)).toMatchObject({ ok: false, code: "unreachable" });
    expect(await runJob({ ...clock(), start: async () => ({ job: "j" }), poll: async () => ({ error: "gone", code: "agent" }) }, 10_000)).toMatchObject({ ok: false, code: "agent" });
  });

  test("times out instead of polling forever", async () => {
    let polls = 0;
    const r = await runJob({ ...clock(), start: async () => ({ job: "j" }), poll: async () => { polls++; return { pending: true }; } }, 10_000, 2000);
    expect(r).toMatchObject({ ok: false, code: "timeout" });
    expect(polls).toBe(5);
  });

  describe("a coded failure that comes back from a poll (background.native returns it, never throws it)", () => {
    test("unreachable is transient: the wait goes on with a growing gap and the job's reply still arrives", async () => {
      const c = clock();
      const gaps: number[] = [];
      let polls = 0;
      const r = await runJob({
        now: c.now, sleep: async ms => { gaps.push(ms); await c.sleep(ms); }, start: async () => ({ job: "j" }),
        poll: async () => (++polls <= 3 ? { error: "the Vani Apply Mac app isn't reachable", code: "unreachable" } : { done: true, reply: { fields: [] } }),
      }, 120_000, 2000);
      expect(r).toEqual({ ok: true, reply: { fields: [] } });
      expect(polls).toBe(4);
      expect(gaps).toEqual([2000, 4000, 8000, 16000]); // doubles per miss, the first gap is the normal one
    });

    test("the shape { ok:false, code } with no text is read as a failure too", async () => {
      let polls = 0;
      const r = await runJob({ ...clock(), start: async () => ({ job: "j" }), poll: async () => (++polls === 1 ? { ok: false, code: "unreachable" } : { done: true, reply: 7 }) }, 60_000);
      expect(r).toEqual({ ok: true, reply: 7 });
    });

    test("the backoff is capped, and a success puts the normal gap back", async () => {
      const c = clock();
      const gaps: number[] = [];
      let polls = 0;
      await runJob({
        now: c.now, sleep: async ms => { gaps.push(ms); await c.sleep(ms); }, start: async () => ({ job: "j" }),
        poll: async () => { polls++; return polls <= 8 ? { error: "x", code: "unreachable" } : polls === 9 ? { pending: true } : { done: true, reply: 1 }; },
      }, 600_000, 2000);
      expect(Math.max(...gaps)).toBe(MAX_BACKOFF_MS);
      expect(gaps.slice(-2)).toEqual([MAX_BACKOFF_MS, 2000]);
    });

    test("it stays inside the overall bound, and ends with the failure it kept hitting, not a bare timeout", async () => {
      const c = clock();
      let polls = 0;
      const r = await runJob({ ...c, start: async () => ({ job: "j" }), poll: async () => { polls++; return { error: "gone", code: "unreachable" }; } }, 60_000, 2000);
      expect(r).toMatchObject({ ok: false, code: "unreachable" });
      expect(c.now()).toBeGreaterThanOrEqual(60_000);
      expect(c.now()).toBeLessThan(60_000 + MAX_BACKOFF_MS + 1);
      expect(polls).toBeLessThan(10);
    });

    test("only a terminal reply ends the wait early: a refusal, no_such_job and the agent not running are not retried", async () => {
      for (const code of ["budget_exhausted", "config", "bad_request", "agent"]) {
        let polls = 0;
        const r = await runJob({ ...clock(), start: async () => ({ job: "j" }), poll: async () => { polls++; return { error: "no", code }; } }, 60_000);
        expect(r).toMatchObject({ ok: false, code });
        expect(polls).toBe(1);
      }
    });
  });

  describe("no such job", () => {
    const gone = { error: "no such job (it finished more than 600 s ago, or the agent restarted)", code: "agent" };

    test("is its own message, not 'the agent isn't running'", async () => {
      let polls = 0;
      const r = await runJob({ ...clock(), start: async () => ({ job: "j" }), poll: async () => { polls++; return gone; } }, 60_000);
      expect(r).toEqual({ ok: false, code: "no_such_job", error: JOB_HELP.no_such_job! });
      expect(JOB_HELP.no_such_job).not.toBe(JOB_HELP.agent);
      expect(JOB_HELP.no_such_job).not.toMatch(/isn't running/);
      expect(polls).toBe(1); // asking again cannot bring it back
    });

    test("an agent that is really not answering keeps its own message", () => {
      expect(parseJobStep({ error: "the agent did not answer", code: "agent" })).toEqual({ ok: false, code: "agent", error: JOB_HELP.agent! });
    });
  });

  test("rides out a missed poll, gives up after three in a row", async () => {
    let n = 0;
    const flaky = await runJob({ ...clock(), start: async () => ({ job: "j" }), poll: async () => { if (++n === 1) throw new Error("unloaded"); return { done: true, reply: 1 }; } }, 60_000);
    expect(flaky).toEqual({ ok: true, reply: 1 });
    const dead = await runJob({ ...clock(), start: async () => ({ job: "j" }), poll: async () => { throw new Error("unloaded"); } }, 60_000);
    expect(dead).toMatchObject({ ok: false, code: "unreachable" });
  });
});

describe("a request of long option lists stays inside what the agent takes", () => {
  test("choice questions stop being added once their options pass MAX_REQUEST_OPTION_CHARS", () => {
    const opt = (q: number) => Array.from({ length: 50 }, (_, i) => `q${q} option ${i} ` + "x".repeat(150));
    const many = Array.from({ length: 10 }, (_, q) => ({ id: `c${q}`, label: `Pick ${q}`, kind: "choice" as const, options: opt(q) }));
    const req = buildSuggestRequest(many, { company: "Acme", title: "Engineer", description: "Build things for people who build things." });
    const sent = req.fields.reduce((n, f) => n + ((f as { options?: string[] }).options ?? []).reduce((m, o) => m + o.length, 0), 0);
    expect(sent).toBeLessThanOrEqual(MAX_REQUEST_OPTION_CHARS);
    expect(req.fields.length).toBeGreaterThan(0);
    expect(req.fields.length).toBeLessThan(10);
  });
});
