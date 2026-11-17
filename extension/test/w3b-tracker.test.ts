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

  test("a custom combobox is captured after a real click on its popup option, and not after the same script events", async () => {
    const s = await scenarios();
    expect(s.comboTrusted, "a trusted click in the owned listbox marks the combobox").toBe(1);
    expect(s.comboHostile, "synthetic pointerdown, click and change on the option capture nothing").toBe(0);
  }, 20_000);

  test("a click in a listbox marks only a combobox that owns it: not a page's plain claims, an ancestor-held one, or one of several", async () => {
    const s = await scenarios();
    expect(s.ownerPlain, "controls naming the listbox without a combobox role are not marked").toBe(0);
    expect(s.ownerAbove, "a combobox inside the listbox is not its owner").toBe(0);
    expect(s.ownerTwo, "two claimants: none marked").toBe(0);
    expect(s.ownerWrongPopup, "aria-haspopup=menu is not a listbox owner").toBe(0);
    expect(s.ownerOneRole, "the single role=combobox owner is marked").toBe(1);
    expect(s.ownerOnePopup, "the single aria-haspopup=listbox owner is marked").toBe(1);
  }, 20_000);

  test("a click inside nested fields marks the deepest one only", async () => {
    const s = await scenarios();
    expect(s.nestedOnlyInner).toBe(1);
  }, 20_000);

  test("a real click on a label marks the control it names", async () => {
    const s = await scenarios();
    expect(s.labelControl).toBe(1);
  }, 20_000);

  test("an explicit Save captures untouched, script-set values, with the strict visibility scan", async () => {
    const s = await scenarios();
    expect(s.explicitSent).toBe(3);
    expect(s.explicitAdded).toBe(3);
    expect(s.explicitStrict, "every capture scan asks for the strict visibility test").toBe(1);
  }, 20_000);
});
