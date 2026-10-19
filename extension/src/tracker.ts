// Capture that does not depend on catching the final click. The tracker keeps a
// per-page set of the answers the user has committed (on change / focus-out /
// choosing an option, never per keystroke) and flushes it when the page is about
// to go away: submit, an advance click, Enter, a route change, pagehide or the
// tab being hidden. A flush sends only what the bank does not already hold, so
// flushing the same page twice saves nothing twice. Only a page that has shown
// at least MIN_FORM_FIELDS questions is flushed on its own; on anything smaller
// (a search page) only an explicit Save saves. The DOM and the chrome
// messaging are injected so the DOM test page can drive it.
import type { Bank, FieldInfo } from "./types";
import { emptyBank } from "./types";
import { answerKey, mergeCapture } from "./bank";
import { classify } from "./classify";
import { scanForm } from "./extract";
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
const ADVANCE = /\b(submit|apply|send application|next|continue|save|review)\b/i;
const ADVANCE_ID = /(next|continue|submit|save)/i;

export interface TrackerDeps {
  doc: Document;
  host: string;
  getBank(): Promise<Bank | null>;
  /** Send fields for storage; resolves with the counts, or null if the background did not accept them. */
  send(fields: FieldInfo[]): Promise<CaptureReply | null>;
  /** The page changed shape (debounced, with a maximum wait so a mutation storm cannot starve it). */
  onDom?: () => void;
  onResult?: (r: FlushResult) => void;
  /** A button that sends the application was clicked (applied.isFinalSubmit); not Next, Continue, Save or Review. */
  onSubmit?: () => void;
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
  /** Fold the page's current fields into the pending set (synchronous). */
  snapshot(): void;
  flush(reason: string, explicit?: boolean): Promise<FlushResult | null>;
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

  function snapshot(): void {
    try {
      const scan = scanForm(d.doc);
      wireRoots(scan.roots);
      peak = Math.max(peak, scan.refs.length);
      for (const r of scan.refs) {
        const f = r.info;
        if (f.kind === "file" || f.kind === "hidden" || f.kind === "other") continue;
        const k = `${answerKey(f)}|${f.kind}`;
        if (f.kind === "password" || classify(f) === "secret") {
          // Kept so the count of skipped sensitive fields is honest; the value is never held.
          pending.delete(k);
          pending.set(k, { ...f, value: [] });
        } else if (f.value.every(v => v.trim() === "")) {
          pending.delete(k); // the user cleared it
        } else {
          pending.delete(k);
          pending.set(k, f);
        }
        if (pending.size > MAX_PENDING) pending.delete(pending.keys().next().value as string);
      }
    } catch {
      // A page mid-render must not break capture; the next trigger scans again.
    }
  }

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
    const bank = await d.getBank();
    lastBank = bank;
    if (bank?.settings.disabledHosts.includes(d.host)) return null;
    const selfId = bank?.settings.captureSelfId ?? false;
    const { fields, skipped } = unsent(selfId);
    const diff = pendingDiff(bank ?? emptyBank(), fields, d.host, explicit);
    const batch = diff.send.slice(0, MAX_SEND);
    const reply = batch.length > 0 ? await d.send(batch) : null;
    if (reply) markSent(batch, selfId);
    const res: FlushResult = {
      added: 0, updated: 0, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 0, ...(reply ?? {}),
      unchanged: diff.unchanged + skipped, reason, failed: batch.length > 0 && reply === null, notSent: diff.send.length - batch.length,
    };
    d.onResult?.(res);
    return res;
  }

  /**
   * On pagehide the page may be gone before an awaited bank read returns, so
   * decide from the last bank this tracker saw and send without waiting. If no
   * bank was ever read the whole pending set is sent (the background merges it).
   */
  function flushNow(): void {
    snapshot();
    if (pending.size === 0 || peak < MIN_FORM_FIELDS || lastBank?.settings.disabledHosts.includes(d.host)) return;
    const base = lastBank ?? emptyBank();
    const selfId = base.settings.captureSelfId;
    const batch = pendingDiff(base, unsent(selfId).fields, d.host, false).send.slice(0, MAX_SEND);
    if (batch.length === 0) return;
    // Remember what was just sent, so pagehide followed by visibilitychange does not send it twice.
    lastBank = mergeCapture(base, batch, d.host, new Date()).bank;
    markSent(batch, selfId);
    void d.send(batch).then(r => { if (r === null) unmarkSent(batch); }, () => unmarkSent(batch));
  }

  function flush(reason: string, explicit = false): Promise<FlushResult | null> {
    snapshot(); // before the page moves on: this is the DOM the user filled in
    const p = chain.then(() => run(reason, explicit));
    chain = p.catch(() => undefined); // flushes run one at a time, or two would both see an empty bank
    return p;
  }

  function commitSoon(ms = commitMs): void {
    clearTimeout(commitTimer);
    commitTimer = setTimeout(() => {
      snapshot();
      void d.getBank().then(b => { lastBank = b; }, () => undefined); // what the flush on pagehide will compare against
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
    if (!once(ev)) return;
    const t = ev.composedPath()[0] as Element | undefined;
    if (!t?.closest) return;
    const adv = t.closest("button, input[type=submit], [role=button], a[role=button]");
    if (adv && isAdvance(adv)) {
      void flush((adv.textContent || "advance").trim().toLowerCase().slice(0, 40));
      if (isFinalSubmit(controlLabel(adv))) d.onSubmit?.();
      return;
    }
    if (t.closest("[role=option], [role=radio], [role=checkbox], [role=switch], input[type=radio], input[type=checkbox], label")) commitSoon(150);
  }

  function onKey(ev: Event): void {
    const k = ev as KeyboardEvent;
    if (!once(ev) || k.key !== "Enter" || k.isComposing) return;
    const t = ev.composedPath()[0] as Element | undefined;
    if (t?.localName === "input") setTimeout(() => { if (!disposed) void flush("enter"); }, 150);
  }

  function wireRoot(root: Document | ShadowRoot): void {
    if (wired.has(root)) return;
    wired.add(root);
    const on = (type: string, fn: (e: Event) => void) => {
      root.addEventListener(type, fn, true);
      cleanups.push(() => root.removeEventListener(type, fn, true));
    };
    on("submit", ev => { if (once(ev)) void flush("submitted"); });
    on("click", onClick);
    on("keydown", onKey);
    on("change", ev => { if (once(ev)) commitSoon(); });   // `change` and `focusout` are separate events, and
    on("focusout", ev => { if (once(ev)) commitSoon(); }); // only focusout crosses a shadow boundary
    on("load", () => commitSoon(50));                      // an iframe finished loading: scan it, wire it
    if (observers.length < MAX_OBSERVED_ROOTS) {
      const mo = new MutationObserver(markDom);
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
