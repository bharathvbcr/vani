// Messages between the content script, the popup and the background worker.
// The background worker is the only writer to storage.
import { isRecord } from "./guards";
import type { Answer, Bank, FieldInfo, Settings } from "./types";
import type { CaptureResult } from "./bank";
import type { FillOutcome } from "./fill";
import type { Coverage } from "./coverage";
import type { DraftReply, JobInfo } from "./draft";

import type { ScrapeResult, ScrapedJob } from "./scraper";
import type { RelevanceResult } from "./relevance";
import type { PortalInfo } from "./portal";
import type { FormAuditReport } from "./formaudit";
import { type IngestJob, type IngestPage, ingestJobsProblem } from "./ingest";

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
  | { type: "jobStart"; kind: "suggest" | "draft" | "resume"; request: object }
  | { type: "jobPoll"; job: string }
  // Offering to mark a job Applied (applied.ts). The page sends its URL without the query or fragment;
  // the background asks the Mac app which tracked job it is, keeps the offer for the sender's tab, and marks only on appliedMark.
  // A content script is always acting for its own tab. The popup is not in a tab, so it names the active one in `tabId`
  // (and sends its URL: opening the popup on a tracked page offers the same question, on any host).
  | { type: "appliedSignal"; url: string; tabId?: number }
  | { type: "appliedPending"; tabId?: number }
  /** Vani's own jobs row for the page's posting (jobrow.ts), asked of the Mac app; `{ row: null }` when there is none. */
  | { type: "jobRow"; url: string }
  | { type: "appliedMark"; tabId?: number }
  | { type: "appliedDismiss"; tabId?: number }
  /** A content script started in the sender's frame (frames.ts): the frame is listed for the popup to ask. */
  | { type: "frameReady" }
  /** The registered frames of a tab; the popup names the active tab, a content script gets its own. */
  | { type: "frames"; tabId?: number }
  /** Scraped jobs for Vani (docs/EXTENSION_INGEST.md); `saveSearch` also saves the page as a daily search. Bounds: ingest.ts. */
  | { type: "ingestJobs"; page: IngestPage; saveSearch: boolean; jobs: IngestJob[] }
  /** Granular operations without transferring the full bank */
  | { type: "queryCandidates"; query: { signatures?: string[]; keys?: string[]; labels?: string[] } }
  | { type: "listAnswers"; offset?: number; limit?: number; search?: string }
  | { type: "patchAnswers"; upsert?: Answer[]; delete?: string[] }
  // The resume chosen for a job (resume.ts), keyed by the page's pageKey. A content script asks for the bytes
  // to fill its own tab; the popup shows, binds and unbinds the one for the active tab's page.
  | { type: "resumeGet"; url: string }
  | { type: "resumeInfo"; url: string }
  /** pdf is base64, at most MAX_RESUME_BYTES decoded; name ends in .pdf. */
  | { type: "resumePut"; url: string; name: string; pdf: string }
  | { type: "resumeDelete"; url: string }
  | { type: "attachResume"; name: string; pdf: string; pickedAt: string; tabId?: number };

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
  | { type: "insertTailored"; label: string; text: string }
  /** Attach a resume PDF to the resume file input across frames */
  | { type: "attachResume"; name: string; pdf: string; pickedAt: string };

// Messages arrive as `unknown` (a buggy or stale script can send anything): each
// receiver checks the shape here before acting, and answers anything else with
// `{ error }`, never with nothing.

export const UNKNOWN_MESSAGE = "unknown message type";

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
  jobStart: m => m.kind === "suggest" || m.kind === "draft" || m.kind === "resume",
  jobPoll: m => typeof m.job === "string",
  appliedSignal: m => optTab(m),
  appliedPending: optTab,
  jobRow: m => typeof m.url === "string" && m.url.length <= 2048,
  appliedMark: optTab,
  appliedDismiss: optTab,
  frameReady: () => true,
  frames: optTab,
  ingestJobs: m => ingestJobsProblem(m) === null,
  queryCandidates: m => isRecord(m.query),
  listAnswers: m => (m.offset === undefined || typeof m.offset === "number") && (m.limit === undefined || typeof m.limit === "number") && (m.search === undefined || typeof m.search === "string"),
  patchAnswers: m => (m.upsert === undefined || Array.isArray(m.upsert)) && (m.delete === undefined || Array.isArray(m.delete)),
  resumeGet: m => typeof m.url === "string",
  resumeInfo: m => typeof m.url === "string",
  resumePut: m => typeof m.url === "string" && typeof m.name === "string" && typeof m.pdf === "string",
  resumeDelete: m => typeof m.url === "string",
  attachResume: m => typeof m.name === "string" && typeof m.pdf === "string" && typeof m.pickedAt === "string" && optTab(m),
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
  attachResume: m => typeof m.name === "string" && typeof m.pdf === "string" && typeof m.pickedAt === "string",
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
/** `rev` is the Mac app's revision of `bank`; absent when the bank is kept in the browser (which has none). */
export type BankReply = { bank: Bank; mode: "mac-app" | "browser"; note: string | null; rev?: number } | { error: string };

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
