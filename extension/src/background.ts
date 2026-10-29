// Background service worker: the single writer to the answer bank, and the
// keyboard shortcut. It makes no network requests.
import { createLock } from "./guards";
import type { Answer, Bank } from "./types";
import { type CaptureResult, MAX_OPTIONS, MAX_VALUE_CHARS, mergeCapture, mergeCaptureDelta, parseBank } from "./bank";
import {
  logActivity,
  loadBank,
  loadSnapshot,
  migrationNote,
  currentNativeTransport,
  patchAnswers,
  replaceBank,
  storageMode,
  updateBank,
} from "./store";
import { type FillReply, MAX_SAME_LABEL, type ToBackground, type ToContent, UNKNOWN_MESSAGE, checkSender, checkToBackground } from "./messages";
import { type FanOut, type FrameStore, createFrameRegistry, mergeFill, toFrames } from "./frames";
import { createDrafter, draftHelp } from "./draft";
import { type OfferStore, type StoredOffer, createBroker, parseFindKey } from "./applied";
import { parseFindRow } from "./jobrow";
import { checkResumeFile, checkResumeName, resumeKey, runResume } from "./resume";

const draft = createDrafter();

function str(x: unknown): string {
  return typeof x === "string" ? x : "";
}

// Serialize every read-modify-write: two tabs capturing at once must not lose
// one another's answers. (The store also retries on a conflicting write from
// another process, e.g. the Safari extension.)
const locked = createLock();

async function update(fn: (b: Bank) => Bank): Promise<Bank> {
  return locked(() => updateBank(fn));
}

/** Job calls answer at once (the agent runs the job), so each is bounded short. */
const JOB_CALL_MS = 10_000;
/** jobsIngest runs `vani jobs ingest`, which the host bounds at 30 s (35 s over the agent's XPC); this
 *  outlasts both, so the host's own timeout answer arrives instead of a false "unreachable". */
const INGEST_CALL_MS = 40_000;

/** sameQuestion runs Apple's on-device model (3 s typical); an answer later than this is not waited for. */
export const SAME_QUESTION_MS = 8_000;

/** One bounded native call. `ms` is the transport's only timer for this call; an unreachable host is a coded reply, never a throw. */
export async function native(msg: object, ms = JOB_CALL_MS): Promise<unknown> {
  try {
    return await currentNativeTransport()(msg, ms);
  } catch {
    return { error: "the Vani Apply Mac app isn't reachable", code: "unreachable" };
  }
}

/** A resume-open reply for the popup or the page: name and size, or an error. Never the PDF. */
function withoutResumeBytes(r: unknown): unknown {
  if (typeof r !== "object" || r === null || Array.isArray(r) || !("pdf" in r)) return r;
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(r)) if (k !== "pdf") out[k] = v;
  return out;
}

function replyRecord(r: unknown): Record<string, unknown> | null {
  return typeof r === "object" && r !== null && !Array.isArray(r) ? r as Record<string, unknown> : null;
}

function isFoundResume(r: unknown): boolean {
  const o = replyRecord(r);
  return o?.found === true && typeof o.name === "string";
}

function isMissingResume(r: unknown): boolean {
  return replyRecord(r)?.found === false;
}

function isHostError(r: unknown): boolean {
  return typeof replyRecord(r)?.error === "string";
}

/** How many other frames of a tab are asked whether they have a resume. More than this is "several", unasked. */
const MAX_FRAME_PROBES = 8;
/** One probe. The whole scan is this times the cap, and a down host stops at the first error. */
const FRAME_PROBE_MS = 2_000;
const NOTE_MS = 1_000;
const BADGE_MS = 4_000;

/** The tab the popup named, read here. A missing tab is null. The message's own URL is not used. */
async function namedTab(tabId: number): Promise<{ id: number; url: string } | null> {
  try {
    const t = await chrome.tabs.get(tabId);
    return { id: tabId, url: typeof t?.url === "string" ? t.url : "" };
  } catch {
    return null;
  }
}

/** The tab the popup is looking at, while the popup is open. A miss is null. */
async function activeTab(): Promise<{ id?: number; url: string } | null> {
  try {
    const tabs = await chrome.tabs.query({ active: true, currentWindow: true });
    const t = tabs[0];
    if (!t) return null;
    return { id: t.id, url: typeof t.url === "string" ? t.url : "" };
  } catch {
    return null;
  }
}

function withResumeKey(info: unknown, key: string): unknown {
  const o = replyRecord(info);
  if (!o || o.found !== true) return info;
  return withoutResumeBytes({ ...o, key });
}

/**
 * Resumes saved for frames of this tab other than the top page. The top page wins when it has one,
 * so this runs only after the top page's own lookup said there is none. One hit is that frame.
 * Two hits, or more candidates than the probe cap, are "several": nothing is chosen.
 */
async function probeOtherFrames(tabId: number, topKey: string | null): Promise<
  | { kind: "none" }
  | { kind: "several" }
  | { kind: "one"; url: string; key: string; info: unknown }
  | { kind: "error"; reply: unknown }
> {
  let pages: string[] = [];
  try { pages = await frameRegistry.pages(tabId); } catch { return { kind: "none" }; }
  const seen = new Set<string>();
  const candidates: { url: string; key: string }[] = [];
  for (const url of pages) {
    const key = resumeKey(url);
    if (!key || key === topKey || seen.has(key)) continue;
    seen.add(key);
    candidates.push({ url, key });
    if (candidates.length > MAX_FRAME_PROBES) return { kind: "several" };
  }
  const hits: { url: string; key: string; info: unknown }[] = [];
  for (const c of candidates) {
    const info = await runResume({ type: "resumeInfo", url: c.url }, m => native(m, FRAME_PROBE_MS));
    if (isHostError(info)) return { kind: "error", reply: info };
    if (!replyRecord(info)) return { kind: "error", reply: { error: "the Vani Apply Mac app isn't reachable", code: "unreachable" } };
    if (isFoundResume(info)) {
      hits.push({ ...c, info });
      if (hits.length > 1) return { kind: "several" };
    } else if (!isMissingResume(info)) {
      return { kind: "error", reply: info };
    }
  }
  if (hits.length === 1) return { kind: "one", url: hits[0]!.url, key: hits[0]!.key, info: hits[0]!.info };
  return { kind: "none" };
}

/** Tell the page Preview opened, so the note survives the popup closing. A page with no content script gets a toolbar badge. The name is checked; the page URL is not sent. */
async function confirmOpened(tabId: number | undefined, reply: unknown): Promise<void> {
  const o = replyRecord(reply);
  if (!o || o.ok !== true || tabId === undefined) return;
  const name = checkResumeName(o.name);
  let shown = false;
  if (name) {
    try {
      const r = await Promise.race([
        chrome.tabs.sendMessage(tabId, { type: "resumeNoted", name } satisfies ToContent, { frameId: 0 }),
        new Promise<never>((_, reject) => setTimeout(() => reject(new Error("note timeout")), NOTE_MS)),
      ]);
      shown = replyRecord(r)?.shown === true;
    } catch {
      shown = false;
    }
  }
  if (!shown) await pdfBadge(tabId);
}

async function pdfBadge(tabId: number): Promise<void> {
  const action = chrome.action;
  if (!action) return;
  try {
    const current = await action.getBadgeText({ tabId });
    // A "✓?" marks a question waiting. Covering it would also clear it four seconds later.
    if (current !== "" && current !== "PDF") return;
    await action.setBadgeText({ tabId, text: "PDF" });
    await action.setBadgeBackgroundColor({ tabId, color: "#2e8b57" });
  } catch { return; }
  setTimeout(() => {
    void action.getBadgeText({ tabId }).then(text => {
      if (text === "PDF") return action.setBadgeText({ tabId, text: "" });
    }).catch(() => undefined);
  }, BADGE_MS);
}

/** The popup's resume row. `tabId` is re-read with chrome.tabs.get. A key is opened only when it is that page, or one frame of that tab. */
async function popupResumeInfo(tabId: number): Promise<unknown> {
  const tab = await namedTab(tabId);
  if (!tab) return { found: false };
  const topKey = resumeKey(tab.url);
  if (topKey) {
    const info = await runResume({ type: "resumeInfo", url: tab.url }, native);
    if (isFoundResume(info)) return withResumeKey(info, topKey);
    if (!isMissingResume(info)) return withoutResumeBytes(info);
  }
  const probe = await probeOtherFrames(tab.id, topKey);
  if (probe.kind === "error") return withoutResumeBytes(probe.reply);
  if (probe.kind === "several") return { found: false, several: true };
  if (probe.kind === "one") return withResumeKey(probe.info, probe.key);
  return { found: false };
}

/** The popup's View. No key: the active tab, then its one embed. A key that is no longer on the tab opens nothing. */
async function popupResumePreview(key: string | undefined, tabId: number | undefined): Promise<unknown> {
  const tab = tabId !== undefined ? await namedTab(tabId) : await activeTab();
  if (!tab) return { found: false };
  const topKey = resumeKey(tab.url);
  const claimed = key && key.length > 0 ? key : undefined;
  const open = async (url: string) => withoutResumeBytes(await runResume({ type: "resumeOpen", url }, native));
  const finish = async (reply: unknown) => {
    await confirmOpened(tab.id, reply);
    return reply;
  };
  if (claimed) {
    if (topKey && claimed === topKey) return finish(await open(tab.url));
    if (tab.id === undefined) return { found: false, changed: true };
    const pages = await frameRegistry.pages(tab.id);
    const match = pages.find(u => resumeKey(u) === claimed);
    if (!match) return { found: false, changed: true };
    return finish(await open(match));
  }
  if (!topKey) return { found: false };
  const opened = await open(tab.url);
  if (!isMissingResume(opened)) return finish(opened);
  if (tab.id === undefined) return opened;
  const probe = await probeOtherFrames(tab.id, topKey);
  if (probe.kind === "error") return withoutResumeBytes(probe.reply);
  if (probe.kind === "several") return { found: false, several: true };
  if (probe.kind === "one") return finish(await open(probe.url));
  return opened;
}

// Offers to mark a job Applied are kept per tab in chrome.storage.session, so a stopped
// worker still has them when the confirmation page loads (memory only where that API is
// missing). Nothing is ever marked unasked.
const OFFERS_KEY = "vani.appliedOffers";
const offerStore: OfferStore | undefined = chrome.storage.session
  ? {
    load: async () => {
      const r = (await chrome.storage.session!.get(OFFERS_KEY))[OFFERS_KEY];
      return typeof r === "object" && r !== null && !Array.isArray(r) ? r as Record<string, StoredOffer> : {};
    },
    save: offers => chrome.storage.session!.set({ [OFFERS_KEY]: offers }),
  }
  : undefined;
const applied = createBroker({ native, store: offerStore });

// The frames of each tab whose content script started, for the popup to ask one by one (frames.ts).
const FRAMES_KEY = "vani.frames";
const frameRegistry = createFrameRegistry({
  store: chrome.storage.session
    ? {
      load: async () => {
        const r = (await chrome.storage.session!.get(FRAMES_KEY))[FRAMES_KEY];
        return typeof r === "object" && r !== null && !Array.isArray(r) ? r as Awaited<ReturnType<FrameStore["load"]>> : {};
      },
      save: v => chrome.storage.session!.set({ [FRAMES_KEY]: v }),
    }
    : undefined,
});

/** Send to every frame of the active tab that runs Vani: the keyboard shortcut here, and the live check (test/live-bg.ts). */
export function toTabFrames<T>(msg: ToContent, timeoutMs?: number): Promise<FanOut<T>> {
  return toFrames<T>(msg, { registered: tabId => frameRegistry.list(tabId), timeoutMs });
}

/** This extension's own URL root ("chrome-extension://<id>/"), or "" where the runtime cannot say (a test's fake chrome). */
function ownURL(): string {
  try { return typeof chrome.runtime.getURL === "function" ? chrome.runtime.getURL("") : ""; } catch { return ""; }
}

/** The origin a content script's message comes from: the browser's own `sender.origin`, else the sender URL's; null when neither is an origin. */
function senderOrigin(sender: chrome.runtime.MessageSender): string | null {
  const ok = (o: string): boolean => o !== "" && o !== "null";
  if (typeof sender.origin === "string") return ok(sender.origin) ? sender.origin : null;
  try {
    const o = new URL(sender.url ?? "").origin;
    return ok(o) ? o : null;
  } catch {
    return null;
  }
}

/** The tab a message acts for. A content script is always its own tab, whatever it claims; the popup is no tab and names one. */
function actingTab(sender: chrome.runtime.MessageSender, named: unknown): number | undefined {
  if (sender.tab) return sender.tab.id;
  return typeof named === "number" && Number.isInteger(named) ? named : undefined;
}

/** A "✓?" on the toolbar button marks a tab that has a question waiting, for pages the content script does not run on. */
async function badge(tabId: number | undefined, show: boolean): Promise<void> {
  if (tabId === undefined || !chrome.action) return;
  try {
    await chrome.action.setBadgeText({ tabId, text: show ? "✓?" : "" });
    if (show) await chrome.action.setBadgeBackgroundColor({ tabId, color: "#2e8b57" });
  } catch { /* the tab is gone */ }
}

/** Ask the top frame to show the question for a form that is in a frame. False when it cannot (no script there). */
async function askTopToShow(tabId: number): Promise<boolean> {
  try {
    const r = await chrome.tabs.sendMessage<{ shown?: boolean } | undefined>(tabId, { type: "appliedShow" } satisfies ToContent, { frameId: 0 });
    return r?.shown === true;
  } catch {
    return false;
  }
}

// Navigation clears a tab's badge; put it back while the question waits, and forget a closed tab's.
chrome.tabs.onUpdated.addListener((tabId, info) => {
  // A top-level navigation ends every document the tab's frames were registered for; the new page registers its own.
  if (info.status === "loading") void frameRegistry.forget(tabId);
  if (info.status === "complete") void applied.pending(tabId).then(o => { if (o) return badge(tabId, true); }, () => undefined);
});
chrome.tabs.onRemoved.addListener(tabId => { void applied.dismiss(tabId); void frameRegistry.forget(tabId); });

/**
 * Count one more fill on each answer used (once per occurrence in `keys`). With the Mac app that is a
 * patch of just those rows against the revision they were read at; if the bank moved on, read it once
 * more and try once more. In the browser it is the ordinary read-modify-write.
 */
async function bumpFills(keys: string[]): Promise<void> {
  const tally = new Map<string, number>();
  for (const k of keys) tally.set(k, (tally.get(k) ?? 0) + 1);
  const inBrowser = () => update(b => {
    const answers = { ...b.answers };
    for (const [k, n] of tally) if (answers[k]) answers[k] = { ...answers[k]!, fills: answers[k]!.fills + n };
    return { ...b, answers };
  });
  if (await storageMode() !== "mac-app") { await inBrowser(); return; }
  for (let attempt = 0; attempt < 2; attempt++) {
    const { bank, rev } = await loadSnapshot({ fresh: attempt > 0 });
    if (rev === null) { await inBrowser(); return; } // the Mac app went away since the mode check
    const upsert: Answer[] = [];
    for (const [k, n] of tally) {
      const a = bank.answers[k];
      if (a) upsert.push({ ...a, fills: a.fills + n });
    }
    if (upsert.length === 0) return;
    if (!(await patchAnswers(upsert, [], rev)).conflict) return;
  }
  throw new Error("answer bank changed concurrently");
}

/**
 * The Mac app screens every row again and drops the ones it classifies as secret. Those were counted
 * as saved here; move them to skippedSecret (from added first, then updated) so the reply never
 * claims a row the bank does not hold.
 */
function refusedByApp(counts: Omit<CaptureResult, "bank">, dropped: number): Omit<CaptureResult, "bank"> {
  const n = Math.max(0, Math.min(dropped, counts.added + counts.updated));
  const fromAdded = Math.min(n, counts.added);
  return { ...counts, added: counts.added - fromAdded, updated: counts.updated - (n - fromAdded), skippedSecret: counts.skippedSecret + n };
}

async function handle(msg: ToBackground, sender: chrome.runtime.MessageSender): Promise<unknown> {
  switch (msg.type) {
    case "getBank": {
      const { bank, rev } = await loadSnapshot(msg.fresh === true ? { fresh: true } : {});
      return { bank, mode: rev === null ? "browser" : "mac-app", note: migrationNote(), ...(rev === null ? {} : { rev }) };
    }
    case "capture": {
      if (!Array.isArray(msg.fields) || msg.fields.length > 500 || typeof msg.host !== "string") return { error: "bad capture" };
      let result: Omit<CaptureResult, "bank"> | undefined;
      const mode = await storageMode();
      try {
        if (mode === "mac-app") {
          const b = await loadBank({ fresh: true }); // the delta is computed against this: never a window-old copy
          const delta = mergeCaptureDelta(b, msg.fields, msg.host, new Date());
          const { droppedSecret } = await patchAnswers(delta.upsert, delta.deleteKeys);
          result = refusedByApp(delta.counts, droppedSecret);
        } else {
          await update(b => {
            const { bank, ...counts } = mergeCapture(b, msg.fields, msg.host, new Date());
            result = counts;
            return bank;
          });
        }
      } catch (e) {
        // Say which store refused: "Mac app" and "this browser" fail for different reasons and are fixed in different places.
        const where = mode === "mac-app" ? "the Mac app did not save it" : "this browser did not save it (the Mac app is not reachable)";
        throw new Error(`${where}: ${e instanceof Error ? e.message : String(e)}`, { cause: e });
      }
      if (result) void logActivity({ event: "capture", host: msg.host, counts: { ...result } });
      return result;
    }
    case "filled": {
      const keys = msg.keys.slice(0, 500);
      try {
        await bumpFills(keys);
      } catch (e) {
        // The page does not wait on this and must not see a rejection: say what was lost (counts only) and carry on.
        console.error(`Vani: fill counts for ${keys.length} answers were not saved: ${e instanceof Error ? e.message : String(e)}`);
        void logActivity({ event: "fill", counts: { filled: keys.length, countsLost: keys.length } });
        return { ok: false, error: "fill counts were not saved" };
      }
      void logActivity({ event: "fill", counts: { filled: keys.length } });
      return { ok: true };
    }
    case "setSettings":
      await update(b => ({
        ...b,
        settings: {
          captureSelfId: typeof msg.settings.captureSelfId === "boolean" ? msg.settings.captureSelfId : b.settings.captureSelfId,
          disabledHosts: Array.isArray(msg.settings.disabledHosts) ? msg.settings.disabledHosts.slice(0, 500).map(String) : b.settings.disabledHosts,
        },
      }));
      void logActivity({ event: "settings" });
      return { ok: true };
    case "deleteAnswer":
      await patchAnswers([], [msg.key]);
      void logActivity({ event: "delete" });
      return { ok: true };
    case "editAnswer":
      await update(b => {
        const a = b.answers[msg.key];
        if (!a || !Array.isArray(msg.value)) return b;
        const value = msg.value.slice(0, MAX_OPTIONS).map(v => String(v).slice(0, MAX_VALUE_CHARS)).filter(Boolean);
        const review = typeof msg.review === "boolean" ? msg.review : a.review;
        return { ...b, answers: { ...b.answers, [msg.key]: { ...a, value, review, updatedAt: new Date().toISOString() } } };
      });
      void logActivity({ event: "edit" });
      return { ok: true };
    case "draft": {
      // Messages cross a trust boundary (a compromised page can't send these,
      // but a buggy content script can): re-check the shape here.
      const job = typeof msg.job === "object" && msg.job !== null ? msg.job : { company: "", title: "", description: "" };
      const r = await draft(str(msg.question), { company: str(job.company), title: str(job.title), description: str(job.description) },
        typeof msg.maxWords === "number" ? msg.maxWords : undefined,
        typeof msg.attempt === "number" ? msg.attempt : 0);
      // The user sees our explanation; the host's own text is kept only where it adds detail.
      if (!r.ok && r.code !== "provider" && r.code !== "bad_request") return { ...r, error: draftHelp(r) };
      return r;
    }
    case "jobStart": {
      if (msg.kind !== "suggest" && msg.kind !== "draft" && msg.kind !== "resume" && msg.kind !== "resumeJob") return { error: "unknown job kind", code: "bad_request" };
      if (typeof msg.request !== "object" || msg.request === null || Array.isArray(msg.request)) return { error: "bad job request", code: "bad_request" };
      return native({ op: "jobStart", kind: msg.kind, request: msg.request });
    }
    case "jobPoll":
      if (typeof msg.job !== "string" || msg.job.length === 0 || msg.job.length > 64) return { error: "bad job id", code: "bad_request" };
      return native({ op: "jobResult", job: msg.job });
    case "sameQuestion": {
      // Two question labels go to the on-device model and nothing else, whatever else the message carried.
      if (typeof msg.a !== "string" || typeof msg.b !== "string" || msg.a === "" || msg.b === "" || msg.a.length > MAX_SAME_LABEL || msg.b.length > MAX_SAME_LABEL) {
        return { error: "bad sameQuestion labels", code: "bad_request" };
      }
      return native({ op: "sameQuestion", a: msg.a, b: msg.b }, SAME_QUESTION_MS);
    }
    case "appliedSignal": {
      const tabId = actingTab(sender, msg.tabId);
      const frame = sender.tab ? sender.frameId ?? 0 : 0;
      let r = await applied.signal(tabId, msg.url);
      // A form in a frame has a URL of its own (a Greenhouse embed maps to its posting); the page around it may
      // be the one Vani tracks. The tab's URL is known only where the extension has access to the page.
      if ("offer" in r && r.offer === null && frame !== 0 && sender.tab?.url) r = await applied.signal(tabId, sender.tab.url);
      if ("offer" in r && r.offer && tabId !== undefined) {
        if (sender.tab) void badge(tabId, true); // the popup is open: no need to flag the button
        if (frame !== 0) r = { ...r, shownInTop: await askTopToShow(tabId) };
      }
      return r;
    }
    case "appliedPending":
      return { offer: await applied.pending(actingTab(sender, msg.tabId)) };
    case "jobRow":
      return parseFindRow(await native({ op: "jobFind", url: msg.url }));
    case "jobKey": {
      // The same jobFind as jobRow, read for the posting's key (which every tracked job has, scored or not).
      const f = parseFindKey(await native({ op: "jobFind", url: msg.url }));
      return f.ok ? (f.ambiguous ? { key: null, ambiguous: true } : { key: f.key }) : { error: f.error, code: f.code };
    }
    case "appliedMark": {
      const tabId = actingTab(sender, msg.tabId);
      const r = await applied.mark(tabId);
      if ("ok" in r) void badge(tabId, false);
      return r;
    }
    case "appliedDismiss": {
      const tabId = actingTab(sender, msg.tabId);
      await applied.dismiss(tabId);
      void badge(tabId, false);
      return { ok: true };
    }
    case "replaceBank": {
      const { bank, droppedSecret } = parseBank(msg.json); // throws on a malformed file
      await locked(() => replaceBank(bank));
      const answers = Object.keys(bank.answers).length;
      void logActivity({ event: "import", counts: { answers, droppedSecret } });
      return { ok: true, answers, droppedSecret };
    }
    case "frameReady": {
      // Only a content script is in a frame; the popup has no tab and no frameId.
      // The page URL is the browser's sender.url. A URL on the message is ignored.
      if (sender.tab?.id === undefined || sender.frameId === undefined) return { error: "not from a frame", code: "bad_request" };
      const pageURL = typeof sender.url === "string" ? sender.url : undefined;
      await frameRegistry.add(sender.tab.id, sender.frameId, senderOrigin(sender) ?? undefined, pageURL);
      return { ok: true };
    }
    case "frames": {
      const tabId = actingTab(sender, msg.tabId);
      return { frames: tabId === undefined ? [] : await frameRegistry.list(tabId) };
    }
    case "ingestJobs":
      // Only the contract's fields go to the host, whatever else the message carried.
      return native({
        op: "jobsIngest",
        page: { url: msg.page.url, title: msg.page.title },
        save_search: msg.saveSearch,
        jobs: msg.jobs.map(j => ({
          title: j.title, company: j.company, location: j.location, url: j.url, snippet: j.snippet,
          ...(j.posted === undefined ? {} : { posted: j.posted }),
        })),
      }, INGEST_CALL_MS);
    case "resumeGet":
    case "resumePut":
    case "resumeDelete": {
      // The bytes go only to the content script that asked, for its own tab (resumeGet is content-only in
      // the sender policy); the popup binds and unbinds the active tab's page and never receives the file.
      // A content script acts for its own frame: the key is that frame's URL, never the one its message claims.
      // The popup is in no tab and names the page in the message (Choose and Remove).
      const r = await runResume({ ...msg, url: sender.tab ? sender.url ?? sender.tab.url ?? "" : msg.url }, native);
      // A form in a frame has a URL of its own; the resume may be bound to the page around it (as for appliedSignal).
      const frame = sender.tab ? sender.frameId ?? 0 : 0;
      if (msg.type === "resumeGet" && frame !== 0 && sender.tab?.url && (r as { found?: unknown } | null)?.found === false && resumeKey(sender.tab.url)) {
        return runResume({ type: "resumeGet", url: sender.tab.url }, native);
      }
      return r;
    }
    case "resumeInfo": {
      // A tab id means the popup asked about that tab. The background reads the tab. The message's URL
      // is not the page that is looked up, so a stale URL cannot show another job's file. With no tab
      // id the popup still names the page, which is how the row worked before frames were recorded.
      if (typeof msg.tabId === "number") return popupResumeInfo(msg.tabId);
      return runResume({ type: "resumeInfo", url: msg.url }, native);
    }
    case "resumePreview": {
      // A content script is its own frame. A key or tab id it sends is ignored, so it cannot name
      // another page's file. The popup is not in a tab: the background reads the tab.
      if (sender.tab) {
        const frameURL = sender.url ?? sender.tab.url ?? "";
        const frame = sender.frameId ?? 0;
        const top = sender.tab.url ?? "";
        const open = async (url: string) => withoutResumeBytes(await runResume({ type: "resumeOpen", url }, native));
        if (!resumeKey(frameURL)) {
          if (frame !== 0 && top !== frameURL && resumeKey(top)) return open(top);
          return { found: false };
        }
        const r = await runResume({ type: "resumeOpen", url: frameURL }, native);
        if (frame !== 0 && top && isMissingResume(r) && resumeKey(top)) return open(top);
        return withoutResumeBytes(r);
      }
      return popupResumePreview(msg.key, msg.tabId);
    }
    case "resumeShow": {
      // The page already holds this PDF (it just built or chose it). Check it before the host sees it.
      // The host opens Preview and does not store it. The reply has no PDF, even if the host echoed one.
      const file = checkResumeFile(msg.name, msg.pdf);
      if ("error" in file) return file;
      return withoutResumeBytes(await native({ op: "resumeOpenData", name: file.name, pdf: msg.pdf }));
    }
    case "attachResume": {
      // Only a content script sends this (sender policy), and it acts for its own tab: never the active one,
      // which may be a different page by the time this runs.
      const tabId = sender.tab?.id;
      if (tabId === undefined) return { error: "no tab" };
      // The same checks the page runs, before any frame is asked.
      const file = checkResumeFile(msg.name, msg.pdf);
      if ("error" in file) return file;
      // The file's name is the owner's: it goes only to frames of the page that offered it, never to whatever
      // else the tab now shows (it may have navigated) or to a frame of another origin.
      const origin = senderOrigin(sender);
      if (origin === null) return { error: "no origin" };
      const r = await toFrames<FillReply>({
        type: "attachResume",
        name: file.name,
        pdf: msg.pdf,
        pickedAt: msg.pickedAt.slice(0, 40),
      }, { registered: (tid, o) => frameRegistry.list(tid, o), tabId, origin });
      return { outcomes: mergeFill(r) };
    }
    default:
      // Unreachable for a message that passed checkToBackground; kept so a missed case answers, never `undefined`.
      return { error: UNKNOWN_MESSAGE };
  }
}

chrome.runtime.onMessage.addListener((raw, sender, sendResponse) => {
  const c = checkToBackground(raw);
  if ("error" in c) {
    sendResponse({ error: c.error });
    return false;
  }
  // Who may send this type (messages.ts SENDER_POLICY): a refused sender gets an error and nothing runs.
  const refused = checkSender(c.msg.type, sender, ownURL());
  if (refused) {
    sendResponse(refused);
    return false;
  }
  handle(c.msg, sender).then(
    r => sendResponse(r === undefined ? { error: "no result" } : r),
    (e: unknown) => sendResponse({ error: e instanceof Error ? e.message : String(e) }),
  );
  return true; // reply asynchronously
});

// The keyboard shortcut fills every frame of the active tab, like the popup's Fill button.
chrome.commands.onCommand.addListener(command => {
  if (command === "fill-form") void toTabFrames({ type: "fill" }).catch(() => undefined);
});
