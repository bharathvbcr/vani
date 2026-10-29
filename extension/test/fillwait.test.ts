import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import type { Answer, FieldInfo, Match } from "../src/types";
import { emptyBank } from "../src/types";
import { type FieldRef, filledBy, readRefValue } from "../src/extract";
import { KEPT_NOT, beginFill, clearMarks, fillAll, fillRef, insertDraft, undoCount, undoFill, waitFor, widgetRoots } from "../src/fill";
import { coverageOf } from "../src/coverage";

// bun test has no DOM. These tests drive fill.ts against a small fake just big enough for it: elements
// with attributes, text and children, a document, a tree walker, events, and a MutationObserver whose
// batches the test triggers. Real-browser behaviour is checked by dom-runner.ts (needs Chrome).

// --- fake DOM -------------------------------------------------------------------------------------

type Handler = (e: FEvent) => void;
class FEvent {
  type: string;
  /** Set on the `input` event the typed-in retry sends, as a browser's InputEvent carries it. */
  inputType?: string;
  constructor(type: string, init?: { inputType?: string }) { this.type = type; this.inputType = init?.inputType; }
}

class FakeMO {
  static live = new Set<FakeMO>();
  static created = 0;
  target: unknown;
  constructor(private cb: () => void) { FakeMO.created++; }
  observe(target?: unknown): void { this.target = target; FakeMO.live.add(this); }
  disconnect(): void { FakeMO.live.delete(this); }
  deliver(): void { if (FakeMO.live.has(this)) this.cb(); }
}
/** One mutation batch, delivered as a microtask to every connected observer (of `root`'s only, when given), as the browser does. */
function mutate(root?: unknown): void {
  for (const o of [...FakeMO.live]) if (root === undefined || o.target === root) queueMicrotask(() => o.deliver());
}

class FakeDoc {
  nodeType = 9;
  body = new FakeEl(this, "body");
  defaultView = {
    Event: FEvent, InputEvent: FEvent, PointerEvent: FEvent, MouseEvent: FEvent, KeyboardEvent: FEvent, MutationObserver: FakeMO,
    HTMLInputElement: class {}, HTMLTextAreaElement: class {},
  };
  ids = new Map<string, FakeEl>();
  /** A hidden tab paints no frames: Fill's settle takes one event-loop turn there instead. */
  hidden = false;
  activeElement: FakeEl | null = null;
  getElementById(id: string): FakeEl | null { return this.ids.get(id) ?? null; }
  querySelectorAll(sel: string): FakeEl[] { return this.body.querySelectorAll(sel); }
  createTreeWalker(root: FakeEl, _show: number, filter: { acceptNode(n: unknown): number }) {
    const queue: Array<{ nodeType: number; data: string }> = [];
    const walk = (el: FakeEl): void => {
      if (el.text) queue.push({ nodeType: 3, data: el.text });
      for (const c of el.children) if (filter.acceptNode(c) !== 2) walk(c);
    };
    walk(root);
    let i = 0;
    return { nextNode: () => queue[i++] ?? null };
  }
}

class FakeEl {
  nodeType = 1;
  parent: FakeEl | null = null;
  children: FakeEl[] = [];
  attrs: Record<string, string> = {};
  style: Record<string, string> = {};
  handlers = new Map<string, Handler[]>();
  isConnected = true;
  rendered = true;
  boom = false;
  constructor(public ownerDocument: FakeDoc, public localName: string, public text = "") {}
  get parentElement(): FakeEl | null { return this.parent; }
  get textContent(): string { return this.text + this.children.map(c => c.textContent).join(""); }
  append(c: FakeEl): FakeEl { c.parent = this; this.children.push(c); return c; }
  getAttribute(n: string): string | null { return this.attrs[n] ?? null; }
  setAttribute(n: string, v: string): void { this.attrs[n] = v; }
  removeAttribute(n: string): void { delete this.attrs[n]; }
  getRootNode(): FakeDoc { return this.ownerDocument; }
  getClientRects(): unknown[] { return this.rendered ? [{}] : []; }
  /** Every focus() and blur() call, with the options focus() was given: what the page could feel of them. */
  focusCalls: Array<{ options?: { preventScroll?: boolean } }> = [];
  blurCalls = 0;
  focus(options?: { preventScroll?: boolean }): void { this.focusCalls.push({ options }); this.ownerDocument.activeElement = this; }
  blur(): void { this.blurCalls++; if (this.ownerDocument.activeElement === this) this.ownerDocument.activeElement = null; }
  matches(): boolean { return false; }
  closest(): FakeEl | null { return null; }
  querySelector(sel: string): FakeEl | null { return this.querySelectorAll(sel)[0] ?? null; }
  querySelectorAll(sel: string): FakeEl[] {
    if (this.boom) throw new Error("page script threw");
    const want = sel === "[role=option]" ? "any" : sel === "[role=option][aria-selected=true]" ? "selected" : null;
    if (!want) return [];
    const out: FakeEl[] = [];
    const walk = (el: FakeEl): void => {
      for (const c of el.children) {
        if (c.attrs["role"] === "option" && (want === "any" || c.attrs["aria-selected"] === "true")) out.push(c);
        walk(c);
      }
    };
    walk(this);
    return out;
  }
  on(type: string, h: Handler): void { this.handlers.set(type, [...(this.handlers.get(type) ?? []), h]); }
  dispatchEvent(e: FEvent): boolean { for (const h of this.handlers.get(e.type) ?? []) h(e); return true; }
}

class FakeInput extends FakeEl {
  private v = "";
  constructor(doc: FakeDoc, public onWrite: () => void, tag = "input") { super(doc, tag); if (tag === "input") this.attrs["type"] = "text"; }
  get value(): string { return this.v; }
  set value(x: string) { this.v = x; this.onWrite(); }
  get type(): string { return this.localName === "textarea" ? "textarea" : this.attrs["type"] ?? "text"; }
  /** The browser's reflection of the attribute: its leading digits, else -1 (absent or not a number). */
  get maxLength(): number { const m = /^\s*\+?(\d+)/.exec(this.attrs["maxlength"] ?? ""); return m ? Number(m[1]) : -1; }
}

const later = (ms: number, fn: () => void): void => { if (ms <= 0) fn(); else setTimeout(fn, ms); };

interface ComboOpts {
  /** ms after the click until the list is on screen; null: never. */
  openAfter: number | null;
  /** What choosing an option does: show it after `ms`, or close the list showing nothing. */
  pick: { shows: number } | "vanish";
  /** Make the page script throw this long after the click. */
  boomAfter?: number;
  onPick?: () => void;
}

function combo(doc: FakeDoc, id: string, options: string[], o: ComboOpts): { trigger: FakeEl; list: FakeEl; opts: FakeEl[] } {
  const trigger = doc.body.append(new FakeEl(doc, "button"));
  trigger.attrs["role"] = "combobox";
  trigger.attrs["aria-controls"] = id;
  const list = doc.body.append(new FakeEl(doc, "div"));
  list.attrs["role"] = "listbox";
  doc.ids.set(id, list);
  const opts = options.map(t => {
    const opt = list.append(new FakeEl(doc, "div", t));
    opt.attrs["role"] = "option";
    opt.rendered = false;
    opt.on("click", () => {
      o.onPick?.();
      if (o.pick === "vanish") { for (const x of opts) x.rendered = false; mutate(); return; }
      later(o.pick.shows, () => { opt.attrs["aria-selected"] = "true"; mutate(); });
    });
    return opt;
  });
  trigger.on("click", () => {
    if (o.openAfter !== null) later(o.openAfter, () => { for (const x of opts) x.rendered = true; mutate(); });
    if (o.boomAfter !== undefined) later(o.boomAfter, () => { list.boom = true; mutate(); });
  });
  return { trigger, list, opts };
}

// --- helpers ----------------------------------------------------------------------------------------

const info = (label: string, kind: FieldInfo["kind"] = "select", multi = false): FieldInfo =>
  ({ kind, label, name: "", autocomplete: "", automationId: "", options: [], required: false, value: [], multi });
const answer = (key: string, value: string[]): Answer =>
  ({ key, labels: [], kind: "text", value, sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: "2025-01-01T00:00:00Z", review: false });
const match = (key: string, value: string[]): Match => ({ answer: answer(key, value), via: "label", score: 0.9, value, review: false });
const ref = (el: FakeEl, label: string, widget: FieldRef["widget"], kind: FieldInfo["kind"] = "select"): FieldRef =>
  ({ info: info(label, kind), els: [el as unknown as HTMLElement], widget, manual: "" });

/** Timers created and not yet fired or cleared, so a leak is a number the test can assert on. */
const timers = new Set<unknown>();
const realSet = globalThis.setTimeout, realClear = globalThis.clearTimeout;
function trackTimers(): void {
  globalThis.setTimeout = ((fn: () => void, ms?: number) => {
    const h = realSet(() => { timers.delete(h); fn(); }, ms);
    timers.add(h);
    return h;
  }) as typeof setTimeout;
  globalThis.clearTimeout = ((h: Parameters<typeof clearTimeout>[0]) => { timers.delete(h); realClear(h); }) as typeof clearTimeout;
}

const saved: Record<string, unknown> = {};
beforeAll(() => {
  saved["Node"] = (globalThis as Record<string, unknown>)["Node"];
  saved["NodeFilter"] = (globalThis as Record<string, unknown>)["NodeFilter"];
  Object.assign(globalThis, {
    Node: { ELEMENT_NODE: 1 },
    NodeFilter: { SHOW_ELEMENT: 1, SHOW_TEXT: 4, SHOW_CDATA_SECTION: 8, FILTER_ACCEPT: 1, FILTER_REJECT: 2 },
  });
  trackTimers();
});
afterAll(() => {
  Object.assign(globalThis, saved);
  globalThis.setTimeout = realSet;
  globalThis.clearTimeout = realClear;
});

function fresh(): void { FakeMO.live.clear(); FakeMO.created = 0; timers.clear(); }
const nothingLeft = (): void => { expect(FakeMO.live.size).toBe(0); expect(timers.size).toBe(0); };
const shownValue = (c: { opts: FakeEl[] }): string[] => c.opts.filter(o => o.attrs["aria-selected"] === "true").map(o => o.text);

// --- waitFor ----------------------------------------------------------------------------------------

describe("waitFor", () => {
  test("a condition already true resolves at once with no observer and no timer", async () => {
    fresh();
    const doc = new FakeDoc();
    expect(await waitFor(() => true, 600, doc as unknown as Node)).toBe(true);
    expect(FakeMO.created).toBe(0);
    nothingLeft();
  });

  test("resolves on the first mutation batch that makes it true, well before the 100 ms fallback tick", async () => {
    fresh();
    const doc = new FakeDoc();
    let ready = false;
    setTimeout(() => { ready = true; mutate(); }, 20);
    const t0 = performance.now();
    expect(await waitFor(() => ready, 600, doc as unknown as Node)).toBe(true);
    expect(performance.now() - t0).toBeLessThan(90);
    nothingLeft();
  });

  test("a change no mutation announces is still found by the fallback tick", async () => {
    fresh();
    const doc = new FakeDoc();
    let ready = false;
    setTimeout(() => { ready = true; }, 30);
    const t0 = performance.now();
    expect(await waitFor(() => ready, 600, doc as unknown as Node)).toBe(true);
    const took = performance.now() - t0;
    expect(took).toBeGreaterThanOrEqual(90);
    expect(took).toBeLessThan(250);
    nothingLeft();
  });

  test("gives up false at the ceiling, and releases the observer and timers", async () => {
    fresh();
    const doc = new FakeDoc();
    const t0 = performance.now();
    expect(await waitFor(() => false, 200, doc as unknown as Node)).toBe(false);
    const took = performance.now() - t0;
    expect(took).toBeGreaterThanOrEqual(190);
    expect(took).toBeLessThan(400);
    expect(FakeMO.created).toBe(1);
    nothingLeft();
  });

  test("a mutation storm that never satisfies it does not extend the ceiling", async () => {
    fresh();
    const doc = new FakeDoc();
    const storm = setInterval(mutate, 5);
    const t0 = performance.now();
    expect(await waitFor(() => false, 150, doc as unknown as Node)).toBe(false);
    clearInterval(storm);
    expect(performance.now() - t0).toBeLessThan(350);
    nothingLeft();
  });

  test("a condition that throws rejects and still releases the observer and timers", async () => {
    fresh();
    const doc = new FakeDoc();
    let calls = 0;
    setTimeout(mutate, 10);
    const p = waitFor(() => { if (++calls > 1) throw new Error("boom"); return false; }, 600, doc as unknown as Node);
    await expect(p).rejects.toThrow("boom");
    nothingLeft();
  });

  test("the condition is checked once more at the ceiling", async () => {
    fresh();
    const doc = new FakeDoc();
    const t0 = performance.now();
    expect(await waitFor(() => performance.now() - t0 >= 60, 60, doc as unknown as Node)).toBe(true);
    nothingLeft();
  });
});

// --- several roots ----------------------------------------------------------------------------------------

/** An open shadow root of `doc`: a node of its own that the document's observer does not see into. */
const shadowOf = (doc: FakeDoc): Node => ({ nodeType: 11, ownerDocument: doc }) as unknown as Node;

describe("waitFor over several roots", () => {
  test("a change inside a shadow root is seen by its own observer, well before the fallback tick", async () => {
    fresh();
    const doc = new FakeDoc();
    const shadow = shadowOf(doc);
    let ready = false;
    setTimeout(() => { ready = true; mutate(shadow); }, 20);
    const t0 = performance.now();
    expect(await waitFor(() => ready, 600, [doc as unknown as Node, shadow])).toBe(true);
    expect(performance.now() - t0).toBeLessThan(90);
    expect(FakeMO.created).toBe(2);
    nothingLeft();
  });

  test("every root's observer is released at the ceiling", async () => {
    fresh();
    const doc = new FakeDoc();
    expect(await waitFor(() => false, 120, [doc as unknown as Node, shadowOf(doc), shadowOf(doc)])).toBe(false);
    expect(FakeMO.created).toBe(3);
    nothingLeft();
  });

  test("every root's observer is released when the condition throws", async () => {
    fresh();
    const doc = new FakeDoc();
    const shadow = shadowOf(doc);
    let calls = 0;
    setTimeout(() => mutate(shadow), 10);
    const p = waitFor(() => { if (++calls > 1) throw new Error("boom"); return false; }, 600, [doc as unknown as Node, shadow]);
    await expect(p).rejects.toThrow("boom");
    nothingLeft();
  });

  test("no more than 64 roots are observed; the fallback tick still finds a change in the rest", async () => {
    fresh();
    const doc = new FakeDoc();
    const roots = [doc as unknown as Node, ...Array.from({ length: 80 }, () => shadowOf(doc))];
    let ready = false;
    setTimeout(() => { ready = true; }, 30);
    expect(await waitFor(() => ready, 600, roots)).toBe(true);
    expect(FakeMO.created).toBe(64);
    nothingLeft();
  });
});

// --- driving a widget ---------------------------------------------------------------------------------

describe("fill waits on the page, not on a clock", () => {
  test("a list that renders 30 ms after the click is chosen long before the 500 ms ceiling", async () => {
    fresh();
    const doc = new FakeDoc();
    const c = combo(doc, "lb1", ["Canada", "United States"], { openAfter: 30, pick: { shows: 0 } });
    const t0 = performance.now();
    const out = await fillRef(ref(c.trigger, "Country", "combobox"), match("country", ["United States"]));
    expect(out.filled).toBe(true);
    expect(shownValue(c)).toEqual(["United States"]);
    expect(performance.now() - t0).toBeLessThan(200);
    nothingLeft();
  });

  test("a verdict that arrives 40 ms after the choice is taken as soon as the widget shows it", async () => {
    fresh();
    const doc = new FakeDoc();
    const c = combo(doc, "lb2", ["Yes", "No"], { openAfter: 0, pick: { shows: 40 } });
    const t0 = performance.now();
    const out = await fillRef(ref(c.trigger, "Relocate", "combobox"), match("relocate", ["Yes"]));
    expect(out.filled).toBe(true);
    expect(shownValue(c)).toEqual(["Yes"]);
    expect(performance.now() - t0).toBeLessThan(200);
    nothingLeft();
  });

  test("a widget that shows the choice at once needs no wait for a verdict", async () => {
    fresh();
    const doc = new FakeDoc();
    const c = combo(doc, "lb3", ["Yes", "No"], { openAfter: 0, pick: { shows: 0 } });
    const out = await fillRef(ref(c.trigger, "Relocate", "combobox"), match("relocate", ["No"]));
    expect(out.filled).toBe(true);
    expect(FakeMO.created).toBe(0);
    nothingLeft();
  });

  test("a widget that never opens is given up on at the ceilings, and left untouched", async () => {
    fresh();
    const doc = new FakeDoc();
    const c = combo(doc, "lb4", ["Yes", "No"], { openAfter: null, pick: { shows: 0 } });
    const t0 = performance.now();
    const out = await fillRef(ref(c.trigger, "Relocate", "combobox"), match("relocate", ["Yes"]));
    const took = performance.now() - t0;
    expect(out.filled).toBe(false);
    expect(out.reason).toBe("manual");
    expect(shownValue(c)).toEqual([]);
    expect(took).toBeGreaterThanOrEqual(750); // 500 ms click ceiling + 300 ms ArrowDown ceiling
    expect(took).toBeLessThan(1300);
    nothingLeft();
  });

  test("options that appear and then vanish without the widget showing a value leave the field untouched", async () => {
    fresh();
    const doc = new FakeDoc();
    const c = combo(doc, "lb5", ["Yes", "No"], { openAfter: 10, pick: "vanish" });
    const out = await fillRef(ref(c.trigger, "Relocate", "combobox"), match("relocate", ["Yes"]));
    expect(out.filled).toBe(false);
    expect(out.reason).toBe("manual");
    expect(shownValue(c)).toEqual([]);
    nothingLeft();
  });

  test("a page script that throws mid-wait is reported manual and nothing is left running", async () => {
    fresh();
    const doc = new FakeDoc();
    const c = combo(doc, "lb6", ["Yes", "No"], { openAfter: null, pick: { shows: 0 }, boomAfter: 30 });
    const out = await fillRef(ref(c.trigger, "Relocate", "combobox"), match("relocate", ["Yes"]));
    expect(out.filled).toBe(false);
    expect(out.reason).toBe("manual");
    nothingLeft();
  });

  test("an aria checkbox that never flips gives up at its ceiling with the observer released", async () => {
    fresh();
    const doc = new FakeDoc();
    const box = doc.body.append(new FakeEl(doc, "div"));
    box.attrs["role"] = "checkbox";
    box.attrs["aria-checked"] = "false";
    const r = ref(box, "Agree", "aria-checkbox", "checkbox");
    const t0 = performance.now();
    const out = await fillRef(r, match("agree", ["yes"]));
    expect(out.filled).toBe(false);
    expect(performance.now() - t0).toBeLessThan(800);
    nothingLeft();
  });

  test("an aria checkbox that flips when clicked is filled at once", async () => {
    fresh();
    const doc = new FakeDoc();
    const box = doc.body.append(new FakeEl(doc, "div"));
    box.attrs["role"] = "checkbox";
    box.attrs["aria-checked"] = "false";
    box.on("click", () => { box.attrs["aria-checked"] = "true"; });
    const out = await fillRef(ref(box, "Agree", "aria-checkbox", "checkbox"), match("agree", ["yes"]));
    expect(out.filled).toBe(true);
    nothingLeft();
  });

  test("a combobox that already shows a value is not overwritten", async () => {
    fresh();
    const doc = new FakeDoc();
    const c = combo(doc, "lb7", ["Yes", "No"], { openAfter: 0, pick: { shows: 0 } });
    c.opts[1]!.attrs["aria-selected"] = "true";
    const out = await fillRef(ref(c.trigger, "Relocate", "combobox"), match("relocate", ["Yes"]));
    expect(out).toMatchObject({ filled: false, reason: "has_value" });
    expect(shownValue(c)).toEqual(["No"]);
    expect(FakeMO.created).toBe(0);
  });
});

// --- ordering -------------------------------------------------------------------------------------------

describe("fillAll order", () => {
  test("plain fields are filled before comboboxes, outcomes come back in the original order", async () => {
    fresh();
    const doc = new FakeDoc();
    const log: string[] = [];
    const text = (label: string): FieldRef => {
      const el = new FakeInput(doc, () => log.push(label));
      doc.body.append(el);
      return ref(el, label, "native", "text");
    };
    const c1 = combo(doc, "o1", ["Yes", "No"], { openAfter: 0, pick: { shows: 0 }, onPick: () => log.push("Combo A") });
    const c2 = combo(doc, "o2", ["Yes", "No"], { openAfter: 0, pick: { shows: 0 }, onPick: () => log.push("Combo B") });
    const refs = [ref(c1.trigger, "Combo A", "combobox"), text("Text 1"), ref(c2.trigger, "Combo B", "combobox"), text("Text 2")];
    const matches = new Map<FieldRef, Match | null>([
      [refs[0]!, match("a", ["Yes"])], [refs[1]!, match("t1", ["one"])], [refs[2]!, match("b", ["No"])], [refs[3]!, match("t2", ["two"])],
    ]);
    const out = await fillAll(refs, emptyBank(), "x.example", { matches });
    expect(out.map(o => o.label)).toEqual(["Combo A", "Text 1", "Combo B", "Text 2"]);
    expect(out.map(o => o.filled)).toEqual([true, true, true, true]);
    expect(log).toEqual(["Text 1", "Text 2", "Combo A", "Combo B"]);
    nothingLeft();
  });

  test("precomputed matches are reused (a null stays no_match); refs missing from the map are matched against the bank", async () => {
    fresh();
    const doc = new FakeDoc();
    const mk = (label: string): FieldRef => { const el = new FakeInput(doc, () => {}); doc.body.append(el); return ref(el, label, "native", "text"); };
    const a = mk("Pre matched"), b = mk("Pre declined"), c = mk("Colour");
    const bank = emptyBank();
    bank.answers["pre declined"] = answer("pre declined", ["bank would fill this"]);
    bank.answers["colour"] = answer("colour", ["green"]);
    const matches = new Map<FieldRef, Match | null>([[a, match("pre", ["given"])], [b, null]]);
    const out = await fillAll([a, b, c], bank, "x.example", { matches });
    expect(out.map(o => [o.label, o.filled, o.reason])).toEqual([
      ["Pre matched", true, undefined], ["Pre declined", false, "no_match"], ["Colour", true, undefined],
    ]);
    expect((c.els[0] as unknown as FakeInput).value).toBe("green");
    expect((b.els[0] as unknown as FakeInput).value).toBe("");
  });

  test("without precomputed matches fillAll matches from the bank as before", async () => {
    fresh();
    const doc = new FakeDoc();
    const el = new FakeInput(doc, () => {});
    doc.body.append(el);
    const bank = emptyBank();
    bank.answers["colour"] = answer("colour", ["green"]);
    const out = await fillAll([ref(el, "Colour", "native", "text")], bank, "x.example");
    expect(out[0]).toMatchObject({ filled: true, via: "label" });
  });

  test("text already typed is never overwritten", async () => {
    fresh();
    const doc = new FakeDoc();
    const el = new FakeInput(doc, () => {});
    doc.body.append(el);
    el.value = "mine";
    const out = await fillRef(ref(el, "Colour", "native", "text"), match("colour", ["green"]));
    expect(out).toMatchObject({ filled: false, reason: "has_value" });
    expect(el.value).toBe("mine");
  });
});

describe("coverageOf exposes its matches", () => {
  test("the map holds each matched or unmatched ref's match, and fillAll uses it without matching again", async () => {
    fresh();
    const doc = new FakeDoc();
    const mk = (label: string): FieldRef => { const el = new FakeInput(doc, () => {}); doc.body.append(el); return ref(el, label, "native", "text"); };
    const known = mk("Colour"), unknown = mk("Shoe size");
    const bank = emptyBank();
    bank.answers["colour"] = answer("colour", ["green"]);
    const matches = new Map<FieldRef, Match | null>();
    const refs = [known, unknown];
    const cov = coverageOf(refs, bank, "x.example", undefined, matches);
    expect(cov).toMatchObject({ fillable: 1, unknown: 1 });
    expect(matches.get(known)?.answer.key).toBe("colour");
    expect(matches.get(unknown)).toBeNull();
    expect(matches.size).toBe(2);
    // The bank is now empty: only the passed matches can produce the fill.
    const out = await fillAll(refs, emptyBank(), "x.example", { matches });
    expect(out.map(o => o.filled)).toEqual([true, false]);
    expect((known.els[0] as unknown as FakeInput).value).toBe("green");
  });
});

/** A contenteditable editor: text is its textContent, and it has no maxlength concept. */
class FakeEditable extends FakeEl {
  constructor(doc: FakeDoc) { super(doc, "div"); this.attrs["contenteditable"] = "true"; }
  get textContent(): string { return this.text; }
  set textContent(t: string) { this.text = t; }
}

// The browser enforces maxlength only for typing; a script's `.value =` is not cut. A saved answer longer than the
// field's limit is therefore left out of the field (never truncated, never written past the limit, the saved answer
// untouched) and the field is outlined yellow with the reason, so nobody believes it was answered.
describe("a field's character limit (maxlength)", () => {
  const YELLOW = "#d4a017", GREEN = "#2e8b57";
  const mkInput = (attrs: Record<string, string> = {}, tag = "input"): FakeInput => {
    const el = new FakeInput(new FakeDoc(), () => {}, tag);
    Object.assign(el.attrs, attrs);
    return el;
  };
  const kindOf = (el: FakeInput): FieldInfo["kind"] => (el.localName === "textarea" ? "textarea" : "text");
  const fillWith = (el: FakeInput, value: string, opts: { overwrite?: boolean } = {}) =>
    fillRef(ref(el, "About you", "native", kindOf(el)), match("about", [value]), opts);

  // Characterization: these held before the limit check existed and must keep holding.
  test("a field with no maxlength takes a long answer verbatim", async () => {
    fresh();
    const el = mkInput();
    const long = "x".repeat(5000);
    expect(await fillWith(el, long)).toMatchObject({ filled: true, review: false });
    expect(el.value).toBe(long);
    expect(el.style["outline"]).toContain(GREEN);
  });
  test("a malformed or negative maxlength is no limit", async () => {
    fresh();
    for (const bad of ["abc", "-1", "", "-5"]) {
      const el = mkInput({ maxlength: bad });
      expect(await fillWith(el, "twelve chars")).toMatchObject({ filled: true });
      expect(el.value).toBe("twelve chars");
    }
  });

  test("an answer longer than the limit is left out: not written, not cut, outlined yellow with the limit and the length", async () => {
    fresh();
    const el = mkInput({ maxlength: "5" });
    const m = match("about", ["abcdefghij"]);
    const out = await fillRef(ref(el, "About you", "native", "text"), m);
    expect(out).toMatchObject({ filled: false, reason: "too_long", key: "about" });
    expect(out.detail).toContain("10");
    expect(out.detail).toContain("5");
    expect(el.value).toBe("");
    expect(el.style["outline"]).toContain(YELLOW);
    expect(el.attrs["data-vani-fill"]).toBe("review");
    expect(el.attrs["data-vani-why"]).toBe(out.detail!);
    expect(m.value).toEqual(["abcdefghij"]);
    expect(m.answer.value).toEqual(["abcdefghij"]);
  });
  test("the on-page reason says what to do", async () => {
    fresh();
    const el = mkInput({ maxlength: "5" });
    const out = await fillWith(el, "abcdefghij");
    expect(out.detail).toMatch(/shorten/i);
    expect(out.detail).toMatch(/by hand/i);
  });
  test("an answer exactly at the limit is filled, green", async () => {
    fresh();
    const el = mkInput({ maxlength: "5" });
    expect(await fillWith(el, "abcde")).toMatchObject({ filled: true, review: false });
    expect(el.value).toBe("abcde");
    expect(el.style["outline"]).toContain(GREEN);
  });
  test("one over the limit is refused", async () => {
    fresh();
    const el = mkInput({ maxlength: "5" });
    expect(await fillWith(el, "abcdef")).toMatchObject({ filled: false, reason: "too_long" });
    expect(el.value).toBe("");
  });
  test("maxlength 0 refuses any answer", async () => {
    fresh();
    const el = mkInput({ maxlength: "0" });
    expect(await fillWith(el, "x")).toMatchObject({ filled: false, reason: "too_long" });
    expect(el.value).toBe("");
  });
  test("maxlength -1 (the property's value for no limit) is no limit", async () => {
    fresh();
    const el = mkInput({ maxlength: "-1" });
    expect(el.maxLength).toBe(-1);
    expect(await fillWith(el, "any length at all")).toMatchObject({ filled: true });
  });
  test("a textarea is held to its limit the same way", async () => {
    fresh();
    const over = mkInput({ maxlength: "10" }, "textarea");
    expect(await fillWith(over, "x".repeat(11))).toMatchObject({ filled: false, reason: "too_long" });
    expect(over.value).toBe("");
    const at = mkInput({ maxlength: "10" }, "textarea");
    expect(await fillWith(at, "x".repeat(10))).toMatchObject({ filled: true });
    expect(at.value).toHaveLength(10);
  });
  test("a textarea counts a line break as the browser stores it, one character", async () => {
    fresh();
    const el = mkInput({ maxlength: "3" }, "textarea");
    expect(await fillWith(el, "a\r\nb")).toMatchObject({ filled: true });
    const over = mkInput({ maxlength: "3" }, "textarea");
    expect(await fillWith(over, "a\r\nbc")).toMatchObject({ filled: false, reason: "too_long" });
  });
  test("a single-line input counts the answer without the line breaks it would drop", async () => {
    fresh();
    const el = mkInput({ maxlength: "3" });
    expect(await fillWith(el, "a\nb\r\nc")).toMatchObject({ filled: true });
  });
  test("types the browser does not apply maxlength to are not limited", async () => {
    fresh();
    const num = mkInput({ type: "number", maxlength: "2" });
    expect(await fillWith(num, "12345")).toMatchObject({ filled: true });
    const email = mkInput({ type: "email", maxlength: "4" });
    expect(await fillWith(email, "a@example.com")).toMatchObject({ filled: false, reason: "too_long" });
    const tel = mkInput({ type: "tel", maxlength: "4" });
    expect(await fillWith(tel, "12345")).toMatchObject({ filled: false, reason: "too_long" });
  });

  test("the limit is read when the field is filled: a maxlength that changes after render is honoured", async () => {
    fresh();
    const el = mkInput({ maxlength: "100" });
    const r = ref(el, "About you", "native", "text");
    el.attrs["maxlength"] = "3";
    expect(await fillRef(r, match("about", ["abcd"]))).toMatchObject({ filled: false, reason: "too_long" });
    expect(el.value).toBe("");
    el.attrs["maxlength"] = "4";
    expect(await fillRef(r, match("about", ["abcd"]))).toMatchObject({ filled: true });
    expect(el.value).toBe("abcd");
    const gone = mkInput({ maxlength: "3" });
    delete gone.attrs["maxlength"];
    expect(await fillWith(gone, "abcdefgh")).toMatchObject({ filled: true });
  });
  test("a control with no maxLength property is read from the attribute", async () => {
    fresh();
    const el = mkInput({ maxlength: "3" });
    Object.defineProperty(el, "maxLength", { value: undefined });
    expect(await fillWith(el, "abcd")).toMatchObject({ filled: false, reason: "too_long" });
    expect(await fillWith(el, "abc")).toMatchObject({ filled: true });
  });

  test("length is counted in UTF-16 code units, so a surrogate pair is two", async () => {
    fresh();
    const emoji = "\u{1F600}";
    expect(emoji).toHaveLength(2);
    const fits = mkInput({ maxlength: "3" });
    expect(await fillWith(fits, `a${emoji}`)).toMatchObject({ filled: true });
    expect(fits.value).toBe(`a${emoji}`);
    const over = mkInput({ maxlength: "3" });
    expect(await fillWith(over, `ab${emoji}`)).toMatchObject({ filled: false, reason: "too_long" });
    expect(over.value).toBe(""); // not cut to "ab" + half a pair
    const lone = mkInput({ maxlength: "1" });
    expect(await fillWith(lone, emoji)).toMatchObject({ filled: false, reason: "too_long" });
    expect(lone.value).toBe("");
  });
  test("a combining mark is its own code unit; the precomposed letter is one", async () => {
    fresh();
    const decomposed = "é", precomposed = "é";
    const one = mkInput({ maxlength: "1" });
    expect(await fillWith(one, decomposed)).toMatchObject({ filled: false, reason: "too_long" });
    const pre = mkInput({ maxlength: "1" });
    expect(await fillWith(pre, precomposed)).toMatchObject({ filled: true });
    const two = mkInput({ maxlength: "2" });
    expect(await fillWith(two, decomposed)).toMatchObject({ filled: true });
    expect(two.value).toBe(decomposed);
  });

  test("text already typed is kept: has_value wins, with or without a limit it would break", async () => {
    fresh();
    const el = mkInput({ maxlength: "5" });
    el.value = "mine";
    expect(await fillWith(el, "abcdefghij")).toMatchObject({ filled: false, reason: "has_value" });
    expect(el.value).toBe("mine");
    expect(el.attrs["data-vani-fill"]).toBeUndefined();
  });
  test("overwrite with an answer that is too long leaves what was typed in place", async () => {
    fresh();
    const el = mkInput({ maxlength: "5" });
    el.value = "mine";
    expect(await fillWith(el, "abcdefghij", { overwrite: true })).toMatchObject({ filled: false, reason: "too_long" });
    expect(el.value).toBe("mine");
    expect(await fillWith(el, "abcde", { overwrite: true })).toMatchObject({ filled: true });
    expect(el.value).toBe("abcde");
  });

  test("a contenteditable has no maxlength concept: a long answer fills it as before", async () => {
    fresh();
    const doc = new FakeDoc();
    const ed = new FakeEditable(doc);
    ed.attrs["maxlength"] = "3";
    const out = await fillRef(ref(ed, "About you", "contenteditable", "text"), match("about", ["a much longer answer"]));
    expect(out).toMatchObject({ filled: true });
    expect(ed.textContent).toBe("a much longer answer");
    expect(ed.style["outline"]).toContain(GREEN);
  });

  test("a split phone writes neither box when either part will not fit", async () => {
    fresh();
    const doc = new FakeDoc();
    const split = (codeMax: string | null, numMax: string | null): { r: FieldRef; code: FakeInput; num: FakeInput } => {
      const code = new FakeInput(doc, () => {}), num = new FakeInput(doc, () => {});
      if (codeMax !== null) code.attrs["maxlength"] = codeMax;
      if (numMax !== null) num.attrs["maxlength"] = numMax;
      const r: FieldRef = { info: info("Phone", "tel"), els: [code as unknown as HTMLElement, num as unknown as HTMLElement], widget: "split-phone", manual: "", parts: ["code", "number"] };
      return { r, code, num };
    };
    const saved = match("phone", ["+1 5550100"]);
    const tight = split("3", "4");
    expect(await fillRef(tight.r, saved)).toMatchObject({ filled: false, reason: "too_long" });
    expect(tight.code.attrs["data-vani-fill"]).toBe("review");
    expect(tight.code.attrs["data-vani-why"]).toMatch(/allows 4/);
    expect([tight.code.value, tight.num.value]).toEqual(["", ""]);
    const tightCode = split("1", null);
    expect(await fillRef(tightCode.r, saved)).toMatchObject({ filled: false, reason: "too_long" });
    expect([tightCode.code.value, tightCode.num.value]).toEqual(["", ""]);
    const roomy = split("3", "7");
    expect(await fillRef(roomy.r, saved)).toMatchObject({ filled: true });
    expect([roomy.code.value, roomy.num.value]).toEqual(["+1", "5550100"]);
  });
  test("a split date part that will not fit its box is left empty and the fill is flagged for review", async () => {
    fresh();
    const doc = new FakeDoc();
    const [mm, dd, yy] = [new FakeInput(doc, () => {}), new FakeInput(doc, () => {}), new FakeInput(doc, () => {})];
    yy.attrs["maxlength"] = "2";
    const r: FieldRef = { info: info("Start date", "text"), els: [mm, dd, yy] as unknown as HTMLElement[], widget: "split-date", manual: "", parts: ["month", "day", "year"] };
    expect(await fillRef(r, match("start", ["2026-10-05"]))).toMatchObject({ filled: true, review: true });
    expect([mm.value, dd.value, yy.value]).toEqual(["10", "05", ""]);
    expect(mm.attrs["data-vani-fill"]).toBe("review");
    expect(mm.attrs["data-vani-why"]).toMatch(/allows 2/);
  });
  test("a split date whose every part is too long is reported too_long, not as a missing option", async () => {
    fresh();
    const doc = new FakeDoc();
    const [mm, dd] = [new FakeInput(doc, () => {}), new FakeInput(doc, () => {})];
    mm.attrs["maxlength"] = "1";
    dd.attrs["maxlength"] = "1";
    const r: FieldRef = { info: info("Start date", "text"), els: [mm, dd] as unknown as HTMLElement[], widget: "split-date", manual: "", parts: ["month", "day"] };
    const out = await fillRef(r, match("start", ["2026-10-05"]));
    expect(out).toMatchObject({ filled: false, reason: "too_long" });
    expect(out.detail).toMatch(/shorten/i);
    expect([mm.value, dd.value]).toEqual(["", ""]);
  });

  test("fillAll leaves the too-long field to the person and fills the others", async () => {
    fresh();
    const doc = new FakeDoc();
    const short = new FakeInput(doc, () => {}), tight = new FakeInput(doc, () => {});
    tight.attrs["maxlength"] = "4";
    const matches = new Map<FieldRef, Match | null>();
    const a = ref(short, "Colour", "native", "text"), b = ref(tight, "Pet", "native", "text");
    matches.set(a, match("colour", ["green"]));
    matches.set(b, match("pet", ["elephant"]));
    const out = await fillAll([a, b], emptyBank(), "x.example", { matches });
    expect(out.map(o => o.reason ?? "filled")).toEqual(["filled", "too_long"]);
    expect(short.value).toBe("green");
    expect(tight.value).toBe("");
  });

  test("a draft longer than the field allows is not inserted, and the field is not marked", () => {
    fresh();
    const el = mkInput({ maxlength: "5" }, "textarea");
    const r = ref(el, "Why us", "native", "textarea");
    const out = insertDraft(r, "a draft well past the limit");
    expect(out).toMatchObject({ filled: false, reason: "too_long" });
    expect(out.detail).toMatch(/shorten/i);
    expect(el.value).toBe("");
    expect(el.attrs["data-vani-draft"]).toBeUndefined();
    expect(el.attrs["data-vani-fill"]).toBeUndefined();
    expect(insertDraft(r, "fits")).toMatchObject({ filled: true, review: true });
    expect(el.value).toBe("fits");
  });
});

// A React or Angular controlled input can take a write and put its own state back a moment later, so the field looks
// empty while Fill reports it filled. Fill waits a frame after a plain write, confirms the page kept it, tries once the
// other way, and otherwise says "manual", never "filled".
describe("a write the page does not keep", () => {
  type Controlled = { el: FakeInput; writes: Array<string | undefined> };
  /**
   * A controlled input: on `input` it puts back what it held. `when` is how: now (inside the handler), at a microtask, or
   * after 5 ms. `until`: how many input events it reverts (Infinity: always). `onlyUntyped`: it takes a write that
   * arrives as typing (`inputType: insertText`) and puts back any other.
   */
  function controlled(opts: { when: "sync" | "micro" | "later"; until: number; onlyUntyped?: boolean; attrs?: Record<string, string> }): Controlled {
    const doc = new FakeDoc();
    const el = new FakeInput(doc, () => {});
    Object.assign(el.attrs, opts.attrs ?? {});
    doc.body.append(el);
    const writes: Array<string | undefined> = [];
    let seen = 0;
    el.on("input", e => {
      writes.push(e.inputType);
      seen++;
      if (seen > opts.until) return;
      if (opts.onlyUntyped && e.inputType === "insertText") return;
      const back = (): void => { el.value = ""; };
      if (opts.when === "sync") back();
      else if (opts.when === "micro") queueMicrotask(back);
      else setTimeout(back, 5);
    });
    return { el, writes };
  }
  const fillText = (el: FakeInput, v = "Ada Lovelace", opts: { overwrite?: boolean } = {}) =>
    fillRef(ref(el, "Full name", "native", "text"), match("full name", [v]), opts);

  test("a page that reverts the first write a moment later (not typed) keeps the retry: filled, value in place, retried once as typing", async () => {
    fresh();
    const c = controlled({ when: "later", until: Infinity, onlyUntyped: true });
    const out = await fillText(c.el);
    expect(out).toMatchObject({ filled: true, key: "full name" });
    expect(c.el.value).toBe("Ada Lovelace");
    expect(c.writes).toEqual([undefined, "insertText"]); // first the plain events, then once as typing
    expect(c.el.attrs["data-vani-fill"]).toBe("ok");
    nothingLeft();
  });

  test("a revert in a microtask is caught too", async () => {
    fresh();
    const c = controlled({ when: "micro", until: 1 });
    expect(await fillText(c.el)).toMatchObject({ filled: true });
    expect(c.el.value).toBe("Ada Lovelace");
    expect(c.writes.length).toBe(2);
  });

  test("a page that always reverts is never reported filled: manual, with the reason, no outline, two attempts and no more", async () => {
    fresh();
    const c = controlled({ when: "later", until: Infinity });
    const out = await fillText(c.el);
    expect(out).toMatchObject({ filled: false, reason: "manual", detail: "the page did not keep the value" });
    expect(KEPT_NOT).toBe("the page did not keep the value");
    expect(c.el.value).toBe("");
    expect(c.el.attrs["data-vani-fill"]).toBeUndefined();
    expect(c.writes.length).toBe(2);
    nothingLeft();
  });

  test("a write that is reverted inside the handler is the same: retried once, then manual", async () => {
    fresh();
    const c = controlled({ when: "sync", until: Infinity });
    expect(await fillText(c.el)).toMatchObject({ filled: false, reason: "manual", detail: KEPT_NOT });
    expect(c.writes.length).toBe(2);
  });

  test("a write the page keeps is written once, with no retry and no typed-in event", async () => {
    fresh();
    const c = controlled({ when: "later", until: 0 });
    expect(await fillText(c.el)).toMatchObject({ filled: true });
    expect(c.writes).toEqual([undefined]);
    nothingLeft();
  });

  test("a mask that reformats what it was given counts as kept", async () => {
    fresh();
    const doc = new FakeDoc();
    const el = new FakeInput(doc, () => {});
    doc.body.append(el);
    el.on("input", () => { setTimeout(() => { el.value = "(555) 0100"; }, 5); });
    expect(await fillText(el, "5550100")).toMatchObject({ filled: true });
    expect(el.value).toBe("(555) 0100");
  });

  /** A field that already holds `prior`, whose page (after 5 ms) sets its value to `shown(typed)` on every input. */
  function masked(prior: string, shown: (typed: string) => string): { el: FakeInput; inputs: number } {
    const doc = new FakeDoc();
    const el = new FakeInput(doc, () => {});
    doc.body.append(el);
    el.value = prior;
    const m = { el, inputs: 0 };
    el.on("input", () => { m.inputs++; const typed = el.value; setTimeout(() => { el.value = shown(typed); }, 5); });
    return m;
  }

  test("a mask that formats the written text into exactly what the field held before is kept, written once", async () => {
    fresh();
    const m = masked("(555) 010-0", () => "(555) 010-0");
    expect(await fillText(m.el, "5550100", { overwrite: true })).toMatchObject({ filled: true, key: "full name" });
    expect(m.el.value).toBe("(555) 010-0");
    expect(m.inputs).toBe(1); // read as kept the first time: no retry as typing
    nothingLeft();
  });

  test("a mask that changes case counts as the same text", async () => {
    fresh();
    const m = masked("ADA LOVELACE", t => t.toUpperCase());
    expect(await fillText(m.el, "Ada Lovelace", { overwrite: true })).toMatchObject({ filled: true });
    expect(m.inputs).toBe(1);
  });

  test("a page that puts the old, differently written value back is still a revert", async () => {
    fresh();
    const m = masked("(555) 010-0", () => "(555) 010-0");
    expect(await fillText(m.el, "Ada Lovelace", { overwrite: true })).toMatchObject({ filled: false, reason: "manual", detail: KEPT_NOT });
    expect(m.inputs).toBe(2); // the plain write, then once as typing
  });

  test("a masked draft that formats into the previous text is kept too", () => {
    fresh();
    const doc = new FakeDoc();
    const el = new FakeInput(doc, () => {}, "textarea");
    doc.body.append(el);
    el.value = "A  draft";
    el.on("input", () => { el.value = "A  draft"; }); // a normaliser that puts back its own spacing
    expect(insertDraft(ref(el, "Why us", "native", "textarea"), "A draft.", { overwrite: true })).toMatchObject({ filled: true });
  });

  test("the retry as typing never scrolls, and gives focus back to the field that had it", async () => {
    fresh();
    const c = controlled({ when: "later", until: 1 });
    const doc = c.el.ownerDocument;
    const other = doc.body.append(new FakeEl(doc, "button"));
    other.focus();
    other.focusCalls.length = 0;
    expect(await fillText(c.el)).toMatchObject({ filled: true });
    expect(c.writes).toEqual([undefined, "insertText"]);
    expect(c.el.focusCalls).toEqual([{ options: { preventScroll: true } }]);
    expect(c.el.blurCalls).toBe(1); // it was not focused: typing ends with a blur
    expect(other.focusCalls).toEqual([{ options: { preventScroll: true } }]);
    expect(doc.activeElement).toBe(other);
  });

  test("the retry as typing does not blur a field the person is in", async () => {
    fresh();
    const c = controlled({ when: "later", until: 1 });
    c.el.focus();
    c.el.focusCalls.length = 0;
    expect(await fillText(c.el)).toMatchObject({ filled: true });
    expect(c.writes).toEqual([undefined, "insertText"]);
    expect(c.el.blurCalls).toBe(0);
    expect(c.el.focusCalls.every(f => f.options?.preventScroll === true)).toBe(true);
    expect(c.el.ownerDocument.activeElement).toBe(c.el);
  });

  describe("in a hidden tab (no frames: the check after a write is one event-loop turn)", () => {
    const hide = (el: FakeInput): void => { el.ownerDocument.hidden = true; };

    test("a revert that lands after that turn is reported not kept, never filled", async () => {
      fresh();
      const c = controlled({ when: "later", until: Infinity });
      hide(c.el);
      beginFill();
      const out = await fillText(c.el);
      expect(out).toMatchObject({ filled: false, reason: "manual", detail: KEPT_NOT, key: "full name" });
      expect(c.el.value).toBe("");
      expect(c.el.attrs["data-vani-fill"]).toBeUndefined();
      expect(undoCount()).toBe(0);
      nothingLeft();
    });

    test("a write the page keeps is filled and outlined, and costs one bounded wait", async () => {
      fresh();
      const c = controlled({ when: "later", until: 0 });
      hide(c.el);
      const t0 = performance.now();
      expect(await fillText(c.el)).toMatchObject({ filled: true });
      expect(performance.now() - t0).toBeLessThan(250);
      expect(c.el.value).toBe("Ada Lovelace");
      expect(c.el.attrs["data-vani-fill"]).toBe("ok");
      nothingLeft();
    });

    test("a whole pass waits once for all its fields, and only the reverted one is lost", async () => {
      fresh();
      const doc = new FakeDoc();
      doc.hidden = true;
      const mk = (label: string, revert: boolean): FieldRef => {
        const el = new FakeInput(doc, () => {});
        doc.body.append(el);
        if (revert) el.on("input", () => { setTimeout(() => { el.value = ""; }, 5); });
        return ref(el, label, "native", "text");
      };
      const refs = [mk("One", false), mk("Two", true), mk("Three", false)];
      const matches = new Map<FieldRef, Match | null>(refs.map((r, i) => [r, match(`k${i}`, [`v${i}`])]));
      const lateWaits: number[] = [];
      const wrapped = globalThis.setTimeout;
      globalThis.setTimeout = ((fn: () => void, ms?: number) => { if (ms === 50) lateWaits.push(ms); return wrapped(fn, ms); }) as typeof setTimeout;
      beginFill();
      let out: Awaited<ReturnType<typeof fillAll>>;
      try { out = await fillAll(refs, emptyBank(), "x.example", { matches }); } finally { globalThis.setTimeout = wrapped; }
      expect(out.map(o => [o.label, o.filled, o.reason])).toEqual([["One", true, undefined], ["Two", false, "manual"], ["Three", true, undefined]]);
      expect(lateWaits.length).toBe(1);
      expect(undoCount()).toBe(2); // the reverted field is not claimed as Vani's
      expect((refs[1]!.els[0] as unknown as FakeInput).attrs["data-vani-fill"]).toBeUndefined();
      expect((refs[0]!.els[0] as unknown as FakeInput).attrs["data-vani-fill"]).toBe("ok");
      nothingLeft();
    });

    test("a visible tab pays no such wait", async () => {
      fresh();
      const c = controlled({ when: "later", until: 0 });
      let waits50 = 0;
      const wrapped = globalThis.setTimeout;
      globalThis.setTimeout = ((fn: () => void, ms?: number) => { if (ms === 50) waits50++; return wrapped(fn, ms); }) as typeof setTimeout;
      try { expect(await fillText(c.el)).toMatchObject({ filled: true }); } finally { globalThis.setTimeout = wrapped; }
      expect(waits50).toBe(0);
    });
  });

  test("a date input is held to its ISO value, read back after the same wait", async () => {
    fresh();
    const kept = controlled({ when: "later", until: 0, attrs: { type: "date" } });
    expect(await fillRef(ref(kept.el, "Start date", "native", "date"), match("start", ["2026-11-02"]))).toMatchObject({ filled: true });
    expect(kept.el.value).toBe("2026-11-02");
    const lost = controlled({ when: "later", until: Infinity, attrs: { type: "date" } });
    const out = await fillRef(ref(lost.el, "Start date", "native", "date"), match("start", ["2026-11-02"]));
    expect(out).toMatchObject({ filled: false, reason: "manual", detail: KEPT_NOT });
    expect(lost.el.value).toBe("");
  });

  test("a control that blanks the text itself (nothing ever shown) stays no_option, as before", async () => {
    fresh();
    const doc = new FakeDoc();
    const el = new FakeInput(doc, () => {});
    Object.defineProperty(el, "value", { get: () => "", set: () => {} });
    doc.body.append(el);
    expect(await fillText(el)).toMatchObject({ filled: false, reason: "no_option" });
  });

  test("an empty saved value is written without waiting", async () => {
    fresh();
    const c = controlled({ when: "later", until: Infinity });
    expect(await fillText(c.el, "")).toMatchObject({ filled: true });
    expect(c.writes).toEqual([undefined]);
  });

  test("overwrite: a page that puts the typed value back is a revert, not a keep", async () => {
    fresh();
    const doc = new FakeDoc();
    const el = new FakeInput(doc, () => {});
    doc.body.append(el);
    el.value = "typed";
    el.on("input", () => { setTimeout(() => { el.value = "typed"; }, 5); });
    const out = await fillText(el, "Ada", { overwrite: true });
    expect(out).toMatchObject({ filled: false, reason: "manual", detail: KEPT_NOT });
  });

  test("a draft the page reverts inside the write is not inserted, and not marked", () => {
    fresh();
    const doc = new FakeDoc();
    const el = new FakeInput(doc, () => {}, "textarea");
    doc.body.append(el);
    el.on("input", () => { el.value = ""; });
    const out = insertDraft(ref(el, "Why us", "native", "textarea"), "A draft.");
    expect(out).toMatchObject({ filled: false, reason: "manual", detail: KEPT_NOT });
    expect(el.attrs["data-vani-draft"]).toBeUndefined();
    expect(el.attrs["data-vani-fill"]).toBeUndefined();
  });
});

// A yellow fuzzy fill that is submitted untouched must not come back as the person's own, confident answer. Fill records
// what it wrote; a capture of the field is marked `filledBy` only while the field still holds exactly that.
describe("what Fill wrote is remembered, until the person changes it", () => {
  const textRef = (label = "Full name"): { el: FakeInput; r: FieldRef } => {
    const doc = new FakeDoc();
    const el = new FakeInput(doc, () => {});
    doc.body.append(el);
    return { el, r: ref(el, label, "native", "text") };
  };
  const held = (r: FieldRef) => filledBy(r.els[0], readRefValue(r));

  test("a fill records its answer key and the review flag, for a confident and for a review fill", async () => {
    fresh();
    const a = textRef(), b = textRef("Cover note");
    await fillRef(a.r, match("full name", ["Ada"]));
    await fillRef(b.r, { ...match("note", ["Hello"]), via: "fuzzy", review: true });
    expect(held(a.r)).toEqual({ key: "full name", review: false });
    expect(held(b.r)).toEqual({ key: "note", review: true });
  });

  test("any edit ends it, however small, and typing the same value back does not need to", async () => {
    fresh();
    const { el, r } = textRef();
    await fillRef(r, match("full name", ["Ada"]));
    expect(held(r)).toBeDefined();
    el.value = "Ada ";
    expect(held(r)).toBeUndefined();
    el.value = "Ada";
    expect(held(r)).toEqual({ key: "full name", review: false });
  });

  test("a field Fill did not write has no record: has_value, no match, too long", async () => {
    fresh();
    const typed = textRef();
    typed.el.value = "mine";
    await fillRef(typed.r, match("full name", ["Ada"]));
    expect(held(typed.r)).toBeUndefined();
    const tight = textRef();
    tight.el.attrs["maxlength"] = "2";
    await fillRef(tight.r, match("full name", ["Ada"]));
    expect(held(tight.r)).toBeUndefined();
  });

  test("an overwritten value is recorded as Fill's, not the person's", async () => {
    fresh();
    const { el, r } = textRef();
    el.value = "typed";
    await fillRef(r, match("full name", ["Ada"]), { overwrite: true });
    expect(held(r)).toEqual({ key: "full name", review: false });
  });

  test("clearing the marks is the person having looked: the record goes", async () => {
    fresh();
    const { r } = textRef();
    await fillRef(r, match("full name", ["Ada"]));
    expect(r.els[0]!.getAttribute("data-vani-fill")).toBe("ok");
    clearMarks({ querySelectorAll: () => [] } as unknown as ParentNode);
    expect(r.els[0]!.getAttribute("data-vani-fill")).toBeNull();
    expect(held(r)).toBeUndefined();
  });

  test("a second Fill clears the outlines only: the first fill's fields, skipped as filled, still read as Vani's", async () => {
    fresh();
    const { r } = textRef();
    await fillRef(r, match("full name", ["Ada"]));
    clearMarks({ querySelectorAll: () => [] } as unknown as ParentNode, { forget: false });
    beginFill();
    const again = await fillRef(r, match("full name", ["Ada"]));
    expect(again.reason).toBe("has_value");
    expect(held(r)).toEqual({ key: "full name", review: false });
  });

  test("fillAll records every field it filled, as the pass ended", async () => {
    fresh();
    const a = textRef(), b = textRef("Colour");
    const matches = new Map<FieldRef, Match | null>([[a.r, match("a", ["one"])], [b.r, null]]);
    await fillAll([a.r, b.r], emptyBank(), "x.example", { matches });
    expect(held(a.r)).toEqual({ key: "a", review: false });
    expect(held(b.r)).toBeUndefined();
  });

  test("a field with no record, or a record for another control, is never claimed", () => {
    expect(filledBy(undefined, ["x"])).toBeUndefined();
    expect(filledBy(new FakeEl(new FakeDoc(), "input") as unknown as Element, ["x"])).toBeUndefined();
  });
});

// "Undo fill" puts back what the last Fill wrote, in the fields that still hold exactly that, and never a user's edit.
describe("undo the last fill", () => {
  const mk = (label: string, prior = ""): { el: FakeInput; r: FieldRef } => {
    const doc = new FakeDoc();
    const el = new FakeInput(doc, () => {});
    doc.body.append(el);
    el.value = prior;
    return { el, r: ref(el, label, "native", "text") };
  };
  const fillBoth = async (...rs: FieldRef[]): Promise<void> => {
    beginFill();
    const matches = new Map<FieldRef, Match | null>(rs.map((r, i) => [r, match(`k${i}`, [`v${i}`])]));
    await fillAll(rs, emptyBank(), "x.example", { matches, overwrite: true });
  };
  const holds = (r: FieldRef) => filledBy(r.els[0], readRefValue(r));

  test("restores what Fill wrote, takes the outline and the record off, and is spent afterwards", async () => {
    fresh();
    const a = mk("A"), b = mk("B");
    await fillBoth(a.r, b.r);
    expect([a.el.value, b.el.value]).toEqual(["v0", "v1"]);
    expect(undoCount()).toBe(2);
    const rep = await undoFill();
    expect(rep).toEqual({ restored: 2, edited: 0, manual: 0, gone: 0 });
    expect([a.el.value, b.el.value]).toEqual(["", ""]);
    expect(a.el.attrs["data-vani-fill"]).toBeUndefined();
    expect(a.el.style["outline"]).toBe("");
    expect(holds(a.r)).toBeUndefined();
    expect(undoCount()).toBe(0);
    expect(await undoFill()).toEqual({ restored: 0, edited: 0, manual: 0, gone: 0 });
    nothingLeft();
  });

  test("an undone field tagged a Suggest draft loses the tag: what the person writes next is theirs", async () => {
    fresh();
    const a = mk("A");
    await fillBoth(a.r);
    a.el.setAttribute("data-vani-draft", "1");
    a.el.setAttribute("data-vani-draft-text", "v0");
    expect((await undoFill()).restored).toBe(1);
    expect(a.el.attrs["data-vani-draft"]).toBeUndefined();
    expect(a.el.attrs["data-vani-draft-text"]).toBeUndefined();
  });

  test("a field the person has edited since is never clobbered, and keeps its outline", async () => {
    fresh();
    const a = mk("A"), b = mk("B");
    await fillBoth(a.r, b.r);
    b.el.value = "my own words";
    const rep = await undoFill();
    expect(rep).toMatchObject({ restored: 1, edited: 1 });
    expect(a.el.value).toBe("");
    expect(b.el.value).toBe("my own words");
    expect(b.el.attrs["data-vani-fill"]).toBe("ok");
  });

  test("a field that held text before an overwrite gets that text back", async () => {
    fresh();
    const a = mk("A", "what was there");
    await fillBoth(a.r);
    expect(a.el.value).toBe("v0");
    expect(await undoFill()).toMatchObject({ restored: 1 });
    expect(a.el.value).toBe("what was there");
  });

  test("it is bounded to the last fill: a new fill replaces the one before it", async () => {
    fresh();
    const a = mk("A"), b = mk("B");
    await fillBoth(a.r);
    await fillBoth(b.r);
    expect(undoCount()).toBe(1);
    expect(await undoFill()).toMatchObject({ restored: 1 });
    expect(a.el.value).toBe("v0"); // the earlier fill was not undone
    expect(b.el.value).toBe("");
  });

  test("a field that left the page is reported, not guessed at", async () => {
    fresh();
    const a = mk("A");
    await fillBoth(a.r);
    a.el.isConnected = false;
    expect(await undoFill()).toEqual({ restored: 0, edited: 0, manual: 0, gone: 1 });
  });

  test("a dropdown Fill drove cannot be put back safely: it says so and stays as Fill left it", async () => {
    fresh();
    const doc = new FakeDoc();
    const c = combo(doc, "o1", ["Yes", "No"], { openAfter: 0, pick: { shows: 0 } });
    const r = ref(c.trigger, "Sponsor", "combobox");
    beginFill();
    await fillRef(r, match("sponsor", ["Yes"]));
    expect(shownValue(c)).toEqual(["Yes"]);
    expect(await undoFill()).toEqual({ restored: 0, edited: 0, manual: 1, gone: 0 });
    expect(shownValue(c)).toEqual(["Yes"]);
  });

  test("an aria checkbox is clicked back, and a value that does not come back is reported by hand", async () => {
    fresh();
    const doc = new FakeDoc();
    const box = doc.body.append(new FakeEl(doc, "div"));
    box.attrs["role"] = "checkbox";
    box.attrs["aria-checked"] = "false";
    box.on("click", () => { box.attrs["aria-checked"] = box.attrs["aria-checked"] === "true" ? "false" : "true"; });
    const r = ref(box, "Agree", "aria-checkbox", "checkbox");
    beginFill();
    await fillRef(r, match("agree", ["yes"]));
    expect(box.attrs["aria-checked"]).toBe("true");
    expect(await undoFill()).toMatchObject({ restored: 1, manual: 0 });
    expect(box.attrs["aria-checked"]).toBe("false");

    // A page that ignores the click-back: not reported as restored.
    const stuck = doc.body.append(new FakeEl(doc, "div"));
    stuck.attrs["role"] = "checkbox";
    stuck.attrs["aria-checked"] = "false";
    let clicks = 0;
    stuck.on("click", () => { if (clicks++ === 0) stuck.attrs["aria-checked"] = "true"; });
    beginFill();
    await fillRef(ref(stuck, "Agree too", "aria-checkbox", "checkbox"), match("agree2", ["yes"]));
    expect(await undoFill()).toMatchObject({ restored: 0, manual: 1 });
  });

  test("a field filled one at a time (a 'Use' button) joins the last fill, and a field Fill refused is not in it", async () => {
    fresh();
    const a = mk("A"), tight = mk("B");
    tight.el.attrs["maxlength"] = "1";
    beginFill();
    await fillRef(a.r, match("a", ["one"]));
    await fillRef(tight.r, match("b", ["too long for it"]));
    expect(undoCount()).toBe(1);
    expect(await undoFill()).toMatchObject({ restored: 1 });
  });
});

describe("widgetRoots", () => {
  const doc = { nodeType: 9 };
  test("a widget in the page watches the page only", () => {
    const el = { ownerDocument: doc, getRootNode: () => doc } as unknown as Element;
    expect(widgetRoots(el)).toEqual([doc as unknown as Node]);
  });
  test("a widget inside a shadow root watches the page and that root", () => {
    const shadow = { nodeType: 11 };
    const el = { ownerDocument: doc, getRootNode: () => shadow } as unknown as Element;
    expect(widgetRoots(el)).toEqual([doc, shadow] as unknown as Node[]);
  });
  test("a detached widget watches its document only", () => {
    const lone = { nodeType: 1 };
    const el = { ownerDocument: doc, getRootNode: () => lone } as unknown as Element;
    expect(widgetRoots(el)).toEqual([doc as unknown as Node]);
  });
});
