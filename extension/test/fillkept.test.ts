import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import type { Answer, FieldInfo, Match } from "../src/types";
import type { FieldRef } from "../src/extract";
import { fillRef } from "../src/fill";

// bun test has no DOM. This fake is only enough for fillRef to drive a combobox, a select, and a
// checkbox/radio group, and to read back what the control kept. Overlapping labels are synthetic tokens.

type Handler = (e: { type: string }) => void;

class FEvent {
  constructor(public type: string) {}
}

class FakeMO {
  static live = new Set<FakeMO>();
  constructor(private cb: () => void) {}
  observe(): void { FakeMO.live.add(this); }
  disconnect(): void { FakeMO.live.delete(this); }
  deliver(): void { if (FakeMO.live.has(this)) this.cb(); }
}

function mutate(): void {
  for (const o of [...FakeMO.live]) queueMicrotask(() => o.deliver());
}

class FakeDoc {
  nodeType = 9;
  body: FakeEl;
  hidden = false;
  activeElement: FakeEl | null = null;
  ids = new Map<string, FakeEl>();
  defaultView = {
    Event: FEvent, InputEvent: FEvent, PointerEvent: FEvent, MouseEvent: FEvent, KeyboardEvent: FEvent, MutationObserver: FakeMO,
    HTMLInputElement: class {}, HTMLTextAreaElement: class {},
  };
  constructor() { this.body = new FakeEl(this, "body"); }
  getElementById(id: string): FakeEl | null { return this.ids.get(id) ?? null; }
  querySelector(sel: string): FakeEl | null { return this.body.querySelector(sel); }
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
  constructor(public ownerDocument: FakeDoc, public localName: string, public text = "") {}
  get parentElement(): FakeEl | null { return this.parent; }
  get textContent(): string { return this.text + this.children.map(c => c.textContent).join(""); }
  get classList(): string[] { return (this.attrs["class"] ?? "").split(/\s+/).filter(Boolean); }
  append(c: FakeEl): FakeEl { c.parent = this; this.children.push(c); return c; }
  getAttribute(n: string): string | null { return this.attrs[n] ?? null; }
  setAttribute(n: string, v: string): void { this.attrs[n] = v; }
  removeAttribute(n: string): void { delete this.attrs[n]; }
  getRootNode(): FakeDoc { return this.ownerDocument; }
  getClientRects(): unknown[] { return this.rendered ? [{}] : []; }
  focus(): void { this.ownerDocument.activeElement = this; }
  blur(): void { if (this.ownerDocument.activeElement === this) this.ownerDocument.activeElement = null; }
  matches(): boolean { return false; }
  closest(): FakeEl | null { return null; }
  querySelector(sel: string): FakeEl | null { return this.querySelectorAll(sel)[0] ?? null; }
  querySelectorAll(sel: string): FakeEl[] {
    const out: FakeEl[] = [];
    const walk = (el: FakeEl): void => {
      for (const c of el.children) {
        if (matchesSel(c, sel)) out.push(c);
        walk(c);
      }
    };
    walk(this);
    return out;
  }
  on(type: string, h: Handler): void { this.handlers.set(type, [...(this.handlers.get(type) ?? []), h]); }
  dispatchEvent(e: { type: string }): boolean { for (const h of this.handlers.get(e.type) ?? []) h(e); return true; }
}

function matchesSel(el: FakeEl, sel: string): boolean {
  return sel.split(",").some(part => matchesOne(el, part.trim()));
}

function matchesOne(el: FakeEl, sel: string): boolean {
  if (sel === "*") return true;
  if (sel === "[role=option]") return el.attrs["role"] === "option";
  if (sel === "[role=option][aria-selected=true]") return el.attrs["role"] === "option" && el.attrs["aria-selected"] === "true";
  const contains = /^\[class\*=([^\s\]]+)/i.exec(sel);
  if (contains) return (el.attrs["class"] ?? "").toLowerCase().includes(contains[1]!.replace(/["']/g, "").toLowerCase());
  const word = /^\[class~=([^\s\]]+)/i.exec(sel);
  if (word) return (el.attrs["class"] ?? "").toLowerCase().split(/\s+/).includes(word[1]!.replace(/["']/g, "").toLowerCase());
  return false;
}

class FakeOption {
  selected = false;
  constructor(public text: string, public value: string) {}
}

class FakeSelect extends FakeEl {
  options: FakeOption[] = [];
  multiple = false;
  private current = "";
  /** Assignment leaves the previous value in place. */
  ignoreWrite = false;
  constructor(doc: FakeDoc) { super(doc, "select"); }
  get value(): string { return this.current; }
  set value(v: string) {
    if (this.ignoreWrite) return;
    this.current = v;
    if (this.multiple) return;
    let hit = false;
    for (const o of this.options) {
      const on = !hit && o.value === v;
      o.selected = on;
      if (on) hit = true;
    }
  }
  get selectedIndex(): number { return this.options.findIndex(o => o.selected); }
  get selectedOptions(): FakeOption[] { return this.options.filter(o => o.selected); }
}

class FakeCheck extends FakeEl {
  checked = false;
  stick = true;
  type: "checkbox" | "radio" = "checkbox";
  value = "";
  siblings: FakeCheck[] = [];
  constructor(doc: FakeDoc) { super(doc, "input"); }
  click(): void {
    if (this.stick) {
      if (this.type === "radio") {
        for (const s of this.siblings) if (s !== this) s.checked = false;
        this.checked = true;
      } else {
        this.checked = !this.checked;
      }
    }
    this.dispatchEvent(new FEvent("click"));
  }
}

const saved: Record<string, unknown> = {};
beforeAll(() => {
  saved["Node"] = (globalThis as Record<string, unknown>)["Node"];
  saved["NodeFilter"] = (globalThis as Record<string, unknown>)["NodeFilter"];
  Object.assign(globalThis, {
    Node: { ELEMENT_NODE: 1 },
    NodeFilter: { SHOW_ELEMENT: 1, SHOW_TEXT: 4, SHOW_CDATA_SECTION: 8, FILTER_ACCEPT: 1, FILTER_REJECT: 2 },
  });
});
afterAll(() => { Object.assign(globalThis, saved); });

const answer = (key: string, value: string[]): Answer =>
  ({ key, labels: [], kind: "text", value, sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: "2025-01-01T00:00:00Z", review: false });
const match = (key: string, value: string[]): Match => ({ answer: answer(key, value), via: "label", score: 0.9, value, review: false });
const baseInfo = (label: string, kind: FieldInfo["kind"], options: string[] = []): FieldInfo =>
  ({ kind, label, name: "", autocomplete: "", automationId: "", options, required: false, value: [], multi: options.length > 1 });

function comboRef(el: FakeEl, label: string): FieldRef {
  return { info: baseInfo(label, "select"), els: [el as unknown as HTMLElement], widget: "combobox", manual: "" };
}
function selectRef(el: FakeSelect, label: string, options: string[]): FieldRef {
  return { info: baseInfo(label, el.multiple ? "checkbox" : "select", options), els: [el as unknown as HTMLElement], widget: "native", manual: "" };
}
function checkRef(els: FakeCheck[], label: string, kind: "checkbox" | "radio", options: string[]): FieldRef {
  return { info: baseInfo(label, kind, options), els: els as unknown as HTMLElement[], widget: "native", manual: "" };
}

interface Combo {
  trigger: FakeEl;
  opts: FakeEl[];
  shown: () => string[];
}

/** `show` names which option the click marks selected: itself, or the other one (a value the control did not keep). */
function combo(doc: FakeDoc, id: string, labels: string[], show: "self" | "other" | ((clicked: FakeEl) => string)): Combo {
  const trigger = doc.body.append(new FakeEl(doc, "button"));
  trigger.attrs["role"] = "combobox";
  trigger.attrs["aria-controls"] = id;
  const list = doc.body.append(new FakeEl(doc, "div"));
  list.attrs["role"] = "listbox";
  doc.ids.set(id, list);
  const opts = labels.map(t => {
    const opt = list.append(new FakeEl(doc, "div", t));
    opt.attrs["role"] = "option";
    opt.rendered = false;
    return opt;
  });
  for (const opt of opts) {
    opt.on("click", () => {
      if (typeof show === "function") opt.text = show(opt);
      for (const x of opts) x.removeAttribute("aria-selected");
      const marked = show === "other" ? (opts.find(o => o !== opt) ?? opt) : opt;
      marked.setAttribute("aria-selected", "true");
      mutate();
    });
  }
  trigger.on("click", () => { for (const x of opts) x.rendered = true; mutate(); });
  return { trigger, opts, shown: () => opts.filter(o => o.attrs["aria-selected"] === "true").map(o => o.text) };
}

function flag(parent: FakeEl, cls: string): void {
  const icon = parent.append(new FakeEl(parent.ownerDocument, "span"));
  icon.attrs["class"] = cls;
}

function dialCombo(doc: FakeDoc, flagClass: string | null, shownFlag: string | null): { trigger: FakeEl; value: FakeEl } {
  const trigger = doc.body.append(new FakeEl(doc, "button"));
  trigger.attrs["role"] = "combobox";
  trigger.attrs["aria-controls"] = "dial";
  const value = trigger.append(new FakeEl(doc, "div"));
  value.attrs["class"] = "single-value";
  if (shownFlag) flag(value, shownFlag);
  const list = doc.body.append(new FakeEl(doc, "div"));
  list.attrs["role"] = "listbox";
  doc.ids.set("dial", list);
  const opt = list.append(new FakeEl(doc, "div", "qa +9"));
  opt.attrs["role"] = "option";
  opt.rendered = false;
  if (flagClass) flag(opt, flagClass);
  opt.on("click", () => { value.text = "+9"; mutate(); });
  trigger.on("click", () => { opt.rendered = true; mutate(); });
  return { trigger, value };
}

function addOption(sel: FakeSelect, text: string, value: string): FakeOption {
  const o = new FakeOption(text, value);
  sel.options.push(o);
  return o;
}

const picked = (sel: FakeSelect): string => sel.options.filter(o => o.selected).map(o => o.text).join(",");

describe("combobox success is the shown value, not an overlap", () => {
  test("a longer shown label that merely contains the token is not success", async () => {
    const doc = new FakeDoc();
    const c = combo(doc, "lb-us", ["status", "us"], "other");
    const out = await fillRef(comboRef(c.trigger, "Token"), match("token", ["us"]));
    expect(c.shown()).toEqual(["status"]);
    expect(out.filled).toBe(false);
    expect(out.reason).toBe("manual");
  });

  test("a shorter shown label that is only a piece of the token is not success", async () => {
    const doc = new FakeDoc();
    const c = combo(doc, "lb-qa", ["qa", "qatest"], "other");
    const out = await fillRef(comboRef(c.trigger, "Token"), match("token", ["qatest"]));
    expect(c.shown()).toEqual(["qa"]);
    expect(out.filled).toBe(false);
    expect(out.reason).toBe("manual");
  });

  test("an exact shown token is success, including a lookalike that normalizes to it", async () => {
    const doc = new FakeDoc();
    const exact = combo(doc, "lb-exact", ["us", "status"], "self");
    const kept = await fillRef(comboRef(exact.trigger, "Token"), match("token", ["us"]));
    expect(kept.filled).toBe(true);
    expect(kept.review).toBeFalsy();
    expect(exact.shown()).toEqual(["us"]);

    const look = combo(doc, "lb-look", ["\uFF55\uFF53"], clicked => (clicked.text = "us", "us"));
    const folded = await fillRef(comboRef(look.trigger, "Token"), match("token", ["us"]));
    expect(folded.filled).toBe(true);
    expect(look.shown()).toEqual(["us"]);
  });

  test("a dialling code is success only when the icons match, and only a review when there is no icon", async () => {
    const doc = new FakeDoc();
    const same = dialCombo(doc, "flag-qa", "flag-qa");
    const ok = await fillRef(comboRef(same.trigger, "Code"), match("code", ["qa +9"]));
    expect(same.value.text).toBe("+9");
    expect(ok.filled).toBe(true);
    expect(ok.review).toBeFalsy();

    const bare = dialCombo(new FakeDoc(), null, null);
    const review = await fillRef(comboRef(bare.trigger, "Code"), match("code", ["qa +9"]));
    expect(bare.value.text).toBe("+9");
    expect(review.filled).toBe(true);
    expect(review.review).toBe(true);
  });

  test("a dialling code with a different icon is not success", async () => {
    const doc = new FakeDoc();
    const diff = dialCombo(doc, "flag-qa", "flag-red");
    const out = await fillRef(comboRef(diff.trigger, "Code"), match("code", ["qa +9"]));
    expect(diff.value.text).toBe("+9");
    expect(out.filled).toBe(false);
    expect(out.reason).toBe("manual");
  });

  test("an empty list, an empty wanted token, and a click that shows nothing are not success", async () => {
    const emptyList = combo(new FakeDoc(), "lb-empty", [], "self");
    const noOpts = await fillRef(comboRef(emptyList.trigger, "Token"), match("token", ["us"]));
    expect(noOpts.filled).toBe(false);
    expect(emptyList.shown()).toEqual([]);

    const blank = combo(new FakeDoc(), "lb-blank", ["us", "qa"], "self");
    const noWant = await fillRef(comboRef(blank.trigger, "Token"), match("token", [""]));
    expect(noWant.filled).toBe(false);
    expect(blank.shown()).toEqual([]);

    const dropped = combo(new FakeDoc(), "lb-drop", ["us"], () => "");
    const gone = await fillRef(comboRef(dropped.trigger, "Token"), match("token", ["us"]));
    expect(gone.filled).toBe(false);
    expect(dropped.shown()).toEqual([""]);
  });
});

describe("select and group success is what the control kept", () => {
  test("a single select that keeps the option is success, and a lookalike normalizes to the same token", async () => {
    const doc = new FakeDoc();
    const sel = new FakeSelect(doc);
    addOption(sel, "status", "s");
    addOption(sel, "us", "u");
    const out = await fillRef(selectRef(sel, "Token", ["status", "us"]), match("token", ["us"]));
    expect(out.filled).toBe(true);
    expect(picked(sel)).toBe("us");

    const look = new FakeSelect(doc);
    addOption(look, "\uFF52\uFF45\uFF44", "r");
    const folded = await fillRef(selectRef(look, "Colour", ["\uFF52\uFF45\uFF44"]), match("colour", ["red"]));
    expect(folded.filled).toBe(true);
    expect(picked(look)).toBe("\uFF52\uFF45\uFF44");
  });

  test("a select is not success when the write is reverted or lands on an overlapping label", async () => {
    const ignored = new FakeSelect(new FakeDoc());
    ignored.ignoreWrite = true;
    addOption(ignored, "us", "u");
    const stayed = await fillRef(selectRef(ignored, "Token", ["us"]), match("token", ["us"]));
    expect(stayed.filled).toBe(false);
    expect(picked(ignored)).toBe("");

    const reverted = new FakeSelect(new FakeDoc());
    addOption(reverted, "us", "u");
    addOption(reverted, "status", "s");
    reverted.on("change", () => { reverted.value = "s"; });
    const overlap = await fillRef(selectRef(reverted, "Token", ["us", "status"]), match("token", ["us"]));
    expect(overlap.filled).toBe(false);
    expect(picked(reverted)).toBe("status");

    const cleared = new FakeSelect(new FakeDoc());
    addOption(cleared, "red", "r");
    cleared.on("change", () => { cleared.value = ""; });
    const gone = await fillRef(selectRef(cleared, "Colour", ["red"]), match("colour", ["red"]));
    expect(gone.filled).toBe(false);
    expect(picked(cleared)).toBe("");
  });

  test("empty options and an empty wanted value are not success", async () => {
    const bare = new FakeSelect(new FakeDoc());
    const noOpts = await fillRef(selectRef(bare, "Token", []), match("token", ["us"]));
    expect(noOpts.filled).toBe(false);

    const blank = new FakeSelect(new FakeDoc());
    addOption(blank, "", "");
    addOption(blank, "red", "r");
    const noWant = await fillRef(selectRef(blank, "Colour", ["", "red"]), match("colour", [""]));
    expect(noWant.filled).toBe(false);
    expect(picked(blank)).toBe("");

    const none = new FakeSelect(new FakeDoc());
    none.multiple = true;
    addOption(none, "red", "r");
    const multiBlank = await fillRef(selectRef(none, "Colour", ["red"]), match("colour", []));
    expect(multiBlank.filled).toBe(false);
    expect(picked(none)).toBe("");
  });

  test("a substring option is not the selected value, and a multi-select must keep every chosen option", async () => {
    const one = new FakeSelect(new FakeDoc());
    addOption(one, "status", "s");
    const missed = await fillRef(selectRef(one, "Token", ["status"]), match("token", ["us"]));
    expect(missed.filled).toBe(false);
    expect(picked(one)).toBe("");

    const many = new FakeSelect(new FakeDoc());
    many.multiple = true;
    const red = addOption(many, "red", "r");
    const blue = addOption(many, "blue", "b");
    addOption(many, "qa", "q");
    const kept = await fillRef(selectRef(many, "Colour", ["red", "blue", "qa"]), match("colour", ["red", "blue"]));
    expect(kept.filled).toBe(true);
    expect(kept.review).toBeFalsy();
    expect(picked(many)).toBe("red,blue");

    const partial = new FakeSelect(new FakeDoc());
    partial.multiple = true;
    addOption(partial, "red", "r");
    addOption(partial, "qa", "q");
    const review = await fillRef(selectRef(partial, "Colour", ["red", "qa"]), match("colour", ["red", "blue"]));
    expect(review.filled).toBe(true);
    expect(review.review).toBe(true);
    expect(picked(partial)).toBe("red");

    const dropped = new FakeSelect(new FakeDoc());
    dropped.multiple = true;
    const dRed = addOption(dropped, "red", "r");
    const dBlue = addOption(dropped, "blue", "b");
    dropped.on("change", () => { dBlue.selected = false; });
    const lost = await fillRef(selectRef(dropped, "Colour", ["red", "blue"]), match("colour", ["red", "blue"]));
    expect(lost.filled).toBe(false);
    expect(dRed.selected).toBe(true);
    expect(dBlue.selected).toBe(false);
    expect(red.selected).toBe(true);
    expect(blue.selected).toBe(true);
  });

  test("a checkbox or radio is success only when the click stuck", async () => {
    const yes = new FakeCheck(new FakeDoc());
    yes.setAttribute("aria-label", "yes");
    const on = await fillRef(checkRef([yes], "Agree", "checkbox", []), match("agree", ["yes"]));
    expect(on.filled).toBe(true);
    expect(yes.checked).toBe(true);

    const stuck = new FakeCheck(new FakeDoc());
    stuck.stick = false;
    stuck.setAttribute("aria-label", "yes");
    const off = await fillRef(checkRef([stuck], "Agree", "checkbox", []), match("agree", ["yes"]));
    expect(off.filled).toBe(false);
    expect(stuck.checked).toBe(false);

    const already = new FakeCheck(new FakeDoc());
    already.setAttribute("aria-label", "no");
    const no = await fillRef(checkRef([already], "Agree", "checkbox", []), match("agree", ["no"]));
    expect(no.filled).toBe(true);
    expect(already.checked).toBe(false);

    const empty = new FakeCheck(new FakeDoc());
    const blank = await fillRef(checkRef([empty], "Agree", "checkbox", []), match("agree", []));
    expect(blank.filled).toBe(false);
    expect(empty.checked).toBe(false);

    const emptyText = new FakeCheck(new FakeDoc());
    const blankText = await fillRef(checkRef([emptyText], "Agree", "checkbox", []), match("agree", [""]));
    expect(blankText.filled).toBe(false);
  });

  test("a radio click that does not stick is not success, and the wanted radio is the one left on", async () => {
    const doc = new FakeDoc();
    const red = new FakeCheck(doc);
    const blue = new FakeCheck(doc);
    red.type = "radio";
    blue.type = "radio";
    red.siblings = [red, blue];
    blue.siblings = [red, blue];
    red.setAttribute("aria-label", "red");
    blue.setAttribute("aria-label", "blue");
    const kept = await fillRef(checkRef([red, blue], "Colour", "radio", ["red", "blue"]), match("colour", ["red"]));
    expect(kept.filled).toBe(true);
    expect(red.checked).toBe(true);
    expect(blue.checked).toBe(false);

    const bad = new FakeDoc();
    const r2 = new FakeCheck(bad);
    const b2 = new FakeCheck(bad);
    r2.type = "radio";
    b2.type = "radio";
    r2.stick = false;
    r2.siblings = [r2, b2];
    b2.siblings = [r2, b2];
    r2.setAttribute("aria-label", "red");
    b2.setAttribute("aria-label", "blue");
    const lost = await fillRef(checkRef([r2, b2], "Colour", "radio", ["red", "blue"]), match("colour", ["red"]));
    expect(lost.filled).toBe(false);
    expect(r2.checked).toBe(false);
    expect(b2.checked).toBe(false);
  });

  test("a multi checkbox must keep every wanted option on and the others off", async () => {
    const doc = new FakeDoc();
    const red = new FakeCheck(doc);
    const blue = new FakeCheck(doc);
    const qa = new FakeCheck(doc);
    for (const [el, name] of [[red, "red"], [blue, "blue"], [qa, "qa"]] as const) el.setAttribute("aria-label", name);
    const kept = await fillRef(checkRef([red, blue, qa], "Colour", "checkbox", ["red", "blue", "qa"]), match("colour", ["red", "blue"]));
    expect(kept.filled).toBe(true);
    expect(red.checked).toBe(true);
    expect(blue.checked).toBe(true);
    expect(qa.checked).toBe(false);

    const prior = new FakeDoc();
    const pRed = new FakeCheck(prior);
    const pQa = new FakeCheck(prior);
    pRed.setAttribute("aria-label", "red");
    pQa.setAttribute("aria-label", "qa");
    pQa.checked = true;
    const cleared = await fillRef(checkRef([pRed, pQa], "Colour", "checkbox", ["red", "qa"]), match("colour", ["red"]), { overwrite: true });
    expect(cleared.filled).toBe(true);
    expect(pRed.checked).toBe(true);
    expect(pQa.checked).toBe(false);

    const drop = new FakeCheck(new FakeDoc());
    const stay = new FakeCheck(drop.ownerDocument);
    drop.setAttribute("aria-label", "red");
    stay.setAttribute("aria-label", "blue");
    stay.stick = false;
    const lost = await fillRef(checkRef([drop, stay], "Colour", "checkbox", ["red", "blue"]), match("colour", ["red", "blue"]));
    expect(lost.filled).toBe(false);
    expect(drop.checked).toBe(true);
    expect(stay.checked).toBe(false);

    const leftover = new FakeCheck(new FakeDoc());
    leftover.checked = true;
    leftover.setAttribute("aria-label", "red");
    const noWant = await fillRef(checkRef([leftover], "Colour", "checkbox", ["red"]), match("colour", []), { overwrite: true });
    expect(noWant.filled).toBe(false);
    expect(leftover.checked).toBe(true);
  });
});
