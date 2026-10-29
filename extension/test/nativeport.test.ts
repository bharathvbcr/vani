import { describe, expect, jest, test } from "bun:test";
import { type NativePort, PortError, createPortTransport } from "../src/nativeport";

/** A native port the test drives by hand: it records what was posted and says what the host says. */
class FakePort implements NativePort {
  posted: { op: string; n?: number }[] = [];
  disconnected = false;
  answered = 0;
  private onMsg: ((m: unknown) => void)[] = [];
  private onDis: (() => void)[] = [];
  onMessage = { addListener: (cb: (m: unknown) => void) => { this.onMsg.push(cb); } };
  onDisconnect = { addListener: (cb: () => void) => { this.onDis.push(cb); } };
  postMessage(m: object) { this.posted.push(m as { op: string; n?: number }); }
  disconnect() { this.disconnected = true; }
  /** The host sends one frame. */
  say(m: unknown) { for (const cb of this.onMsg) cb(m); }
  /** The host answers the oldest request it has not answered, echoing its n, as the serial host does. */
  answerNext() { const r = this.posted[this.answered++]; this.say({ ok: true, n: r.n, op: r.op }); }
  hangUp() { for (const cb of this.onDis) cb(); }
}

function rig(o: { callTimeoutMs?: number; maxInFlight?: number; maxQueued?: number; backoff?: number[]; onConnect?: (p: FakePort, i: number) => void } = {}) {
  const ports: FakePort[] = [];
  const spawned: { op: string; n?: number }[] = [];
  let clock = 0;
  const t = createPortTransport({
    connect: () => {
      const p = new FakePort();
      ports.push(p);
      o.onConnect?.(p, ports.length - 1);
      return p;
    },
    spawn: async m => { spawned.push(m as { op: string }); return { via: "spawn" }; },
    callTimeoutMs: o.callTimeoutMs ?? 5_000,
    maxInFlight: o.maxInFlight,
    maxQueued: o.maxQueued,
    backoff: o.backoff ?? [1000, 2000, 4000],
    now: () => clock,
  });
  return { t, ports, spawned, advance: (ms: number) => { clock += ms; } };
}

const settle = () => new Promise<void>(r => setTimeout(r, 0));
const outcome = (p: Promise<unknown>) => p.then(v => ({ ok: true as const, v }), (e: Error) => ({ ok: false as const, e }));

describe("port transport: ordering and limits", () => {
  test("replies are matched to calls in FIFO order across 10 interleaved calls", async () => {
    const { t, ports } = rig();
    const calls = Array.from({ length: 10 }, (_, n) => t({ op: "get", n }));
    const results = calls.map(c => c as Promise<{ n: number }>);
    // The host answers whenever it likes; each answer goes to the oldest call still waiting.
    for (let i = 0; i < 10; i++) {
      await settle();
      ports[0].answerNext();
    }
    expect((await Promise.all(results)).map(r => r.n)).toEqual([0, 1, 2, 3, 4, 5, 6, 7, 8, 9]);
    expect(ports.length).toBe(1); // one process for all ten
  });

  test("the 5th concurrent call waits and is sent when one completes", async () => {
    const { t, ports } = rig();
    const calls = [0, 1, 2, 3, 4].map(n => t({ op: "get", n }));
    await settle();
    expect(ports[0].posted.map(m => m.n)).toEqual([0, 1, 2, 3]);
    ports[0].answerNext();
    await settle();
    expect(ports[0].posted.map(m => m.n)).toEqual([0, 1, 2, 3, 4]);
    for (let i = 0; i < 4; i++) ports[0].answerNext();
    expect((await Promise.all(calls) as { n: number }[]).map(r => r.n)).toEqual([0, 1, 2, 3, 4]);
  });

  test("the 65th pending call is rejected at once and nothing already pending is lost", async () => {
    const { t, ports } = rig();
    const calls = Array.from({ length: 64 }, (_, n) => outcome(t({ op: "get", n })));
    const over = await outcome(t({ op: "get", n: 64 }));
    expect(over.ok).toBe(false);
    expect(String((over as { e: Error }).e.message)).toMatch(/too many pending/);
    expect(ports[0].posted.length).toBe(4);
    for (let i = 0; i < 64; i++) { await settle(); ports[0].answerNext(); }
    const all = await Promise.all(calls);
    expect(all.every(r => r.ok)).toBe(true);
    expect(ports[0].posted.length).toBe(64);
  });
});

describe("port transport: poisoning", () => {
  test("a timeout rejects every pending call, closes the port, and the next call reconnects", async () => {
    const { t, ports } = rig({ callTimeoutMs: 30 });
    const a = outcome(t({ op: "get", n: 1 }));
    const b = outcome(t({ op: "get", n: 2 }));
    const ra = await a, rb = await b;
    expect(ra.ok || rb.ok).toBe(false);
    expect(String((ra as { e: Error }).e.message)).toMatch(/timed out/);
    expect(rb.ok).toBe(false);
    expect(ports[0].disconnected).toBe(true);
    // The late reply for call 1 must not be handed to a later call.
    const c = t({ op: "get", n: 3 });
    await settle();
    expect(ports.length).toBe(2);
    ports[0].say({ ok: true, n: 1 });
    ports[1].answerNext();
    expect(((await c) as { n: number }).n).toBe(3);
  });

  test("a disconnect mid-call rejects it, and the next call reconnects", async () => {
    const { t, ports } = rig();
    const warm = t({ op: "ping", n: 0 });
    await settle();
    ports[0].answerNext();
    await warm; // a connection that has answered once: its loss is not a failed connect, so there is no backoff
    const first = outcome(t({ op: "put", n: 1 }));
    await settle();
    ports[0].hangUp();
    const r = await first;
    expect(r.ok).toBe(false);
    expect((r as { e: unknown }).e).toBeInstanceOf(PortError);
    const next = t({ op: "put", n: 2 });
    await settle();
    ports[1].answerNext();
    expect(((await next) as { n: number }).n).toBe(2);
    expect(ports.length).toBe(2);
  });

  test("a reply with no call waiting poisons the port instead of being kept for the next call", async () => {
    const { t, ports } = rig();
    const a = t({ op: "get", n: 1 });
    await settle();
    ports[0].answerNext();
    await a;
    ports[0].say({ ok: true, n: 99 }); // nobody asked
    expect(ports[0].disconnected).toBe(true);
    const b = t({ op: "get", n: 2 });
    await settle();
    ports[1].answerNext();
    expect(((await b) as { n: number }).n).toBe(2);
  });

  test("a frame with a notify key is not a reply", async () => {
    const { t, ports } = rig();
    const a = t({ op: "get", n: 1 });
    const b = t({ op: "get", n: 2 });
    await settle();
    ports[0].say({ notify: "bank-changed", n: 777 });
    ports[0].answerNext();
    ports[0].say({ notify: "again" });
    ports[0].answerNext();
    expect([((await a) as { n: number }).n, ((await b) as { n: number }).n]).toEqual([1, 2]);
    expect(ports[0].disconnected).toBe(false);
  });
});

describe("port transport: backoff and the per-message fallback", () => {
  test("three failed connections back off, route through spawn meanwhile, and a good reply resets the count", async () => {
    const { t, ports, spawned, advance } = rig({ onConnect: (p, i) => { if (i < 3) queueMicrotask(() => setTimeout(() => p.hangUp(), 0)); } });
    for (let i = 0; i < 3; i++) {
      const r = await outcome(t({ op: "put", n: i })); // a write is never retried, so each call is one connection
      expect(r.ok).toBe(false);
      if (i < 2) advance(10_000); // past the wait, so the next call tries the port again
    }
    expect(ports.length).toBe(3);
    // Backing off after the third failure (4000 ms): spawn, no new connection.
    const during = (await t({ op: "get", n: 9 })) as { via: string };
    expect(during.via).toBe("spawn");
    expect(ports.length).toBe(3);
    advance(3_999);
    await t({ op: "get", n: 9 });
    expect(spawned.length).toBe(2);
    advance(1);
    const back = t({ op: "get", n: 10 });
    await settle();
    expect(ports.length).toBe(4);
    ports[3].answerNext();
    expect(((await back) as { n: number }).n).toBe(10);
    // The reply reset the count: the next failure waits backoff[0], not backoff[3].
    const w = outcome(t({ op: "put", n: 11 }));
    await settle();
    ports[3].hangUp();
    expect((await w).ok).toBe(false); // replied before, so this is not a connect failure: no backoff at all
    const after = t({ op: "get", n: 12 });
    await settle();
    expect(ports.length).toBe(5);
    ports[4].hangUp(); // a connect failure again
    await outcome(after);
    advance(999);
    expect(((await t({ op: "get", n: 13 })) as { via: string }).via).toBe("spawn");
    advance(1);
    const again = t({ op: "get", n: 14 });
    await settle();
    expect(ports.length).toBe(6);
    ports[5].answerNext();
    await again;
  });

  test("a read-only op is retried once on a fresh connection after a disconnect", async () => {
    const { t, ports, spawned } = rig();
    const warm = t({ op: "ping", n: 0 });
    await settle();
    ports[0].answerNext();
    await warm;
    const g = t({ op: "get", n: 1 });
    await settle();
    ports[0].hangUp();
    await settle();
    expect(ports.length).toBe(2);
    expect(ports[1].posted.map(m => m.n)).toEqual([1]);
    ports[1].answerNext();
    expect(((await g) as { n: number }).n).toBe(1);
    expect(spawned.length).toBe(0);
  });

  test("the retry happens once: a second disconnect rejects", async () => {
    const { t, ports } = rig();
    const warm = t({ op: "ping", n: 0 });
    await settle();
    ports[0].answerNext();
    await warm;
    const g = outcome(t({ op: "get", n: 1 }));
    await settle();
    ports[0].hangUp();
    await settle();
    ports[1].hangUp();
    expect((await g).ok).toBe(false);
    expect(ports.length).toBe(2);
  });

  test("a read-only op whose first connection never worked is answered by spawn, not lost", async () => {
    const { t, ports, spawned } = rig();
    const g = t({ op: "get", n: 1 });
    await settle();
    ports[0].hangUp();
    expect(((await g) as { via: string }).via).toBe("spawn");
    expect(spawned.length).toBe(1);
  });

  test("a write is never retried after a disconnect", async () => {
    const { t, ports } = rig();
    const warm = t({ op: "ping", n: 0 });
    await settle();
    ports[0].answerNext();
    await warm;
    for (const op of ["put", "patch", "log", "resumePut", "resumeDelete"]) {
      const w = outcome(t({ op, n: 1 }));
      await settle();
      const n = ports.length;
      ports[n - 1].hangUp();
      expect((await w).ok).toBe(false);
      await settle();
      expect(ports.length).toBe(n); // nothing reconnected on its behalf
      const ok = t({ op: "ping", n: 0 }); // reconnects for the next caller and warms the port again
      await settle();
      ports[ports.length - 1].answerNext();
      await ok;
    }
  });

  test("nothing is retried after a timeout, not even a read", async () => {
    const { t, ports } = rig({ callTimeoutMs: 20 });
    const g = await outcome(t({ op: "get", n: 1 }));
    expect(g.ok).toBe(false);
    await settle();
    expect(ports.length).toBe(1);
    expect(ports[0].posted.length).toBe(1);
  });

  test("ops that can take seconds go per message and never touch the port", async () => {
    const { t, ports, spawned } = rig();
    // sameQuestion runs the on-device model: on the port it would block bank calls and be clamped to the port's 5 s.
    for (const op of ["draft", "jobFind", "jobMark", "jobsIngest", "resumeOpen", "sameQuestion", "somethingNew"]) {
      expect(((await t({ op })) as { via: string }).via).toBe("spawn");
    }
    expect(spawned.map(m => m.op)).toEqual(["draft", "jobFind", "jobMark", "jobsIngest", "resumeOpen", "sameQuestion", "somethingNew"]);
    expect(ports.length).toBe(0);
  });

  test("resumeOpen stays off a warm port, so Preview cannot stall the bank", async () => {
    const { t, ports, spawned } = rig();
    const warm = t({ op: "ping" });
    await settle();
    ports[0].answerNext();
    await warm;
    expect(((await t({ op: "resumeOpen", key: "https://boards.greenhouse.io/acme/jobs/1" })) as { via: string }).via).toBe("spawn");
    expect(spawned.map(m => m.op)).toEqual(["resumeOpen"]);
    expect(ports.length).toBe(1);
    expect(ports[0].posted.map(m => m.op)).toEqual(["ping"]);
  });

  test("a connect that throws is a failed connection, not an exception", async () => {
    const spawned: object[] = [];
    const t = createPortTransport({
      connect: () => { throw new Error("no host"); },
      spawn: async m => { spawned.push(m); return { via: "spawn" }; },
    });
    expect((await outcome(t({ op: "put" }))).ok).toBe(false);
    expect(((await t({ op: "get" })) as { via: string }).via).toBe("spawn");
  });

  test("close rejects pending calls and later ones", async () => {
    const { t, ports } = rig();
    const p = outcome(t({ op: "put", n: 1 }));
    await settle();
    t.close();
    expect((await p).ok).toBe(false);
    expect(ports[0].disconnected).toBe(true);
    expect((await outcome(t({ op: "get" }))).ok).toBe(false);
  });
});

/** A promise the test can observe without awaiting a timer that has not fired. */
function track(p: Promise<unknown>) {
  const box: { settled: boolean; ok: boolean; v?: unknown; e?: unknown } = { settled: false, ok: false };
  const done = p.then(
    v => { box.settled = true; box.ok = true; box.v = v; },
    e => { box.settled = true; box.ok = false; box.e = e; },
  );
  return { box, done };
}

async function flush() {
  await Promise.resolve();
  await Promise.resolve();
}

/** Fake timers for one test. Restored even when the assertion fails, so later tests keep real time. */
async function underFakeTimers(fn: () => Promise<void>): Promise<void> {
  jest.useFakeTimers();
  try {
    await fn();
  } finally {
    jest.clearAllTimers();
    jest.useRealTimers();
  }
}

describe("port transport: a shorter per-call deadline", () => {
  test("a 2000ms probe that does not settle is not left in flight, and advancing to 5000ms does not poison the next call", async () => {
    await underFakeTimers(async () => {
      const { t, ports } = rig();
      const probe = track(t({ op: "resumeInfo", key: "https://boards.example/jobs/1" }, 2_000));
      expect(ports[0].posted).toEqual([{ op: "resumeInfo", key: "https://boards.example/jobs/1" }]);
      expect(ports[0].posted[0]).not.toHaveProperty("timeoutMs");
      jest.advanceTimersByTime(1_999);
      await flush();
      expect(probe.box.settled).toBe(false);
      jest.advanceTimersByTime(1);
      await flush();
      expect(probe.box.settled).toBe(true);
      expect(probe.box.ok).toBe(false);
      expect(probe.box.e).toBeInstanceOf(PortError);
      expect(String((probe.box.e as Error).message)).toMatch(/timed out after 2000 ms/);
      // Written, so this connection is poisoned. The probe timer is gone: it must not fire again at 5000.
      expect(ports[0].disconnected).toBe(true);
      expect(jest.getTimerCount()).toBe(0);

      const next = track(t({ op: "get", n: 2 }));
      expect(ports.length).toBe(2);
      expect(ports[1].disconnected).toBe(false);
      expect(ports[1].posted).toEqual([{ op: "get", n: 2 }]);
      // From the probe's t=0 to t=5000. The next call's own 5000ms bound has 3000ms left.
      jest.advanceTimersByTime(3_000);
      await flush();
      expect(next.box.settled).toBe(false);
      expect(ports[1].disconnected).toBe(false);
      // A late frame on the abandoned port is not this call's reply.
      ports[0].say({ ok: true, n: 1, late: true });
      await flush();
      expect(next.box.settled).toBe(false);
      ports[1].answerNext();
      await flush();
      expect(next.box).toMatchObject({ settled: true, ok: true, v: { ok: true, n: 2, op: "get" } });
      expect(ports[1].disconnected).toBe(false);
    });
  });

  test("a probe that was never written is dropped without poisoning the connection", async () => {
    await underFakeTimers(async () => {
      const { t, ports } = rig({ maxInFlight: 1 });
      const first = track(t({ op: "get", n: 1 }));
      const queued = track(t({ op: "resumeInfo", key: "https://boards.example/jobs/2" }, 2_000));
      expect(ports[0].posted).toEqual([{ op: "get", n: 1 }]);
      jest.advanceTimersByTime(2_000);
      await flush();
      expect(queued.box.ok).toBe(false);
      expect(String((queued.box.e as Error).message)).toMatch(/timed out after 2000 ms/);
      expect(ports[0].disconnected).toBe(false);
      expect(first.box.settled).toBe(false);
      ports[0].answerNext();
      await flush();
      expect(first.box).toMatchObject({ settled: true, ok: true, v: { ok: true, n: 1, op: "get" } });
      jest.advanceTimersByTime(5_000);
      await flush();
      expect(ports[0].disconnected).toBe(false);
    });
  });

  test("a call that returns before 2000ms resolves and leaves the port usable", async () => {
    await underFakeTimers(async () => {
      const { t, ports } = rig();
      const call = track(t({ op: "resumeInfo", key: "https://boards.example/jobs/3" }, 2_000));
      expect(ports[0].posted).toEqual([{ op: "resumeInfo", key: "https://boards.example/jobs/3" }]);
      ports[0].answerNext();
      await flush();
      expect(call.box).toMatchObject({
        settled: true,
        ok: true,
        v: { ok: true, op: "resumeInfo", n: undefined },
      });
      jest.advanceTimersByTime(5_000);
      await flush();
      expect(ports[0].disconnected).toBe(false);
      expect(jest.getTimerCount()).toBe(0);
      const again = track(t({ op: "get", n: 4 }));
      expect(ports.length).toBe(1);
      ports[0].answerNext();
      await flush();
      expect(again.box).toMatchObject({ settled: true, ok: true, v: { ok: true, n: 4, op: "get" } });
    });
  });

  test("a call with no shorter deadline still poisons at 5000ms, and a late reply does not complete the next call", async () => {
    await underFakeTimers(async () => {
      const { t, ports } = rig();
      const hanging = track(t({ op: "get", n: 1 }));
      const longer = track(t({ op: "get", n: 8 }, 8_000));
      jest.advanceTimersByTime(4_999);
      await flush();
      expect(hanging.box.settled).toBe(false);
      expect(longer.box.settled).toBe(false);
      jest.advanceTimersByTime(1);
      await flush();
      expect(hanging.box.ok).toBe(false);
      expect(longer.box.ok).toBe(false);
      expect(String((hanging.box.e as Error).message)).toMatch(/timed out after 5000 ms/);
      expect(String((longer.box.e as Error).message)).toMatch(/timed out after 5000 ms/);
      expect(ports[0].disconnected).toBe(true);
      const next = track(t({ op: "get", n: 3 }));
      expect(ports.length).toBe(2);
      ports[0].say({ ok: true, n: 1 });
      await flush();
      expect(next.box.settled).toBe(false);
      ports[1].answerNext();
      await flush();
      expect(next.box).toMatchObject({ settled: true, ok: true, v: { ok: true, n: 3, op: "get" } });
    });
  });

  test("NaN or a negative deadline is not a shorter deadline", async () => {
    await underFakeTimers(async () => {
      const { t, ports } = rig();
      const nan = track(t({ op: "get", n: 1 }, Number.NaN));
      const neg = track(t({ op: "put", n: 2 }, -1));
      jest.advanceTimersByTime(4_999);
      await flush();
      expect(nan.box.settled).toBe(false);
      expect(neg.box.settled).toBe(false);
      expect(ports[0].disconnected).toBe(false);
      jest.advanceTimersByTime(1);
      await flush();
      expect(nan.box.ok).toBe(false);
      expect(neg.box.ok).toBe(false);
      expect(ports[0].disconnected).toBe(true);
    });
  });

  test("a per-message op keeps the caller's longer deadline and is not sent on the port", async () => {
    await underFakeTimers(async () => {
      const spawned: { msg: object; ms: number | undefined }[] = [];
      const ports: FakePort[] = [];
      const t = createPortTransport({
        connect: () => { const p = new FakePort(); ports.push(p); return p; },
        spawn: (msg, ms) => new Promise((_resolve, reject) => {
          spawned.push({ msg, ms });
          setTimeout(() => reject(new Error(`spawn ${String(ms)}`)), ms ?? 999_999);
        }),
        callTimeoutMs: 5_000,
      });
      const ingest = track(t({ op: "jobsIngest", jobs: [] }, 40_000));
      expect(ports.length).toBe(0);
      expect(spawned).toEqual([{ msg: { op: "jobsIngest", jobs: [] }, ms: 40_000 }]);
      jest.advanceTimersByTime(5_000);
      await flush();
      expect(ingest.box.settled).toBe(false);
      jest.advanceTimersByTime(35_000);
      await flush();
      expect(ingest.box.ok).toBe(false);
      expect(String((ingest.box.e as Error).message)).toBe("spawn 40000");
    });
  });

  test("sameQuestion keeps the 8 s the background gives it, past the port's 5 s", async () => {
    const spawned: { msg: object; ms: number | undefined }[] = [];
    const ports: FakePort[] = [];
    const t = createPortTransport({
      connect: () => { const p = new FakePort(); ports.push(p); return p; },
      spawn: async (msg, ms) => { spawned.push({ msg, ms }); return { reading: "answered", value: "same" }; },
      callTimeoutMs: 5_000,
    });
    await t({ op: "sameQuestion", a: "A", b: "B" }, 8_000);
    expect(ports.length).toBe(0);
    expect(spawned).toEqual([{ msg: { op: "sameQuestion", a: "A", b: "B" }, ms: 8_000 }]);
  });
});

/** The deadline stays a timer argument. It is never copied onto the host message. */
function noDeadline(m: object | undefined) {
  expect(m).toBeDefined();
  expect(m).not.toHaveProperty("timeout");
  expect(m).not.toHaveProperty("timeoutMs");
  expect(m).not.toHaveProperty("ms");
}

describe("adversarial: frame probe deadline", () => {
  test("a queued 2000ms probe is not in flight, the same port stays up, the next ping on it succeeds, and advancing to 5000ms does not poison", async () => {
    await underFakeTimers(async () => {
      const { t, ports } = rig({ maxInFlight: 1 });
      const blocker = track(t({ op: "get", n: 1 }));
      const probe = track(t({ op: "resumeInfo", key: "https://boards.example/jobs/queued" }, 2_000));
      expect(ports.length).toBe(1);
      expect(ports[0].posted).toEqual([{ op: "get", n: 1 }]);
      noDeadline(ports[0].posted[0]);
      expect(jest.getTimerCount()).toBe(2);
      jest.advanceTimersByTime(1_999);
      await flush();
      expect(probe.box.settled).toBe(false);
      expect(blocker.box.settled).toBe(false);
      jest.advanceTimersByTime(1);
      await flush();
      expect(probe.box.settled).toBe(true);
      expect(probe.box.ok).toBe(false);
      expect(probe.box.e).toBeInstanceOf(PortError);
      expect((probe.box.e as PortError).kind).toBe("timeout");
      expect(String((probe.box.e as Error).message)).toMatch(/timed out after 2000 ms/);
      expect(ports[0].disconnected).toBe(false);
      expect(ports[0].posted).toEqual([{ op: "get", n: 1 }]);
      expect(jest.getTimerCount()).toBe(1);
      ports[0].answerNext();
      await flush();
      expect(blocker.box).toMatchObject({ settled: true, ok: true, v: { ok: true, n: 1, op: "get" } });
      expect(jest.getTimerCount()).toBe(0);

      const ping = track(t({ op: "ping", n: 2 }));
      expect(ports.length).toBe(1);
      expect(ports[0].disconnected).toBe(false);
      expect(ports[0].posted.map(m => m.op)).toEqual(["get", "ping"]);
      noDeadline(ports[0].posted[1]);
      jest.advanceTimersByTime(3_000);
      await flush();
      expect(ping.box.settled).toBe(false);
      expect(ports[0].disconnected).toBe(false);
      expect(ports.length).toBe(1);
      expect(jest.getTimerCount()).toBe(1);
      ports[0].answerNext();
      await flush();
      expect(ping.box).toMatchObject({ settled: true, ok: true, v: { ok: true, n: 2, op: "ping" } });
      expect(ports[0].disconnected).toBe(false);
      jest.advanceTimersByTime(5_000);
      await flush();
      expect(ports[0].disconnected).toBe(false);
      expect(jest.getTimerCount()).toBe(0);
    });
  });

  test("a written 2000ms probe poisons that connection, its late reply is not the next call, and advancing to 5000ms does not poison the new port", async () => {
    await underFakeTimers(async () => {
      const { t, ports } = rig({
        onConnect: (p) => {
          p.disconnect = () => {
            p.disconnected = true;
            p.say({ ok: true, stolen: true });
          };
        },
      });
      const probe = track(t({ op: "resumeInfo", key: "https://boards.example/jobs/written" }, 2_000));
      expect(ports[0].posted).toEqual([{ op: "resumeInfo", key: "https://boards.example/jobs/written" }]);
      noDeadline(ports[0].posted[0]);
      jest.advanceTimersByTime(2_000);
      await flush();
      expect(probe.box.ok).toBe(false);
      expect(probe.box.v).toBeUndefined();
      expect(String((probe.box.e as Error).message)).toMatch(/timed out after 2000 ms/);
      expect(ports[0].disconnected).toBe(true);
      expect(jest.getTimerCount()).toBe(0);

      const next = track(t({ op: "get", n: 7 }));
      expect(ports.length).toBe(2);
      expect(ports[1].disconnected).toBe(false);
      expect(ports[1].posted).toEqual([{ op: "get", n: 7 }]);
      noDeadline(ports[1].posted[0]);
      ports[0].say({ ok: true, n: 7, late: true });
      await flush();
      expect(next.box.settled).toBe(false);
      jest.advanceTimersByTime(3_000);
      await flush();
      expect(next.box.settled).toBe(false);
      expect(ports[1].disconnected).toBe(false);
      expect(ports[0].disconnected).toBe(true);
      ports[1].answerNext();
      await flush();
      expect(next.box).toMatchObject({ settled: true, ok: true, v: { ok: true, n: 7, op: "get" } });
      expect(ports[1].disconnected).toBe(false);
    });
  });

  test("5000, 5001, NaN, Infinity, and -1 on a port op all use the 5000 timer, and a written timeout poisons", async () => {
    const deadlines = [5_000, 5_001, Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY, -1];
    for (const ms of deadlines) {
      await underFakeTimers(async () => {
        const { t, ports, spawned } = rig();
        const call = track(t({ op: "get", n: 1 }, ms));
        expect(ports.length).toBe(1);
        expect(spawned).toEqual([]);
        expect(ports[0].posted).toEqual([{ op: "get", n: 1 }]);
        noDeadline(ports[0].posted[0]);
        jest.advanceTimersByTime(4_999);
        await flush();
        expect(call.box.settled).toBe(false);
        expect(ports[0].disconnected).toBe(false);
        jest.advanceTimersByTime(1);
        await flush();
        expect(call.box.ok).toBe(false);
        expect(String((call.box.e as Error).message)).toMatch(/timed out after 5000 ms/);
        expect(ports[0].disconnected).toBe(true);
        expect(jest.getTimerCount()).toBe(0);
        jest.advanceTimersByTime(40_000);
        await flush();
        expect(ports.length).toBe(1);
      });
    }
  });

  test("an omitted per-call deadline and the default callTimeoutMs are both 5000, and a written timeout poisons", async () => {
    await underFakeTimers(async () => {
      const ports: FakePort[] = [];
      const t = createPortTransport({
        connect: () => { const p = new FakePort(); ports.push(p); return p; },
        spawn: async () => ({ via: "spawn" }),
      });
      const omitted = track(t({ op: "get", n: 1 }));
      const explicit = track(t({ op: "put", n: 2 }, undefined));
      expect(ports.length).toBe(1);
      expect(ports[0].posted.map(m => m.op)).toEqual(["get", "put"]);
      noDeadline(ports[0].posted[0]);
      noDeadline(ports[0].posted[1]);
      jest.advanceTimersByTime(4_999);
      await flush();
      expect(omitted.box.settled).toBe(false);
      expect(explicit.box.settled).toBe(false);
      expect(ports[0].disconnected).toBe(false);
      jest.advanceTimersByTime(1);
      await flush();
      expect(omitted.box.ok).toBe(false);
      expect(explicit.box.ok).toBe(false);
      expect(String((omitted.box.e as Error).message)).toMatch(/timed out after 5000 ms/);
      expect(String((explicit.box.e as Error).message)).toMatch(/timed out after 5000 ms/);
      expect(ports[0].disconnected).toBe(true);
      expect(jest.getTimerCount()).toBe(0);
    });
  });

  test("a queued deadline of 0 fires immediately and does not poison the connection", async () => {
    await underFakeTimers(async () => {
      const { t, ports } = rig({ maxInFlight: 1 });
      const blocker = track(t({ op: "get", n: 1 }));
      const zero = track(t({ op: "ping", n: 2 }, 0));
      expect(ports[0].posted).toEqual([{ op: "get", n: 1 }]);
      noDeadline(ports[0].posted[0]);
      expect(zero.box.settled).toBe(false);
      jest.advanceTimersByTime(0);
      await flush();
      expect(zero.box.settled).toBe(true);
      expect(zero.box.ok).toBe(false);
      expect(zero.box.e).toBeInstanceOf(PortError);
      expect((zero.box.e as PortError).kind).toBe("timeout");
      expect(String((zero.box.e as Error).message)).toMatch(/timed out after 0 ms/);
      expect(ports[0].disconnected).toBe(false);
      expect(ports[0].posted).toEqual([{ op: "get", n: 1 }]);
      expect(blocker.box.settled).toBe(false);
      expect(jest.getTimerCount()).toBe(1);
      ports[0].answerNext();
      await flush();
      expect(blocker.box.ok).toBe(true);
      const ping = track(t({ op: "ping", n: 3 }));
      expect(ports.length).toBe(1);
      ports[0].answerNext();
      await flush();
      expect(ping.box).toMatchObject({ settled: true, ok: true, v: { ok: true, n: 3, op: "ping" } });
      expect(ports[0].disconnected).toBe(false);
    });
  });

  test("a port op given 40000 is still the 5000 timer and poisons, while a non-PORT_OP given 40000 is not clamped", async () => {
    await underFakeTimers(async () => {
      const spawned: { msg: object; ms: number | undefined }[] = [];
      const ports: FakePort[] = [];
      const t = createPortTransport({
        connect: () => { const p = new FakePort(); ports.push(p); return p; },
        spawn: (msg, ms) => new Promise((resolve, reject) => {
          spawned.push({ msg, ms });
          noDeadline(msg);
          setTimeout(() => reject(new Error(`spawn ${String(ms)}`)), (ms ?? 0) + 1);
          setTimeout(() => resolve({ queued: 1 }), 39_999);
        }),
        callTimeoutMs: 5_000,
      });
      const portOp = track(t({ op: "get", n: 1 }, 40_000));
      expect(ports[0].posted).toEqual([{ op: "get", n: 1 }]);
      noDeadline(ports[0].posted[0]);
      expect(spawned).toEqual([]);
      jest.advanceTimersByTime(5_000);
      await flush();
      expect(portOp.box.ok).toBe(false);
      expect(String((portOp.box.e as Error).message)).toMatch(/timed out after 5000 ms/);
      expect(ports[0].disconnected).toBe(true);

      for (const op of ["jobsIngest", "draft"]) {
        const call = track(t({ op, jobs: [] }, 40_000));
        const sent = spawned.at(-1);
        expect(sent).toEqual({ msg: { op, jobs: [] }, ms: 40_000 });
        noDeadline(sent?.msg);
        expect(ports.length).toBe(1);
        jest.advanceTimersByTime(5_000);
        await flush();
        expect(call.box.settled).toBe(false);
        jest.advanceTimersByTime(34_998);
        await flush();
        expect(call.box.settled).toBe(false);
        jest.advanceTimersByTime(1);
        await flush();
        expect(call.box).toMatchObject({ settled: true, ok: true, v: { queued: 1 } });
        jest.advanceTimersByTime(2);
        await flush();
        expect(call.box).toMatchObject({ settled: true, ok: true, v: { queued: 1 } });
      }
    });
  });

  test("when the written call times out at 2000 the queued call is rejected by that poison, not left running until 5000 on a dead port", async () => {
    await underFakeTimers(async () => {
      const { t, ports } = rig({ maxInFlight: 1 });
      const written = track(t({ op: "resumeInfo", key: "https://boards.example/jobs/overlap" }, 2_000));
      const queued = track(t({ op: "ping", n: 2 }));
      expect(ports[0].posted).toEqual([{ op: "resumeInfo", key: "https://boards.example/jobs/overlap" }]);
      noDeadline(ports[0].posted[0]);
      jest.advanceTimersByTime(1_999);
      await flush();
      expect(written.box.settled).toBe(false);
      expect(queued.box.settled).toBe(false);
      expect(ports[0].disconnected).toBe(false);
      expect(ports[0].posted).toHaveLength(1);
      jest.advanceTimersByTime(1);
      await flush();
      expect(written.box.ok).toBe(false);
      expect(queued.box.settled).toBe(true);
      expect(queued.box.ok).toBe(false);
      expect(queued.box.e).toBeInstanceOf(PortError);
      expect((queued.box.e as PortError).kind).toBe("timeout");
      expect(String((written.box.e as Error).message)).toMatch(/timed out after 2000 ms/);
      expect(String((queued.box.e as Error).message)).toMatch(/timed out after 2000 ms/);
      expect(ports[0].disconnected).toBe(true);
      expect(ports[0].posted).toHaveLength(1);
      expect(jest.getTimerCount()).toBe(0);
      jest.advanceTimersByTime(3_000);
      await flush();
      expect(queued.box.ok).toBe(false);
      expect(ports.length).toBe(1);
      expect(ports[0].disconnected).toBe(true);
      const next = track(t({ op: "ping", n: 3 }));
      expect(ports.length).toBe(2);
      expect(ports[1].disconnected).toBe(false);
      ports[0].say({ ok: true, n: 3, op: "ping" });
      await flush();
      expect(next.box.settled).toBe(false);
      ports[1].answerNext();
      await flush();
      expect(next.box).toMatchObject({ settled: true, ok: true, v: { ok: true, n: 3, op: "ping" } });
    });
  });
});
