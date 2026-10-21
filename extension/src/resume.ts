// The resume chosen for one job. The owner binds a PDF to a job's application
// page from the popup (the Mac app keeps a copy, keyed by the page's pageKey); on
// Fill, the file input that reads as a resume gets that PDF and nothing else. A
// job with nothing bound gets no file and a reason: there is no "default" resume,
// because a resume meant for another job is the wrong one. No DOM here, so
// `bun test` covers it; fill.ts attaches the bytes.
//
// The PDF goes from the Mac app to the tab being filled and nowhere else. Its
// file name (which carries the owner's name) is shown on the page and in the
// popup, and never logged.
import { isRecord } from "./guards";
import type { Bank, FieldInfo } from "./types";
import type { JobInfo } from "./draft";
import { pageKey, postingKey } from "./applied";
import { basicsValues } from "./basics";
import { JOB_HELP } from "./suggestjob";

/**
 * The largest resume accepted. Native messaging replies are capped at 1 MiB and
 * base64 adds a third, so 512 KiB leaves room for the envelope. ResumeStore.maxBytes
 * (macos/Sources/VaniApplyKit/ResumeStore.swift) is the same number.
 */
export const MAX_RESUME_BYTES = 512 * 1024;
const MAX_NAME_CHARS = 200;
const MAX_DETAIL = 200;
/** Only this much of a label or name is read: a page can put anything in one. */
const MAX_READ = 300;

export type ResumeFetch =
  | { ok: true; name: string; bytes: Uint8Array; pickedAt: string }
  | { ok: false; reason: "no_resume" | "resume_unavailable"; detail: string };

/** What the popup shows for the active job: the bound file, none, or why it cannot say. */
export type ResumeInfo =
  | { found: true; name: string; bytes: number; pickedAt: string }
  | { found: false }
  | { error: string };

const RESUME_WORDS = new Set(["resume", "resumes", "cv", "cvs"]);
/** A box for another document, or for several: never given the resume. */
const OTHER_WORDS = new Set([
  "cover", "letter", "other", "additional", "transcript", "transcripts", "portfolio", "sample", "samples",
  "supporting", "reference", "references", "certificate", "certificates", "writing", "photo", "headshot",
]);

function words(s: string): string[] {
  return s.slice(0, MAX_READ).normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase().split(/[^a-z]+/).filter(Boolean);
}

/** camelCase names (resumeUpload) split before lower-casing. */
function nameWords(s: string): string[] {
  return words(s.slice(0, MAX_READ).replace(/([a-z])([A-Z])/g, "$1 $2"));
}

/**
 * A file input for the resume: its label, name or test id has the word resume,
 * résumé, CV or "curriculum vitae", and none of them names another document
 * (cover letter, other attachments, transcript...). "Resume or cover letter" is
 * two documents in one box, so it is not filled either.
 */
export function isResumeField(info: Pick<FieldInfo, "kind" | "label" | "name" | "automationId">): boolean {
  if (info.kind !== "file") return false;
  const all = [...words(info.label), ...nameWords(info.name), ...nameWords(info.automationId)];
  const resume = all.some(w => RESUME_WORDS.has(w)) || all.some((w, i) => w === "curriculum" && all[i + 1] === "vitae");
  return resume && !all.some(w => OTHER_WORDS.has(w));
}

/**
 * The key a resume is bound under: the posting's key (applied.ts postingKey, one per posting even on
 * Workday's several routes), or null for a page that has none. The Mac app treats it as an opaque URL.
 */
export function resumeKey(url: unknown): string | null {
  return postingKey(url);
}

/**
 * The key a resume was bound under before Workday's routes shared one: this page's own pageKey.
 * Null when it is the same as resumeKey. Only a resume bound on the route being looked up is
 * recovered this way; one stored under another route's key is not (the Mac app would have to re-key it).
 */
export function legacyResumeKey(url: unknown): string | null {
  const legacy = pageKey(url);
  return legacy !== null && legacy !== resumeKey(url) ? legacy : null;
}

export function isPdf(b: Uint8Array): boolean {
  return b.length >= 5 && b[0] === 0x25 && b[1] === 0x50 && b[2] === 0x44 && b[3] === 0x46 && b[4] === 0x2d; // %PDF-
}

/** A bounded, printable file name ending in .pdf, trimmed; null otherwise. */
export function checkResumeName(raw: unknown): string | null {
  if (typeof raw !== "string") return null;
  const s = raw.trim();
  if (s.length > MAX_NAME_CHARS || !/\.pdf$/i.test(s) || s.length <= 4) return null;
  if (/[\u0000-\u001f\u007f-\u009f/\\]/.test(s)) return null;
  return s;
}

export function toBase64(b: Uint8Array): string {
  let s = "";
  for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode(...b.subarray(i, i + 0x8000));
  return btoa(s);
}

/** Strict base64 (no whitespace, correct padding), or null. */
export function fromBase64(s: unknown): Uint8Array | null {
  if (typeof s !== "string" || s.length % 4 !== 0 || !/^[A-Za-z0-9+/]*={0,2}$/.test(s)) return null;
  let bin: string;
  try { bin = atob(s); } catch { return null; }
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

const clip = (s: string) => (s.length > MAX_DETAIL ? `${s.slice(0, MAX_DETAIL - 1)}…` : s);
const unavailable = (detail: string): ResumeFetch => ({ ok: false, reason: "resume_unavailable", detail: clip(detail) });

/**
 * The Mac app's resumeGet reply (through the background, which turns an
 * unreachable app into `{error, code: "unreachable"}`). Anything but a checked
 * PDF is a reason; nothing is ever passed on unchecked.
 */
export function parseResumeGet(r: unknown): ResumeFetch {
  if (!isRecord(r)) return unavailable("the Vani Apply Mac app sent no reply about the resume");
  if (typeof r.error === "string") {
    const why = r.code === "unreachable" ? "the Vani Apply Mac app isn't reachable" : `the Vani Apply Mac app could not read the resume: ${r.error}`;
    return unavailable(why);
  }
  if (r.found === false) return { ok: false, reason: "no_resume", detail: "no resume chosen for this job (choose one in the Vani Apply popup)" };
  if (r.found !== true) return unavailable("the Vani Apply Mac app's resume reply was not understood");
  const name = checkResumeName(r.name);
  const bytes = fromBase64(r.pdf);
  if (!name) return unavailable("the resume's file name was refused");
  if (!bytes) return unavailable("the resume arrived damaged");
  if (bytes.length > MAX_RESUME_BYTES) return unavailable(`the resume is over ${MAX_RESUME_BYTES / 1024} KB`);
  if (!isPdf(bytes)) return unavailable("the resume is not a PDF");
  if (r.bytes !== bytes.length) return unavailable("the resume arrived incomplete");
  return { ok: true, name, bytes, pickedAt: typeof r.pickedAt === "string" ? r.pickedAt.slice(0, 40) : "" };
}

export type ResumeMessage =
  | { type: "resumeGet" | "resumeInfo" | "resumeDelete"; url: string }
  | { type: "resumePut"; url: string; name: string; pdf: string };

/** base64 of MAX_RESUME_BYTES plus padding slack: a longer string is refused before it is decoded. */
const MAX_BASE64 = Math.ceil(MAX_RESUME_BYTES / 3) * 4 + 8;

/**
 * The native request for a resume message, checked here so a bad file is refused with a
 * reason before it crosses to the Mac app (which checks it again).
 */
export function resumeOp(m: ResumeMessage): Record<string, unknown> | { error: string; code: "bad_request" | "too_large" } {
  const key = resumeKey(m.url);
  if (!key) return { error: "this page has no job address a resume can be chosen for", code: "bad_request" };
  if (m.type !== "resumePut") return { op: m.type, key };
  const file = checkResumeFile(m.name, m.pdf);
  if ("error" in file) return file;
  return { op: "resumePut", key, name: file.name, pdf: m.pdf };
}

/**
 * A resume as it crosses a message (a resumePut, or an attachResume to a page): a bounded .pdf name and
 * base64 that decodes to a PDF of at most MAX_RESUME_BYTES (exactly that many is fine). The one check
 * every path runs before the bytes are stored or placed in a page.
 */
export function checkResumeFile(rawName: unknown, pdf: unknown): { name: string; bytes: Uint8Array } | { error: string; code: "bad_request" | "too_large" } {
  const name = checkResumeName(rawName);
  if (!name) return { error: `choose a .pdf file with a name of at most ${MAX_NAME_CHARS} characters`, code: "bad_request" };
  if (typeof pdf !== "string") return { error: "the file could not be read", code: "bad_request" };
  if (pdf.length > MAX_BASE64) return { error: `the resume is over ${MAX_RESUME_BYTES / 1024} KB`, code: "too_large" };
  const bytes = fromBase64(pdf);
  if (!bytes || bytes.length === 0) return { error: "the file could not be read", code: "bad_request" };
  if (bytes.length > MAX_RESUME_BYTES) return { error: `the resume is over ${MAX_RESUME_BYTES / 1024} KB`, code: "too_large" };
  if (!isPdf(bytes)) return { error: "that file is not a PDF", code: "bad_request" };
  return { name, bytes };
}

const isError = (r: unknown): boolean => isRecord(r) && typeof r.error === "string";

/**
 * One resume message end to end: the native call under the posting's key and, for a page whose key
 * changed (Workday), the key it used to have.
 *  - get / info: not found under the new key is looked up under the old one; a failed lookup is
 *    returned as the failure, never as "not found".
 *  - delete: removes both, or the old binding would answer the next lookup.
 *  - put: binds the new key, then retires this page's old binding so one job holds one slot.
 */
export async function runResume(m: ResumeMessage, native: (op: object) => Promise<unknown>): Promise<unknown> {
  const op = resumeOp(m);
  if ("error" in op) return op;
  const legacy = legacyResumeKey(m.url);
  const old = legacy === null ? null : { op: m.type === "resumePut" ? "resumeDelete" : m.type, key: legacy };
  const r = await native(op);
  if (old === null || isError(r)) return r;
  switch (m.type) {
    case "resumeGet":
    case "resumeInfo":
      return isRecord(r) && r.found === false ? native(old) : r;
    case "resumeDelete": {
      const o = await native(old);
      if (isError(o)) return o;
      return isRecord(r) && isRecord(o) && o.removed === true ? { ...r, removed: true } : r;
    }
    default:
      await native(old).catch(() => undefined); // the new binding stands even if the old one cannot be retired
      return r;
  }
}

// --- building one (the offer on a form with no resume chosen) ---------------------------
// `vani resume` (run by the Vani Apply agent, job kind "resume") builds a PDF for the job
// with DevPrism: its own plain layout from the knowledge base, not one of the owner's LaTeX
// resumes, and no model call. The header is the owner's Your basics, the same name and
// contact details Fill puts on the form; it goes to the Mac app with the job and nowhere else.

/** The agent allows a resume job 180 s; leave room for start-up and the last poll. */
export const RESUME_JOB_TIMEOUT_MS = 195_000;
/** The request is capped at 128 KiB by the agent; a long posting fits well under it. */
const MAX_JOB_DESCRIPTION = 100_000;
const MAX_JOB_LINE = 300;

export interface ResumeHeader { name: string; contact_lines: string[] }

/**
 * The resume header from Your basics: first and last name, then "email | phone | city, country"
 * and "LinkedIn | GitHub | website" (each line only when something is in it). Null without a name.
 */
export function resumeHeader(bank: Bank | null): ResumeHeader | null {
  const v = basicsValues(bank);
  const name = [v.first, v.last].map(s => (s ?? "").trim()).filter(Boolean).join(" ");
  if (!name) return null;
  const line = (parts: (string | undefined)[]) => parts.map(s => (s ?? "").trim()).filter(Boolean).join(" | ");
  const place = [v.city, v.country].map(s => (s ?? "").trim()).filter(Boolean).join(", ");
  const contact_lines = [line([v.email, v.phone, place]), line([v.linkedin, v.github, v.website])].filter(Boolean)
    .map(l => l.slice(0, 200));
  return { name: name.slice(0, 200), contact_lines };
}

/** The `vani resume` request; throws when the page has no job description to build from. */
export function buildResumeRequest(job: JobInfo, header: ResumeHeader): object {
  const description = job.description.trim().slice(0, MAX_JOB_DESCRIPTION);
  if (!description) throw new Error("this page has no job description to build a resume from");
  const one = (s: string) => s.replace(/\s+/g, " ").trim().slice(0, MAX_JOB_LINE);
  return { job: { company: one(job.company), title: one(job.title), description }, header };
}

/** Codes whose text from vani itself says more than the shared job help does. */
const OWN_TEXT = new Set(["config", "devprism", "render", "pages", "too_large", "no_name", "bad_request"]);

/** A built resume from `vani resume`'s reply (through runJob), checked like a stored one; or why not. */
export function parseBuiltResume(r: unknown): ResumeFetch {
  if (!isRecord(r)) return unavailable("Vani sent no resume");
  if (typeof r.error === "string") {
    const code = typeof r.code === "string" ? r.code : "";
    return unavailable(!OWN_TEXT.has(code) && JOB_HELP[code] ? JOB_HELP[code]! : r.error);
  }
  const name = checkResumeName(r.name);
  const bytes = fromBase64(r.pdf);
  if (!name) return unavailable("the built resume's file name was refused");
  if (!bytes || bytes.length === 0) return unavailable("the built resume arrived damaged");
  if (bytes.length > MAX_RESUME_BYTES) return unavailable(`the built resume is over ${MAX_RESUME_BYTES / 1024} KB`);
  if (!isPdf(bytes)) return unavailable("the built resume is not a PDF");
  if (r.bytes !== bytes.length) return unavailable("the built resume arrived incomplete");
  return { ok: true, name, bytes, pickedAt: new Date().toISOString() };
}

/** The Mac app's resumeInfo reply, for the popup. */
export function parseResumeInfo(r: unknown): ResumeInfo {
  if (!isRecord(r)) return { error: "the Vani Apply Mac app sent no reply" };
  if (typeof r.error === "string") return { error: clip(r.error) };
  if (r.found === false) return { found: false };
  const name = checkResumeName(r.name);
  if (r.found === true && name && typeof r.bytes === "number" && Number.isSafeInteger(r.bytes) && r.bytes >= 0) {
    return { found: true, name, bytes: r.bytes, pickedAt: typeof r.pickedAt === "string" ? r.pickedAt.slice(0, 40) : "" };
  }
  return { error: "the Vani Apply Mac app's reply was not understood" };
}
