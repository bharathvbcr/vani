import { type Answer, type Bank, type FieldInfo, type FieldKind, type Sensitivity, CHOICE_KINDS, emptyBank } from "./types";
import { autocompleteKey, dialCodeOnly, normalizeLabel, signature, tokens } from "./normalize";
import { classify, untouchable } from "./classify";
import { intentOf } from "./intents";

/** The capture host for answers typed in the popup: never a real site, so no per-site signature is kept. */
export const BASICS_HOST = "vani-basics";

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
 * Serialized bank cap (safety bound against memory exhaustion).
 * Chunked native-messaging streaming removes the 1 MB message limit.
 */
export const MAX_BANK_BYTES = 50_000_000;

/**
 * Whether `s` is at most `max` UTF-8 bytes. A string's length is in UTF-16 units, and a unit is 1 to 3 bytes (a
 * surrogate pair is 2 units for 4 bytes), so units <= bytes <= 3 * units: the two ends are decided without encoding.
 */
export function utf8Within(s: string, max: number): boolean {
  if (s.length > max) return false;
  if (s.length * 3 <= max) return true;
  return new TextEncoder().encode(s).length <= max;
}

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

/**
 * The meaning of a question, independent of its wording: the intent id when the
 * label reads as exactly one intent, else the autocomplete meaning, else its
 * sorted unique content tokens ("LinkedIn" and "LinkedIn Profile URL" both give
 * "linkedin"). "" when nothing is left to identify it. Kept here, not in
 * normalize.ts: intents.ts imports normalize.ts, so the reverse import is a cycle.
 * Swift's Classifier.conceptKey is held to the same extension/test/vectors/concept.json.
 */
export function conceptKey(label: string, autocomplete: string): string {
  const intent = intentOf(label);
  if (intent) return intent;
  const ac = autocompleteKey(autocomplete);
  if (ac) return ac;
  return [...tokens(label)].sort().join(" ");
}

/** Choice answers and text answers never stand in for each other. */
function kindsCompatible(a: FieldKind, b: FieldKind): boolean {
  return CHOICE_KINDS.has(a) === CHOICE_KINDS.has(b);
}

export interface OwnerIndex {
  /** The answer that already holds this question, under its own key or a sibling wording's. */
  find(f: Pick<FieldInfo, "kind" | "label" | "autocomplete">, sens: Sensitivity): Answer | undefined;
  /** Record an answer stored or changed since the index was built. */
  add(a: Answer): void;
  /** Forget what was built: answers were deleted. */
  invalidate(): void;
  /** conceptKey calls made so far: one per stored wording at build, then one per find. */
  readonly computed: number;
}

/**
 * Finds the existing answer to a question. The concept map is built once, on the
 * first miss on the exact key, from the key and every wording of each answer (O(N));
 * a lookup is then one map read, however many answers the bank holds. A concept
 * key only counts when non-empty, the kinds are compatible and the sensitivity is
 * equal. Among several matches the most used wins, then the newest, then the key.
 */
export function ownerIndex(answers: Record<string, Answer>): OwnerIndex {
  let byConcept: Map<string, Set<string>> | null = null;
  let computed = 0;
  const addAnswer = (m: Map<string, Set<string>>, a: Answer) => {
    for (const l of [a.key, ...a.labels]) {
      computed++;
      const c = conceptKey(l, "");
      if (!c) continue;
      const set = m.get(c);
      if (set) set.add(a.key); else m.set(c, new Set([a.key]));
    }
  };
  return {
    find(f, sens) {
      const direct = Object.hasOwn(answers, answerKey(f)) ? answers[answerKey(f)] : undefined;
      if (direct) return direct;
      computed++;
      const want = conceptKey(f.label, f.autocomplete);
      if (!want) return undefined;
      if (!byConcept) {
        byConcept = new Map();
        for (const a of Object.values(answers)) addAnswer(byConcept, a);
      }
      let best: Answer | undefined;
      for (const k of byConcept.get(want) ?? []) {
        const a = Object.hasOwn(answers, k) ? answers[k] : undefined;
        if (!a || a.sensitivity !== sens || !kindsCompatible(a.kind, f.kind)) continue;
        // The stored row is re-classified from its key and every wording joined (parseBank): a
        // wording that would change that reading (to secret, which drops the row, or to self_id)
        // makes no owner.
        const joined = (extra: string[]): Sensitivity => classify({ kind: a.kind, label: [a.key, ...a.labels, ...extra].join(" "), name: "", autocomplete: "" });
        if (joined([f.label]) !== joined([])) continue;
        if (!best || a.uses > best.uses || (a.uses === best.uses && (a.updatedAt > best.updatedAt || (a.updatedAt === best.updatedAt && a.key < best.key)))) best = a;
      }
      return best;
    },
    add(a) {
      if (byConcept) addAnswer(byConcept, a);
    },
    invalidate() {
      byConcept = null;
    },
    get computed() {
      return computed;
    },
  };
}

/** One-shot owner lookup; callers that look up many fields keep an ownerIndex instead. */
export function findOwner(bank: Bank, f: Pick<FieldInfo, "kind" | "label" | "autocomplete">, sens: Sensitivity): Answer | undefined {
  return ownerIndex(bank.answers).find(f, sens);
}

function clip(xs: string[], n: number): string[] {
  return xs.slice(0, n);
}

/**
 * A new value that is only dialling codes does not replace a saved answer that
 * says more. job-boards.greenhouse.io's Country shows just "+1" once United
 * States is chosen; saving that over "United States" would leave the next form
 * nothing to choose from. Where "+1" is all there is, it is kept as the answer.
 */
/** A question whose answer may be a bare dialling code: a code, prefix or phone-country picker. */
const CODE_QUESTION = /\b(code|dial|dialing|dialling|calling|prefix|phone|telephone|mobile|tel)\b/;

/**
 * A value that is only dialling codes, captured from a question that does not
 * ask for a code: job-boards.greenhouse.io's Country shows "+1" once a country is
 * chosen, which says nothing about which one. Never stored or sent.
 */
export function bareCodeOutOfPlace(label: string, value: readonly string[]): boolean {
  return value.length > 0 && value.every(dialCodeOnly) && !CODE_QUESTION.test(normalizeLabel(label));
}

export function keepsSaved(saved: readonly string[], value: readonly string[]): boolean {
  return value.length > 0 && value.every(dialCodeOnly) && !saved.every(dialCodeOnly);
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
  const owners = ownerIndex(out.answers);
  for (const f of fields) {
    if (f.kind === "password") { res.skippedSecret++; continue; }
    if (untouchable(f.kind)) continue;
    const sens = classify(f);
    if (sens === "secret") { res.skippedSecret++; continue; }
    if (sens === "self_id" && !bank.settings.captureSelfId) { res.skippedSelfId++; continue; }
    const value = clip(f.value.map(v => v.trim()).filter(Boolean), MAX_OPTIONS).map(v => v.slice(0, MAX_VALUE_CHARS));
    // An unchecked single checkbox reads "no": that is an answer, but an
    // empty text box is not. When saving basics, an empty value means the
    // user explicitly cleared that basic: remove it and related wordings.
    if (value.length === 0) {
      if (host === BASICS_HOST) {
        const key = answerKey(f);
        const want = intentOf(f.label);
        let removed = false;
        if (out.answers[key]) {
          delete out.answers[key];
          removed = true;
        }
        if (want) {
          for (const [k, a] of Object.entries(out.answers)) {
            if ([a.key, ...a.labels].some(l => intentOf(l) === want)) {
              delete out.answers[k];
              removed = true;
            }
          }
        }
        if (removed) { res.updated++; owners.invalidate(); }
      } else {
        res.skippedEmpty++;
      }
      continue;
    }
    const key = answerKey(f);
    if (key.length < 2) { res.skippedEmpty++; continue; }
    if (bareCodeOutOfPlace(f.label, value)) { res.skippedEmpty++; continue; }
    // The answer that already holds this question, under this key or a sibling
    // wording's ("LinkedIn" / "LinkedIn Profile URL"): stored into, never renamed.
    const prev = owners.find(f, sens);
    const target = prev?.key ?? key;
    if (prev && keepsSaved(prev.value, value)) continue;
    if (!prev && Object.keys(out.answers).length >= MAX_ANSWERS) { res.skippedEmpty++; continue; }
    const label = normalizeLabel(f.label);
    const sig = signature(host, f);
    // Long free text is job-specific, and a model draft is never a confident
    // answer however short: both are filled next time only as drafts to review.
    const long = f.drafted === true || (!CHOICE_KINDS.has(f.kind) && value.join(" ").length > REVIEW_CHARS);
    const next: Answer = {
      key: target,
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
    out.answers[target] = next;
    owners.add(next);
    if (prev) res.updated++; else res.added++;

    // Every other answer that shares this intent (a basic or a captured wording) takes the new value.
    const want = intentOf(f.label);
    if (want) {
      for (const [k, a] of Object.entries(out.answers)) {
        if (k !== target && [a.key, ...a.labels].some(l => intentOf(l) === want)) {
          out.answers[k] = {
            ...a,
            value,
            updatedAt: now.toISOString(),
            review: host === BASICS_HOST ? false : long,
          };
        }
      }
    }
  }
  return res;
}

export interface CaptureDelta {
  upsert: Answer[];
  deleteKeys: string[];
  counts: Omit<CaptureResult, "bank">;
}

/**
 * Computes the minimal delta (upsert and deleteKeys) for granular row-level patching.
 */
export function mergeCaptureDelta(bank: Bank, fields: FieldInfo[], host: string, now: Date): CaptureDelta {
  const result = mergeCapture(bank, fields, host, now);
  const upsert: Answer[] = [];
  const deleteKeys: string[] = [];
  for (const [k, a] of Object.entries(result.bank.answers)) {
    if (bank.answers[k] !== a) {
      upsert.push(a);
    }
  }
  for (const k of Object.keys(bank.answers)) {
    if (!result.bank.answers[k]) {
      deleteKeys.push(k);
    }
  }
  const { bank: _, ...counts } = result;
  return { upsert, deleteKeys, counts };
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
  if (!utf8Within(json, MAX_BANK_BYTES)) throw new Error(`bank file is larger than ${MAX_BANK_BYTES} bytes`);
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
