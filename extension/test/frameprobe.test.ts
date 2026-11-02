// The frame probe's deadline is the native call's only timer. A 2s miss must not leave a 5s timer
// that poisons the next call on the shared port, and a down host is a coded reply, never a throw.
import { afterAll, afterEach, beforeAll, describe, expect, jest, test } from "bun:test";
import type { NativePort } from "../src/nativeport";

class FakePort implements NativePort {
  posted: object[] = [];
  disconnected = false;
  private onMsg: ((m: unknown) => void)[] = [];
  private onDis: (() => void)[] = [];
  onMessage = { addListener: (cb: (m: unknown) => void) => { this.onMsg.push(cb); } };
  onDisconnect = { addListener: (cb: () => void) => { this.onDis.push(cb); } };
  postMessage(m: object) { this.posted.push(m); }
  disconnect() { this.disconnected = true; }
  say(m: unknown) { for (const cb of this.onMsg) cb(m); }
  hangUp() { for (const cb of this.onDis) cb(); }
}

const g = globalThis as Record<string, unknown>;
const saved = { chrome: g.chrome, browser: g.browser };

const runtime: {
  lastError?: { message: string };
  connectNative(host: string): NativePort;
  sendNativeMessage(app: string, message: object, cb: (response: unknown) => void): void;
  onMessage: { addListener(): void };
} = {
  lastError: undefined,
  connectNative: () => connectImpl(),
  sendNativeMessage: (app, message, cb) => sendImpl(app, message, cb),
  onMessage: { addListener() {} },
};

/** Every port the shared transport has opened. A call reuses the last one until it is poisoned. */
const opened: FakePort[] = [];
let connectImpl: () => NativePort = () => {
  const p = new FakePort();
  opened.push(p);
  return p;
};
let sendImpl: (app: string, message: object, cb: (response: unknown) => void) => void = (_app, _message, cb) => {
  runtime.lastError = { message: "host not found" };
  cb(undefined);
  runtime.lastError = undefined;
};

function track(p: Promise<unknown>) {
  const box: { settled: boolean; ok: boolean; v?: unknown; e?: unknown } = { settled: false, ok: false };
  void p.then(
    v => { box.settled = true; box.ok = true; box.v = v; },
    e => { box.settled = true; box.ok = false; box.e = e; },
  );
  return { box };
}

async function flush() {
  await Promise.resolve();
  await Promise.resolve();
}

const UNREACHABLE = { error: "the Vani Apply Mac app isn't reachable", code: "unreachable" };

let native: (msg: object, ms?: number) => Promise<unknown>;
let spawnTransport: (msg: object, timeoutMs?: number) => Promise<unknown>;

beforeAll(async () => {
  g.browser = undefined;
  g.chrome = {
    runtime,
    storage: {
      local: {
        get: async () => ({}),
        set: async () => undefined,
        remove: async () => undefined,
      },
      session: undefined,
    },
    tabs: {
      onUpdated: { addListener() {} },
      onRemoved: { addListener() {} },
      query: async () => [],
      get: async () => { throw new Error("no tab"); },
      sendMessage: async () => undefined,
    },
    action: undefined,
    commands: { onCommand: { addListener() {} } },
  };
  // A query keeps this copy off the module background.test.ts loads, so that file still registers its own listener.
  const bg = await import("../src/background.ts?frameprobe");
  native = bg.native;
  const store = await import("../src/store.ts?frameprobe");
  spawnTransport = store.spawnTransport;
});

afterAll(() => {
  g.chrome = saved.chrome;
  g.browser = saved.browser;
});

afterEach(() => {
  if (jest.isFakeTimers()) {
    jest.clearAllTimers();
    jest.useRealTimers();
  }
  connectImpl = () => {
    const p = new FakePort();
    opened.push(p);
    return p;
  };
  sendImpl = (_app, _message, cb) => {
    runtime.lastError = { message: "host not found" };
    cb(undefined);
    runtime.lastError = undefined;
  };
});

/** Close a port that has already answered. That is not a failed connect, so the backoff counter stays put. */
function dropAnsweredPort() {
  const port = opened[opened.length - 1];
  if (port && !port.disconnected) port.hangUp();
}

describe("native frame probe", () => {
  test("a 2000ms probe that does not settle is unreachable, and advancing to 5000ms does not poison the next call", async () => {
    const before = opened.length;
    jest.useFakeTimers();
    const probe = track(native({ op: "resumeInfo", key: "https://boards.example/jobs/1" }, 2_000));
    const first = opened[before];
    expect(first?.posted).toEqual([{ op: "resumeInfo", key: "https://boards.example/jobs/1" }]);
    expect(jest.getTimerCount()).toBe(1);
    jest.advanceTimersByTime(2_000);
    await flush();
    expect(probe.box).toEqual({ settled: true, ok: true, v: UNREACHABLE });
    expect(first?.disconnected).toBe(false);
    expect(jest.getTimerCount()).toBe(0);

    const next = track(native({ op: "get" }));
    expect(opened.length).toBe(before + 1);
    expect(first?.disconnected).toBe(false);
    expect(first?.posted).toEqual([
      { op: "resumeInfo", key: "https://boards.example/jobs/1" },
      { op: "get" },
    ]);
    expect(jest.getTimerCount()).toBe(1);
    jest.advanceTimersByTime(3_000);
    await flush();
    expect(next.box.settled).toBe(false);
    expect(first?.disconnected).toBe(false);
    first?.say({ ok: true, late: true });
    await flush();
    expect(next.box.settled).toBe(false);
    first?.say({ ok: true, rev: 1 });
    await flush();
    expect(next.box).toEqual({ settled: true, ok: true, v: { ok: true, rev: 1 } });
  });

  test("a probe that returns before 2000ms resolves", async () => {
    jest.useFakeTimers();
    const port = opened[opened.length - 1];
    expect(port?.disconnected).toBe(false);
    const call = track(native({ op: "ping" }, 2_000));
    expect(port?.posted.at(-1)).toEqual({ op: "ping" });
    port?.say({ ok: true, op: "ping" });
    await flush();
    expect(call.box).toEqual({ settled: true, ok: true, v: { ok: true, op: "ping" } });
    jest.advanceTimersByTime(5_000);
    await flush();
    expect(port?.disconnected).toBe(false);
    expect(jest.getTimerCount()).toBe(0);
  });

  test("a host that disconnects before any reply is unreachable, not a throw", async () => {
    dropAnsweredPort();
    connectImpl = () => {
      const p = new FakePort();
      opened.push(p);
      p.postMessage = m => { p.posted.push(m); p.hangUp(); };
      return p;
    };
    let threw = false;
    let write: unknown;
    let read: unknown;
    try {
      write = await native({ op: "put" }, 2_000);
      read = await native({ op: "resumeInfo", key: "https://boards.example/jobs/9" }, 2_000);
    } catch {
      threw = true;
    }
    expect(threw).toBe(false);
    expect(write).toEqual(UNREACHABLE);
    expect(read).toEqual(UNREACHABLE);
  });

  test("a host whose connect throws is unreachable, not a throw", async () => {
    // The disconnect above backed off the port. Wait it out so this call actually connects.
    await new Promise(r => setTimeout(r, 1_100));
    connectImpl = () => { throw new Error("no host"); };
    let threw = false;
    let reply: unknown;
    try {
      reply = await native({ op: "put" }, 2_000);
    } catch {
      threw = true;
    }
    expect(threw).toBe(false);
    expect(reply).toEqual(UNREACHABLE);
  });

  test("a per-message call uses its own deadline and the host never sees it", async () => {
    let sent: object | undefined;
    sendImpl = (_app, message, _cb) => { sent = message; };
    jest.useFakeTimers();
    const hanging = track(spawnTransport({ op: "jobsIngest", jobs: [] }, 40_000));
    jest.advanceTimersByTime(5_000);
    await flush();
    expect(hanging.box.settled).toBe(false);
    expect(sent).toEqual({ op: "jobsIngest", jobs: [] });
    jest.advanceTimersByTime(35_000);
    await flush();
    expect(hanging.box.ok).toBe(false);
    expect(String((hanging.box.e as Error).message)).toBe("timed out");

    sendImpl = (_app, _message, cb) => { cb({ queued: 1 }); };
    const quick = await spawnTransport({ op: "jobsIngest", jobs: [1] }, 40_000);
    expect(quick).toEqual({ queued: 1 });
    jest.advanceTimersByTime(40_000);
    await flush();
  });

  test("a queued 2000ms probe is not written, the same port's next ping succeeds, and advancing to 5000ms does not poison it", async () => {
    // The connect-throw above leaves the port backing off for 2s. This call has to reach the port.
    await new Promise(r => setTimeout(r, 2_500));
    jest.useFakeTimers();
    const before = opened.length;
    const blockers = [0, 1, 2, 3].map(n => track(native({ op: "get", n })));
    const probe = track(native({ op: "resumeInfo", key: "https://boards.example/jobs/queued" }, 2_000));
    const port = opened[before];
    expect(port).toBeDefined();
    expect(opened.length).toBe(before + 1);
    expect(port?.posted).toEqual([{ op: "get", n: 0 }, { op: "get", n: 1 }, { op: "get", n: 2 }, { op: "get", n: 3 }]);
    for (const m of port?.posted ?? []) {
      expect(m).not.toHaveProperty("timeout");
      expect(m).not.toHaveProperty("timeoutMs");
      expect(m).not.toHaveProperty("ms");
    }
    expect(jest.getTimerCount()).toBe(5);
    jest.advanceTimersByTime(2_000);
    await flush();
    expect(probe.box).toEqual({ settled: true, ok: true, v: UNREACHABLE });
    expect(port?.disconnected).toBe(false);
    expect(port?.posted).toHaveLength(4);
    expect(jest.getTimerCount()).toBe(4);
    for (const [n, blocker] of blockers.entries()) {
      expect(blocker.box.settled).toBe(false);
      port?.say({ ok: true, n });
      await flush();
      expect(blocker.box).toEqual({ settled: true, ok: true, v: { ok: true, n } });
    }
    expect(jest.getTimerCount()).toBe(0);
    expect(port?.disconnected).toBe(false);

    const ping = track(native({ op: "ping" }));
    expect(opened.length).toBe(before + 1);
    expect(port?.posted.at(-1)).toEqual({ op: "ping" });
    expect(port?.posted.at(-1)).not.toHaveProperty("timeout");
    expect(port?.posted.at(-1)).not.toHaveProperty("timeoutMs");
    expect(port?.posted.at(-1)).not.toHaveProperty("ms");
    jest.advanceTimersByTime(3_000);
    await flush();
    expect(ping.box.settled).toBe(false);
    expect(port?.disconnected).toBe(false);
    expect(jest.getTimerCount()).toBe(1);
    port?.say({ ok: true, op: "ping" });
    await flush();
    expect(ping.box).toEqual({ settled: true, ok: true, v: { ok: true, op: "ping" } });
    expect(port?.disconnected).toBe(false);
    jest.advanceTimersByTime(5_000);
    await flush();
    expect(port?.disconnected).toBe(false);
    expect(jest.getTimerCount()).toBe(0);
  });

  test("jobsIngest given 40000 does not settle at 5000; a reply at 39999 resolves, and silence is unreachable only at 40000", async () => {
    jest.useFakeTimers();
    let sent: object | undefined;
    sendImpl = (_app, message, cb) => {
      sent = message;
      setTimeout(() => cb({ queued: 3 }), 39_999);
    };
    let threw = false;
    const quickP = native({ op: "jobsIngest", jobs: [] }, 40_000);
    const quick = track(quickP);
    expect(sent).toEqual({ op: "jobsIngest", jobs: [] });
    expect(sent).not.toHaveProperty("timeout");
    expect(sent).not.toHaveProperty("timeoutMs");
    expect(sent).not.toHaveProperty("ms");
    jest.advanceTimersByTime(5_000);
    await flush();
    expect(quick.box.settled).toBe(false);
    jest.advanceTimersByTime(5_000);
    await flush();
    expect(quick.box.settled).toBe(false);
    jest.advanceTimersByTime(29_998);
    await flush();
    expect(quick.box.settled).toBe(false);
    jest.advanceTimersByTime(1);
    await flush();
    try {
      expect(await quickP).toEqual({ queued: 3 });
    } catch {
      threw = true;
    }
    expect(threw).toBe(false);
    expect(quick.box).toEqual({ settled: true, ok: true, v: { queued: 3 } });
    jest.advanceTimersByTime(1);
    await flush();
    expect(quick.box).toEqual({ settled: true, ok: true, v: { queued: 3 } });

    sendImpl = (_app, message, _cb) => { sent = message; };
    const hanging = track(native({ op: "draft" }, 40_000));
    expect(sent).toEqual({ op: "draft" });
    expect(sent).not.toHaveProperty("timeout");
    expect(sent).not.toHaveProperty("timeoutMs");
    expect(sent).not.toHaveProperty("ms");
    jest.advanceTimersByTime(39_999);
    await flush();
    expect(hanging.box.settled).toBe(false);
    jest.advanceTimersByTime(1);
    await flush();
    expect(hanging.box).toEqual({ settled: true, ok: true, v: UNREACHABLE });

    const rejected = track(spawnTransport({ op: "resumeOpen" }, 40_000));
    jest.advanceTimersByTime(39_999);
    await flush();
    expect(rejected.box.settled).toBe(false);
    jest.advanceTimersByTime(1);
    await flush();
    expect(rejected.box.ok).toBe(false);
    expect(String((rejected.box.e as Error).message)).toBe("timed out");
  });

  test("a string, null, or array reply does not throw out of native; unreachable is only a transport rejection", async () => {
    await new Promise(r => setTimeout(r, 2_500));
    const replies: unknown[] = ["host said no", null, ["not", "an", "object"]];
    for (const reply of replies) {
      const before = opened.length;
      const pending = native({ op: "ping" });
      const port = opened[opened.length - 1];
      expect(port).toBeDefined();
      expect(opened.length === before || opened.length === before + 1).toBe(true);
      expect(port?.posted.at(-1)).toEqual({ op: "ping" });
      expect(port?.posted.at(-1)).not.toHaveProperty("timeout");
      expect(port?.posted.at(-1)).not.toHaveProperty("timeoutMs");
      expect(port?.posted.at(-1)).not.toHaveProperty("ms");
      port?.say(reply);
      let threw = false;
      let got: unknown;
      try {
        got = await pending;
      } catch {
        threw = true;
      }
      expect(threw).toBe(false);
      expect(got).toEqual(reply);
    }
    const before = opened.length;
    const pending = native({ op: "put" }, 2_000);
    const port = opened[opened.length - 1];
    expect(opened.length === before || opened.length === before + 1).toBe(true);
    port?.hangUp();
    let threw = false;
    let got: unknown;
    try {
      got = await pending;
    } catch {
      threw = true;
    }
    expect(threw).toBe(false);
    expect(got).toEqual(UNREACHABLE);
  });
});
