// Helper for w2badv-tracker.test.ts (not collected: no .test.). Run as its own `bun` process because it replaces
// ../src/extract with a fake form scan, which must not leak into other test files. Prints one JSON line:
// how many sends each scenario made.
import { mock } from "bun:test";
import type { Bank, FieldInfo } from "../src/types";
import { emptyBank } from "../src/types";

const info = (label: string): FieldInfo => ({ kind: "text", label, name: "", autocomplete: "", automationId: "", options: [], required: false, value: ["blue"] });
const real = await import("../src/extract");
mock.module("../src/extract", () => ({ ...real, scanForm: () => ({ roots: [], refs: ["Favourite colour", "Favourite editor", "Favourite shell"].map(l => ({ info: info(l), els: [], widget: "native", manual: "" })) }) }));
(globalThis as Record<string, unknown>).MutationObserver = class { observe() {} disconnect() {} };
const { createTracker } = await import("../src/tracker");

function target() {
  const l = new Map<string, Array<(e: Event) => void>>();
  return {
    addEventListener(t: string, f: (e: Event) => void) { l.set(t, [...(l.get(t) ?? []), f]); },
    removeEventListener() {},
    fire(t: string) { const e = new Event(t); for (const f of l.get(t) ?? []) f(e); },
  };
}
const sleep = (ms: number) => new Promise(r => setTimeout(r, ms));
const okBank = (): Bank => ({ ...emptyBank() });

async function scenario(getBank: () => Promise<Bank | null>, steps: (doc: ReturnType<typeof target>, win: ReturnType<typeof target>, t: ReturnType<typeof createTracker>) => Promise<void>): Promise<number> {
  const win = Object.assign(target(), { location: { href: "https://jobs.example/apply" } });
  const doc = Object.assign(target(), { nodeType: 9, defaultView: win, visibilityState: "visible" });
  let sends = 0;
  const t = createTracker({ doc: doc as unknown as Document, host: "jobs.example", getBank, send: async f => { sends++; return { added: f.length, updated: 0, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 0 }; }, commitMs: 10 });
  await steps(doc, win, t);
  t.dispose();
  return sends;
}

const out: Record<string, number> = {};
// R1: the first bank read is still in flight when the page goes away (slow storage or Mac app at page load).
out.slowFirstRead = await scenario(async () => { await sleep(80); return okBank(); }, async (doc, win) => {
  doc.fire("change"); await sleep(25); win.fire("pagehide"); await sleep(120);
});
// R2: the user edits and leaves inside the commit window, before any bank read has started.
out.leaveInsideCommitWindow = await scenario(async () => okBank(), async (doc, win) => {
  doc.fire("change"); win.fire("pagehide"); await sleep(60);
});
// R3: one good read, then one transient failure that the content script reports as null (content.ts bank() catches), then pagehide.
let n = 0;
out.transientNullAfterGoodRead = await scenario(async () => (++n === 1 ? okBank() : null), async (doc, win) => {
  doc.fire("change"); await sleep(40); doc.fire("change"); await sleep(40); win.fire("pagehide"); await sleep(40);
});
// R4: a read that throws once (not null): the pending fields stay, so the next flush still sends them.
let m = 0;
out.retryAfterThrow = await scenario(async () => { if (++m === 1) throw new Error("storage hiccup"); return okBank(); }, async (_d, _w, t) => {
  await t.flush("first", true).catch(() => null);
  await t.flush("second", true);
});
// R5 (round 2): an explicit Save when the background refuses with a reason reports that reason, not a count.
{
  const win = Object.assign(target(), { location: { href: "https://jobs.example/apply" } });
  const doc = Object.assign(target(), { nodeType: 9, defaultView: win, visibilityState: "visible" });
  const t = createTracker({ doc: doc as unknown as Document, host: "jobs.example", getBank: async () => null, send: async () => ({ error: "the answer bank could not be read" }), commitMs: 10 });
  doc.fire("change");
  const r = await t.flush("saved", true);
  t.dispose();
  out.refusedExplicit = r?.refused === "the answer bank could not be read" && r.failed ? 1 : 0;
}
// R6 (round 2): a bank known to disable the host keeps the page from sending, even when a later read fails.
{
  let reads = 0;
  out.knownDisabledThenNull = await scenario(async () => (++reads === 1 ? { ...emptyBank(), settings: { captureSelfId: false, disabledHosts: ["Jobs.Example."] } } : null), async (doc, win) => {
    doc.fire("change"); await sleep(40); doc.fire("change"); await sleep(40); win.fire("pagehide"); await sleep(40);
  });
}
console.log(JSON.stringify(out));
