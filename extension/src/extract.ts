// Read the questions on an application form from the DOM: native controls,
// ARIA widgets (combobox, listbox, radio, checkbox, switch), contenteditable
// editors, open shadow roots and same-origin iframes. Radio/checkbox groups,
// split dates and split phone numbers each become one field. Everything here
// is realm-safe (tag names, not instanceof) because iframe elements belong to
// another window's constructors.
import type { FieldInfo, FieldKind } from "./types";
import { normalizeLabel } from "./normalize";
import {
  type DatePart, assignSections, composeDate, composePhone, datePartRole, isPhoneCode, isPhoneNumber,
} from "./sections";

export type Widget = "native" | "combobox" | "aria-radio" | "aria-checkbox" | "contenteditable" | "split-date" | "split-phone";

export interface FieldRef {
  info: FieldInfo;
  /** The control, every member of a radio/checkbox group, or every part of a split field, in order. */
  els: HTMLElement[];
  widget: Widget;
  /** Why this cannot be driven (shown as "manual"); empty when it can. */
  manual: string;
  /** For split fields: the role of each element in `els` ("month"|"day"|"year", "code"|"number"). */
  parts?: string[];
}

export interface FormScan {
  refs: FieldRef[];
  /** Every document or shadow root walked, so callers can listen and observe in each. */
  roots: ParentNode[];
  /** True when a cap stopped the scan: `refs` is a sample, not the whole page. */
  capped: boolean;
  /** Controls seen after the field cap (at least this many were not read). */
  unread: number;
  /** The element or time budget ran out before the whole tree was walked. */
  nodesCapped: boolean;
}

const SKIP_TYPES = new Set(["hidden", "submit", "button", "reset", "image"]);
const QUESTION_CONTAINER = /(question|field|form-group|formgroup|form-item|input-wrapper|application-|custom-question)/i;
export const MAX_FIELDS = 500;
export const MAX_SCAN_NODES = 40_000;
const MAX_SCAN_MS = 1500;
const MAX_DEPTH = 6;
const MAX_LABEL = 300;
const CANDIDATE = "input, select, textarea, button[aria-haspopup=listbox], [contenteditable], [role=combobox], [role=listbox], [role=radio], [role=checkbox], [role=switch]";
const PLACEHOLDER_TEXT = /^(select|choose|please select|pick|none selected|search|type to search|-+)\b/i;
const SINGLE = "[class*=single-value i], [class*=singlevalue i]";
// Chips of a multi-select: react-select's multi-value, MUI's chip label, or an element whose class is exactly chip/pill/tag/token.
const MULTI = "[class*=multi-value i], [class*=multivalue i], [class*=chip-label i], [class~=chip], [class~=pill], [class~=tag], [class~=token]";
const MULTI_LABEL = "[class*=multi-value__label i], [class*=multivalue__label i], [class*=chip-label i]";
// A wrapper that says the widget takes several values even before any is chosen.
const MULTI_WRAPPER = "[aria-multiselectable=true], [class*=is-multi i], [class*=ismulti i], [class*=multiselect i], [class*=multi-select i]";

function clean(s: string | null | undefined): string {
  return (s ?? "").replace(/\s+/g, " ").trim().slice(0, MAX_LABEL);
}

/** Text of an element without the text of controls or select options inside it. */
function ownText(el: Element): string {
  const clone = el.cloneNode(true) as Element;
  clone.querySelectorAll("input, select, textarea, option, button, script, style, [aria-hidden=true]").forEach(n => n.remove());
  return clean(clone.textContent);
}

/** An element's parent, stepping out of a shadow root to its host. */
function parentOf(n: Element): HTMLElement | null {
  if (n.parentElement) return n.parentElement;
  const root = n.getRootNode() as ShadowRoot;
  return (root.host as HTMLElement | undefined) ?? null;
}

/** ids resolve inside the element's own root (document or shadow root). */
function byIds(ids: string, el: Element): string {
  const root = el.getRootNode() as DocumentFragment;
  const doc = el.ownerDocument;
  return clean(ids.split(/\s+/).map(id => root.getElementById?.(id) ?? doc.getElementById(id)).filter(Boolean).map(n => ownText(n!)).join(" "));
}

function kindOf(el: HTMLElement): FieldKind {
  const t = el.localName;
  if (t === "textarea") return "textarea";
  if (t === "select") return (el as HTMLSelectElement).multiple ? "checkbox" : "select";
  if (t !== "input") return "other";
  switch ((el as HTMLInputElement).type) {
    case "text": return "text";
    case "email": return "email";
    case "tel": return "tel";
    case "url": return "url";
    case "number": return "number";
    case "date": case "month": case "week": return "date";
    case "password": return "password";
    case "file": return "file";
    case "checkbox": return "checkbox";
    case "radio": return "radio";
    case "hidden": return "hidden";
    default: return "other";
  }
}

function visible(el: HTMLElement): boolean {
  if (el.getClientRects().length === 0) {
    // Custom-styled radios and checkboxes often hide the native input; their
    // visible <label> is what the user clicks, so count it.
    const lab = (el as HTMLInputElement).labels?.[0];
    return !!lab && lab.getClientRects().length > 0;
  }
  const cs = el.ownerDocument.defaultView?.getComputedStyle(el);
  return cs ? cs.visibility !== "hidden" : true;
}

/** A site or page search box: what it holds is a query, not an answer to keep. */
function searchBox(el: HTMLElement): boolean {
  return (el as HTMLInputElement).type === "search" || el.getAttribute("role") === "searchbox" || el.closest("[role=search], search") !== null;
}

/**
 * A text box no person can reach: hidden from assistive technology and out of
 * the tab order. React-Select keeps one beside each required dropdown until a
 * value is chosen, only for the browser's required check (job-boards.greenhouse.io);
 * honeypots look the same. A select or a check/radio input hidden this way is
 * often the real value holder of a custom widget, so those are still read.
 */
function requiredProxy(el: HTMLElement, kind: FieldKind): boolean {
  if (el.localName === "select" || kind === "checkbox" || kind === "radio") return false;
  return el.getAttribute("aria-hidden") === "true" && el.tabIndex < 0;
}

/** The question text for a single control. */
export function labelOf(el: HTMLElement): string {
  const lb = el.getAttribute("aria-labelledby");
  if (lb) { const t = byIds(lb, el); if (t) return t; }
  const al = clean(el.getAttribute("aria-label"));
  if (al) return al;
  const labels = (el as HTMLInputElement).labels;
  if (labels && labels.length > 0) { const t = ownText(labels[0]!); if (t) return t; }
  const q = containerQuestion(el);
  if (q) return q;
  return clean(el.getAttribute("placeholder")) || clean(el.getAttribute("title")) ||
    clean(((el as HTMLInputElement).name || el.id || "").replace(/[_\-[\].]+/g, " "));
}

/** The text of an upload button, which names the action and not the document. */
const UPLOAD_VERB = /^(attach|upload|browse|choose|select|add)( (a |your )?(file|files|document))?$/i;

/**
 * A file input's question. Upload widgets often label the hidden input with its
 * button's text ("Attach", job-boards.greenhouse.io); the document it asks for
 * (Resume/CV, Cover Letter) is the label of the group around it.
 */
function fileLabel(el: HTMLElement): string {
  const own = labelOf(el);
  if (own && !UPLOAD_VERB.test(own)) return own;
  return groupLabel([el]) || own;
}

/** A non-labelable widget (role=checkbox, a div combobox) is usually labelled by its own text. */
function widgetLabel(el: HTMLElement): string {
  const lb = el.getAttribute("aria-labelledby");
  if (lb) { const t = byIds(lb, el); if (t) return t; }
  const al = clean(el.getAttribute("aria-label"));
  if (al) return al;
  const labels = (el as HTMLInputElement).labels;
  if (labels && labels.length > 0) { const t = ownText(labels[0]!); if (t) return t; }
  const own = el.getAttribute("role") === "checkbox" || el.getAttribute("role") === "switch" ? ownText(el) : "";
  return own || labelOf(el);
}

/** The question shared by a radio/checkbox group (native or ARIA). */
function groupLabel(els: HTMLElement[], avoid: string[] = []): string {
  const first = els[0]!;
  const group = first.closest("fieldset, [role=radiogroup], [role=group]");
  if (group) {
    const lb = group.getAttribute("aria-labelledby");
    if (lb) { const t = byIds(lb, group); if (t) return t; }
    const al = clean(group.getAttribute("aria-label"));
    if (al) return al;
    const legend = group.querySelector(":scope > legend");
    if (legend) { const t = ownText(legend); if (t) return t; }
  }
  return containerQuestion(first, avoid) || clean(((first as HTMLInputElement).name || first.id || "").replace(/[_\-[\].]+/g, " "));
}

// Walk up a few levels to a question-like container and take its leading
// label-ish text: the shape most ATS forms use when <label for> is missing.
// `avoid` lists normalized texts that are a control's own label (a date part's
// "Month"), never the question.
function containerQuestion(el: HTMLElement, avoid: string[] = []): string {
  let node: HTMLElement | null = parentOf(el);
  for (let depth = 0; node && depth < 5; depth++, node = parentOf(node)) {
    const cls = `${node.className && typeof node.className === "string" ? node.className : ""} ${node.id} ${node.getAttribute("data-automation-id") ?? ""} ${node.getAttribute("data-qa") ?? ""} ${node.getAttribute("data-testid") ?? ""}`;
    if (!QUESTION_CONTAINER.test(cls) && node.tagName !== "FIELDSET") continue;
    for (const cand of Array.from(node.querySelectorAll("legend, label, .label, [class*=label], [class*=Label], h3, h4, p"))) {
      if (cand.contains(el)) continue;
      const t = ownText(cand);
      if (t && !avoid.includes(normalizeLabel(t))) return t;
    }
  }
  return "";
}

function optionLabel(el: HTMLElement): string {
  const labels = (el as HTMLInputElement).labels;
  if (labels && labels.length > 0) { const t = ownText(labels[0]!); if (t) return t; }
  const lb = el.getAttribute("aria-labelledby");
  if (lb) { const t = byIds(lb, el); if (t) return t; }
  return clean(el.getAttribute("aria-label")) || ownText(el) || clean((el as HTMLInputElement).value);
}

function base(el: HTMLElement, kind: FieldKind, label: string): FieldInfo {
  const i = el as HTMLInputElement;
  return {
    kind,
    label,
    name: i.name || el.id || "",
    autocomplete: (el.getAttribute("autocomplete") ?? "").toLowerCase(),
    automationId: el.getAttribute("data-automation-id") ?? el.getAttribute("data-qa") ?? el.getAttribute("data-testid") ?? "",
    options: [],
    required: i.required === true || el.getAttribute("aria-required") === "true",
    value: [],
    drafted: el.getAttribute("data-vani-draft") === "1",
  };
}

// --- ARIA widgets ------------------------------------------------------------------

function isCombo(el: HTMLElement): boolean {
  const t = el.localName;
  return el.getAttribute("role") === "combobox" ||
    ((t === "button" || t === "input") && el.getAttribute("aria-haspopup") === "listbox");
}

export function ownedListbox(el: HTMLElement): HTMLElement | null {
  const root = el.getRootNode() as DocumentFragment;
  for (const a of ["aria-controls", "aria-owns"]) {
    for (const id of (el.getAttribute(a) ?? "").split(/\s+/)) {
      if (!id) continue;
      const n = root.getElementById?.(id) ?? el.ownerDocument.getElementById(id);
      if (n) return n as HTMLElement;
    }
  }
  return null;
}

/**
 * Where a library shows the chosen value. A button or div widget shows it
 * inside itself. A search input (React-Select) sits beside it: climb a few
 * levels, but never into an ancestor that holds another control, or another
 * widget's value would be read as this one's.
 */
function valueScope(el: HTMLElement): HTMLElement | null {
  const has = (n: HTMLElement) => n.querySelector(`${SINGLE}, ${MULTI}`) !== null;
  if (el.localName !== "input") return has(el) ? el : null;
  let n: HTMLElement | null = el;
  for (let d = 0; n && d < 4; d++, n = parentOf(n)) {
    if (n.querySelectorAll("input:not([type=hidden]), select, textarea, button, [role=combobox]").length > 1) return null;
    if (has(n)) return n;
  }
  return null;
}

/** A chip's text without its remove button or a trailing cross. */
function chipText(n: Element): string {
  return ownText(n).replace(/\s*[×✕✖⨯]$/, "").trim();
}

/** The options a listbox marks aria-selected, in document order. */
function selectedOptions(lb: Element): string[] {
  return Array.from(lb.querySelectorAll("[role=option][aria-selected=true]")).map(o => ownText(o)).filter(Boolean);
}

/** What a combobox/listbox/button dropdown currently shows as chosen; placeholder text is not a value. */
export function readWidgetValue(el: HTMLElement): string[] {
  const t = el.localName;
  if (el.getAttribute("role") === "listbox") return selectedOptions(el);
  if (t === "input" || t === "textarea") {
    const v = clean((el as HTMLInputElement).value);
    if (v) return [v];
  }
  const scope = valueScope(el);
  if (scope) {
    const multi = Array.from(scope.querySelectorAll(MULTI_LABEL)).map(chipText).filter(Boolean);
    if (multi.length) return multi;
    // Chips without a label element: take the outermost matches only, so a chip and its inner parts are one value.
    const chips = Array.from(scope.querySelectorAll(MULTI));
    const multiBoxes = chips.filter(c => !chips.some(o => o !== c && o.contains(c))).map(chipText).filter(Boolean);
    if (multiBoxes.length) return multiBoxes;
    const single = clean(scope.querySelector(SINGLE)?.textContent);
    if (single) return [single];
  }
  const owned = ownedListbox(el);
  const chosen = owned ? selectedOptions(owned) : [];
  if (chosen.length) return chosen;
  if (t === "input" || t === "textarea") return [];
  const own = ownText(el);
  return own && !PLACEHOLDER_TEXT.test(own) ? [own] : [];
}

/** Option labels already in the DOM for a combobox/listbox (an open popup, or an inline list). */
export function comboOptions(el: HTMLElement): string[] {
  const lb = el.getAttribute("role") === "listbox" ? el : ownedListbox(el);
  if (!lb) return [];
  return Array.from(lb.querySelectorAll("[role=option]")).map(o => ownText(o)).filter(Boolean).slice(0, 500);
}

function comboManual(el: HTMLElement): string {
  const hp = el.getAttribute("aria-haspopup");
  return hp === "dialog" || hp === "grid" ? "date or dialog picker" : "";
}

/** Does this widget take several values (it is filled by choosing each, not one)? */
function isMultiWidget(el: HTMLElement): boolean {
  const lb = el.getAttribute("role") === "listbox" ? el : ownedListbox(el);
  if (lb?.getAttribute("aria-multiselectable") === "true") return true;
  let n: HTMLElement | null = el;
  for (let d = 0; n && d < 4; d++, n = parentOf(n)) if (n.matches(MULTI_WRAPPER)) return true; // the widget's own wrapper, not a page-level container
  return valueScope(el)?.querySelector(MULTI) != null;
}

// --- the walk ----------------------------------------------------------------------

interface Walk { cands: HTMLElement[]; roots: ParentNode[]; scanned: number; capped: boolean; t0: number }

function frameDoc(el: Element): Document | null {
  try { return (el as HTMLIFrameElement).contentDocument; } catch { return null; }
}

/** Candidates in document order, descending into open shadow roots and same-origin frames. */
function walk(root: ParentNode, w: Walk, depth: number): void {
  w.roots.push(root);
  const own = (root as Element).shadowRoot; // scanning from a shadow host itself
  if (own && depth < MAX_DEPTH) walk(own, w, depth + 1);
  for (const el of Array.from(root.querySelectorAll<HTMLElement>("*"))) {
    if (w.capped) return;
    if (++w.scanned > MAX_SCAN_NODES || ((w.scanned & 255) === 0 && performance.now() - w.t0 > MAX_SCAN_MS)) { w.capped = true; return; }
    if (el.matches(CANDIDATE)) w.cands.push(el);
    if (depth >= MAX_DEPTH) continue;
    if (el.shadowRoot) walk(el.shadowRoot, w, depth + 1);
    else if (el.localName === "iframe" || el.localName === "frame") {
      const d = frameDoc(el);
      if (d) walk(d, w, depth + 1);
    }
  }
}

interface Scopes { ids: WeakMap<object, number>; next: number }

function scopeId(o: object, s: Scopes): number {
  let n = s.ids.get(o);
  if (n === undefined) { n = s.next++; s.ids.set(o, n); }
  return n;
}

/** Every fillable question under root, in document order, plus how complete that is. */
export function scanForm(root: ParentNode = document): FormScan {
  const w: Walk = { cands: [], roots: [], scanned: 0, capped: false, t0: performance.now() };
  walk(root, w, 0);

  const owned = new Set<string>();
  for (const el of w.cands) if (isCombo(el)) for (const a of ["aria-controls", "aria-owns"]) for (const id of (el.getAttribute(a) ?? "").split(/\s+/)) if (id) owned.add(id);

  const out: FieldRef[] = [];
  const claimed = new Set<Element>();
  const scopeIds: Scopes = { ids: new WeakMap<object, number>(), next: 1 };
  const nativeGroups = new Map<string, FieldRef>();
  const ariaGroups = new Map<Element, FieldRef>();
  const finalize: Array<() => void> = [];
  let unread = 0;
  const full = (): boolean => { if (out.length < MAX_FIELDS) return false; unread++; return true; };

  for (const el of w.cands) {
    try {
      if (claimed.has(el) || !el.isConnected) continue;
      const t = el.localName;
      const role = el.getAttribute("role") ?? "";
      if ((el as HTMLInputElement).disabled === true || el.getAttribute("aria-disabled") === "true") continue;
      const combo = isCombo(el) && t !== "select";

      if (!combo && (t === "input" || t === "select" || t === "textarea")) {
        const i = el as HTMLInputElement;
        if (t === "input" && SKIP_TYPES.has(i.type)) continue;
        if (!visible(el)) continue;
        if (searchBox(el)) continue;
        const kind = kindOf(el);
        if (requiredProxy(el, kind)) continue;
        if (kind === "radio" || (kind === "checkbox" && t === "input" && i.name)) {
          const key = `${kind}|${scopeId(i.form ?? el.getRootNode(), scopeIds)}|${i.name}`;
          const g = nativeGroups.get(key);
          if (g) { g.els.push(el); continue; }
          if (full()) continue;
          const ref: FieldRef = { info: base(el, kind, ""), els: [el], widget: "native", manual: "" };
          nativeGroups.set(key, ref);
          out.push(ref);
          finalize.push(() => {
            const members = ref.els as HTMLInputElement[];
            if (kind === "checkbox" && members.length === 1) {
              ref.info.label = labelOf(members[0]!);
              ref.info.value = [members[0]!.checked ? "yes" : "no"];
              return;
            }
            ref.info.label = groupLabel(members);
            ref.info.options = members.map(optionLabel);
            ref.info.value = members.filter(m => m.checked).map(optionLabel);
          });
          continue;
        }
        if (full()) continue;
        const info = base(el, kind, kind === "file" ? fileLabel(el) : labelOf(el));
        let manual = "";
        if (t === "select") {
          const s = el as HTMLSelectElement;
          info.options = Array.from(s.options).map(o => clean(o.text));
          if (s.multiple) info.multi = true;
          info.value = s.multiple
            ? Array.from(s.selectedOptions).map(o => clean(o.text))
            : s.value !== "" && s.selectedIndex >= 0 ? [clean(s.options[s.selectedIndex]!.text)] : [];
        } else if (kind === "checkbox") {
          info.value = [i.checked ? "yes" : "no"];
        } else if (kind !== "file" && kind !== "password") {
          info.value = i.value ? [i.value] : [];
          const hp = el.getAttribute("aria-haspopup");
          if (hp === "dialog" || hp === "grid") manual = "date or dialog picker";
          else if (i.type === "week") manual = "week picker"; // no saved answer is a week
          else if (i.readOnly && kind !== "date") manual = "read-only field (picker)";
        }
        out.push({ info, els: [el], widget: "native", manual });
        continue;
      }

      if (combo) {
        if (!visible(el)) continue;
        if (full()) continue;
        el.querySelectorAll("input, textarea, [role=textbox]").forEach(n => claimed.add(n));
        const info = base(el, "select", widgetLabel(el));
        info.options = comboOptions(el);
        info.value = readWidgetValue(el);
        if (isMultiWidget(el)) info.multi = true;
        out.push({ info, els: [el], widget: "combobox", manual: comboManual(el) });
        continue;
      }

      if (role === "radio") {
        if (!visible(el)) continue;
        const group = el.closest("[role=radiogroup]") ?? parentOf(el) ?? el;
        let ref = ariaGroups.get(group);
        if (!ref) {
          if (full()) continue;
          ref = { info: base(el, "radio", ""), els: [], widget: "aria-radio", manual: "" };
          ariaGroups.set(group, ref);
          out.push(ref);
          const r = ref;
          finalize.push(() => {
            r.info.label = groupLabel(r.els);
            r.info.name = group.id || r.info.name;
            r.info.options = r.els.map(optionLabel);
            r.info.value = r.els.filter(m => m.getAttribute("aria-checked") === "true").map(optionLabel);
          });
        }
        ref.els.push(el);
        continue;
      }

      if (role === "checkbox" || role === "switch") {
        if (!visible(el)) continue;
        if (full()) continue;
        const info = base(el, "checkbox", widgetLabel(el));
        info.value = [el.getAttribute("aria-checked") === "true" ? "yes" : "no"];
        out.push({ info, els: [el], widget: "aria-checkbox", manual: "" });
        continue;
      }

      if (role === "listbox") {
        // An inline list (not the popup of a combobox, which reads through its trigger).
        if ((el.id && owned.has(el.id)) || el.closest("[role=combobox]") || !visible(el)) continue;
        if (!el.querySelector("[role=option]")) continue;
        if (full()) continue;
        const info = base(el, "select", widgetLabel(el));
        info.options = comboOptions(el);
        info.value = readWidgetValue(el);
        if (isMultiWidget(el)) info.multi = true;
        out.push({ info, els: [el], widget: "combobox", manual: "" });
        continue;
      }

      const ce = el.getAttribute("contenteditable");
      if (ce === "" || ce === "true" || ce === "plaintext-only") {
        const up = parentOf(el)?.closest("[contenteditable]");
        const upCe = up?.getAttribute("contenteditable");
        if (up && (upCe === "" || upCe === "true" || upCe === "plaintext-only")) continue; // only the outermost editor
        if (!visible(el)) continue;
        if (full()) continue;
        const multiline = el.getAttribute("aria-multiline") === "true" || el.getBoundingClientRect().height > 60;
        const info = base(el, multiline ? "textarea" : "text", labelOf(el));
        const text = ((el as HTMLElement).innerText || el.textContent || "").trim();
        info.value = text ? [text] : [];
        out.push({ info, els: [el], widget: "contenteditable", manual: "" });
      }
    } catch {
      // A node removed or replaced mid-scan: skip it, keep the rest.
    }
  }
  for (const f of finalize) { try { f(); } catch { /* group member detached */ } }

  const refs = mergeSplits(out);
  assignSections(refs.map(r => r.info));
  return { refs, roots: w.roots, capped: w.capped || unread > 0, unread, nodesCapped: w.capped };
}

export function collectFields(root: ParentNode = document): FieldRef[] {
  return scanForm(root).refs;
}

// --- split dates and phone numbers --------------------------------------------------

function nativeSingle(r: FieldRef): HTMLElement | null {
  if (r.widget !== "native" || r.els.length !== 1) return null;
  const k = r.info.kind;
  return k === "text" || k === "number" || k === "select" || k === "tel" ? r.els[0]! : null;
}

function ownHints(r: FieldRef): string[] {
  const el = r.els[0]!;
  return [r.info.label, el.getAttribute("aria-label") ?? "", el.getAttribute("placeholder") ?? "", el.getAttribute("title") ?? ""];
}

function ids(r: FieldRef): string[] {
  return [r.info.automationId, r.info.name, r.els[0]!.id];
}

function datePart(r: FieldRef): DatePart | "" {
  if (!nativeSingle(r)) return "";
  for (const h of ownHints(r)) {
    const p = datePartRole({ label: h, ids: [] });
    if (p) return p;
  }
  return datePartRole({ label: "", ids: ids(r) });
}

/** Do two controls share a small enclosing container (so they are one widget, not neighbours)? */
function together(a: HTMLElement, b: HTMLElement): boolean {
  let n: HTMLElement | null = a;
  for (let d = 0; n && d < 4; d++, n = parentOf(n)) if (n.contains(b)) return true;
  return false;
}

function partValue(el: HTMLElement): string {
  if (el.localName === "select") { const s = el as HTMLSelectElement; return s.selectedIndex >= 0 && s.value !== "" ? clean(s.options[s.selectedIndex]!.text) : ""; }
  return clean((el as HTMLInputElement).value);
}

function mergeSplits(refs: FieldRef[]): FieldRef[] {
  const out: FieldRef[] = [];
  for (let i = 0; i < refs.length; i++) {
    const r = refs[i]!;
    const role = datePart(r);
    if (role) {
      const run: FieldRef[] = [r];
      const roles: string[] = [role];
      while (i + run.length < refs.length && run.length < 3) {
        const n = refs[i + run.length]!;
        const nr = datePart(n);
        if (!nr || roles.includes(nr) || !together(r.els[0]!, n.els[0]!)) break;
        run.push(n); roles.push(nr);
      }
      if (run.length >= 2) {
        out.push(dateRef(run, roles as DatePart[]));
        i += run.length - 1;
        continue;
      }
    }
    const next = refs[i + 1];
    if (next) {
      const pair = phonePair(r, next);
      if (pair) { out.push(pair); i++; continue; }
    }
    out.push(r);
  }
  return out;
}

function dateRef(run: FieldRef[], roles: DatePart[]): FieldRef {
  const els = run.map(r => r.els[0]!);
  const avoid = ["month", "day", "year", "mm", "dd", "yyyy", "yy", ...run.flatMap(ownHints).map(normalizeLabel)];
  const container = els[0]!.closest("[data-automation-id]");
  const label = groupLabel([els[0]!], avoid) || "Date";
  const info = base(els[0]!, "date", label);
  info.name = "";
  info.autocomplete = "";
  info.automationId = container?.getAttribute("data-automation-id") ?? "";
  info.required = run.some(r => r.info.required);
  const v: { y?: string; m?: string; d?: string } = {};
  els.forEach((el, k) => { const p = roles[k]!; v[p === "year" ? "y" : p === "month" ? "m" : "d"] = partValue(el); });
  const iso = composeDate(v);
  info.value = iso ? [iso] : [];
  return { info, els, widget: "split-date", manual: "", parts: roles };
}

/** A country-code control next to a phone-number control is one phone answer. */
function phonePair(a: FieldRef, b: FieldRef): FieldRef | null {
  for (const [code, num] of [[a, b], [b, a]] as const) {
    const ce = code.els[0]!, ne = num.els[0]!;
    if (code.els.length !== 1 || num.els.length !== 1) continue;
    const codeOk = (code.widget === "native" && ["text", "select", "tel", "number"].includes(code.info.kind)) || code.widget === "combobox";
    if (!codeOk || num.widget !== "native" || !["tel", "text", "number"].includes(num.info.kind)) continue;
    if (!isPhoneCode({ label: ownHints(code).join(" "), autocomplete: code.info.autocomplete, ids: ids(code) })) continue;
    if (isPhoneCode({ label: ownHints(num).join(" "), autocomplete: num.info.autocomplete, ids: ids(num) })) continue;
    if (!isPhoneNumber(num.info) || !together(ce, ne)) continue;
    const info = { ...num.info, options: [], value: [] as string[] };
    const codeVal = code.widget === "combobox" ? (readWidgetValue(ce)[0] ?? "") : partValue(ce);
    const joined = composePhone(codeVal, partValue(ne));
    info.value = joined ? [joined] : [];
    info.required = code.info.required || num.info.required;
    info.kind = "tel";
    return { info, els: [ce, ne], widget: "split-phone", manual: code.manual, parts: ["code", "number"] };
  }
  return null;
}
