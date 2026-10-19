import { type Answer, type Bank, type FieldInfo, type FieldKind, CHOICE_KINDS, emptyBank } from "./types";
import { autocompleteKey, normalizeLabel, signature } from "./normalize";
import { classify, untouchable } from "./classify";

// Bounds: the bank lives in chrome.storage.local (10 MB quota) or the Mac app,
// and is exported as JSON, so every list and string is capped.
export const MAX_ANSWERS = 2000;
export const MAX_VALUE_CHARS = 10_000;
export const MAX_LABELS = 20;
export const MAX_SIGNATURES = 50;
export const MAX_OPTIONS = 200;
/** Free text longer than this is usually job-specific ("why us?"): kept, filled as a draft. */
export const REVIEW_CHARS = 280;
/**
 * Serialized bank cap. Chrome limits a native-messaging host's reply to the
 * extension at 1 MB, so the whole bank must fit in one reply with headroom.
 */
export const MAX_BANK_BYTES = 900_000;

/** UTF-8 size of the bank as it travels (compact JSON). */
export function bankBytes(bank: Bank): number {
  return new TextEncoder().encode(JSON.stringify(bank)).length;
}

/**
 * Combine two banks without losing either side's answers. Per key the later
 * `updatedAt` wins (ISO strings compare lexically; a tie keeps `base`). Keys
 * on one side only are kept, base first, up to MAX_ANSWERS. Settings: base's
 * captureSelfId, and the union of disabled hosts capped at 500.
 */
export function mergeBanks(base: Bank, incoming: Bank): Bank {
  const answers: Record<string, Answer> = {};
  for (const [k, a] of Object.entries(base.answers)) {
    if (Object.keys(answers).length >= MAX_ANSWERS) break;
    answers[k] = a;
  }
  for (const [k, a] of Object.entries(incoming.answers)) {
    const have = answers[k];
    if (have) {
      if (a.updatedAt > have.updatedAt) answers[k] = a;
    } else if (Object.keys(answers).length < MAX_ANSWERS) {
      answers[k] = a;
    }
  }
  return {
    version: 1,
    answers,
    settings: {
      captureSelfId: base.settings.captureSelfId,
      disabledHosts: [...new Set([...base.settings.disabledHosts, ...incoming.settings.disabledHosts])].slice(0, 500),
    },
  };
}

export interface CaptureResult {
  bank: Bank;
  added: number;
  updated: number;
  skippedSecret: number;
  skippedSelfId: number;
  skippedEmpty: number;
}

/** The key an answer is stored under: the autocomplete meaning if any, else the label. */
export function answerKey(f: Pick<FieldInfo, "label" | "autocomplete">): string {
  return autocompleteKey(f.autocomplete) || normalizeLabel(f.label);
}

function clip(xs: string[], n: number): string[] {
  return xs.slice(0, n);
}

function addUnique(list: string[], item: string, cap: number): string[] {
  if (!item || list.includes(item)) return list;
  const out = [...list, item];
  return out.length > cap ? out.slice(out.length - cap) : out; // keep the newest
}

/**
 * Merge the fields of a submitted (or saved) form into the bank. A new value
 * replaces the old one: the latest answer is the one the user means now.
 * Secret fields are never stored; self-id fields only when enabled.
 */
export function mergeCapture(bank: Bank, fields: FieldInfo[], host: string, now: Date): CaptureResult {
  const out: Bank = { ...bank, answers: { ...bank.answers } };
  const res: CaptureResult = { bank: out, added: 0, updated: 0, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 0 };
  for (const f of fields) {
    if (f.kind === "password") { res.skippedSecret++; continue; }
    if (untouchable(f.kind)) continue;
    const sens = classify(f);
    if (sens === "secret") { res.skippedSecret++; continue; }
    if (sens === "self_id" && !bank.settings.captureSelfId) { res.skippedSelfId++; continue; }
    const value = clip(f.value.map(v => v.trim()).filter(Boolean), MAX_OPTIONS).map(v => v.slice(0, MAX_VALUE_CHARS));
    // An unchecked single checkbox reads "no": that is an answer, but an
    // empty text box is not.
    if (value.length === 0) { res.skippedEmpty++; continue; }
    const key = answerKey(f);
    if (key.length < 2) { res.skippedEmpty++; continue; }
    const prev = out.answers[key];
    if (!prev && Object.keys(out.answers).length >= MAX_ANSWERS) { res.skippedEmpty++; continue; }
    const label = normalizeLabel(f.label);
    const sig = signature(host, f);
    // Long free text is job-specific, and a model draft is never a confident
    // answer however short: both are filled next time only as drafts to review.
    const long = f.drafted === true || (!CHOICE_KINDS.has(f.kind) && value.join(" ").length > REVIEW_CHARS);
    const next: Answer = {
      key,
      labels: addUnique(prev?.labels ?? [], label, MAX_LABELS),
      kind: f.kind,
      value,
      sensitivity: sens,
      signatures: addUnique(prev?.signatures ?? [], sig, MAX_SIGNATURES),
      uses: (prev?.uses ?? 0) + 1,
      fills: prev?.fills ?? 0,
      updatedAt: now.toISOString(),
      review: long,
    };
    out.answers[key] = next;
    if (prev) res.updated++; else res.added++;
  }
  return res;
}

const KINDS: ReadonlySet<string> = new Set<FieldKind>([
  "text", "email", "tel", "url", "number", "date", "textarea", "select", "radio", "checkbox", "file", "password", "hidden", "other",
]);

function isStrArray(x: unknown, cap: number): x is string[] {
  return Array.isArray(x) && x.length <= cap && x.every(s => typeof s === "string" && s.length <= MAX_VALUE_CHARS);
}

/**
 * Parse an exported bank strictly. Anything malformed is refused whole, never
 * half-imported; a secret-looking answer in the file is dropped (and counted),
 * because a hand-edited export must not smuggle in what capture refuses.
 */
export function parseBank(json: string): { bank: Bank; droppedSecret: number } {
  if (json.length > 8_000_000) throw new Error("bank file is larger than 8 MB");
  const raw: unknown = JSON.parse(json);
  if (!raw || typeof raw !== "object") throw new Error("bank is not an object");
  const r = raw as Record<string, unknown>;
  if (r.version !== 1) throw new Error(`bank version ${String(r.version)}, want 1`);
  const answers = r.answers;
  if (!answers || typeof answers !== "object" || Array.isArray(answers)) throw new Error("bank.answers is not an object");
  const entries = Object.entries(answers as Record<string, unknown>);
  if (entries.length > MAX_ANSWERS) throw new Error(`bank has ${entries.length} answers, max ${MAX_ANSWERS}`);
  const bank = emptyBank();
  const s = r.settings as Record<string, unknown> | undefined;
  if (s && typeof s === "object") {
    bank.settings.captureSelfId = s.captureSelfId === true;
    if (isStrArray(s.disabledHosts, 500)) bank.settings.disabledHosts = s.disabledHosts;
  }
  let droppedSecret = 0;
  for (const [key, v] of entries) {
    const a = v as Record<string, unknown>;
    if (!a || typeof a !== "object" || a.key !== key || typeof key !== "string" || key.length < 2 || key.length > 200) {
      throw new Error(`answer ${JSON.stringify(key).slice(0, 60)} is malformed`);
    }
    if (typeof a.kind !== "string" || !KINDS.has(a.kind)) throw new Error(`answer ${key}: unknown kind`);
    if (!isStrArray(a.value, MAX_OPTIONS) || !isStrArray(a.labels, MAX_LABELS) || !isStrArray(a.signatures, MAX_SIGNATURES)) {
      throw new Error(`answer ${key}: value, labels or signatures malformed`);
    }
    const sens = classify({ kind: a.kind as FieldKind, label: [key, ...(a.labels as string[])].join(" "), name: "", autocomplete: "" });
    if (sens === "secret") { droppedSecret++; continue; }
    bank.answers[key] = {
      key,
      labels: a.labels as string[],
      kind: a.kind as FieldKind,
      value: a.value as string[],
      sensitivity: sens,
      signatures: a.signatures as string[],
      uses: Number.isSafeInteger(a.uses) && (a.uses as number) >= 0 ? (a.uses as number) : 0,
      fills: Number.isSafeInteger(a.fills) && (a.fills as number) >= 0 ? (a.fills as number) : 0,
      updatedAt: typeof a.updatedAt === "string" ? a.updatedAt.slice(0, 40) : new Date(0).toISOString(),
      review: a.review === true,
    };
  }
  const bytes = bankBytes(bank);
  if (bytes > MAX_BANK_BYTES) throw new Error(`bank is ${bytes} bytes, max ${MAX_BANK_BYTES}`);
  return { bank, droppedSecret };
}

export function exportBank(bank: Bank): string {
  return JSON.stringify(bank, null, 2);
}
