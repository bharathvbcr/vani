// Background service worker: the single writer to the answer bank, and the
// keyboard shortcut. It makes no network requests.
import type { Bank } from "./types";
import { type CaptureResult, MAX_OPTIONS, MAX_VALUE_CHARS, mergeCapture, parseBank } from "./bank";
import { logActivity, loadBank, migrationNote, nativeTransport, replaceBank, storageMode, updateBank } from "./store";
import { type ToBackground, type ToContent, UNKNOWN_MESSAGE, checkToBackground } from "./messages";
import { type FanOut, type FrameStore, createFrameRegistry, toFrames } from "./frames";
import { DRAFT_HELP, createDrafter } from "./draft";
import { type OfferStore, type StoredOffer, createBroker } from "./applied";
import { resumeOp } from "./resume";

const draft = createDrafter();

function str(x: unknown): string {
  return typeof x === "string" ? x : "";
}

// Serialize every read-modify-write: two tabs capturing at once must not lose
// one another's answers. (The store also retries on a conflicting write from
// another process, e.g. the Safari extension.)
let chain: Promise<unknown> = Promise.resolve();
function locked<T>(fn: () => Promise<T>): Promise<T> {
  const next = chain.then(fn, fn);
  chain = next.catch(() => undefined);
  return next;
}

async function update(fn: (b: Bank) => Bank): Promise<Bank> {
  return locked(() => updateBank(fn));
}

/** Job calls answer at once (the agent runs the job), so each is bounded short. */
const JOB_CALL_MS = 10_000;

/** One bounded native call; an unreachable host is a coded reply, never a throw. */
async function native(msg: object): Promise<unknown> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      nativeTransport(msg),
      new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error("timed out")), JOB_CALL_MS); }),
    ]);
  } catch {
    return { error: "the Vani Apply Mac app isn't reachable", code: "unreachable" };
  } finally {
    clearTimeout(timer);
  }
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
  if (info.status === "complete") void applied.pending(tabId).then(o => { if (o) return badge(tabId, true); }, () => undefined);
});
chrome.tabs.onRemoved.addListener(tabId => { void applied.dismiss(tabId); void frameRegistry.forget(tabId); });

async function handle(msg: ToBackground, sender: chrome.runtime.MessageSender): Promise<unknown> {
  switch (msg.type) {
    case "getBank": {
      const bank = await loadBank();
      return { bank, mode: await storageMode(), note: migrationNote() };
    }
    case "capture": {
      if (!Array.isArray(msg.fields) || msg.fields.length > 500 || typeof msg.host !== "string") return { error: "bad capture" };
      let result: Omit<CaptureResult, "bank"> | undefined;
      await update(b => {
        const { bank, ...counts } = mergeCapture(b, msg.fields, msg.host, new Date());
        result = counts;
        return bank;
      });
      if (result) void logActivity({ event: "capture", host: msg.host, counts: { ...result } });
      return result;
    }
    case "filled": {
      const keys = msg.keys.slice(0, 500);
      await update(b => {
        const answers = { ...b.answers };
        for (const k of keys) if (answers[k]) answers[k] = { ...answers[k]!, fills: answers[k]!.fills + 1 };
        return { ...b, answers };
      });
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
      await update(b => {
        const answers = { ...b.answers };
        delete answers[msg.key];
        return { ...b, answers };
      });
      void logActivity({ event: "delete" });
      return { ok: true };
    case "editAnswer":
      await update(b => {
        const a = b.answers[msg.key];
        if (!a || !Array.isArray(msg.value)) return b;
        const value = msg.value.slice(0, MAX_OPTIONS).map(v => String(v).slice(0, MAX_VALUE_CHARS)).filter(Boolean);
        return { ...b, answers: { ...b.answers, [msg.key]: { ...a, value, updatedAt: new Date().toISOString() } } };
      });
      void logActivity({ event: "edit" });
      return { ok: true };
    case "draft": {
      // Messages cross a trust boundary (a compromised page can't send these,
      // but a buggy content script can): re-check the shape here.
      const job = typeof msg.job === "object" && msg.job !== null ? msg.job : { company: "", title: "", description: "" };
      const r = await draft(str(msg.question), { company: str(job.company), title: str(job.title), description: str(job.description) },
        typeof msg.maxWords === "number" ? msg.maxWords : undefined);
      // The user sees our explanation; the host's own text is kept only where it adds detail.
      if (!r.ok && r.code !== "provider" && r.code !== "bad_request") return { ...r, error: DRAFT_HELP[r.code] };
      return r;
    }
    case "jobStart": {
      if (msg.kind !== "suggest" && msg.kind !== "draft" && msg.kind !== "resume") return { error: "unknown job kind", code: "bad_request" };
      if (typeof msg.request !== "object" || msg.request === null || Array.isArray(msg.request)) return { error: "bad job request", code: "bad_request" };
      return native({ op: "jobStart", kind: msg.kind, request: msg.request });
    }
    case "jobPoll":
      if (typeof msg.job !== "string" || msg.job.length === 0 || msg.job.length > 64) return { error: "bad job id", code: "bad_request" };
      return native({ op: "jobResult", job: msg.job });
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
      if (sender.tab?.id === undefined || sender.frameId === undefined) return { error: "not from a frame", code: "bad_request" };
      await frameRegistry.add(sender.tab.id, sender.frameId);
      return { ok: true };
    }
    case "frames": {
      const tabId = actingTab(sender, msg.tabId);
      return { frames: tabId === undefined ? [] : await frameRegistry.list(tabId) };
    }
    case "resumeGet":
    case "resumeInfo":
    case "resumePut":
    case "resumeDelete": {
      // The bytes go only to the content script that asked, for its own tab; the popup gets the name, never the file.
      if (msg.type === "resumeGet" && !sender.tab) return { error: "only a page being filled gets the resume", code: "bad_request" };
      // A content script acts for its own frame: the key is that frame's URL, never the one its message claims.
      // The popup is in no tab and names the active tab's page.
      const op = resumeOp({ ...msg, url: sender.tab ? sender.url ?? sender.tab.url ?? "" : msg.url });
      if ("error" in op) return op;
      const r = await native(op);
      // A form in a frame has a URL of its own; the resume may be bound to the page around it (as for appliedSignal).
      const frame = sender.tab ? sender.frameId ?? 0 : 0;
      if (msg.type === "resumeGet" && frame !== 0 && sender.tab?.url && (r as { found?: unknown } | null)?.found === false) {
        const top = resumeOp({ type: "resumeGet", url: sender.tab.url });
        if (!("error" in top)) return native(top);
      }
      return r;
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
