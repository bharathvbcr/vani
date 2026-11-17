// Capture that does not depend on catching the final click. The tracker keeps a
// per-page set of the answers the user has given (a field is read when a trusted
// event lands on it, again once the page's handlers have run, and for a short
// window after it; a value a page script set on its own is never read) and flushes it when the page is about
// to go away: submit, an advance click, Enter, a route change, pagehide or the
// tab being hidden. A flush sends only what the bank does not already hold, so
// flushing the same page twice saves nothing twice. Only a page that has shown
// at least MIN_FORM_FIELDS questions is flushed on its own; on anything smaller
// (a search page) only an explicit Save saves. The DOM and the chrome
// messaging are injected so the DOM test page can drive it.
import type { Bank, FieldInfo } from "./types";
import { emptyBank } from "./types";
import { answerKey, hostDisabled, mergeCapture } from "./bank";
import { classify } from "./classify";
import { type FieldRef, type FormScan, readRefValue, scanForm } from "./extract";
import { fieldDigest, pendingDiff } from "./capture";
import type { CaptureReply } from "./messages";
import { isFinalSubmit } from "./applied";

/**
 * Fewer questions than this and a page is not treated as an application form:
 * no panel, and nothing is saved unless you press Save. A search page the popup
 * ran Vani on must not turn the query into an answer when you press Enter.
 */
export const MIN_FORM_FIELDS = 3;
const MAX_PENDING = 1000;
/** The background refuses a capture message with more fields than this. */
const MAX_SEND = 500;
const MAX_OBSERVED_ROOTS = 64;
/** How far up from a touched element the search for its field climbs. */
const TOUCH_DEPTH = 64;
/**
 * How long after its last trusted event a field's DOM value is still read as the user's. Long enough for a widget
 * or an input mask to finish setting the value after a real click or keystroke, short enough that a page's later
 * rewrite is not.
 */
export const TRUST_WINDOW_MS = 1000;
/** A target that can be a form control: only these cause a rescan when the last scan did not hold the target. */
const FIELDISH = "input, select, textarea, [contenteditable], [role=combobox], [role=listbox], [role=radiogroup], [role=radio], [role=checkbox], [role=switch], [role=textbox], [role=spinbutton], [role=slider], [role=option]";
const ADVANCE = /\b(submit|apply|send application|next|continue|save|review)\b/i;
const ADVANCE_ID = /(next|continue|submit|save)/i;

export interface TrackerDeps {
  doc: Document;
  host: string;
  getBank(): Promise<Bank | null>;
  /** Send fields for storage; resolves with the counts, `{ error }` when the background refused them with a reason, or null if it did not answer. */
  send(fields: FieldInfo[]): Promise<CaptureReply | { error: string } | null>;
  /** The page changed shape (debounced, with a maximum wait so a mutation storm cannot starve it). */
  onDom?: () => void;
  onResult?: (r: FlushResult) => void;
  /** A button that sends the application was clicked (applied.isFinalSubmit); not Next, Continue, Save or Review. */
  onSubmit?: () => void;
  /**
   * Whether an event came from the user. Defaults to `ev.isTrusted === true`; only the DOM test page, which
   * can dispatch nothing but script events, replaces it. The content script never passes it, and a page script
   * cannot reach a content script's arguments, so a page cannot turn it on.
   */
  isTrusted?: (ev: Event) => boolean;
  commitMs?: number;
  domMs?: number;
  domMaxWaitMs?: number;
  /** The clock and timers the DOM debounce runs on; tests inject a virtual one. Defaults to the real ones. */
  clock?: Clock;
}

export interface Clock {
  now(): number;
  setTimer(fn: () => void, ms: number): unknown;
  clearTimer(handle: unknown): void;
}

const REAL_CLOCK: Clock = {
  now: () => Date.now(),
  setTimer: (fn, ms) => setTimeout(fn, ms),
  clearTimer: h => clearTimeout(h as ReturnType<typeof setTimeout>),
};

/** `failed`: the background did not accept the save (nothing was stored). `notSent`: answers over the per-message cap, left for the next flush. */
export type FlushResult = CaptureReply & { unchanged: number; reason: string; failed: boolean; notSent: number };

export interface Tracker {
  /**
   * Fold the fields the user has touched (a trusted event on the field) into the pending set (synchronous).
   * `all` reads every visible field: only an explicit Save from the owner passes it.
   */
  snapshot(all?: boolean): void;
  flush(reason: string, explicit?: boolean): Promise<FlushResult | null>;
  /**
   * This page already saved `field` itself. A later flush must not send the same
   * value again: mergeCapture counts a use for every field it is handed, and the
   * bank read can still be the one from before this save.
   */
  noteSent(field: FieldInfo, captureSelfId: boolean): void;
  pendingCount(): number;
  observedRoots(): number;
  dispose(): void;
}

function controlLabel(t: Element): string {
  return (t.textContent || (t as HTMLInputElement).value || t.getAttribute("aria-label") || "").trim();
}

function isAdvance(t: Element): boolean {
  const text = controlLabel(t);
  if (text.length <= 40 && ADVANCE.test(text)) return true;
  if (t.getAttribute("type") === "submit") return true;
  return ADVANCE_ID.test(t.getAttribute("data-automation-id") ?? "");
}

/** The recorded draft wording stays on the page. A save sends the answer, not a second copy of it. */
function forCapture(f: FieldInfo): FieldInfo {
  if (f.draftText === undefined) return f;
  const copy = { ...f };
  delete copy.draftText;
  return copy;
}

export function createTracker(d: TrackerDeps): Tracker {
  const win = d.doc.defaultView ?? window;
  const commitMs = d.commitMs ?? 250;
  const domMs = d.domMs ?? 800;
  const domMaxWait = d.domMaxWaitMs ?? 3000;
  const pending = new Map<string, FieldInfo>();
  const wired = new WeakSet<object>();
  const observers: MutationObserver[] = [];
  const cleanups: Array<() => void> = [];
  const seen = new WeakSet<Event>();
  let lastBank: Bank | null = null;
  let lastHref = win.location.href;
  let chain: Promise<unknown> = Promise.resolve();
  const clock = d.clock ?? REAL_CLOCK;
  let commitTimer: ReturnType<typeof setTimeout> | undefined;
  let domTimer: unknown;
  /** When the current burst of mutations began; null while the page is quiet. */
  let domFirst: number | null = null;
  let disposed = false;
  /** The most questions one scan of this page has found (see MIN_FORM_FIELDS). */
  let peak = 0;
  // What each pending field last carried to the background on this page (see capture.fieldDigest).
  const sent = new Map<string, string>();

  const isTrusted = d.isTrusted ?? ((ev: Event) => ev.isTrusted === true);
  /**
   * Elements that received a trusted user event, or that contain one (a combobox wrapper around the input
   * the user typed in). A page script can set a value and dispatch events, but cannot make them trusted, so
   * a field it only set never enters this set and is never read by `snapshot`.
   */
  /**
   * When a trusted event last landed on each control of a field (keyed by the control, so a rescan finds it).
   * A page script can set a value and dispatch events, but cannot make them trusted, so a field it only set
   * has no mark and is never read. A mark also expires: the DOM is read for a field only within
   * TRUST_WINDOW_MS of its last trusted event, so a later script rewrite is not taken for the user's answer.
   */
  const marks = new WeakMap<object, number>();
  /** The last strict scan, by control: how an event's target is resolved to the field that contains it. */
  let index = new Map<Element, FieldRef>();
  /** The DOM changed since `index` was built (see the MutationObserver in wireRoot). */
  let indexStale = true;
  /** Targets that were not a field even after a fresh scan; they are not rescanned until the DOM changes. */
  let missed = new WeakSet<object>();

  function rescan(): FormScan {
    const scan = scanForm(d.doc, { strict: true });
    wireRoots(scan.roots);
    index = new Map();
    for (const r of scan.refs) for (const e of r.els) index.set(e, r);
    indexStale = false;
    return scan;
  }

  /** An element and its ancestors (across shadow boundaries), nearest first. */
  function ancestorsOf(start: Element): Element[] {
    const out: Element[] = [];
    for (let n: Element | null | undefined = start; n && out.length < TOUCH_DEPTH; n = n.parentElement ?? (n.getRootNode?.() as ShadowRoot | undefined)?.host) out.push(n);
    return out;
  }

  /** The deepest scanned field that contains `o`: one event marks one field, never the fields around it. */
  function fieldOf(o: Element): FieldRef | undefined {
    const ancestors = ancestorsOf(o);
    const find = (): FieldRef | undefined => { for (const n of ancestors) { const r = index.get(n); if (r) return r; } return undefined; };
    const hit = find();
    if (hit) return hit;
    if (!indexStale && (missed.has(o) || !o.closest?.(FIELDISH))) return undefined;
    rescan();
    const r = find();
    if (!r) missed.add(o);
    return r;
  }

  /** What a click on a listbox option stands for: the one combobox that owns the listbox, or nothing. */
  function popupOwner(first: Element): Element | undefined {
    const popup = first.closest?.("[role=listbox]");
    const id = popup?.id;
    if (!popup || !id) return undefined;
    const owners: Element[] = [];
    for (const o of (popup.getRootNode() as ParentNode).querySelectorAll?.("[aria-controls], [aria-owns]") ?? []) {
      const names = `${o.getAttribute("aria-controls") ?? ""} ${o.getAttribute("aria-owns") ?? ""}`.split(/\s+/);
      if (!names.includes(id)) continue;
      // A page can mark anything it likes with aria-controls: only a combobox, outside the listbox, counts.
      if (o.getAttribute("role") !== "combobox" && o.getAttribute("aria-haspopup") !== "listbox") continue;
      if (ancestorsOf(o).includes(popup)) continue;
      owners.push(o);
    }
    return owners.length === 1 ? owners[0] : undefined; // several claim it: the user's own touch on the field decides
  }

  /**
   * What a user event stands for: its target, the control a clicked label names, and the combobox that owns a
   * clicked listbox popup (a custom widget moves the value with script events after a real click on another node).
   */
  function originsOf(ev: Event): Element[] {
    const first = ev.composedPath()[0] as Element | undefined;
    if (first?.nodeType !== 1) return [];
    const out = [first];
    const control = (first.closest?.("label") as HTMLLabelElement | null | undefined)?.control;
    if (control) out.push(control);
    const owner = popupOwner(first);
    if (owner) out.push(owner);
    return out;
  }

  function markedAt(r: FieldRef): number | undefined {
    let at: number | undefined;
    for (const e of r.els) { const t = marks.get(e); if (t !== undefined && (at === undefined || t > at)) at = t; }
    return at;
  }
  const live = (r: FieldRef): boolean => { const at = markedAt(r); return at !== undefined && clock.now() - at <= TRUST_WINDOW_MS; };

  /** Fold one field into the pending set: a sensitive one is counted but never held, an emptied one is dropped. */
  function fold(f: FieldInfo): void {
    if (f.kind === "file" || f.kind === "hidden" || f.kind === "other") return;
    const k = `${answerKey(f)}|${f.kind}`;
    const kept = forCapture(f);
    if (f.kind === "password" || classify(f) === "secret") {
      pending.delete(k);
      pending.set(k, { ...kept, value: [] });
    } else if (f.value.every(v => v.trim() === "")) {
      pending.delete(k); // the user cleared it
    } else {
      pending.delete(k);
      pending.set(k, kept);
    }
    if (pending.size > MAX_PENDING) pending.delete(pending.keys().next().value as string);
  }

  /** The field's value now, as the user's answer. */
  function absorb(r: FieldRef): void {
    try { fold({ ...r.info, value: readRefValue(r) }); } catch { /* a control mid-render: the next event reads it again */ }
  }

  /**
   * A trusted event, seen before the page's own handlers: the fields it stands for are marked and read at once,
   * so what the user typed or chose is held before any script can rewrite it.
   */
  function touch(ev: Event): void {
    if (!isTrusted(ev)) return;
    const now = clock.now();
    for (const o of originsOf(ev)) {
      const r = fieldOf(o);
      if (!r) continue;
      for (const e of r.els) marks.set(e, now);
      absorb(r);
    }
  }

  /** The same event after the page's own handlers ran: an input mask has finished rewriting the value by now. */
  function settle(ev: Event): void {
    if (!isTrusted(ev)) return;
    for (const o of originsOf(ev)) { const r = fieldOf(o); if (r && live(r)) absorb(r); }
  }

  /**
   * Scan the page (wiring every root it finds) and fold in what may be read: a sensitive field always (so its
   * count is honest), a field the user touched within TRUST_WINDOW_MS (`window`), or every field (`all`). With
   * `none` the scan only counts questions: a flush the page can trigger itself reads no DOM.
   */
  function fromDom(read: "none" | "window" | "all"): void {
    try {
      const scan = rescan();
      // Whether this is a form must not depend on which questions are visible enough to capture.
      peak = Math.max(peak, scan.refs.length);
      if (peak < MIN_FORM_FIELDS) peak = Math.max(peak, scanForm(d.doc).refs.length);
      for (const r of scan.refs) {
        const f = r.info;
        if (f.kind === "password" || classify(f) === "secret") fold(f);
        else if (read === "all" || (read === "window" && live(r))) fold(f);
      }
    } catch {
      // A page mid-render must not break capture; the next trigger scans again.
    }
  }

  function snapshot(all = false): void { fromDom(all ? "all" : "window"); }

  const keyOf = (f: FieldInfo): string => `${answerKey(f)}|${f.kind}`;

  /**
   * The pending fields minus those whose current value this page already sent.
   * The bank can lag behind a save (a stale read, a slow background), and
   * mergeCapture counts a use for every field it is handed, so the same value
   * for the same field must not go out twice whatever the bank says.
   */
  function unsent(selfId: boolean): { fields: FieldInfo[]; skipped: number } {
    const fields: FieldInfo[] = [];
    let skipped = 0;
    for (const [k, f] of pending) {
      const g = fieldDigest(f, d.host, selfId);
      if (g !== "" && sent.get(k) === g) skipped++; else fields.push(f);
    }
    return { fields, skipped };
  }

  function markSent(batch: FieldInfo[], selfId: boolean): void {
    for (const f of batch) {
      const g = fieldDigest(f, d.host, selfId);
      if (g === "") continue;
      const k = keyOf(f);
      sent.delete(k);
      sent.set(k, g);
      if (sent.size > MAX_PENDING) sent.delete(sent.keys().next().value as string);
    }
  }

  /** The background did not take the batch: it has not been sent, so it may go again. */
  function unmarkSent(batch: FieldInfo[]): void {
    for (const f of batch) sent.delete(keyOf(f));
  }

  async function run(reason: string, explicit: boolean): Promise<FlushResult | null> {
    if (pending.size === 0) return null;
    if (!explicit && peak < MIN_FORM_FIELDS) return null; // not a form: only Save keeps it
    const read = await d.getBank().catch(() => null);
    if (read) lastBank = read; // a failed read never erases a bank this tracker already knows
    const bank = read ?? lastBank;
    // A bank this tracker knows says the host is disabled: nothing leaves the page. With no bank at all the
    // background decides (it checks disabledHosts against a fresh bank and refuses when it cannot read one).
    if (bank && hostDisabled(bank, d.host)) return null;
    const selfId = bank?.settings.captureSelfId ?? false;
    const { fields, skipped } = unsent(selfId);
    const diff = pendingDiff(bank ?? emptyBank(), fields, d.host, explicit);
    const batch = diff.send.slice(0, MAX_SEND);
    const raw = batch.length > 0 ? await d.send(batch) : null;
    const refused = raw !== null && "error" in raw ? raw.error : undefined;
    const reply = raw !== null && !("error" in raw) ? raw : null;
    if (reply) markSent(batch, selfId);
    const res: FlushResult = {
      added: 0, updated: 0, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 0, ...(reply ?? {}),
      ...(refused !== undefined ? { refused } : {}),
      unchanged: diff.unchanged + skipped, reason, failed: batch.length > 0 && reply === null, notSent: diff.send.length - batch.length,
    };
    d.onResult?.(res);
    return res;
  }

  /**
   * On pagehide the page may be gone before an awaited bank read returns, so
   * decide from the last bank this tracker saw and send without waiting. If no
   * bank was ever read the whole pending set is sent: the background merges it
   * and is the authority on disabledHosts (it checks a fresh bank, failing
   * closed). A known bank that disables this host keeps the page from sending.
   */
  function flushNow(): void {
    fromDom("none"); // a page can fire pagehide itself: only what the user was seen to commit leaves
    if (pending.size === 0 || peak < MIN_FORM_FIELDS || (lastBank && hostDisabled(lastBank, d.host))) return;
    const base = lastBank ?? emptyBank();
    const selfId = base.settings.captureSelfId;
    const batch = pendingDiff(base, unsent(selfId).fields, d.host, false).send.slice(0, MAX_SEND);
    if (batch.length === 0) return;
    // Remember what was just sent, so pagehide followed by visibilitychange does not send it twice.
    if (lastBank) lastBank = mergeCapture(base, batch, d.host, new Date()).bank;
    markSent(batch, selfId);
    void d.send(batch).then(r => { if (r === null || "error" in r) unmarkSent(batch); }, () => unmarkSent(batch));
  }

  /**
   * `read` is what the flush may read from the page first. A flush the user caused (their click on Next, Enter, a
   * submit) reads the fields they touched in the last TRUST_WINDOW_MS; one a page can cause (a route change, a
   * script's pagehide) reads nothing and sends what was held when the user acted; Save reads everything.
   */
  function flushWith(reason: string, explicit: boolean, read: "none" | "window" | "all"): Promise<FlushResult | null> {
    fromDom(read);
    const p = chain.then(() => run(reason, explicit));
    chain = p.catch(() => undefined); // flushes run one at a time, or two would both see an empty bank
    return p;
  }
  const flush = (reason: string, explicit = false): Promise<FlushResult | null> => flushWith(reason, explicit, explicit ? "all" : "none");
  const flushByUser = (reason: string): Promise<FlushResult | null> => flushWith(reason, false, "window");

  function commitSoon(ms = commitMs): void {
    clearTimeout(commitTimer);
    commitTimer = setTimeout(() => {
      snapshot();
      // What the flush on pagehide will compare against. A failed or empty read keeps the bank already known.
      void d.getBank().then(b => { if (b) lastBank = b; }, () => undefined);
    }, ms);
  }

  function checkRoute(): void {
    const href = win.location.href;
    if (href !== lastHref) { lastHref = href; void flush("route change"); }
  }

  function fireDom(): void {
    clock.clearTimer(domTimer);
    domFirst = null;
    checkRoute();
    d.onDom?.();
  }

  // Debounced, but a burst that has run for domMaxWait fires now: a timer set
  // for "as soon as possible" would be cleared by the next mutation, which is
  // how a steady storm would starve the callback.
  function markDom(): void {
    const now = clock.now();
    if (domFirst === null) domFirst = now;
    clock.clearTimer(domTimer);
    const wait = Math.min(domMs, domFirst + domMaxWait - now);
    if (wait <= 0) { fireDom(); return; }
    domTimer = clock.setTimer(fireDom, wait);
  }

  const once = (ev: Event): boolean => { if (seen.has(ev)) return false; seen.add(ev); return true; };

  function onClick(ev: Event): void {
    if (!isTrusted(ev) || !once(ev)) return; // a page script's click is neither a commit nor an Applied prompt
    const t = ev.composedPath()[0] as Element | undefined;
    if (!t?.closest) return;
    const adv = t.closest("button, input[type=submit], [role=button], a[role=button]");
    if (adv && isAdvance(adv)) {
      void flushByUser((adv.textContent || "advance").trim().toLowerCase().slice(0, 40));
      if (isFinalSubmit(controlLabel(adv)) && adv.getAttribute("data-vani-suppress-applied") !== "1") d.onSubmit?.();
      return;
    }
    if (t.closest("[role=option], [role=radio], [role=checkbox], [role=switch], input[type=radio], input[type=checkbox], label")) commitSoon(150);
  }

  function onKey(ev: Event): void {
    const k = ev as KeyboardEvent;
    if (!isTrusted(ev) || !once(ev) || k.key !== "Enter" || k.isComposing) return;
    const t = ev.composedPath()[0] as Element | undefined;
    if (t?.localName === "input") setTimeout(() => { if (!disposed) void flushByUser("enter"); }, 150);
  }

  function wireRoot(root: Document | ShadowRoot): void {
    if (wired.has(root)) return;
    wired.add(root);
    const on = (type: string, fn: (e: Event) => void, capture = true) => {
      root.addEventListener(type, fn, capture);
      cleanups.push(() => root.removeEventListener(type, fn, capture));
    };
    // Provenance first: a user event marks and reads the one field it landed in; the bubble pass reads it again
    // once the page's own handlers (an input mask) have run.
    for (const type of ["input", "pointerdown", "mousedown"]) on(type, touch);
    for (const type of ["input", "change", "click", "focusout"]) on(type, settle, false);
    on("submit", ev => { if (isTrusted(ev) && once(ev)) void flushByUser("submitted"); });
    on("click", ev => { touch(ev); onClick(ev); });
    on("keydown", ev => { touch(ev); onKey(ev); });
    on("change", ev => { touch(ev); if (isTrusted(ev) && once(ev)) commitSoon(); });   // `change` and `focusout` are separate events, and
    on("focusout", ev => { touch(ev); if (isTrusted(ev) && once(ev)) commitSoon(); }); // only focusout crosses a shadow boundary
    on("load", () => commitSoon(50));                      // an iframe finished loading: scan it, wire it
    if (observers.length < MAX_OBSERVED_ROOTS) {
      const mo = new MutationObserver(() => { indexStale = true; missed = new WeakSet(); markDom(); });
      mo.observe(root, { childList: true, subtree: true });
      observers.push(mo);
    }
  }

  function wireRoots(roots: ParentNode[]): void {
    for (const r of roots) if (r.nodeType === 9 || r.nodeType === 11) wireRoot(r as Document | ShadowRoot);
  }

  wireRoot(d.doc);
  const onHide = () => flushNow();
  const onVis = () => { if (d.doc.visibilityState === "hidden") flushNow(); };
  const onRoute = () => { lastHref = win.location.href; void flush("route change"); };
  win.addEventListener("pagehide", onHide);
  d.doc.addEventListener("visibilitychange", onVis);
  win.addEventListener("popstate", onRoute);
  win.addEventListener("hashchange", onRoute);
  // The Navigation API sees history.pushState, which a content script's isolated
  // world cannot patch; where it is missing, markDom's href check covers it.
  const nav = (win as unknown as { navigation?: EventTarget }).navigation;
  const onNavigate = () => { void flush("route change"); };
  nav?.addEventListener("navigate", onNavigate);
  cleanups.push(() => {
    win.removeEventListener("pagehide", onHide);
    d.doc.removeEventListener("visibilitychange", onVis);
    win.removeEventListener("popstate", onRoute);
    win.removeEventListener("hashchange", onRoute);
    nav?.removeEventListener("navigate", onNavigate);
  });

  return {
    snapshot,
    flush,
    noteSent(field, captureSelfId) { markSent([field], captureSelfId); },
    pendingCount: () => pending.size,
    observedRoots: () => observers.length,
    dispose() {
      disposed = true;
      clearTimeout(commitTimer);
      clock.clearTimer(domTimer);
      for (const o of observers) o.disconnect();
      for (const c of cleanups) c();
    },
  };
}
