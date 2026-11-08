// The cover letter chosen for one job. It is a sibling of the resume binding:
// Fill attaches it only to a cover-letter file or textarea, never to a resume
// box, and a missing letter is not replaced with the resume.
import { isRecord } from "./guards";
import type { FieldInfo } from "./types";
import { postingKey } from "./applied";
import { fromBase64, isPdf, MAX_RESUME_BYTES } from "./resume";

const MAX_READ = 300;
const BAN = new Set(["resume", "resumes", "cv", "cvs", "transcript", "transcripts", "other", "vitae"]);

function words(s: string): string[] {
  return s.slice(0, MAX_READ).normalize("NFD").replace(/[̀-ͯ]/g, "").toLowerCase().split(/[^a-z]+/).filter(Boolean);
}

function nameWords(s: string): string[] {
  return words(s.slice(0, MAX_READ).replace(/([a-z])([A-Z])/g, "$1 $2"));
}

function labelWords(info: Pick<FieldInfo, "label" | "name" | "automationId">): string[] {
  return [...words(info.label), ...nameWords(info.name), ...nameWords(info.automationId)];
}

/** Both words, and none of the documents a cover letter must not be given to. */
export function isCoverWords(info: Pick<FieldInfo, "label" | "name" | "automationId">): boolean {
  const all = labelWords(info);
  return all.includes("cover") && all.includes("letter") && !all.some(w => BAN.has(w));
}

/** A file input for a cover letter. "Resume or cover letter" is neither. */
export function isCoverField(info: Pick<FieldInfo, "kind" | "label" | "name" | "automationId">): boolean {
  return info.kind === "file" && isCoverWords(info);
}

/** A text box whose label asks for a cover letter. */
export function isCoverText(info: Pick<FieldInfo, "kind" | "label" | "name" | "automationId">): boolean {
  return (info.kind === "textarea" || info.kind === "text") && isCoverWords(info);
}

export function coverKey(url: unknown): string | null {
  return postingKey(url);
}

export type CoverFetch =
  | { ok: true; name: string; bytes: Uint8Array<ArrayBufferLike>; body: string; pickedAt: string; hasPDF: boolean }
  | { ok: false; reason: "no_cover" | "cover_unavailable" | "refused"; detail: string };

const clip = (s: string) => (s.length > 200 ? `${s.slice(0, 199)}…` : s);

export function parseCoverGet(r: unknown): CoverFetch {
  if (!isRecord(r)) return { ok: false, reason: "cover_unavailable", detail: "the Vani Apply Mac app sent no reply about the cover letter" };
  if (typeof r.error === "string") {
    const why = r.code === "unreachable" ? "the Vani Apply Mac app isn't reachable" : `the Vani Apply Mac app could not read the cover letter: ${r.error}`;
    return { ok: false, reason: "cover_unavailable", detail: clip(why) };
  }
  if (r.found === false) return { ok: false, reason: "no_cover", detail: "no cover letter chosen for this job" };
  if (r.found !== true) return { ok: false, reason: "cover_unavailable", detail: "the cover letter reply was not understood" };
  const body = typeof r.body === "string" ? r.body : "";
  if (body.trim() === "") return { ok: false, reason: "no_cover", detail: "the used letter has no text" };
  const name = typeof r.name === "string" && r.name.endsWith(".pdf") ? r.name : "Cover_Letter.pdf";
  let bytes: Uint8Array<ArrayBufferLike> = new Uint8Array();
  let hasPDF = false;
  if (typeof r.pdf === "string" && r.pdf !== "") {
    const decoded = fromBase64(r.pdf);
    if (!decoded || !isPdf(decoded) || decoded.length > MAX_RESUME_BYTES) {
      return { ok: false, reason: "cover_unavailable", detail: "the cover letter PDF was refused" };
    }
    if (typeof r.bytes === "number" && r.bytes !== decoded.length) {
      return { ok: false, reason: "cover_unavailable", detail: "the cover letter arrived incomplete" };
    }
    bytes = decoded;
    hasPDF = true;
  }
  return { ok: true, name, bytes, body, pickedAt: typeof r.pickedAt === "string" ? r.pickedAt.slice(0, 40) : "", hasPDF };
}

export type CoverMessage = { type: "coverGet" | "coverInfo" | "coverOpen"; url: string };

export function coverOp(m: CoverMessage): Record<string, unknown> | { error: string; code: "bad_request" } {
  const key = coverKey(m.url);
  if (!key) return { error: "this page has no job address a cover letter can be chosen for", code: "bad_request" };
  return { op: m.type, key };
}

export async function runCover(m: CoverMessage, native: (op: object) => Promise<unknown>): Promise<unknown> {
  const op = coverOp(m);
  if ("error" in op) return op;
  return native(op);
}
