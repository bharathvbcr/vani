import { describe, expect, test } from "bun:test";
import {
  JOB_HELP, MAX_DRAFT_CHARS, MAX_FIELDS, buildSuggestRequest, parseJobStep, parseSuggestReply, runJob,
} from "../src/suggestjob";

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

  test("drops a drafted field with an empty or oversized draft", () => {
    const r = parseSuggestReply({ fields: [{ id: "f0", status: "drafted", draft: " " }, { id: "f1", status: "drafted", draft: "x".repeat(MAX_DRAFT_CHARS + 1) }] }, asked);
    expect(r.ok && r.fields.length).toBe(0);
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

  test("rides out a missed poll, gives up after three in a row", async () => {
    let n = 0;
    const flaky = await runJob({ ...clock(), start: async () => ({ job: "j" }), poll: async () => { if (++n === 1) throw new Error("unloaded"); return { done: true, reply: 1 }; } }, 60_000);
    expect(flaky).toEqual({ ok: true, reply: 1 });
    const dead = await runJob({ ...clock(), start: async () => ({ job: "j" }), poll: async () => { throw new Error("unloaded"); } }, 60_000);
    expect(dead).toMatchObject({ ok: false, code: "unreachable" });
  });
});
