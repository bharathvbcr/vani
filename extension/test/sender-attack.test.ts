// Attack pass on the sender context (card ft-8ab8faff3c5f2995adea11dd40bc17a4): message shapes, sender shapes,
// origin tricks, rights per kind and oversized input, against the real background behind a fake chrome.
// Its own copy of the harness: the background registers its listener once per load.
import { afterAll, beforeAll, beforeEach, describe, expect, test } from "bun:test";
import { type PolicyKey, SENDER_POLICY, checkToBackground, isPageKind } from "../src/messages";

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
  await import("../src/background.ts?sender-attack");
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
const popup = {};
const top = { tab: { id: 3, url: "https://a.example/" }, frameId: 0, url: "https://a.example/", origin: "https://a.example" };
const sub = { tab: { id: 3, url: "https://a.example/" }, frameId: 4, url: "https://a.example/frame", origin: "https://a.example" };
const extTab = { tab: { id: 9 }, frameId: 0, url: EXT + "popup.html" };

const job = { title: "Engineer", company: "Acme", location: "Remote", url: "https://a.example/j/1", snippet: "s" };
const ingest = (url: string, extra: Record<string, unknown> = {}) => ({ type: "ingestJobs", page: { url, title: "t" }, saveSearch: false, jobs: [job], ...extra });
const ingests = () => nativeSent.filter(m => m.op === "jobsIngest");

describe("attack: message shape", () => {
  test("type that is not a string, unknown, or an Object.prototype name is answered unknown and runs nothing", async () => {
    for (const type of [undefined, null, 1, true, {}, [], ["getBank"], "", "getBank ", "GETBANK", "__proto__", "constructor", "hasOwnProperty", "toString", "valueOf", "prototype", "contacts.get"]) {
      for (const s of [popup, top, sub, extTab]) {
        const r = await ask({ type }, s);
        expect(r.replies).toEqual([{ error: "unknown message type" }]);
      }
    }
    expect(nativeSent).toEqual([]);
  });

  test("contacts op missing, non-string, lookalike or prototype-named is malformed and reaches no native call", async () => {
    for (const op of [undefined, null, 1, {}, [], "PUT", "put ", " put", "Put", "puт", "pаt", "__proto__", "constructor", "put\u0000", "list ", "ｌist"]) {
      for (const s of [popup, top, sub, extTab]) {
        const r = await ask({ type: "contacts", op, key: "email", text: "x", label: "Email", kind: "email", items: [] }, s);
        expect((r.replies[0] as { error?: string }).error).toBe("malformed contacts message");
      }
    }
    expect(nativeSent).toEqual([]);
  });

  test("a page cannot put, list or import contacts; it may get and choose", async () => {
    const bodies = { put: { op: "put", key: "email", text: "x" }, list: { op: "list" }, import: { op: "import", items: [{ key: "email", text: "x" }] } };
    for (const s of [top, sub]) for (const b of Object.values(bodies)) {
      const r = await ask({ type: "contacts", ...b }, s);
      expect(refused(r)).toBe(true);
    }
    expect(nativeSent).toEqual([]);
    for (const s of [top, sub]) {
      expect(refused(await ask({ type: "contacts", op: "get", key: "email" }, s))).toBe(false);
      expect(refused(await ask({ type: "contacts", op: "choose", label: "Email", kind: "email" }, s))).toBe(false);
    }
    // choose is page-only: the popup does not choose through this message
    expect(refused(await ask({ type: "contacts", op: "choose", label: "Email", kind: "email" }, popup))).toBe(true);
  });
});

const pageBodies: Record<string, Record<string, unknown>> = {
  appliedSignal: { type: "appliedSignal" }, appliedPending: { type: "appliedPending" }, appliedMark: { type: "appliedMark" }, appliedDismiss: { type: "appliedDismiss" },
  frames: { type: "frames" }, resumePut: { type: "resumePut", name: "R.pdf", pdf: "JVBERi0=" }, resumePreview: { type: "resumePreview" },
  resumeGet: { type: "resumeGet" }, coverGet: { type: "coverGet" }, jobRow: { type: "jobRow" }, jobKey: { type: "jobKey" },
  ingestJobs: ingest("https://a.example/jobs"),
};

describe("attack: page messages carry no url or tabId", () => {
  const variants: Array<[string, (b: Record<string, unknown>, f: string) => Record<string, unknown>]> = [
    ["value", (b, f) => ({ ...b, [f]: f === "tabId" ? 7 : "https://evil.example/" })],
    ["undefined", (b, f) => ({ ...b, [f]: undefined })],
    ["null", (b, f) => ({ ...b, [f]: null })],
    ["non-enumerable", (b, f) => Object.defineProperty({ ...b }, f, { value: f === "tabId" ? 7 : "https://evil.example/", enumerable: false })],
  ];
  test("an own url or tabId, whatever its value or enumerability, is refused from both page kinds and nothing runs", async () => {
    for (const [name, make] of Object.entries(pageBodies)) for (const f of ["url", "tabId"]) for (const [vn, v] of variants) for (const s of [top, sub]) {
      const r = await ask(v(make, f), s);
      // a malformed shape is also a refusal that runs nothing; either way no native/tab traffic
      expect((r.replies[0] as { error?: string }).error, name + " " + f + " " + vn).toBeDefined();
    }
    expect(nativeSent).toEqual([]);
    expect(tabSent).toEqual([]);
  });

  test("a url inherited through the prototype is not read: jobRow, jobKey, appliedSignal and resumeGet use the browser's sender URL", async () => {
    const evil = "https://evil.example/posting";
    for (const type of ["jobRow", "jobKey", "appliedSignal", "resumeGet", "coverGet"]) {
      nativeSent = [];
      const msg = Object.assign(Object.create({ url: evil, tabId: 99 }), { type });
      await ask(msg, top);
      for (const m of nativeSent) expect(JSON.stringify(m)).not.toContain("evil.example");
    }
    await ask(Object.assign(Object.create({ tabId: 99 }), { type: "frames" }), top);
    await ask(Object.assign(Object.create({ tabId: 99 }), { type: "appliedPending" }), top);
    expect(tabSent).toEqual([]);
  });

  test("a symbol-keyed or JSON-nested url is ignored; resumePreview key from a page is refused", async () => {
    const sym = { type: "jobRow", [Symbol("url")]: "https://evil.example/" };
    await ask(sym, top);
    for (const m of nativeSent) expect(JSON.stringify(m)).not.toContain("evil.example");
    expect(refused(await ask({ type: "resumePreview", key: "x" }, top))).toBe(true);
    expect(refused(await ask({ type: "resumePreview", key: undefined }, sub))).toBe(true);
  });
});

describe("attack: ingestJobs origin", () => {
  const from = (url: string, origin?: string) => ({ tab: { id: 3, url }, frameId: 0, url, ...(origin === undefined ? {} : { origin }) });
  test("lookalike and mismatching page.url hosts are refused; equivalent spellings of the same origin are accepted", async () => {
    const s = from("https://a.com/x", "https://a.com");
    const refusedUrls = [
      "https://a.com@b.com/", "https://b.com@a.com.evil.com/", "https://a.com.evil.com/", "https://xn--a-9ca.com/", "https://а.com/", "https://a.com./x",
      "http://a.com/x", "https://a.com:8443/x", "https://[::1]/x", "https://b.com/", "https://sub.a.com/", "//a.com/x", "javascript:alert(1)", "data:text/html,a", "not a url",
      "https://a.com%2eevil.com/",
    ];
    for (const u of refusedUrls) {
      nativeSent = [];
      const r = await ask(ingest(u), s);
      expect(ingests().length, u).toBe(0);
      expect((r.replies[0] as { error?: string }).error, u).toBeDefined();
    }
    // WHATWG parsing: a backslash is a slash in http(s), so this host is a.com and the origin matches.
    for (const u of ["https://a.com\\@b.com/", "https:///a.com", "https://A.COM/y", "https://a.com:443/y", "https://a.com", "https://A.Com:443/path?q=1#h"]) {
      nativeSent = [];
      await ask(ingest(u), s);
      expect(ingests().length, u).toBe(1);
    }
  });

  test("sender origin null, absent with about:blank/data:, or unparsable refuses; blob inheriting the origin matches only that origin", async () => {
    for (const s of [
      { tab: { id: 3 }, frameId: 2, url: "https://a.com/x", origin: "null" },
      { tab: { id: 3 }, frameId: 2, url: "about:blank" },
      { tab: { id: 3 }, frameId: 2, url: "about:srcdoc" },
      { tab: { id: 3 }, frameId: 2, url: "data:text/html,x" },
      { tab: { id: 3 }, frameId: 2 },
      { tab: { id: 3 }, frameId: 2, url: "", origin: "" },
      { tab: { id: 3 }, frameId: 2, url: "https://a.com/x", origin: "" },
    ]) {
      nativeSent = [];
      await ask(ingest("https://a.com/x"), s);
      expect(ingests().length, JSON.stringify(s)).toBe(0);
    }
    nativeSent = [];
    await ask(ingest("https://a.com/x"), { tab: { id: 3 }, frameId: 2, url: "blob:https://a.com/uuid" });
    expect(ingests().length).toBe(1);
    nativeSent = [];
    await ask(ingest("https://b.com/x"), { tab: { id: 3 }, frameId: 2, url: "blob:https://a.com/uuid" });
    expect(ingests().length).toBe(0);
  });

  test("the reported origin wins over a conflicting sender.url", async () => {
    nativeSent = [];
    await ask(ingest("https://evil.example/"), { tab: { id: 3 }, frameId: 0, url: "https://evil.example/", origin: "https://a.com" });
    expect(ingests().length).toBe(0);
  });

  test("saveSearch from a page on another origin is refused and adds no host; the popup is unrestricted", async () => {
    await ask(ingest("https://b.com/search", { saveSearch: true }), from("https://a.com/", "https://a.com"));
    expect(ingests().length).toBe(0);
    await ask(ingest("https://b.com/search", { saveSearch: true }), popup);
    await ask(ingest("https://b.com/search", { saveSearch: true }), extTab);
    expect(ingests().length).toBe(2);
  });

  test("oversized bodies are refused before the native call", async () => {
    const s = from("https://a.com/x", "https://a.com");
    await ask(ingest("https://a.com/" + "a".repeat(100_000)), s);
    await ask({ ...ingest("https://a.com/x"), jobs: Array.from({ length: 5000 }, () => job) }, s);
    await ask(ingest("https://a.com/x", { jobs: [{ ...job, snippet: "s".repeat(1_000_000) }] }), s);
    expect(ingests()).toEqual([]);
  });
});

describe("attack: sender shapes", () => {
  test("a tab with no id, or TAB_ID_NONE, never acts on a tab", async () => {
    for (const id of [undefined, -1, NaN, 1.5, -0.5, 1e300]) {
      for (const type of ["appliedMark", "appliedDismiss", "appliedPending", "fillClaim", "frames", "attachResume"]) {
        nativeSent = []; tabSent = [];
        const body: Record<string, unknown> = type === "attachResume" ? { type, name: "R.pdf", pdf: "JVBERi0=", pickedAt: "" } : { type };
        const r = await ask(body, { tab: { id }, frameId: 0, url: "https://a.example/" });
        expect(nativeSent.filter(m => m.op === "jobMark"), type + String(id)).toEqual([]);
        expect(tabSent.filter(t => (t.msg as { type: string }).type === "fill" || (t.msg as { type: string }).type === "attachResume"), type + String(id)).toEqual([]);
        expect(r.replies.length).toBe(1);
      }
    }
  });

  test("frameId undefined is a subframe, so it cannot claim a fill or register a frame", async () => {
    const s = { tab: { id: 3, url: "https://a.example/" }, url: "https://a.example/" };
    expect(await ask({ type: "fillClaim" }, s)).toEqual({ async: true, replies: [{ error: "not from a frame", code: "bad_request" }] });
    expect(await ask({ type: "frameReady" }, s)).toEqual({ async: true, replies: [{ error: "not from a frame", code: "bad_request" }] });
  });

  test("a missing or oversized sender URL makes jobRow/jobKey refuse without a native call", async () => {
    for (const url of [undefined, "x".repeat(2049), "https://a.example/" + "x".repeat(3000)]) {
      for (const type of ["jobRow", "jobKey"]) for (const frameId of [0, 3]) {
        const r = await ask({ type }, { tab: { id: 3 }, frameId, ...(url === undefined ? {} : { url }) });
        expect(refused(r), type + String(url?.length)).toBe(true);
      }
    }
    expect(nativeSent.filter(m => m.op === "jobFind")).toEqual([]);
    await ask({ type: "jobRow" }, { tab: { id: 3 }, frameId: 0, url: "https://a.example/" + "x".repeat(2030) });
    expect(nativeSent.filter(m => m.op === "jobFind").length).toBe(1);
  });

  test("a sender URL that merely begins with the extension's id is a page, not an extension page", async () => {
    const popupOnly = { type: "setSettings", settings: { captureSelfId: true } };
    const lookalikes = [
      "chrome-extension://testextensionidxxxxxxxxxxxxxxxxEVIL/popup.html", "chrome-extension://testextensionidxxxxxxxxxxxxxxxx.evil/", "chrome-extension://testextensionidxxxxxxxxxxxxxxx/",
      "https://chrome-extension://testextensionidxxxxxxxxxxxxxxxx/", "CHROME-EXTENSION://testextensionidxxxxxxxxxxxxxxxx/x", "chrome-extension://other/", "safari-web-extension://testextensionidxxxxxxxxxxxxxxxx/",
    ];
    for (const url of lookalikes) {
      const r = await ask(popupOnly, { tab: { id: 5 }, frameId: 0, url });
      expect(refused(r), url).toBe(true);
    }
    expect(refused(await ask(popupOnly, extTab))).toBe(false);
  });

  test("an empty or throwing getURL fails closed: an extension-looking sender in a tab gets no popup rights", async () => {
    const popupOnly = { type: "deleteAnswer", key: "zz" };
    ownRoot = "";
    expect(refused(await ask(popupOnly, extTab))).toBe(true);
    ownRoot = () => { throw new Error("no runtime"); };
    expect(refused(await ask(popupOnly, extTab))).toBe(true);
  });

  test("Safari's scheme is an extension page when getURL says so, and only for that id", async () => {
    ownRoot = "safari-web-extension://ABC-123/";
    const popupOnly = { type: "deleteAnswer", key: "zz" };
    expect(refused(await ask(popupOnly, { tab: { id: 5 }, frameId: 0, url: "safari-web-extension://ABC-123/popup.html" }))).toBe(false);
    expect(refused(await ask(popupOnly, { tab: { id: 5 }, frameId: 0, url: "safari-web-extension://ABC-1234/popup.html" }))).toBe(true);
    expect(refused(await ask(popupOnly, { tab: { id: 5 }, frameId: 0, url: EXT + "popup.html" }))).toBe(true);
  });

});

describe("attack: rights", () => {
  const bank = JSON.stringify({ version: 1, answers: {}, settings: { captureSelfId: true, disabledHosts: [] } });
  const popupBodies: Record<string, Record<string, unknown>> = {
    setSettings: { type: "setSettings", settings: { captureSelfId: true } }, deleteAnswer: { type: "deleteAnswer", key: "zz" }, editAnswer: { type: "editAnswer", key: "zz", value: ["v"] },
    replaceBank: { type: "replaceBank", json: bank }, "contacts.put": { type: "contacts", op: "put", key: "email", text: "x" }, "contacts.list": { type: "contacts", op: "list" },
    "contacts.import": { type: "contacts", op: "import", items: [{ key: "email", text: "x" }] }, contactsCopied: { type: "contactsCopied", set: true },
    coverInfo: { type: "coverInfo", url: "https://a.example/" }, coverOpen: { type: "coverOpen", url: "https://a.example/" }, resumeInfo: { type: "resumeInfo", url: "https://a.example/" },
    resumeDelete: { type: "resumeDelete", url: "https://a.example/" }, fillRun: { type: "fillRun" },
  };
  test("every popup-only policy key is refused from top and sub frames, runs nothing, and the table covers them all", async () => {
    const popupOnly = (Object.keys(SENDER_POLICY) as PolicyKey[]).filter(k => SENDER_POLICY[k].every(s => !isPageKind(s)));
    expect(popupOnly.sort()).toEqual(Object.keys(popupBodies).sort());
    for (const k of popupOnly) for (const s of [top, sub]) expect(refused(await ask(popupBodies[k], s)), k).toBe(true);
    expect(nativeSent).toEqual([]);
    expect(tabSent).toEqual([]);
    expect(local).toEqual({});
  });

  test("an extension page in a tab acts for no tab unless it names one: frames of its own tab 9 are not listed, a named tab 7 is", async () => {
    await ask({ type: "frameReady" }, { tab: { id: 9 }, frameId: 2, url: "https://a.example/" });
    await ask({ type: "frameReady" }, { tab: { id: 7 }, frameId: 6, url: "https://a.example/" });
    expect(await ask({ type: "frames" }, extTab)).toEqual({ async: true, replies: [{ frames: [] }] });
    expect(await ask({ type: "frames", tabId: 7 }, extTab)).toEqual({ async: true, replies: [{ frames: [6] }] });
    expect(await ask({ type: "frames", tabId: 7 }, popup)).toEqual({ async: true, replies: [{ frames: [6] }] });
    // a page naming a tab is refused, not obeyed
    expect(refused(await ask({ type: "frames", tabId: 7 }, { tab: { id: 9 }, frameId: 0, url: "https://a.example/" }))).toBe(true);
  });

  test("an extension page in a tab never is a page: it cannot send page-only messages", async () => {
    for (const body of [{ type: "filled", keys: [] }, { type: "fillClaim" }, { type: "jobRow" }, { type: "jobKey" }, { type: "frameReady" }, { type: "resumeGet" }, { type: "coverGet" }, { type: "draft", question: "q" }]) {
      expect(refused(await ask(body, extTab)), body.type).toBe(true);
    }
  });

  test("a popup tabId that is NaN, negative, fractional, huge, or a string never reaches the browser as a tab or is refused", async () => {
    for (const tabId of ["7", "0x7", [7], { valueOf: 7 }, true, null]) {
      for (const type of ["appliedMark", "appliedDismiss", "appliedPending", "frames", "resumeInfo", "resumePreview", "fillRun"]) {
        const r = await ask({ type, url: "https://a.example/", tabId }, popup);
        expect((r.replies[0] as { error?: string }).error, type).toBeDefined();
      }
    }
    for (const tabId of [NaN, -1, -5, 1.5, Infinity, 1e300, Number.MAX_SAFE_INTEGER + 2]) {
      tabSent = []; nativeSent = [];
      for (const type of ["resumeInfo", "resumePreview", "fillRun"]) {
        const r = await ask({ type, url: "https://a.example/", tabId }, popup);
        expect((r.replies[0] as { error?: string }).error, type + String(tabId)).toBeDefined();
      }
      for (const type of ["appliedMark", "appliedDismiss", "appliedPending", "frames"]) await ask({ type, tabId }, popup);
      expect(tabSent, String(tabId)).toEqual([]);
      expect(nativeSent.filter(m => m.op === "jobMark"), String(tabId)).toEqual([]);
    }
  });
});

describe("attack: regression against 4a53b96 for embed-URL frames", () => {
  const embed = "https://job-boards.greenhouse.io/embed/job_app?for=acme&token=12345";
  test("appliedSignal from a Greenhouse embed subframe asks about the embed's posting first, then the tab's page", async () => {
    nativeReply = () => ({ ok: true, found: false });
    await ask({ type: "appliedSignal" }, { tab: { id: 3, url: "https://acme.example/careers" }, frameId: 4, url: embed });
    const finds = nativeSent.filter(m => m.op === "jobFind").map(m => m.url);
    expect(finds[0]).toBe("https://boards.greenhouse.io/acme/jobs/12345");
    expect(finds[1]).toBe("https://acme.example/careers");
    expect(finds.length).toBe(2);
  });
  test("a top frame asks once under its own URL, and the popup asks under the url it names for the tab it names", async () => {
    nativeReply = () => ({ ok: true, found: false });
    await ask({ type: "appliedSignal" }, { tab: { id: 3, url: "https://acme.example/careers" }, frameId: 0, url: "https://acme.example/careers" });
    expect(nativeSent.filter(m => m.op === "jobFind").map(m => m.url)).toEqual(["https://acme.example/careers"]);
    nativeSent = [];
    await ask({ type: "appliedSignal", url: "https://x.example/job/1", tabId: 5 }, popup);
    expect(nativeSent.filter(m => m.op === "jobFind").map(m => m.url)).toEqual(["https://x.example/job/1"]);
  });
  test("resumeGet from an embed subframe keys on the embed URL, falling back to the tab's page only when the embed has none", async () => {
    nativeReply = () => ({ ok: true, found: false });
    await ask({ type: "resumeGet" }, { tab: { id: 3, url: "https://acme.example/careers" }, frameId: 4, url: embed });
    const urls = nativeSent.map(m => JSON.stringify(m));
    expect(urls.length).toBeGreaterThanOrEqual(1);
    expect(urls[0]).toContain("12345");
  });
});

describe("attack round 2: a sender in no tab is the popup only when it is this extension's", () => {
  const foreign = ["https://evil.example/", "chrome-extension://otherextensionidxxxxxxxxxxxxxxxxx/popup.html", "devtools://devtools/bundled/devtools_app.html", "about:blank", "", EXT.slice(0, -1) + "x/popup.html", "https://evil.example/" + EXT];
  const sets = { type: "setSettings", settings: { captureSelfId: true } };
  test("a tabless sender with a foreign URL is refused a popup-only message and touches nothing", async () => {
    for (const url of foreign) {
      const before = JSON.stringify(local);
      const r = await ask(sets, { url });
      expect([url, refused(r)]).toEqual([url, true]);
      expect([url, JSON.stringify(local), nativeSent.length, tabSent.length]).toEqual([url, before, 0, 0]);
    }
  });
  test("a tabless sender with a foreign URL is refused every popup-only key", async () => {
    const keys = (Object.keys(SENDER_POLICY) as PolicyKey[]).filter(k => SENDER_POLICY[k].includes("popup") && !SENDER_POLICY[k].includes("topFrame"));
    expect(keys.length).toBeGreaterThan(3);
    for (const k of keys) {
      const r = await ask({ type: k.split(".")[0], op: k.split(".")[1], key: "k", settings: {}, json: "{}", value: [], url: "https://a.example/", set: true, overwrite: true, items: [], text: "t" }, { url: "https://evil.example/" });
      expect([k, (r.replies[0] as { code?: string })?.code]).toEqual([k, "bad_request"]);
      expect(nativeSent.length).toBe(0);
    }
  });
  test("with no own URL (getURL failed) a tabless sender that names a URL fails closed; one that names none stays the popup", async () => {
    ownRoot = "";
    expect(refused(await ask(sets, { url: EXT + "popup.html" }))).toBe(true);
    expect(refused(await ask(sets, { url: "https://evil.example/" }))).toBe(true);
    expect(refused(await ask(sets, {}))).toBe(false);
  });
  test("the real popup (own URL, no tab), a tabless sender with no URL, and an extension page in a tab keep their rights", async () => {
    expect(refused(await ask(sets, { url: EXT + "popup.html" }))).toBe(false);
    expect(refused(await ask(sets, {}))).toBe(false);
    expect(refused(await ask(sets, extTab))).toBe(false);
  });
});

describe("attack round 2: an empty page URL is no URL", () => {
  test("jobRow and jobKey from a frame whose URL is empty are refused and ask the host nothing", async () => {
    for (const type of ["jobRow", "jobKey"]) {
      const r = await ask({ type }, { tab: { id: 3, url: "https://a.example/" }, frameId: 0, url: "" });
      expect([type, refused(r)]).toEqual([type, true]);
    }
    expect(nativeSent.filter(m => m.op === "jobFind")).toEqual([]);
  });
});

describe("attack round 2: every tabId a popup names is checked as strictly as chrome.tabs.get needs", () => {
  const bad = [Number.NaN, -1, 1.5, Number.POSITIVE_INFINITY, Number.MAX_SAFE_INTEGER + 2, 1e300, "3", null, {}];
  for (const type of ["appliedSignal", "appliedPending", "appliedMark", "appliedDismiss", "frames"]) {
    test(type + " refuses a malformed tabId at the shape check", async () => {
      for (const tabId of bad) {
        expect([type, String(tabId), "error" in checkToBackground({ type, tabId })]).toEqual([type, String(tabId), true]);
        nativeSent = [];
        const r = await ask({ type, tabId }, popup);
        expect([type, String(tabId), typeof (r.replies[0] as { error?: unknown } | undefined)?.error]).toEqual([type, String(tabId), "string"]);
        expect(nativeSent).toEqual([]);
        expect(tabSent).toEqual([]);
      }
      for (const tabId of [undefined, 0, 7]) expect([type, tabId, "error" in checkToBackground({ type, tabId })]).toEqual([type, tabId, false]);
    });
  }
});
