// Round 2 of card ft-8ab8faff3c5f2995adea11dd40bc17a4: a single-page board pushes a new URL without a new document, so the
// browser's sender.url is the load URL. appliedSignal, jobRow and jobKey may name the page they are on, accepted only on the sender's own origin.
// Its own copy of the harness: the background registers its listener once per load.
import { afterAll, beforeAll, beforeEach, describe, expect, test } from "bun:test";
const MAX_JOB_URL = 2048; // the bound the host lookup has had since the message carried the URL

type Listener = (msg: unknown, sender: object, sendResponse: (r?: unknown) => void) => boolean;
const g = globalThis as Record<string, unknown>;
const saved = { chrome: g.chrome, browser: g.browser };
let listener: Listener | undefined;
const EXT = "chrome-extension://testextensionidxxxxxxxxxxxxxxxx/";
let ownRoot: string | (() => string) = EXT;
let local: Record<string, unknown> = {};
let nativeSent: Array<Record<string, unknown>> = [];
let tabSent: Array<{ tabId: number; msg: unknown; frameId: number | undefined }> = [];
let nativeReply: (m: Record<string, unknown>) => unknown = () => ({ ok: true });
let tabsById: Array<{ id: number; url?: string }> = [];

beforeAll(async () => {
  const on = { addListener() {} };
  g.browser = undefined;
  g.chrome = {
    runtime: {
      onMessage: { addListener(cb: Listener) { listener = cb; } },
      sendNativeMessage(_app: string, m: Record<string, unknown>, cb: (r: unknown) => void) {
        nativeSent.push(m);
        (g.chrome as { runtime: { lastError?: unknown } }).runtime.lastError = undefined;
        cb(nativeReply(m));
      },
      connectNative() { throw new Error("no host"); },
      getURL: (p: string) => {
        const r = typeof ownRoot === "function" ? ownRoot() : ownRoot;
        return r + p;
      },
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
    tabs: {
      onUpdated: on,
      onRemoved: on,
      query: async () => [],
      get: async (id: number) => { const t = tabsById.find(x => x.id === id); if (!t) throw new Error("No tab"); return t; },
      sendMessage: async (tabId: number, msg: unknown, opts?: { frameId?: number }) => { tabSent.push({ tabId, msg, frameId: opts?.frameId }); return undefined; },
    },
    action: undefined,
    scripting: { executeScript: async () => [] },
    commands: { onCommand: on },
  };
  await import("../src/background.ts?spa-url");
});
afterAll(() => { g.chrome = saved.chrome; g.browser = saved.browser; });
beforeEach(() => { nativeSent = []; tabSent = []; ownRoot = EXT; nativeReply = () => ({ ok: true }); tabsById = []; });

async function ask(msg: unknown, sender: object = {}): Promise<{ async: boolean; replies: unknown[] }> {
  const replies: unknown[] = [];
  let done: () => void = () => undefined;
  const answered = new Promise<void>(r => { done = r; });
  const async = listener!(msg, sender, r => { replies.push(r); done(); });
  await Promise.race([answered, new Promise(r => setTimeout(r, 1500))]);
  return { async, replies };
}
const refused = (r: { replies: unknown[] }) => (r.replies[0] as { code?: string } | undefined)?.code === "bad_request";

const LOAD = "https://x.myworkdayjobs.com/en-US/careers";
const PUSHED = "https://x.myworkdayjobs.com/en-US/careers/job/123";
const ORIGIN = "https://x.myworkdayjobs.com";
const top = { tab: { id: 3, url: LOAD }, frameId: 0, url: LOAD, origin: ORIGIN };
const sub = { tab: { id: 3, url: "https://top.example/" }, frameId: 4, url: "https://x.myworkdayjobs.com/embed", origin: ORIGIN };
const lookups = () => nativeSent.filter(m => m.op === "jobFind");
const URL_TYPES = ["appliedSignal", "jobRow", "jobKey"] as const;

describe("page url on appliedSignal, jobRow and jobKey (single-page boards)", () => {
  test("a same-origin pushed URL is the one looked up, from the top frame and from a subframe", async () => {
    nativeReply = () => ({ row: null });
    for (const type of URL_TYPES) for (const s of [top, sub]) {
      nativeSent = [];
      const url = s === top ? PUSHED : "https://x.myworkdayjobs.com/embed/job/9";
      const r = await ask({ type, url }, s);
      expect([type, refused(r)]).toEqual([type, false]);
      expect([type, lookups().map(m => m.url)]).toEqual([type, [url]]);
    }
  });

  test("with no url the browser's sender URL is used, as before", async () => {
    nativeReply = () => ({ row: null });
    for (const type of URL_TYPES) {
      nativeSent = [];
      await ask({ type }, top);
      expect([type, lookups().map(m => m.url)]).toEqual([type, [LOAD]]);
    }
  });

  test("a subframe's url must match the subframe's origin, not the top frame's", async () => {
    nativeReply = () => ({ row: null });
    const topOnly = "https://top.example/jobs/1";
    for (const type of URL_TYPES) {
      nativeSent = [];
      expect([type, refused(await ask({ type, url: topOnly }, sub))]).toEqual([type, true]);
      expect(nativeSent).toEqual([]);
    }
    // and the reverse: a top frame cannot name the subframe's embed origin
    const other = { tab: { id: 3, url: "https://top.example/" }, frameId: 0, url: "https://top.example/", origin: "https://top.example" };
    for (const type of URL_TYPES) expect(refused(await ask({ type, url: PUSHED }, other))).toBe(true);
    expect(nativeSent).toEqual([]);
  });

  const bad: Array<[string, unknown]> = [
    ["cross-origin", "https://evil.example/job/1"],
    ["lookalike suffix", "https://x.myworkdayjobs.com.evil.example/job/1"],
    ["userinfo trick", "https://x.myworkdayjobs.com@evil.example/job/1"],
    ["http vs https", "http://x.myworkdayjobs.com/job/1"],
    ["explicit port", "https://x.myworkdayjobs.com:8443/job/1"],
    ["punycode host", "https://xn--x-myworkdayjobs.com/job/1"],
    ["unicode lookalike host", "https://x.myworkdayjоbs.com/job/1"],
    ["javascript:", "javascript:alert(1)"],
    ["data:", "data:text/html,x"],
    ["blob:", "blob:https://x.myworkdayjobs.com/abc"],
    ["relative", "/job/1"],
    ["empty", ""],
    ["number", 7],
    ["object", { href: PUSHED }],
    ["array", [PUSHED]],
    ["null", null],
    ["undefined", undefined],
    ["oversized", PUSHED + "?q=" + "x".repeat(MAX_JOB_URL)],
  ];
  test("every bad url is refused with bad_request, reaches no host, and is not replaced by the browser's URL", async () => {
    nativeReply = () => ({ row: null });
    for (const type of URL_TYPES) for (const s of [top, sub]) for (const [name, url] of bad) {
      nativeSent = [];
      const r = await ask({ type, url }, s);
      expect([type, name, refused(r)]).toEqual([type, name, true]);
      expect([type, name, nativeSent]).toEqual([type, name, []]);
    }
  });

  test("a url exactly at MAX_JOB_URL on the right origin is accepted; one byte more is refused", async () => {
    nativeReply = () => ({ row: null });
    const pad = (n: number) => PUSHED + "?q=" + "x".repeat(n - PUSHED.length - 3);
    nativeSent = [];
    expect(refused(await ask({ type: "jobRow", url: pad(MAX_JOB_URL) }, top))).toBe(false);
    expect(lookups().length).toBe(1);
    expect(refused(await ask({ type: "jobRow", url: pad(MAX_JOB_URL + 1) }, top))).toBe(true);
    expect(lookups().length).toBe(1);
  });

  test("a sender whose origin the browser does not give refuses any url", async () => {
    const noOrigin = { tab: { id: 3, url: "about:blank" }, frameId: 0, url: "about:blank", origin: "null" };
    for (const type of URL_TYPES) expect(refused(await ask({ type, url: PUSHED }, noOrigin))).toBe(true);
  });

  test("popup and extension-page senders are unchanged: the popup names its page; jobRow and jobKey stay page-only", async () => {
    nativeReply = () => ({ row: null });
    nativeSent = [];
    expect(refused(await ask({ type: "appliedSignal", url: "https://any.example/job/1", tabId: 5 }, {}))).toBe(false);
    expect(lookups().map(m => m.url)).toEqual(["https://any.example/job/1"]);
    for (const type of ["jobRow", "jobKey"]) {
      expect(refused(await ask({ type, url: PUSHED }, {}))).toBe(true);
      expect(refused(await ask({ type, url: PUSHED }, { tab: { id: 9 }, frameId: 0, url: EXT + "popup.html" }))).toBe(true);
    }
  });

  test("messages that use the browser's URL still refuse a page-sent url, even a same-origin one", async () => {
    nativeSent = [];
    for (const body of [{ type: "coverGet" }, { type: "resumeGet" }, { type: "resumePut", name: "R.pdf", pdf: "JVBERi0=" }, { type: "frameReady" }, { type: "frames" }, { type: "resumePreview" }]) {
      for (const s of [top, sub]) {
        const r = await ask({ ...body, url: PUSHED }, s);
        expect([body.type, (r.replies[0] as { error?: string }).error]).toEqual([body.type, `${body.type} from a page carries no url`]);
      }
    }
    expect(nativeSent).toEqual([]);
  });

  test("a page-sent tabId and a resumePreview key stay refused; ingestJobs keeps its origin check", async () => {
    for (const type of URL_TYPES) expect(refused(await ask({ type, tabId: 9 }, top))).toBe(true);
    expect(refused(await ask({ type: "resumePreview", key: "k" }, top))).toBe(true);
    expect(refused(await ask({ type: "ingestJobs", page: { url: "https://evil.example/", title: "t" }, saveSearch: true, jobs: [{ title: "Engineer", company: "Acme", location: "Remote", url: "https://x.myworkdayjobs.com/j/1", snippet: "s" }] }, top))).toBe(true);
  });
});
