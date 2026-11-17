// Slice w3b (ft-bf5e): a field is captured only after a trusted user event on it. The scenarios live in
// w3b-tracker-run.ts, run in its own bun process because it fakes the form scan.
import { describe, expect, test } from "bun:test";
import { join } from "node:path";

async function scenarios(): Promise<Record<string, number>> {
  const p = Bun.spawn([process.execPath, "run", join(import.meta.dir, "w3b-tracker-run.ts")], { stdout: "pipe", stderr: "pipe" });
  const [text, err] = await Promise.all([new Response(p.stdout).text(), new Response(p.stderr).text()]);
  expect(await p.exited, err).toBe(0);
  return JSON.parse(text.trim().split("\n").pop() ?? "{}") as Record<string, number>;
}

describe("w3b: provenance of captured fields", () => {
  test("a hostile page's synthetic events capture nothing and raise no Applied prompt", async () => {
    const s = await scenarios();
    expect(s.hostilePending, "no field became pending").toBe(0);
    expect(s.hostileSends, "nothing left the page").toBe(0);
    expect(s.hostileSubmitPrompts, "an untrusted click on Submit raised no Applied prompt").toBe(0);
  }, 20_000);

  test("real typing is still captured, and only on the fields it touched", async () => {
    const s = await scenarios();
    expect(s.typedSent).toBe(2);
    expect(s.typedNames).toBe(1);
    expect(s.trustedSubmitPrompts, "a trusted Submit click still prompts").toBe(1);
    expect(s.trustedSubmitSends, "and flushes what was typed").toBeGreaterThan(0);
  }, 20_000);

  test("an explicit Save captures untouched, script-set values, with the strict visibility scan", async () => {
    const s = await scenarios();
    expect(s.explicitSent).toBe(3);
    expect(s.explicitAdded).toBe(3);
    expect(s.explicitStrict, "every capture scan asks for the strict visibility test").toBe(1);
  }, 20_000);
});
