// Messages between the popup (or the keyboard command) and the content script
// in each frame of a tab, against a fake chrome that answers like Chrome 155
// does (test/live.ts checks the same things in the real browser):
// - a message sent to the tab without a frameId is delivered to every frame with
//   a listener and resolves with whichever frame replies first;
// - scripting.executeScript lists only frames the extension may script, never a
//   cross-origin iframe whose host is only in content_scripts.matches;
// - a message to a frame with no listener fails "Receiving end does not exist".
import { afterEach, describe, expect, test } from "bun:test";
import {
  ACCEPT, type FrameStore, MAX_FRAMES_PER_TAB, MAX_TABS, contentListener, createFrameRegistry, mergeFill, notAnswered, sumCaptures, toFrames,
} from "../src/frames";
import { type CaptureReply, type FillReply, type ScanReply, SENDER_POLICY, TO_BACKGROUND, TO_CONTENT } from "../src/messages";
import { MAX_RESUME_BYTES, toBase64 } from "../src/resume";

const g = globalThis as Record<string, unknown>;
const saved = { chrome: g.chrome, window: g.window };
afterEach(() => { g.chrome = saved.chrome; g.window = saved.window; });

const never = <T>(): Promise<T> => new Promise<T>(() => undefined);
const later = <T>(ms: number, v: T): Promise<T> => new Promise<T>(r => setTimeout(() => r(v), ms));

/** `script`: a content script listener runs there. `scriptable`: executeScript may run there (default true). */
interface FakeFrame { id: number; script: boolean; scriptable?: boolean; reply: (msg: unknown) => Promise<unknown> }

function fakeChrome(frames: FakeFrame[]) {
  const calls = { injected: 0, sent: [] as Array<number | undefined> };
  const scriptable = () => frames.filter(f => f.scriptable !== false);
  g.chrome = {
    tabs: {
      query: async () => [{ id: 42 }],
      sendMessage: (tabId: number, msg: unknown, o?: { frameId?: number }) => {
        expect(tabId).toBe(42);
        calls.sent.push(o?.frameId);
        const live = frames.filter(f => f.script);
        const nobody = () => Promise.reject(new Error("Could not establish connection. Receiving end does not exist."));
        if (o?.frameId === undefined) return live.length ? Promise.race(live.map(f => f.reply(msg))) : nobody();
        const f = live.find(x => x.id === o.frameId);
        return f ? f.reply(msg) : nobody();
      },
    },
    scripting: {
      executeScript: async (d: { target: { tabId: number; allFrames?: boolean }; files?: string[]; func?: () => unknown }) => {
        expect(d.target).toEqual({ tabId: 42, allFrames: true });
        if (d.files) {
          calls.injected++;
          for (const f of scriptable()) f.script = true;
          return scriptable().map(f => ({ frameId: f.id }));
        }
        // Run the probe in each scriptable frame's own window.
        return scriptable().map(f => {
          g.window = { __vaniApply: f.script ? true : undefined };
          return { frameId: f.id, result: d.func!() };
        });
      },
    },
  };
  return calls;
}

const none = { registered: async () => [] };
const reg = (ids: number[]) => ({ registered: async (tabId: number) => { expect(tabId).toBe(42); return ids; } });
const filled = (label: string) => ({ label, filled: true, key: label.toLowerCase() });
const EMPTY_FILL: FillReply = { outcomes: [] };

describe("fill and save across frames", () => {
  test("a form whose fields are only in an iframe is reported, not the empty top frame's reply", async () => {
    fakeChrome([
      { id: 0, script: true, reply: async () => EMPTY_FILL },
      { id: 7, script: true, reply: () => later(20, { outcomes: [filled("Email"), filled("Phone"), { label: "Essay", filled: false, reason: "no_match" }] }) },
    ]);
    const r = await toFrames<FillReply>({ type: "fill" }, none);
    const outcomes = mergeFill(r);
    expect(outcomes.filter(o => o.filled).map(o => o.label)).toEqual(["Email", "Phone"]);
    expect(outcomes.length).toBe(3);
    expect(notAnswered(r)).toBe("");
  });

  test("an iframe executeScript cannot list (a manifest match only) is asked because its content script registered", async () => {
    const calls = fakeChrome([
      { id: 0, script: false, reply: async () => EMPTY_FILL },
      { id: 5, script: true, scriptable: false, reply: () => later(5, { outcomes: [filled("First Name"), filled("Email")] }) },
    ]);
    const r = await toFrames<FillReply>({ type: "fill" }, reg([5]));
    expect(mergeFill(r).map(o => o.label)).toEqual(["First Name", "Email"]);
    expect(r).toMatchObject({ asked: 1, failed: [], gone: [] });
    expect(calls.injected).toBe(0);
  });

  test("a registered frame that is also probed is asked once", async () => {
    const calls = fakeChrome([{ id: 0, script: true, reply: async () => ({ outcomes: [filled("Name")] }) }]);
    const r = await toFrames<FillReply>({ type: "fill" }, reg([0, 0]));
    expect(calls.sent).toEqual([0]);
    expect(mergeFill(r).length).toBe(1);
  });

  test("a registered frame with no listener any more is gone: not asked, not failed, not 'did not answer'", async () => {
    fakeChrome([
      { id: 0, script: true, reply: async () => ({ outcomes: [filled("Name")] }) },
      { id: 12, script: false, scriptable: false, reply: () => never() },
    ]);
    const r = await toFrames<FillReply>({ type: "fill" }, reg([12, 0]));
    expect(r).toMatchObject({ asked: 1, failed: [], gone: [12] });
    expect(notAnswered(r)).toBe("");
  });

  test("when every listed frame is gone, the content script is injected and the frames asked again", async () => {
    const calls = fakeChrome([
      { id: 0, script: false, reply: async () => ({ outcomes: [filled("City")] }) },
      { id: 12, script: false, scriptable: false, reply: () => never() },
    ]);
    const r = await toFrames<FillReply>({ type: "fill" }, reg([12]));
    expect(calls.injected).toBe(1);
    expect(mergeFill(r).map(o => o.label)).toEqual(["City"]);
    expect(r.gone).toEqual([12]);
  });

  test("the background's frame list failing or hanging still asks the frames the probe found", async () => {
    fakeChrome([{ id: 0, script: true, reply: async () => ({ outcomes: [filled("Name")] }) }]);
    const broken = await toFrames<FillReply>({ type: "fill" }, { registered: async () => { throw new Error("worker gone"); } });
    expect(mergeFill(broken).length).toBe(1);
    const hung = await toFrames<FillReply>({ type: "fill" }, { registered: () => never(), timeoutMs: 50 });
    expect(mergeFill(hung).length).toBe(1);
  });

  test("a frame that never answers does not hang the popup past the timeout, and the others still count", async () => {
    const calls = fakeChrome([
      { id: 0, script: true, reply: async () => EMPTY_FILL },
      { id: 3, script: true, reply: () => later(5, { outcomes: [filled("Name")] }) },
      { id: 9, script: true, reply: () => never() },
    ]);
    const start = Date.now();
    const r = await toFrames<FillReply>({ type: "fill" }, { ...none, timeoutMs: 80 });
    expect(Date.now() - start).toBeLessThan(1000);
    expect(mergeFill(r).map(o => o.label)).toEqual(["Name"]);
    expect(r.asked).toBe(3);
    expect(r.failed.map(f => f.frameId)).toEqual([9]);
    expect(notAnswered(r)).toContain("1 of 3 frames");
    expect(calls.sent.sort()).toEqual([0, 3, 9]);
  });

  test("a same-origin child frame (no listener of its own) is not asked, so it is not reported as silent", async () => {
    const calls = fakeChrome([
      { id: 0, script: true, reply: async () => ({ outcomes: [filled("Name")] }) },
      { id: 4, script: false, reply: () => never() },
    ]);
    const r = await toFrames<FillReply>({ type: "fill" }, { ...none, timeoutMs: 80 });
    expect(calls.sent).toEqual([0]);
    expect(r.asked).toBe(1);
    expect(calls.injected).toBe(0);
  });

  test("a page with no content script anywhere gets it injected once, then every frame is asked", async () => {
    const calls = fakeChrome([
      { id: 0, script: false, reply: async () => EMPTY_FILL },
      { id: 2, script: false, reply: async () => ({ outcomes: [filled("City")] }) },
    ]);
    const r = await toFrames<FillReply>({ type: "fill" }, none);
    expect(calls.injected).toBe(1);
    expect(mergeFill(r).map(o => o.label)).toEqual(["City"]);
  });

  test("a page Vani cannot start on is an error, not an empty result", async () => {
    fakeChrome([{ id: 0, script: false, scriptable: false, reply: () => never() }]);
    await expect(toFrames<FillReply>({ type: "fill" }, none)).rejects.toThrow("Vani could not start on this page");
  });

  test("an error reply or a malformed reply is a frame that failed, never a frame with nothing to fill", async () => {
    fakeChrome([
      { id: 0, script: true, reply: async () => ({ error: "storage refused" }) },
      { id: 1, script: true, reply: async () => undefined },
      { id: 2, script: true, reply: async () => ({ outcomes: "nope" }) },
      { id: 3, script: true, reply: async () => ({ outcomes: [filled("Name")] }) },
    ]);
    const r = await toFrames<FillReply>({ type: "fill" }, none);
    expect(mergeFill(r).map(o => o.label)).toEqual(["Name"]);
    expect(r.failed.map(f => f.frameId)).toEqual([0, 1, 2]);
    expect(r.failed[0]!.error).toBe("storage refused");
  });

  test("save adds every frame's counts", async () => {
    const c = (added: number, unchanged: number): CaptureReply => ({ added, updated: 1, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 2, unchanged });
    fakeChrome([
      { id: 0, script: true, reply: async () => c(0, 0) },
      { id: 5, script: true, scriptable: false, reply: () => later(10, c(4, 3)) },
    ]);
    const r = await toFrames<CaptureReply>({ type: "capture" }, reg([5]));
    expect(sumCaptures(r)).toEqual({ added: 4, updated: 2, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 4, unchanged: 3 });
  });

  test("no active tab is an error, not an empty result", async () => {
    fakeChrome([]);
    (g.chrome as { tabs: { query: () => Promise<unknown[]> } }).tabs.query = async () => [];
    await expect(toFrames<FillReply>({ type: "fill" }, none)).rejects.toThrow("no active tab");
  });
});

describe("attachResume and tabId across frames", () => {
  const attach = { type: "attachResume", name: "Resume_Synthetic.pdf", pdf: "JVBERi0=", pickedAt: "" } as const;
  /** A chrome whose active tab is 42 and whose tabs reply as `reply` says; records every tab and frame asked. */
  function twoTabs(reply: (tabId: number, frameId: number) => unknown) {
    const asked: Array<[number, number | undefined]> = [];
    const queried = { n: 0 };
    g.chrome = {
      tabs: {
        query: async () => { queried.n++; return [{ id: 42 }]; },
        sendMessage: async (tabId: number, _msg: unknown, o?: { frameId?: number }) => { asked.push([tabId, o?.frameId]); return reply(tabId, o?.frameId ?? 0); },
      },
      scripting: { executeScript: async () => [] },
    };
    return { asked, queried };
  }

  test("attachResume is accepted by its own check: a reply with outcomes answers, anything else is a failed frame", async () => {
    twoTabs((_t, f) => (f === 3 ? { nope: true } : { outcomes: [{ label: "Resume", filled: true }] }));
    const r = await toFrames<FillReply>(attach, { registered: async () => [0, 3], tabId: 42 });
    expect(r.answered.map(a => a.frameId)).toEqual([0]);
    expect(r.failed).toEqual([{ frameId: 3, error: "malformed reply" }]);
    expect(mergeFill(r).map(o => o.label)).toEqual(["Resume"]);
  });

  test("tabId asks that tab's frames and never looks up the active tab", async () => {
    const { asked, queried } = twoTabs((t, f) => ({ outcomes: [{ label: `tab ${t} frame ${f}`, filled: true }] }));
    const r = await toFrames<FillReply>(attach, { registered: async tabId => (tabId === 7 ? [0, 4] : [9]), tabId: 7 });
    expect(asked).toEqual([[7, 0], [7, 4]]);
    expect(queried.n).toBe(0);
    expect(mergeFill(r).map(o => o.label)).toEqual(["tab 7 frame 0", "tab 7 frame 4"]);
  });

  test("without tabId the active tab is asked, as before", async () => {
    const { asked, queried } = twoTabs(() => ({ outcomes: [] }));
    await toFrames<FillReply>(attach, { registered: async () => [0] });
    expect(asked).toEqual([[42, 0]]);
    expect(queried.n).toBe(1);
  });

  test("a tabId whose tab has gone (no listener anywhere) is an error, not an empty success", async () => {
    twoTabs(() => { throw new Error("Could not establish connection. Receiving end does not exist."); });
    await expect(toFrames<FillReply>(attach, { registered: async () => [0], tabId: 7 })).rejects.toThrow("Vani could not start on this page");
  });

  test("a frame that throws is reported as failed, with its error", async () => {
    twoTabs(() => { throw new Error("boom"); });
    const r = await toFrames<FillReply>(attach, { registered: async () => [0], tabId: 7 });
    expect(r.failed).toEqual([{ frameId: 0, error: "boom" }]);
    expect(mergeFill(r)).toEqual([]);
  });

  test("every ToContent type has an ACCEPT check, and the shape tables list the same types", () => {
    expect(Object.keys(ACCEPT).sort()).toEqual(Object.keys(TO_CONTENT).sort());
    expect(Object.keys(SENDER_POLICY).sort()).toEqual(Object.keys(TO_BACKGROUND).sort());
  });
});

describe("frame registry", () => {
  test("frames of a tab register, are listed newest last, and are forgotten with the tab", async () => {
    const r = createFrameRegistry();
    await r.add(7, 5);
    await r.add(7, 0);
    await r.add(7, 5);
    await r.add(8, 3);
    expect(await r.list(7)).toEqual([0, 5]);
    expect(await r.list(8)).toEqual([3]);
    await r.forget(7);
    expect(await r.list(7)).toEqual([]);
    expect(await r.list(8)).toEqual([3]);
  });

  test("the top frame registering (the popup injecting into a loaded page) keeps the tab's iframes", async () => {
    const r = createFrameRegistry();
    await r.add(7, 5);
    await r.add(7, 0);
    expect(await r.list(7)).toEqual([5, 0]);
  });

  test("frames of one page registering at once are all kept", async () => {
    let stored: Awaited<ReturnType<FrameStore["load"]>> = {};
    const slow: FrameStore = { load: async () => { await later(2, 0); return structuredClone(stored); }, save: async v => { await later(1, 0); stored = structuredClone(v); } };
    const r = createFrameRegistry({ store: slow });
    await Promise.all([1, 2, 3, 4, 5, 6].map(f => r.add(9, f)));
    expect((await r.list(9)).sort()).toEqual([1, 2, 3, 4, 5, 6]);
  });

  test("bounded: the newest frames per tab and the most recent tabs are kept", async () => {
    let t = 0;
    const r = createFrameRegistry({ now: () => ++t });
    for (let f = 1; f <= MAX_FRAMES_PER_TAB + 5; f++) await r.add(1, f);
    expect(await r.list(1)).toEqual(Array.from({ length: MAX_FRAMES_PER_TAB }, (_, i) => i + 6));
    for (let tab = 100; tab < 100 + MAX_TABS; tab++) await r.add(tab, 1);
    expect(await r.list(1)).toEqual([]);
    expect(await r.list(100 + MAX_TABS - 1)).toEqual([1]);
  });

  test("a frame's page is the URL it was given, and a script URL or an over-long one is not kept", async () => {
    const r = createFrameRegistry();
    const job = "https://boards.greenhouse.io/acme/jobs/2";
    await r.add(7, 4, "https://boards.greenhouse.io", job);
    await r.add(7, 0, "https://careers.acme.com", "https://careers.acme.com/roles/1");
    expect(await r.pages(7)).toEqual([job, "https://careers.acme.com/roles/1"]);
    await r.add(7, 4, "https://evil.example", "javascript:alert(1)");
    expect(await r.pages(7)).toEqual(["https://careers.acme.com/roles/1"]);
    await r.add(7, 5, "https://evil.example", `https://evil.example/${"a".repeat(2100)}`);
    expect(await r.pages(7)).toEqual(["https://careers.acme.com/roles/1"]);
    await r.forget(7);
    expect(await r.pages(7)).toEqual([]);
  });

  test("a stored page that is not http is dropped on read", async () => {
    const stored = { 4: { frames: [1], at: 1, urls: { 1: "javascript:alert(1)", 2: "https://boards.greenhouse.io/acme/jobs/2" } } };
    const r = createFrameRegistry({ store: { load: async () => stored, save: async () => undefined } });
    expect(await r.pages(4)).toEqual([]);
  });

  test("bad ids are ignored, never stored", async () => {
    const r = createFrameRegistry();
    for (const [tab, f] of [[-1, 1], [1.5, 1], [Number.NaN, 1], [3, -2], [3, 0.5], [3, Number.POSITIVE_INFINITY]] as Array<[number, number]>) await r.add(tab, f);
    expect(await r.list(3)).toEqual([]);
    expect(await r.list(-1)).toEqual([]);
  });

  test("storage that fails keeps the worker's own copy; malformed stored rows are dropped", async () => {
    let fail = false;
    let stored: unknown = { 4: { frames: [1, "x", -3, 2], at: 1 }, 5: "junk", 6: { frames: "no", at: 1 } };
    const store = { load: async () => { if (fail) throw new Error("gone"); return stored as Awaited<ReturnType<FrameStore["load"]>>; }, save: async (v: unknown) => { if (fail) throw new Error("gone"); stored = v; } };
    const r = createFrameRegistry({ store });
    expect(await r.list(4)).toEqual([1, 2]);
    expect(await r.list(5)).toEqual([]);
    await r.add(4, 9);
    fail = true;
    expect(await r.list(4)).toEqual([1, 2, 9]);
    await r.add(4, 10);
    expect(await r.list(4)).toEqual([1, 2, 9, 10]);
  });
});

describe("content script replies", () => {
  const NOTHING: CaptureReply = { added: 0, updated: 0, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 0, unchanged: 0 };
  const NO_FIELDS: ScanReply = { fields: 0, matched: 0, host: "embed.example", disabled: false };
  const subframe = (over: Partial<Parameters<typeof contentListener>[0]> = {}) => contentListener({
    scan: async () => NO_FIELDS, fill: async () => EMPTY_FILL, capture: async () => NOTHING, appliedShow: null, ...over,
  });

  async function ask(listener: ReturnType<typeof contentListener>, msg: unknown) {
    const replies: unknown[] = [];
    const async = listener(msg, {}, r => replies.push(r));
    await later(5, undefined);
    return { async, replies };
  }

  test("a sub-frame with nothing to fill, save or scan still answers, so its message port is closed", async () => {
    const l = subframe();
    expect(await ask(l, { type: "fill" })).toEqual({ async: true, replies: [EMPTY_FILL] });
    expect(await ask(l, { type: "capture" })).toEqual({ async: true, replies: [NOTHING] });
    expect(await ask(l, { type: "scan" })).toEqual({ async: true, replies: [NO_FIELDS] });
  });

  test("a failing handler answers with the error, once", async () => {
    const l = subframe({ fill: async () => { throw new Error("bank unreadable"); } });
    expect(await ask(l, { type: "fill" })).toEqual({ async: true, replies: [{ error: "bank unreadable" }] });
    const sync = subframe({ capture: () => { throw new Error("boom"); } });
    expect(await ask(sync, { type: "capture" })).toEqual({ async: true, replies: [{ error: "boom" }] });
  });

  test("fill passes its options through", async () => {
    let seen: unknown[] = [];
    const l = subframe({ fill: async (o, t) => { seen = [o, t]; return EMPTY_FILL; } });
    await ask(l, { type: "fill", overwrite: true, typeahead: false });
    expect(seen).toEqual([true, false]);
  });

  test("resumeNoted shows a checked file name and refuses a disguised one", async () => {
    const names: string[] = [];
    const l = subframe({ resumeNoted: name => names.push(name) });
    expect(await ask(l, { type: "resumeNoted", name: "Resume_Acme.pdf" })).toEqual({ async: false, replies: [{ shown: true }] });
    expect(names).toEqual(["Resume_Acme.pdf"]);
    expect(await ask(l, { type: "resumeNoted", name: "Resume_\u202Efdp.pdf" })).toEqual({ async: false, replies: [{ shown: false }] });
    expect(await ask(l, { type: "resumeNoted", name: "../Resume_Acme.pdf" })).toEqual({ async: false, replies: [{ shown: false }] });
    expect(names).toEqual(["Resume_Acme.pdf"]);
    expect(await ask(subframe(), { type: "resumeNoted", name: "Resume_Acme.pdf" })).toEqual({ async: false, replies: [{ shown: false }] });
  });

  test("appliedShow: a sub-frame says it cannot show the question, the top frame answers after showing it", async () => {
    expect(await ask(subframe(), { type: "appliedShow" })).toEqual({ async: false, replies: [{ shown: false }] });
    let shown = 0;
    const top = subframe({ appliedShow: async () => { shown++; } });
    expect(await ask(top, { type: "appliedShow" })).toEqual({ async: true, replies: [{ shown: true }] });
    expect(shown).toBe(1);
    const broken = subframe({ appliedShow: async () => { throw new Error("x"); } });
    expect(await ask(broken, { type: "appliedShow" })).toEqual({ async: true, replies: [{ shown: false }] });
  });

  test("attachResume passes resume details through to the sub-frame handler", async () => {
    let received: unknown[] = [];
    const l = subframe({
      attachResume: async (name, pdf, pickedAt) => {
        received = [name, pdf, pickedAt];
        return { outcomes: [{ label: "Resume", filled: true }] };
      },
    });
    const reply = await ask(l, { type: "attachResume", name: "test.pdf", pdf: "JVBERi0=", pickedAt: "2026-10-19" });
    expect(received).toEqual(["test.pdf", "JVBERi0=", "2026-10-19"]);
    expect(reply.replies).toEqual([{ outcomes: [{ label: "Resume", filled: true }] }]);
  });

  test("attachResume refuses a bad name, a non-PDF or an over-cap file with an error, and never calls the handler", async () => {
    let ran = 0;
    const l = subframe({ attachResume: async () => { ran++; return { outcomes: [] }; } });
    const pdf = (n: number, sig = "%PDF-") => { const b = new Uint8Array(n).fill(0x20); b.set(new TextEncoder().encode(sig).subarray(0, n)); return toBase64(b); };
    const good = { type: "attachResume", name: "Resume_Synthetic.pdf", pdf: pdf(32), pickedAt: "" };
    for (const bad of [
      { name: "" }, { name: "a/b.pdf" }, { name: "..\\x.pdf" }, { name: "x.txt" }, { pdf: pdf(32, "<html") }, { pdf: "!!!" }, { pdf: pdf(MAX_RESUME_BYTES + 1) },
    ]) {
      const r = await ask(l, { ...good, ...bad });
      expect(r.async).toBe(false);
      expect(r.replies.length).toBe(1);
      expect((r.replies[0] as { error?: string }).error).toBeTruthy();
    }
    expect(ran).toBe(0);
    expect(await ask(l, { ...good, pdf: pdf(MAX_RESUME_BYTES) })).toEqual({ async: true, replies: [{ outcomes: [] }] });
    expect(ran).toBe(1);
  });

  test("draftField starts in the frame that has the handler, and a bad label never reaches it", async () => {
    let seen: unknown[] = [];
    const l = subframe({ draftField: (label, ordinal) => { seen = [label, ordinal]; return { ok: true }; } });
    expect(await ask(l, { type: "draftField", label: "Why us?" })).toEqual({ async: true, replies: [{ ok: true }] });
    expect(seen).toEqual(["Why us?", undefined]);
    expect(await ask(l, { type: "draftField", label: "Why us?", ordinal: 2 })).toEqual({ async: true, replies: [{ ok: true }] });
    expect(seen).toEqual(["Why us?", 2]);
    for (const label of [undefined, "", "x".repeat(1001)]) {
      const r = await ask(l, { type: "draftField", label });
      expect(r).toEqual({ async: false, replies: [{ error: "malformed draftField message" }] });
    }
    for (const ordinal of [1.5, -1, 10001]) {
      const r = await ask(l, { type: "draftField", label: "Why us?", ordinal });
      expect(r).toEqual({ async: false, replies: [{ error: "malformed draftField message" }] });
    }
    expect(seen).toEqual(["Why us?", 2]);
    const bare = subframe();
    expect(await ask(bare, { type: "draftField", label: "Why us?" })).toEqual({ async: true, replies: [{ ok: false, error: "This frame cannot draft." }] });
  });

  test("insertTailored returns the handler's result, including a refusal", async () => {
    const l = subframe({ insertTailored: (_label, _text, ordinal, overwrite) => ({ ok: false, error: "no", reason: ordinal === 3 && overwrite ? "has_value" : undefined }) });
    expect(await ask(l, { type: "insertTailored", label: "Why us?", text: "hello", ordinal: 3, overwrite: true })).toEqual({
      async: true, replies: [{ ok: false, error: "no", reason: "has_value" }],
    });
    const bare = subframe();
    expect(await ask(bare, { type: "insertTailored", label: "Why us?", text: "hello" })).toEqual({
      async: true, replies: [{ ok: false, error: "This frame cannot insert." }],
    });
    expect(await ask(l, { type: "insertTailored", label: "Why us?", text: "hello", ordinal: 1.5 })).toEqual({
      async: false, replies: [{ error: "malformed insertTailored message" }],
    });
  });

  test("a message that is not one of ours is answered with an error and runs nothing", async () => {
    let ran = 0;
    const l = subframe({ fill: async () => { ran++; return EMPTY_FILL; }, scan: async () => { ran++; return NO_FIELDS; } });
    for (const m of [null, undefined, "fill", 7, [], {}, { type: 5 }, { type: "delete-everything" }, { type: "__proto__" }]) {
      expect(await ask(l, m)).toEqual({ async: false, replies: [{ error: "unknown message type" }] });
    }
    expect(ran).toBe(0);
  });
});
