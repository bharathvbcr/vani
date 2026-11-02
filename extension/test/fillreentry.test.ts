// A second fill must not run while the first is still writing, and must not wipe that fill's undo.
// The frame reply budget is not a lock: these tests never wait it out.
import { afterAll, afterEach, beforeAll, describe, expect, test } from "bun:test";
import type { FieldRef } from "../src/extract";
import { beginFill, fillRef, undoCount, undoFill } from "../src/fill";
import { FILL_BUSY, contentListener } from "../src/frames";
import { isFillReply } from "../src/messages";
import type { Answer, FieldInfo, Match } from "../src/types";

const BUSY = FILL_BUSY;

type Handler = (e: { type: string }) => void;

class FEvent {
  constructor(public type: string) {}
}

class FakeDoc {
  nodeType = 9;
  hidden = false;
  body: FakeEl;
  activeElement: FakeEl | null = null;
  defaultView = {
    Event: FEvent,
    InputEvent: FEvent,
    HTMLInputElement: class {},
    HTMLTextAreaElement: class {},
    requestAnimationFrame: (fn: () => void) => { fn(); return 1; },
  };
  constructor() { this.body = new FakeEl(this, "body"); }
}

class FakeEl {
  nodeType = 1;
  parent: FakeEl | null = null;
  attrs: Record<string, string> = {};
  style: Record<string, string> = {};
  handlers = new Map<string, Handler[]>();
  isConnected = true;
  text = "";
  constructor(public ownerDocument: FakeDoc, public localName: string) {}
  get parentElement(): FakeEl | null { return this.parent; }
  get textContent(): string { return this.text; }
  set textContent(v: string) { this.text = v; }
  getAttribute(n: string): string | null { return this.attrs[n] ?? null; }
  setAttribute(n: string, v: string): void { this.attrs[n] = v; }
  removeAttribute(n: string): void { delete this.attrs[n]; }
  getRootNode(): FakeDoc { return this.ownerDocument; }
  focus(): void { this.ownerDocument.activeElement = this; }
  blur(): void { if (this.ownerDocument.activeElement === this) this.ownerDocument.activeElement = null; }
  closest(): FakeEl | null { return null; }
  dispatchEvent(e: { type: string }): boolean {
    for (const h of this.handlers.get(e.type) ?? []) h(e);
    return true;
  }
}

class FakeInput extends FakeEl {
  private v = "";
  constructor(doc: FakeDoc) { super(doc, "input"); this.attrs["type"] = "text"; }
  get value(): string { return this.v; }
  set value(x: string) { this.v = x; }
  get type(): string { return "text"; }
  get maxLength(): number { return -1; }
}

const answer = (key: string, value: string[]): Answer =>
  ({ key, labels: [], kind: "text", value, sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: "2026-01-01T00:00:00Z", review: false });
const match = (key: string, value: string[]): Match =>
  ({ answer: answer(key, value), via: "label", score: 0.9, value, review: false });
const info = (label: string): FieldInfo =>
  ({ kind: "text", label, name: "", autocomplete: "", automationId: "", options: [], required: false, value: [] });

function textField(): { el: FakeInput; ref: FieldRef } {
  const doc = new FakeDoc();
  const el = new FakeInput(doc);
  doc.body.parent = null;
  el.parent = doc.body;
  const ref: FieldRef = { info: info("Note"), els: [el as unknown as HTMLElement], widget: "native", manual: "" };
  return { el, ref };
}

const NOTHING = { added: 0, updated: 0, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 0, unchanged: 0 };
const NO_FIELDS = { fields: 0, matched: 0, host: "embed.example", disabled: false };

function listener(fill: (overwrite?: boolean, typeahead?: boolean) => Promise<{ outcomes: unknown[] }>) {
  return contentListener({
    scan: async () => NO_FIELDS,
    fill,
    capture: async () => NOTHING,
    appliedShow: null,
  });
}

function ask(l: ReturnType<typeof listener>, msg: unknown): Promise<{ async: boolean; replies: unknown[] }> {
  const replies: unknown[] = [];
  let done = (): void => undefined;
  const answered = new Promise<void>(r => { done = r; });
  const async = l(msg, {}, r => { replies.push(r); done(); });
  return answered.then(() => ({ async, replies }));
}

describe("overlapping fill messages", () => {
  test("a second fill that arrives after the first has awaited, and before the first returns, is rejected and does not clear the first fill's undo", async () => {
    const { el, ref } = textField();
    let calls = 0;
    let releaseFirst: () => void = () => undefined;
    const gate = new Promise<void>(r => { releaseFirst = r; });
    let wrote: () => void = () => undefined;
    const wroteP = new Promise<void>(r => { wrote = r; });
    let releaseHold: () => void = () => undefined;
    const hold = new Promise<void>(r => { releaseHold = r; });
    const l = listener(async () => {
      const call = ++calls;
      if (call === 1) {
        await gate;
        beginFill();
        const out = await fillRef(ref, match("k", ["ok"]));
        expect(out.filled).toBe(true);
        expect(undoCount()).toBe(1);
        beginFill();
        expect(undoCount()).toBe(1);
        wrote();
        await hold;
        return { outcomes: [{ label: "Note", filled: true }] };
      }
      await wroteP;
      beginFill();
      return { outcomes: [] };
    });

    const first = ask(l, { type: "fill" });
    await Promise.resolve();
    expect(calls).toBe(1);
    const second = ask(l, { type: "fill" });
    await Promise.resolve();
    releaseFirst();
    await wroteP;
    await Promise.resolve();
    releaseHold();
    const [a, b] = await Promise.all([first, second]);
    expect(a.replies).toEqual([{ outcomes: [{ label: "Note", filled: true }] }]);
    expect(calls).toBe(1);
    expect(b.replies).toEqual([{ error: BUSY }]);
    expect(isFillReply(b.replies[0])).toBe(false);
    expect(el.value).toBe("ok");
    expect(undoCount()).toBe(1);
    expect(await undoFill()).toMatchObject({ restored: 1, edited: 0, manual: 0, gone: 0 });
    expect(el.value).toBe("");
    expect(undoCount()).toBe(0);
  });

  test("a throw from the first fill releases the lock so a later fill can run", async () => {
    const { el, ref } = textField();
    let calls = 0;
    let release: () => void = () => undefined;
    const gate = new Promise<void>(r => { release = r; });
    const l = listener(async () => {
      const n = ++calls;
      if (n === 1) {
        await gate;
        throw new Error("boom");
      }
      beginFill();
      await fillRef(ref, match("k", ["ok"]));
      return { outcomes: [{ label: "Note", filled: true }] };
    });
    const first = ask(l, { type: "fill" });
    await Promise.resolve();
    const second = await ask(l, { type: "fill" });
    expect(second.replies).toEqual([{ error: BUSY }]);
    expect(isFillReply(second.replies[0])).toBe(false);
    release();
    expect((await first).replies).toEqual([{ error: "boom" }]);
    const third = await ask(l, { type: "fill" });
    expect(third.replies).toEqual([{ outcomes: [{ label: "Note", filled: true }] }]);
    expect(calls).toBe(2);
    expect(el.value).toBe("ok");
    expect(await undoFill()).toMatchObject({ restored: 1 });
    expect(el.value).toBe("");
  });
});

// --- background gate: shortcut and popup share one check-and-set per tab -----------------

type Listener = (msg: unknown, sender: object, sendResponse: (r?: unknown) => void) => boolean;

const g = globalThis as Record<string, unknown>;
const saved = { chrome: g.chrome, browser: g.browser };
let listenerBg: Listener | undefined;
const removed: Array<(tabId: number) => void> = [];
const commands: Array<(command: string) => void> = [];
let activeTabs: Array<{ id?: number; url?: string }> = [];
const tabSent: Array<{ tabId: number; msg: unknown; frameId: number | undefined }> = [];
let tabReply: (tabId: number, frameId: number) => unknown = () => ({ outcomes: [] });

beforeAll(async () => {
  g.browser = undefined;
  g.chrome = {
    runtime: {
      onMessage: { addListener(cb: Listener) { listenerBg = cb; } },
      sendNativeMessage(_app: string, _m: object, cb: (r: unknown) => void) {
        const runtime = (g.chrome as { runtime: { lastError?: { message: string } | undefined } }).runtime;
        runtime.lastError = { message: "host not found" };
        cb(undefined);
        runtime.lastError = undefined;
      },
      connectNative() { throw new Error("no host"); },
      lastError: undefined,
    },
    storage: {
      local: {
        get: async () => ({}),
        set: async () => undefined,
        remove: async () => undefined,
      },
      session: undefined,
    },
    tabs: {
      onUpdated: { addListener() {} },
      onRemoved: { addListener(cb: (tabId: number) => void) { removed.push(cb); } },
      query: async () => activeTabs,
      get: async (id: number) => ({ id, url: "https://a.example/" }),
      sendMessage: async (tabId: number, msg: unknown, opts?: { frameId?: number }) => {
        tabSent.push({ tabId, msg, frameId: opts?.frameId });
        return tabReply(tabId, opts?.frameId ?? 0);
      },
    },
    action: undefined,
    scripting: { executeScript: async () => [] },
    commands: { onCommand: { addListener(cb: (command: string) => void) { commands.push(cb); } } },
  };
  await import("../src/background.ts?fillreentry");
});
afterAll(() => { g.chrome = saved.chrome; g.browser = saved.browser; });

async function askBg(msg: unknown, sender: object = {}): Promise<{ async: boolean; replies: unknown[] }> {
  const replies: unknown[] = [];
  let done = (): void => undefined;
  const answered = new Promise<void>(r => { done = r; });
  const async = listenerBg!(msg, sender, r => { replies.push(r); done(); });
  await Promise.race([answered, new Promise(r => setTimeout(r, 2000))]);
  return { async, replies };
}

const page = (tabId: number, frameId = 0) => ({ tab: { id: tabId }, frameId, url: "https://a.example/" });
const fillsSent = () => tabSent.filter(s => (s.msg as { type?: string }).type === "fill");

afterEach(() => {
  for (const id of [4, 6, 8]) for (const cb of removed) cb(id);
  tabReply = async () => ({ outcomes: [] });
  activeTabs = [];
});

async function register(tabId: number, frameId = 0): Promise<void> {
  expect(await askBg({ type: "frameReady" }, page(tabId, frameId))).toEqual({ async: true, replies: [{ ok: true }] });
}

function shortcut(): void {
  for (const cb of commands) cb("fill-form");
}

describe("shortcut and popup share one fill gate per tab", () => {
  test("a shortcut fired while the first fill promise is unresolved does not send another fill", async () => {
    tabSent.length = 0;
    activeTabs = [{ id: 4, url: "https://a.example/" }];
    let release: (v: unknown) => void = () => undefined;
    tabReply = () => new Promise(r => { release = r; });
    await register(4, 0);
    shortcut();
    for (let i = 0; i < 20 && fillsSent().length < 1; i++) await new Promise(r => setTimeout(r, 5));
    expect(fillsSent().length).toBe(1);
    shortcut();
    await new Promise(r => setTimeout(r, 20));
    expect(fillsSent().length).toBe(1);
    expect(fillsSent()[0]).toMatchObject({ tabId: 4, frameId: 0 });
    release({ outcomes: [{ label: "Note", filled: true }] });
    await new Promise(r => setTimeout(r, 20));
  });

  test("a frame reply does not open the gate, an empty outcome list is not how a busy fill is reported, and two tabs do not share the gate", async () => {
    tabSent.length = 0;
    activeTabs = [{ id: 4, url: "https://a.example/" }];
    tabReply = async () => ({ outcomes: [{ label: "Note", filled: true }] });
    await register(4, 0);
    await register(8, 0);
    const run = (tabId: number) => askBg({ type: "fillRun", tabId, overwrite: false, typeahead: false }, {});
    const first = await run(4);
    expect(first.replies[0]).toMatchObject({ answered: [{ frameId: 0, reply: { outcomes: [{ label: "Note", filled: true }] } }] });
    const sent = fillsSent().length;
    const again = await run(4);
    expect(again.replies).toEqual([{ error: BUSY }]);
    expect(isFillReply(again.replies[0])).toBe(false);
    expect(fillsSent().length).toBe(sent);
    const other = await run(8);
    expect(other.replies[0]).toMatchObject({ answered: [{ frameId: 0 }] });
    expect(fillsSent().filter(s => s.tabId === 8).length).toBe(1);
    expect(fillsSent().filter(s => s.tabId === 4).length).toBe(sent);
    expect(await askBg({ type: "fillSettled" }, page(4, 0))).toEqual({ async: true, replies: [{ ok: true }] });
    const retried = await run(4);
    expect(retried.replies[0]).toMatchObject({ asked: 1 });
    expect(fillsSent().filter(s => s.tabId === 4).length).toBe(sent + 1);
  });

  test("the panel's claim blocks a shortcut, a removed tab drops the gate, and the wrong realm cannot claim or settle", async () => {
    tabSent.length = 0;
    activeTabs = [{ id: 6, url: "https://a.example/" }];
    tabReply = async () => ({ outcomes: [] });
    await register(6, 2);
    expect(await askBg({ type: "fillClaim" }, page(6, 2))).toEqual({ async: true, replies: [{ ok: true }] });
    shortcut();
    await new Promise(r => setTimeout(r, 20));
    expect(fillsSent().length).toBe(0);
    expect(await askBg({ type: "fillRun" }, page(6, 2))).toEqual({ async: false, replies: [{ error: "fillRun is not accepted from a page", code: "bad_request" }] });
    expect(await askBg({ type: "fillClaim" }, {})).toEqual({ async: false, replies: [{ error: "fillClaim is not accepted from the popup", code: "bad_request" }] });
    expect(await askBg({ type: "fillSettled" }, {})).toEqual({ async: false, replies: [{ error: "fillSettled is not accepted from the popup", code: "bad_request" }] });
    expect(await askBg({ type: "fillRun", tabId: -1 }, {})).toEqual({ async: false, replies: [{ error: "malformed fillRun message" }] });
    for (const cb of removed) cb(6);
    await new Promise(r => setTimeout(r, 10));
    await register(6, 2);
    shortcut();
    for (let i = 0; i < 20 && fillsSent().length < 1; i++) await new Promise(r => setTimeout(r, 5));
    expect(fillsSent().length).toBe(1);
    expect(await askBg({ type: "fillSettled" }, page(6, 2))).toEqual({ async: true, replies: [{ ok: true }] });
  });
});
