import { describe, expect, test } from "bun:test";
import type { FieldInfo } from "../src/types";
import { emptyBank } from "../src/types";
import { MAX_WHY_CHARS, coverageOf, noteLines, questionRows, tabCoverage } from "../src/coverage";

function ref(label: string, p: Partial<FieldInfo> = {}, manual = "") {
  return { manual, info: { kind: "text", label, name: "", autocomplete: "", automationId: "", options: [], required: false, value: [], ...p } as FieldInfo };
}

const POSTING = 80;

describe("why a question with a saved answer is still listed as new", () => {
  const bank = emptyBank();
  const refs = [ref("Start date", { kind: "date" }), ref("Why us?", { kind: "textarea" }), ref("Notice period")];
  // A stand-in for match.whyNoMatch: a reason for one field, null (never seen) for the others.
  const why = (f: FieldInfo) => (f.label === "Start date" ? "Your saved start date, 1 Sep 2026, has passed." : null);

  test("a row carries the reason when there is one, and no key at all when there is not", () => {
    const c = coverageOf(refs, bank, "x.example", undefined, undefined, POSTING, why);
    const [start, essay, notice] = c.questions;
    expect(start).toMatchObject({ label: "Start date", why: "Your saved start date, 1 Sep 2026, has passed." });
    expect(Object.keys(essay!)).not.toContain("why");
    expect(Object.keys(notice!)).not.toContain("why");
    expect(c.unknown).toBe(3);
  });

  test("no bank, no reason source, a thrown reason, a blank one and a non-string are all 'nothing to say'", () => {
    expect(coverageOf(refs, null, "x.example", undefined, undefined, POSTING, () => "never asked").questions.every(q => q.why === undefined)).toBe(true);
    expect(coverageOf(refs, bank, "x.example", undefined, undefined, POSTING, undefined).questions.every(q => q.why === undefined)).toBe(true);
    for (const bad of [() => { throw new Error("boom"); }, () => "   ", () => 7 as unknown as string, () => null]) {
      const c = coverageOf(refs, bank, "x.example", undefined, undefined, POSTING, bad);
      expect(c.questions.length).toBe(3);
      expect(c.questions.every(q => q.why === undefined)).toBe(true);
    }
  });

  test("a reason is bounded, so a long one cannot flood the panel", () => {
    const c = coverageOf([refs[0]!], bank, "x.example", undefined, undefined, POSTING, () => "w".repeat(5000));
    expect(c.questions[0]!.why!.length).toBe(MAX_WHY_CHARS);
  });

  test("only unknown rows ask: a manual field or a known one is not given a reason", () => {
    let asked = 0;
    coverageOf([ref("Start date", { kind: "date" }, "date or dialog picker"), ref("Password", { kind: "password" })], bank, "x.example", undefined, undefined, POSTING, () => { asked++; return "x"; });
    expect(asked).toBe(0);
  });

  test("the reason survives a frame's report being added to the tab's", () => {
    const c = coverageOf(refs, bank, "x.example", undefined, undefined, POSTING, why);
    const t = tabCoverage([{ frameId: 0, result: { host: "x.example", disabled: false, fields: 3, coverage: c } }]);
    expect(questionRows(t.total).find(q => q.label === "Start date")).toMatchObject({ why: "Your saved start date, 1 Sep 2026, has passed.", frameId: 0 });
  });
});

describe("with match.whyNoMatch itself", () => {
  test("a saved start date that has passed is listed as such, not as a plain new question; a never-seen question has no reason", () => {
    const bank = emptyBank();
    bank.answers["earliest start date"] = {
      key: "earliest start date", labels: ["earliest start date", "when can you start"], kind: "text", value: ["2020-01-15"], sensitivity: "standard",
      signatures: [], uses: 1, fills: 0, updatedAt: "2020-01-01T00:00:00.000Z", review: false,
    };
    const c = coverageOf([ref("Earliest start date", { kind: "date" }), ref("Favourite colour")], bank, "x.example", undefined, undefined, POSTING);
    const stale = c.questions.find(q => q.label === "Earliest start date");
    expect(stale?.why).toMatch(/Your saved start date \(2020-01-15\) has passed/);
    expect(noteLines(stale!)).toEqual([stale!.why!]); // a date field has no Draft button: the reason replaces "Fill this one on the page."
    expect(c.questions.find(q => q.label === "Favourite colour")!.why).toBeUndefined();
  });
});

describe("noteLines: what a row shows under its label", () => {
  test("a reason leads and replaces a generic note on a row with no button", () => {
    expect(noteLines({ offer: "other", note: "Fill this one on the page.", why: "The saved date has passed." })).toEqual(["The saved date has passed."]);
    expect(noteLines({ offer: "choice", note: "Choose an option on the page.", why: "Why" })).toEqual(["Why"]);
  });

  test("a note that explains a button the row has stays, after the reason", () => {
    expect(noteLines({ offer: "draft", note: "Empty. A draft sends the question and the posting only.", why: "Why" })).toEqual(["Why", "Empty. A draft sends the question and the posting only."]);
    expect(noteLines({ offer: "needs-posting", note: "No job description on this page to draft from.", why: "Why" })).toHaveLength(2);
  });

  test("with no reason, the note is what it always was", () => {
    expect(noteLines({ offer: "draft", note: "Empty." })).toEqual(["Empty."]);
    expect(noteLines({ offer: "other", note: "" })).toEqual([]);
    for (const why of ["", "   ", undefined]) expect(noteLines({ offer: "other", note: "N", why })).toEqual(["N"]);
  });

  test("a reason from a frame that is not a string, or is oversized, cannot break or flood the row", () => {
    expect(noteLines({ offer: "other", note: "N", why: 5 as unknown as string })).toEqual(["N"]);
    expect(noteLines({ offer: "other", note: "N", why: "w".repeat(9000) })[0]!.length).toBe(MAX_WHY_CHARS);
  });
});
