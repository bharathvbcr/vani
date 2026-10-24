// Adversarial review of the G10 trust/routing fixes (sender policy, attachResume routed to the sender's tab,
// replaceBank bound, deleted message types). The background worker is loaded for real behind a fake chrome,
// as in background.test.ts; its own copy here because the module registers its listener once per load.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { MAX_BANK_BYTES } from "../src/bank";
import { SENDER_POLICY, type ToBackground, TO_BACKGROUND, checkToBackground } from "../src/messages";
import { MAX_RESUME_BYTES, toBase64 } from "../src/resume";

type Listener = (msg: unknown, sender: object, sendResponse: (r?: unknown) => void) => boolean;

const g = globalThis as Record<string, unknown>;
const saved = { chrome: g.chrome, browser: g.browser };
let listener: Listener | undefined;
let local: Record<string, unknown> = {};
const removed: Array<(tabId: number) => void> = [];
const nativeSent: object[] = [];
let activeTabs: Array<{ id?: number }> = [];
const tabSent: Array<{ tabId: number; msg: unknown; frameId: number | undefined }> = [];
let tabReply: (tabId: number, frameId: number) => unknown = () => undefined;

beforeAll(async () => {
  const on = { addListener() {} };
  g.browser = undefined;
  g.chrome = {
    runtime: {
      onMessage: { addListener(cb: Listener) { listener = cb; } },
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
    tabs: {
      onUpdated: on,
      onRemoved: { addListener(cb: (tabId: number) => void) { removed.push(cb); } },
      query: async () => activeTabs,
      sendMessage: async (tabId: number, msg: unknown, opts?: { frameId?: number }) => {
        tabSent.push({ tabId, msg, frameId: opts?.frameId });
        return tabReply(tabId, opts?.frameId ?? 0);
      },
    },
    action: undefined,
    scripting: { executeScript: async () => [] },
    commands: { onCommand: on },
  };
  await import("../src/background.ts?g10-adversarial");
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

function pdfBytes(n: number, signature = "%PDF-"): Uint8Array {
  const b = new Uint8Array(n).fill(0x20);
  b.set(new TextEncoder().encode(signature).subarray(0, n));
  return b;
}
const pdfOf = (n: number, signature = "%PDF-") => toBase64(pdfBytes(n, signature));

describe("adversarial: sender shapes", () => {
  const types = () => Object.keys(SENDER_POLICY) as ToBackground["type"][];
  const own = "https://boards.greenhouse.io/acme/jobs/1";
  const bankJson = JSON.stringify({ version: 1, answers: {}, settings: { captureSelfId: true, disabledHosts: [] } });
  const valid: Record<string, Record<string, unknown>> = {
    setSettings: { settings: { captureSelfId: true } }, deleteAnswer: { key: "zz" }, editAnswer: { key: "zz", value: ["v"] },
    replaceBank: { json: bankJson }, resumeInfo: { url: own }, resumeDelete: { url: own },
    filled: { keys: [] }, draft: { question: "Why?" }, jobStart: { kind: "suggest", request: {} }, jobPoll: { job: "j1" },
    jobRow: { url: own }, frameReady: {}, resumeGet: { url: own }, resumePreview: {},
    resumeShow: { name: "R.pdf", pdf: pdfOf(32) }, attachResume: { name: "R.pdf", pdf: pdfOf(32), pickedAt: "" },
  };

  test("the manifest has one tabless context (the popup): no options page, offscreen document, side panel, devtools page or externally_connectable, in either build", () => {
    const manifest = JSON.parse(readFileSync(join(import.meta.dir, "../static/manifest.json"), "utf8")) as Record<string, unknown>;
    for (const k of ["options_page", "options_ui", "side_panel", "devtools_page", "externally_connectable", "web_accessible_resources", "chrome_url_overrides", "sandbox", "background"]) {
      expect([k, manifest[k]]).toEqual([k, undefined]);
    }
    expect(Object.keys(manifest.action as object)).toContain("default_popup");
    expect(manifest.permissions as string[]).not.toContain("offscreen");
    // build.ts spreads the same base into dist-chrome and dist-safari and adds only nativeMessaging, key and background.
    const build = readFileSync(join(import.meta.dir, "../build.ts"), "utf8");
    expect(build).not.toMatch(/externally_connectable|web_accessible|options_ui|side_panel|offscreen/);
    for (const f of ["background.ts", "content.ts", "popup.ts", "nativeport.ts", "store.ts"]) {
      expect(readFileSync(join(import.meta.dir, "../src", f), "utf8")).not.toMatch(/onMessageExternal|onConnectExternal|runtime\.onConnect\b/);
    }
  });

  test("a sender with no tab and any other shape (id, url, frameId, origin) is popup-shaped: page types are refused and nothing is sent", async () => {
    for (const sender of [{ id: "clhaobdmkmapebgnmpcfmakbfakmgfip" }, { url: "chrome-extension://clhaobdmkmapebgnmpcfmakbfakmgfip/popup.html" }, { frameId: 0 }, { frameId: 3, documentId: "d" }, { origin: "null" }]) {
      for (const t of types().filter(t => SENDER_POLICY[t] === "content")) {
        const natives = nativeSent.length;
        const r = await ask({ type: t, ...valid[t] }, sender);
        expect([t, r.replies]).toEqual([t, [{ error: `${t} is not accepted from the popup`, code: "bad_request" }]]);
        expect(nativeSent.length).toBe(natives);
      }
    }
  });

  test("tab null/undefined is no tab; any other tab value (0, '', false, []) is a page: popup-only types refused, router never throws", async () => {
    for (const sender of [{ tab: null }, { tab: undefined }]) {
      expect((await ask({ type: "attachResume", name: "R.pdf", pdf: pdfOf(32), pickedAt: "" }, sender)).replies).toEqual([{ error: "attachResume is not accepted from the popup", code: "bad_request" }]);
    }
    for (const tab of [0, "", false, 1, "x", []]) {
      const r = await ask({ type: "setSettings", settings: { captureSelfId: true } }, { tab });
      expect([tab, r.replies]).toEqual([tab, [{ error: "setSettings is not accepted from a page", code: "bad_request" }]]);
    }
  });

  test("a content script in a sub-frame (frameId 5) may send content-only types, and is refused popup-only ones", async () => {
    const sub = { tab: { id: 31, url: "https://boards.greenhouse.io/acme/jobs/1" }, frameId: 5, url: "https://job-boards.greenhouse.io/embed/job_app?for=acme" };
    // runs the handler (a bad job id is its own reply) without touching the store, whose mode other test files leave behind
    expect((await ask({ type: "jobPoll", job: "" }, sub)).replies).toEqual([{ error: "bad job id", code: "bad_request" }]);
    expect((await ask({ type: "frameReady" }, sub)).replies).toEqual([{ ok: true }]);
    for (const t of types().filter(t => SENDER_POLICY[t] === "popup")) {
      expect((await ask({ type: t, ...valid[t] }, sub)).replies).toEqual([{ error: `${t} is not accepted from a page`, code: "bad_request" }]);
    }
  });

  test("a type missing from one of the two tables is not routable: the router answers unknown message type", () => {
    expect(Object.keys(SENDER_POLICY).sort()).toEqual(Object.keys(TO_BACKGROUND).sort());
    expect(checkToBackground({ type: "newType" })).toEqual({ error: "unknown message type" });
  });

  test("an 'either' type from a page acts for the page's own tab even when the message names another tab or -1", async () => {
    for (const cb of removed) { cb(21); cb(22); }
    await new Promise(r => setTimeout(r, 10));
    await ask({ type: "frameReady" }, { tab: { id: 21 }, frameId: 0, url: "https://a.example/" });
    await ask({ type: "frameReady" }, { tab: { id: 22 }, frameId: 4, url: "https://a.example/" });
    expect((await ask({ type: "frames", tabId: 22 }, { tab: { id: 21 }, frameId: 0, url: "https://a.example/" })).replies).toEqual([{ frames: [0] }]);
    expect((await ask({ type: "frames", tabId: 22 }, { tab: { id: -1 }, frameId: 0, url: "https://a.example/" })).replies).toEqual([{ frames: [] }]);
  });

  test("the deleted message types from every sender shape: one error reply, nothing written, nothing sent, no throw", async () => {
    local = { vani: "sentinel" };
    const natives = nativeSent.length;
    const before = JSON.stringify(local);
    for (const t of ["queryCandidates", "listAnswers", "patchAnswers"]) {
      for (const sender of [{}, { tab: { id: 5 }, frameId: 0, url: "https://a.example/" }, { tab: { id: 5 }, frameId: 2, url: "https://a.example/" }, { tab: null }]) {
        for (const body of [{}, { query: {}, upsert: [], deleteKeys: ["a"], offset: 0, limit: 5, search: "x", signatures: ["s"] }, { upsert: "x", deleteKeys: 7 }]) {
          expect(await ask({ type: t, ...body }, sender)).toEqual({ async: false, replies: [{ error: "unknown message type" }] });
        }
      }
    }
    expect(JSON.stringify(local)).toBe(before);
    expect(nativeSent.length).toBe(natives);
  });
});

describe("adversarial: attachResume", () => {
  const send = (over: Record<string, unknown>, sender: object = { tab: { id: 61 }, frameId: 0, url: "https://a.example/" }) =>
    ask({ type: "attachResume", name: "Resume_Synthetic.pdf", pdf: pdfOf(64), pickedAt: "2026-10-19", ...over }, sender);
  const sent = () => tabSent.filter(s => (s.msg as { type?: string }).type === "attachResume");
  const fresh = () => { tabSent.length = 0; tabReply = () => ({ outcomes: [] }); activeTabs = [{ id: 99 }]; };

  test("sender.tab.id of -1 (TAB_ID_NONE) is never turned into the active tab: only tab -1 is asked", async () => {
    fresh();
    const r = await send({}, { tab: { id: -1 }, frameId: 0, url: "https://a.example/" });
    expect(tabSent.some(s => s.tabId === 99)).toBe(false);
    expect(sent().every(s => s.tabId === -1)).toBe(true);
    expect(r.replies.length).toBe(1);
  });

  test("a tab with id undefined is refused; null or NaN never fall back to the active tab", async () => {
    fresh();
    expect((await send({}, { tab: {}, frameId: 0 })).replies).toEqual([{ error: "no tab" }]);
    for (const id of [null, Number.NaN]) {
      const r = await send({}, { tab: { id }, frameId: 0 });
      expect(r.replies.length).toBe(1);
    }
    expect(tabSent.some(s => s.tabId === 99)).toBe(false);
  });

  test("a reply carries only the sender's tab's outcomes; another tab's registered frames are never asked", async () => {
    fresh();
    for (const cb of removed) { cb(61); cb(62); }
    await new Promise(r => setTimeout(r, 10));
    await ask({ type: "frameReady" }, { tab: { id: 61 }, frameId: 0, url: "https://a.example/" });
    await ask({ type: "frameReady" }, { tab: { id: 62 }, frameId: 0, url: "https://a.example/" });
    tabReply = tabId => ({ outcomes: [{ label: `tab ${tabId}`, filled: true }] });
    const r = await send({});
    expect(r.replies).toEqual([{ outcomes: [{ label: "tab 61", filled: true }] }]);
    expect(sent().every(s => s.tabId === 61)).toBe(true);
  });

  test("a message that names its own tabId, frameId or url cannot redirect the attach", async () => {
    fresh();
    await send({ tabId: 99, frameId: 4, url: "https://evil.example/" });
    expect(sent().length).toBeGreaterThan(0);
    expect(sent().every(s => s.tabId === 61)).toBe(true);
  });

  test("frames receive only the contract's fields, the trimmed name and a 40-char pickedAt", async () => {
    fresh();
    await ask({ type: "frameReady" }, { tab: { id: 61 }, frameId: 0, url: "https://a.example/" });
    await send({ name: "  Resume_Synthetic.pdf  ", pickedAt: "x".repeat(500), extra: "no" });
    const m = sent()[0]!.msg as Record<string, unknown>;
    expect(Object.keys(m).sort()).toEqual(["name", "pdf", "pickedAt", "type"]);
    expect(m.name).toBe("Resume_Synthetic.pdf");
    expect((m.pickedAt as string).length).toBe(40);
  });

  test("base64 variants are refused before any frame is asked: whitespace, newlines, data: prefix, url-safe, bad padding, wrong length, full-width", async () => {
    fresh();
    const good = pdfOf(64);
    const bad = [
      good.slice(0, 8) + "\n" + good.slice(8), good + "\n", " " + good, good.slice(0, 4) + " " + good.slice(4), good.slice(0, 4) + "\r\n" + good.slice(4),
      `data:application/pdf;base64,${good}`, good + "x", good.slice(0, -1), good + "=", good + "==", "A===", "====", "JVBERi0", "JVBERi0=\u0000",
      good.replace(/.$/, "*"), "​JVBERi0=", "ＪＶＢＥＲｉ０＝",
    ];
    for (const pdf of bad) {
      const r = await send({ pdf });
      expect([pdf.slice(0, 20), (r.replies[0] as { code?: string }).code]).toEqual([pdf.slice(0, 20), "bad_request"]);
    }
    expect(sent()).toEqual([]);
  });

  test("the size cap: cap-2..cap bytes are sent; cap+1..cap+4 are refused as too_large though the base64 length cap still passes", async () => {
    fresh();
    await ask({ type: "frameReady" }, { tab: { id: 61 }, frameId: 0, url: "https://a.example/" });
    for (const n of [MAX_RESUME_BYTES - 2, MAX_RESUME_BYTES - 1, MAX_RESUME_BYTES]) {
      tabSent.length = 0;
      const r = await send({ pdf: toBase64(pdfBytes(n)) });
      expect([n, r.replies]).toEqual([n, [{ outcomes: [] }]]);
      expect(sent().length).toBe(1);
    }
    for (const n of [MAX_RESUME_BYTES + 1, MAX_RESUME_BYTES + 2, MAX_RESUME_BYTES + 3, MAX_RESUME_BYTES + 4]) {
      tabSent.length = 0;
      const r = await send({ pdf: toBase64(pdfBytes(n)) });
      expect([n, (r.replies[0] as { code?: string }).code]).toEqual([n, "too_large"]);
      expect(sent()).toEqual([]);
    }
  });

  test("a 4 MB base64 string is refused by length without decoding; a non-string pdf is refused by shape", async () => {
    fresh();
    expect((await send({ pdf: "A".repeat(4_000_000) })).replies[0]).toMatchObject({ code: "too_large" });
    expect(await send({ pdf: 7 })).toEqual({ async: false, replies: [{ error: "malformed attachResume message" }] });
    expect(await send({ pdf: null })).toEqual({ async: false, replies: [{ error: "malformed attachResume message" }] });
    expect(sent()).toEqual([]);
  });

  test("the signature is strict: BOM, whitespace, junk, lower-case or short signatures are refused; a bare %PDF- is accepted", async () => {
    fresh();
    await ask({ type: "frameReady" }, { tab: { id: 61 }, frameId: 0, url: "https://a.example/" });
    const text = (s: string) => [...new TextEncoder().encode(s)];
    const enc = (b: number[]) => toBase64(Uint8Array.from(b));
    for (const body of [[0xef, 0xbb, 0xbf, ...text("%PDF-1.4")], [0x20, ...text("%PDF-1.4")], [0x0a, ...text("%PDF-1.4")], text("junk%PDF-1.4"), text("%pdf-1.4"), text("%PDF"), text("%PDF1.4"), [0x4d, 0x5a, 0x90, 0x00, 0x03]]) {
      const r = await send({ pdf: enc(body) });
      expect([body.slice(0, 6), (r.replies[0] as { code?: string }).code]).toEqual([body.slice(0, 6), "bad_request"]);
    }
    expect(sent()).toEqual([]);
    expect((await send({ pdf: enc(text("%PDF-")) })).replies).toEqual([{ outcomes: [] }]);
  });

  test("hostile file names: NUL, controls, separators, dot-only, over-long and double-extension names are refused; Windows-reserved and dotted names are harmless and kept", async () => {
    fresh();
    const refuse = ["a\u0000b.pdf", "a\nb.pdf", "a\tb.pdf", "\u0085x.pdf", "\u007fx.pdf", "a/b.pdf", "a\\b.pdf", "../x.pdf", "..\\x.pdf", ".pdf", "  .pdf  ", ".PDF", "x.pdf\u0000", `${"x".repeat(197)}.pdf`, "resume.pdf.exe", "resume.pdf ."];
    for (const name of refuse) {
      const r = await send({ name });
      expect([name, (r.replies[0] as { code?: string }).code]).toEqual([name, "bad_request"]);
    }
    expect(sent()).toEqual([]);
    for (const name of ["CON.pdf", "NUL.pdf", "a..pdf", "..pdf", "résumé 日本語.PDF", `${"x".repeat(196)}.pdf`]) {
      tabSent.length = 0;
      await ask({ type: "frameReady" }, { tab: { id: 61 }, frameId: 0, url: "https://a.example/" });
      const r = await send({ name });
      expect([name, r.replies]).toEqual([name, [{ outcomes: [] }]]);
      expect((sent()[0]!.msg as { name: string }).name).toBe(name);
    }
  });
});

describe("adversarial: replaceBank bound", () => {
  const head = '{"version":1,"answers":{},"settings":{"captureSelfId":false,"disabledHosts":[]},"pad":"';
  const withUnits = (units: number, ch = " ") => head + ch.repeat(units - head.length - 2) + '"}';

  test("exactly MAX_BANK_BYTES UTF-16 units is parsed and accepted; one more is refused before parse", async () => {
    local = {};
    const at = withUnits(MAX_BANK_BYTES);
    expect(at.length).toBe(MAX_BANK_BYTES);
    // Past the size gate and parsed (the store behind it may be in either mode when other test files ran first).
    const r = await ask({ type: "replaceBank", json: at }, {});
    expect(r.async).toBe(true);
    expect(r.replies[0]).not.toEqual({ error: "malformed replaceBank message" });
    expect(r.replies[0]).not.toMatchObject({ error: expect.stringContaining("larger than") });
    expect(await ask({ type: "replaceBank", json: withUnits(MAX_BANK_BYTES + 1) }, {})).toEqual({ async: false, replies: [{ error: "malformed replaceBank message" }] });
  });
});
