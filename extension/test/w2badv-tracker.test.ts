// Slice w2b adversarial (w2badv): the tracker's fail-closed pagehide drops captures the background would have accepted.
// The scenarios live in w2badv-tracker-run.ts, run in its own bun process because it fakes the form scan.
import { describe, expect, test } from "bun:test";
import { join } from "node:path";

describe("w2badv: pagehide fail-closed must not lose a legitimate capture", () => {
  test("a pagehide with a slow first bank read, a leave inside the commit window, or a transient null read still sends", async () => {
    const p = Bun.spawn([process.execPath, "run", join(import.meta.dir, "w2badv-tracker-run.ts")], { stdout: "pipe", stderr: "pipe" });
    const [text, err] = await Promise.all([new Response(p.stdout).text(), new Response(p.stderr).text()]);
    expect(await p.exited, err).toBe(0);
    const sends = JSON.parse(text.trim().split("\n").pop() ?? "{}") as Record<string, number>;
    // Same scenarios at 10df9f9 send once each: the background merges them (and now enforces disabledHosts itself).
    expect(sends.slowFirstRead, "pagehide while the first getBank is still in flight").toBeGreaterThan(0);
    expect(sends.leaveInsideCommitWindow, "pagehide inside the 250 ms commit window").toBeGreaterThan(0);
    expect(sends.transientNullAfterGoodRead, "one null read after a good one must not erase the known bank").toBeGreaterThan(0);
    // Not a break: a throwing read leaves the pending set intact and the next flush sends it.
    expect(sends.retryAfterThrow).toBe(1);
  }, 20_000);
});
