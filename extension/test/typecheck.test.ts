// The type-check gate (scripts/typecheck.sh) must never say "ok" unless the compiler ran and passed.
// The real compiler run is the gate itself (`bun run typecheck`); here the compiler path is swapped
// (VANI_TSC) and the install step skipped (VANI_SKIP_INSTALL) so the script's own logic is what is tested.
import { describe, expect, test } from "bun:test";
import { join } from "node:path";

const script = join(import.meta.dir, "../scripts/typecheck.sh");

function run(env: Record<string, string>): { code: number; out: string; err: string } {
  const r = Bun.spawnSync(["sh", script], { env: { PATH: process.env.PATH ?? "", VANI_SKIP_INSTALL: "1", ...env } });
  return { code: r.exitCode ?? -1, out: r.stdout.toString(), err: r.stderr.toString() };
}

describe("typecheck gate", () => {
  test("a missing compiler exits non-zero, names the install step, and never prints ok", () => {
    for (const tsc of ["/nonexistent/dir/tsc", join(import.meta.dir, "fixtures")]) {
      const r = run({ VANI_TSC: tsc });
      expect(r.code).not.toBe(0);
      expect(r.err).toContain("bun install --frozen-lockfile");
      expect(r.err).toContain("not installed");
      expect(r.out).not.toContain("ok");
    }
  });

  test("a compiler that reports errors fails the gate", () => {
    const r = run({ VANI_TSC: "/usr/bin/false" });
    expect(r.code).not.toBe(0);
    expect(r.out).not.toContain("ok");
    expect(r.err).toContain("tsc reported errors");
  });

  test("ok is printed only after the compiler exits zero", () => {
    const r = run({ VANI_TSC: "/usr/bin/true" });
    expect(r.code).toBe(0);
    expect(r.out.trim()).toBe("typecheck: ok");
  });

  test("the gate never uses bunx or npx, which would download a missing compiler", async () => {
    const text = await Bun.file(script).text();
    const code = text.split("\n").filter(l => !l.trimStart().startsWith("#")).join("\n");
    expect(code).not.toMatch(/\b(bunx|npx)\b/);
    expect(code).toContain("bun install --frozen-lockfile");
    expect(code).toContain("./node_modules/.bin/tsc");
  });

  test("typescript is an exact-pinned devDependency and not a runtime dependency", async () => {
    const pkg = JSON.parse(await Bun.file(join(import.meta.dir, "../package.json")).text()) as Record<string, unknown>;
    expect(pkg.private).toBe(true);
    expect(pkg.dependencies).toBeUndefined();
    const dev = pkg.devDependencies as Record<string, string>;
    expect(Object.keys(dev)).toEqual(["typescript"]);
    expect(dev.typescript).toMatch(/^\d+\.\d+\.\d+$/); // no caret, tilde or range
  });
});
