// Safari decodes an extension script with no charset as windows-1252. A literal non-ASCII character in a
// bundle then turns into mojibake, and inside a regex class it can be a SyntaxError that kills the whole
// background script ("Invalid regular expression: range out of order in character class").
// The bundles must therefore be pure ASCII and must parse the same under either decoding.
import { describe, expect, test } from "bun:test";
import { spawnSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import { join } from "node:path";

const root = join(import.meta.dir, "..");

/** Parse `bytes` as windows-1252 text, the way Safari reads a script with no charset; returns the error, if any. */
function parseAs1252(bytes: Uint8Array): string | null {
  try {
    new Function(new TextDecoder("windows-1252").decode(bytes));
    return null;
  } catch (e) {
    return String(e);
  }
}

describe("built bundles", () => {
  const built = spawnSync(process.execPath, ["run", join(root, "build.ts")], { encoding: "utf8", timeout: 120_000 });

  test("build ran", () => {
    expect(built.error).toBeUndefined();
    expect(built.status).toBe(0);
  });

  for (const dist of ["dist-chrome", "dist-safari"]) {
    test(`${dist} scripts are ASCII and parse under windows-1252`, () => {
      const files = readdirSync(join(root, dist)).filter(f => f.endsWith(".js"));
      expect(files).toEqual(expect.arrayContaining(["background.js", "content.js", "popup.js"]));
      for (const f of files) {
        const bytes = readFileSync(join(root, dist, f));
        const high = bytes.findIndex(b => b > 0x7f);
        expect({ f, high }).toEqual({ f, high: -1 });
        expect({ f, err: parseAs1252(bytes) }).toEqual({ f, err: null });
      }
    });
  }

  test("the probe catches the original failure", () => {
    // The pre-fix line from background.js: U+0300 and U+036F written as literal UTF-8.
    const bytes = new TextEncoder().encode('"café".normalize("NFD").replace(/[̀-ͯ]/g, "");');
    expect(parseAs1252(bytes)).toContain("range out of order");
  });

  test("escaped combining-mark regex still strips accents", () => {
    const bg = readFileSync(join(root, "dist-safari/background.js"), "latin1");
    expect(bg).toContain("/[\\u0300-\\u036f]/g");
    expect(new Function('return "Renée".normalize("NFD").replace(/[\\u0300-\\u036f]/g, "")')()).toBe("Renee");
  });
});
