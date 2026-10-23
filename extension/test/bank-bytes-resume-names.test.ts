// Review findings on G10, each failing against ff0ba38/0dd2984: the replaceBank bound is in UTF-8 bytes (not UTF-16
// units), and resume file names refuse text that can disguise or break the page/popup it is shown in.
import { describe, expect, test } from "bun:test";
import { MAX_BANK_BYTES, utf8Within } from "../src/bank";
import { checkToBackground } from "../src/messages";
import { checkResumeName } from "../src/resume";

describe("replaceBank bound is in UTF-8 bytes, as the constant's name says", () => {
  test("a payload of more than MAX_BANK_BYTES UTF-8 bytes but fewer UTF-16 units is refused before parse", () => {
    const pad = "€".repeat(Math.ceil(MAX_BANK_BYTES / 3) + 1); // 3 bytes each: > MAX bytes, ~MAX/3 units
    const json = `{"version":1,"answers":{},"settings":{},"pad":"${pad}"}`;
    expect(new TextEncoder().encode(json).length).toBeGreaterThan(MAX_BANK_BYTES);
    expect(json.length).toBeLessThan(MAX_BANK_BYTES);
    expect(checkToBackground({ type: "replaceBank", json })).toEqual({ error: "malformed replaceBank message" });
  });

  test("utf8Within is exact at every boundary: ASCII, 2-, 3- and 4-byte characters, and its two shortcuts", () => {
    for (const [s, bytes] of [["", 0], ["abc", 3], ["é", 2], ["€", 3], ["😀", 4], ["a€😀", 8]] as Array<[string, number]>) {
      expect([s, utf8Within(s, bytes)]).toEqual([s, true]);
      expect([s, utf8Within(s, bytes - 1)]).toEqual([s, false]);
    }
    expect(utf8Within("😀😀", 7)).toBe(false); // 4 units <= 7 but 8 bytes: needs the exact count
    expect(utf8Within("😀😀", 8)).toBe(true);
    expect(utf8Within("abcd", 3)).toBe(false); // more units than the cap: refused without encoding
    expect(utf8Within("ab", 6)).toBe(true); // units * 3 <= cap: accepted without encoding
  });
});

describe("resume file names", () => {
  const refused: Array<[string, string]> = [
    ["right-to-left override U+202E", "resume\u202Efdp.pdf"],
    ["left-to-right override U+202D", "resume\u202Dx.pdf"],
    ["embedding U+202A", "a\u202Ab.pdf"],
    ["pop directional U+202C", "a\u202Cb.pdf"],
    ["isolates U+2066..U+2069", "a\u2066b\u2069.pdf"],
    ["first strong isolate U+2068", "a\u2068b.pdf"],
    ["left-to-right mark U+200E", "a\u200Eb.pdf"],
    ["right-to-left mark U+200F", "a\u200Fb.pdf"],
    ["arabic letter mark U+061C", "a\u061Cb.pdf"],
    ["line separator U+2028", "a\u2028b.pdf"],
    ["paragraph separator U+2029", "a\u2029b.pdf"],
    ["zero-width space U+200B", "a\u200Bb.pdf"],
    ["byte-order mark inside U+FEFF", "a\uFEFFb.pdf"],
    ["lone high surrogate", "a\ud800b.pdf"],
    ["lone low surrogate", "a\udc00b.pdf"],
    ["reversed surrogate pair", "a\udc00\ud800b.pdf"],
    ["high surrogate before the extension", "resume\ud83d.pdf"],
  ];
  for (const [what, name] of refused) {
    test(`refuses ${what}`, () => {
      expect([JSON.stringify(name), checkResumeName(name)]).toEqual([JSON.stringify(name), null]);
    });
  }

  test("ordinary names, accents, CJK and a valid surrogate pair are kept", () => {
    for (const name of ["Resume_Synthetic.pdf", "résumé 日本語.PDF", "cv \u{1F600}.pdf", "a b.pdf"]) {
      expect([name, checkResumeName(name)]).toEqual([name, name]);
    }
  });
});
