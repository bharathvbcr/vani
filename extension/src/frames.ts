// Talking to every frame of a tab. ATS forms often sit in a cross-origin iframe
// (a Greenhouse embed on a company page), where a copy of the content script runs
// beside the top frame's. A message sent to the whole tab resolves with whichever
// frame replies first, so an empty top frame used to answer "filled 0" while the
// frame holding the form was dropped. Instead each frame that holds a listener
// is asked on its own (by frameId), every one is bounded by a timeout, and the
// answers are added up. A frame that fails or does not answer is counted and
// reported, never taken as "nothing to fill".
//
// Finding the frames. scripting.executeScript lists only frames the extension
// may script: the activeTab page, not a cross-origin iframe whose host is only
// in content_scripts.matches (seen in Chrome 155, test/live.ts). So each frame's
// content script also registers itself with the background when it starts
// (`frameReady`; the sender's frameId needs no host permission), and a tab's
// frames are the registered ones plus the probed ones. A registered frame whose
// listener has gone (the frame navigated away) is dropped as gone, not counted.
//
// The content side answers every message it accepts, empty results included,
// so no sender is left with an open port.
import {
  type CaptureReply, type FillReply, type ScanReply, type ToContent,
  checkToContent, isCaptureReply, isFillReply, isScanReply,
} from "./messages";
import type { FillOutcome } from "./fill";
import { checkResumeFile, checkResumeName } from "./resume";

/** Reply when a fill is already writing. Not an empty outcome list: that would look like a successful fill of nothing. */
export const FILL_BUSY = "a fill is already running";

let fillClaimed = false;
let fillRecording = false;

/**
 * Per-realm fill lock. Taken synchronously, before any await on the fill path.
 * The frame reply budget does not release it; releaseFillClaim does, when the fill returns or throws.
 */
export function tryClaimFill(): boolean {
  if (fillClaimed) return false;
  fillClaimed = true;
  return true;
}

export function releaseFillClaim(): void {
  fillClaimed = false;
  fillRecording = false;
}

/** A claimed fill has already called beginFill and is still recording writes. */
export function fillAlreadyRecording(): boolean {
  return fillClaimed && fillRecording;
}

export function noteFillRecording(): void {
  if (fillClaimed) fillRecording = true;
}

/** True while this realm's lock is held. A panel click uses it to refuse a second fill before any await. */
export function fillClaimHeld(): boolean {
  return fillClaimed;
}

/**
 * True only through the synchronous start of the fill the lock holder just called.
 * Cleared before that fill's first await resumes, so a later click is not treated as the holder.
 */
let fillCaller = false;

export function fillInvokedByHolder(): boolean {
  return fillCaller;
}

function callHeldFill<T>(fn: () => T): T {
  fillCaller = true;
  try {
    return fn();
  } finally {
    fillCaller = false;
  }
}

declare global {
  interface Window {
    /** Set by the content script in the frame that owns a page (a same-origin child frame leaves it to its parent). */
    __vaniApply?: boolean;
  }
}

/** Fill polls up to 600 ms per widget, so a whole form can take seconds; past this a frame is reported as not answering. */
export const FRAME_REPLY_MS = 20_000;

export interface FanOut<T> {
  /** Frames with a live listener that were asked (answered + failed). */
  asked: number;
  answered: Array<{ frameId: number; reply: T }>;
  /** Asked but no usable answer: an error reply, a malformed one, or no answer in time. */
  failed: Array<{ frameId: number; error: string }>;
  /** Listed but with no listener any more (the frame navigated or was removed): not Vani frames, not counted. */
  gone: number[];
}

/** Chrome's error for a message to a frame with no onMessage listener. */
const NO_RECEIVER = /Receiving end does not exist/;

function errorText(x: unknown): string | null {
  return typeof x === "object" && x !== null && "error" in x && typeof x.error === "string" ? x.error : null;
}

/** Reject after `ms`, clearing the timer either way. */
async function bounded<T>(p: Promise<T>, ms: number, what: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([p, new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error(`${what}: no answer in ${Math.round(ms / 1000)} s`)), ms); })]);
  } finally {
    clearTimeout(timer);
  }
}

/** Ask each frame on its own and wait for all of them, each for at most `timeoutMs`. Never throws. */
export async function fanOut<T>(frameIds: number[], send: (frameId: number) => Promise<unknown>, accept: (r: unknown) => r is T, timeoutMs: number): Promise<FanOut<T>> {
  const out: FanOut<T> = { asked: 0, answered: [], failed: [], gone: [] };
  type Settled = { ok: true; frameId: number; reply: T } | { ok: false; frameId: number; error: string };
  const settled = await Promise.all([...new Set(frameIds)].map(async (frameId): Promise<Settled> => {
    try {
      const r = await bounded(send(frameId), timeoutMs, `frame ${frameId}`);
      if (accept(r)) return { ok: true, frameId, reply: r };
      return { ok: false, frameId, error: errorText(r) ?? "malformed reply" };
    } catch (e) {
      return { ok: false, frameId, error: e instanceof Error ? e.message : String(e) };
    }
  }));
  for (const s of settled.sort((a, b) => a.frameId - b.frameId)) {
    if (s.ok) out.answered.push({ frameId: s.frameId, reply: s.reply });
    else if (NO_RECEIVER.test(s.error)) out.gone.push(s.frameId);
    else out.failed.push({ frameId: s.frameId, error: s.error });
  }
  out.asked = out.answered.length + out.failed.length;
  return out;
}

/** The frames of a tab that hold a content script listener as far as the extension may script them. */
async function probeFrames(tabId: number, timeoutMs: number): Promise<number[]> {
  const rs = await bounded(chrome.scripting.executeScript<boolean>({ target: { tabId, allFrames: true }, func: () => window.__vaniApply === true }), timeoutMs, "listing the page's frames");
  return rs.filter(r => r.result === true).map(r => r.frameId);
}

export const ACCEPT: { [K in ToContent["type"]]: (r: unknown) => boolean } = {
  scan: isScanReply,
  fill: isFillReply,
  capture: isCaptureReply,
  clearMarks: () => true,
  appliedShow: r => typeof r === "object" && r !== null && "shown" in r,
  scrapeJobs: r => typeof r === "object" && r !== null && "jobs" in r,
  getRelevance: r => typeof r === "object" && r !== null,
  jumpToMissed: () => true,
  insertTailored: () => true,
  draftField: r => typeof r === "object" && r !== null && !Array.isArray(r) &&
    ((r as { ok?: unknown }).ok === true || typeof (r as { error?: unknown }).error === "string"),
  attachResume: isFillReply,
  resumeNoted: r => typeof r === "object" && r !== null && "shown" in r,
};

export interface ToFramesOptions {
  /** The frames of this tab whose content script registered (FrameRegistry.list, through the background from the popup). */
  registered(tabId: number, origin?: string): Promise<number[]>;
  timeoutMs?: number;
  /**
   * Pin the ask to the page that made the request: only registered frames whose recorded origin is this one are asked.
   * Nothing is probed or injected (a frame the registry has no origin for cannot be vouched for), and no such frame
   * is an error, never a fallback to the other frames of the tab.
   */
  origin?: string;
  /** The tab to ask. Absent, the active tab (the popup's and the shortcut's own); a content script's request names its own tab, never the active one. */
  tabId?: number;
  /**
   * Called synchronously with the frame ids about to be asked, before any frame is sent a message.
   * A fill gate records those frames here. A reply timeout is not this callback and does not clear the gate.
   */
  beforeAsk?(frameIds: readonly number[]): void;
  /**
   * Ask only this frame. A field action must not run in every frame that
   * happens to show the same question: that would start two paid drafts.
   */
  frameId?: number;
}

/** The one frame a field action may message. Throws when the id is not a frame id. */
export function pinnedFrame(frameId: number): number {
  if (!Number.isInteger(frameId) || frameId < 0) throw new Error("bad frame");
  return frameId;
}

/**
 * Send a message to every frame of a tab that runs Vani (the active tab unless `tabId` names one) and
 * collect each frame's answer. Where no frame has a live listener, the content script is injected into
 * every frame the extension may script (activeTab grants the popup's page, for this click) and the frames
 * are asked again.
 */
export async function toFrames<T>(msg: ToContent, opts: ToFramesOptions): Promise<FanOut<T>> {
  const timeoutMs = opts.timeoutMs ?? FRAME_REPLY_MS;
  let tabId = opts.tabId;
  if (tabId === undefined) {
    const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (tab?.id === undefined) throw new Error("no active tab");
    tabId = tab.id;
  }
  const target = tabId;
  const accept = ACCEPT[msg.type] as (r: unknown) => r is T;
  const askIds = (ids: readonly number[]): number[] => {
    const uniq = [...new Set(ids)];
    opts.beforeAsk?.(uniq);
    return uniq;
  };
  if (opts.frameId !== undefined) {
    const only = pinnedFrame(opts.frameId);
    return fanOut(askIds([only]), frameId => chrome.tabs.sendMessage(target, msg, { frameId }), accept, timeoutMs);
  }
  if (opts.origin !== undefined) {
    const ids = await bounded(opts.registered(target, opts.origin), timeoutMs, "the background's frame list");
    const pinned = await fanOut(askIds(ids), frameId => chrome.tabs.sendMessage(target, msg, { frameId }), accept, timeoutMs);
    if (pinned.asked === 0) throw new Error("the page that offered this is no longer open in the tab");
    return pinned;
  }
  const ask = async (): Promise<FanOut<T>> => {
    const [probed, registered] = await Promise.all([
      probeFrames(target, timeoutMs).catch((): number[] => []),
      bounded(opts.registered(target), timeoutMs, "the background's frame list").catch((): number[] => []),
    ]);
    return fanOut(askIds([...probed, ...registered]), frameId => chrome.tabs.sendMessage(target, msg, { frameId }), accept, timeoutMs);
  };
  const first = await ask();
  if (first.asked > 0) return first;
  await bounded(chrome.scripting.executeScript({ target: { tabId: target, allFrames: true }, files: ["content.js"] }), timeoutMs, "starting Vani on the page");
  const second = await ask();
  if (second.asked === 0) throw new Error("Vani could not start on this page");
  return second;
}

// --- the background's per-tab frame list ---------------------------------------------

export const MAX_FRAMES_PER_TAB = 32;
export const MAX_TABS = 64;

/** `origins` maps a frame id to the origin its content script reported; a frame with no entry has no known origin.
 *  `urls` maps a frame id to the page URL the browser put on that frame's sender, never a URL a message claimed. */
interface TabFrames { frames: number[]; at: number; origins?: Record<string, string>; urls?: Record<string, string> }

/** A frame page the browser reported. Anything else (a script URL, an over-long string) is not kept. */
function keptPage(raw: string | undefined): string | undefined {
  if (typeof raw !== "string" || raw.length === 0 || raw.length > 2048) return undefined;
  if (raw.startsWith("https://") || raw.startsWith("http://")) return raw;
  return undefined;
}

/** Where the list is kept: chrome.storage.session survives the worker being stopped; without it, memory only. */
export interface FrameStore {
  load(): Promise<Record<string, TabFrames>>;
  save(v: Record<string, TabFrames>): Promise<void>;
}

export interface FrameRegistry {
  /** A content script started in this frame (at `origin`, when known). `pageURL` is the browser's frame URL. The newest MAX_FRAMES_PER_TAB frames of a tab are kept. */
  add(tabId: number, frameId: number, origin?: string, pageURL?: string): Promise<void>;
  /** The tab's frames; with `origin`, only those registered at exactly that origin. */
  list(tabId: number, origin?: string): Promise<number[]>;
  /** Page URLs recorded for this tab's frames, in frame order. Empty when none were recorded. */
  pages(tabId: number): Promise<string[]>;
  forget(tabId: number): Promise<void>;
}

const isFrameId = (x: unknown): x is number => typeof x === "number" && Number.isInteger(x) && x >= 0;

export function createFrameRegistry(d: { store?: FrameStore; now?: () => number } = {}): FrameRegistry {
  const now = d.now ?? Date.now;
  let memory: Record<string, TabFrames> = {};
  const store: FrameStore = d.store ?? { load: async () => memory, save: async v => { memory = v; } };
  let cache: Record<string, TabFrames> = {};
  let chain: Promise<unknown> = Promise.resolve();
  // Every read-modify-write runs alone: frames of one page register at once and must not lose one another.
  const locked = <T>(fn: () => Promise<T>): Promise<T> => {
    const next = chain.then(fn, fn);
    chain = next.catch(() => undefined);
    return next;
  };

  async function read(): Promise<Record<string, TabFrames>> {
    let m: unknown;
    try { m = await store.load(); } catch { m = cache; } // storage failing must not lose what this worker holds
    const out: Record<string, TabFrames> = {};
    if (typeof m !== "object" || m === null) return out;
    for (const [k, v] of Object.entries(m)) {
      const e = v as Partial<TabFrames> | null;
      if (e && Array.isArray(e.frames) && typeof e.at === "number") {
        const frames = e.frames.filter(isFrameId);
        const origins: Record<string, string> = {};
        if (typeof e.origins === "object" && e.origins !== null) {
          for (const [f, o] of Object.entries(e.origins)) if (typeof o === "string" && frames.includes(Number(f))) origins[f] = o;
        }
        const urls: Record<string, string> = {};
        if (typeof e.urls === "object" && e.urls !== null) {
          for (const [f, u] of Object.entries(e.urls)) {
            const page = typeof u === "string" ? keptPage(u) : undefined;
            if (page && frames.includes(Number(f))) urls[f] = page;
          }
        }
        out[k] = { frames, at: e.at, origins, urls };
      }
    }
    return out;
  }

  async function write(m: Record<string, TabFrames>): Promise<void> {
    cache = Object.fromEntries(Object.entries(m).sort((a, b) => b[1].at - a[1].at).slice(0, MAX_TABS));
    try { await store.save(cache); } catch { /* the in-memory copy still serves this worker */ }
  }

  const slot = (tabId: number): string | null => (Number.isInteger(tabId) && tabId >= 0 ? String(tabId) : null);

  return {
    add: (tabId, frameId, origin, pageURL) => locked(async () => {
      const k = slot(tabId);
      if (k === null || !isFrameId(frameId)) return;
      const m = await read();
      // No reset when frame 0 registers: the popup injecting into a loaded top frame is not a new
      // document, and its iframes are still there. Frames that did go are dropped when asked (gone).
      const old = (m[k]?.frames ?? []).filter(f => f !== frameId);
      const frames = [...old, frameId].slice(-MAX_FRAMES_PER_TAB);
      // A frame id is reused by the next document in that frame: its origin and page are replaced, or dropped when not reported.
      const origins: Record<string, string> = {};
      const urls: Record<string, string> = {};
      for (const f of frames) {
        const o = f === frameId ? origin : m[k]?.origins?.[String(f)];
        if (typeof o === "string" && o !== "") origins[String(f)] = o;
        const u = f === frameId ? keptPage(pageURL) : m[k]?.urls?.[String(f)];
        if (typeof u === "string") urls[String(f)] = u;
      }
      m[k] = { frames, at: now(), origins, urls };
      await write(m);
    }),
    list: (tabId, origin) => locked(async () => {
      const k = slot(tabId);
      if (k === null) return [];
      const e = (await read())[k];
      if (!e) return [];
      return origin === undefined ? [...e.frames] : e.frames.filter(f => e.origins?.[String(f)] === origin);
    }),
    pages: tabId => locked(async () => {
      const k = slot(tabId);
      if (k === null) return [];
      const e = (await read())[k];
      if (!e) return [];
      const out: string[] = [];
      for (const f of e.frames) {
        const u = e.urls?.[String(f)];
        if (typeof u === "string") out.push(u);
      }
      return out;
    }),
    forget: tabId => locked(async () => {
      const k = slot(tabId);
      if (k === null) return;
      const m = await read();
      if (!(k in m)) return;
      delete m[k];
      await write(m);
    }),
  };
}

export function mergeFill(r: FanOut<FillReply>): FillOutcome[] {
  return r.answered.flatMap(a => a.reply.outcomes);
}

export function sumCaptures(r: FanOut<CaptureReply>): Required<CaptureReply> {
  const s = { added: 0, updated: 0, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 0, unchanged: 0 };
  for (const { reply: c } of r.answered) {
    s.added += c.added; s.updated += c.updated; s.skippedSecret += c.skippedSecret;
    s.skippedSelfId += c.skippedSelfId; s.skippedEmpty += c.skippedEmpty; s.unchanged += c.unchanged ?? 0;
  }
  return s;
}

/** A sentence for the status line when any frame gave no usable answer; empty when all did. */
export function notAnswered(r: FanOut<unknown>): string {
  if (r.failed.length === 0) return "";
  return ` ${r.failed.length} of ${r.asked} frames did not answer (${r.failed[0]!.error}${r.failed.length > 1 ? ", …" : ""}); their fields are not counted.`;
}

export interface ContentHandlers {
  scan(): Promise<ScanReply>;
  fill(overwrite?: boolean, typeahead?: boolean): Promise<FillReply>;
  capture(): Promise<CaptureReply>;
  clearMarks?(): void | Promise<void>;
  /** Show a pending Applied question; only the top frame has one (null in a sub-frame). */
  appliedShow: (() => Promise<void>) | null;
  scrapeJobs?(): Promise<unknown> | unknown;
  getRelevance?(): Promise<unknown> | unknown;
  jumpToMissed?(index: number): void | Promise<void>;
  insertTailored?(label: string, text: string, ordinal?: number, overwrite?: boolean): { ok: boolean; error?: string; reason?: string } | Promise<{ ok: boolean; error?: string; reason?: string }>;
  /** Start a draft for one question. Returns immediately; the editor stays on the page. */
  draftField?(label: string, ordinal?: number): { ok: boolean; error?: string } | Promise<{ ok: boolean; error?: string }>;
  attachResume?(name: string, pdf: string, pickedAt: string): Promise<unknown> | unknown;
  /** The Mac app opened this file in Preview. The name is already checked. */
  resumeNoted?(name: string): void;
  /** The fill that held the lock has returned or thrown. Not called when a second fill is rejected. */
  fillSettled?(): void;
}

type Listener = (msg: unknown, sender: chrome.runtime.MessageSender, sendResponse: (response?: unknown) => void) => boolean;

/**
 * The content script's runtime.onMessage listener. It returns true (an answer
 * will follow) only when it will call sendResponse exactly once, with the
 * result, however empty, or with `{ error }`.
 */
export function contentListener(h: ContentHandlers): Listener {
  return (raw, _sender, sendResponse) => {
    const c = checkToContent(raw);
    if ("error" in c) {
      sendResponse({ error: c.error });
      return false;
    }
    const msg = c.msg;
    if (msg.type === "appliedShow") {
      const show = h.appliedShow;
      // Only the top frame is asked; answering "shown" is what lets the frame stay quiet.
      if (!show) { sendResponse({ shown: false }); return false; }
      void Promise.resolve().then(show).then(() => sendResponse({ shown: true }), () => sendResponse({ shown: false }));
      return true;
    }
    if (msg.type === "clearMarks") {
      const clear = h.clearMarks;
      void Promise.resolve().then(() => clear?.()).then(() => sendResponse({ ok: true }), (e: unknown) => sendResponse({ error: e instanceof Error ? e.message : String(e) }));
      return true;
    }
    if (msg.type === "scrapeJobs") {
      void Promise.resolve().then(() => h.scrapeJobs?.()).then(sendResponse, (e: unknown) => sendResponse({ error: e instanceof Error ? e.message : String(e) }));
      return true;
    }
    if (msg.type === "getRelevance") {
      void Promise.resolve().then(() => h.getRelevance?.()).then(sendResponse, (e: unknown) => sendResponse({ error: e instanceof Error ? e.message : String(e) }));
      return true;
    }
    if (msg.type === "jumpToMissed") {
      void Promise.resolve().then(() => h.jumpToMissed?.(msg.index)).then(() => sendResponse({ ok: true }), (e: unknown) => sendResponse({ error: e instanceof Error ? e.message : String(e) }));
      return true;
    }
    if (msg.type === "insertTailored") {
      void Promise.resolve()
        .then(() => h.insertTailored ? h.insertTailored(msg.label, msg.text, msg.ordinal, msg.overwrite) : { ok: false, error: "This frame cannot insert." })
        .then(result => sendResponse(result && typeof result === "object" ? result : { ok: false, error: "This frame cannot insert." }),
          (e: unknown) => sendResponse({ ok: false, error: e instanceof Error ? e.message : String(e) }));
      return true;
    }
    if (msg.type === "draftField") {
      void Promise.resolve()
        .then(() => h.draftField ? h.draftField(msg.label, msg.ordinal) : { ok: false, error: "This frame cannot draft." })
        .then(sendResponse, (e: unknown) => sendResponse({ ok: false, error: e instanceof Error ? e.message : String(e) }));
      return true;
    }
    if (msg.type === "attachResume") {
      // The file is checked again here: whoever sent this, a page never gets a name or bytes the checks refuse.
      const file = checkResumeFile(msg.name, msg.pdf);
      if ("error" in file) { sendResponse({ error: file.error }); return false; }
      void Promise.resolve().then(() => h.attachResume?.(msg.name, msg.pdf, msg.pickedAt)).then(sendResponse, (e: unknown) => sendResponse({ error: e instanceof Error ? e.message : String(e) }));
      return true;
    }
    if (msg.type === "resumeNoted") {
      // A disguised name is not shown. A frame with no note handler does not claim it showed one.
      const name = checkResumeName(msg.name);
      if (!name || !h.resumeNoted) { sendResponse({ shown: false }); return false; }
      h.resumeNoted(name);
      sendResponse({ shown: true });
      return false;
    }
    if (msg.type === "fill") {
      // Synchronous: a second message in this realm is rejected before h.fill, including before its first await.
      // Released when that fill returns or throws, not when the sender's frame budget expires.
      if (!tryClaimFill()) {
        sendResponse({ error: FILL_BUSY });
        return false;
      }
      const finish = (send: () => void): void => {
        releaseFillClaim();
        try { h.fillSettled?.(); } catch { /* the fill result is still delivered */ }
        send();
      };
      void Promise.resolve()
        .then(() => callHeldFill(() => h.fill(msg.overwrite, msg.typeahead)))
        .then(
          res => finish(() => sendResponse(res)),
          (e: unknown) => finish(() => sendResponse({ error: e instanceof Error ? e.message : String(e) })),
        );
      return true;
    }
    const run = (): Promise<unknown> => msg.type === "scan" ? h.scan() : h.capture();
    void Promise.resolve().then(run).then(sendResponse, (e: unknown) => sendResponse({ error: e instanceof Error ? e.message : String(e) }));
    return true;
  };
}
