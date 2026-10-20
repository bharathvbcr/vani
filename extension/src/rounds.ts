// Fill in rounds. Some fields exist only once another is filled: a state list after the country, an
// "Other: please specify" box after a radio. One pass over the page cannot see them, so after a round
// that filled something the page is given a moment to settle, scanned again, and only the fields not
// yet tried are filled. Pure logic over injected scan / fill / clock, so the limits are testable
// without a browser; content.ts wires it to the real page.

export const MAX_ROUNDS = 3;
/** The whole fill, rounds and waits together; a round already running is not cut short, a new one is not started past this. */
export const ROUNDS_BUDGET_MS = 6000;
/** The page counts as settled after this long with no element added or removed... */
export const QUIET_MS = 250;
/** ...but a page that never settles is waited on no longer than this per round. */
export const QUIET_CAP_MS = 1500;

/** The slice of a scanned field the rounds look at. */
export interface RoundRef {
  els: Element[];
  info: { value: string[] };
}

/** The slice of a fill outcome the rounds look at. */
export interface RoundOutcome {
  filled: boolean;
  reason?: string;
}

/**
 * Remember which fields a round tried, so a later round never touches them again: a value the user
 * typed or cleared in between is theirs. A field that failed with "no_option" is the one exception, since
 * its list may simply not have loaded yet; it stays eligible (while it is still empty).
 */
export function recordAttempts<R extends RoundRef>(attempted: WeakSet<Element>, refs: R[], outcomes: RoundOutcome[]): void {
  for (let i = 0; i < refs.length; i++) {
    if (outcomes[i]?.reason === "no_option") continue;
    for (const el of refs[i]!.els) attempted.add(el);
  }
}

/** From a fresh scan: the fields never attempted that are attached to the page and currently empty. */
export function untriedRefs<R extends RoundRef>(attempted: WeakSet<Element>, refs: R[]): R[] {
  return refs.filter(r =>
    r.els.length > 0 &&
    r.els.every(e => e.isConnected) &&
    !r.els.some(e => attempted.has(e)) &&
    r.info.value.every(v => v.trim() === ""));
}

export interface RoundsDeps<R extends RoundRef, O extends RoundOutcome> {
  /** The fields on the page now. */
  scan(): R[];
  /** Fill these (round 1 gets every field; later rounds only the untried ones). Outcomes in the order of `refs`. */
  fill(refs: R[], round: number): Promise<O[]>;
  /** Resolve once the page has been quiet, or after `maxMs`. */
  settle(maxMs: number): Promise<void>;
  now(): number;
}

export interface RoundsLimits {
  maxRounds?: number;
  budgetMs?: number;
  quietCapMs?: number;
}

/** Run up to `maxRounds` rounds within `budgetMs`; the outcomes of every round, in order. */
export async function runRounds<R extends RoundRef, O extends RoundOutcome>(deps: RoundsDeps<R, O>, limits: RoundsLimits = {}): Promise<O[]> {
  const maxRounds = limits.maxRounds ?? MAX_ROUNDS;
  const budgetMs = limits.budgetMs ?? ROUNDS_BUDGET_MS;
  const quietCapMs = limits.quietCapMs ?? QUIET_CAP_MS;
  const started = deps.now();
  const attempted = new WeakSet<Element>();
  const all: O[] = [];
  let todo = deps.scan();
  for (let round = 1; ; round++) {
    const outcomes = await deps.fill(todo, round);
    recordAttempts(attempted, todo, outcomes);
    all.push(...outcomes);
    // A round that filled nothing changed nothing on the page, so nothing new can have appeared.
    if (round >= maxRounds || !outcomes.some(o => o.filled)) break;
    const left = budgetMs - (deps.now() - started);
    if (left <= 0) break;
    await deps.settle(Math.min(quietCapMs, left));
    if (deps.now() - started >= budgetMs) break;
    todo = untriedRefs(attempted, deps.scan());
    if (todo.length === 0) break;
  }
  return all;
}

/**
 * At most this many roots are observed at once. A page with more shadow roots than this is watched
 * through the first ones only (the scan lists the document first); the rest rely on the caller's own
 * timer (waitFor's 100 ms tick) or on the quiet period running out.
 */
export const MAX_OBSERVED_ROOTS = 64;

/**
 * One MutationObserver per root (the document and every open shadow root the scan walked): a node
 * added inside a shadow root is not reported to an observer on the document. Returns the function
 * that disconnects them all. If starting one throws, those already started are disconnected before
 * the error propagates, so nothing is left watching.
 */
export function observeRoots(
  roots: readonly Node[],
  onChange: () => void,
  init: MutationObserverInit = { childList: true, subtree: true },
): () => void {
  const started: MutationObserver[] = [];
  const stop = (): void => { for (const o of started.splice(0)) o.disconnect(); };
  try {
    for (const root of roots.slice(0, MAX_OBSERVED_ROOTS)) {
      const doc = root.nodeType === 9 ? (root as Document) : root.ownerDocument;
      const Observer = (doc?.defaultView as (Window & typeof globalThis) | null | undefined)?.MutationObserver
        ?? (typeof MutationObserver === "undefined" ? undefined : MutationObserver);
      if (!Observer) continue;
      const o = new Observer(onChange);
      started.push(o);
      o.observe(root, init);
    }
  } catch (e) {
    stop();
    throw e;
  }
  return stop;
}

/**
 * Resolve when `observe`'s change callback has not fired for `quietMs`, or after `capMs` regardless.
 * `observe(onChange)` starts watching the page and returns the function that stops it (always called,
 * after the quiet period, after the cap, and whatever else ends the wait). Build it with `observeRoots`
 * over the scan's roots so a field added inside a shadow root counts as activity.
 */
export function waitForQuiet(
  observe: (onChange: () => void) => () => void,
  quietMs = QUIET_MS,
  capMs = QUIET_CAP_MS,
): Promise<void> {
  return new Promise(resolve => {
    let quiet: ReturnType<typeof setTimeout> | undefined;
    let stop: (() => void) | undefined;
    const done = (): void => {
      clearTimeout(quiet); clearTimeout(cap);
      try { stop?.(); } catch { /* a stop that throws must not leave the fill waiting forever */ }
      resolve();
    };
    const cap = setTimeout(done, Math.max(0, capMs));
    const arm = (): void => { clearTimeout(quiet); quiet = setTimeout(done, Math.max(0, quietMs)); };
    try { stop = observe(arm); } catch { /* cannot watch: fall back to waiting out the quiet period once */ }
    arm();
  });
}

/** The parts of a scanned field that decide how it is matched. */
interface Matchable extends RoundRef {
  info: RoundRef["info"] & { kind: string; label: string; name: string; options: string[] };
  widget: string;
  manual: string;
}

const sameField = (a: Matchable, b: Matchable): boolean =>
  a.widget === b.widget && a.manual === b.manual &&
  a.info.kind === b.info.kind && a.info.label === b.info.label && a.info.name === b.info.name &&
  a.info.options.length === b.info.options.length && a.info.options.every((o, i) => o === b.info.options[i]) &&
  a.els.length === b.els.length && a.els.every((e, i) => e === b.els[i]);

/**
 * A scan makes new field objects, so matches computed for the previous scan are keyed by objects that no
 * longer exist. Carry one over to a fresh field only when it is provably the same: the very same elements,
 * all still attached, with the same label, kind, name and options. Anything else is left out, and the
 * caller matches it again. The caller must also know the bank is the same one the matches came from.
 */
export function carryMatches<R extends Matchable, M>(fresh: R[], previous: { refs: R[]; matches: Map<R, M> }): Map<R, M> {
  const byFirst = new Map<Element, R>();
  for (const r of previous.refs) if (r.els[0]) byFirst.set(r.els[0], r);
  const out = new Map<R, M>();
  for (const r of fresh) {
    const old = r.els[0] ? byFirst.get(r.els[0]) : undefined;
    if (!old || !previous.matches.has(old)) continue;
    if (!r.els.every(e => e.isConnected) || !sameField(old, r)) continue;
    out.set(r, previous.matches.get(old) as M);
  }
  return out;
}
