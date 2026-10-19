// Messages between the content script, the popup and the background worker.
// The background worker is the only writer to storage.
import type { Bank, FieldInfo, Settings } from "./types";
import type { CaptureResult } from "./bank";
import type { FillOutcome } from "./fill";
import type { Coverage } from "./coverage";
import type { DraftReply, JobInfo } from "./draft";

import type { ScrapeResult, ScrapedJob } from "./scraper";
import type { RelevanceResult } from "./relevance";
import type { PortalInfo } from "./portal";
import type { FormAuditReport } from "./formaudit";

export type ToBackground =
  | { type: "getBank" }
  | { type: "capture"; host: string; fields: FieldInfo[] }
  | { type: "filled"; keys: string[] }
  | { type: "setSettings"; settings: Partial<Settings> }
  | { type: "deleteAnswer"; key: string }
  | { type: "editAnswer"; key: string; value: string[]; review?: boolean }
  | { type: "replaceBank"; json: string }
  /** Only the question label and the posting as shown on the page: never field values or bank content. */
  | { type: "draft"; question: string; job: JobInfo; maxWords?: number }
  /** A job in the Vani Apply agent: a request built by buildSuggestRequest or buildDraftRequest (labels and the posting only). */
  | { type: "jobStart"; kind: "suggest" | "draft"; request: object }
  | { type: "jobPoll"; job: string }
  // Offering to mark a job Applied (applied.ts). The page sends its URL without the query or fragment;
  // the background asks the Mac app which tracked job it is, keeps the offer for the sender's tab, and marks only on appliedMark.
  // A content script is always acting for its own tab. The popup is not in a tab, so it names the active one in `tabId`
  // (and sends its URL: opening the popup on a tracked page offers the same question, on any host).
  | { type: "appliedSignal"; url: string; tabId?: number }
  | { type: "appliedPending"; tabId?: number }
  | { type: "appliedMark"; tabId?: number }
  | { type: "appliedDismiss"; tabId?: number }
  /** A content script started in the sender's frame (frames.ts): the frame is listed for the popup to ask. */
  | { type: "frameReady" }
  /** The registered frames of a tab; the popup names the active tab, a content script gets its own. */
  | { type: "frames"; tabId?: number }
  /** Track a scraped or discovered job posting */
  | { type: "trackJob"; job: { title: string; company: string; url: string } };

export type DraftResponse = DraftReply;

export type ToContent =
  | { type: "scan" }
  | { type: "fill"; overwrite?: boolean; typeahead?: boolean }
  | { type: "capture" }
  | { type: "clearMarks" }
  /** To the top frame only: a form in a frame found a job to ask about, so show the question here. */
  | { type: "appliedShow" }
  /** Live scrape jobs on the current page */
  | { type: "scrapeJobs" }
  /** Real-time relevance check and portal details */
  | { type: "getRelevance" }
  /** Scroll and highlight a missed filling */
  | { type: "jumpToMissed"; index: number }
  /** Insert a tailored answer into a specific field by label */
  | { type: "insertTailored"; label: string; text: string };

// Messages arrive as `unknown` (a buggy or stale script can send anything): each
// receiver checks the shape here before acting, and answers anything else with
// `{ error }`, never with nothing.

export const UNKNOWN_MESSAGE = "unknown message type";

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === "object" && x !== null && !Array.isArray(x);
}

const optBool = (x: unknown) => x === undefined || typeof x === "boolean";
const optTab = (m: Record<string, unknown>) => m.tabId === undefined || typeof m.tabId === "number";

/** One check per message type; the mapped type makes a new message type fail to compile until it has one. */
const TO_BACKGROUND: { [K in ToBackground["type"]]: (m: Record<string, unknown>) => boolean } = {
  getBank: () => true,
  capture: m => Array.isArray(m.fields) && typeof m.host === "string",
  filled: m => Array.isArray(m.keys) && m.keys.every(k => typeof k === "string"),
  setSettings: m => isRecord(m.settings),
  deleteAnswer: m => typeof m.key === "string",
  editAnswer: m => typeof m.key === "string" && Array.isArray(m.value) && (m.review === undefined || typeof m.review === "boolean"),
  replaceBank: m => typeof m.json === "string",
  draft: m => typeof m.question === "string",
  jobStart: m => m.kind === "suggest" || m.kind === "draft",
  jobPoll: m => typeof m.job === "string",
  appliedSignal: m => optTab(m),
  appliedPending: optTab,
  appliedMark: optTab,
  appliedDismiss: optTab,
  frameReady: () => true,
  frames: optTab,
  trackJob: m => isRecord(m.job) && typeof (m.job as Record<string, unknown>).title === "string",
};

const TO_CONTENT: { [K in ToContent["type"]]: (m: Record<string, unknown>) => boolean } = {
  scan: () => true,
  fill: m => optBool(m.overwrite) && optBool(m.typeahead),
  capture: () => true,
  clearMarks: () => true,
  appliedShow: () => true,
  scrapeJobs: () => true,
  getRelevance: () => true,
  jumpToMissed: m => typeof m.index === "number",
  insertTailored: m => typeof m.label === "string" && typeof m.text === "string",
};

function check<M>(table: Record<string, (m: Record<string, unknown>) => boolean>, m: unknown): { msg: M } | { error: string } {
  if (!isRecord(m) || typeof m.type !== "string" || !Object.hasOwn(table, m.type)) return { error: UNKNOWN_MESSAGE };
  return table[m.type]!(m) ? { msg: m as M } : { error: `malformed ${m.type} message` };
}

export function checkToBackground(m: unknown): { msg: ToBackground } | { error: string } {
  return check<ToBackground>(TO_BACKGROUND, m);
}

export function checkToContent(m: unknown): { msg: ToContent } | { error: string } {
  return check<ToContent>(TO_CONTENT, m);
}

/** `fields`/`matched` are kept for older callers; `coverage` is the full breakdown. */
export interface ScanReply {
  fields: number;
  matched: number;
  host: string;
  disabled: boolean;
  coverage?: Coverage;
  audit?: FormAuditReport;
  relevance?: RelevanceResult;
  portal?: PortalInfo;
  job?: JobInfo;
}
export interface FillReply { outcomes: FillOutcome[] }
/** `unchanged` counts answers the bank already held, which a repeat save skips. */
export type CaptureReply = Omit<CaptureResult, "bank"> & { unchanged?: number };
export type BankReply = { bank: Bank; mode: "mac-app" | "browser"; note: string | null } | { error: string };

const COUNTS = ["added", "updated", "skippedSecret", "skippedSelfId", "skippedEmpty"] as const;
const isCount = (x: unknown): x is number => typeof x === "number" && Number.isFinite(x) && x >= 0;

export function isFillReply(r: unknown): r is FillReply {
  return isRecord(r) && Array.isArray(r.outcomes) && r.outcomes.every(o => isRecord(o) && typeof o.filled === "boolean");
}

export function isCaptureReply(r: unknown): r is CaptureReply {
  return isRecord(r) && COUNTS.every(k => isCount(r[k])) && (r.unchanged === undefined || isCount(r.unchanged));
}

export function isScanReply(r: unknown): r is ScanReply {
  return isRecord(r) && isCount(r.fields) && isCount(r.matched) && typeof r.host === "string" && typeof r.disabled === "boolean" &&
    (r.coverage === undefined || isRecord(r.coverage));
}
