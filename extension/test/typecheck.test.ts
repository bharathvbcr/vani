// The type-check gate (scripts/typecheck.sh) must never say "ok" unless the pinned compiler ran and passed.
// The script has no overrides: each test runs a COPY of it in a scratch extension/ (its own package.json and
// node_modules/.bin/tsc, and a stub `bun` first on PATH so the install step does not touch the network).
// The real compiler run is the gate itself (`bun run typecheck`).
import { describe, expect, test } from "bun:test";
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const script = join(import.meta.dir, "../scripts/typecheck.sh");
const PIN = "7.0.2";

/** A scratch extension/ with the script, a package.json pinning `pin`, and (unless null) a `tsc` file with this body. */
function scratch(tsc: string | null, pin = PIN): string {
  const root = mkdtempSync(join(tmpdir(), "vani-tc-"));
  mkdirSync(join(root, "scripts"));
  copyFileSync(script, join(root, "scripts/typecheck.sh"));
  writeFileSync(join(root, "package.json"), JSON.stringify({ name: "x", private: true, devDependencies: { typescript: pin } }));
  writeFileSync(join(root, "tsconfig.json"), "{}");
  if (tsc !== null) {
    mkdirSync(join(root, "node_modules/.bin"), { recursive: true });
    writeFileSync(join(root, "node_modules/.bin/tsc"), tsc);
    chmodSync(join(root, "node_modules/.bin/tsc"), 0o755);
  }
  const stub = join(root, "stub");
  mkdirSync(stub);
  writeFileSync(join(stub, "bun"), "#!/bin/sh\nexit 0\n");
  chmodSync(join(stub, "bun"), 0o755);
  return root;
}

function run(root: string): { code: number; out: string; err: string } {
  const r = Bun.spawnSync(["sh", join(root, "scripts/typecheck.sh")], { env: { PATH: `${join(root, "stub")}:/usr/bin:/bin` } });
  return { code: r.exitCode ?? -1, out: r.stdout.toString(), err: r.stderr.toString() };
}

const compiler = (version: string, exit: number) => `#!/bin/sh\nif [ "$1" = "--version" ]; then echo "Version ${version}"; exit 0; fi\nexit ${exit}\n`;

describe("typecheck gate", () => {
  test("a missing compiler exits non-zero, names the install step, and never prints ok", () => {
    const r = run(scratch(null));
    expect(r.code).not.toBe(0);
    expect(r.err).toContain("bun install --frozen-lockfile");
    expect(r.err).toContain("not installed");
    expect(r.out).not.toContain("ok");
  });

  test("an executable zero-byte tsc (a truncated install) fails and names the install step", () => {
    const r = run(scratch(""));
    expect(r.code).not.toBe(0);
    expect(r.err).toContain("bun install --frozen-lockfile");
    expect(r.out).not.toContain("ok");
  });

  test("a bogus tsc that exits 0 without naming the pinned version (true-like, wrong version, other tool) fails", () => {
    for (const body of ["#!/bin/sh\nexit 0\n", compiler("6.9.9", 0), compiler("7.0.20", 0), "#!/bin/sh\necho 'Version 7.0.2 extra'\nexit 0\n"]) {
      const r = run(scratch(body));
      expect([body, r.code]).not.toEqual([body, 0]);
      expect(r.out).not.toContain("ok");
      expect(r.err).toContain("bun install --frozen-lockfile");
    }
  });

  test("a package.json that pins no typescript fails", () => {
    const root = scratch(compiler(PIN, 0));
    writeFileSync(join(root, "package.json"), JSON.stringify({ name: "x", devDependencies: {} }));
    const r = run(root);
    expect(r.code).not.toBe(0);
    expect(r.out).not.toContain("ok");
  });

  test("a non-executable compiler file fails and never prints ok", () => {
    const root = scratch(compiler(PIN, 0));
    chmodSync(join(root, "node_modules/.bin/tsc"), 0o644);
    const r = run(root);
    expect(r.code).not.toBe(0);
    expect(r.out).not.toContain("ok");
    expect(r.err).toContain("not installed");
  });

  test("a pinned compiler that prints success but exits non-zero, exits 127, or is killed by a signal fails the gate", () => {
    const real = (exit: string) => `#!/bin/sh\nif [ "$1" = "--version" ]; then echo "Version ${PIN}"; exit 0; fi\n${exit}\n`;
    for (const tail of ["echo 'all good'\nexit 1", "exit 127", "kill -9 $$", "echo 'error TS2322' >&2\nexit 2"]) {
      const r = run(scratch(real(tail)));
      expect([tail, r.code]).not.toEqual([tail, 0]);
      expect(r.out).not.toContain("typecheck: ok");
      expect(r.err).toContain("tsc reported errors");
    }
  });

  test("bun install failing (the install step cannot run) fails the gate before the compiler is considered", () => {
    const root = scratch(compiler(PIN, 0));
    writeFileSync(join(root, "stub/bun"), "#!/bin/sh\nexit 1\n");
    const r = run(root);
    expect(r.code).not.toBe(0);
    expect(r.out).not.toContain("ok");
    expect(r.err).toContain("bun install --frozen-lockfile did not succeed");
  });

  test("the pinned compiler reporting errors fails the gate", () => {
    const r = run(scratch(compiler(PIN, 2)));
    expect(r.code).not.toBe(0);
    expect(r.out).not.toContain("ok");
    expect(r.err).toContain("tsc reported errors");
  });

  test("ok is printed only after the pinned compiler exits zero", () => {
    const r = run(scratch(compiler(PIN, 0)));
    expect(r.code).toBe(0);
    expect(r.out.trim()).toBe("typecheck: ok");
  });

  test("the shipped script has no environment override that could stand in for the compiler or skip the install", async () => {
    const text = await Bun.file(script).text();
    const code = text.split("\n").filter(l => !l.trimStart().startsWith("#")).join("\n");
    expect(code).not.toMatch(/VANI_|\$\{[A-Z_]+:-/);
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
