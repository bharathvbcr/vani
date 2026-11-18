// Messages between the content script, the popup and the background worker.
// The background worker is the only writer to storage.
import { isRecord } from "./guards";
import type { Bank, FieldInfo, Settings } from "./types";
import { type CaptureResult, MAX_BANK_BYTES, utf8Within } from "./bank";
import type { FillOutcome } from "./fill";
import type { Coverage } from "./coverage";
import { MAX_ATTEMPT, type DraftReply, type JobInfo } from "./draft";

import type { ScrapeResult, ScrapedJob } from "./scraper";
import type { RelevanceResult } from "./relevance";
import type { PortalInfo } from "./portal";
import type { RepeatApply } from "./repeatapply";
import type { FormAuditReport } from "./formaudit";
import { type IngestJob, type IngestPage, ingestJobsProblem } from "./ingest";

/** A contacts message by operation; the sender table is keyed by type.op for it. */
type ContactsMessage =
  /** Jarvis contact catalog. put/get/import carry values to the host only; the background does not log them. */
  | { type: "contacts"; op: "put"; key: string; text: string }
  | { type: "contacts"; op: "get"; key: string }
  | { type: "contacts"; op: "list" }
  | { type: "contacts"; op: "choose"; label: string; kind: string }
  | { type: "contacts"; op: "import"; items: { key: string; text: string }[] };

/**
 * What a content script sends. A page message names no tab, and no URL except appliedSignal, jobRow and jobKey
 * (PAGE_URL_TYPES), whose `url` is the page as it is now: the browser's sender URL is fixed when the document loads and
 * misses history.pushState on single-page boards. pageSentURL accepts it only on the sender's own origin. The background
 * reads everything else from the browser's sender (SenderContext), so a page, or a script that has been taken over,
 * cannot point a message at another site or another tab. checkSender refuses any other message that carries either field,
 * and the content scripts' `send` accepts only this type, so the compiler proves they do not add one.
 */
export type PageToBackground =
  | { type: "getBank"; fresh?: boolean } // fresh: skip the background's 2 s memo (Fill: the Mac app may have changed behind it)
  | { type: "capture"; host: string; fields: FieldInfo[]; postingKey?: string }
  | { type: "filled"; keys: string[] }
  /** Only the question label and the posting as shown on the page: never field values or bank content. */
  | { type: "draft"; question: string; job: JobInfo; maxWords?: number; attempt?: number }
  | Extract<ContactsMessage, { op: "get" | "choose" }>
  /** A job in the Vani Apply agent: a request built by buildSuggestRequest or buildDraftRequest (labels and the posting only). */
  | { type: "jobStart"; kind: "suggest" | "draft" | "resume" | "resumeJob"; request: object }
  | { type: "jobPoll"; job: string }
  /** Whether two wordings ask the same question, answered by the Mac app's on-device model. Two question labels, never a field value or a saved answer. */
  | { type: "sameQuestion"; a: string; b: string }
  // Offering to mark a job Applied (applied.ts). The background looks the page up under the URL the page sends only if it
  // is on the sender's own origin (pageSentURL: a single-page board pushes new URLs the browser's sender.url never shows), else the sender URL; keeps the offer for the sender's tab, and marks only on appliedMark.
  | { type: "appliedSignal"; url?: string }
  | { type: "appliedPending" }
  | { type: "appliedMark" }
  | { type: "appliedDismiss" }
  /** Vani's own jobs row for the sender's page (jobrow.ts), asked of the Mac app; `{ row: null }` when there is none. */
  | { type: "jobRow"; url?: string }
  /** The posting key Vani tracks the sender's page under (jobFind), to build a resume for that job; `{ key: null }` when none. */
  | { type: "jobKey"; url?: string }
  /** A content script started in the sender's frame (frames.ts): the frame is listed for the popup to ask. */
  | { type: "frameReady" }
  /** The registered frames of the sender's own tab. */
  | { type: "frames" }
  /** Scraped jobs for Vani (docs/EXTENSION_INGEST.md); `saveSearch` also saves the page as a daily search. Bounds: ingest.ts. `page.url` must be on the sender's origin. */
  | { type: "ingestJobs"; page: IngestPage; saveSearch: boolean; jobs: IngestJob[] }
  // The resume and cover letter chosen for the sender's page (resume.ts keys them by the browser's sender URL).
  // resumePreview asks the Mac app to open that file in Preview for the sender's own frame.
  | { type: "coverGet" }
  | { type: "resumeGet" }
  | { type: "resumePut"; name: string; pdf: string }
  | { type: "resumePreview" }
  | { type: "resumeShow"; name: string; pdf: string }
  | { type: "attachResume"; name: string; pdf: string; pickedAt: string }
  /**
   * The in-page panel is about to fill its own frame. One claim per tab; a second claim is refused.
   * Cleared by fillSettled from that frame, or when the tab is removed. A frame reply timing out does not clear it.
   */
  | { type: "fillClaim" }
  /** The content fill() returned or threw. The sender's frame only. */
  | { type: "fillSettled" };

/**
 * What the popup, or an extension page opened in a tab, sends. Only these senders may name a page (a URL) or a
 * tab (`tabId`): the popup is in no tab, so it says which one it means, and the background validates the name.
 */
export type ExtensionToBackground =
  | { type: "getBank"; fresh?: boolean }
  | { type: "capture"; host: string; fields: FieldInfo[]; postingKey?: string }
  | { type: "setSettings"; settings: Partial<Settings> }
  | { type: "deleteAnswer"; key: string }
  | { type: "editAnswer"; key: string; value: string[]; review?: boolean }
  | { type: "replaceBank"; json: string }
  | ContactsMessage
  /** Whether the one-time copy of bank basics into Jarvis has finished. */
  | { type: "contactsCopied"; set?: boolean }
  // The popup is not in a tab, so it names the active one in `tabId` and sends its URL (opening the popup on a
  // tracked page offers the same question, on any host).
  | { type: "appliedSignal"; url: string; tabId?: number }
  | { type: "appliedPending"; tabId?: number }
  | { type: "appliedMark"; tabId?: number }
  | { type: "appliedDismiss"; tabId?: number }
  /** The registered frames of a tab; the popup names the active tab. */
  | { type: "frames"; tabId?: number }
  | { type: "ingestJobs"; page: IngestPage; saveSearch: boolean; jobs: IngestJob[] }
  // The resume row of the active tab's page. The popup shows, binds and unbinds the resume for the page it names.
  // resumeInfo and resumePreview with a tabId make the background read that tab's URL itself; the reply names the
  // file and has no PDF.
  | { type: "coverInfo"; url: string }
  | { type: "coverOpen"; url: string }
  | { type: "resumeInfo"; url: string; tabId?: number }
  | { type: "resumePut"; url: string; name: string; pdf: string }
  | { type: "resumeDelete"; url: string }
  | { type: "resumePreview"; key?: string; tabId?: number }
  /**
   * Fill the active tab (or `tabId`) from the popup. The background claims the tab's fill gate, then asks each frame.
   * Refused, with no frames asked, when that tab already has a fill in flight.
   */
  | { type: "fillRun"; overwrite?: boolean; typeahead?: boolean; tabId?: number };

export type ToBackground = PageToBackground | ExtensionToBackground;

export type DraftResponse = DraftReply;

export type ToContent =
  | { type: "scan" }
  | { type: "fill"; overwrite?: boolean; typeahead?: boolean }
  /** The owner turned Submit this page on. Fill, Draft and scan do not send it. */
  | { type: "submitPage"; armed: true }
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
  /**
   * Insert a tailored answer into one field. `ordinal` is that field's index in
   * the frame's scan. `overwrite` is the second click, after the field refused
   * because it already had text.
   */
  | { type: "insertTailored"; label: string; text: string; ordinal?: number; overwrite?: boolean }
  /**
   * Open the draft editor for one detected question. The label and its scan
   * index: the frame reads the posting itself and never receives what is typed
   * in the field.
   */
  | { type: "draftField"; label: string; ordinal?: number }
  /** Attach a resume PDF to the resume file input across frames */
  | { type: "attachResume"; name: string; pdf: string; pickedAt: string }
  /** The Mac app opened this file in Preview. `name` is the file name, never a page URL. */
  | { type: "resumeNoted"; name: string };

// Messages arrive as `unknown` (a buggy or stale script can send anything): each
// receiver checks the shape here before acting, and answers anything else with
// `{ error }`, never with nothing.

export const UNKNOWN_MESSAGE = "unknown message type";

const optBool = (x: unknown) => x === undefined || typeof x === "boolean";
/** A tab id the background will pass to chrome.tabs.get. A float or a negative is not one. */
const optTabId = (m: Record<string, unknown>) => m.tabId === undefined || (typeof m.tabId === "number" && Number.isInteger(m.tabId) && m.tabId >= 0 && m.tabId <= Number.MAX_SAFE_INTEGER);
const optOrdinal = (x: unknown) => x === undefined || (typeof x === "number" && Number.isInteger(x) && x >= 0 && x <= 10000);
/** The longest question label sameQuestion carries (the host's own bound). */
export const MAX_SAME_LABEL = 300;
const label = (x: unknown) => typeof x === "string" && x.length > 0 && x.length <= MAX_SAME_LABEL;
const optAttempt = (x: unknown) => x === undefined || (typeof x === "number" && Number.isInteger(x) && x >= 0 && x <= MAX_ATTEMPT);

/** One check per message type; the mapped type makes a new message type fail to compile until it has one. */
export const TO_BACKGROUND: { [K in ToBackground["type"]]: (m: Record<string, unknown>) => boolean } = {
  getBank: m => m.fresh === undefined || typeof m.fresh === "boolean",
  capture: m => Array.isArray(m.fields) && typeof m.host === "string" && (m.postingKey === undefined || (typeof m.postingKey === "string" && m.postingKey.length <= 2048)),
  filled: m => Array.isArray(m.keys) && m.keys.every(k => typeof k === "string"),
  setSettings: m => isRecord(m.settings),
  deleteAnswer: m => typeof m.key === "string",
  editAnswer: m => typeof m.key === "string" && Array.isArray(m.value) && (m.review === undefined || typeof m.review === "boolean"),
  replaceBank: m => typeof m.json === "string" && utf8Within(m.json, MAX_BANK_BYTES),
  draft: m => typeof m.question === "string" && optAttempt(m.attempt),
  contacts: m => (m.op === "list")
    || (m.op === "get" && typeof m.key === "string" && m.key.length > 0 && m.key.length <= 64)
    || (m.op === "put" && typeof m.key === "string" && m.key.length > 0 && m.key.length <= 64 && typeof m.text === "string" && m.text.length > 0 && m.text.length <= 10_000)
    || (m.op === "choose" && typeof m.label === "string" && m.label.length > 0 && m.label.length <= 1000 && typeof m.kind === "string" && m.kind.length <= 32)
    || (m.op === "import" && Array.isArray(m.items) && m.items.length <= 9 && m.items.every(item => isRecord(item) && typeof item.key === "string" && typeof item.text === "string" && item.text.length > 0 && item.text.length <= 10_000)),
  contactsCopied: m => m.set === undefined || typeof m.set === "boolean",
  jobStart: m => m.kind === "suggest" || m.kind === "draft" || m.kind === "resume" || m.kind === "resumeJob",
  jobPoll: m => typeof m.job === "string",
  sameQuestion: m => label(m.a) && label(m.b),
  appliedSignal: optTabId,
  appliedPending: optTabId,
  jobRow: () => true,
  jobKey: () => true,
  appliedMark: optTabId,
  appliedDismiss: optTabId,
  frameReady: () => true,
  frames: optTabId,
  ingestJobs: m => ingestJobsProblem(m) === null,
  coverGet: () => true,
  coverInfo: m => typeof m.url === "string",
  coverOpen: m => typeof m.url === "string",
  resumeGet: () => true,
  resumeInfo: m => typeof m.url === "string" && optTabId(m),
  resumePut: m => (m.url === undefined || typeof m.url === "string") && typeof m.name === "string" && typeof m.pdf === "string",
  resumeDelete: m => typeof m.url === "string",
  resumePreview: m => (m.key === undefined || (typeof m.key === "string" && m.key.length <= 2048)) && optTabId(m),
  resumeShow: m => typeof m.name === "string" && typeof m.pdf === "string",
  attachResume: m => typeof m.name === "string" && typeof m.pdf === "string" && typeof m.pickedAt === "string",
  fillClaim: () => true,
  fillSettled: () => true,
  fillRun: m => optBool(m.overwrite) && optBool(m.typeahead) && optTabId(m),
};

/**
 * The kinds of sender, told apart by the browser's own sender and never by the message:
 *   popup        an extension page in no tab (the toolbar popup);
 *   extensionTab an extension page opened in a tab of its own (the browser says its URL is this extension's);
 *   topFrame     a content script in a page's top frame;
 *   subframe     a content script in a frame inside a page;
 *   unknown      a sender in no tab whose URL is not this extension's (another extension, devtools, a web page):
 *                it holds no right, whatever it sends.
 */
export type SenderKind = "popup" | "extensionTab" | "topFrame" | "subframe" | "unknown";
export const isPageKind = (k: SenderKind): boolean => k === "topFrame" || k === "subframe";

/** What the browser says about one message's sender, built once per message (senderContext); handlers read only this. */
export interface SenderContext {
  kind: SenderKind;
  /** The tab the message acts for: a page sender's own tab, or for an extension sender the `tabId` it named (an integer; the background asks the browser what that tab is). */
  tabId?: number;
  /** The sending frame (a page sender only). */
  frameId?: number;
  /** The sender's URL as the browser reports it. */
  url?: string;
  /** The sender's origin as the browser reports it, else the URL's; null when neither is an origin. */
  origin: string | null;
  /** The tab's top-level URL, when the browser reports it (only for tabs the extension has host access to). */
  tabURL?: string;
  /** The hosts whose disabled setting governs this sender's capture: its own, plus the tab's for a subframe. null when its own cannot be read. */
  hosts: string[] | null;
}

export interface SenderLike { tab?: { id?: number; url?: string } | null; url?: string; origin?: string; frameId?: number }

export function senderContext(sender: SenderLike, ownURL: string, named?: unknown): SenderContext {
  const ok = (o: string): boolean => o !== "" && o !== "null";
  const originOf = (u: string | undefined): string | null => {
    try { const o = new URL(u ?? "").origin; return ok(o) ? o : null; } catch { return null; }
  };
  const hostOf = (u: string | undefined): string | null => {
    try { return new URL(u ?? "").hostname || null; } catch { return null; }
  };
  const inTab = sender.tab !== undefined && sender.tab !== null;
  const own = ownURL !== "" && typeof sender.url === "string" && sender.url.startsWith(ownURL);
  // A sender in no tab is the popup only when it names no URL (Safari may omit it) or one of this extension's own pages.
  const tablessKind: SenderKind = sender.url === undefined || own ? "popup" : "unknown";
  const kind: SenderKind = !inTab ? tablessKind : own ? "extensionTab" : sender.frameId === 0 ? "topFrame" : "subframe";
  const frame = hostOf(sender.url);
  const tabURL = inTab ? sender.tab?.url : undefined;
  const top = kind === "subframe" ? hostOf(tabURL) : null;
  const origin = typeof sender.origin === "string" ? (ok(sender.origin) ? sender.origin : null) : originOf(sender.url);
  const tabId = isPageKind(kind) ? sender.tab?.id : typeof named === "number" && Number.isInteger(named) ? named : undefined;
  return {
    kind,
    ...(tabId === undefined ? {} : { tabId }),
    ...(isPageKind(kind) && sender.frameId !== undefined ? { frameId: sender.frameId } : {}),
    ...(typeof sender.url === "string" ? { url: sender.url } : {}),
    origin,
    ...(typeof tabURL === "string" ? { tabURL } : {}),
    hosts: frame === null ? null : top === null ? [frame] : [frame, top],
  };
}

/** The longest page URL a page message names, or jobRow and jobKey ask the host about. */
export const MAX_JOB_URL = 2048;

/** The only page messages that may carry a `url`: the page being viewed, which the browser's sender.url misses after history.pushState. */
export const PAGE_URL_TYPES: ReadonlySet<string> = new Set(["appliedSignal", "jobRow", "jobKey"]);

/**
 * The page URL a page sender's message names: {} when it names none (or is not a PAGE_URL_TYPES message, which
 * checkSender refuses for carrying one), {url} when it is a string within MAX_JOB_URL, an http(s) URL, and on exactly
 * the origin the browser reports for the sender; otherwise {error}. A url that is sent and bad is never replaced by a guess.
 */
export function pageSentURL(msg: object, ctx: SenderContext): { url?: string } | { error: string } {
  const own = Object.getOwnPropertyDescriptor(msg, "url");
  if (own === undefined || !("type" in msg) || typeof msg.type !== "string" || !PAGE_URL_TYPES.has(msg.type)) return {};
  const v: unknown = own.value;
  if (typeof v !== "string" || v === "" || v.length > MAX_JOB_URL) return { error: "carries a url that is not a bounded string" };
  let u: URL;
  try { u = new URL(v); } catch { return { error: "carries a url that does not parse" }; }
  if (u.protocol !== "http:" && u.protocol !== "https:") return { error: "carries a url that is not http(s)" };
  if (ctx.origin === null || u.origin !== ctx.origin) return { error: "carries a url that is not on the sender's origin" };
  return { url: v };
}

/** The sender-table key: the message type, or `type.op` for a type whose operations differ in who may send them. */
export type PolicyKey = Exclude<ToBackground["type"], "contacts"> | `contacts.${ContactsMessage["op"]}`;
export const policyKey = (m: { type: ToBackground["type"]; op?: unknown }): PolicyKey =>
  (m.type === "contacts" ? `contacts.${String(m.op)}` : m.type) as PolicyKey;

/**
 * Who may send each background message, by kind. The background refuses any other sender with `bad_request`
 * before it reads or writes anything, so a confused content script cannot rewrite the bank or its settings, and
 * the popup cannot ask for what only a page gets. The mapped type makes a new message type fail to compile until
 * it has a policy.
 *
 * The extension page in a tab holds the popup's rights wherever the popup holds them (the popup is also opened that
 * way to drive it), but it is its own kind: it is never a page, and handlers never act for the tab it sits in.
 */
const POPUP: readonly SenderKind[] = ["popup", "extensionTab"];
const PAGE: readonly SenderKind[] = ["topFrame", "subframe"];
const ANY: readonly SenderKind[] = [...POPUP, ...PAGE];
export const SENDER_POLICY: { [K in PolicyKey]: readonly SenderKind[] } = {
  getBank: ANY,
  capture: ANY,
  filled: PAGE,
  setSettings: POPUP,
  deleteAnswer: POPUP,
  editAnswer: POPUP,
  replaceBank: POPUP,
  draft: PAGE,
  // A page reads one contact and asks the owner to choose one; only the popup writes, lists or imports the catalog.
  "contacts.get": ANY,
  "contacts.choose": PAGE,
  "contacts.put": POPUP,
  "contacts.list": POPUP,
  "contacts.import": POPUP,
  contactsCopied: POPUP,
  jobStart: PAGE,
  jobPoll: PAGE,
  sameQuestion: PAGE,
  appliedSignal: ANY,
  appliedPending: ANY,
  jobRow: PAGE,
  jobKey: PAGE,
  appliedMark: ANY,
  appliedDismiss: ANY,
  frameReady: PAGE,
  frames: ANY,
  ingestJobs: ANY,
  coverGet: PAGE,
  coverInfo: POPUP,
  coverOpen: POPUP,
  resumeGet: PAGE,
  resumeInfo: POPUP,
  resumePut: ANY,
  resumeDelete: POPUP,
  resumePreview: ANY,
  resumeShow: PAGE,
  attachResume: PAGE,
  fillClaim: PAGE,
  fillSettled: PAGE,
  fillRun: POPUP,
};

const KIND_WORDS: Record<SenderKind, string> = { popup: "the popup", extensionTab: "an extension page in a tab", topFrame: "a page", subframe: "a page", unknown: "an unrecognised sender" };

/**
 * Null when this sender may send this message; otherwise the refusal to reply with. A page sender's message
 * carries no `tabId` (the browser's sender says it), no `url` except on PAGE_URL_TYPES (and then one that
 * pageSentURL accepts), no `key` for resumePreview, and an ingestJobs
 * `page.url` on the sender's own origin: a page cannot save a search for, or ingest jobs under, another site.
 */
export function checkSender(msg: ToBackground, ctx: SenderContext): { error: string; code: "bad_request" } | null {
  const key = policyKey(msg);
  const refuse = (error: string) => ({ error, code: "bad_request" as const });
  if (!SENDER_POLICY[key].includes(ctx.kind)) return refuse(`${key} is not accepted from ${KIND_WORDS[ctx.kind]}`);
  if (!isPageKind(ctx.kind)) return null;
  if (Object.hasOwn(msg, "tabId")) return refuse(`${key} from a page carries no tabId`);
  if (Object.hasOwn(msg, "url")) {
    if (!PAGE_URL_TYPES.has(msg.type)) return refuse(`${key} from a page carries no url`);
    const sent = pageSentURL(msg, ctx);
    if ("error" in sent) return refuse(`${key} ${sent.error}`);
  }
  if (msg.type === "resumePreview" && Object.hasOwn(msg, "key")) return refuse("resumePreview from a page carries no key");
  if (msg.type === "ingestJobs") {
    let pageOrigin: string | null = null;
    try { pageOrigin = new URL(msg.page.url).origin; } catch { /* refused below */ }
    if (pageOrigin === null || ctx.origin === null || pageOrigin !== ctx.origin) return refuse("ingestJobs page.url is not on the sender's origin");
  }
  return null;
}

export const TO_CONTENT: { [K in ToContent["type"]]: (m: Record<string, unknown>) => boolean } = {
  scan: () => true,
  fill: m => optBool(m.overwrite) && optBool(m.typeahead),
  submitPage: m => m.armed === true,
  capture: () => true,
  clearMarks: () => true,
  appliedShow: () => true,
  scrapeJobs: () => true,
  getRelevance: () => true,
  jumpToMissed: m => typeof m.index === "number",
  insertTailored: m => typeof m.label === "string" && m.label.length > 0 && m.label.length <= 1000 && typeof m.text === "string" && optOrdinal(m.ordinal) && optBool(m.overwrite),
  draftField: m => typeof m.label === "string" && m.label.length > 0 && m.label.length <= 1000 && optOrdinal(m.ordinal),
  attachResume: m => typeof m.name === "string" && typeof m.pdf === "string" && typeof m.pickedAt === "string",
  resumeNoted: m => typeof m.name === "string" && m.name.length > 0 && m.name.length <= 200,
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
  /** Other postings at this page's company were applied to lately (repeatapply.ts): a note for the panel, never a gate. */
  repeatApply?: RepeatApply;
  /** The posting's application form has a flagged question (formnote.ts): a note for the panel, never a gate. */
  formNote?: string;
  portal?: PortalInfo;
  job?: JobInfo;
}
export interface FillReply { outcomes: FillOutcome[] }
/** `unchanged` counts answers the bank already held, which a repeat save skips. */
/** `refused`: why a save was turned away (capture off for the site, bank unreadable), shown instead of a count of 0. */
export type CaptureReply = Omit<CaptureResult, "bank"> & { unchanged?: number; refused?: string };
/** `rev` is the Mac app's revision of `bank`; absent when the bank is kept in the browser (which has none). */
export type BankReply = { bank: Bank; mode: "mac-app" | "browser"; note: string | null; rev?: number } | { error: string };

const COUNTS = ["added", "updated", "skippedSecret", "skippedSelfId", "skippedEmpty"] as const;
const isCount = (x: unknown): x is number => typeof x === "number" && Number.isFinite(x) && x >= 0;

export function isFillReply(r: unknown): r is FillReply {
  return isRecord(r) && Array.isArray(r.outcomes) && r.outcomes.every(o => isRecord(o) && typeof o.filled === "boolean");
}

export function isCaptureReply(r: unknown): r is CaptureReply {
  return isRecord(r) && COUNTS.every(k => isCount(r[k])) && (r.unchanged === undefined || isCount(r.unchanged)) && (r.skippedFull === undefined || isCount(r.skippedFull)) && (r.refused === undefined || (typeof r.refused === "string" && r.refused.length <= 500));
}

export function isScanReply(r: unknown): r is ScanReply {
  return isRecord(r) && isCount(r.fields) && isCount(r.matched) && typeof r.host === "string" && typeof r.disabled === "boolean" &&
    (r.coverage === undefined || isRecord(r.coverage));
}
