// Write remembered answers into a form. Never submits, never touches password
// inputs, and by default never overwrites what the user already typed. The one
// file input it fills is the resume, with the PDF the owner chose for this job
// (resume.ts); every other file input is left alone.
// ARIA widgets are driven the way a person would: open, click the option whose
// text maps to the saved answer, and check the widget now shows it. A widget
// that cannot be driven is reported "manual", never "filled".
import type { Bank, Match } from "./types";
import { type FieldRef, comboOptions, iconSignature, ownedListbox, readWidgetValue, widgetValueIcon } from "./extract";
import { mapChoice, matchField } from "./match";
import { dialCodeOnly, normalizeLabel } from "./normalize";
import { codeDigits, monthIndex, parseDate, parsePhone } from "./sections";
import { type ResumeFetch, isResumeField } from "./resume";

export interface FillOutcome {
  label: string;
  filled: boolean;
  via?: Match["via"];
  score?: number;
  review?: boolean;
  key?: string;
  reason?: "no_match" | "has_value" | "no_option" | "untouchable" | "manual" | "detached"
    /** A resume input: nothing chosen for this job, the file could not be had, or the page dropped it. */
    | "no_resume" | "resume_unavailable" | "refused";
  /** Why, in words, for a resume reason. */
  detail?: string;
}

const COLORS = { ok: "#2e8b57", review: "#d4a017" } as const;
const marked = new Set<HTMLElement>();

type Win = Window & typeof globalThis;
const winOf = (el: Element): Win => (el.ownerDocument.defaultView ?? window) as Win;
const txt = (el: Element): string => (el.textContent ?? "").replace(/\s+/g, " ").trim();
const sleep = (ms: number): Promise<void> => new Promise(r => setTimeout(r, ms));

async function until(cond: () => boolean, timeout = 600, step = 25): Promise<boolean> {
  const end = Date.now() + timeout;
  for (;;) {
    if (cond()) return true;
    if (Date.now() >= end) return false;
    await sleep(step);
  }
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

/** Set a text value the way a user would, so React/Vue-controlled inputs see it. */
function setText(el: HTMLInputElement | HTMLTextAreaElement, v: string): void {
  const w = winOf(el);
  const proto = el.localName === "textarea" ? w.HTMLTextAreaElement.prototype : w.HTMLInputElement.prototype;
  const setter = Object.getOwnPropertyDescriptor(proto, "value")?.set;
  if (setter) setter.call(el, v); else el.value = v;
  fire(el, "input");
  fire(el, "change");
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
    const want = new Set(values.map(normalizeLabel));
    const o = Array.from(el.options).find(x => want.has(normalizeLabel(x.text)));
    if (!o) return { ok: false, partial: false };
    el.value = o.value;
    fire(el, "input"); fire(el, "change");
    return { ok: true, partial: false };
  }
  const options = Array.from(el.options);
  const texts = options.map(o => o.text);
  const chosen = new Set<number>();
  let missing = 0;
  for (const v of values) {
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

/** Click radio/checkbox members to reach the wanted state; a click is how sites listen. */
function setGroup(els: HTMLInputElement[], values: string[], single: boolean): boolean {
  if (single) {
    const want = values[0] === "yes";
    if (els[0]!.checked !== want) els[0]!.click();
    return true;
  }
  const want = new Set(values.map(normalizeLabel));
  let any = false;
  for (const el of els) {
    const on = want.has(optionText(el));
    if (el.type === "radio") { if (on && !el.checked) { el.click(); any = true; } else if (on) any = true; }
    else if (el.checked !== on) { el.click(); any = true; } else if (on) any = true;
  }
  return any;
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
    if (!(await until(() => showing().length > 0, 500))) {
      key(trigger, "ArrowDown");
      await until(() => showing().length > 0, 300);
    }
  }
  let target = choose();
  if (!target && isInput && typed && typeahead) {
    setText(trigger as HTMLInputElement, typed);
    typedHere = true;
    await until(() => (target = choose()) !== null, 900);
  }
  if (!target) return giveUp();
  const want = normalizeLabel(txt(target));
  const icon = iconSignature(target);
  realClick(target);
  let verdict: "ok" | "check" | null = null;
  // What the widget shows must be what was chosen. A box that shows only a dialling code ("+1") could
  // be any of the countries that share it: the flag beside it must be the chosen option's flag, and
  // with no flag to compare the fill is only one to review.
  const judge = (): "ok" | "check" | null => {
    for (const v of readWidgetValue(trigger)) {
      const n = normalizeLabel(v);
      if (n === "") continue;
      if (n === want || n.includes(want)) return "ok";
      if (!want.includes(n)) continue;
      if (!dialCodeOnly(v)) return "ok";
      const shown = widgetValueIcon(trigger);
      if (icon && shown) return icon === shown ? "ok" : null;
      return "check";
    }
    return null;
  };
  if (!(await until(() => (verdict = judge()) !== null, 600)) || verdict === null) return giveUp();
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
  return until(() => el.getAttribute("aria-checked") === "true", 400);
}

async function fillAriaCheckbox(el: HTMLElement, values: string[]): Promise<boolean> {
  const want = values[0] === "yes";
  const is = () => el.getAttribute("aria-checked") === "true";
  if (is() !== want) realClick(el);
  return until(() => is() === want, 400);
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
function fillSplitDate(ref: FieldRef, saved: string): { ok: boolean; partial: boolean } {
  const p = parseDate(saved);
  if (!p) return { ok: false, partial: false };
  let any = false, partial = false;
  ref.els.forEach((el, k) => {
    const role = ref.parts?.[k] ?? "";
    const want = role === "year" ? p.y : role === "month" ? p.m : p.d;
    if (!want) { partial = true; return; }
    if (setPart(el, role, want)) any = true;
  });
  return { ok: any, partial };
}

function codeDigitsOffered(el: HTMLElement): string[] {
  const texts = el.localName === "select" ? Array.from((el as HTMLSelectElement).options).map(o => o.text) : comboOptions(el);
  return texts.map(codeDigits).filter(Boolean);
}

async function fillSplitPhone(ref: FieldRef, saved: string): Promise<boolean> {
  const [codeEl, numEl] = ref.els as [HTMLElement, HTMLElement];
  const p = parsePhone(saved, codeDigitsOffered(codeEl));
  if (!p) return false;
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
    if (!ok) return false; // a number without its code would land under the wrong country
  }
  setText(numEl as HTMLInputElement, p.national);
  return (numEl as HTMLInputElement).value !== "";
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

function mark(ref: FieldRef, review: boolean, title: string): void {
  const first = ref.els[0]!;
  let target: HTMLElement;
  if (ref.els.length > 1 && ref.widget !== "split-phone") target = (first.closest("fieldset, [role=radiogroup], [role=group]") as HTMLElement | null) ?? first.parentElement ?? first;
  else if (ref.widget === "split-phone" || ref.widget === "split-date") target = first.parentElement ?? first;
  else if (first.localName === "input" && ref.widget === "combobox") target = first.parentElement ?? first;
  // An upload input is usually hidden behind its button: outline the widget around it.
  else if (ref.info.kind === "file") target = (first.closest("[role=group], fieldset") as HTMLElement | null) ?? first.parentElement ?? first;
  else target = first;
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

export function clearMarks(root: ParentNode = document): void {
  for (const el of marked) unmark(el);
  marked.clear();
  root.querySelectorAll<HTMLElement>("[data-vani-fill]").forEach(unmark);
}

type Result = { ok: boolean; review?: boolean; reason?: FillOutcome["reason"] };

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
    case "split-date": { const r = fillSplitDate(ref, m.value[0] ?? ""); return r.ok ? { ok: true, review: r.partial } : { ok: false, reason: "no_option" }; }
    case "split-phone": return (await fillSplitPhone(ref, m.value[0] ?? "")) ? { ok: true } : { ok: false, reason: "no_option" };
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
    setText(el as HTMLInputElement, iso);
    return (el as HTMLInputElement).value === iso ? { ok: true } : { ok: false, reason: "no_option" };
  }
  setText(el as HTMLInputElement, v);
  return v === "" || (el as HTMLInputElement).value !== "" ? { ok: true } : { ok: false, reason: "no_option" };
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
}

export async function fillRef(ref: FieldRef, m: Match, opts: FillOptions = {}): Promise<FillOutcome> {
  const label = ref.info.label;
  const key = m.answer.key;
  if (ref.info.kind === "file" || ref.info.kind === "password") return { label, filled: false, reason: "untouchable" };
  if (ref.manual) return { label, filled: false, reason: "manual", key };
  if (ref.els.some(e => !e.isConnected)) return { label, filled: false, reason: "detached", key };
  try {
    if (!opts.overwrite && hasValue(ref)) return { label, filled: false, reason: "has_value", key };
    const r = await fillOne(ref, m, opts.typeahead === true);
    if (!r.ok) return { label, filled: false, reason: r.reason ?? "no_option", key };
    const review = m.review || r.review === true;
    mark(ref, review, `${m.via} ${m.score.toFixed(2)}: ${key}`);
    return { label, filled: true, via: m.via, score: m.score, review, key };
  } catch {
    return { label, filled: false, reason: "manual", key };
  }
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
    setText(el as HTMLInputElement, text);
  }
  el.setAttribute("data-vani-draft", "1");
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
    dt.items.add(new w.File([r.bytes], r.name, { type: "application/pdf" }));
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

/** Match and fill every field; returns what happened to each, in order. */
export async function fillAll(refs: FieldRef[], bank: Bank, host: string, opts: FillOptions = {}): Promise<FillOutcome[]> {
  const out: FillOutcome[] = [];
  // One request for the resume per fill, made only if a resume input is there.
  let resume: Promise<ResumeFetch> | undefined;
  const getResume = opts.resume ? () => (resume ??= opts.resume!()) : undefined;
  for (const ref of refs) {
    if (ref.info.kind === "file" && getResume && isResumeField(ref.info)) { out.push(await attachResume(ref, getResume, opts)); continue; }
    if (ref.info.kind === "file" || ref.info.kind === "password") { out.push({ label: ref.info.label, filled: false, reason: "untouchable" }); continue; }
    const m = matchField(ref.info, bank, host);
    if (!m) { out.push({ label: ref.info.label, filled: false, reason: "no_match" }); continue; }
    out.push(await fillRef(ref, m, opts));
  }
  return out;
}
