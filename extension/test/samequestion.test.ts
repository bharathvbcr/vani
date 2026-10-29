import { describe, expect, test } from "bun:test";
import type { Answer, FieldInfo, Match } from "../src/types";
import { MAX_SAME_ASKS, SAME_CONCURRENCY, alternativeOf, matchAll, parseSameQuestion, settleAmbiguous } from "../src/samequestion";
import { emptyBank } from "../src/types";

function answer(key: string, value: string[], review = false): Answer {
  return { key, labels: [key], kind: "text", value, sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: "2026-10-01T00:00:00.000Z", review };
}

interface R { info: FieldInfo; manual: string; id: string }
function field(id: string, label: string, p: Partial<FieldInfo> = {}, manual = ""): R {
  return { id, manual, info: { kind: "text", label, name: id, autocomplete: "", automationId: "", options: [], required: false, value: [], ...p } };
}

/** The match for the field's exact wording (yellow, with a note), carrying the id's answer as the alternative. */
function ambiguous(own: Answer, alt: Answer, wording: string): Match {
  return {
    answer: own, via: "label", score: 0.9, value: own.value, review: true, note: "this site's id belongs to another saved answer",
    alternative: { answer: alt, value: alt.value, wording },
  } as Match;
}

const refs3 = () => [field("a", "Notice period"), field("b", "Years of experience"), field("c", "Salary expectation")];
function setup(rs: R[]) {
  const own = answer("notice period", ["30 days"]);
  const ids = answer("availability", ["Immediately"]);
  const matches = new Map<R, Match | null>(rs.map(r => [r, ambiguous(own, ids, "When can you start?")]));
  return { own, ids, matches };
}

describe("parseSameQuestion", () => {
  test("an answer is an answer; every other shape is abstained or down, never a throw", () => {
    expect(parseSameQuestion({ reading: "answered", value: "same" })).toBe("same");
    expect(parseSameQuestion({ reading: "answered", value: "different" })).toBe("different");
    expect(parseSameQuestion({ reading: "answered", value: "maybe" })).toBe("abstained");
    for (const reading of ["abstained", "refused", "failed"]) expect(parseSameQuestion({ reading, detail: "x" })).toBe("abstained");
    expect(parseSameQuestion({ reading: "unavailable", detail: "no model" })).toBe("down");
    // The transport's error, and what an old Mac app answers for an op it does not know.
    expect(parseSameQuestion({ error: "unknown op", code: "bad_request" })).toBe("down");
    expect(parseSameQuestion({ error: "the Vani Apply Mac app isn't reachable", code: "unreachable" })).toBe("down");
    expect(parseSameQuestion({ ok: false, code: "timeout" })).toBe("down");
    for (const junk of [null, undefined, 3, "same", [], {}, { reading: "weird" }, { value: "same" }]) expect(parseSameQuestion(junk)).toBe("down");
  });
});

describe("alternativeOf", () => {
  const base = answer("k", ["v"]);
  test("reads a well-formed alternative and nothing else", () => {
    expect(alternativeOf(ambiguous(base, answer("j", ["w"]), "Other"))?.wording).toBe("Other");
    expect(alternativeOf(null)).toBeNull();
    expect(alternativeOf({ answer: base, via: "label", score: 1, value: ["v"], review: false })).toBeNull();
    for (const alternative of [null, 3, { answer: base, value: [], wording: "x" }, { answer: base, value: [1], wording: "x" }, { answer: base, value: ["v"] }, { value: ["v"], wording: "x" }, { answer: {}, value: ["v"], wording: "x" }]) {
      expect(alternativeOf({ answer: base, via: "label", score: 1, value: ["v"], review: true, alternative } as unknown as Match)).toBeNull();
    }
  });
});

describe("settleAmbiguous", () => {
  test("same fills the id's answer, via signature, confident; the ask carries the two labels and no value", async () => {
    const rs = [field("a", "Notice period")];
    const { ids, matches } = setup(rs);
    const asked: Array<[string, string]> = [];
    const report = await settleAmbiguous(rs, matches, async (a, b) => { asked.push([a, b]); return { reading: "answered", value: "same" }; });
    expect(asked).toEqual([["Notice period", "When can you start?"]]);
    const m = matches.get(rs[0]!)!;
    expect(m).toMatchObject({ answer: ids, via: "signature", value: ["Immediately"], review: false });
    expect(JSON.stringify(asked)).not.toContain("30 days");
    expect(JSON.stringify(asked)).not.toContain("Immediately");
    expect(report).toMatchObject({ candidates: 1, asked: 1, same: 1, different: 0, timedOut: false });
  });

  test("different fills the field's own answer, confident, with the doubt (note, alternative) gone", async () => {
    const rs = [field("a", "Notice period")];
    const { own, matches } = setup(rs);
    await settleAmbiguous(rs, matches, async () => ({ reading: "answered", value: "different" }));
    const m = matches.get(rs[0]!)! as Match & { alternative?: unknown };
    expect(m).toMatchObject({ answer: own, value: ["30 days"], review: false });
    expect(m.note).toBeUndefined();
    expect(m.alternative).toBeUndefined();
  });

  test("an answer that is itself kept for review stays yellow after either verdict", async () => {
    for (const value of ["same", "different"]) {
      const rs = [field("a", "Why us?")];
      const own = answer("why us", ["long draft"], true);
      const ids = answer("motivation", ["another draft"], true);
      const matches = new Map<R, Match | null>([[rs[0]!, ambiguous(own, ids, "Motivation")]]);
      await settleAmbiguous(rs, matches, async () => ({ reading: "answered", value }));
      expect(matches.get(rs[0]!)!.review).toBe(true);
    }
  });

  test("anything but an answer leaves the yellow match exactly as it was", async () => {
    for (const reply of [{ reading: "abstained", detail: "unsure" }, { reading: "refused", detail: "guardrail" }, { reading: "failed", detail: "x" }, { reading: "unavailable", detail: "none" },
      { error: "unknown op", code: "bad_request" }, null, "junk", { reading: "answered", value: "maybe" }]) {
      const rs = [field("a", "Notice period")];
      const { matches } = setup(rs);
      const before = matches.get(rs[0]!);
      await settleAmbiguous(rs, matches, async () => reply);
      expect(matches.get(rs[0]!)).toBe(before);
      expect(matches.get(rs[0]!)!.review).toBe(true);
    }
  });

  test("an ask that throws is read as unavailable: nothing changes, nothing is thrown", async () => {
    const rs = refs3();
    const { matches } = setup(rs);
    const before = rs.map(r => matches.get(r));
    let asks = 0;
    const report = await settleAmbiguous(rs, matches, async () => { asks++; throw new Error("worker gone"); });
    expect(rs.map(r => matches.get(r))).toEqual(before);
    expect(report.same + report.different).toBe(0);
    // A down host stops the phase: the queued asks would fail the same way (at most the pair in flight went out).
    expect(asks).toBeLessThanOrEqual(SAME_CONCURRENCY);
  });

  test("an old Mac app (unknown op) stops the phase after the first replies instead of asking every field", async () => {
    const rs = Array.from({ length: 8 }, (_, i) => field(`f${i}`, `Question ${i}`));
    const { matches } = setup(rs);
    let asks = 0;
    await settleAmbiguous(rs, matches, async () => { asks++; return { error: "unknown op", code: "bad_request" }; });
    expect(asks).toBeLessThanOrEqual(SAME_CONCURRENCY);
  });

  test("one pair the model abstains on does not stop the others", async () => {
    const rs = refs3();
    const { matches } = setup(rs);
    let n = 0;
    const report = await settleAmbiguous(rs, matches, async () => (++n === 1 ? { reading: "abstained" } : { reading: "answered", value: "same" }), { concurrency: 1 });
    expect(report).toMatchObject({ asked: 3, same: 2 });
  });

  test("at most MAX_SAME_ASKS asks per fill, the rest stay yellow, and the report carries both numbers", async () => {
    const rs = Array.from({ length: MAX_SAME_ASKS + 5 }, (_, i) => field(`f${i}`, `Question ${i}`));
    const { matches } = setup(rs);
    let asks = 0;
    const report = await settleAmbiguous(rs, matches, async () => { asks++; return { reading: "answered", value: "same" }; });
    expect(asks).toBe(MAX_SAME_ASKS);
    expect(report).toMatchObject({ candidates: MAX_SAME_ASKS + 5, asked: MAX_SAME_ASKS, same: MAX_SAME_ASKS });
    expect(rs.filter(r => matches.get(r)!.review).length).toBe(5);
  });

  test("never more than SAME_CONCURRENCY asks are in flight", async () => {
    const rs = Array.from({ length: 7 }, (_, i) => field(`f${i}`, `Question ${i}`));
    const { matches } = setup(rs);
    let live = 0, peak = 0;
    await settleAmbiguous(rs, matches, async () => {
      peak = Math.max(peak, ++live);
      await new Promise(r => setTimeout(r, 5));
      live--;
      return { reading: "answered", value: "different" };
    });
    expect(peak).toBe(SAME_CONCURRENCY);
  });

  test("a model that never answers ends the phase at the deadline, and its late answer is dropped", async () => {
    const rs = refs3();
    const { matches } = setup(rs);
    const before = rs.map(r => matches.get(r));
    let release: (v: unknown) => void = () => undefined;
    const t0 = Date.now();
    const report = await settleAmbiguous(rs, matches, () => new Promise(r => { release = r; }), { phaseMs: 40 });
    expect(Date.now() - t0).toBeLessThan(400);
    expect(report.timedOut).toBe(true);
    release({ reading: "answered", value: "same" });
    await new Promise(r => setTimeout(r, 10));
    expect(rs.map(r => matches.get(r))).toEqual(before);
  });

  test("a field that already has text, a manual widget and a plain match are not asked about; overwrite asks again", async () => {
    const typed = field("a", "Notice period", { value: ["two weeks"] });
    const manual = field("b", "Years of experience", {}, "date or dialog picker");
    const plain = field("c", "Salary expectation");
    const { matches } = setup([typed, manual]);
    matches.set(plain, { answer: answer("salary", ["100"]), via: "label", score: 1, value: ["100"], review: false });
    const asked: string[] = [];
    const ask = async (a: string) => { asked.push(a); return { reading: "answered", value: "same" }; };
    const refs = [typed, manual, plain];
    const r1 = await settleAmbiguous(refs, matches, ask);
    expect(asked).toEqual([]);
    expect(r1.candidates).toBe(0);
    await settleAmbiguous(refs, matches, ask, { overwrite: true });
    expect(asked).toEqual(["Notice period"]);
  });

  test("labels are folded and cut to 300 characters; an empty one is not asked", async () => {
    const long = field("a", `  ${"w ".repeat(400)}`);
    const blank = field("b", "   ");
    const rs = [long, blank];
    const { matches } = setup(rs);
    const asked: Array<[string, string]> = [];
    await settleAmbiguous(rs, matches, async (a, b) => { asked.push([a, b]); return { reading: "abstained" }; });
    expect(asked.length).toBe(1);
    expect(asked[0]![0].length).toBe(300);
    expect(asked[0]![0]).not.toMatch(/\s{2}/);
  });

  test("no candidates, no timer, no asks", async () => {
    let asks = 0;
    const report = await settleAmbiguous([], new Map(), async () => { asks++; return {}; });
    expect(asks).toBe(0);
    expect(report).toEqual({ candidates: 0, asked: 0, same: 0, different: 0, timedOut: false });
  });
});

describe("against match.ts's own ambiguity (a real bank, nothing faked)", () => {
  const HOST = "jobs.example.com";
  // Wordings that are no known intent (intents.ts), so only the signature-versus-wording doubt is in play.
  const info: FieldInfo = { kind: "text", label: "Team size you managed", name: "team_size_field", autocomplete: "", automationId: "", options: [], required: false, value: [] };
  function bankWith(own: Answer, ids: Answer) {
    const bank = emptyBank();
    bank.answers[own.key] = own;
    bank.answers[ids.key] = ids;
    return bank;
  }
  const exactWording = (review = false): Answer => ({ ...answer("team size you managed", ["12"], review), labels: ["team size you managed"], updatedAt: "2026-09-01T00:00:00.000Z" });
  const idsOwner = (): Answer => ({ ...answer("direct reports", ["40"]), labels: ["how many direct reports"], signatures: [`example.com|team_size_field`], updatedAt: "2026-10-01T00:00:00.000Z" });
  const refOf = (): R => ({ id: "a", manual: "", info: { ...info } });

  test("the walk really produces the ambiguous match this module settles", () => {
    const bank = bankWith(exactWording(), idsOwner());
    const m = matchAll([refOf()], bank, HOST).values().next().value!;
    expect(m).toMatchObject({ review: true, value: ["12"] });
    expect(alternativeOf(m)).toMatchObject({ value: ["40"], wording: "how many direct reports" });
  });

  test("same fills the id's answer, confident; different fills the wording's, confident, with no note", async () => {
    for (const [value, want, key, via] of [["same", "40", "direct reports", "signature"], ["different", "12", "team size you managed", "label"]] as const) {
      const bank = bankWith(exactWording(), idsOwner());
      const r = refOf();
      const matches = matchAll([r], bank, HOST);
      const asked: Array<[string, string]> = [];
      await settleAmbiguous([r], matches, async (a, b) => { asked.push([a, b]); return { reading: "answered", value }; }, { bank, host: HOST });
      expect(asked).toEqual([["Team size you managed", "how many direct reports"]]);
      const m = matches.get(r)!;
      expect([value, m.answer.key, m.value, m.review, m.note, alternativeOf(m), m.via]).toEqual([value, key, [want], false, undefined, null, via]);
    }
  });

  test("a reason to check the answer that is not the doubt survives 'different' (an answer kept for review stays yellow)", async () => {
    const bank = bankWith(exactWording(true), idsOwner());
    const r = refOf();
    const matches = matchAll([r], bank, HOST);
    await settleAmbiguous([r], matches, async () => ({ reading: "answered", value: "different" }), { bank, host: HOST });
    expect(matches.get(r)).toMatchObject({ review: true, value: ["12"] });
  });

  test("a model that is down leaves the walk's own yellow match", async () => {
    const bank = bankWith(exactWording(), idsOwner());
    const r = refOf();
    const matches = matchAll([r], bank, HOST);
    const before = matches.get(r);
    await settleAmbiguous([r], matches, async () => ({ error: "unknown op", code: "bad_request" }), { bank, host: HOST });
    expect(matches.get(r)).toBe(before);
    expect(before!.review).toBe(true);
    expect(before!.note).toBeDefined();
  });
});

describe("matchAll", () => {
  test("keeps carried matches, computes the rest, and never matches a file or a password", () => {
    const carried = field("a", "Notice period");
    const fresh = field("b", "Anything");
    const file = field("c", "Resume", { kind: "file" });
    const pw = field("d", "Password", { kind: "password" });
    const m: Match = { answer: answer("k", ["v"]), via: "label", score: 1, value: ["v"], review: false };
    const out = matchAll([carried, fresh, file, pw], emptyBank(), "x.example", new Map([[carried, m]]));
    expect(out.get(carried)).toBe(m);
    expect(out.get(fresh)).toBeNull();
    expect(out.has(file)).toBe(false);
    expect(out.has(pw)).toBe(false);
  });
});

describe("legal and sensitive questions are never settled by the on-device model", () => {
  test("work authorization, sponsorship, agreements and self-identification keep their yellow fill and are not asked", async () => {
    const own = answer("authorized to work in the us", ["Yes"]);
    const ids = answer("visa", ["No"]);
    const cases: Array<[R, string]> = [
      [field("a", "Can you work for us without needing sponsorship?"), "Do you require sponsorship?"],
      [field("b", "Are you legally authorized to work here?"), "Work status"],
      [field("c", "Status"), "Do you agree to the arbitration agreement?"],
      [field("d", "Please select your gender"), "Gender identity"],
    ];
    const matches = new Map<R, Match | null>(cases.map(([r, wording]) => [r, ambiguous(own, ids, wording)]));
    let asked = 0;
    const report = await settleAmbiguous(cases.map(c => c[0]), matches, async () => { asked++; return { reading: "answered", value: "same" }; });
    expect(asked).toBe(0);
    expect(report.candidates).toBe(0);
    for (const [r] of cases) expect(matches.get(r)?.review).toBe(true);
  });
});
