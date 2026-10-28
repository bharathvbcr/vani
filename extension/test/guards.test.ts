import { describe, expect, test } from "bun:test";
import { clean, clip, createLock, isRecord, maxLengthOf, overLimit, storedLength } from "../src/guards";

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

describe("maxLengthOf, storedLength and overLimit", () => {
  const el = (localName: string, attrs: Record<string, string> = {}, extra: object = {}): Element =>
    ({ localName, tagName: localName.toUpperCase(), getAttribute: (n: string) => attrs[n] ?? null, ...extra }) as unknown as Element;
  test("only an input of a type maxlength applies to, or a textarea, has a limit", () => {
    expect(maxLengthOf(el("input", { maxlength: "5" }))).toBe(5);
    expect(maxLengthOf(el("input", { maxlength: "5", type: "number" }, { type: "number" }))).toBeNull();
    expect(maxLengthOf(el("textarea", { maxlength: "7" }))).toBe(7);
    for (const tag of ["div", "select", "button"]) expect(maxLengthOf(el(tag, { maxlength: "5" }))).toBeNull();
  });
  test("0 is a limit; absent, negative and malformed are not", () => {
    expect(maxLengthOf(el("input", { maxlength: "0" }))).toBe(0);
    for (const v of ["-1", "-3", "abc", ""]) expect(maxLengthOf(el("input", { maxlength: v }))).toBeNull();
    expect(maxLengthOf(el("input"))).toBeNull();
  });
  test("the maxLength property wins over the attribute, and is read afresh each time", () => {
    const e = el("input", { maxlength: "9" }, { maxLength: 4 });
    expect(maxLengthOf(e)).toBe(4);
    Object.assign(e, { maxLength: -1 });
    expect(maxLengthOf(e)).toBeNull();
  });
  test("length is UTF-16 code units; a textarea keeps a line break as one, an input drops it", () => {
    expect(storedLength(el("input"), "\u{1F600}")).toBe(2);
    expect(storedLength(el("input"), "é")).toBe(2);
    expect(storedLength(el("input"), "a\r\nb\nc")).toBe(3);
    expect(storedLength(el("textarea"), "a\r\nb\rc\nd")).toBe(7);
  });
  test("overLimit names the limit and the length, and is null at the limit", () => {
    const e = el("textarea", { maxlength: "3" });
    expect(overLimit(e, "abc")).toBeNull();
    expect(overLimit(e, "abcd")).toEqual({ limit: 3, length: 4 });
    expect(overLimit(el("div", { maxlength: "1" }), "abcd")).toBeNull();
  });
});
