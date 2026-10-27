import { describe, expect, test } from "bun:test";
import { BANK_FRESH_MS, createBankMemo } from "../src/bankcache";

/**
 * Fetches that stay pending until this test settles them, one at a time.
 * `calls` counts every entry, including one that later rejects.
 */
function deferred<T>() {
  const slots: Array<{ resolve: (value: T) => void; reject: (error: Error) => void } | undefined> = [];
  const box = {
    calls: 0,
    clock: 1_000,
    fetch: (): Promise<T> => {
      const i = box.calls++;
      return new Promise<T>((resolve, reject) => {
        slots[i] = { resolve, reject };
      });
    },
    resolve(i: number, value: T): void {
      const slot = slots[i];
      if (!slot) throw new Error(`fetch ${i} was not started`);
      slots[i] = undefined;
      slot.resolve(value);
    },
    reject(i: number, error: Error = new Error("down")): void {
      const slot = slots[i];
      if (!slot) throw new Error(`fetch ${i} was not started`);
      slots[i] = undefined;
      slot.reject(error);
    },
  };
  return box;
}

function memoOf<T>(box: ReturnType<typeof deferred<T>>, reuse?: (held: T, next: T) => T) {
  return createBankMemo<T>({ fetch: box.fetch, now: () => box.clock, ...(reuse ? { reuse } : {}) });
}

describe("bank memo adversarial", () => {
  test("fresh does not share the in-flight get, and the older answer still reaches only its caller", async () => {
    const box = deferred<string>();
    const m = memoOf(box);
    const older = m.get();
    expect(box.calls).toBe(1);
    const newer = m.fresh();
    expect(box.calls).toBe(2);
    expect(newer).not.toBe(older);

    // The generation fresh just opened is the one concurrent gets share.
    expect(m.get()).toBe(newer);
    expect(m.get()).toBe(newer);
    expect(box.calls).toBe(2);

    box.resolve(1, "new");
    expect(await newer).toBe("new");
    expect(m.peek()).toBe("new");

    // Settling the superseded fetch must not drop the successor that already held,
    // and must not publish "old" into the slot.
    box.resolve(0, "old");
    expect(await older).toBe("old");
    expect(m.peek()).toBe("new");
    expect(await m.get()).toBe("new");
    expect(box.calls).toBe(2);
  });

  test("an older fetch that settles first still does not hold; the later answer does", async () => {
    const box = deferred<{ n: number }>();
    const m = memoOf(box);
    const older = m.get();
    const newer = m.fresh();
    const oldValue = { n: 1 };
    const newValue = { n: 2 };
    box.resolve(0, oldValue);
    expect(await older).toBe(oldValue);
    // fresh() already bumped the generation, so the earlier answer is not stored on the way through.
    expect(m.peek()).toBeUndefined();
    box.resolve(1, newValue);
    expect(await newer).toBe(newValue);
    expect(m.peek()).toBe(newValue);
    oldValue.n = 9;
    expect(m.peek()).toEqual({ n: 2 });
  });

  test("two overlapping fresh calls: only the later generation holds, whichever settles first", async () => {
    const box = deferred<string>();
    const m = memoOf(box);
    const first = m.fresh();
    const second = m.fresh();
    expect(m.get()).toBe(second);
    expect(box.calls).toBe(2);

    box.resolve(1, "second");
    expect(await second).toBe("second");
    box.resolve(0, "first");
    expect(await first).toBe("first");
    expect(m.peek()).toBe("second");

    // And the earlier fetch settling first must not be the value that survives the later one.
    const third = m.fresh();
    const fourth = m.fresh();
    box.resolve(2, "third");
    expect(await third).toBe("third");
    expect(m.peek()).toBe("second");
    box.resolve(3, "fourth");
    expect(await fourth).toBe("fourth");
    expect(m.peek()).toBe("fourth");
    expect(box.calls).toBe(4);
  });

  test("a superseded fetch cannot clear the successor, so a later get still shares it", async () => {
    const box = deferred<string>();
    const m = memoOf(box);
    const older = m.get();
    const newer = m.fresh();
    box.resolve(0, "old");
    expect(await older).toBe("old");
    const mid = m.get();
    expect(mid).toBe(newer);
    expect(box.calls).toBe(2);
    box.resolve(1, "new");
    expect(await mid).toBe("new");
    expect(m.peek()).toBe("new");

    // Same slot rule when the superseded fetch rejects instead of resolving.
    box.clock += BANK_FRESH_MS;
    expect(m.peek()).toBeUndefined();
    const rejected = m.get();
    const successor = m.fresh();
    box.reject(2);
    await expect(rejected).rejects.toThrow("down");
    const shared = m.get();
    expect(shared).toBe(successor);
    expect(box.calls).toBe(4);
    box.resolve(3, "after-reject");
    expect(await shared).toBe("after-reject");
    expect(m.peek()).toBe("after-reject");
  });

  test("reuse runs only for the generation allowed to hold", async () => {
    const box = deferred<{ n: number }>();
    let reuseCalls = 0;
    const m = memoOf(box, (held, next) => {
      reuseCalls++;
      return held.n === next.n ? held : next;
    });
    const original = { n: 1 };
    m.set(original);
    expect(m.peek()).toBe(original);
    expect(reuseCalls).toBe(0);

    const older = m.fresh();
    const newer = m.fresh();
    const kept = { n: 1 };
    const displaced = { n: 7 };
    box.resolve(1, kept);
    expect(await newer).toBe(original);
    expect(reuseCalls).toBe(1);
    expect(m.peek()).toBe(original);

    box.resolve(0, displaced);
    expect(await older).toBe(displaced);
    expect(reuseCalls).toBe(1);
    expect(m.peek()).toBe(original);
    displaced.n = 8;
    expect(m.peek()).toEqual({ n: 1 });
  });

  test("an older generation cannot replace the object a later fresh reused", async () => {
    const box = deferred<{ n: number }>();
    const seen: number[] = [];
    const m = memoOf(box, (held, next) => {
      seen.push(next.n);
      return held.n === next.n ? held : next;
    });
    const original = { n: 1 };
    m.set(original);
    const first = m.fresh();
    const second = m.fresh();
    const secondValue = { n: 4 };
    box.resolve(1, secondValue);
    expect(await second).toBe(secondValue);
    expect(m.peek()).toBe(secondValue);
    box.resolve(0, { n: 4 });
    expect(await first).toEqual({ n: 4 });
    expect(m.peek()).toBe(secondValue);
    expect(seen).toEqual([4]);
  });

  test("fresh during an in-flight get, then set before either settles: set wins both orders", async () => {
    const box = deferred<string>();
    const m = memoOf(box);
    const older = m.get();
    const newer = m.fresh();
    const written = "from-set";
    m.set(written);
    expect(m.peek()).toBe(written);
    await expect(m.get()).resolves.toBe(written);
    expect(box.calls).toBe(2);

    box.resolve(1, "new");
    box.resolve(0, "old");
    expect(await newer).toBe("new");
    expect(await older).toBe("old");
    expect(m.peek()).toBe(written);
    expect(await m.get()).toBe(written);
    expect(box.calls).toBe(2);

    // set() dropped inflight. After the window, get must not join either fetch that set disowned.
    box.clock += BANK_FRESH_MS;
    expect(m.peek()).toBeUndefined();
    const older2 = m.get();
    const newer2 = m.fresh();
    m.set("second-set");
    expect(m.peek()).toBe("second-set");
    box.clock += BANK_FRESH_MS;
    expect(m.peek()).toBeUndefined();
    const after = m.get();
    expect(box.calls).toBe(5);
    box.resolve(3, "newer-late");
    box.resolve(2, "older-late");
    box.resolve(4, "asked");
    expect(await newer2).toBe("newer-late");
    expect(await older2).toBe("older-late");
    expect(await after).toBe("asked");
    expect(m.peek()).toBe("asked");
  });

  test("set holds the reference it was given and a prior fetch cannot replace it", async () => {
    const box = deferred<{ n: number }>();
    const m = memoOf(box);
    const pending = m.get();
    const given = { n: 7 };
    m.set(given);
    expect(m.peek()).toBe(given);
    expect(await m.get()).toBe(given);
    expect(box.calls).toBe(1);
    box.resolve(0, { n: 1 });
    expect(await pending).toEqual({ n: 1 });
    expect(m.peek()).toBe(given);
    given.n = 8;
    expect(m.peek()).toEqual({ n: 8 });
  });

  test("invalidate drops inflight and the held value; a settled fresh must not put it back", async () => {
    const box = deferred<string>();
    const m = memoOf(box);
    m.set("kept");
    await expect(m.get()).resolves.toBe("kept");
    expect(box.calls).toBe(0);
    const newer = m.fresh();
    expect(box.calls).toBe(1);
    m.invalidate();
    expect(m.peek()).toBeUndefined();
    const again = m.get();
    const same = m.get();
    expect(again).toBe(same);
    expect(again).not.toBe(newer);
    expect(box.calls).toBe(2);

    box.resolve(0, "fresh-late");
    expect(await newer).toBe("fresh-late");
    expect(m.peek()).toBeUndefined();
    box.resolve(1, "after");
    expect(await again).toBe("after");
    expect(m.peek()).toBe("after");
  });

  test("fresh then invalidate before the fetch settles leaves peek empty", async () => {
    const box = deferred<string>();
    const m = memoOf(box);
    const seeded = m.get();
    box.resolve(0, "seed");
    expect(await seeded).toBe("seed");
    const newer = m.fresh();
    m.invalidate();
    expect(m.peek()).toBeUndefined();
    box.resolve(1, "late");
    expect(await newer).toBe("late");
    expect(m.peek()).toBeUndefined();
    const next = m.get();
    expect(box.calls).toBe(3);
    box.resolve(2, "asked");
    expect(await next).toBe("asked");
    expect(m.peek()).toBe("asked");
  });

  test("a rejection is not memoised, including a failed fresh", async () => {
    const box = deferred<string>();
    const m = memoOf(box);
    const first = m.fresh();
    const shared = m.get();
    expect(shared).toBe(first);
    box.reject(0, new Error("fresh-down"));
    await expect(first).rejects.toThrow("fresh-down");
    await expect(shared).rejects.toThrow("fresh-down");
    expect(m.peek()).toBeUndefined();

    const next = m.get();
    expect(box.calls).toBe(2);
    expect(next).not.toBe(first);
    box.resolve(1, "recovered");
    expect(await next).toBe("recovered");
    expect(m.peek()).toBe("recovered");
    expect(await m.get()).toBe("recovered");
    expect(box.calls).toBe(2);
  });

  test("a failed fresh does not poison a later get, and does not erase a value still inside the window", async () => {
    const box = deferred<{ n: number }>();
    const m = memoOf(box);
    const seeded = m.get();
    box.resolve(0, { n: 1 });
    const held = await seeded;
    const failed = m.fresh();
    box.reject(1, new Error("fresh-down"));
    await expect(failed).rejects.toThrow("fresh-down");
    expect(m.peek()).toBe(held);
    expect(await m.get()).toBe(held);
    expect(box.calls).toBe(2);

    box.clock += BANK_FRESH_MS;
    expect(m.peek()).toBeUndefined();
    const again = m.get();
    expect(box.calls).toBe(3);
    box.resolve(2, { n: 3 });
    expect(await again).toEqual({ n: 3 });
  });

  test("a failed later fresh does not let the earlier fetch hold", async () => {
    const box = deferred<string>();
    const m = memoOf(box);
    const earlier = m.fresh();
    const later = m.fresh();
    const joined = m.get();
    expect(joined).toBe(later);
    box.reject(1, new Error("later-down"));
    await expect(later).rejects.toThrow("later-down");
    await expect(joined).rejects.toThrow("later-down");
    box.resolve(0, "earlier");
    expect(await earlier).toBe("earlier");
    expect(m.peek()).toBeUndefined();
    const recovered = m.get();
    expect(box.calls).toBe(3);
    box.resolve(2, "third");
    expect(await recovered).toBe("third");
    expect(m.peek()).toBe("third");
  });

  test("peek serves the window without a fetch, and a backwards clock is not freshness", async () => {
    const box = deferred<string>();
    const m = memoOf(box);
    const pending = m.get();
    box.resolve(0, "v");
    expect(await pending).toBe("v");
    expect(box.calls).toBe(1);
    box.clock += BANK_FRESH_MS - 1;
    expect(m.peek()).toBe("v");
    expect(await m.get()).toBe("v");
    expect(box.calls).toBe(1);
    box.clock += 1;
    expect(m.peek()).toBeUndefined();

    const again = m.get();
    box.resolve(1, "next");
    expect(await again).toBe("next");
    box.clock -= 5;
    expect(m.peek()).toBeUndefined();
    // Dropped. Moving forward to the old timestamp must not resurrect it.
    box.clock += 5;
    expect(m.peek()).toBeUndefined();
    const third = m.get();
    expect(box.calls).toBe(3);
    box.resolve(2, "after-rewind");
    expect(await third).toBe("after-rewind");
  });

  test("a falsy value inside the window is served and does not start a fetch", async () => {
    const box = deferred<string | number>();
    const m = memoOf(box);
    m.set(0);
    expect(m.peek()).toBe(0);
    expect(await m.get()).toBe(0);
    m.set("");
    expect(m.peek()).toBe("");
    expect(await m.get()).toBe("");
    expect(box.calls).toBe(0);
    box.clock -= 1;
    expect(m.peek()).toBeUndefined();
  });

  test("many fresh calls keep distinct generations and only the last overlapping one holds", async () => {
    let n = 0;
    const clock = { t: 1_000 };
    const sequential = createBankMemo({
      fetch: async () => ({ n: n++ }),
      now: () => clock.t,
    });
    let last = { n: -1 };
    for (let i = 0; i < 1_000; i++) last = await sequential.fresh();
    expect(last).toEqual({ n: 999 });
    expect(sequential.peek()).toEqual({ n: 999 });
    expect(n).toBe(1_000);

    const box = deferred<number>();
    const m = memoOf(box);
    const pending: Promise<number>[] = [];
    for (let i = 0; i < 80; i++) pending.push(m.fresh());
    expect(box.calls).toBe(80);
    expect(m.get()).toBe(pending[79]);
    for (let i = 79; i >= 0; i--) box.resolve(i, i);
    const got = await Promise.all(pending);
    expect(got[0]).toBe(0);
    expect(got[79]).toBe(79);
    expect(m.peek()).toBe(79);
    expect(await m.get()).toBe(79);
    expect(box.calls).toBe(80);
  });

  test("the memo shares the fetched object; a later mutation is visible and a superseded object is not", async () => {
    const box = deferred<{ n: number; tag: string }>();
    const m = memoOf(box);
    const pending = m.get();
    const shared = { n: 1, tag: "live" };
    box.resolve(0, shared);
    expect(await pending).toBe(shared);
    shared.n = 5;
    shared.tag = "mutated";
    expect(m.peek()).toBe(shared);
    expect(m.peek()).toEqual({ n: 5, tag: "mutated" });
    expect(await m.get()).toBe(shared);

    const older = m.fresh();
    const newer = m.fresh();
    const oldObj = { n: 1, tag: "old" };
    const newObj = { n: 2, tag: "new" };
    box.resolve(2, newObj);
    expect(await newer).toBe(newObj);
    box.resolve(1, oldObj);
    expect(await older).toBe(oldObj);
    oldObj.n = 100;
    oldObj.tag = "clobber";
    expect(m.peek()).toBe(newObj);
    expect(m.peek()).toEqual({ n: 2, tag: "new" });
    newObj.tag = "kept";
    expect(m.peek()).toEqual({ n: 2, tag: "kept" });
  });

  test("a fetch that throws before returning a promise does not poison the next get", async () => {
    const box = deferred<string>();
    let throwNext = false;
    const m = createBankMemo<string>({
      fetch: () => {
        if (throwNext) {
          throwNext = false;
          throw new Error("sync");
        }
        return box.fetch();
      },
      now: () => box.clock,
    });
    const older = m.get();
    expect(box.calls).toBe(1);
    throwNext = true;
    expect(() => m.fresh()).toThrow("sync");
    box.resolve(0, "old");
    expect(await older).toBe("old");
    expect(m.peek()).toBeUndefined();
    const next = m.get();
    expect(box.calls).toBe(2);
    box.resolve(1, "next");
    expect(await next).toBe("next");
    expect(m.peek()).toBe("next");
  });

  test("invalidate then set: only the value set after the last bump is held", async () => {
    const box = deferred<string>();
    const m = memoOf(box);
    const inflight = m.fresh();
    m.invalidate();
    expect(m.peek()).toBeUndefined();
    const given = "exact";
    m.set(given);
    box.resolve(0, "late");
    expect(await inflight).toBe("late");
    expect(m.peek()).toBe(given);
    m.invalidate();
    expect(m.peek()).toBeUndefined();
    expect(box.calls).toBe(1);
    const again = m.get();
    expect(box.calls).toBe(2);
    box.resolve(1, "asked");
    expect(await again).toBe("asked");
  });
});
