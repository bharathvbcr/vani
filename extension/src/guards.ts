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

/** Input types the browser applies `maxlength` to (the HTML "maxlength applies" list). */
const MAXLENGTH_TYPES: ReadonlySet<string> = new Set(["text", "search", "url", "tel", "email", "password"]);

/**
 * The character limit of a text control, in UTF-16 code units, or null when it has none: a plain
 * `<input>` of a type `maxlength` applies to, or a `<textarea>`, whose `maxLength` is not negative
 * (read from the `maxlength` attribute when the element has no such property). Read on every call,
 * since a page may change it after render. Anything else (a contenteditable, a select, a number
 * input) has no limit the browser knows.
 */
export function maxLengthOf(el: Element): number | null {
  const tag = String((el as { localName?: string }).localName ?? el.tagName ?? "").toLowerCase();
  if (tag === "input") {
    const type = String((el as HTMLInputElement).type ?? el.getAttribute("type") ?? "text").toLowerCase();
    if (!MAXLENGTH_TYPES.has(type)) return null;
  } else if (tag !== "textarea") return null;
  const prop = (el as HTMLInputElement).maxLength;
  let n = typeof prop === "number" && Number.isFinite(prop) ? prop : NaN;
  if (Number.isNaN(n)) {
    const m = /^\s*\+?(\d+)/.exec(el.getAttribute("maxlength") ?? "");
    n = m ? Number(m[1]) : -1;
  }
  return n >= 0 ? n : null;
}

/**
 * What the control would hold after `text` is written to its `value`: a textarea keeps a line break
 * as one character, a single-line input drops line breaks. This is the length the browser measures
 * against `maxlength`.
 */
export function storedLength(el: Element, text: string): number {
  const tag = String((el as { localName?: string }).localName ?? el.tagName ?? "").toLowerCase();
  return (tag === "textarea" ? text.replace(/\r\n?/g, "\n") : text.replace(/[\r\n]/g, "")).length;
}

/**
 * Whether writing `text` into `el` would pass its character limit. The browser enforces `maxlength`
 * only for typing, so a script that sets `.value` can write past it, and a site may then drop or cut
 * the answer on submit. Returns the limit and the length, or null when it fits or the control has no limit.
 */
export function overLimit(el: Element, text: string): { limit: number; length: number } | null {
  const limit = maxLengthOf(el);
  if (limit === null) return null;
  const length = storedLength(el, text);
  return length > limit ? { limit, length } : null;
}
