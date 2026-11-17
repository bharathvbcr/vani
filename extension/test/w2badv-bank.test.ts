// Slice w2b adversarial (w2badv): page-derived strings must not be able to poison the bank, and the disabled-host
// comparison must agree with every other reader of the list. Each test here fails against e62f5de.
import { describe, expect, test } from "bun:test";
import { exportBank, hostDisabled, mergeCapture, parseBank } from "../src/bank";
import { type LocalArea, createStore } from "../src/store";
import { type FieldInfo, emptyBank } from "../src/types";
import { readFileSync } from "node:fs";
import { join } from "node:path";

const field = (label: string, value = "Acme"): FieldInfo => ({ kind: "text", label, name: "", autocomplete: "", automationId: "", options: [], required: false, value: [value] });

describe("w2badv: a page field labelled like an Object.prototype member cannot make the bank unreadable", () => {
  for (const label of ["constructor", "Constructor", "prototype", "__proto__"]) {
    test(`mergeCapture(${label}) then export then parseBank round-trips`, () => {
      const r = mergeCapture(emptyBank(), [field(label)], "jobs.example", new Date("2026-01-01"));
      // Either the row is refused (counted, not stored) or it is stored under a key the reader accepts.
      expect(() => parseBank(exportBank(r.bank)), `stored keys ${JSON.stringify(Object.keys(r.bank.answers))}, added ${r.added}`).not.toThrow();
    });
  }

  test("__proto__ label: a row counted as added is really in the bank, and no prototype was swapped", () => {
    const r = mergeCapture(emptyBank(), [field("__proto__")], "jobs.example", new Date("2026-01-01"));
    expect(Object.getPrototypeOf(r.bank.answers)).toBe(Object.prototype);
    expect(Object.keys(r.bank.answers).length).toBe(r.added);
  });

  test("one saved 'constructor' field leaves the whole stored bank unreadable by the same store (loadBank throws)", async () => {
    class Local implements LocalArea {
      data: Record<string, unknown> = {};
      async get(k: string) { return k in this.data ? { [k]: this.data[k] } : {}; }
      async set(i: Record<string, unknown>) { Object.assign(this.data, i); }
      async remove(k: string) { delete this.data[k]; }
    }
    const local = new Local();
    const store = createStore({ transport: async () => { throw new Error("no host"); }, local });
    await store.updateBank(b => mergeCapture(b, [field("Favourite colour", "teal"), field("Constructor", "yes")], "jobs.example", new Date("2026-01-01")).bank);
    const reread = createStore({ transport: async () => { throw new Error("no host"); }, local });
    const bank = await reread.loadBank();
    expect(bank.answers["favourite colour"]).toBeDefined();
  });
});

describe("w2badv: disabledHosts comparison drift (popup.ts / AppModel.swift compare exactly)", () => {
  test("popup.ts reads and edits the list with the same comparison capture uses (hostDisabled / normalizeHost)", () => {
    const src = readFileSync(join(import.meta.dir, "../src/popup.ts"), "utf-8");
    const exact = src.match(/disabledHosts\??\.includes\(host\)|disabledHosts\.filter\(h => h !== host\)/g) ?? [];
    expect(exact, "popup.ts still compares disabledHosts case-sensitively").toEqual([]);
  });

  test("the exact comparison leaves 'Example.com' unremovable from the popup while capture still refuses it", () => {
    const bank = { ...emptyBank(), settings: { captureSelfId: false, disabledHosts: ["Example.com"] } };
    // popup.ts #disabled onchange (off): hosts = disabledHosts.filter(h => h !== host) with host = "example.com"
    const afterUntick = bank.settings.disabledHosts.filter(h => h !== "example.com");
    expect(hostDisabled({ ...bank, settings: { ...bank.settings, disabledHosts: afterUntick } }, "example.com")).toBe(false);
  });
});
