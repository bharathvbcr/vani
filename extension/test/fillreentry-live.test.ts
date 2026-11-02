// The real content-script fill (not a stand-in handler): a second message that arrives after
// bank() has been awaited, and again after a round has written, must not write and must not clear undo.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { FILL_BUSY } from "../src/frames";
import { beginFill, fillRef, undoCount, undoFill } from "../src/fill";
import { isFillReply } from "../src/messages";
import { emptyBank, type Answer, type Bank, type Match } from "../src/types";

const BUSY = FILL_BUSY;
const LABEL = "名前";

type Listener = (msg: unknown, sender: object, sendResponse: (r?: unknown) => void) => boolean;

class Ev {
  constructor(public type: string, public init?: unknown) {}
}

class InputEl {
  nodeType = 1;
  localName = "input";
  isConnected = true;
  attrs: Record<string, string> = { type: "text", "aria-label": LABEL };
  style: Record<string, string> = {};
  handlers = new Map<string, Array<(e: { type: string }) => void>>();
  private v = "";
  ownerDocument!: Doc;
  constructor(doc: Doc) { this.ownerDocument = doc; }
  get type(): string { return "text"; }
  get maxLength(): number { return -1; }
  get value(): string { return this.v; }
  set value(x: string) { this.v = x; }
  get tabIndex(): number { return 0; }
  get parentElement(): InputEl | null { return null; }
  get textContent(): string { return ""; }
  getAttribute(n: string): string | null { return this.attrs[n] ?? null; }
  setAttribute(n: string, v: string): void { this.attrs[n] = v; }
  removeAttribute(n: string): void { delete this.attrs[n]; }
  getRootNode(): Doc { return this.ownerDocument; }
  getClientRects(): unknown[] { return [{}]; }
  matches(sel: string): boolean { return sel.includes("input"); }
  closest(): null { return null; }
  focus(): void { this.ownerDocument.activeElement = this; }
  blur(): void { if (this.ownerDocument.activeElement === this) this.ownerDocument.activeElement = null; }
  dispatchEvent(e: { type: string }): boolean {
    for (const h of this.handlers.get(e.type) ?? []) h(e);
    return true;
  }
  addEventListener(type: string, fn: (e: { type: string }) => void): void {
    const list = this.handlers.get(type) ?? [];
    list.push(fn);
    this.handlers.set(type, list);
  }
}

class Doc {
  nodeType = 9;
  hidden = false;
  visibilityState = "visible";
  title = "";
  activeElement: InputEl | null = null;
  documentElement = { appendChild() {} };
  body: InputEl | null = null;
  defaultView!: View;
  input: InputEl;
  constructor() { this.input = new InputEl(this); }
  addEventListener(): void {}
  removeEventListener(): void {}
  createElement(): InputEl { return new InputEl(this); }
  querySelector(): null { return null; }
  querySelectorAll(sel: string): unknown[] {
    if (sel === "*") return [this.input];
    return [];
  }
}

class View {
  Event = Ev;
  InputEvent = Ev;
  KeyboardEvent = Ev;
  MouseEvent = Ev;
  PointerEvent = Ev;
  HTMLInputElement = InputEl;
  HTMLTextAreaElement = InputEl;
  location = { href: "https://jobs.example/role", hostname: "jobs.example", origin: "https://jobs.example" };
  requestAnimationFrame(fn: () => void): number { fn(); return 1; }
  addEventListener(): void {}
  removeEventListener(): void {}
  getComputedStyle(): { visibility: string } { return { visibility: "visible" }; }
  MutationObserver = class {
    observe(): void {}
    disconnect(): void {}
  };
}

const g = globalThis as Record<string, unknown>;
const savedGlobals = {
  window: g.window,
  document: g.document,
  location: g.location,
  MutationObserver: g.MutationObserver,
  chrome: g.chrome,
};

const doc = new Doc();
const view = new View();
doc.defaultView = view;
doc.input.addEventListener("input", () => {
  if (!reenter) return;
  reenter = false;
  listener!({ type: "fill", overwrite: true }, {}, r => { reenterReplies.push(r); });
});

const answer = (key: string, value: string[]): Answer =>
  ({ key, labels: [LABEL], kind: "text", value, sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: "2026-01-01T00:00:00Z", review: false });

function bankWith(value: string[], hosts: string[] = []): Bank {
  const b = emptyBank();
  b.settings.disabledHosts = hosts;
  const a = answer(LABEL, value);
  b.answers[LABEL] = a;
  return b;
}

let listener: Listener | undefined;
let bank: Bank = bankWith(["qa"]);
let bankMode: "ok" | "missing" | "disabled" = "ok";
let gate: Promise<void> | null = null;
let openGate = (): void => undefined;
let heldBanks = 0;
let settled = 0;
let breakStar = false;
let reenter = false;
const reenterReplies: unknown[] = [];

function armGate(): void {
  gate = new Promise<void>(r => { openGate = () => { gate = null; r(); }; });
}

function ask(msg: unknown): Promise<{ async: boolean; replies: unknown[] }> {
  const replies: unknown[] = [];
  let done = (): void => undefined;
  const answered = new Promise<void>(r => { done = r; });
  const async = listener!(msg, {}, r => { replies.push(r); done(); });
  return answered.then(() => ({ async, replies }));
}

afterAll(() => {
  g.window = savedGlobals.window;
  g.document = savedGlobals.document;
  g.location = savedGlobals.location;
  g.MutationObserver = savedGlobals.MutationObserver;
  g.chrome = savedGlobals.chrome;
});

beforeAll(async () => {
  g.window = view;
  g.document = doc;
  g.location = view.location;
  g.MutationObserver = view.MutationObserver;
  view.parent = view;
  view.top = view;
  g.chrome = {
    runtime: {
      onMessage: { addListener(cb: Listener) { listener = cb; } },
      sendMessage(msg: { type?: string }) {
        if (msg.type === "getBank") {
          const wait = gate;
          return (async () => {
            if (wait) { heldBanks++; await wait; }
            if (bankMode === "missing") return { error: "no bank" };
            if (bankMode === "disabled") return { bank: bankWith(["qa"], ["jobs.example"]) };
            return { bank };
          })();
        }
        if (msg.type === "fillSettled") { settled++; return Promise.resolve({ ok: true }); }
        if (msg.type === "sameQuestion") return Promise.resolve({ error: "down" });
        return Promise.resolve({ ok: true });
      },
    },
  };
  (view as unknown as { parent: unknown; top: unknown }).parent = view;
  (view as unknown as { top: unknown }).top = view;
  await import("../src/content.ts?livefill");
  await new Promise(r => setTimeout(r, 30));
  await new Promise(r => setTimeout(r, 20));
  expect(typeof listener).toBe("function");
});

async function seedUndo(): Promise<void> {
  beginFill();
  const other = new InputEl(doc);
  const ref = {
    info: { kind: "text" as const, label: "seed", name: "", autocomplete: "", automationId: "", options: [], required: false, value: [] },
    els: [other as unknown as HTMLElement],
    widget: "native" as const,
    manual: "",
  };
  const m: Match = { answer: answer("seed", ["seed"]), via: "label", score: 0.9, value: ["seed"], review: false };
  expect((await fillRef(ref, m)).filled).toBe(true);
  expect(undoCount()).toBe(1);
}

describe("real content fill reentrancy", () => {
  test("a second fill after bank() is awaited does not clear undo or write, and a burst is not an empty outcome list", async () => {
    doc.input.value = "";
    bank = bankWith(["qa"]);
    bankMode = "ok";
    await seedUndo();
    const seeded = undoCount();
    const heldAt = heldBanks;
    armGate();
    const first = ask({ type: "fill", overwrite: false, typeahead: false });
    for (let i = 0; i < 40 && heldBanks === heldAt; i++) await new Promise(r => setTimeout(r, 5));
    expect(heldBanks).toBeGreaterThan(heldAt);
    const burst = await Promise.all(Array.from({ length: 24 }, (_, n) => ask({ type: "fill", overwrite: n % 2 === 0, typeahead: n % 3 === 0 })));
    expect(undoCount()).toBe(seeded);
    expect(doc.input.value).toBe("");
    for (const b of burst) {
      expect(b.async).toBe(false);
      expect(b.replies).toEqual([{ error: BUSY }]);
      expect(isFillReply(b.replies[0])).toBe(false);
    }
    const settledBefore = settled;
    openGate();
    let attacked = false;
    const attack = (async () => {
      for (let i = 0; i < 80; i++) {
        if (doc.input.value === "qa" && undoCount() === 1) {
          const mid = await ask({ type: "fill", overwrite: true });
          expect(mid.replies).toEqual([{ error: BUSY }]);
          expect(isFillReply(mid.replies[0])).toBe(false);
          expect(doc.input.value).toBe("qa");
          expect(undoCount()).toBe(1);
          attacked = true;
          return;
        }
        await new Promise(r => setTimeout(r, 5));
      }
    })();
    const done = await first;
    await attack;
    expect(attacked).toBe(true);
    expect(done.replies[0]).toMatchObject({ outcomes: [{ label: LABEL, filled: true }] });
    expect(isFillReply(done.replies[0])).toBe(true);
    expect(settled).toBe(settledBefore + 1);
    expect(doc.input.value).toBe("qa");
    expect(await undoFill()).toMatchObject({ restored: 1 });
    expect(doc.input.value).toBe("");
    expect(undoCount()).toBe(0);
  });

  test("an input handler cannot start a second fill while the first is writing", async () => {
    doc.input.value = "";
    bankMode = "ok";
    reenterReplies.length = 0;
    reenter = true;
    const heldAt = heldBanks;
    armGate();
    const first = ask({ type: "fill" });
    for (let i = 0; i < 40 && heldBanks === heldAt; i++) await new Promise(r => setTimeout(r, 5));
    expect(heldBanks).toBeGreaterThan(heldAt);
    openGate();
    const done = await first;
    expect(reenterReplies.every(r => !isFillReply(r) && (r as { error?: string }).error === BUSY)).toBe(true);
    expect(reenterReplies.length).toBeGreaterThan(0);
    reenter = false;
    expect(done.replies[0]).toMatchObject({ outcomes: [{ filled: true }] });
    expect(doc.input.value).toBe("qa");
    expect(await undoFill()).toMatchObject({ restored: 1 });
  });

  test("a missing bank and a disabled host release the lock without clearing the previous undo", async () => {
    for (const mode of ["missing", "disabled"] as const) {
      doc.input.value = "";
      bankMode = mode;
      await seedUndo();
      const heldAt = heldBanks;
      armGate();
      const first = ask({ type: "fill", overwrite: true });
      for (let i = 0; i < 40 && heldBanks === heldAt; i++) await new Promise(r => setTimeout(r, 5));
      expect(heldBanks).toBeGreaterThan(heldAt);
      const second = await ask({ type: "fill", overwrite: true });
      expect(second.replies).toEqual([{ error: BUSY }]);
      expect(undoCount()).toBe(1);
      expect(doc.input.value).toBe("");
      openGate();
      const done = await first;
      expect(done.replies).toEqual([{ outcomes: [] }]);
      expect(isFillReply(done.replies[0])).toBe(true);
      expect(undoCount()).toBe(1);
      expect(doc.input.value).toBe("");
      const third = await ask({ type: "fill" });
      expect(third.replies).toEqual([{ outcomes: [] }]);
      expect(undoCount()).toBe(1);
      expect(await undoFill()).toMatchObject({ restored: 1 });
    }
  });

  test("a fill that throws after beginFill releases the lock and a later fill writes", async () => {
    doc.input.value = "";
    bankMode = "ok";
    breakStar = false;
    const orig = doc.querySelectorAll.bind(doc);
    doc.querySelectorAll = (sel: string) => {
      if (breakStar && sel === "*") throw new Error("scan broke");
      return orig(sel);
    };
    const heldAt = heldBanks;
    armGate();
    const first = ask({ type: "fill", overwrite: true });
    for (let i = 0; i < 40 && heldBanks === heldAt; i++) await new Promise(r => setTimeout(r, 5));
    expect(heldBanks).toBeGreaterThan(heldAt);
    const second = await ask({ type: "fill", overwrite: true });
    expect(second.replies).toEqual([{ error: BUSY }]);
    breakStar = true;
    openGate();
    const done = await first;
    breakStar = false;
    doc.querySelectorAll = orig;
    expect(done.replies).toEqual([{ error: "scan broke" }]);
    const third = await ask({ type: "fill", overwrite: true });
    expect(third.replies[0]).toMatchObject({ outcomes: [{ filled: true }] });
    expect(doc.input.value).toBe("qa");
    expect(await undoFill()).toMatchObject({ restored: 1 });
    expect(doc.input.value).toBe("");
  });
});
