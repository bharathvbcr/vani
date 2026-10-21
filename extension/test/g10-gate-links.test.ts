// Adversarial coverage for the popup's link helper (the typecheck gate's failure paths moved to typecheck.test.ts).
import { describe, expect, test } from "bun:test";
import { isHttpUrl, openButton, titleElement } from "../src/joblink";

describe("adversarial: the popup's link helper", () => {
  const doc = { createElement: (tag: string) => ({ tagName: tag.toUpperCase(), textContent: "", className: "", href: undefined, target: undefined, onclick: null }) } as unknown as Pick<Document, "createElement">;

  test("only an absolute http(s) URL is a link; scheme-only, hostless, relative, odd-case and smuggled schemes are not, or are normalised", () => {
    for (const bad of ["http:", "https:", "http://", "https://", "//evil.example/x", "/x", "x", "HTTP", "ftp://a.example/", "java\nscript:alert(1)", "javascript:alert(1)", "vbscript:x", "blob:https://a.example/1", "about:blank", "chrome-extension://a/popup.html", "data:text/html,x", "\u0000javascript:alert(1)"]) {
      expect([bad, isHttpUrl(bad)]).toEqual([bad, false]);
      expect(titleElement(doc, "T", bad).tagName).toBe("SPAN");
      expect(openButton(doc, bad, () => { throw new Error("opened"); })).toBeNull();
    }
    for (const nonString of [null, undefined, 0, {}, [], ["https://a.example/"], true]) expect(isHttpUrl(nonString)).toBe(false);
    for (const ok of ["HTTPS://a.example/x", "http://a.example", "https://user:pw@a.example/x?y#z", "\thttps://a.example/x"]) expect([ok, isHttpUrl(ok)]).toEqual([ok, true]);
  });

  test("a 1 MB http URL does not throw", () => {
    expect(isHttpUrl(`https://a.example/${"x".repeat(1_000_000)}`)).toBe(true);
    expect(isHttpUrl(`javascript:${"x".repeat(1_000_000)}`)).toBe(false);
  });
});
