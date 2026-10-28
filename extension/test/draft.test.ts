import { describe, expect, test } from "bun:test";
import {
  DEFAULT_WORDS, DRAFT_FIELD_ABSENT, DRAFT_HELP, DRAFT_FIELD_BUSY, DRAFT_FIELD_NONE, MAX_DESCRIPTION_CHARS, MAX_QUESTION_CHARS, MAX_WORDS, MIN_WORDS,
  buildDraftRequest, draftHelp, draftStartError, parseDraftReply, requestDraft,
} from "../src/draft";

const job = { company: "Acme", title: "ML Engineer", description: "We build inference systems on GPUs. ".repeat(5) };

type Req = { question: string; job: { company: string; title: string; description: string }; max_words: number };
const req = (m: { request: object }) => m.request as Req;

describe("buildDraftRequest", () => {
  test("sends only the question and the posting, clipped and clamped", () => {
    const m = buildDraftRequest("  Why do you want\n to work here? *", job);
    expect(m.op).toBe("draft");
    expect(Object.keys(m.request).sort()).toEqual(["job", "max_words", "question"]);
    expect(Object.keys(req(m).job).sort()).toEqual(["company", "description", "title"]);
    expect(req(m).question).toBe("Why do you want to work here? *");
    expect(req(m).max_words).toBe(DEFAULT_WORDS);
    expect(req(buildDraftRequest("Why us?", job, 5)).max_words).toBe(MIN_WORDS);
    expect(req(buildDraftRequest("Why us?", job, 10_000)).max_words).toBe(MAX_WORDS);
    expect(req(buildDraftRequest("Why us?", job, Number.NaN)).max_words).toBe(DEFAULT_WORDS);
    const big = buildDraftRequest("q".repeat(5000), { ...job, description: "d".repeat(100_000), company: "c".repeat(999) });
    expect(req(big).question.length).toBe(MAX_QUESTION_CHARS);
    expect(req(big).job.description.length).toBe(MAX_DESCRIPTION_CHARS);
    // A long real posting reaches `vani draft` whole: its company and role sections sit past the first
    // screens (vani draft splits by section and caps at 60,000 runes, draft.MaxPostingRunes).
    expect(MAX_DESCRIPTION_CHARS).toBe(60_000);
    expect(req(buildDraftRequest("Why us?", { ...job, description: "d".repeat(50_000) })).job.description.length).toBe(50_000);
    expect(req(big).job.company.length).toBe(200);
  });

  test("a first draft omits attempt, and a later one sends only the counter", () => {
    expect(Object.keys(buildDraftRequest("Why us?", job, undefined, 0).request).sort()).toEqual(["job", "max_words", "question"]);
    const again = buildDraftRequest("Why us?", job, undefined, 2).request as { attempt?: number; question: string };
    expect(again.attempt).toBe(2);
    expect(again.question).toBe("Why us?");
    expect(Object.keys(again).sort()).toEqual(["attempt", "job", "max_words", "question"]);
    for (const bad of [-1, 9, 1.5]) expect(() => buildDraftRequest("Why us?", job, undefined, bad)).toThrow(/attempt is out of range/);
  });

  test("refuses a field with no question or a page with no description", () => {
    expect(() => buildDraftRequest("  ", job)).toThrow(/no readable question/);
    expect(() => buildDraftRequest("Why us?", { ...job, description: "short" })).toThrow(/no job description/);
  });
});

describe("parseDraftReply", () => {
  test("a draft", () => {
    expect(parseDraftReply({ draft: "  I build inference systems.  ", model: "claude-opus-5-5", evidence_items: 3 }))
      .toEqual({ ok: true, draft: "I build inference systems.", model: "claude-opus-5-5", evidenceItems: 3 });
  });

  test("every host error code is kept; an unknown code reads as provider", () => {
    for (const code of ["bad_request", "budget_exhausted", "timeout", "provider", "empty_reply", "ungrounded", "config", "kb", "account", "unsupported"]) {
      const r = parseDraftReply({ error: "x", code });
      expect(r.ok).toBe(false);
      if (!r.ok) expect(r.code).toBe(code as typeof r.code);
    }
    const r = parseDraftReply({ error: "x", code: "weird" });
    expect(!r.ok && r.code).toBe("provider");
  });

  test("an ungrounded draft is refused with a message the user can act on", () => {
    expect(DRAFT_HELP.ungrounded).toMatch(/figure/);
    expect(DRAFT_HELP.ungrounded).toMatch(/Try again, or write this answer yourself/);
    const r = parseDraftReply({ error: 'draft: the draft says "73%"', code: "ungrounded" });
    expect(r).toEqual({ ok: false, code: "ungrounded", error: 'draft: the draft says "73%"' });
  });

  test("an ungrounded refusal names what was unbacked, from vani's kind", () => {
    const named = {
      figure: /a figure.*knowledge base or the posting/,
      years: /years of experience.*your own records/,
      credential: /a credential.*your own records/,
      acronym: /an acronym.*knowledge base or the posting/,
    } as const;
    for (const [kind, re] of Object.entries(named)) {
      const r = parseDraftReply({ error: "draft: x", code: "ungrounded", kind });
      expect(r).toEqual({ ok: false, code: "ungrounded", error: "draft: x", kind: kind as keyof typeof named });
      if (r.ok) throw new Error("not a failure");
      const help = draftHelp(r);
      expect(help).toMatch(re);
      expect(help).toMatch(/Try again, or write this answer yourself/);
      expect(help).not.toMatch(/cited a figure/);
    }
  });

  test("an ungrounded refusal with no kind, an unknown kind or a kind on another code is worded generally", () => {
    for (const kind of [undefined, "", "mystery", 7, null, {}, "__proto__", "toString"]) {
      const r = parseDraftReply({ error: "e", code: "ungrounded", kind });
      expect(r).toEqual({ ok: false, code: "ungrounded", error: "e" });
      if (r.ok) throw new Error("not a failure");
      expect("kind" in r).toBe(false);
      expect(draftHelp(r)).toBe(DRAFT_HELP.ungrounded);
    }
    const other = parseDraftReply({ error: "e", code: "timeout", kind: "figure" });
    expect(other).toEqual({ ok: false, code: "timeout", error: "e" });
    if (!other.ok) expect(draftHelp(other)).toBe(DRAFT_HELP.timeout);
    expect(DRAFT_HELP.ungrounded).not.toMatch(/cited a figure/);
    expect(DRAFT_HELP.ungrounded).toMatch(/credential/);
  });

  test("anything that is not a draft is a failure, never an empty or partial draft", () => {
    for (const bad of [null, undefined, "draft", [], {}, { draft: "" }, { draft: "   " }, { draft: 3 }, { draft: "x".repeat(8001) }]) {
      const r = parseDraftReply(bad);
      expect(r.ok).toBe(false);
      if (!r.ok) expect(r.code).toBe("bad_reply");
    }
  });

  test("bad evidence counts read as zero", () => {
    for (const ev of [-1, 1.5, "3", null]) {
      const r = parseDraftReply({ draft: "ok", evidence_items: ev });
      expect(r.ok && r.evidenceItems).toBe(0);
    }
  });
});

describe("requestDraft", () => {
  test("round trip through a transport", async () => {
    let sent: unknown;
    const r = await requestDraft("Why us?", job, { transport: async m => { sent = m; return { draft: "Because.", model: "m", evidence_items: 1 }; } });
    expect(r).toEqual({ ok: true, draft: "Because.", model: "m", evidenceItems: 1 });
    expect((sent as { op: string }).op).toBe("draft");
  });

  test("an unreachable host, a hang and a bad request each come back coded, never thrown", async () => {
    const down = await requestDraft("Why us?", job, { transport: async () => { throw new Error("Specified native messaging host not found."); } });
    expect(!down.ok && down.code).toBe("unreachable");
    const hung = await requestDraft("Why us?", job, { transport: () => new Promise(() => undefined), timeoutMs: 20 });
    expect(!hung.ok && hung.code).toBe("timeout");
    let called = false;
    const bad = await requestDraft("", job, { transport: async () => { called = true; return {}; } });
    expect(!bad.ok && bad.code).toBe("bad_request");
    expect(called).toBe(false);
  });

  test("the default timeout is long enough for the host's 150 s budget", async () => {
    const { DRAFT_TIMEOUT_MS } = await import("../src/draft");
    expect(DRAFT_TIMEOUT_MS).toBeGreaterThanOrEqual(160_000);
  });
});

describe("draftStartError", () => {
  test("one frame starting is enough, and a real refusal beats 'not on this page'", () => {
    expect(draftStartError([{ ok: true }, { ok: false, error: DRAFT_FIELD_ABSENT }])).toBeNull();
    expect(draftStartError([{ ok: false, error: DRAFT_FIELD_ABSENT }, { ok: false, error: DRAFT_FIELD_BUSY }])).toBe(DRAFT_FIELD_BUSY);
    expect(draftStartError([{ ok: false, error: DRAFT_FIELD_ABSENT }, { ok: false, error: DRAFT_FIELD_ABSENT }])).toBe(DRAFT_FIELD_ABSENT);
    expect(draftStartError([])).toBe(DRAFT_FIELD_NONE);
    expect(draftStartError([{ ok: false }])).toBe(DRAFT_FIELD_ABSENT);
  });
});
