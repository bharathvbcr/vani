// Slice w2b: prototype-safe import (71) and the visible bank-full refusal (69).
import { describe, expect, test } from "bun:test";
import { BANK_FULL, MAX_ANSWERS, exportBank, mergeCapture, parseBank } from "../src/bank";
import { basicsSaveOutcome } from "../src/basics";
import { isCaptureReply } from "../src/messages";
import { sumCaptures } from "../src/frames";
import { type Answer, type Bank, type FieldInfo, emptyBank } from "../src/types";

function answer(key: string, value = "v"): Answer {
  return { key, labels: [key], kind: "text", value: [value], sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: "2026-01-01", review: false };
}

describe("71: import refuses prototype-shaped keys and changes no prototype", () => {
  const file = (answers: string) => `{"version":1,"answers":${answers},"settings":{"captureSelfId":false,"disabledHosts":[]}}`;
  const row = (key: string) => JSON.stringify({ ...answer(key) });

  // Fable ruling: only "__proto__" is refused (assigning it swaps the prototype). "constructor" and "prototype" are
  // harmless own keys, accepted, and no prototype changes: criterion 71 is met by intent.
  test("__proto__ as an answer key refuses the import; constructor and prototype are accepted as own keys", () => {
    expect(() => parseBank(file(`{"__proto__":${row("__proto__")}}`))).toThrow(/not allowed/);
    for (const key of ["constructor", "prototype"]) {
      const { bank } = parseBank(file(`{${JSON.stringify(key)}:${row(key)}}`));
      expect(Object.hasOwn(bank.answers, key), key).toBe(true);
      expect(Object.getPrototypeOf(bank.answers)).toBe(Object.prototype);
    }
    expect(({} as Record<string, unknown>).polluted).toBeUndefined();
    expect(Object.getPrototypeOf({})).toBe(Object.prototype);
  });

  test("a __proto__ key beside good rows refuses the whole file, not just the row, and the good rows are not half-imported", () => {
    const json = file(`{"good":${row("good")},"__proto__":${row("__proto__")}}`);
    expect(() => parseBank(json)).toThrow();
  });

  test("a nested __proto__ inside a row is not copied anywhere and pollutes nothing", () => {
    const poisoned = `{"key":"good","labels":["good"],"kind":"text","value":["v"],"sensitivity":"standard","signatures":[],"uses":1,"fills":0,"updatedAt":"2026","review":false,"__proto__":{"polluted":true},"extra":{"__proto__":{"polluted":true}}}`;
    const { bank } = parseBank(file(`{"good":${poisoned}}`));
    expect(Object.keys(bank.answers.good!).sort()).not.toContain("__proto__");
    expect(Object.getPrototypeOf(bank.answers)).toBe(Object.prototype);
    expect(({} as Record<string, unknown>).polluted).toBeUndefined();
    expect((bank.answers.good as unknown as Record<string, unknown>).polluted).toBeUndefined();
  });

  test("a row's key must equal its map key, so a __proto__ value under another name cannot rename itself", () => {
    expect(() => parseBank(file(`{"good":${row("__proto__")}}`))).toThrow();
  });

  test("an import over the cap is refused whole, and exactly at the cap is accepted", () => {
    const rows = (n: number) => `{${Array.from({ length: n }, (_, i) => `"question ${i}":${row(`question ${i}`)}`).join(",")}}`;
    expect(() => parseBank(file(rows(3000)))).toThrow(/max 2000/);
    expect(() => parseBank(file(rows(MAX_ANSWERS + 1)))).toThrow(/max 2000/);
    expect(Object.keys(parseBank(file(rows(MAX_ANSWERS))).bank.answers).length).toBe(MAX_ANSWERS);
  });

  test("an ordinary export still round-trips", () => {
    const b: Bank = { ...emptyBank(), answers: { alpha: answer("alpha"), beta: answer("beta") } };
    expect(Object.keys(parseBank(exportBank(b)).bank.answers).sort()).toEqual(["alpha", "beta"]);
  });
});

describe("69: at MAX_ANSWERS a new answer is refused, counted and shown, never evicting", () => {
  const field = (label: string, value: string): FieldInfo => ({ kind: "text", label, name: "", autocomplete: "", automationId: "", options: [], required: false, value: [value] });
  function full(): Bank {
    const b = emptyBank();
    for (let i = 0; i < MAX_ANSWERS; i++) b.answers[`question number ${i}`] = answer(`question number ${i}`);
    return b;
  }
  const now = new Date("2026-02-01T00:00:00Z");

  test("a bank at the cap refuses a new question, reports skippedFull, and keeps every old answer", () => {
    const b = full();
    const r = mergeCapture(b, [field("Favourite tool", "hammer")], "x.example", now);
    expect(r.skippedFull).toBe(1);
    expect(r.added).toBe(0);
    expect(r.skippedEmpty).toBe(0);
    expect(Object.keys(r.bank.answers).length).toBe(MAX_ANSWERS);
    expect(r.bank.answers["question number 0"]).toBeDefined();
    expect(r.bank.answers["favourite tool"]).toBeUndefined();
  });

  test("one under the cap saves the last one; at the cap an existing question is still updated", () => {
    const b = full();
    delete b.answers["question number 0"];
    expect(mergeCapture(b, [field("Favourite tool", "hammer")], "x.example", now)).toMatchObject({ added: 1 });
    const at = full();
    const upd = mergeCapture(at, [field("question number 5", "changed")], "x.example", now);
    expect(upd.skippedFull ?? 0).toBe(0);
    expect(upd.updated).toBe(1);
  });

  test("the count reaches the owner: a basics save and a frame sum both say the bank is full", () => {
    const reply = { added: 0, updated: 0, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 0, skippedFull: 2 };
    expect(isCaptureReply(reply)).toBe(true);
    expect(basicsSaveOutcome(reply).status).toContain(BANK_FULL);
    const sum = sumCaptures({ answered: [{ reply }, { reply }], failed: [] } as unknown as Parameters<typeof sumCaptures>[0]);
    expect(sum.skippedFull).toBe(4);
    expect(isCaptureReply({ ...reply, skippedFull: -1 })).toBe(false);
    expect(isCaptureReply({ ...reply, skippedFull: "2" })).toBe(false);
  });

  test("the wording states the cap and the way out", () => {
    expect(BANK_FULL).toContain(String(MAX_ANSWERS));
    expect(BANK_FULL).toMatch(/delete/i);
  });
});

