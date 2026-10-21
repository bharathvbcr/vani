// Adversarial coverage for the typecheck gate's failure paths and the popup's link helper.
import { describe, expect, test } from "bun:test";
import { chmodSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { isHttpUrl, openButton, titleElement } from "../src/joblink";

const script = join(import.meta.dir, "../scripts/typecheck.sh");
function run(env: Record<string, string>): { code: number; out: string; err: string } {
  const r = Bun.spawnSync(["sh", script], { env: { PATH: process.env.PATH ?? "", VANI_SKIP_INSTALL: "1", ...env } });
  return { code: r.exitCode ?? -1, out: r.stdout.toString(), err: r.stderr.toString() };
}
function fakeTsc(body: string, mode = 0o755): string {
  const tsc = join(mkdtempSync(join(tmpdir(), "tsc-")), "tsc");
  writeFileSync(tsc, body);
  chmodSync(tsc, mode);
  return tsc;
}

describe("adversarial: typecheck gate failure paths", () => {
  test("a non-executable compiler file fails and never prints ok", () => {
    const r = run({ VANI_TSC: fakeTsc("#!/bin/sh\necho 'typecheck: ok'\nexit 0\n", 0o644) });
    expect(r.code).not.toBe(0);
    expect(r.out).not.toContain("ok");
    expect(r.err).toContain("not installed");
  });

  test("a compiler that prints success but exits non-zero, exits 127, or is killed by a signal fails the gate", () => {
    for (const body of ["#!/bin/sh\necho 'all good'\nexit 1\n", "#!/bin/sh\nexit 127\n", "#!/bin/sh\nkill -9 $$\n", "#!/bin/sh\necho 'error TS2322' >&2\nexit 2\n"]) {
      const r = run({ VANI_TSC: fakeTsc(body) });
      expect([body, r.code]).not.toEqual([body, 0]);
      expect(r.out).not.toContain("typecheck: ok");
      expect(r.err).toContain("tsc reported errors");
    }
  });

  test("bun install failing (bun not on PATH, the install step not skipped) fails the gate before the compiler is considered", () => {
    const r = Bun.spawnSync(["/bin/sh", script], { env: { PATH: "/nonexistent", VANI_TSC: "/usr/bin/true" } });
    expect(r.exitCode).not.toBe(0);
    expect(r.stdout.toString()).not.toContain("ok");
  });
});

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
