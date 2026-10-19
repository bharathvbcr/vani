// Pure helpers for fields that are not one-control-one-answer: repeated
// sections (work history 1..n), dates split into month/day/year, and phone
// numbers split into country code + number. No DOM, so `bun test` covers them.
import type { FieldInfo } from "./types";
import { normalizeLabel } from "./normalize";

// --- repeated sections ---------------------------------------------------------

// The entry marker has to survive normalizeLabel (letters and digits only), so it
// is a namespaced word pair no form writes: "vani section 2". Earlier versions
// wrote a bare "section 2", which a genuine label such as "Project Section 2"
// also produces; those keys are still read (see entryReadings) but never written.
const MARK = /(?:^| )vani section (\d{1,2})$/;
const LEGACY_MARK = /(?:^| )section (\d{1,2})$/;

/** "Job title" in the 2nd entry reads "Job title (vani section 2)"; the 1st entry keeps its plain label. */
export function sectionLabel(label: string, n: number): string {
  if (n < 2) return label;
  // normalizeLabel cuts at 200 characters: shorten the question, never the marker.
  let base = label;
  for (;;) {
    const out = `${base} (vani section ${n})`;
    if (MARK.test(normalizeLabel(out)) || base.length === 0) return out;
    base = base.slice(0, Math.max(0, base.length - 20)).trimEnd();
  }
}

/**
 * The page's own label with any entry marker removed, so a page cannot forge
 * entry n of another question by ending a label with the reserved words.
 */
export function stripEntryMark(label: string): string {
  let s = label;
  for (let i = 0; i < 4; i++) {
    const next = s.replace(/\s*\(?\s*vani\s+section\s+\d{1,2}\s*\)?\s*$/i, "");
    if (next === s) break;
    s = next;
  }
  return s;
}

/** The entry number a normalized label or answer key carries: 1 when it has no marker. */
export function sectionOf(normalized: string): number {
  const m = MARK.exec(normalized);
  return m ? Number(m[1]) : 1;
}

/** One way of reading a normalized label: the question text and its entry number. */
export interface EntryReading { base: string; n: number }

/**
 * How a normalized label can be read. A label from the page has one reading.
 * A stored label or key may also be an older "<question> section n" key; that
 * cannot be told from a genuine label ending in "section n", so both readings
 * are offered and the caller compares entry numbers against each. Exact
 * equality is always tried on the string itself first, so a genuine label
 * still finds its own answer.
 */
export function entryReadings(normalized: string, stored: boolean): EntryReading[] {
  const m = MARK.exec(normalized);
  if (m) return [{ base: normalized.slice(0, m.index), n: Number(m[1]) }];
  const out: EntryReading[] = [{ base: normalized, n: 1 }];
  if (stored) {
    const lm = LEGACY_MARK.exec(normalized);
    if (lm && Number(lm[1]) >= 2) out.push({ base: normalized.slice(0, lm.index), n: Number(lm[1]) });
  }
  return out;
}

/** The pre-marker spelling of an entry label ("job title vani section 2" -> "job title section 2"), or "" for entry 1. */
export function legacyEntryKey(normalized: string): string {
  const m = MARK.exec(normalized);
  return m && Number(m[1]) >= 2 ? `${normalized.slice(0, m.index)} section ${m[1]}`.trim() : "";
}

/**
 * Give the 2nd, 3rd... occurrence of the same question (same kind and wording)
 * its own entry number, so employer 2 does not overwrite employer 1. The first
 * occurrence is untouched, which keeps every key saved before this existed
 * readable. Entry n>=2 loses its `autocomplete` (two "address-line1" fields would
 * otherwise share one key) and gets `@n` on its name and automation id so its
 * per-site signature is its own.
 */
export function assignSections(infos: FieldInfo[]): void {
  const seen = new Map<string, number>();
  for (const f of infos) {
    if (MARK.test(normalizeLabel(f.label))) f.label = stripEntryMark(f.label); // reserved words: only we write the marker
    const base = normalizeLabel(f.label);
    if (base.length < 2) continue;
    const id = `${f.kind}|${base}`;
    const n = (seen.get(id) ?? 0) + 1;
    seen.set(id, n);
    if (n < 2) continue;
    f.label = sectionLabel(f.label, n);
    f.autocomplete = "";
    if (f.name) f.name = `${f.name}@${n}`;
    if (f.automationId) f.automationId = `${f.automationId}@${n}`;
  }
}

// --- dates -----------------------------------------------------------------------

export type DatePart = "month" | "day" | "year";

const MONTHS = ["january", "february", "march", "april", "may", "june", "july", "august", "september", "october", "november", "december"];

/** 1..12 for "5", "05", "May", "may.", "September"; 0 when it is not a month. */
export function monthIndex(text: string): number {
  const t = text.trim().toLowerCase().replace(/\.$/, "");
  if (/^\d{1,2}$/.test(t)) { const n = Number(t); return n >= 1 && n <= 12 ? n : 0; }
  if (t.length < 3) return 0;
  const i = MONTHS.findIndex(m => m === t || (t.length === 3 && m.startsWith(t)));
  return i >= 0 ? i + 1 : 0;
}

const PART_WORDS: Record<string, DatePart> = {
  month: "month", mm: "month", day: "day", dd: "day", year: "year", yyyy: "year", yy: "year",
};

/**
 * Which part of a date a control is, from its own label, aria-label or
 * placeholder ("Month", "MM") or a stable id ("dateSectionYear-input"). Whole
 * words only, so "Years of experience" is not a year part.
 */
export function datePartRole(hint: { label: string; ids: string[] }): DatePart | "" {
  const l = normalizeLabel(hint.label);
  if (PART_WORDS[l]) return PART_WORDS[l]!;
  for (const id of hint.ids) {
    const m = /(?:^|[-_.[\]])(?:date)?(?:section)?(month|day|year)(?:-input|-display)?(?:$|[-_.[\]])/i.exec(id);
    if (m) return m[1]!.toLowerCase() as DatePart;
  }
  return "";
}

/** ISO "YYYY-MM-DD", or "YYYY-MM" when no day; "" unless a 4-digit year and a month are present. */
export function composeDate(p: { y?: string; m?: string; d?: string }): string {
  const y = (p.y ?? "").trim();
  const m = monthIndex(p.m ?? "");
  const d = Number((p.d ?? "").trim());
  if (!/^\d{4}$/.test(y) || !m) return "";
  const mm = String(m).padStart(2, "0");
  if (Number.isInteger(d) && d >= 1 && d <= 31) return `${y}-${mm}-${String(d).padStart(2, "0")}`;
  return `${y}-${mm}`;
}

/** Read "2024-05-01", "2024-05", "05/01/2024", "05/2024" (US order for 3 parts). */
export function parseDate(v: string): { y: string; m: string; d: string } | null {
  const s = v.trim();
  let m = /^(\d{4})-(\d{1,2})(?:-(\d{1,2}))?$/.exec(s);
  if (m) return fix(m[1]!, m[2]!, m[3] ?? "");
  m = /^(\d{1,2})\/(\d{1,2})\/(\d{4})$/.exec(s);
  if (m) return fix(m[3]!, m[1]!, m[2]!);
  m = /^(\d{1,2})\/(\d{4})$/.exec(s);
  if (m) return fix(m[2]!, m[1]!, "");
  return null;
}

function fix(y: string, mo: string, d: string): { y: string; m: string; d: string } | null {
  const mi = monthIndex(mo);
  if (!mi) return null;
  if (d && !(Number(d) >= 1 && Number(d) <= 31)) return null;
  return { y, m: String(mi).padStart(2, "0"), d: d ? d.padStart(2, "0") : "" };
}

// --- phone -----------------------------------------------------------------------

export type PhonePart = "code" | "number";

/** The calling-code digits in "United States (+1)", "+44", "1"; "" when there are none. */
export function codeDigits(text: string): string {
  const plus = /\+\s*(\d{1,3})\b/.exec(text);
  if (plus) return plus[1]!;
  const bare = /^\s*(\d{1,3})\s*$/.exec(text);
  return bare ? bare[1]! : "";
}

/** One saved answer from the two controls: "+1 5550100", or the number alone when the code is unreadable. */
export function composePhone(code: string, national: string): string {
  const n = national.trim();
  const cc = codeDigits(code);
  return n && cc ? `+${cc} ${n}` : n;
}

/**
 * Split a saved phone number into calling code and national part. Unambiguous
 * only: "+1 555 0100" splits at the space our own capture writes. A bare
 * "+15550100" splits only when `knownCodes` (the digits offered by the page's
 * code control) contains exactly one prefix of it. A number with no "+" has
 * no code. Returns null when a code is present but cannot be separated.
 */
export function parsePhone(v: string, knownCodes: string[] = []): { cc: string; national: string } | null {
  const s = v.trim();
  if (!s) return null;
  if (!s.startsWith("+")) return { cc: "", national: s };
  const sp = /^\+\s*(\d{1,3})[\s.-]+(.+)$/.exec(s);
  if (sp) return { cc: sp[1]!, national: sp[2]!.trim() };
  const bare = /^\+(\d{4,15})$/.exec(s);
  if (!bare) return null;
  const digits = bare[1]!;
  const hits = [...new Set(knownCodes)].filter(c => digits.startsWith(c) && digits.length > c.length);
  if (hits.length !== 1) return null;
  return { cc: hits[0]!, national: digits.slice(hits[0]!.length) };
}

/** Is this control the country-code half of a split phone? */
export function isPhoneCode(hint: { label: string; autocomplete: string; ids: string[] }): boolean {
  if (/\btel-country-code\b/.test(hint.autocomplete)) return true;
  if (/\b(country|phone|dial|dialing|dialling|calling|area) code\b/.test(normalizeLabel(hint.label))) return true;
  return hint.ids.some(id => /(country|phone|dial)[-_ ]?(phone)?[-_ ]?code/i.test(id));
}

/** Is this control a phone number (the half that is not the code)? */
export function isPhoneNumber(f: { kind: string; label: string; autocomplete: string }): boolean {
  if (/\btel(-national|-local)?\b/.test(f.autocomplete)) return true;
  if (f.kind === "tel") return true;
  return /\b(phone|mobile|telephone|cell)\b/.test(normalizeLabel(f.label));
}
