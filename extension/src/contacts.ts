// Contact basics live in Jarvis, not in the answer bank. Fill resolves a known
// autocomplete token or contact label here, in code. A field that still has no
// match may ask the host to name one stored catalog key. Values are never logged.
import type { Bank, FieldInfo } from "./types";
import { BASICS, basicsValues } from "./basics";
import { classify } from "./classify";
import { intentOf } from "./intents";
import { autocompleteKey, contactOf, normalizeLabel } from "./normalize";

/** How many unmatched fields on one page may ask the model. */
export const MAX_CONTACT_ASKS = 8;

export const NOT_RUN_NOTE = "the check did not run";

/** Jarvis catalog key for each contact basic the popup shows. */
export const CONTACT_BY_ID: Readonly<Record<string, string>> = {
  first: "given_name",
  last: "family_name",
  email: "email",
  phone: "phone",
  city: "city",
  country: "country",
  linkedin: "linkedin",
  github: "github",
  website: "website",
};

/** Answer-bank key each catalog value used to be stored under. */
export const ANSWER_KEY_BY_JARVIS: Readonly<Record<string, string>> = {
  given_name: "first name",
  family_name: "last name",
  email: "email",
  phone: "phone",
  city: "city",
  country: "country",
  linkedin: "linkedin",
  github: "github",
  website: "website",
};

const MEANING_TO_JARVIS: Readonly<Record<string, string>> = {
  "first name": "given_name",
  "last name": "family_name",
  email: "email",
  phone: "phone",
  city: "city",
  country: "country",
  linkedin: "linkedin",
  github: "github",
  website: "website",
};

const CONSENT = /\b(agree|agreement|arbitrat\w*|consent|acknowledg\w*|certify|terms (and|&) conditions|privacy (notice|policy))\b/;
const SECRET_EXTRA = /\b(national id|national identification|taxpayer identification|social insurance|national insurance)\b/;

/** The catalog key an autocomplete token or contact label names, or "". */
export function jarvisKeyOf(field: Pick<FieldInfo, "label" | "autocomplete">): string {
  const meaning = autocompleteKey(field.autocomplete) || contactOf(field.label);
  return MEANING_TO_JARVIS[meaning] ?? "";
}

/**
 * A text field Fill may ask the model about. Known contact labels are resolved
 * in code. Work authorization, consent, self-identification and secrets are not.
 */
export function mayAskContactKey(field: Pick<FieldInfo, "kind" | "label" | "name" | "autocomplete">): boolean {
  if (jarvisKeyOf(field)) return false;
  if (field.kind !== "text" && field.kind !== "email" && field.kind !== "tel" && field.kind !== "url") return false;
  if (classify(field) !== "standard") return false;
  if (SECRET_EXTRA.test(normalizeLabel(field.label))) return false;
  if (intentOf(field.label)) return false;
  if (CONSENT.test(normalizeLabel(field.label))) return false;
  return true;
}

export interface ContactGet {
  status: "value" | "missing" | "not_run";
  text?: string;
  note?: string;
}

export interface ContactChoice {
  status: "filled" | "none" | "refused" | "not_run" | "empty" | "skipped" | "missing";
  key?: string;
  text?: string;
  review?: boolean;
  note?: string;
}

function record(reply: unknown): Record<string, unknown> | null {
  return typeof reply === "object" && reply !== null && !Array.isArray(reply) ? reply as Record<string, unknown> : null;
}

function notRun(note = NOT_RUN_NOTE): { status: "not_run"; note: string } {
  return { status: "not_run", note };
}

/** A host reply for get. Anything that is not a string value is missing or did not run, never a fill. */
export function readGet(reply: unknown): ContactGet {
  const r = record(reply);
  if (!r) return notRun();
  if (typeof r.error === "string" || typeof r.code === "string") {
    const msg = typeof r.error === "string" ? r.error : "";
    if (msg.includes("not stored")) return { status: "missing" };
    if (msg.includes("bank executable is missing")) return notRun(`${NOT_RUN_NOTE}: bank executable is missing`);
    return notRun(msg.includes(NOT_RUN_NOTE) ? msg : NOT_RUN_NOTE);
  }
  if (typeof r.text === "string" && r.text !== "") return { status: "value", text: r.text };
  if (r.status === "not_run") return notRun(typeof r.note === "string" ? r.note : NOT_RUN_NOTE);
  if (r.status === "missing") return { status: "missing" };
  return notRun();
}

const CHOICES = new Set(["filled", "none", "refused", "not_run", "empty", "skipped", "missing"]);

/** A host reply for choose. A missing or unknown status did not run. */
export function readChoice(reply: unknown): ContactChoice {
  const r = record(reply);
  if (!r) return notRun();
  if (typeof r.error === "string" && r.status === undefined) {
    const msg = r.error;
    if (msg.includes("bank executable is missing")) return notRun(`${NOT_RUN_NOTE}: bank executable is missing`);
    return notRun(msg.includes(NOT_RUN_NOTE) ? msg : NOT_RUN_NOTE);
  }
  const status = r.status;
  if (typeof status !== "string" || !CHOICES.has(status)) return notRun();
  if (status === "filled") {
    if (typeof r.key !== "string" || typeof r.text !== "string" || r.text === "" || r.review !== true) return notRun();
    return { status: "filled", key: r.key, text: r.text, review: true };
  }
  if (status === "not_run") return notRun(typeof r.note === "string" && r.note.includes(NOT_RUN_NOTE) ? r.note : NOT_RUN_NOTE);
  return { status: status as ContactChoice["status"], note: typeof r.note === "string" ? r.note : undefined };
}

export interface ContactEdit {
  key: string;
  text: string;
}

/** Contact basics to copy out of the bank, and later edits the popup sends as puts. */
export function contactItems(bank: Bank | null): ContactEdit[] {
  const values = basicsValues(bank);
  const out: ContactEdit[] = [];
  for (const b of BASICS) {
    if (b.group !== "contact") continue;
    const key = CONTACT_BY_ID[b.id];
    const text = (values[b.id] ?? "").trim();
    if (key && text) out.push({ key, text });
  }
  return out;
}

export function contactEdits(values: Readonly<Record<string, string>>, saved: Readonly<Record<string, string>>): { puts: ContactEdit[]; cleared: string[] } {
  const puts: ContactEdit[] = [];
  const cleared: string[] = [];
  for (const b of BASICS) {
    if (b.group !== "contact") continue;
    const key = CONTACT_BY_ID[b.id];
    if (!key) continue;
    const next = (values[b.id] ?? "").trim();
    const prev = (saved[b.id] ?? "").trim();
    if (next && next !== prev) puts.push({ key, text: next });
    else if (!next && prev) cleared.push(key);
  }
  return { puts, cleared };
}

/** The bank without catalog contact answers. Other saved answers stay. */
export function omitContactAnswers(bank: Bank): Bank {
  const drop = new Set(Object.values(ANSWER_KEY_BY_JARVIS));
  const answers: Bank["answers"] = {};
  for (const [key, answer] of Object.entries(bank.answers)) {
    if (!drop.has(key)) answers[key] = answer;
  }
  return { ...bank, answers };
}

export function readImport(reply: unknown): { ok: boolean; stored: string[]; note: string } {
  const r = record(reply);
  if (!r) return { ok: false, stored: [], note: NOT_RUN_NOTE };
  if (typeof r.error === "string") {
    const note = r.error.includes("bank executable is missing") ? `${NOT_RUN_NOTE}: bank executable is missing` : NOT_RUN_NOTE;
    return { ok: false, stored: [], note };
  }
  const stored = Array.isArray(r.stored) ? r.stored.filter((k): k is string => typeof k === "string") : [];
  const failed = Array.isArray(r.failed) ? r.failed : [];
  if (failed.length > 0 || !Array.isArray(r.stored)) {
    let note = NOT_RUN_NOTE;
    for (const item of failed) {
      const msg = record(item);
      if (msg && typeof msg.error === "string" && msg.error.includes("bank executable is missing")) {
        note = `${NOT_RUN_NOTE}: bank executable is missing`;
      }
    }
    return { ok: false, stored, note };
  }
  return { ok: true, stored, note: "" };
}
