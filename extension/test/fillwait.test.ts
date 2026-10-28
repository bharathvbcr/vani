import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import type { Answer, FieldInfo, Match } from "../src/types";
import { emptyBank } from "../src/types";
import type { FieldRef } from "../src/extract";
import { fillAll, fillRef, insertDraft, waitFor, widgetRoots } from "../src/fill";
import { coverageOf } from "../src/coverage";

// bun test has no DOM. These tests drive fill.ts against a small fake just big enough for it: elements
// with attributes, text and children, a document, a tree walker, events, and a MutationObserver whose
// batches the test triggers. Real-browser behaviour is checked by dom-runner.ts (needs Chrome).

// --- fake DOM -------------------------------------------------------------------------------------

type Handler = (e: FEvent) => void;
class FEvent {
  type: string;
  constructor(type: string, _init?: unknown) { this.type = type; }
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
    Event: FEvent, PointerEvent: FEvent, MouseEvent: FEvent, KeyboardEvent: FEvent, MutationObserver: FakeMO,
    HTMLInputElement: class {}, HTMLTextAreaElement: class {},
  };
  ids = new Map<string, FakeEl>();
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
  focus(): void {}
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
    expect(await fillRef(tight.r, saved)).toMatchObject({ filled: false });
    expect([tight.code.value, tight.num.value]).toEqual(["", ""]);
    const tightCode = split("1", null);
    expect(await fillRef(tightCode.r, saved)).toMatchObject({ filled: false });
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
