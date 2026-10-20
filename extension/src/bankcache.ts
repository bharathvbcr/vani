// A one-slot, short-lived memo of the answer bank, shared by the background (store.ts) and the
// content script. Chrome spawns the native host for every message (about 30-60 ms), so a scan
// that fires every 300-800 ms must not ask for the bank each time. It is bounded on purpose:
// one value, younger than `freshMs`, never served past that, and dropped by `invalidate()` on
// any write or conflict. The Mac app can edit the bank behind this memo, so the window is the
// only thing that makes a served value acceptable, never "we wrote it last".

/** Default window in which a fetched bank is served without asking again. */
export const BANK_FRESH_MS = 2000;

export interface BankMemoOptions<T> {
  /** One real request. A rejection is passed to every caller waiting on it and is never memoised. */
  fetch: () => Promise<T>;
  now?: () => number;
  freshMs?: number;
  /**
   * Called with the held value and a new one; returns the value to hold. Lets the caller keep the
   * old object when the new one is provably the same (equal revision), so identity checks hold.
   */
  reuse?: (held: T, next: T) => T;
}

export interface BankMemo<T> {
  /** The held value if it is younger than the window; otherwise one shared request. */
  get(): Promise<T>;
  /** Always asks (bypassing the window, not sharing an earlier request) and holds the answer. */
  fresh(): Promise<T>;
  /** Hold `value` as just fetched (a write whose result is known exactly). */
  set(value: T): void;
  /** Forget the held value and disown any request in flight, whose answer may predate a write. */
  invalidate(): void;
  /** The held value if still inside the window, else undefined. Never starts a request. */
  peek(): T | undefined;
}

export function createBankMemo<T>(opts: BankMemoOptions<T>): BankMemo<T> {
  const now = opts.now ?? Date.now;
  const freshMs = opts.freshMs ?? BANK_FRESH_MS;
  let held: { value: T; at: number } | undefined;
  let inflight: Promise<T> | undefined;
  // Bumped by set() and invalidate(): a request started before one must not store its answer.
  let generation = 0;

  function hold(value: T): T {
    const kept = held && opts.reuse ? opts.reuse(held.value, value) : value;
    held = { value: kept, at: now() };
    return kept;
  }

  function request(): Promise<T> {
    const started = generation;
    const p: Promise<T> = opts.fetch().then(v => (started === generation ? hold(v) : v));
    // Only the latest request is shared; a superseded one must not clear its successor's slot.
    inflight = p;
    const clear = (): void => { if (inflight === p) inflight = undefined; };
    p.then(clear, clear);
    return p;
  }

  function peek(): T | undefined {
    if (!held) return undefined;
    const age = now() - held.at;
    // A clock that went backwards is not evidence of freshness.
    if (age < 0 || age >= freshMs) { held = undefined; return undefined; }
    return held.value;
  }

  return {
    get() {
      const v = peek();
      if (v !== undefined) return Promise.resolve(v);
      return inflight ?? request();
    },
    fresh() { return request(); },
    set(value) { generation++; inflight = undefined; hold(value); },
    invalidate() { generation++; inflight = undefined; held = undefined; },
    peek,
  };
}
