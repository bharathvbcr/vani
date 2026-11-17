// Slice w2b round 2: a map read of an absent key must never return an Object.prototype member, and a field
// labelled "Constructor" must capture, store, read back and fill like any other.
import { describe, expect, test } from "bun:test";
import { exportBank, mergeBanks, mergeCapture, mergeCaptureDelta, parseBank } from "../src/bank";
import { autocompleteKey } from "../src/normalize";
import { sumCaptures } from "../src/frames";
import { isCaptureReply } from "../src/messages";
import { matchField } from "../src/match";
import { basicsValues } from "../src/basics";
import { type LocalArea, createStore } from "../src/store";
import { type Answer, type FieldInfo, emptyBank, ownAnswer } from "../src/types";

const field = (label: string, value = "Acme", autocomplete = ""): FieldInfo => ({ kind: "text", label, name: "", autocomplete, automationId: "", options: [], required: false, value: [value] });
const NOW = new Date("2026-01-01");
const ABSENT = ["toString", "constructor", "hasOwnProperty", "valueOf", "__proto__", "prototype"];

describe("ownAnswer reads own keys only", () => {
  test("absent Object.prototype names return undefined, present ones are returned", () => {
    const bank = mergeCapture(emptyBank(), [field("Favourite colour", "teal")], "jobs.example", NOW).bank;
    for (const k of ABSENT) expect(ownAnswer(bank.answers, k), k).toBeUndefined();
    expect(ownAnswer(bank.answers, "favourite colour")?.value).toEqual(["teal"]);
  });

  test("mergeBanks, mergeCaptureDelta and the basics reader do not treat an absent 'constructor' as held", () => {
    const a = mergeCapture(emptyBank(), [field("Favourite colour", "teal")], "jobs.example", NOW).bank;
    const b = mergeCapture(emptyBank(), [field("Constructor", "yes")], "jobs.example", NOW).bank;
    const merged = mergeBanks(a, b);
    expect(Object.keys(merged.answers).sort()).toEqual(["constructor", "favourite colour"]);
    const delta = mergeCaptureDelta(a, [field("Constructor", "yes")], "jobs.example", NOW);
    expect(delta.upsert.map(x => x.key)).toEqual(["constructor"]);
    expect(delta.deleteKeys).toEqual([]);
    expect(basicsValues(b)["Constructor"]).toBeUndefined();
    expect(basicsValues(a)).toBeDefined();
  });

  test("an autocomplete attribute named like a prototype member is no autocomplete key", () => {
    for (const k of ["constructor", "toString", "hasOwnProperty", "__proto__", "valueOf"]) expect(autocompleteKey(k), k).toBe("");
    expect(autocompleteKey("section-a shipping given-name")).toBe("first name");
  });
});

describe("a 'Constructor' field round-trips", () => {
  test("capture, store, reread and match", async () => {
    class Local implements LocalArea {
      data: Record<string, unknown> = {};
      async get(k: string) { return k in this.data ? { [k]: this.data[k] } : {}; }
      async set(i: Record<string, unknown>) { Object.assign(this.data, i); }
      async remove(k: string) { delete this.data[k]; }
    }
    const local = new Local();
    const mk = () => createStore({ transport: async () => { throw new Error("no host"); }, local });
    const f = field("Constructor", "yes");
    await mk().updateBank(b => mergeCapture(b, [f], "jobs.example", NOW).bank);
    const bank = await mk().loadBank();
    expect(ownAnswer(bank.answers, "constructor")?.value).toEqual(["yes"]);
    const m = matchField(f, bank, "jobs.example");
    expect(m?.value).toEqual(["yes"]);
    expect(() => parseBank(exportBank(bank))).not.toThrow();
  });

  test("an absent-key lookup on a bank without 'constructor' finds nothing", () => {
    const bank = mergeCapture(emptyBank(), [field("Favourite colour", "teal")], "jobs.example", NOW).bank;
    for (const label of ["Constructor", "toString", "hasOwnProperty"]) {
      const m = matchField(field(label, ""), bank, "jobs.example");
      expect(m, label).toBeNull();
    }
  });
});

describe("a refused save carries its reason to the popup", () => {
  const zero = { added: 0, updated: 0, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 0 };
  test("isCaptureReply accepts a bounded string refusal and sumCaptures keeps the first one", () => {
    expect(isCaptureReply({ ...zero, refused: "capture is turned off for this site" })).toBe(true);
    expect(isCaptureReply({ ...zero, refused: 3 })).toBe(false);
    expect(isCaptureReply({ ...zero, refused: "x".repeat(501) })).toBe(false);
    const sum = sumCaptures({ asked: 2, failed: [], answered: [{ reply: { ...zero, refused: "first" } }, { reply: { ...zero, refused: "second" } }] } as never);
    expect(sum.refused).toBe("first");
    expect(sumCaptures({ asked: 1, failed: [], answered: [{ reply: zero }] } as never).refused).toBeUndefined();
  });
});

describe("import of prototype-named keys", () => {
  const file = (answers: string) => `{"version":1,"answers":${answers},"settings":{"captureSelfId":false,"disabledHosts":[]}}`;
  const row = (key: string): string => {
    const a: Answer = { key, labels: [key], kind: "text", value: ["v"], sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: "2026-01-01", review: false };
    return JSON.stringify(a);
  };
  test("a 'constructor' key is accepted and reads back; '__proto__' is refused and no prototype changes", () => {
    const { bank } = parseBank(file(`{"constructor":${row("constructor")}}`));
    expect(ownAnswer(bank.answers, "constructor")?.value).toEqual(["v"]);
    expect(() => parseBank(file(`{"__proto__":${row("__proto__")}}`))).toThrow();
    expect(Object.getPrototypeOf({})).toBe(Object.prototype);
    expect(Object.getPrototypeOf(bank.answers)).toBe(Object.prototype);
  });
});
