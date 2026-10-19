// The background worker's message router, loaded for real behind a fake chrome:
// every message gets an answer, and one it does not know is an error object, never undefined.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { checkToBackground, checkToContent } from "../src/messages";

type Listener = (msg: unknown, sender: object, sendResponse: (r?: unknown) => void) => boolean;

const g = globalThis as Record<string, unknown>;
const saved = { chrome: g.chrome, browser: g.browser };
let listener: Listener | undefined;
let local: Record<string, unknown> = {};
const removed: Array<(tabId: number) => void> = [];
/** What the background asked the (absent) Mac app, in order. */
const nativeSent: object[] = [];

beforeAll(async () => {
  const on = { addListener() {} };
  g.browser = undefined;
  g.chrome = {
    runtime: {
      onMessage: { addListener(cb: Listener) { listener = cb; } },
      // No Mac app: the store falls back to chrome.storage.local.
      sendNativeMessage(_app: string, m: object, cb: (r: unknown) => void) {
        nativeSent.push(m);
        (g.chrome as { runtime: { lastError?: { message: string } } }).runtime.lastError = { message: "host not found" };
        cb(undefined);
        (g.chrome as { runtime: { lastError?: unknown } }).runtime.lastError = undefined;
      },
      connectNative() { throw new Error("no host"); },
      lastError: undefined,
    },
    storage: {
      local: {
        get: async (k: string | string[]) => Object.fromEntries((Array.isArray(k) ? k : [k]).filter(x => x in local).map(x => [x, local[x]])),
        set: async (items: Record<string, unknown>) => { local = { ...local, ...items }; },
        remove: async (k: string | string[]) => { for (const x of Array.isArray(k) ? k : [k]) delete local[x]; },
      },
      session: undefined,
    },
    tabs: { onUpdated: on, onRemoved: { addListener(cb: (tabId: number) => void) { removed.push(cb); } }, query: async () => [], sendMessage: async () => undefined },
    action: undefined,
    scripting: { executeScript: async () => [] },
    commands: { onCommand: on },
  };
  await import("../src/background");
});
afterAll(() => { g.chrome = saved.chrome; g.browser = saved.browser; });

async function ask(msg: unknown, sender: object = {}): Promise<{ async: boolean; replies: unknown[] }> {
  const replies: unknown[] = [];
  let done: () => void = () => undefined;
  const answered = new Promise<void>(r => { done = r; });
  const async = listener!(msg, sender, r => { replies.push(r); done(); });
  await Promise.race([answered, new Promise(r => setTimeout(r, 2000))]);
  return { async, replies };
}

describe("background router", () => {
  test("registers one runtime.onMessage listener", () => {
    expect(typeof listener).toBe("function");
  });

  test("an unknown or non-object message is answered with an error object at once, never left unanswered", async () => {
    for (const m of [null, undefined, 3, "getBank", [], {}, { type: 1 }, { type: "dropTables" }, { type: "toString" }, { type: "__proto__" }]) {
      expect(await ask(m)).toEqual({ async: false, replies: [{ error: "unknown message type" }] });
    }
  });

  test("a known type with the wrong shape is refused before anything runs", async () => {
    expect(await ask({ type: "filled", keys: "first_name" })).toEqual({ async: false, replies: [{ error: "malformed filled message" }] });
    expect(await ask({ type: "deleteAnswer" })).toEqual({ async: false, replies: [{ error: "malformed deleteAnswer message" }] });
    expect(await ask({ type: "replaceBank", json: { answers: {} } })).toEqual({ async: false, replies: [{ error: "malformed replaceBank message" }] });
    expect(await ask({ type: "appliedMark", tabId: "7" })).toEqual({ async: false, replies: [{ error: "malformed appliedMark message" }] });
  });

  test("a valid message is still routed and answered", async () => {
    const r = await ask({ type: "getBank" });
    expect(r.async).toBe(true);
    expect(r.replies.length).toBe(1);
    expect(r.replies[0]).toMatchObject({ mode: "browser" });
    expect(await ask({ type: "filled", keys: [] })).toEqual({ async: true, replies: [{ ok: true }] });
  });
});

describe("frame registry messages", () => {
  test("a content script's frame registers; the popup lists a tab's frames; a content script only its own tab's", async () => {
    expect(await ask({ type: "frameReady" }, { tab: { id: 7 }, frameId: 5 })).toEqual({ async: true, replies: [{ ok: true }] });
    expect(await ask({ type: "frameReady" }, { tab: { id: 7 }, frameId: 0 })).toEqual({ async: true, replies: [{ ok: true }] });
    // The popup is in no tab and no frame: it cannot register one.
    expect(await ask({ type: "frameReady" }, {})).toEqual({ async: true, replies: [{ error: "not from a frame", code: "bad_request" }] });
    expect(await ask({ type: "frames", tabId: 7 }, {})).toEqual({ async: true, replies: [{ frames: [5, 0] }] });
    // A content script in tab 8 naming tab 7 gets its own tab's (none), never tab 7's.
    expect(await ask({ type: "frames", tabId: 7 }, { tab: { id: 8 }, frameId: 0 })).toEqual({ async: true, replies: [{ frames: [] }] });
    expect(await ask({ type: "frames", tabId: "7" }, {})).toEqual({ async: false, replies: [{ error: "malformed frames message" }] });
  });

  test("a closed tab's frames are forgotten", async () => {
    await ask({ type: "frameReady" }, { tab: { id: 11 }, frameId: 3 });
    expect(await ask({ type: "frames", tabId: 11 }, {})).toEqual({ async: true, replies: [{ frames: [3] }] });
    for (const cb of removed) cb(11);
    await new Promise(r => setTimeout(r, 10));
    expect(await ask({ type: "frames", tabId: 11 }, {})).toEqual({ async: true, replies: [{ frames: [] }] });
  });
});

describe("resume messages", () => {
  const own = "https://boards.greenhouse.io/acme/jobs/1";
  const claimed = "https://boards.greenhouse.io/other/jobs/2";
  const last = () => nativeSent.at(-1) as { op?: string; key?: string } | undefined;

  test("a content script gets the resume keyed by its own frame's URL, not the one its message claims", async () => {
    const r = await ask({ type: "resumeGet", url: claimed }, { tab: { id: 3, url: own }, frameId: 0, url: own });
    expect(last()).toEqual({ op: "resumeGet", key: own });
    // No Mac app here: an unreachable app is a coded reply, which Fill shows as a reason.
    expect(r.replies).toEqual([{ error: "the Vani Apply Mac app isn't reachable", code: "unreachable" }]);
  });

  test("a content script's resumePut binds to its own page too", async () => {
    await ask({ type: "resumePut", url: claimed, name: "Resume_Test.pdf", pdf: btoa("%PDF-1.4\n%%EOF\n") }, { tab: { id: 3, url: own }, frameId: 0, url: own });
    expect(last()).toMatchObject({ op: "resumePut", key: own, name: "Resume_Test.pdf" });
  });

  test("the popup (no tab) names the page; it can never fetch the bytes", async () => {
    await ask({ type: "resumeInfo", url: claimed }, {});
    expect(last()).toEqual({ op: "resumeInfo", key: claimed });
    const before = nativeSent.length;
    const r = await ask({ type: "resumeGet", url: claimed }, {});
    expect(r.replies).toEqual([{ error: "only a page being filled gets the resume", code: "bad_request" }]);
    expect(nativeSent.length).toBe(before);
  });
});

describe("message checks", () => {
  test("every content message type is accepted with its own fields, and bad options are refused", () => {
    for (const m of [{ type: "scan" }, { type: "fill" }, { type: "fill", overwrite: true, typeahead: false }, { type: "capture" }, { type: "appliedShow" }]) {
      expect(checkToContent(m)).toEqual({ msg: m as never });
    }
    expect(checkToContent({ type: "fill", overwrite: "yes" })).toEqual({ error: "malformed fill message" });
  });

  test("background messages: the inherited Object keys are not message types", () => {
    for (const t of ["constructor", "hasOwnProperty", "valueOf"]) expect(checkToBackground({ type: t })).toEqual({ error: "unknown message type" });
    expect(checkToBackground({ type: "jobStart", kind: "rm", request: {} })).toEqual({ error: "malformed jobStart message" });
  });
});
