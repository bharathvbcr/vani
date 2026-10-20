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
    for (const m of [{ type: "scan" }, { type: "fill" }, { type: "fill", overwrite: true, typeahead: false }, { type: "capture" }, { type: "clearMarks" }, { type: "appliedShow" }]) {
      expect(checkToContent(m)).toEqual({ msg: m as never });
    }
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
        for (const a of m.upsert as Row[]) host.answers[a.key] = a;
        for (const k of m.delete as string[]) delete host.answers[k];
        host.rev++;
        return { rev: host.rev, droppedSecret: 0 };
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
    expect((await ask({ type: "filled", keys: ["city", "city", "name"] })).replies).toEqual([{ ok: true }]);
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
    expect((await ask({ type: "filled", keys: ["city"] })).replies).toEqual([{ ok: true }]);
    expect(ops().filter(o => o !== "log")).toEqual(["patch", "get", "patch"]);
    expect(host.answers.city!.fills).toBe(11);
  });

  test("two conflicts in a row: the loss is logged with counts only and the page gets a reply, not a throw", async () => {
    await reset();
    await ask({ type: "getBank" });
    host.calls.length = 0;
    host.conflicts = 2;
    const r = await ask({ type: "filled", keys: ["city", "zip"] });
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
    const r = await ask({ type: "filled", keys: ["city"] });
    expect(r.replies).toEqual([{ ok: false, error: "fill counts were not saved" }]);
    expect(host.answers.city!.fills).toBe(2);
  });

  test("a key the bank no longer holds is not invented", async () => {
    await reset();
    expect((await ask({ type: "filled", keys: ["deleted-meanwhile"] })).replies).toEqual([{ ok: true }]);
    expect(ops().includes("patch")).toBe(false);
  });
});
