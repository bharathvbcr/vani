// A frame whose message port has closed is not still writing. The shortcut must be able to send
// again while the tab is still open. A sender timeout must not be what opens the gate.
import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { FILL_BUSY } from "../src/frames";
import { isFillReply } from "../src/messages";

type Listener = (msg: unknown, sender: object, sendResponse: (r?: unknown) => void) => boolean;

const g = globalThis as Record<string, unknown>;
const savedGlobals = { chrome: g.chrome, browser: g.browser };
let listener: Listener | undefined;
const commands: Array<(command: string) => void> = [];
const removed: Array<(tabId: number) => void> = [];
const sent: Array<{ msg: unknown }> = [];
let active: Array<{ id?: number; url?: string }> = [{ id: 6, url: "https://a.example/" }];
let mode: "hang" | "closed" | "ok" = "ok";

beforeAll(async () => {
  g.browser = undefined;
  g.chrome = {
    runtime: {
      onMessage: { addListener(cb: Listener) { listener = cb; } },
      sendNativeMessage(_a: string, _m: object, cb: (r: unknown) => void) {
        const runtime = (g.chrome as { runtime: { lastError?: { message: string } } }).runtime;
        runtime.lastError = { message: "host not found" };
        cb(undefined);
        runtime.lastError = undefined;
      },
      connectNative() { throw new Error("no host"); },
      lastError: undefined,
    },
    storage: {
      local: { get: async () => ({}), set: async () => undefined, remove: async () => undefined },
      session: undefined,
    },
    tabs: {
      onUpdated: { addListener() {} },
      onRemoved: { addListener(cb: (tabId: number) => void) { removed.push(cb); } },
      query: async () => active,
      get: async (id: number) => ({ id, url: "https://a.example/" }),
      sendMessage: async (_tabId: number, msg: unknown) => {
        sent.push({ msg });
        if ((msg as { type?: string }).type !== "fill") return {};
        if (mode === "hang") return new Promise(() => undefined);
        if (mode === "closed") throw new Error("The message port closed before a response was received.");
        return { outcomes: [{ label: "Note", filled: true }] };
      },
    },
    action: undefined,
    scripting: { executeScript: async () => [] },
    commands: { onCommand: { addListener(cb: (command: string) => void) { commands.push(cb); } } },
  };
  await import("../src/background.ts?deadframe");
});

afterAll(() => {
  g.chrome = savedGlobals.chrome;
  g.browser = savedGlobals.browser;
});

async function ask(msg: unknown, sender: object = {}): Promise<unknown[]> {
  const replies: unknown[] = [];
  let done = (): void => undefined;
  const answered = new Promise<void>(r => { done = r; });
  listener!(msg, sender, r => { replies.push(r); done(); });
  await Promise.race([answered, new Promise(r => setTimeout(r, 2000))]);
  return replies;
}

const fills = () => sent.filter(s => (s.msg as { type?: string }).type === "fill");

describe("dead content script, tab still open", () => {
  test("a closed message port is not a live fill, so a later shortcut sends", async () => {
    expect(await ask({ type: "frameReady" }, { tab: { id: 6 }, frameId: 0, url: "https://a.example/" })).toEqual([{ ok: true }]);
    mode = "closed";
    sent.length = 0;
    for (const cb of commands) cb("fill-form");
    for (let i = 0; i < 30 && fills().length < 1; i++) await new Promise(r => setTimeout(r, 5));
    expect(fills().length).toBe(1);
    await new Promise(r => setTimeout(r, 20));
    for (const cb of commands) cb("fill-form");
    await new Promise(r => setTimeout(r, 30));
    expect(isFillReply({ outcomes: [] })).toBe(true);
    expect(isFillReply({ error: FILL_BUSY })).toBe(false);
    // The port is already closed and the tab is still open. A later shortcut has to send.
    expect(fills().length).toBe(2);
    for (const cb of removed) cb(6);
  });
});
