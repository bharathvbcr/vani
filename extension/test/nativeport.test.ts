import { describe, expect, test } from "bun:test";
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
    for (const op of ["draft", "jobFind", "jobMark", "jobsIngest", "somethingNew"]) {
      expect(((await t({ op })) as { via: string }).via).toBe("spawn");
    }
    expect(spawned.map(m => m.op)).toEqual(["draft", "jobFind", "jobMark", "jobsIngest", "somethingNew"]);
    expect(ports.length).toBe(0);
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
