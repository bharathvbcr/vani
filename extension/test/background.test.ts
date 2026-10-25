// The background worker's message router, loaded for real behind a fake chrome:
// every message gets an answer, and one it does not know is an error object, never undefined.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { MAX_BANK_BYTES } from "../src/bank";
import { SENDER_POLICY, type ToBackground, TO_BACKGROUND, checkToBackground, checkToContent } from "../src/messages";
import { MAX_RESUME_BYTES, toBase64 } from "../src/resume";

type Listener = (msg: unknown, sender: object, sendResponse: (r?: unknown) => void) => boolean;

const g = globalThis as Record<string, unknown>;
const saved = { chrome: g.chrome, browser: g.browser };
let listener: Listener | undefined;
let local: Record<string, unknown> = {};
/** When set, chrome.storage.local.set rejects with this message (a browser whose extension storage cannot be written). */
let localSetFails: string | null = null;
const removed: Array<(tabId: number) => void> = [];
const updated: Array<(tabId: number, info: { status?: string }) => void> = [];
/** What the background asked the (absent) Mac app, in order. */
const nativeSent: object[] = [];
/** When set, sendNativeMessage answers with this instead of "host not found". */
let nativeReply: ((m: object) => unknown) | undefined;
/** The tab(s) chrome.tabs.query reports as active, and every message the background sent to a tab's frame. */
let activeTabs: Array<{ id?: number; url?: string }> = [];
/** Tabs chrome.tabs.get can see. The popup names an id; the background reads the URL from here. */
let tabsById: Array<{ id: number; url?: string }> = [];
const tabSent: Array<{ tabId: number; msg: unknown; frameId: number | undefined }> = [];
/** What a frame answers; a test replaces it to make a frame throw or a tab disappear. */
let tabReply: (tabId: number, frameId: number) => unknown = () => undefined;

beforeAll(async () => {
  const on = { addListener() {} };
  g.browser = undefined;
  g.chrome = {
    runtime: {
      onMessage: { addListener(cb: Listener) { listener = cb; } },
      // No Mac app: the store falls back to chrome.storage.local.
      sendNativeMessage(_app: string, m: object, cb: (r: unknown) => void) {
        nativeSent.push(m);
        const runtime = (g.chrome as { runtime: { lastError?: { message: string } | undefined } }).runtime;
        if (nativeReply) {
          runtime.lastError = undefined;
          cb(nativeReply(m));
          return;
        }
        runtime.lastError = { message: "host not found" };
        cb(undefined);
        runtime.lastError = undefined;
      },
      connectNative() { throw new Error("no host"); },
      lastError: undefined,
    },
    storage: {
      local: {
        get: async (k: string | string[]) => Object.fromEntries((Array.isArray(k) ? k : [k]).filter(x => x in local).map(x => [x, local[x]])),
        set: async (items: Record<string, unknown>) => {
          if (localSetFails) throw new Error(localSetFails);
          local = { ...local, ...items };
        },
        remove: async (k: string | string[]) => { for (const x of Array.isArray(k) ? k : [k]) delete local[x]; },
      },
      session: undefined,
    },
    tabs: {
      onUpdated: { addListener(cb: (tabId: number, info: { status?: string }) => void) { updated.push(cb); } },
      onRemoved: { addListener(cb: (tabId: number) => void) { removed.push(cb); } },
      query: async () => activeTabs,
      get: async (id: number) => {
        const t = tabsById.find(x => x.id === id);
        if (!t) throw new Error("No tab with id");
        return t;
      },
      sendMessage: async (tabId: number, msg: unknown, opts?: { frameId?: number }) => {
        tabSent.push({ tabId, msg, frameId: opts?.frameId });
        return tabReply(tabId, opts?.frameId ?? 0);
      },
    },
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

/** A content script's sender: always in a tab. The popup's is `{}`. */
const content = { tab: { id: 3 }, frameId: 0, url: "https://a.example/" };

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
    expect(await ask({ type: "filled", keys: [] }, content)).toEqual({ async: true, replies: [{ ok: true }] });
  });
});

/** A basic as the popup sends it (test value only). */
const cityBasic = { kind: "text", label: "City", name: "", autocomplete: "address-level2", automationId: "", options: [], required: false, value: ["Testville"] };

describe("capture without the Mac app", () => {
  test("a basics save lands in browser storage and is counted", async () => {
    local = {};
    const r = await ask({ type: "capture", host: "vani-basics", fields: [cityBasic] });
    expect(r.replies[0]).toMatchObject({ added: 1, updated: 0, skippedSecret: 0 });
  });

  test("storage that refuses the write is an error that says where and why, not a silent count", async () => {
    local = {};
    localSetFails = "QuotaExceededError: storage is read-only";
    try {
      const r = await ask({ type: "capture", host: "vani-basics", fields: [cityBasic] });
      const reply = r.replies[0] as { error?: string; added?: number };
      expect(reply.added).toBeUndefined();
      expect(reply.error).toContain("this browser did not save it (the Mac app is not reachable)");
      expect(reply.error).toContain("storage is read-only");
    } finally {
      localSetFails = null;
    }
  });
});

describe("frame registry messages", () => {
  test("a content script's frame registers; the popup lists a tab's frames; a content script only its own tab's", async () => {
    expect(await ask({ type: "frameReady" }, { tab: { id: 7 }, frameId: 5, url: "https://a.example/" })).toEqual({ async: true, replies: [{ ok: true }] });
    expect(await ask({ type: "frameReady" }, { tab: { id: 7 }, frameId: 0, url: "https://a.example/" })).toEqual({ async: true, replies: [{ ok: true }] });
    // The popup is in no tab and no frame: it cannot register one.
    expect(await ask({ type: "frameReady" }, {})).toEqual({ async: false, replies: [{ error: "frameReady is not accepted from the popup", code: "bad_request" }] });
    // A tab with no frame id is still not a frame.
    expect(await ask({ type: "frameReady" }, { tab: { id: 7 } })).toEqual({ async: true, replies: [{ error: "not from a frame", code: "bad_request" }] });
    expect(await ask({ type: "frames", tabId: 7 }, {})).toEqual({ async: true, replies: [{ frames: [5, 0] }] });
    // A content script in tab 8 naming tab 7 gets its own tab's (none), never tab 7's.
    expect(await ask({ type: "frames", tabId: 7 }, { tab: { id: 8 }, frameId: 0, url: "https://a.example/" })).toEqual({ async: true, replies: [{ frames: [] }] });
    expect(await ask({ type: "frames", tabId: "7" }, {})).toEqual({ async: false, replies: [{ error: "malformed frames message" }] });
  });

  test("a closed tab's frames are forgotten", async () => {
    await ask({ type: "frameReady" }, { tab: { id: 11 }, frameId: 3, url: "https://a.example/" });
    expect(await ask({ type: "frames", tabId: 11 }, {})).toEqual({ async: true, replies: [{ frames: [3] }] });
    for (const cb of removed) cb(11);
    await new Promise(r => setTimeout(r, 10));
    expect(await ask({ type: "frames", tabId: 11 }, {})).toEqual({ async: true, replies: [{ frames: [] }] });
  });
});

/** A synthetic PDF of exactly `n` bytes (the %PDF- signature, then filler); never a real resume. */
function pdfOf(n: number, signature = "%PDF-"): string {
  const b = new Uint8Array(n).fill(0x20);
  b.set(new TextEncoder().encode(signature).subarray(0, n));
  return toBase64(b);
}

describe("attachResume routing", () => {
  const sendAttach = (over: Record<string, unknown>, sender: object) =>
    ask({ type: "attachResume", name: "Resume_Synthetic.pdf", pdf: pdfOf(64), pickedAt: "2026-10-19", ...over }, sender);
  const reset = () => { tabSent.length = 0; tabReply = () => undefined; activeTabs = []; };
  const sentToTabs = () => tabSent.filter(s => (s.msg as { type?: string }).type === "attachResume");

  test("a content script in tab 7 reaches only tab 7's frames, though tab 9 is the active one, and the reply carries their outcomes", async () => {
    reset();
    activeTabs = [{ id: 9 }];
    for (const cb of removed) cb(7); // frames registered for tab 7 by an earlier test are forgotten
    await new Promise(r => setTimeout(r, 10));
    await ask({ type: "frameReady" }, { tab: { id: 7 }, frameId: 2, url: "https://a.example/" });
    await ask({ type: "frameReady" }, { tab: { id: 7 }, frameId: 6, url: "https://a.example/" });
    await ask({ type: "frameReady" }, { tab: { id: 9 }, frameId: 0, url: "https://a.example/" });
    tabReply = (tabId, frameId) => ({ outcomes: [{ label: `tab ${tabId} frame ${frameId}`, filled: true }] });
    const r = await sendAttach({}, { tab: { id: 7 }, frameId: 2, url: "https://a.example/" });
    expect(sentToTabs().map(s => [s.tabId, s.frameId])).toEqual([[7, 2], [7, 6]]);
    expect(r.replies).toEqual([{ outcomes: [{ label: "tab 7 frame 2", filled: true }, { label: "tab 7 frame 6", filled: true }] }]);
  });

  test("a pdf over the cap, a bad name, or a non-PDF is refused with an error before any frame is asked; exactly the cap is sent", async () => {
    reset();
    activeTabs = [{ id: 9 }];
    await ask({ type: "frameReady" }, { tab: { id: 8 }, frameId: 0, url: "https://a.example/" });
    tabReply = () => ({ outcomes: [] });
    const content = { tab: { id: 8 }, frameId: 0, url: "https://a.example/" };
    const refused: Array<[Record<string, unknown>, string]> = [
      [{ pdf: pdfOf(MAX_RESUME_BYTES + 1) }, "too_large"],
      [{ name: "" }, "bad_request"],
      [{ name: "   " }, "bad_request"],
      [{ name: "../../etc/cv.pdf" }, "bad_request"],
      [{ name: "a\\b.pdf" }, "bad_request"],
      [{ name: "a/b.pdf" }, "bad_request"],
      [{ name: "resume.txt" }, "bad_request"],
      [{ name: `${"x".repeat(300)}.pdf` }, "bad_request"],
      [{ pdf: pdfOf(64, "<html") }, "bad_request"],
      [{ pdf: "not base64!" }, "bad_request"],
      [{ pdf: "" }, "bad_request"],
    ];
    for (const [over, code] of refused) {
      const r = await sendAttach(over, content);
      expect(r.replies[0]).toMatchObject({ code });
      expect((r.replies[0] as { error?: string }).error).toBeTruthy();
    }
    expect(sentToTabs()).toEqual([]);
    const ok = await sendAttach({ pdf: pdfOf(MAX_RESUME_BYTES) }, content);
    expect(ok.replies).toEqual([{ outcomes: [] }]);
    expect(sentToTabs().length).toBe(1);
  });

  test("a sender with a tab but no id has no tab to act for: refused, nothing asked", async () => {
    reset();
    activeTabs = [{ id: 9 }];
    const r = await sendAttach({}, { tab: {}, frameId: 0 });
    expect(r.replies).toEqual([{ error: "no tab" }]);
    expect(sentToTabs()).toEqual([]);
  });

  test("a tab that has since closed (no listener anywhere) is an error reply, not a hang or an empty success", async () => {
    reset();
    tabReply = () => { throw new Error("Could not establish connection. Receiving end does not exist."); };
    const r = await sendAttach({}, { tab: { id: 404 }, frameId: 0, url: "https://a.example/" });
    expect(r.replies).toEqual([{ error: "the page that offered this is no longer open in the tab" }]);
    expect(sentToTabs().every(s => s.tabId === 404)).toBe(true);
  });

  const A = "https://a.example/apply";
  const B = "https://b.example/other";
  const navigate = async (tabId: number) => {
    for (const cb of updated) cb(tabId, { status: "loading" });
    await new Promise(r => setTimeout(r, 10));
  };

  test("offer from origin A, the tab navigates to origin B (no frame has registered yet): no frame is asked and an error comes back", async () => {
    reset();
    for (const cb of removed) cb(70);
    await new Promise(r => setTimeout(r, 10));
    await ask({ type: "frameReady" }, { tab: { id: 70 }, frameId: 0, url: A });
    await navigate(70);
    tabReply = () => ({ outcomes: [{ label: "x", filled: true }] });
    const r = await sendAttach({}, { tab: { id: 70 }, frameId: 0, url: A });
    expect(sentToTabs()).toEqual([]);
    expect(r.replies).toEqual([{ error: "the page that offered this is no longer open in the tab" }]);
  });

  test("the tab navigated to B and B's frame registered: an attach offered by A still asks no frame; B's own offer reaches it", async () => {
    reset();
    for (const cb of removed) cb(71);
    await new Promise(r => setTimeout(r, 10));
    await ask({ type: "frameReady" }, { tab: { id: 71 }, frameId: 0, url: A });
    await navigate(71);
    await ask({ type: "frameReady" }, { tab: { id: 71 }, frameId: 0, url: B });
    tabReply = () => ({ outcomes: [] });
    expect((await sendAttach({}, { tab: { id: 71 }, frameId: 0, url: A })).replies).toEqual([{ error: "the page that offered this is no longer open in the tab" }]);
    expect(sentToTabs()).toEqual([]);
    expect((await sendAttach({}, { tab: { id: 71 }, frameId: 0, url: B })).replies).toEqual([{ outcomes: [] }]);
    expect(sentToTabs().map(s => s.frameId)).toEqual([0]);
  });

  test("even with no navigation event, a frame id reused by another origin is replaced, not matched by its old origin", async () => {
    reset();
    for (const cb of removed) cb(72);
    await new Promise(r => setTimeout(r, 10));
    await ask({ type: "frameReady" }, { tab: { id: 72 }, frameId: 0, url: A });
    await ask({ type: "frameReady" }, { tab: { id: 72 }, frameId: 0, url: B });
    tabReply = () => ({ outcomes: [] });
    await sendAttach({}, { tab: { id: 72 }, frameId: 0, url: A }).then(r => expect(r.replies[0]).toMatchObject({ error: expect.any(String) }));
    expect(sentToTabs()).toEqual([]);
  });

  test("frames of a different origin in the same tab (a cross-origin iframe) are not asked; same-origin frames are", async () => {
    reset();
    for (const cb of removed) cb(73);
    await new Promise(r => setTimeout(r, 10));
    await ask({ type: "frameReady" }, { tab: { id: 73 }, frameId: 0, url: A });
    await ask({ type: "frameReady" }, { tab: { id: 73 }, frameId: 4, url: B });
    await ask({ type: "frameReady" }, { tab: { id: 73 }, frameId: 5, url: "https://a.example/embedded", origin: "https://a.example" });
    await ask({ type: "frameReady" }, { tab: { id: 73 }, frameId: 6 }); // no origin known: cannot be vouched for
    tabReply = (_t, f) => ({ outcomes: [{ label: `frame ${f}`, filled: true }] });
    const r = await sendAttach({}, { tab: { id: 73 }, frameId: 0, url: A });
    expect(sentToTabs().map(s => s.frameId)).toEqual([0, 5]);
    expect(r.replies).toEqual([{ outcomes: [{ label: "frame 0", filled: true }, { label: "frame 5", filled: true }] }]);
  });

  test("a sender with no usable origin (none, opaque, unparsable) is refused and nothing is asked", async () => {
    reset();
    await ask({ type: "frameReady" }, { tab: { id: 74 }, frameId: 0, url: A });
    for (const sender of [{ tab: { id: 74 }, frameId: 0 }, { tab: { id: 74 }, frameId: 0, origin: "null" }, { tab: { id: 74 }, frameId: 0, url: "not a url" }, { tab: { id: 74 }, frameId: 0, url: "about:blank" }]) {
      expect([sender, (await sendAttach({}, sender)).replies]).toEqual([sender, [{ error: "no origin" }]]);
    }
    expect(sentToTabs()).toEqual([]);
  });

  test("a frame that throws yields no outcomes from it and never reaches another tab", async () => {
    reset();
    activeTabs = [{ id: 9 }];
    await ask({ type: "frameReady" }, { tab: { id: 12 }, frameId: 0, url: "https://a.example/" });
    tabReply = () => { throw new Error("boom"); };
    const r = await sendAttach({}, { tab: { id: 12 }, frameId: 0, url: "https://a.example/" });
    expect(r.replies).toEqual([{ outcomes: [] }]);
    expect(sentToTabs().every(s => s.tabId === 12)).toBe(true);
  });
});

describe("sender policy", () => {
  const page = { tab: { id: 5 }, frameId: 0, url: "https://a.example/" };
  const types = () => Object.keys(SENDER_POLICY) as ToBackground["type"][];
  const own = "https://boards.greenhouse.io/acme/jobs/1";
  const bankJson = JSON.stringify({ version: 1, answers: {}, settings: { captureSelfId: true, disabledHosts: ["x.example"] } });
  /** One valid message per type, so a refusal can only be the sender's. */
  const valid: { [K in ToBackground["type"]]: Record<string, unknown> } = {
    getBank: {}, capture: { host: "vani-basics", fields: [] }, filled: { keys: [] },
    setSettings: { settings: { captureSelfId: true } }, deleteAnswer: { key: "k" }, editAnswer: { key: "k", value: ["v"] },
    replaceBank: { json: bankJson }, draft: { question: "Why?" }, jobStart: { kind: "suggest", request: {} }, jobPoll: { job: "j1" },
    appliedSignal: { url: own }, appliedPending: {}, jobRow: { url: own }, jobKey: { url: own }, appliedMark: {}, appliedDismiss: {},
    frameReady: {}, frames: {}, ingestJobs: { page: { url: own, title: "Jobs at Acme" }, saveSearch: false, jobs: [{ title: "Staff Engineer", company: "Acme", location: "Remote", url: "https://careers.acme.com/jobs/1", snippet: "Build things" }] },
    resumeGet: { url: own }, resumeInfo: { url: own }, resumePut: { url: own, name: "Resume_Synthetic.pdf", pdf: pdfOf(32) },
    resumeDelete: { url: own }, resumePreview: {}, resumeShow: { name: "Resume_Synthetic.pdf", pdf: pdfOf(32) },
    attachResume: { name: "Resume_Synthetic.pdf", pdf: pdfOf(32), pickedAt: "" },
  };

  test("the policy table and the shape table name exactly the same message types, each with a valid policy", () => {
    expect(Object.keys(SENDER_POLICY).sort()).toEqual(Object.keys(TO_BACKGROUND).sort());
    expect(Object.keys(valid).sort()).toEqual(Object.keys(TO_BACKGROUND).sort());
    for (const p of Object.values(SENDER_POLICY)) expect(["popup", "content", "either"]).toContain(p);
    for (const [t, m] of Object.entries(valid)) expect([t, checkToBackground({ type: t, ...m })]).toEqual([t, { msg: { type: t, ...m } }]);
  });

  test("a popup-only message from a page (sender.tab set) is refused and writes nothing", async () => {
    for (const t of types().filter(t => SENDER_POLICY[t] === "popup")) {
      const before = JSON.stringify(local);
      const natives = nativeSent.length;
      const r = await ask({ type: t, ...valid[t] }, page);
      expect(r.replies).toEqual([{ error: `${t} is not accepted from a page`, code: "bad_request" }]);
      expect(JSON.stringify(local)).toBe(before);
      expect(nativeSent.length).toBe(natives);
    }
  });

  test("a page-only message from the popup (no tab) is refused and asks nothing", async () => {
    for (const t of types().filter(t => SENDER_POLICY[t] === "content")) {
      const before = JSON.stringify(local);
      const natives = nativeSent.length;
      const r = await ask({ type: t, ...valid[t] }, {});
      expect(r.replies).toEqual([{ error: `${t} is not accepted from the popup`, code: "bad_request" }]);
      expect(JSON.stringify(local)).toBe(before);
      expect(nativeSent.length).toBe(natives);
    }
  });

  test("the popup's own messages still work from the popup", async () => {
    local = {};
    expect((await ask({ type: "replaceBank", json: bankJson }, {})).replies[0]).toMatchObject({ ok: true, answers: 0 });
    expect((await ask({ type: "setSettings", settings: { captureSelfId: false } }, {})).replies).toEqual([{ ok: true }]);
  });

  test("replaceBank over the bound is refused before it is parsed", async () => {
    const before = JSON.stringify(local);
    const r = await ask({ type: "replaceBank", json: " ".repeat(MAX_BANK_BYTES + 1) }, {});
    expect(r).toEqual({ async: false, replies: [{ error: "malformed replaceBank message" }] });
    expect(JSON.stringify(local)).toBe(before);
  });

  test("the three message types nothing sends are gone", async () => {
    for (const t of ["queryCandidates", "listAnswers", "patchAnswers"]) {
      expect(await ask({ type: t, query: {}, upsert: [] }, {})).toEqual({ async: false, replies: [{ error: "unknown message type" }] });
      expect(await ask({ type: t, query: {}, upsert: [] }, page)).toEqual({ async: false, replies: [{ error: "unknown message type" }] });
    }
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
    expect(r.replies).toEqual([{ error: "resumeGet is not accepted from the popup", code: "bad_request" }]);
    expect(nativeSent.length).toBe(before);
  });

  test("View from the popup opens the active tab's resume, never a URL the message claims", async () => {
    activeTabs = [{ id: 1, url: own }];
    const r = await ask({ type: "resumePreview", url: claimed }, {});
    expect(last()).toEqual({ op: "resumeOpen", key: own });
    expect(r.replies).toEqual([{ error: "the Vani Apply Mac app isn't reachable", code: "unreachable" }]);
    activeTabs = [];
  });

  test("View with no active tab, or a page that is not a job, does not call the Mac app", async () => {
    activeTabs = [];
    const before = nativeSent.length;
    expect((await ask({ type: "resumePreview", url: own }, {})).replies).toEqual([{ found: false }]);
    activeTabs = [{ id: 1, url: "chrome://extensions" }];
    expect((await ask({ type: "resumePreview" }, {})).replies).toEqual([{ found: false }]);
    activeTabs = [{ id: 1 }];
    expect((await ask({ type: "resumePreview" }, {})).replies).toEqual([{ found: false }]);
    expect(nativeSent.length).toBe(before);
    activeTabs = [];
  });

  test("a content script's View uses its own frame URL", async () => {
    await ask({ type: "resumePreview", url: claimed }, { tab: { id: 3, url: own }, frameId: 0, url: own });
    expect(last()).toEqual({ op: "resumeOpen", key: own });
  });

  test("resumeShow from the popup is refused, and a page's bad PDF never reaches the Mac app", async () => {
    const before = nativeSent.length;
    const popup = await ask({ type: "resumeShow", name: "Resume_Synthetic.pdf", pdf: pdfOf(32) }, {});
    expect(popup.replies).toEqual([{ error: "resumeShow is not accepted from the popup", code: "bad_request" }]);
    const bad = await ask({ type: "resumeShow", name: "notes.docx", pdf: pdfOf(32) }, { tab: { id: 3, url: own }, frameId: 0, url: own });
    expect(bad.replies[0]).toMatchObject({ code: "bad_request" });
    const html = await ask({ type: "resumeShow", name: "r.pdf", pdf: btoa("<html>") }, { tab: { id: 3, url: own }, frameId: 0, url: own });
    expect(html.replies[0]).toMatchObject({ code: "bad_request" });
    expect(nativeSent.length).toBe(before);
  });

  test("resumeShow sends the checked PDF as resumeOpenData and not as a stored resume", async () => {
    const pdf = btoa("%PDF-1.4\n%%EOF\n");
    await ask({ type: "resumeShow", name: " Resume_Acme.pdf ", pdf }, { tab: { id: 3, url: own }, frameId: 0, url: own });
    expect(last()).toMatchObject({ op: "resumeOpenData", name: "Resume_Acme.pdf", pdf });
    expect(last()).not.toMatchObject({ op: "resumePut" });
  });

  test("a host that echoes the PDF does not get that echo through to the popup or the page", async () => {
    nativeReply = () => ({ ok: true, name: "Resume_Acme.pdf", bytes: 12, pdf: "SECRET" });
    try {
      activeTabs = [{ id: 1, url: own }];
      const popup = await ask({ type: "resumePreview", url: claimed }, {});
      expect(popup.replies).toEqual([{ ok: true, name: "Resume_Acme.pdf", bytes: 12 }]);
      expect(JSON.stringify(popup.replies)).not.toContain("SECRET");
      const page = await ask(
        { type: "resumeShow", name: "Resume_Acme.pdf", pdf: pdfOf(32) },
        { tab: { id: 3, url: own }, frameId: 0, url: own },
      );
      expect(page.replies).toEqual([{ ok: true, name: "Resume_Acme.pdf", bytes: 12 }]);
      expect(JSON.stringify(page.replies)).not.toContain("SECRET");
    } finally {
      nativeReply = undefined;
      activeTabs = [];
    }
  });

  test("a frame that is not the job falls back to the top page and still receives no PDF", async () => {
    const frame = "https://evil.example/pixel";
    nativeReply = m => {
      const key = (m as { key?: string }).key;
      return key === own
        ? { ok: true, name: "Resume_Acme.pdf", bytes: 20, pdf: "SECRET" }
        : { found: false };
    };
    try {
      const r = await ask(
        { type: "resumePreview", url: claimed },
        { tab: { id: 3, url: own }, frameId: 4, url: frame },
      );
      expect(nativeSent.filter(m => (m as { op?: string }).op === "resumeOpen").map(m => (m as { key?: string }).key)).toContain(own);
      expect(r.replies).toEqual([{ ok: true, name: "Resume_Acme.pdf", bytes: 20 }]);
      expect(JSON.stringify(r.replies)).not.toContain("SECRET");
    } finally {
      nativeReply = undefined;
    }
  });

  const job = (n: number) => `https://boards.greenhouse.io/acme/jobs/${n}`;
  const topPage = "https://careers.acme.com/roles/1";

  async function forget(tabId: number): Promise<void> {
    for (const cb of updated) cb(tabId, { status: "loading" });
    await new Promise(r => setTimeout(r, 0));
  }

  test("a key or tab id from a content script is ignored: it opens its own frame", async () => {
    const before = nativeSent.length;
    await ask(
      { type: "resumePreview", key: claimed, tabId: 99 },
      { tab: { id: 3, url: own }, frameId: 0, url: own },
    );
    expect(nativeSent.slice(before)).toEqual([{ op: "resumeOpen", key: own }]);
  });

  test("a popup key that is not the tab the background reads, and not one of its frames, opens nothing", async () => {
    tabsById = [{ id: 7, url: job(2) }];
    const before = nativeSent.length;
    const r = await ask({ type: "resumePreview", key: own, tabId: 7 }, {});
    expect(r.replies).toEqual([{ found: false, changed: true }]);
    expect(nativeSent.length).toBe(before);
    tabsById = [];
  });

  test("the popup's tab id is re-read, and a URL in the message is not the page", async () => {
    tabsById = [{ id: 8, url: own }];
    nativeReply = m => (m as { key?: string }).key === own
      ? { found: true, name: "Resume_Acme.pdf", bytes: 20, pickedAt: "2026-10-19T00:00:00Z", pdf: "SECRET" }
      : { found: false };
    try {
      const r = await ask({ type: "resumeInfo", url: claimed, tabId: 8 }, {});
      expect(r.replies).toEqual([{ found: true, name: "Resume_Acme.pdf", bytes: 20, pickedAt: "2026-10-19T00:00:00Z", key: own }]);
      expect(JSON.stringify(r.replies)).not.toContain("SECRET");
      expect(nativeSent.filter(m => (m as { op?: string }).op === "resumeInfo").at(-1)).toEqual({ op: "resumeInfo", key: own });
    } finally {
      nativeReply = undefined;
      tabsById = [];
    }
  });

  test("a resume saved from a frame is shown when the top page has none", async () => {
    tabsById = [{ id: 40, url: topPage }];
    // The message claims a different job. Only the browser's frame URL is recorded.
    await ask({ type: "frameReady", url: job(9) }, { tab: { id: 40 }, frameId: 4, url: job(2) });
    nativeReply = m => {
      const key = (m as { key?: string }).key;
      const op = (m as { op?: string }).op;
      if (key === job(2) && op === "resumeInfo") return { found: true, name: "Resume_Other.pdf", bytes: 30, pickedAt: "2026-10-20T00:00:00Z", pdf: "SECRET" };
      if (key === job(2) && op === "resumeOpen") return { ok: true, name: "Resume_Other.pdf", bytes: 30, pdf: "SECRET" };
      return { found: false };
    };
    try {
      const info = await ask({ type: "resumeInfo", url: claimed, tabId: 40 }, {});
      expect(info.replies).toEqual([{ found: true, name: "Resume_Other.pdf", bytes: 30, pickedAt: "2026-10-20T00:00:00Z", key: job(2) }]);
      expect(JSON.stringify(info.replies)).not.toContain("SECRET");
      const before = nativeSent.length;
      const opened = await ask({ type: "resumePreview", key: job(2), tabId: 40 }, {});
      expect(opened.replies).toEqual([{ ok: true, name: "Resume_Other.pdf", bytes: 30 }]);
      expect(JSON.stringify(opened.replies)).not.toContain("SECRET");
      const openedKeys = nativeSent.slice(before).filter(m => (m as { op?: string }).op === "resumeOpen").map(m => (m as { key?: string }).key);
      expect(openedKeys).toEqual([job(2)]);
      const probed = nativeSent.filter(m => (m as { op?: string }).op === "resumeInfo").map(m => (m as { key?: string }).key);
      expect(probed).toContain(job(2));
      expect(probed).not.toContain(job(9));
    } finally {
      nativeReply = undefined;
      await forget(40);
      tabsById = [];
    }
  });

  test("the top page's resume wins over a frame, and two frames are not guessed", async () => {
    tabsById = [{ id: 41, url: own }];
    await ask({ type: "frameReady" }, { tab: { id: 41 }, frameId: 4, url: job(3) });
    nativeReply = () => ({ found: true, name: "Resume_Top.pdf", bytes: 11, pickedAt: "2026-10-19T00:00:00Z" });
    try {
      const info = await ask({ type: "resumeInfo", url: claimed, tabId: 41 }, {});
      expect(info.replies[0]).toMatchObject({ found: true, name: "Resume_Top.pdf", key: own });
      const infos = nativeSent.filter(m => (m as { op?: string }).op === "resumeInfo").map(m => (m as { key?: string }).key);
      expect(infos.at(-1)).toBe(own);
      expect(infos.filter(k => k === job(3)).length).toBe(0);
    } finally {
      nativeReply = undefined;
    }
    await forget(41);
    tabsById = [{ id: 42, url: topPage }];
    await ask({ type: "frameReady" }, { tab: { id: 42 }, frameId: 2, url: job(4) });
    await ask({ type: "frameReady" }, { tab: { id: 42 }, frameId: 3, url: job(5) });
    nativeReply = m => {
      const key = (m as { key?: string }).key;
      if (key === job(4) || key === job(5)) return { found: true, name: "Resume_Other.pdf", bytes: 10, pickedAt: "" };
      return { found: false };
    };
    try {
      const before = nativeSent.length;
      const r = await ask({ type: "resumePreview", tabId: 42 }, {});
      expect(r.replies).toEqual([{ found: false, several: true }]);
      const opens = nativeSent.slice(before).filter(m => (m as { op?: string }).op === "resumeOpen").map(m => (m as { key?: string }).key);
      expect(opens).toEqual([topPage]);
    } finally {
      nativeReply = undefined;
      await forget(41);
      await forget(42);
      tabsById = [];
    }
  });

  test("more frame resumes than the probe cap are not opened and are not all asked", async () => {
    tabsById = [{ id: 43, url: topPage }];
    for (let n = 2; n <= 11; n++) {
      await ask({ type: "frameReady" }, { tab: { id: 43 }, frameId: n, url: job(n) });
    }
    nativeReply = () => ({ found: false });
    try {
      const before = nativeSent.length;
      const r = await ask({ type: "resumeInfo", url: topPage, tabId: 43 }, {});
      expect(r.replies).toEqual([{ found: false, several: true }]);
      const asked = nativeSent.slice(before).filter(m => (m as { op?: string }).op === "resumeInfo");
      expect(asked).toEqual([{ op: "resumeInfo", key: topPage }]);
    } finally {
      nativeReply = undefined;
      await forget(43);
      tabsById = [];
    }
  });

  test("a successful popup open tells the page, with the file name and without the page URL", async () => {
    activeTabs = [{ id: 1, url: own }];
    nativeReply = () => ({ ok: true, name: "Resume_Acme.pdf", bytes: 12, pdf: "SECRET" });
    tabReply = () => ({ shown: true });
    const before = tabSent.length;
    try {
      await ask({ type: "resumePreview" }, {});
      const notes = tabSent.slice(before).filter(s => (s.msg as { type?: string }).type === "resumeNoted");
      expect(notes).toEqual([{ tabId: 1, msg: { type: "resumeNoted", name: "Resume_Acme.pdf" }, frameId: 0 }]);
      expect(JSON.stringify(notes)).not.toContain("SECRET");
      expect(JSON.stringify(notes)).not.toContain(own);
    } finally {
      nativeReply = undefined;
      activeTabs = [];
      tabReply = () => undefined;
    }
  });

  test("a page with no content script gets a toolbar badge, and a disguised name is not sent to the page", async () => {
    const badges: string[] = [];
    const chromeObj = g.chrome as { action?: { setBadgeText: (d: { tabId: number; text: string }) => Promise<void>; setBadgeBackgroundColor?: (d: unknown) => Promise<void>; getBadgeText?: (d: { tabId: number }) => Promise<string> } };
    chromeObj.action = {
      setBadgeText: async d => { badges.push(d.text); },
      setBadgeBackgroundColor: async () => undefined,
      getBadgeText: async () => badges.at(-1) ?? "",
    };
    activeTabs = [{ id: 1, url: own }];
    tabReply = () => { throw new Error("no receiver"); };
    nativeReply = () => ({ ok: true, name: "Resume_\u202Efdp.pdf", bytes: 12 });
    const before = tabSent.length;
    try {
      const r = await ask({ type: "resumePreview" }, {});
      expect(r.replies[0]).toMatchObject({ ok: true });
      const notes = tabSent.slice(before).filter(s => (s.msg as { type?: string }).type === "resumeNoted");
      expect(notes).toEqual([]);
      expect(badges).toContain("PDF");
    } finally {
      nativeReply = undefined;
      activeTabs = [];
      tabReply = () => undefined;
      chromeObj.action = undefined;
    }
  });

  test("a waiting question badge is not replaced by the PDF badge", async () => {
    const badges: string[] = [];
    const chromeObj = g.chrome as { action?: { setBadgeText: (d: { tabId: number; text: string }) => Promise<void>; setBadgeBackgroundColor?: (d: unknown) => Promise<void>; getBadgeText?: (d: { tabId: number }) => Promise<string> } };
    chromeObj.action = {
      setBadgeText: async d => { badges.push(d.text); },
      setBadgeBackgroundColor: async () => undefined,
      getBadgeText: async () => "✓?",
    };
    activeTabs = [{ id: 1, url: own }];
    tabReply = () => { throw new Error("no receiver"); };
    nativeReply = () => ({ ok: true, name: "Resume_Acme.pdf", bytes: 12 });
    try {
      const r = await ask({ type: "resumePreview" }, {});
      expect(r.replies[0]).toMatchObject({ ok: true });
      expect(badges).toEqual([]);
    } finally {
      nativeReply = undefined;
      activeTabs = [];
      tabReply = () => undefined;
      chromeObj.action = undefined;
    }
  });

  test("a resumePreview key that is not a string is refused before the Mac app is asked", async () => {
    const before = nativeSent.length;
    expect((await ask({ type: "resumePreview", key: 1 }, {})).replies).toEqual([{ error: "malformed resumePreview message" }]);
    expect((await ask({ type: "resumePreview", tabId: -1 }, {})).replies).toEqual([{ error: "malformed resumePreview message" }]);
    expect((await ask({ type: "resumeInfo", url: own, tabId: 1.5 }, {})).replies).toEqual([{ error: "malformed resumeInfo message" }]);
    expect(nativeSent.length).toBe(before);
  });
});

describe("ingestJobs", () => {
  const page = { url: "https://careers.acme.com/search?q=engineer", title: "Jobs at Acme" };
  const job = { title: "Staff Engineer", company: "Acme", location: "Remote", url: "https://careers.acme.com/jobs/1", snippet: "Build things" };

  test("is forwarded to the host as jobsIngest, with only the contract's fields", async () => {
    const r = await ask({ type: "ingestJobs", page: { ...page, extra: 1 }, saveSearch: true, jobs: [{ ...job, relevance: { score: 9 }, posted: "2026-10-01T00:00:00Z" }, job] });
    expect(nativeSent.at(-1)).toEqual({
      op: "jobsIngest",
      page,
      save_search: true,
      jobs: [{ ...job, posted: "2026-10-01T00:00:00Z" }, job],
    });
    // No Mac app here: the unreachable host is a coded reply, shown to the user rather than swallowed.
    expect(r.replies).toEqual([{ error: "the Vani Apply Mac app isn't reachable", code: "unreachable" }]);
  });

  test("a batch over a bound never reaches the host", async () => {
    const before = nativeSent.length;
    const r = await ask({ type: "ingestJobs", page, saveSearch: false, jobs: [{ ...job, url: "javascript:alert(1)" }] });
    expect(r.replies).toEqual([{ error: "malformed ingestJobs message" }]);
    expect(nativeSent.length).toBe(before);
  });
});

describe("message checks", () => {
  test("every content message type is accepted with its own fields, and bad options are refused", () => {
    for (const m of [{ type: "scan" }, { type: "fill" }, { type: "fill", overwrite: true, typeahead: false }, { type: "capture" }, { type: "clearMarks" }, { type: "appliedShow" }, { type: "resumeNoted", name: "Resume_Acme.pdf" }]) {
      expect(checkToContent(m)).toEqual({ msg: m as never });
    }
    expect(checkToContent({ type: "resumeNoted", name: "" })).toEqual({ error: "malformed resumeNoted message" });
    expect(checkToContent({ type: "fill", overwrite: "yes" })).toEqual({ error: "malformed fill message" });
  });

  test("editAnswer accepts key, value array, and optional review flag", () => {
    expect(checkToBackground({ type: "editAnswer", key: "phone", value: ["555-0100"] })).toEqual({ msg: { type: "editAnswer", key: "phone", value: ["555-0100"] } as never });
    expect(checkToBackground({ type: "editAnswer", key: "phone", value: ["555-0100"], review: true })).toEqual({ msg: { type: "editAnswer", key: "phone", value: ["555-0100"], review: true } as never });
    expect(checkToBackground({ type: "editAnswer", key: "phone", value: ["555-0100"], review: "yes" })).toEqual({ error: "malformed editAnswer message" });
  });

  test("background messages: the inherited Object keys are not message types", () => {
    for (const t of ["constructor", "hasOwnProperty", "valueOf"]) expect(checkToBackground({ type: t })).toEqual({ error: "unknown message type" });
    expect(checkToBackground({ type: "jobStart", kind: "rm", request: {} })).toEqual({ error: "malformed jobStart message" });
    // The keyed resume build is a job kind of its own, and its lookup is bounded like jobRow's.
    expect(checkToBackground({ type: "jobStart", kind: "resumeJob", request: { key: "gh:acme:1" } })).toEqual({ msg: { type: "jobStart", kind: "resumeJob", request: { key: "gh:acme:1" } } as never });
    expect(checkToBackground({ type: "jobKey", url: "x".repeat(2049) })).toEqual({ error: "malformed jobKey message" });
    expect(checkToBackground({ type: "jobKey" })).toEqual({ error: "malformed jobKey message" });
  });
});

// With the Mac app present: what the background asks it, and how often.
describe("Mac app bank round trips", () => {
  type Row = { key: string; fills: number; [k: string]: unknown };
  const host = {
    rev: 7,
    answers: {} as Record<string, Row>,
    calls: [] as Array<Record<string, unknown>>,
    /** Upcoming patches to refuse as a conflict, bumping the rev as a concurrent writer would. */
    conflicts: 0,
    failPatch: false,
    /** Upserted rows the host screens out as secret on the next patch (it stores the rest). */
    dropSecret: 0,
  };
  const ops = (): string[] => host.calls.map(c => String(c.op));
  const sleep = (ms: number) => new Promise(r => setTimeout(r, ms));
  let originalSend: unknown;

  function row(key: string, fills = 0): Row {
    return { key, labels: [key], kind: "text", value: ["v"], sensitivity: "standard", signatures: [], uses: 1, fills, updatedAt: "2026-01-01T00:00:00Z", review: false };
  }

  function reply(m: Record<string, unknown>): unknown {
    switch (m.op) {
      case "ping": return { ok: true, protocol: 1, rev: host.rev };
      case "get": return { rev: host.rev, bank: { version: 1, answers: host.answers, settings: { captureSelfId: false, disabledHosts: [] } } };
      case "log": return { ok: true };
      case "patch": {
        if (host.failPatch) return { error: "disk full" };
        if (host.conflicts > 0) { host.conflicts--; host.rev++; return { conflict: true, rev: host.rev }; }
        if (m.baseRev !== undefined && m.baseRev !== host.rev) return { conflict: true, rev: host.rev };
        const upsert = m.upsert as Row[];
        const dropped = Math.min(host.dropSecret, upsert.length);
        for (const a of upsert.slice(dropped)) host.answers[a.key] = a;
        for (const k of m.delete as string[]) delete host.answers[k];
        host.rev++;
        return { rev: host.rev, droppedSecret: dropped };
      }
      default: return { error: "unknown op" };
    }
  }

  beforeAll(() => {
    const rt = (g.chrome as { runtime: { sendNativeMessage: unknown } }).runtime;
    originalSend = rt.sendNativeMessage;
    rt.sendNativeMessage = (_app: string, m: Record<string, unknown>, cb: (r: unknown) => void) => {
      host.calls.push(m);
      queueMicrotask(() => cb(JSON.parse(JSON.stringify(reply(m)))));
    };
  });
  afterAll(() => { (g.chrome as { runtime: { sendNativeMessage: unknown } }).runtime.sendNativeMessage = originalSend; });

  async function reset(): Promise<void> {
    local = {};
    host.rev = 7;
    host.answers = { city: row("city", 2), zip: row("zip", 5), name: row("name", 0) };
    host.conflicts = 0;
    host.failPatch = false;
    host.dropSecret = 0;
    await ask({ type: "deleteAnswer", key: "nothing-here" }); // any write drops the memo
    host.calls.length = 0;
  }

  test("ten getBank calls inside the window make one native get, and the reply carries the revision", async () => {
    await reset();
    const rs = await Promise.all(Array.from({ length: 10 }, () => ask({ type: "getBank" })));
    for (const r of rs) expect(r.replies[0]).toMatchObject({ mode: "mac-app", rev: 8 });
    expect(ops().filter(o => o === "get").length).toBe(1);
  });

  test("the first getBank after the window asks again", async () => {
    await reset();
    await ask({ type: "getBank" });
    await ask({ type: "getBank" });
    expect(ops().filter(o => o === "get").length).toBe(1);
    await sleep(2_050);
    await ask({ type: "getBank" });
    expect(ops().filter(o => o === "get").length).toBe(2);
  });

  test("concurrent getBank calls make one native get", async () => {
    await reset();
    await Promise.all([ask({ type: "getBank" }), ask({ type: "getBank" }), ask({ type: "getBank" })]);
    expect(ops().filter(o => o === "get").length).toBe(1);
  });

  test("filled patches only the affected rows, counting a key once per use, against the revision it read", async () => {
    await reset();
    await ask({ type: "getBank" });
    host.calls.length = 0;
    expect((await ask({ type: "filled", keys: ["city", "city", "name"] }, content)).replies).toEqual([{ ok: true }]);
    expect(ops().filter(o => o !== "log")).toEqual(["patch"]); // no get, no put: the memo served the read
    const patch = host.calls.find(c => c.op === "patch")!;
    expect((patch.upsert as Row[]).map(r => [r.key, r.fills])).toEqual([["city", 4], ["name", 1]]);
    expect(patch.baseRev).toBe(8);
    expect(patch.delete).toEqual([]);
    expect(host.answers.zip!.fills).toBe(5);
    // The next read is the patched bank, from the memo.
    host.calls.length = 0;
    const next = (await ask({ type: "getBank" })).replies[0] as { bank: { answers: Record<string, Row> }; rev: number };
    expect(next.rev).toBe(9);
    expect(next.bank.answers.city!.fills).toBe(4);
    expect(ops()).toEqual([]);
  });

  test("a conflict drops the memo; filled reads once more, retries once, and loses nothing", async () => {
    await reset();
    await ask({ type: "getBank" });
    host.calls.length = 0;
    host.conflicts = 1; // someone else wrote since
    host.answers.city = row("city", 10); // and their row is what must be counted on
    expect((await ask({ type: "filled", keys: ["city"] }, content)).replies).toEqual([{ ok: true }]);
    expect(ops().filter(o => o !== "log")).toEqual(["patch", "get", "patch"]);
    expect(host.answers.city!.fills).toBe(11);
  });

  test("two conflicts in a row: the loss is logged with counts only and the page gets a reply, not a throw", async () => {
    await reset();
    await ask({ type: "getBank" });
    host.calls.length = 0;
    host.conflicts = 2;
    const r = await ask({ type: "filled", keys: ["city", "zip"] }, content);
    expect(r.replies).toEqual([{ ok: false, error: "fill counts were not saved" }]);
    await sleep(10);
    const logged = host.calls.filter(c => c.op === "log").map(c => c.entry as { event: string; counts: Record<string, number> });
    expect(logged.at(-1)).toEqual({ event: "fill", counts: { filled: 2, countsLost: 2 } });
    expect(JSON.stringify(host.calls.filter(c => c.op === "log"))).not.toContain("\"v\""); // no answer content
    expect(host.answers.city!.fills).toBe(2);
  });

  test("a host that refuses the patch is the same: logged, answered, not thrown", async () => {
    await reset();
    host.failPatch = true;
    const r = await ask({ type: "filled", keys: ["city"] }, content);
    expect(r.replies).toEqual([{ ok: false, error: "fill counts were not saved" }]);
    expect(host.answers.city!.fills).toBe(2);
  });

  test("a basics save the Mac app refuses is an error naming the Mac app and its reason", async () => {
    await reset();
    host.failPatch = true;
    const reply = (await ask({ type: "capture", host: "vani-basics", fields: [cityBasic] })).replies[0] as { error?: string; added?: number };
    expect(reply.added).toBeUndefined();
    expect(reply.error).toContain("the Mac app did not save it");
    expect(reply.error).toContain("disk full");
  });

  test("rows the Mac app drops as secret are not reported as saved", async () => {
    await reset();
    host.dropSecret = 1;
    const reply = (await ask({ type: "capture", host: "vani-basics", fields: [cityBasic, { ...cityBasic, label: "Country", autocomplete: "country-name", value: ["Testland"] }] })).replies[0];
    const r = reply as { added: number; updated: number; skippedSecret: number };
    expect(r.skippedSecret).toBe(1);
    expect(r.added + r.updated).toBe(1); // two sent, one stored
    expect(r.added).toBe(0); // a dropped row comes off the added count first
    expect(host.answers.country).toBeDefined(); // the host kept one of the two
    expect(host.answers.city!.value).toEqual(["v"]); // and dropped the other: the old row stands
  });

  test("a key the bank no longer holds is not invented", async () => {
    await reset();
    expect((await ask({ type: "filled", keys: ["deleted-meanwhile"] }, content)).replies).toEqual([{ ok: true }]);
    expect(ops().includes("patch")).toBe(false);
  });
});
