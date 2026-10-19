import { describe, expect, test } from "bun:test";
import {
  DEFAULT_WORDS, MAX_DESCRIPTION_CHARS, MAX_QUESTION_CHARS, MAX_WORDS, MIN_WORDS,
  buildDraftRequest, parseDraftReply, requestDraft,
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
    const big = buildDraftRequest("q".repeat(5000), { ...job, description: "d".repeat(50_000), company: "c".repeat(999) });
    expect(req(big).question.length).toBe(MAX_QUESTION_CHARS);
    expect(req(big).job.description.length).toBe(MAX_DESCRIPTION_CHARS);
    expect(req(big).job.company.length).toBe(200);
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
    for (const code of ["bad_request", "budget_exhausted", "timeout", "provider", "empty_reply", "config", "kb", "keychain", "unsupported"]) {
      const r = parseDraftReply({ error: "x", code });
      expect(r.ok).toBe(false);
      if (!r.ok) expect(r.code).toBe(code as typeof r.code);
    }
    const r = parseDraftReply({ error: "x", code: "weird" });
    expect(!r.ok && r.code).toBe("provider");
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
