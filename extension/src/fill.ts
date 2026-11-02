// Write remembered answers into a form. Never submits, never touches password
// inputs, and by default never overwrites what the user already typed. The one
// file input it fills is the resume, with the PDF the owner chose for this job
// (resume.ts); every other file input is left alone.
// ARIA widgets are driven the way a person would: open, click the option whose
// text maps to the saved answer, and check the widget now shows it. A widget
// that cannot be driven is reported "manual", never "filled".
import type { Bank, Match } from "./types";
import {
  type FieldRef, comboOptions, forgetFill, forgetFills, iconSignature, ownedListbox, readRefValue, readWidgetValue, recordFill, sameValues, widgetValueIcon,
} from "./extract";
import { mapChoice, matchField } from "./match";
import { dialCodeOnly, normalizeLabel } from "./normalize";
import { codeDigits, monthIndex, parseDate, parsePhone } from "./sections";
import { type ResumeFetch, isResumeField } from "./resume";
import { observeRoots } from "./rounds";
import { overLimit } from "./guards";

export interface FillOutcome {
  label: string;
  filled: boolean;
  via?: Match["via"];
  score?: number;
  review?: boolean;
  key?: string;
  reason?: "no_match" | "has_value" | "no_option" | "untouchable" | "manual" | "detached"
    /** A resume input: nothing chosen for this job, the file could not be had, or the page dropped it. */
    | "no_resume" | "resume_unavailable" | "refused"
    /** The answer is longer than the field's character limit; the field was left as it was. */
    | "too_long";
  /** Why, in words, for a resume or too_long reason. */
  detail?: string;
}

const COLORS = { ok: "#2e8b57", review: "#d4a017" } as const;
const marked = new Set<HTMLElement>();

type Win = Window & typeof globalThis;
const winOf = (el: Element): Win => (el.ownerDocument.defaultView ?? window) as Win;
const txt = (el: Element): string => (el.textContent ?? "").replace(/\s+/g, " ").trim();
/** How often a wait re-checks on its own, for changes a MutationObserver cannot see (shadow roots, properties). */
const FALLBACK_TICK_MS = 100;

/**
 * Wait until `cond()` is true. Resolves true at once if it already is; otherwise re-checks on the
 * first mutation batch under `root` (a node or a list of roots such as a scan's document and open
 * shadow roots, one observer each, at most 64; default: the global document) and on a 100 ms fallback tick,
 * and resolves false at the `timeout` ceiling after one last check. The observer and both timers
 * are released however the wait ends, including when `cond` throws (the promise then rejects).
 */
export function waitFor(cond: () => boolean, timeout = 600, root?: Node | readonly Node[]): Promise<boolean> {
  if (cond()) return Promise.resolve(true);
  return new Promise<boolean>((resolve, reject) => {
    let stopObserving: (() => void) | undefined;
    let tick: ReturnType<typeof setTimeout> | undefined;
    let ceiling: ReturnType<typeof setTimeout> | undefined;
    let done = false;
    const end = (settle: () => void): void => {
      done = true;
      stopObserving?.();
      clearTimeout(tick);
      clearTimeout(ceiling);
      settle();
    };
    const check = (last: boolean): void => {
      if (done) return;
      let ok: boolean;
      try { ok = cond(); } catch (e) { end(() => reject(e)); return; }
      if (ok) end(() => resolve(true));
      else if (last) end(() => resolve(false));
    };
    const targets: readonly Node[] = root === undefined ? (typeof document === "undefined" ? [] : [document]) : Array.isArray(root) ? root : [root as Node];
    // One observer per root (at most MAX_OBSERVED_ROOTS); the tick below covers any root beyond that.
    stopObserving = observeRoots(targets, () => check(false), { childList: true, attributes: true, characterData: true, subtree: true });
    const again = (): void => {
      if (done) return;
      check(false);
      if (!done) tick = setTimeout(again, FALLBACK_TICK_MS);
    };
    tick = setTimeout(again, FALLBACK_TICK_MS);
    ceiling = setTimeout(() => check(true), timeout);
  });
}

/**
 * Why `text` was not written into `el`, or null when it fits. A script's `.value =` is not held to
 * `maxlength` as typing is, and a site may drop or cut an over-long answer on submit without a word, so
 * an answer past the limit is never written (not cut either: half a sentence, or half a surrogate pair,
 * is worse than none). The person gets the numbers and what to do.
 */
function tooLong(el: Element, text: string, what: "answer" | "draft"): string | null {
  const over = overLimit(el, text);
  if (!over) return null;
  return `The ${what} is ${over.length} characters and this field allows ${over.limit}. Vani left it empty rather than cut it: shorten it by hand here` +
    (what === "answer" ? ", or edit the saved answer." : ".");
}

function fire(el: HTMLElement, type: string): void {
  el.dispatchEvent(new (winOf(el).Event)(type, { bubbles: true }));
}

/** The pointer sequence real clicks produce; some libraries open on mousedown, not click. */
function realClick(el: HTMLElement): void {
  const w = winOf(el);
  const init = { bubbles: true, cancelable: true, composed: true, view: w, button: 0 };
  for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup", "click"]) {
    const Ctor = type.startsWith("pointer") && typeof w.PointerEvent === "function" ? w.PointerEvent : w.MouseEvent;
    el.dispatchEvent(new Ctor(type, init));
  }
}

function key(el: HTMLElement, k: string): void {
  const w = winOf(el);
  for (const type of ["keydown", "keyup"]) el.dispatchEvent(new w.KeyboardEvent(type, { key: k, bubbles: true, cancelable: true, composed: true }));
}

/** The page's own `value` setter, so a framework that wraps the property still hears the write. */
function assign(el: HTMLInputElement | HTMLTextAreaElement, v: string): string {
  const w = winOf(el);
  const proto = el.localName === "textarea" ? w.HTMLTextAreaElement.prototype : w.HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(proto, "value")?.set;
  if (setter) setter.call(el, v); else el.value = v;
  return el.value; // what the control took, before any page handler has run
}

/**
 * Set a text value the way a user would, so React/Vue-controlled inputs see it. Returns what the
 * control held right after the write (the browser's own sanitising applied, no handler run yet).
 */
function setText(el: HTMLInputElement | HTMLTextAreaElement, v: string): string {
  const held = assign(el, v);
  fire(el, "input");
  fire(el, "change");
  return held;
}

/**
 * The second way to write: focus, the native setter, an `input` event the way typing makes it
 * (`inputType: insertText`), `change`, blur. Some frameworks only accept a value that arrives so.
 */
function setTextAsTyped(el: HTMLInputElement | HTMLTextAreaElement, v: string): string {
  const w = winOf(el);
  // The retry must not move the page under the person: no scroll to the field, and no blur (with the validation a
  // blur can trigger) on a field they were already in. A field that was not focused is blurred as typing would end,
  // and focus goes back to whatever had it.
  const prev = activeIn(el);
  const wasActive = prev === el;
  el.focus({ preventScroll: true });
  const held = assign(el, v);
  const Ctor = (w as { InputEvent?: typeof InputEvent }).InputEvent ?? w.Event;
  el.dispatchEvent(new Ctor("input", { bubbles: true, inputType: "insertText", data: v } as InputEventInit));
  fire(el, "change");
  if (!wasActive) {
    el.blur();
    if (prev && prev !== el.ownerDocument.body && prev.isConnected && typeof (prev as HTMLElement).focus === "function") (prev as HTMLElement).focus({ preventScroll: true });
  }
  return held;
}

/** The element that has focus in `el`'s own tree (its shadow root, else its document). */
function activeIn(el: Element): Element | null {
  return (el.getRootNode() as Partial<DocumentOrShadowRoot>).activeElement ?? el.ownerDocument.activeElement ?? null;
}

/** How long a write is given to be reverted by the page before it is judged: one frame, never more than this. */
const SETTLE_MS = 50;
const FRAME_MS = 16;
export const KEPT_NOT = "the page did not keep the value";

/**
 * Resolve after the page has had a turn to react to a write: a microtask (a handler that reverts
 * in a promise), then one animation frame, or at most SETTLE_MS when no frame comes (a hidden tab).
 * The timer is released however it ends.
 */
function settle(el: Element): Promise<void> {
  const w = winOf(el);
  const raf = typeof w.requestAnimationFrame === "function" ? w.requestAnimationFrame.bind(w) : null;
  return new Promise<void>(resolve => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const done = (): void => { clearTimeout(timer); resolve(); };
    queueMicrotask(() => {
      // A hidden tab paints no frames and the browser holds its timers to about one a second, so a plain
      // write there would cost a second each: it gets one turn of the event loop instead, and what it
      // keeps is only provisional (writeKept's recheck, confirmLate).
      if (el.ownerDocument.hidden === true && typeof MessageChannel === "function") {
        const c = new MessageChannel();
        c.port1.onmessage = () => { c.port1.close(); done(); };
        c.port2.postMessage(0);
        return;
      }
      timer = setTimeout(done, raf ? SETTLE_MS : FRAME_MS);
      raf?.(done);
    });
  });
}

/**
 * Write `v` into a text control and confirm the page kept it. A controlled input (React, Angular)
 * can take the write and put its own state back a moment later, and the field then looks empty
 * while Fill reports it filled. `kept` says whether what the field holds now is a value the page
 * accepted. A reverted write is retried once the other way (setTextAsTyped); a second revert is
 * "lost", never "kept". "empty": the control itself refused the text (a number box given letters).
 *
 * In a hidden tab the check after a write is one turn of the event loop (see settle), too short for a page that
 * reverts in a timer or a frame. A "kept" there is only provisional: `recheck` reads the field again, and the
 * caller calls it once the whole pass is done (confirmLate), after one bounded wait shared by every field, so a
 * revert that lands later is reported "manual", never "filled".
 */
interface WriteResult { verdict: "kept" | "empty" | "lost"; recheck?: () => boolean }

async function writeKept(
  el: HTMLInputElement | HTMLTextAreaElement, v: string, kept: (now: string, wrote: string, before: string) => boolean,
): Promise<WriteResult> {
  const before = el.value;
  let wrote = setText(el, v);
  if (wrote === "") return { verdict: "empty" };
  await settle(el);
  let ok = kept(el.value, wrote, before);
  if (!ok) {
    wrote = setTextAsTyped(el, v);
    if (wrote === "") return { verdict: "empty" };
    await settle(el);
    ok = kept(el.value, wrote, before);
  }
  if (!ok) return { verdict: "lost" };
  const held = wrote;
  return el.ownerDocument.hidden === true ? { verdict: "kept", recheck: () => kept(el.value, held, before) } : { verdict: "kept" };
}

/**
 * A text write the page kept: the field is not empty and not back to what it held before. A mask that
 * reformats the text ("5550100" to "(555) 0100") still counts: the page took it and made it its own, and
 * when it formats the text into exactly what the field held before ("5550100" over "(555) 010-0") the
 * letters and digits give it away: the same ones that were written are there.
 */
const textKept = (now: string, wrote: string, before: string): boolean => now !== "" && (now !== before || fold(now) === fold(wrote));
/** A text with its case, spacing and punctuation taken out: what a mask leaves alone. */
const fold = (s: string): string => s.toLowerCase().replace(/[^\p{L}\p{N}]/gu, "");
/** A date control's value is its ISO text exactly, or it was not taken. */
const sameKept = (now: string, wrote: string): boolean => now === wrote;

/** The option a single select is showing, or "" when nothing is selected. */
function selectedText(el: HTMLSelectElement): string {
  const i = el.selectedIndex;
  if (i < 0) return "";
  return el.options[i]?.text ?? "";
}

/**
 * A single select takes the option whose text is the saved value. A multiple
 * select takes every saved value that maps (mapChoice) onto one of its options,
 * and ends with exactly those selected; `partial` says a saved value had no
 * option, so the fill is flagged for review. Nothing is reported unless the
 * control now shows what was chosen.
 */
function setSelect(el: HTMLSelectElement, values: string[]): { ok: boolean; partial: boolean } {
  if (!el.multiple) {
    const wanted = values.map(normalizeLabel).filter(v => v !== "");
    if (wanted.length === 0) return { ok: false, partial: false };
    const want = new Set(wanted);
    const o = Array.from(el.options).find(x => want.has(normalizeLabel(x.text)));
    if (!o) return { ok: false, partial: false };
    // Captured before the events: a handler can rewrite the option's text and still leave its value.
    const label = normalizeLabel(o.text);
    const value = o.value;
    try {
      el.value = value;
      fire(el, "input"); fire(el, "change");
    } catch {
      return { ok: false, partial: false };
    }
    const kept = el.value === value && normalizeLabel(selectedText(el)) === label;
    return { ok: kept, partial: false };
  }
  const options = Array.from(el.options);
  const texts = options.map(o => o.text);
  const chosen = new Set<number>();
  let missing = 0;
  for (const v of values) {
    if (normalizeLabel(v) === "") continue;
    const t = mapChoice(v, texts);
    const i = t === null ? -1 : texts.indexOf(t);
    if (i >= 0) chosen.add(i); else missing++;
  }
  if (chosen.size === 0) return { ok: false, partial: false };
  options.forEach((o, i) => { o.selected = chosen.has(i); });
  fire(el, "input"); fire(el, "change");
  const shownAll = [...chosen].every(i => options[i]!.selected);
  return { ok: shownAll, partial: missing > 0 };
}

function optionText(el: HTMLInputElement): string {
  const lab = el.labels?.[0];
  const t = lab ? (lab.textContent ?? "") : (el.getAttribute("aria-label") ?? el.value);
  return normalizeLabel(t);
}

/**
 * Click radio/checkbox members to reach the wanted state; a click is how sites listen.
 * Success is the checked state afterwards. A click that does not stick, or an empty wanted
 * value, is not a fill.
 */
function setGroup(els: HTMLInputElement[], values: string[], single: boolean): boolean {
  const labels = values.map(normalizeLabel).filter(v => v !== "");
  if (labels.length === 0) return false;
  if (single) {
    const box = els[0];
    if (!box) return false;
    const want = values[0] === "yes";
    if (box.checked !== want) box.click();
    return box.checked === want;
  }
  const want = new Set(labels);
  for (const el of els) {
    const on = want.has(optionText(el));
    if (el.type === "radio") { if (on && !el.checked) el.click(); }
    else if (el.checked !== on) el.click();
  }
  let matched = 0;
  for (const el of els) {
    const on = want.has(optionText(el));
    if (on) matched++;
    if (el.checked !== on) return false;
  }
  return matched > 0;
}

// --- ARIA drivers ---------------------------------------------------------------------

const visibleOptions = (s: ParentNode): HTMLElement[] =>
  Array.from(s.querySelectorAll<HTMLElement>("[role=option]")).filter(o => o.getClientRects().length > 0);

/**
 * The options the widget is showing. A widget that names its list
 * (aria-controls/aria-owns, or is itself the listbox) shows exactly that list:
 * options of any other widget on the page are never its options. One that does
 * not name a list (a popup appended to the page when it opens) shows the options
 * that were not already on screen before it was opened (`before`).
 */
function shown(trigger: HTMLElement, before: ReadonlySet<HTMLElement>): HTMLElement[] {
  const lb = trigger.getAttribute("role") === "listbox" ? trigger : ownedListbox(trigger);
  if (lb) return visibleOptions(lb);
  for (const s of [trigger.getRootNode() as ParentNode, trigger.ownerDocument]) {
    const opts = visibleOptions(s).filter(o => !before.has(o));
    if (opts.length) return opts;
  }
  return [];
}

/**
 * Open a combobox/listbox/button dropdown, click the option `pick` chooses
 * from the option texts, and check the widget shows it. A typeahead input is
 * typed into only when `typeahead` is true (the user opted in: typing can make
 * the site run its own autocomplete request) and the open list has no
 * acceptable option; then it is typed into once, and cleared again if that did
 * not help. Returns false, leaving the field as it was, when any step does not
 * happen; "check" when the choice was made but the widget cannot show which
 * (fill it for review); "ok" when it shows the chosen option.
 */
async function driveCombo(trigger: HTMLElement, pick: (options: string[]) => string | null, typed: string, typeahead = false): Promise<false | "ok" | "check"> {
  const isInput = trigger.localName === "input";
  // Watch the page and, for a widget inside a shadow root, that root too: its list
  // usually renders there, where a document observer hears nothing.
  const root = widgetRoots(trigger);
  const before = new Set(visibleOptions(trigger.ownerDocument)); // options on screen before this widget is opened are not its own
  const showing = (): HTMLElement[] => shown(trigger, before);
  const choose = (): HTMLElement | null => {
    const opts = showing();
    const texts = opts.map(txt);
    const p = pick(texts);
    return p === null ? null : opts[texts.indexOf(p)] ?? null;
  };
  let typedHere = false;
  const giveUp = (): false => {
    if (typedHere) setText(trigger as HTMLInputElement, "");
    if (showing().length > 0) key(trigger, "Escape");
    return false;
  };
  if (showing().length === 0 && trigger.getAttribute("role") !== "listbox") {
    trigger.focus();
    realClick(trigger);
    if (!(await waitFor(() => showing().length > 0, 500, root))) {
      key(trigger, "ArrowDown");
      await waitFor(() => showing().length > 0, 300, root);
    }
  }
  let target = choose();
  if (!target && isInput && typed && typeahead) {
    setText(trigger as HTMLInputElement, typed);
    typedHere = true;
    await waitFor(() => (target = choose()) !== null, 900, root);
  }
  if (!target) return giveUp();
  const chosenText = txt(target);
  const want = normalizeLabel(chosenText);
  const wantDigits = codeDigits(chosenText);
  const icon = iconSignature(target);
  realClick(target);
  let verdict: "ok" | "check" | null = null;
  // What the widget shows must be the chosen option. A longer or shorter label that merely
  // overlaps it is a different option. A box that shows only a dialling code ("+9", "(+9)") can
  // stand for the option only when those digits are the option's own, read before the click.
  // The flag beside it must then be the chosen option's flag; with no flag the fill is a review.
  // A different code, or a code on an option that has none, is not that option.
  const judge = (): "ok" | "check" | null => {
    for (const v of readWidgetValue(trigger)) {
      const n = normalizeLabel(v);
      if (n === "") continue;
      if (n === want) return "ok";
      if (!dialCodeOnly(v) || wantDigits === "" || codeDigits(v) !== wantDigits) continue;
      const shownIcon = widgetValueIcon(trigger);
      if (icon && shownIcon) return icon === shownIcon ? "ok" : null;
      return "check";
    }
    return null;
  };
  // A widget that already shows the chosen option needs no wait for a verdict.
  verdict = judge();
  if (verdict === null && (!(await waitFor(() => (verdict = judge()) !== null, 600, root)) || verdict === null)) return giveUp();
  if (showing().length > 0 && trigger.getAttribute("role") !== "listbox") key(trigger, "Escape");
  return verdict;
}

function pickFrom(values: string[]): (options: string[]) => string | null {
  return options => {
    for (const v of values) { const r = mapChoice(v, options); if (r !== null) return r; }
    return null;
  };
}

/**
 * Choose every saved value in a multi-select widget by clicking its existing
 * options, one at a time, each through mapChoice, and require the widget to
 * show the chip or selection afterwards (driveCombo checks that per value).
 * It only adds: values already shown are kept, nothing is removed. A value with
 * no option is skipped and the fill is flagged for review; none chosen at all
 * is "manual".
 */
async function fillMulti(el: HTMLElement, values: string[], typeahead: boolean): Promise<Result> {
  let chosen = 0;
  for (const v of values) {
    if (mapChoice(v, readWidgetValue(el)) !== null) { chosen++; continue; }
    const have = new Set(readWidgetValue(el).map(normalizeLabel));
    const ok = await driveCombo(el, opts => mapChoice(v, opts.filter(o => !have.has(normalizeLabel(o)))), v, typeahead);
    if (ok !== false) chosen++;
  }
  return chosen === 0 ? { ok: false, reason: "manual" } : { ok: true, review: chosen < values.length };
}

/** ISO text for a native date or month input, or null when the saved value cannot be written there. */
function isoFor(type: "date" | "month", saved: string): string | null {
  const p = parseDate(saved);
  if (!p) return null;
  if (type === "month") return `${p.y}-${p.m}`;
  return p.d ? `${p.y}-${p.m}-${p.d}` : null; // a month with no day is not a date
}

async function fillAriaRadio(ref: FieldRef, values: string[]): Promise<boolean> {
  const want = new Set(values.map(normalizeLabel));
  const i = ref.info.options.findIndex(o => want.has(normalizeLabel(o)));
  const el = ref.els[i];
  if (!el) return false;
  if (el.getAttribute("aria-checked") !== "true") realClick(el);
  return waitFor(() => el.getAttribute("aria-checked") === "true", 400, widgetRoots(el));
}

async function fillAriaCheckbox(el: HTMLElement, values: string[]): Promise<boolean> {
  const want = values[0] === "yes";
  const is = () => el.getAttribute("aria-checked") === "true";
  if (is() !== want) realClick(el);
  return waitFor(() => is() === want, 400, widgetRoots(el));
}

/** The roots a widget's changes can land in: its document, plus its shadow root when it has one. */
export function widgetRoots(el: Element): Node[] {
  const doc = el.ownerDocument;
  const own = el.getRootNode();
  return own !== doc && own.nodeType === 11 ? [doc, own] : [doc];
}

function setEditable(el: HTMLElement, text: string): boolean {
  const same = () => (el.innerText || el.textContent || "").replace(/\s+/g, " ").trim() === text.replace(/\s+/g, " ").trim();
  el.focus();
  try {
    el.ownerDocument.execCommand("selectAll", false);
    el.ownerDocument.execCommand("insertText", false, text);
  } catch { /* fall through to the direct write */ }
  if (!same()) { el.textContent = text; fire(el, "input"); }
  return same();
}

// --- split fields -------------------------------------------------------------------------

function setPart(el: HTMLElement, role: string, want: string): boolean {
  if (el.localName === "select") {
    const s = el as HTMLSelectElement;
    const n = Number(want);
    const o = Array.from(s.options).find(x => {
      const t = x.text.trim();
      if (role === "month") return monthIndex(t) === n || monthIndex(x.value) === n;
      if (role === "day") return Number(t) === n && t !== "";
      return t === want || x.value === want;
    });
    if (!o) return false;
    s.value = o.value;
    fire(s, "input"); fire(s, "change");
    return s.value === o.value;
  }
  setText(el as HTMLInputElement, want);
  return (el as HTMLInputElement).value !== "";
}

/** Write one ISO date into its month/day/year controls; a missing day leaves the day control alone (review). */
function fillSplitDate(ref: FieldRef, saved: string): { ok: boolean; partial: boolean; long?: string } {
  const p = parseDate(saved);
  if (!p) return { ok: false, partial: false };
  let any = false, partial = false;
  let long: string | undefined;
  ref.els.forEach((el, k) => {
    const role = ref.parts?.[k] ?? "";
    const want = role === "year" ? p.y : role === "month" ? p.m : p.d;
    if (!want) { partial = true; return; }
    const tooBig = tooLong(el, want, "answer");
    if (tooBig) { partial = true; long ??= tooBig; return; } // a part that will not fit its box is left for review, not cut
    if (setPart(el, role, want)) any = true;
  });
  return long === undefined ? { ok: any, partial } : { ok: any, partial, long };
}

function codeDigitsOffered(el: HTMLElement): string[] {
  const texts = el.localName === "select" ? Array.from((el as HTMLSelectElement).options).map(o => o.text) : comboOptions(el);
  return texts.map(codeDigits).filter(Boolean);
}

async function fillSplitPhone(ref: FieldRef, saved: string): Promise<Result> {
  const [codeEl, numEl] = ref.els as [HTMLElement, HTMLElement];
  const p = parsePhone(saved, codeDigitsOffered(codeEl));
  const none: Result = { ok: false, reason: "no_option" };
  if (!p) return none;
  // Nothing is written unless both parts fit their boxes: a number without its code would land under the wrong country.
  const codeTyped = codeEl.localName !== "select" && codeEl.getAttribute("role") !== "combobox" && codeEl.getAttribute("aria-haspopup") !== "listbox";
  const long = tooLong(numEl, p.national, "answer") ?? (p.cc && codeTyped ? tooLong(codeEl, `+${p.cc}`, "answer") : null);
  if (long) return { ok: false, reason: "too_long", detail: long };
  if (p.cc) {
    let ok: boolean;
    if (codeEl.localName === "select") {
      const s = codeEl as HTMLSelectElement;
      const o = Array.from(s.options).find(x => codeDigits(x.text) === p.cc);
      ok = !!o;
      if (o) { s.value = o.value; fire(s, "input"); fire(s, "change"); }
    } else if (codeEl.getAttribute("role") === "combobox" || codeEl.getAttribute("aria-haspopup") === "listbox") {
      ok = (await driveCombo(codeEl, opts => opts.find(o => codeDigits(o) === p.cc) ?? null, "", false)) !== false;
    } else {
      setText(codeEl as HTMLInputElement, `+${p.cc}`);
      ok = true;
    }
    if (!ok) return none; // a number without its code would land under the wrong country
  }
  setText(numEl as HTMLInputElement, p.national);
  return (numEl as HTMLInputElement).value !== "" ? { ok: true } : none;
}

// --- the fill loop ------------------------------------------------------------------------------

function hasValue(ref: FieldRef): boolean {
  const first = ref.els[0]!;
  switch (ref.widget) {
    case "split-date": return ref.els.some(e => (e as HTMLInputElement).value !== "");
    case "split-phone": return (ref.els[1] as HTMLInputElement).value !== "";
    case "combobox": return readWidgetValue(first).length > 0;
    case "aria-radio": return ref.els.some(e => e.getAttribute("aria-checked") === "true");
    case "aria-checkbox": return first.getAttribute("aria-checked") === "true";
    case "contenteditable": return (first.innerText || first.textContent || "").trim() !== "";
    default: break;
  }
  if (first.localName === "select") {
    const s = first as HTMLSelectElement;
    return s.multiple ? s.selectedOptions.length > 0 : s.value !== "";
  }
  const k = ref.info.kind;
  if (k === "radio" || (k === "checkbox" && ref.els.length > 1)) return ref.els.some(e => (e as HTMLInputElement).checked);
  if (k === "checkbox") return (first as HTMLInputElement).checked;
  if (k === "file") return ((first as HTMLInputElement).files?.length ?? 0) > 0;
  return ((first as HTMLInputElement).value ?? "") !== "";
}

/** The element a field's outline goes on. */
function markTarget(ref: FieldRef): HTMLElement {
  const first = ref.els[0]!;
  if (ref.els.length > 1 && ref.widget !== "split-phone") return (first.closest("fieldset, [role=radiogroup], [role=group]") as HTMLElement | null) ?? first.parentElement ?? first;
  if (ref.widget === "split-phone" || ref.widget === "split-date") return first.parentElement ?? first;
  if (first.localName === "input" && ref.widget === "combobox") return first.parentElement ?? first;
  // An upload input is usually hidden behind its button: outline the widget around it.
  if (ref.info.kind === "file") return (first.closest("[role=group], fieldset") as HTMLElement | null) ?? first.parentElement ?? first;
  return first;
}

function mark(ref: FieldRef, review: boolean, title: string): void {
  const target = markTarget(ref);
  target.style.outline = `2px solid ${review ? COLORS.review : COLORS.ok}`;
  target.style.outlineOffset = "2px";
  target.setAttribute("data-vani-fill", review ? "review" : "ok");
  target.setAttribute("data-vani-why", title);
  marked.add(target);
}

function unmark(el: HTMLElement): void {
  el.style.outline = "";
  el.style.outlineOffset = "";
  el.removeAttribute("data-vani-fill");
  el.removeAttribute("data-vani-why");
}

/**
 * Take the outlines off. When the person clears them (the default) they have looked at what Fill wrote, so it is
 * no longer "still exactly what Fill wrote": the record of that goes too (extract.recordFill). A new Fill clears
 * the outlines only (`forget: false`): the fields it then skips as already filled still hold the earlier fill,
 * and must still read as Vani's when the form is saved.
 */
export function clearMarks(root: ParentNode = document, opts: { forget?: boolean } = {}): void {
  for (const el of marked) unmark(el);
  marked.clear();
  root.querySelectorAll<HTMLElement>("[data-vani-fill]").forEach(unmark);
  if (opts.forget !== false) forgetFills();
}

type Result = { ok: boolean; review?: boolean; reason?: FillOutcome["reason"]; detail?: string; recheck?: () => boolean };

/** A write that was not kept is "manual" (never filled); one the control itself refused has no option for it. */
function keptResult(w: WriteResult): Result {
  if (w.verdict === "kept") return { ok: true, ...(w.recheck ? { recheck: w.recheck } : {}) };
  return w.verdict === "lost" ? { ok: false, reason: "manual", detail: KEPT_NOT } : { ok: false, reason: "no_option" };
}

/**
 * Settle the provisional writes of a hidden tab (WriteResult.recheck): one wait of at most SETTLE_MS for the lot
 * (a hidden tab's browser may stretch it, once per fill, not once per field), then each is read again. Returns
 * the ones the page took back; their outline comes off here, and the caller reports them not kept.
 */
async function confirmLate(ws: Written[]): Promise<Set<Written>> {
  const lost = new Set<Written>();
  const pending = ws.filter(w => w.recheck);
  if (pending.length === 0) return lost;
  await new Promise<void>(resolve => { setTimeout(resolve, SETTLE_MS); });
  for (const w of pending) {
    let held = false;
    try { held = w.recheck!(); } catch { /* an unreadable field is not claimed as kept */ }
    if (held) continue;
    const target = markTarget(w.ref);
    unmark(target);
    marked.delete(target);
    lost.add(w);
  }
  return lost;
}

/** A filled outcome the page then reverted. */
const revertedOutcome = (o: FillOutcome): FillOutcome => ({ label: o.label, filled: false, reason: "manual", detail: KEPT_NOT, ...(o.key ? { key: o.key } : {}) });

async function fillOne(ref: FieldRef, m: Match, typeahead: boolean): Promise<Result> {
  const el = ref.els[0]!;
  switch (ref.widget) {
    case "combobox":
    {
      if (ref.info.multi) return fillMulti(el, m.value, typeahead);
      const r = await driveCombo(el, pickFrom(m.value), m.value[0] ?? "", typeahead);
      return r ? { ok: true, review: r === "check" } : { ok: false, reason: "manual" };
    }
    case "aria-radio": return (await fillAriaRadio(ref, m.value)) ? { ok: true } : { ok: false, reason: "no_option" };
    case "aria-checkbox": return (await fillAriaCheckbox(el, m.value)) ? { ok: true } : { ok: false, reason: "manual" };
    case "contenteditable": return setEditable(el, m.value[0] ?? "") ? { ok: true } : { ok: false, reason: "manual" };
    case "split-date": {
      const r = fillSplitDate(ref, m.value[0] ?? "");
      if (r.ok) return { ok: true, review: r.partial, ...(r.long ? { detail: r.long } : {}) };
      return r.long ? { ok: false, reason: "too_long", detail: r.long } : { ok: false, reason: "no_option" };
    }
    case "split-phone": return fillSplitPhone(ref, m.value[0] ?? "");
    default: break;
  }
  if (el.localName === "select") {
    const r = setSelect(el as HTMLSelectElement, m.value);
    return r.ok ? { ok: true, review: r.partial } : { ok: false, reason: "no_option" };
  }
  if (ref.info.kind === "radio" || ref.info.kind === "checkbox") {
    return setGroup(ref.els as HTMLInputElement[], m.value, ref.info.kind === "checkbox" && ref.info.options.length <= 1) ? { ok: true } : { ok: false, reason: "no_option" };
  }
  const v = m.value[0] ?? "";
  const type = el.localName === "input" ? (el as HTMLInputElement).type : "";
  if (type === "date" || type === "month") {
    // The control only accepts its own ISO form and blanks anything else: write that, and read it back.
    const iso = isoFor(type, v);
    if (iso === null) return { ok: false, reason: "no_option" };
    return keptResult(await writeKept(el as HTMLInputElement, iso, sameKept));
  }
  const long = tooLong(el, v, "answer");
  if (long) return { ok: false, reason: "too_long", detail: long };
  if (v === "") { setText(el as HTMLInputElement, v); return { ok: true }; }
  return keptResult(await writeKept(el as HTMLInputElement, v, textKept));
}

/** Fill one field with a chosen match (from matchField, or a suggestion the user picked). */
export interface FillOptions {
  /** Replace values the user already typed. */
  overwrite?: boolean;
  /** Let a search-as-you-type input be typed into when its list has no matching option. Off by default: the site may run its own query. */
  typeahead?: boolean;
  /**
   * The resume chosen for this job, asked for only when the form has a resume input, and at most
   * once per fill. Without it every file input is left alone ("untouchable").
   */
  resume?: () => Promise<ResumeFetch>;
  /** Matches already computed for these refs (coverageOf's `matches`); fillAll matches only the refs missing from it. */
  matches?: Map<FieldRef, Match | null>;
}

// --- undo ---------------------------------------------------------------------------------------

/** What it takes to put a field back as it was before Fill wrote it, or null when it cannot be done safely. */
function restoreFor(ref: FieldRef): (() => void) | null {
  const els = ref.els;
  const el = els[0]!;
  switch (ref.widget) {
    // A widget Fill drove by clicking its options has no safe way back: clicking again chooses, it does not clear.
    case "combobox": case "aria-radio": return null;
    case "aria-checkbox": {
      const was = el.getAttribute("aria-checked") === "true";
      return () => { if ((el.getAttribute("aria-checked") === "true") !== was) realClick(el); };
    }
    case "contenteditable": {
      const was = el.textContent ?? "";
      return () => { if (was === "") { el.textContent = ""; fire(el, "input"); } else setEditable(el, was); };
    }
    case "split-date": case "split-phone": {
      if (els.some(p => p.getAttribute("role") === "combobox" || p.getAttribute("aria-haspopup") === "listbox")) return null;
      const was = els.map(p => (p as HTMLInputElement).value);
      return () => els.forEach((p, i) => {
        if (p.localName === "select") { (p as HTMLSelectElement).value = was[i]!; fire(p, "input"); fire(p, "change"); }
        else setText(p as HTMLInputElement, was[i]!);
      });
    }
    default: break;
  }
  if (el.localName === "select") {
    const s = el as HTMLSelectElement;
    const was = Array.from(s.options).map(o => o.selected);
    return () => {
      if (s.multiple) Array.from(s.options).forEach((o, i) => { o.selected = was[i] ?? false; });
      else s.selectedIndex = was.indexOf(true);
      fire(s, "input"); fire(s, "change");
    };
  }
  if (ref.info.kind === "radio" || ref.info.kind === "checkbox") {
    const members = els as HTMLInputElement[];
    const was = members.map(m => m.checked);
    return () => members.forEach((m, i) => {
      if (m.checked === was[i]) return;
      if (m.type === "checkbox" || was[i]) m.click(); // a click is how sites listen; a radio cannot be clicked off
      else { m.checked = false; fire(m, "input"); fire(m, "change"); }
    });
  }
  const was = (el as HTMLInputElement).value;
  return () => { setText(el as HTMLInputElement, was); };
}

/** One field Fill wrote: enough to record it and, if asked, to put it back. */
interface Written {
  ref: FieldRef;
  key: string;
  review: boolean;
  /** The field's value before Fill wrote it. */
  before: string[];
  restore: (() => void) | null;
  /** What the field held right after Fill wrote it (set by remember). */
  after: string[];
  /** Set for a write made in a hidden tab, whose "kept" is provisional until confirmLate has read it again. */
  recheck?: () => boolean;
}

/** The last fill's writes, so "Undo fill" can put them back. Bounded to one fill (beginFill) and to the scan's field cap. */
let lastFill: Written[] = [];
const MAX_UNDO = 500;

/** A new fill starts: the one before it can no longer be undone. */
export function beginFill(): void {
  lastFill = [];
}

/** How many fields the last fill wrote and has not yet been undone. */
export function undoCount(): number {
  return lastFill.length;
}

/** Record each field Fill wrote (extract.recordFill) and keep it for undo. */
function remember(ws: Written[]): void {
  for (const w of ws) {
    try {
      w.after = recordFill(w.ref, w.key, w.review);
    } catch { continue; } // an unreadable field is a field never claimed as Vani's own
    lastFill.push(w);
  }
  if (lastFill.length > MAX_UNDO) lastFill = lastFill.slice(lastFill.length - MAX_UNDO);
}

export interface UndoReport {
  /** Fields put back as they were, outline and record removed. */
  restored: number;
  /** Fields the person has changed since: left exactly as they are. */
  edited: number;
  /** Fields Fill wrote that cannot be put back (a dropdown it drove) or did not go back; they still hold Vani's value. */
  manual: number;
  /** Fields no longer on the page. */
  gone: number;
}

/**
 * Put back what the last fill wrote, in the fields that still hold exactly that. A field the person
 * has edited since is never touched, whatever it holds. Each restored field loses its outline and its
 * record. The last fill is spent afterwards: a second call undoes nothing.
 */
export async function undoFill(): Promise<UndoReport> {
  const todo = lastFill;
  lastFill = [];
  const report: UndoReport = { restored: 0, edited: 0, manual: 0, gone: 0 };
  const attempted: Written[] = [];
  for (const w of todo) {
    try {
      if (w.ref.els.some(e => !e.isConnected)) { report.gone++; continue; }
      if (!sameValues(readRefValue(w.ref), w.after)) { report.edited++; continue; }
      if (!w.restore) { report.manual++; continue; }
      w.restore();
      attempted.push(w);
    } catch { report.manual++; }
  }
  if (attempted.length > 0) await settle(attempted[0]!.ref.els[0]!); // a widget answers a click a moment later
  for (const w of attempted) {
    let back = false;
    try { back = sameValues(readRefValue(w.ref), w.before); } catch { /* counted below */ }
    if (!back) { report.manual++; continue; }
    const target = markTarget(w.ref);
    unmark(target);
    marked.delete(target);
    forgetFill(w.ref);
    // A Suggest answer filled here was tagged a draft; with it undone, what the person writes next is their own.
    for (const e of w.ref.els) { e.removeAttribute("data-vani-draft"); e.removeAttribute("data-vani-draft-text"); }
    report.restored++;
  }
  return report;
}

// --- the fill of one field -------------------------------------------------------------------------

async function fillTracked(ref: FieldRef, m: Match, opts: FillOptions): Promise<{ outcome: FillOutcome; written?: Written }> {
  const label = ref.info.label;
  const key = m.answer.key;
  const no = (outcome: FillOutcome) => ({ outcome });
  if (ref.info.kind === "file" || ref.info.kind === "password") return no({ label, filled: false, reason: "untouchable" });
  if (ref.manual) return no({ label, filled: false, reason: "manual", key });
  if (ref.els.some(e => !e.isConnected)) return no({ label, filled: false, reason: "detached", key });
  try {
    if (!opts.overwrite && hasValue(ref)) return no({ label, filled: false, reason: "has_value", key });
    let before: string[] = [];
    let restore: (() => void) | null = null;
    try { before = readRefValue(ref); restore = restoreFor(ref); } catch { /* nothing to put back: undo reports it by hand */ }
    const r = await fillOne(ref, m, opts.typeahead === true);
    if (r.reason === "too_long" && r.detail) {
      // Left empty on purpose: outline it, so the person sees the field was not answered and why.
      mark(ref, true, r.detail);
      return no({ label, filled: false, reason: "too_long", detail: r.detail, key });
    }
    if (!r.ok) return no({ label, filled: false, reason: r.reason ?? "no_option", ...(r.detail ? { detail: r.detail } : {}), key });
    const review = m.review || r.review === true;
    mark(ref, review, r.detail ?? `${m.via} ${m.score.toFixed(2)}: ${key}` + (m.note ? `. ${m.note}` : "")); // a part left out for its limit, or why an answer needs a look, says so on the page
    return { outcome: { label, filled: true, via: m.via, score: m.score, review, key }, written: { ref, key, review, before, restore, after: [], ...(r.recheck ? { recheck: r.recheck } : {}) } };
  } catch {
    return no({ label, filled: false, reason: "manual", key });
  }
}

export async function fillRef(ref: FieldRef, m: Match, opts: FillOptions = {}): Promise<FillOutcome> {
  const { outcome, written } = await fillTracked(ref, m, opts);
  if (!written) return outcome;
  if ((await confirmLate([written])).size > 0) return revertedOutcome(outcome);
  remember([written]);
  return outcome;
}

/**
 * Put a model draft into a free-text field, marked yellow and tagged so that,
 * when the form is saved, the answer is stored for review (never as a
 * confident answer). Text fields only; overwrites only with the user's say-so.
 */
export function insertDraft(ref: FieldRef, text: string, opts: { overwrite?: boolean } = {}): FillOutcome {
  const label = ref.info.label;
  const el = ref.els[0];
  const textual = ref.info.kind === "text" || ref.info.kind === "textarea" || ref.widget === "contenteditable";
  if (!el || !textual || ref.els.length !== 1) return { label, filled: false, reason: "untouchable" };
  if (!el.isConnected) return { label, filled: false, reason: "detached" };
  if (!opts.overwrite && hasValue(ref)) return { label, filled: false, reason: "has_value" };
  if (ref.widget === "contenteditable") {
    if (!setEditable(el, text)) return { label, filled: false, reason: "manual" };
  } else {
    const long = tooLong(el, text, "draft");
    if (long) return { label, filled: false, reason: "too_long", detail: long };
    // Not awaited (callers take the outcome at once), so only a page that puts its own value back inside the
    // write is caught here; one that reverts a frame later is not (Fill's own writes wait for that).
    const input = el as HTMLInputElement;
    const before = input.value;
    const wrote = setText(input, text);
    if (!textKept(input.value, wrote, before)) {
      const again = setTextAsTyped(input, text);
      if (!textKept(input.value, again, before)) return { label, filled: false, reason: "manual", detail: KEPT_NOT };
    }
  }
  el.setAttribute("data-vani-draft", "1");
  el.setAttribute("data-vani-draft-text", text);
  mark(ref, true, "draft: review before you submit");
  return { label, filled: true, review: true };
}

/**
 * Put the chosen resume into a file input through DataTransfer, the way a drop does, and
 * read it back: a page that drops the file is "refused", never "filled". Outlined yellow,
 * with the file name, so the owner checks which version went in before submitting.
 */
async function attachResume(ref: FieldRef, get: () => Promise<ResumeFetch>, opts: FillOptions): Promise<FillOutcome> {
  const label = ref.info.label;
  const el = ref.els[0] as HTMLInputElement | undefined;
  if (!el || ref.els.length !== 1) return { label, filled: false, reason: "untouchable" };
  if (!el.isConnected) return { label, filled: false, reason: "detached" };
  if (!opts.overwrite && hasValue(ref)) return { label, filled: false, reason: "has_value" };
  const r = await get();
  if (!r.ok) return { label, filled: false, reason: r.reason, detail: r.detail };
  if (!el.isConnected) return { label, filled: false, reason: "detached" };
  const w = winOf(el);
  try {
    const dt = new w.DataTransfer();
    dt.items.add(new w.File([r.bytes.slice()], r.name, { type: "application/pdf" }));
    el.files = dt.files;
  } catch {
    return { label, filled: false, reason: "refused", detail: "this page did not take the file; attach it by hand" };
  }
  const got = el.files?.[0];
  if (el.files?.length !== 1 || !got || got.name !== r.name || got.size !== r.bytes.length) {
    return { label, filled: false, reason: "refused", detail: "this page did not take the file; attach it by hand" };
  }
  fire(el, "input");
  fire(el, "change");
  const when = r.pickedAt ? `, chosen ${r.pickedAt.slice(0, 10)}` : "";
  mark(ref, true, `resume: ${r.name}${when}. Check it is the version for this job`);
  return { label, filled: true, review: true };
}

/** Widgets that open a list or wait on the page: filled after the plain fields, which settle at once. */
const driven = (ref: FieldRef): boolean => ref.widget === "combobox" || ref.widget === "aria-radio" || ref.widget === "aria-checkbox";

/**
 * Match and fill every field; returns what happened to each, in the order of `refs`. Plain fields
 * (native controls, contenteditable, split date/phone) are filled first, then the comboboxes, aria
 * radios/checkboxes and multi-selects, which wait on the page. `opts.matches` (from coverageOf) lets
 * the page's matches be computed once: a ref found in it is not matched again.
 */
export async function fillAll(refs: FieldRef[], bank: Bank, host: string, opts: FillOptions = {}): Promise<FillOutcome[]> {
  const out = new Array<FillOutcome>(refs.length);
  // One request for the resume per fill, made only if a resume input is there.
  let resume: Promise<ResumeFetch> | undefined;
  const getResume = opts.resume ? () => (resume ??= opts.resume!()) : undefined;
  const written: Written[] = [];
  const at = new Map<Written, number>();
  const one = async (ref: FieldRef): Promise<FillOutcome> => {
    if (ref.info.kind === "file" && getResume && isResumeField(ref.info)) return attachResume(ref, getResume, opts);
    if (ref.info.kind === "file" || ref.info.kind === "password") return { label: ref.info.label, filled: false, reason: "untouchable" };
    const m = opts.matches?.has(ref) ? opts.matches.get(ref) ?? null : matchField(ref.info, bank, host);
    if (!m) return { label: ref.info.label, filled: false, reason: "no_match" };
    const t = await fillTracked(ref, m, opts);
    if (t.written) written.push(t.written);
    return t.outcome;
  };
  for (const wave of [false, true]) {
    for (let i = 0; i < refs.length; i++) {
      if (driven(refs[i]!) === wave) {
        out[i] = await one(refs[i]!);
        const w = written.at(-1);
        if (w && !at.has(w)) at.set(w, i);
      }
    }
  }
  // A hidden tab's writes are only provisionally kept: the ones the page took back after all are reported so, and not remembered.
  const lost = await confirmLate(written);
  for (const w of lost) out[at.get(w)!] = revertedOutcome(out[at.get(w)!]!);
  // Read once the whole pass is done, so a field a later one changed (a country resetting its state) is recorded as it ended up.
  remember(written.filter(w => !lost.has(w)));
  return out;
}
