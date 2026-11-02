// Attacks the fill lock past the cases the first reentrancy test already names:
// a write that throws later, a frame budget that elapses, the popup's own click, two tabs, a dead frame.
import { afterAll, afterEach, beforeAll, describe, expect, test } from "bun:test";
import { FILL_BUSY, FRAME_REPLY_MS, contentListener, releaseFillClaim } from "../src/frames";
import { beginFill, fillRef, undoCount, undoFill } from "../src/fill";
import { isFillReply } from "../src/messages";
import type { Answer, FieldInfo, Match } from "../src/types";

const BUSY = FILL_BUSY;

type Handler = (e: { type: string }) => void;
class FEvent { constructor(public type: string) {} }
class FakeDoc {
  nodeType = 9;
  hidden = false;
  body: FakeEl;
  activeElement: FakeEl | null = null;
  defaultView = {
    Event: FEvent, InputEvent: FEvent,
    HTMLInputElement: class {}, HTMLTextAreaElement: class {},
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
  closest(): null { return null; }
  dispatchEvent(e: { type: string }): boolean {
    for (const h of this.handlers.get(e.type) ?? []) h(e);
    return true;
  }
}
class FakeInput extends FakeEl {
  private v = "";
  constructor(doc: FakeDoc) { super(doc, "input"); this.attrs.type = "text"; }
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

function textField(): { el: FakeInput; ref: { info: FieldInfo; els: HTMLElement[]; widget: "native"; manual: string } } {
  const doc = new FakeDoc();
  const el = new FakeInput(doc);
  el.parent = doc.body;
  return { el, ref: { info: info("名前"), els: [el as unknown as HTMLElement], widget: "native", manual: "" } };
}

type ContentL = (msg: unknown, sender: object, sendResponse: (r?: unknown) => void) => boolean;

function askContent(l: ContentL, msg: unknown): Promise<{ async: boolean; replies: unknown[] }> {
  const replies: unknown[] = [];
  let done = (): void => undefined;
  const answered = new Promise<void>(r => { done = r; });
  const async = l(msg, {}, r => { replies.push(r); done(); });
  return answered.then(() => ({ async, replies }));
}

describe("a fill that has already written", () => {
  afterEach(() => { releaseFillClaim(); });

  test("a second message during a later await does not write, and the lock stays until the throw finishes writing", async () => {
    const { el, ref } = textField();
    let release: () => void = () => undefined;
    const gate = new Promise<void>(r => { release = r; });
    let calls = 0;
    const l = contentListener({
      scan: async () => ({ fields: 0, matched: 0, host: "embed.example", disabled: false }),
      capture: async () => ({ added: 0, updated: 0, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 0, unchanged: 0 }),
      appliedShow: null,
      fill: async (overwrite) => {
        const n = ++calls;
        if (n !== 1) {
          beginFill();
          el.value = overwrite ? "second-true" : "second-false";
          return { outcomes: [] };
        }
        await Promise.resolve();
        beginFill();
        const wrote = await fillRef(ref, match("k", ["partial"]), { overwrite: true });
        expect(wrote.filled).toBe(true);
        await gate;
        el.value = "partial-2";
        throw new Error("mid");
      },
    });
    const first = askContent(l, { type: "fill", overwrite: false });
    for (let i = 0; i < 40 && undoCount() === 0; i++) await new Promise(r => setTimeout(r, 5));
    expect(el.value).toBe("partial");
    expect(undoCount()).toBe(1);
    const during = await askContent(l, { type: "fill", overwrite: true });
    expect(during.async).toBe(false);
    expect(during.replies).toEqual([{ error: BUSY }]);
    expect(isFillReply(during.replies[0])).toBe(false);
    expect(el.value).toBe("partial");
    expect(undoCount()).toBe(1);
    expect(calls).toBe(1);
    release();
    const failed = await first;
    expect(failed.replies).toEqual([{ error: "mid" }]);
    expect(el.value).toBe("partial-2");
    expect(undoCount()).toBe(1);
    // The same fill wrote again after the undo snapshot. Undo must not clobber that, and must not have run early.
    expect(await undoFill()).toMatchObject({ edited: 1, restored: 0 });
    expect(el.value).toBe("partial-2");
    const later = await askContent(l, { type: "fill", overwrite: true });
    expect(later.replies).toEqual([{ outcomes: [] }]);
    expect(calls).toBe(2);
    expect(el.value).toBe("second-true");
  });
});

// --- background gate and the popup's Fill click ---------------------------------------

type Listener = (msg: unknown, sender: object, sendResponse: (r?: unknown) => void) => boolean;
type El = {
  disabled: boolean;
  hidden: boolean;
  textContent: string;
  value: string;
  checked: boolean;
  className: string;
  title: string;
  innerHTML: string;
  style: Record<string, string>;
  dataset: Record<string, string>;
  attrs: Record<string, string>;
  onclick: ((ev?: unknown) => unknown) | null;
  classList: { add: () => void; remove: () => void; toggle: () => boolean; contains: () => boolean };
  querySelector: (sel: string) => El;
  querySelectorAll: () => El[];
  setAttribute: (n: string, v: string) => void;
  getAttribute: (n: string) => string | null;
  append: (...n: unknown[]) => void;
  appendChild: () => void;
  remove: () => void;
  click: () => void;
  replaceChildren: () => void;
  focus: () => void;
  addEventListener: () => void;
  removeEventListener: () => void;
  children: unknown[];
  childElementCount: number;
};

const els = new Map<string, El>();
function makeEl(sel: string): El {
  const found = els.get(sel);
  if (found) return found;
  const el = {
    disabled: false, hidden: false, textContent: "", value: "", checked: false, className: "", title: "", innerHTML: "",
    style: {}, dataset: {}, attrs: {} as Record<string, string>, onclick: null as El["onclick"],
    classList: { add() {}, remove() {}, toggle() { return false; }, contains() { return false; } },
    querySelector(s: string) { return makeEl(sel + " " + s); },
    querySelectorAll() { return []; },
    setAttribute(n: string, v: string) { el.attrs[n] = v; },
    getAttribute(n: string) { return el.attrs[n] ?? null; },
    append() {}, appendChild() {}, remove() {}, click() {}, replaceChildren() {}, focus() {},
    addEventListener() {}, removeEventListener() {},
    children: [], childElementCount: 0,
  };
  els.set(sel, el);
  return el;
}

const g = globalThis as Record<string, unknown>;
const saved = { chrome: g.chrome, browser: g.browser, document: g.document, window: g.window };
let listenerBg: Listener | undefined;
const removed: Array<(tabId: number) => void> = [];
const commands: Array<(command: string) => void> = [];
let activeTabs: Array<{ id?: number; url?: string }> = [];
const tabSent: Array<{ tabId: number; msg: unknown; frameId: number | undefined }> = [];
const hangers: Array<{ resolve: (v: unknown) => void }> = [];
let hangFills = false;
let injectFails = false;
let probeResults: Array<{ frameId: number; result: boolean }> = [];
let queryThrows = false;
let noReceiver = new Set<number>();
let portClosed = new Set<number>();
const realSetTimeout = globalThis.setTimeout;
const realClearTimeout = globalThis.clearTimeout;

function fillsSent(): Array<{ tabId: number; msg: unknown; frameId: number | undefined }> {
  return tabSent.filter(s => (s.msg as { type?: string }).type === "fill");
}

beforeAll(async () => {
  g.browser = undefined;
  g.window = g;
  g.addEventListener = () => undefined;
  g.document = {
    querySelector: (sel: string) => makeEl(sel),
    querySelectorAll: () => [],
    getElementById: (id: string) => makeEl("#" + id),
    createElement: (tag: string) => makeEl(tag),
    createTextNode: (t: string) => ({ textContent: t }),
    documentElement: makeEl("html"),
  };
  g.chrome = {
    runtime: {
      onMessage: { addListener(cb: Listener) { listenerBg = cb; } },
      sendMessage(msg: unknown) {
        return new Promise(resolve => { listenerBg!(msg, {}, resolve); });
      },
      sendNativeMessage(_app: string, _m: object, cb: (r: unknown) => void) {
        const runtime = (g.chrome as { runtime: { lastError?: { message: string } } }).runtime;
        runtime.lastError = { message: "host not found" };
        cb(undefined);
        runtime.lastError = undefined;
      },
      connectNative() { throw new Error("no host"); },
      lastError: undefined,
    },
    storage: {
      local: {
        get: async (k: string | string[]) => {
          const keys = Array.isArray(k) ? k : [k];
          return Object.fromEntries(keys.filter(x => x in local).map(x => [x, local[x]]));
        },
        set: async (items: Record<string, unknown>) => { local = { ...local, ...items }; },
        remove: async (k: string | string[]) => { for (const x of Array.isArray(k) ? k : [k]) delete local[x]; },
      },
      session: undefined,
    },
    tabs: {
      onUpdated: { addListener() {} },
      onRemoved: { addListener(cb: (tabId: number) => void) { removed.push(cb); } },
      query: async () => { if (queryThrows) throw new Error("no window"); return activeTabs; },
      get: async (id: number) => ({ id, url: "https://a.example/" }),
      sendMessage: async (tabId: number, msg: unknown, opts?: { frameId?: number }) => {
        const frameId = opts?.frameId;
        tabSent.push({ tabId, msg, frameId });
        if ((msg as { type?: string }).type === "fill" && hangFills) {
          return new Promise(resolve => { hangers.push({ resolve }); });
        }
        if (frameId !== undefined && noReceiver.has(frameId)) throw new Error("Could not establish connection. Receiving end does not exist.");
        if (frameId !== undefined && portClosed.has(frameId)) throw new Error("The message port closed before a response was received.");
        return { outcomes: [{ label: "Note", filled: true }] };
      },
    },
    action: undefined,
    scripting: {
      executeScript: async (opts: { files?: string[]; func?: unknown }) => {
        if (opts.files) { if (injectFails) throw new Error("cannot inject"); return []; }
        return probeResults;
      },
    },
    commands: { onCommand: { addListener(cb: (command: string) => void) { commands.push(cb); } } },
  };
  let local: Record<string, unknown> = {};
  await import("../src/background.ts?fillattack");
  await import("../src/popup.ts?fillattack");
  await new Promise(r => setTimeout(r, 40));
});

afterAll(() => {
  g.chrome = saved.chrome;
  g.browser = saved.browser;
  g.document = saved.document;
  g.window = saved.window;
  globalThis.setTimeout = realSetTimeout;
  globalThis.clearTimeout = realClearTimeout;
});

function installFastBudget(): void {
  globalThis.setTimeout = ((fn: TimerHandler, ms?: number, ...args: unknown[]) => {
    const wait = typeof ms === "number" && ms >= FRAME_REPLY_MS ? 0 : ms;
    return realSetTimeout(fn as () => void, wait, ...args);
  }) as typeof setTimeout;
  globalThis.clearTimeout = realClearTimeout;
}
function restoreBudget(): void {
  globalThis.setTimeout = realSetTimeout;
  globalThis.clearTimeout = realClearTimeout;
}

async function ticks(n: number): Promise<void> {
  for (let i = 0; i < n; i++) await new Promise(r => realSetTimeout(r, 0));
}

async function askBg(msg: unknown, sender: object = {}): Promise<{ async: boolean; replies: unknown[] }> {
  const replies: unknown[] = [];
  let done = (): void => undefined;
  const answered = new Promise<void>(r => { done = r; });
  const async = listenerBg!(msg, sender, r => { replies.push(r); done(); });
  await Promise.race([answered, new Promise(r => realSetTimeout(r, 2000))]);
  return { async, replies };
}

const page = (tabId: number, frameId = 0) => ({ tab: { id: tabId }, frameId, url: "https://a.example/" });

async function register(tabId: number, frameId = 0): Promise<void> {
  expect(await askBg({ type: "frameReady" }, page(tabId, frameId))).toEqual({ async: true, replies: [{ ok: true }] });
}

function shortcut(): void {
  for (const cb of commands) cb("fill-form");
}

async function untilFills(n: number): Promise<void> {
  for (let i = 0; i < 30 && fillsSent().length < n; i++) await new Promise(r => realSetTimeout(r, 5));
}

afterEach(async () => {
  restoreBudget();
  for (const h of hangers.splice(0)) h.resolve({ outcomes: [{ label: "Note", filled: true }] });
  await ticks(3);
  for (const id of [4, 6, 8, 9]) for (const cb of removed) cb(id);
  tabSent.length = 0;
  hangFills = false;
  injectFails = false;
  probeResults = [];
  queryThrows = false;
  noReceiver = new Set();
  portClosed = new Set();
  activeTabs = [];
  releaseFillClaim();
});

describe("shortcut, popup, and the per-tab gate", () => {
  test("a burst of shortcuts and a popup click share one gate, and the frame budget does not open it", async () => {
    installFastBudget();
    activeTabs = [{ id: 4, url: "https://a.example/" }];
    hangFills = true;
    await register(4, 0);
    tabSent.length = 0;
    shortcut();
    shortcut();
    for (let i = 0; i < 8; i++) shortcut();
    await untilFills(1);
    expect(fillsSent().length).toBe(1);
    expect(fillsSent()[0]).toMatchObject({ tabId: 4, frameId: 0 });
    await ticks(5);
    shortcut();
    await ticks(3);
    expect(fillsSent().length).toBe(1);
    const fillBtn = makeEl("#fill");
    const overwrite = makeEl("#overwrite");
    overwrite.checked = true;
    const click = fillBtn.onclick;
    expect(typeof click).toBe("function");
    void click!.call(fillBtn);
    await ticks(5);
    expect(fillsSent().length).toBe(1);
    const again = await askBg({ type: "fillRun", tabId: 4, overwrite: true, typeahead: false }, {});
    expect(again.replies).toEqual([{ error: BUSY }]);
    expect(isFillReply(again.replies[0])).toBe(false);
    expect(fillsSent().length).toBe(1);
    expect(makeEl("#status").textContent).not.toContain("Filled");
  });

  test("after the budget the popup button is enabled and a second click still does not send", async () => {
    installFastBudget();
    activeTabs = [{ id: 4, url: "https://a.example/role" }];
    hangFills = true;
    await register(4, 0);
    tabSent.length = 0;
    const fillBtn = makeEl("#fill");
    makeEl("#overwrite").checked = false;
    makeEl("#typeahead").checked = true;
    const click = fillBtn.onclick!;
    const firstClick = click.call(fillBtn);
    const secondClick = click.call(fillBtn);
    await untilFills(1);
    await ticks(8);
    await firstClick;
    await secondClick;
    expect(fillsSent().length).toBe(1);
    expect(fillBtn.disabled).toBe(false);
    const statusBefore = makeEl("#status").textContent;
    const third = click.call(fillBtn);
    await third;
    expect(fillsSent().length).toBe(1);
    expect(makeEl("#status").textContent).toBe(BUSY);
    expect(statusBefore.length).toBeGreaterThan(0);
    expect(await askBg({ type: "fillSettled" }, page(4, 0))).toEqual({ async: true, replies: [{ ok: true }] });
    tabSent.length = 0;
    await click.call(fillBtn);
    await untilFills(1);
    expect(fillsSent().length).toBe(1);
  });

  test("two tabs do not share a gate, and finishing one does not clear the other", async () => {
    hangFills = true;
    activeTabs = [{ id: 4, url: "https://a.example/" }];
    await register(4, 0);
    await register(8, 2);
    tabSent.length = 0;
    const a = askBg({ type: "fillRun", tabId: 4, overwrite: false }, {});
    const b = askBg({ type: "fillRun", tabId: 8, overwrite: true }, {});
    await untilFills(2);
    expect(fillsSent().map(s => s.tabId).sort()).toEqual([4, 8]);
    expect((fillsSent().find(s => s.tabId === 8)!.msg as { overwrite?: boolean }).overwrite).toBe(true);
    shortcut();
    await ticks(3);
    expect(fillsSent().filter(s => s.tabId === 4).length).toBe(1);
    hangers[0]?.resolve({ outcomes: [{ label: "Note", filled: true }] });
    await askBg({ type: "fillSettled" }, page(4, 0));
    await ticks(2);
    hangFills = false;
    const retried = await askBg({ type: "fillRun", tabId: 4, overwrite: false }, {});
    expect(retried.replies[0]).toMatchObject({ asked: 1 });
    const still = await askBg({ type: "fillRun", tabId: 8, overwrite: true }, {});
    expect(still.replies).toEqual([{ error: BUSY }]);
    expect(isFillReply(still.replies[0])).toBe(false);
    expect(fillsSent().filter(s => s.tabId === 8).length).toBe(1);
    for (const h of hangers.splice(0)) h.resolve({ outcomes: [{ label: "Note", filled: true }] });
    await a;
    await b;
  });

  test("removing the tab clears a fill that never settles, including one whose port already closed", async () => {
    installFastBudget();
    hangFills = true;
    activeTabs = [{ id: 6, url: "https://a.example/" }];
    await register(6, 0);
    tabSent.length = 0;
    shortcut();
    await untilFills(1);
    await ticks(6);
    shortcut();
    await ticks(2);
    expect(fillsSent().length).toBe(1);
    for (const cb of removed) cb(6);
    await ticks(2);
    activeTabs = [{ id: 6, url: "https://a.example/next" }];
    await register(6, 0);
    tabSent.length = 0;
    hangFills = false;
    portClosed = new Set([0]);
    shortcut();
    await untilFills(1);
    expect(fillsSent().length).toBe(1);
    for (const cb of removed) cb(6);
    portClosed = new Set();
    activeTabs = [{ id: 6, url: "https://a.example/after" }];
    await register(6, 0);
    tabSent.length = 0;
    shortcut();
    await untilFills(1);
    expect(fillsSent().length).toBe(1);
  });

  test("a missing listener does not wedge the gate, duplicate frame ids are asked once, and a query failure claims nothing", async () => {
    activeTabs = [{ id: 9, url: "https://a.example/" }];
    await register(9, 0);
    await register(9, 3);
    noReceiver = new Set([0, 3]);
    injectFails = true;
    tabSent.length = 0;
    const failed = await askBg({ type: "fillRun", tabId: 9 }, {});
    expect(failed.replies[0]).toEqual({ error: "cannot inject" });
    expect(isFillReply(failed.replies[0])).toBe(false);
    injectFails = false;
    noReceiver = new Set();
    probeResults = [
      { frameId: 0, result: true },
      { frameId: 0, result: true },
      { frameId: 3, result: true },
    ];
    tabSent.length = 0;
    const ok = await askBg({ type: "fillRun", tabId: 9, overwrite: false, typeahead: false }, {});
    expect(ok.replies[0]).toMatchObject({ asked: 2 });
    const ids = fillsSent().map(s => s.frameId).sort();
    expect(ids).toEqual([0, 3]);
    await askBg({ type: "fillSettled" }, page(9, 0));
    await askBg({ type: "fillSettled" }, page(9, 3));
    queryThrows = true;
    tabSent.length = 0;
    shortcut();
    await ticks(3);
    expect(fillsSent().length).toBe(0);
    queryThrows = false;
    activeTabs = [{ id: 9, url: "https://a.example/" }];
    shortcut();
    await untilFills(1);
    expect(fillsSent().length).toBeGreaterThan(0);
  });

  test("a nested fillRun from inside the frame send is refused and does not send another fill", async () => {
    hangFills = true;
    activeTabs = [{ id: 4, url: "https://a.example/" }];
    await register(4, 0);
    tabSent.length = 0;
    const nested: unknown[] = [];
    const orig = (g.chrome as { tabs: { sendMessage: (tabId: number, msg: unknown, opts?: { frameId?: number }) => Promise<unknown> } }).tabs.sendMessage;
    (g.chrome as { tabs: { sendMessage: typeof orig } }).tabs.sendMessage = async (tabId, msg, opts) => {
      const frameId = opts?.frameId;
      tabSent.push({ tabId, msg, frameId });
      if ((msg as { type?: string }).type === "fill" && nested.length === 0) {
        listenerBg!({ type: "fillRun", tabId, overwrite: true, typeahead: true }, {}, r => { nested.push(r); });
      }
      return new Promise(resolve => { hangers.push({ resolve }); });
    };
    try {
      const run = askBg({ type: "fillRun", tabId: 4, overwrite: false }, {});
      await untilFills(1);
      await ticks(3);
      expect(nested).toEqual([{ error: BUSY }]);
      expect(isFillReply(nested[0])).toBe(false);
      expect(fillsSent().length).toBe(1);
      hangers.splice(0).forEach(h => h.resolve({ outcomes: [] }));
      await run;
    } finally {
      (g.chrome as { tabs: { sendMessage: typeof orig } }).tabs.sendMessage = orig;
    }
  });
});
