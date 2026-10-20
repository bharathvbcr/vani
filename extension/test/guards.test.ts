import { describe, expect, test } from "bun:test";
import { clean, clip, createLock, isRecord } from "../src/guards";

describe("isRecord", () => {
  test("accepts plain objects only", () => {
    expect(isRecord({})).toBe(true);
    expect(isRecord({ a: 1 })).toBe(true);
    for (const x of [null, undefined, [], [1], "s", 1, true]) expect(isRecord(x)).toBe(false);
  });
});

describe("createLock", () => {
  test("runs operations one at a time, in call order", async () => {
    const lock = createLock();
    const log: string[] = [];
    const slow = lock(async () => { log.push("a:start"); await new Promise(r => setTimeout(r, 20)); log.push("a:end"); return 1; });
    const fast = lock(async () => { log.push("b"); return 2; });
    expect(await Promise.all([slow, fast])).toEqual([1, 2]);
    expect(log).toEqual(["a:start", "a:end", "b"]);
  });

  test("a failure reaches its own caller and does not stop the queue", async () => {
    const lock = createLock();
    const bad = lock(async () => { throw new Error("boom"); });
    const good = lock(async () => "ok");
    await expect(bad).rejects.toThrow("boom");
    expect(await good).toBe("ok");
  });

  test("separate locks do not wait on each other", async () => {
    const a = createLock();
    const b = createLock();
    let release: () => void = () => {};
    const held = a(() => new Promise<void>(r => { release = r; }));
    expect(await b(async () => "free")).toBe("free");
    release();
    await held;
  });
});

describe("clean and clip", () => {
  test("clean folds whitespace and tolerates null", () => {
    expect(clean("  a \n\t b  ")).toBe("a b");
    expect(clean(null)).toBe("");
    expect(clean(undefined)).toBe("");
  });
  test("clip cuts only when over the limit", () => {
    expect(clip("abcdef", 3)).toBe("abc");
    expect(clip("abc", 3)).toBe("abc");
    expect(clip("", 0)).toBe("");
  });
});
