import { describe, expect, test } from "bun:test";
import { BANK_FRESH_MS, createBankMemo } from "../src/bankcache";

/** A fetch that counts its calls and answers when told to. */
function source() {
  const s = {
    calls: 0,
    clock: 1_000,
    value: 1,
    gate: null as Promise<void> | null,
    fail: false,
    fetch: async (): Promise<{ n: number }> => {
      s.calls++;
      const n = s.value;
      if (s.gate) await s.gate;
      if (s.fail) throw new Error("down");
      return { n };
    },
  };
  return s;
}

describe("createBankMemo", () => {
  test("ten reads inside the window make one request; the first read after it makes another", async () => {
    const s = source();
    const m = createBankMemo({ fetch: s.fetch, now: () => s.clock });
    for (let i = 0; i < 10; i++) expect(await m.get()).toEqual({ n: 1 });
    expect(s.calls).toBe(1);
    s.clock += BANK_FRESH_MS - 1;
    await m.get();
    expect(s.calls).toBe(1);
    s.clock += 1;
    s.value = 2;
    expect(await m.get()).toEqual({ n: 2 });
    expect(s.calls).toBe(2);
  });

  test("concurrent reads share one request", async () => {
    const s = source();
    let release: () => void = () => undefined;
    s.gate = new Promise<void>(r => { release = r; });
    const m = createBankMemo({ fetch: s.fetch, now: () => s.clock });
    const all = Promise.all([m.get(), m.get(), m.get(), m.get()]);
    release();
    const got = await all;
    expect(s.calls).toBe(1);
    expect(got.every(g => g === got[0])).toBe(true);
  });

  test("fresh always asks and the answer is what get serves next", async () => {
    const s = source();
    const m = createBankMemo({ fetch: s.fetch, now: () => s.clock });
    await m.get();
    s.value = 2;
    expect(await m.fresh()).toEqual({ n: 2 });
    expect(await m.get()).toEqual({ n: 2 });
    expect(s.calls).toBe(2);
  });

  test("invalidate forces the next read to ask", async () => {
    const s = source();
    const m = createBankMemo({ fetch: s.fetch, now: () => s.clock });
    await m.get();
    m.invalidate();
    expect(m.peek()).toBeUndefined();
    s.value = 2;
    expect(await m.get()).toEqual({ n: 2 });
  });

  test("a request that was in flight when a write invalidated is not kept", async () => {
    const s = source();
    let release: () => void = () => undefined;
    s.gate = new Promise<void>(r => { release = r; });
    const m = createBankMemo({ fetch: s.fetch, now: () => s.clock });
    const pending = m.get(); // reads value 1, before the write
    m.invalidate();
    release();
    expect(await pending).toEqual({ n: 1 });
    expect(m.peek()).toBeUndefined(); // the next read asks again
    s.gate = null;
    s.value = 2;
    expect(await m.get()).toEqual({ n: 2 });
  });

  test("a failed request is shared by its waiters, never kept, and does not poison the next", async () => {
    const s = source();
    s.fail = true;
    const m = createBankMemo({ fetch: s.fetch, now: () => s.clock });
    const [a, b] = await Promise.allSettled([m.get(), m.get()]);
    expect(a.status).toBe("rejected");
    expect(b.status).toBe("rejected");
    expect(s.calls).toBe(1);
    s.fail = false;
    expect(await m.get()).toEqual({ n: 1 });
    expect(s.calls).toBe(2);
  });

  test("set holds a value for the window; a clock that goes backwards is not freshness", async () => {
    const s = source();
    const m = createBankMemo({ fetch: s.fetch, now: () => s.clock });
    m.set({ n: 7 });
    expect(await m.get()).toEqual({ n: 7 });
    expect(s.calls).toBe(0);
    s.clock -= 5;
    expect(m.peek()).toBeUndefined();
  });

  test("reuse keeps the held object when the new one is provably the same", async () => {
    const s = source();
    const m = createBankMemo({ fetch: s.fetch, now: () => s.clock, reuse: (held, next) => (held.n === next.n ? held : next) });
    const first = await m.get();
    expect(await m.fresh()).toBe(first);
    s.value = 2;
    expect(await m.fresh()).not.toBe(first);
  });

  test("holds one value only: a second fetch replaces the first", async () => {
    const s = source();
    const m = createBankMemo({ fetch: s.fetch, now: () => s.clock });
    await m.get();
    s.value = 2;
    await m.fresh();
    expect(m.peek()).toEqual({ n: 2 });
  });

  test("fresh drops the generation an in-flight get started, so the older answer cannot hold", async () => {
    const pending: Array<(value: string) => void> = [];
    let calls = 0;
    const clock = { t: 1_000 };
    const m = createBankMemo<string>({
      fetch: () => new Promise<string>(resolve => {
        calls++;
        pending.push(resolve);
      }),
      now: () => clock.t,
    });
    const resolveAt = (i: number, value: string): void => {
      const resolve = pending[i];
      if (!resolve) throw new Error(`fetch ${i} was not started`);
      resolve(value);
    };

    // get() starts A and leaves it pending. fresh() must not share that request.
    const older = m.get();
    expect(calls).toBe(1);
    const newer = m.fresh();
    expect(calls).toBe(2);

    // A settles after B. That is the order in which the older answer can clobber the hold.
    resolveAt(1, "new");
    expect(await newer).toBe("new");
    resolveAt(0, "old");
    expect(await older).toBe("old");

    expect(m.peek()).toBe("new");
    expect(await m.get()).toBe("new");
    expect(calls).toBe(2);

    // Two concurrent gets, with nothing in flight, still share one fetch.
    clock.t += BANK_FRESH_MS;
    expect(m.peek()).toBeUndefined();
    const before = calls;
    const shared = Promise.all([m.get(), m.get()]);
    expect(calls - before).toBe(1);
    resolveAt(2, "shared");
    expect(await shared).toEqual(["shared", "shared"]);
  });
});
