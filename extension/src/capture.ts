// What a flush sends: only answers the bank does not already hold, so the same
// page state captured twice changes nothing (mergeCapture bumps `uses` on every
// call). Secrets and unopted self-ID leave the page with their value removed.
import type { Bank, FieldInfo } from "./types";
import { MAX_OPTIONS, MAX_VALUE_CHARS, answerKey, bareCodeOutOfPlace, keepsSaved, ownerIndex } from "./bank";
import { classify, untouchable } from "./classify";
import { normalizeLabel, signature } from "./normalize";

/** The value mergeCapture would store for this field. */
function stored(f: FieldInfo): string[] {
  return f.value.map(v => v.trim()).filter(Boolean).slice(0, MAX_OPTIONS).map(v => v.slice(0, MAX_VALUE_CHARS));
}

/** A copy that is safe to hold and send: secrets never carry their value. */
export function redact(f: FieldInfo, captureSelfId: boolean): FieldInfo {
  const sens = classify(f);
  if (f.kind === "password" || sens === "secret" || (sens === "self_id" && !captureSelfId)) return { ...f, value: [] };
  return f;
}

/**
 * What a flush would carry for this field, as one comparable string: the value
 * that would be stored, the wording and the per-site signature. "" when there
 * is nothing to carry (empty, or a secret whose value is removed).
 */
export function fieldDigest(f: FieldInfo, host: string, captureSelfId: boolean): string {
  const r = redact(f, captureSelfId);
  const value = stored(r);
  return value.length === 0 ? "" : JSON.stringify([value, normalizeLabel(r.label), signature(host, r)]);
}

export interface Diff {
  send: FieldInfo[];
  /** Answers the bank already holds exactly (value, wording and site). */
  unchanged: number;
  /** Sensitive fields (value removed), sent only alongside real changes. */
  sensitive: number;
}

export function pendingDiff(bank: Bank, fields: FieldInfo[], host: string, explicit = false): Diff {
  const changed: FieldInfo[] = [];
  const sens: FieldInfo[] = [];
  let unchanged = 0;
  const owners = ownerIndex(bank.answers);
  for (const raw of fields) {
    if (untouchable(raw.kind) && raw.kind !== "password") continue;
    const f = redact(raw, bank.settings.captureSelfId);
    const value = stored(f);
    if (value.length === 0) {
      if (raw.kind === "password" || classify(raw) !== "standard") sens.push(f);
      continue;
    }
    if (bareCodeOutOfPlace(f.label, value)) continue; // not an answer (bank.bareCodeOutOfPlace): nothing to send
    // Held under this key or a sibling wording's (bank.ownerIndex), as mergeCapture will store it.
    const a = answerKey(f).length >= 2 ? owners.find(f, classify(raw)) : undefined;
    const label = normalizeLabel(f.label);
    const sig = signature(host, f);
    // A bare dialling code over a fuller saved answer is never stored (bank.keepsSaved): nothing to send.
    const same = !!a && (keepsSaved(a.value, value) || (a.value.length === value.length && a.value.every((v, i) => v === value[i]) &&
      (!label || a.labels.includes(label)) && (!sig || a.signatures.includes(sig))));
    if (same) unchanged++; else changed.push(f);
  }
  const send = changed.length > 0 || explicit ? [...changed, ...sens] : [];
  return { send, unchanged, sensitive: sens.length };
}
