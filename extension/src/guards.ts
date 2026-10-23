// Small shared guards: the plain-object test every boundary check uses, the
// promise queue that serialises read-modify-write sequences, and the text
// clean-up the request builders and scrapers apply.

/** A plain object: not null and not an array. */
export function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === "object" && x !== null && !Array.isArray(x);
}

/**
 * A queue that runs one operation at a time, in call order. An operation runs
 * whether or not the one before it failed, and a failure reaches only its own
 * caller.
 */
export function createLock(): <T>(op: () => Promise<T>) => Promise<T> {
  let chain: Promise<unknown> = Promise.resolve();
  return <T>(op: () => Promise<T>): Promise<T> => {
    const next = chain.then(op, op);
    chain = next.catch(() => undefined);
    return next;
  };
}

/** `s` with every run of whitespace folded to one space and the ends trimmed; "" for null or undefined. */
export function clean(s: string | null | undefined): string {
  return (s ?? "").replace(/\s+/g, " ").trim();
}

/** `s` cut to at most `n` UTF-16 code units. */
export function clip(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) : s;
}
