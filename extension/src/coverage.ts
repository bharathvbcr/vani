// What the extension made of the current page: the owner's check that it
// "collects all the fields". Pure, shared by the scan reply, the panel and the popup.
import type { Bank, FieldInfo, Match } from "./types";
import { classify, untouchable } from "./classify";
import { matchField } from "./match";
import { DRAFT_EMPTY_NOTE, UNDRAFTED_NOTE, type DraftOffer, draftOffer } from "./suggest";

export const MAX_LISTED = 50;

export interface Coverage {
  /** Controls read from the page (a radio group, a split date or a combobox is one). */
  found: number;
  /** Has a saved answer, including manual ones. */
  known: number;
  /** found = fillable + unknown + manual + skipped; these four never overlap. */
  fillable: number;
  unknown: number;
  manual: number;
  skipped: number;
  /** The scan stopped at its field cap: `found` is a sample and `unread` more controls were not read. */
  capped: boolean;
  unread: number;
  unknownLabels: string[];
  /** unknown > unknownLabels.length when the list was cut at MAX_LISTED. */
  unknownListed: number;
  /**
   * One row per listed unknown field, in the same order as `unknownLabels`.
   * `offer` says whether that field can be drafted, regenerated, or only answered on the page.
   * A scan that stopped at MAX_LISTED omits the rest here too.
   */
  questions: PageQuestion[];
  manualLabels: string[];
  /** Manual fields, with the scanner's reason. Same cap as `manualLabels`. */
  manualQuestions: PageQuestion[];
  /** Secret and self-id fields that were not drafted. The value is never copied onto the row. */
  held: PageQuestion[];
}

/** A detected question with no saved answer, and what the extension can do for it. */
export interface PageQuestion {
  label: string;
  offer: DraftOffer;
  note: string;
  /** Index of this field in the frame's scan. Absent on a legacy row that only has a label. */
  ordinal?: number;
  /** Frame that reported the field. Set when per-frame reports are added together. */
  frameId?: number;
}

export type Bucket = "skipped" | "manual" | "fillable" | "unknown";

/** The one place a field is sorted, so scan and fill cannot disagree. `onMatch` receives the match it computed (not called for a skipped field or no bank). */
export function bucketOf(f: FieldInfo, manual: string, bank: Bank | null, host: string, onMatch?: (m: Match | null) => void): { bucket: Bucket; known: boolean } {
  if (untouchable(f.kind)) return { bucket: "skipped", known: false };
  const sens = classify(f);
  if (sens === "secret" || (sens === "self_id" && !(bank?.settings.captureSelfId))) return { bucket: "skipped", known: false };
  const match = bank ? matchField(f, bank, host) : undefined;
  if (match !== undefined) onMatch?.(match);
  const known = match != null;
  if (manual) return { bucket: "manual", known };
  return { bucket: known ? "fillable" : "unknown", known };
}

/**
 * `matches`, when given, is filled with the match computed for each ref (null: none) so the
 * caller can hand the same map to fillAll (FillOptions.matches) and the page is matched once.
 */
export function coverageOf<R extends { info: FieldInfo; manual: string }>(
  refs: R[],
  bank: Bank | null,
  host: string,
  scan: { capped: boolean; unread: number } = { capped: false, unread: 0 },
  matches?: Map<R, Match | null>,
  /** Trimmed posting length. Under 40 characters, a free-text question is `needs-posting` rather than `draft`. */
  postingChars = 0,
): Coverage {
  const c: Coverage = {
    found: refs.length, known: 0, fillable: 0, unknown: 0, manual: 0, skipped: 0,
    capped: scan.capped, unread: scan.unread, unknownLabels: [], unknownListed: 0, questions: [], manualLabels: [],
    manualQuestions: [], held: [],
  };
  for (let i = 0; i < refs.length; i++) {
    const r = refs[i]!;
    const { bucket, known } = bucketOf(r.info, r.manual, bank, host, matches && (m => matches.set(r, m)));
    if (known) c.known++;
    c[bucket]++;
    const label = r.info.label.trim() || r.info.name || "(unlabelled)";
    if (bucket === "unknown" && c.unknownLabels.length < MAX_LISTED) {
      c.unknownLabels.push(label);
      const o = draftOffer(r.info, r.manual, postingChars);
      c.questions.push({ label, offer: o.offer, note: o.note, ordinal: i });
    }
    if (bucket === "manual" && c.manualLabels.length < MAX_LISTED) {
      c.manualLabels.push(label);
      const o = draftOffer(r.info, r.manual, postingChars);
      c.manualQuestions.push({ label, offer: o.offer, note: o.note, ordinal: i });
    }
    if (bucket === "skipped" && c.held.length < MAX_LISTED) {
      const sens = classify(r.info);
      if (sens === "secret" || sens === "self_id") c.held.push({ label, offer: "other", note: UNDRAFTED_NOTE, ordinal: i });
    }
  }
  c.unknownListed = c.unknownLabels.length;
  return c;
}

/**
 * Rows for the popup. A coverage that lists labels but no `questions` (a frame
 * that answered before this field existed) still shows a Draft action; the
 * page refuses it when the field cannot take one.
 */
export function questionRows(c: { questions?: readonly PageQuestion[] | null; unknownLabels?: readonly string[] | null }): PageQuestion[] {
  const qs = Array.isArray(c.questions) ? c.questions.filter(q => q && typeof q.label === "string" && typeof q.offer === "string" && typeof q.note === "string") : [];
  if (qs.length > 0) return qs;
  const labels = Array.isArray(c.unknownLabels) ? c.unknownLabels : [];
  return labels.filter(label => typeof label === "string").map(label => ({ label, offer: "draft", note: DRAFT_EMPTY_NOTE }));
}

/** What one frame answered when the popup asked it to scan. */
export interface FrameAnswer {
  frameId: number;
  result?: { host: string; disabled: boolean; fields: number; coverage?: Coverage } | null;
}

export interface FrameCoverage { frameId: number; host: string; disabled: boolean; coverage: Coverage }

export interface TabCoverage {
  /** Every frame's report added together. */
  total: Coverage;
  /** The frames that found at least one field, top frame first. */
  frames: FrameCoverage[];
  /** Frames the browser ran the scan in. */
  asked: number;
  /** Of those, frames with a content script that replied (the rest hold none: no access, or nothing to read). */
  answered: number;
  /** Every frame that replied with fields is switched off on its host. */
  disabled: boolean;
}

function sum(a: Coverage, b: Coverage): Coverage {
  return {
    found: a.found + b.found, known: a.known + b.known, fillable: a.fillable + b.fillable, unknown: a.unknown + b.unknown,
    manual: a.manual + b.manual, skipped: a.skipped + b.skipped, capped: a.capped || b.capped, unread: a.unread + b.unread,
    unknownLabels: [...a.unknownLabels, ...b.unknownLabels].slice(0, MAX_LISTED),
    unknownListed: 0,
    questions: rows(a.questions, b.questions),
    manualLabels: [...a.manualLabels, ...b.manualLabels].slice(0, MAX_LISTED),
    manualQuestions: rows(a.manualQuestions, b.manualQuestions),
    held: rows(a.held, b.held),
  };
}

function rows(a: readonly PageQuestion[] | undefined, b: readonly PageQuestion[] | undefined): PageQuestion[] {
  return [...(Array.isArray(a) ? a : []), ...(Array.isArray(b) ? b : [])].slice(0, MAX_LISTED);
}

/** Stamp the frame id onto every row that frame reported, so a later action asks only that frame. */
function withFrame(c: Coverage, frameId: number): Coverage {
  const stamp = (list: readonly PageQuestion[] | undefined): PageQuestion[] =>
    (Array.isArray(list) ? list : []).map(q => ({ ...q, frameId }));
  return { ...c, questions: stamp(c.questions), manualQuestions: stamp(c.manualQuestions), held: stamp(c.held) };
}

/**
 * One report for a tab from the per-frame answers of a scan run in every frame.
 * A frame that did not answer contributes nothing and is counted as not
 * answering, so a page whose form lives in a frame is not reported as empty.
 */
export function tabCoverage(answers: FrameAnswer[]): TabCoverage {
  const empty = coverageOf([], null, "");
  let total = empty;
  const frames: FrameCoverage[] = [];
  let answered = 0;
  for (const a of [...answers].sort((x, y) => x.frameId - y.frameId)) {
    const r = a.result;
    if (!r || typeof r !== "object") continue;
    answered++;
    const coverage = withFrame(r.coverage ?? { ...empty, found: r.fields }, a.frameId);
    if (coverage.found === 0) continue;
    frames.push({ frameId: a.frameId, host: r.host, disabled: r.disabled, coverage });
    total = sum(total, coverage);
  }
  total.unknownListed = total.unknownLabels.length;
  return { total, frames, asked: answers.length, answered, disabled: frames.length > 0 && frames.every(f => f.disabled) };
}

/** One line for the panel and popup. */
export function coverageLine(c: Coverage): string {
  const parts = [`${c.found} fields`, `${c.fillable} ready`, `${c.unknown} new`];
  if (c.manual) parts.push(`${c.manual} manual`);
  if (c.skipped) parts.push(`${c.skipped} skipped`);
  const cap = c.capped ? ` Scan capped: ${c.unread} more controls were not read.` : "";
  return parts.join(", ") + "." + cap;
}
