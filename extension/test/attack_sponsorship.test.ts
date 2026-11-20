import { expect, test } from "bun:test";
import { parseSponsorship } from "../src/relevance";
const cases: [string, boolean | null][] = [["No.",false],["N/A",null],["Not required",false],["Will not need",false],["I am a US citizen",null],["NO",false],["YES",true],["Yes, I will require sponsorship",true],["I require sponsorship",true],["I will need sponsorship in future",true],["Non",null],["Sí",null],["nein",null],["<p>No</p>",false],["no sponsorship needed",false],["Yes, I do not hold work authorization",true],["N",null],["false",false],["TRUE",true],["  \n No \n",false],["Nobody",null],["unknown",null],["Not sure",null],["I don't require sponsorship",false],["I do require sponsorship",true],["Yes, I don't have a green card",true],["NO​",false],["no sponsorship",false]];
for (const [s, want] of cases) {
  test("attack parseSponsorship " + JSON.stringify(s), () => { expect(parseSponsorship(s)).toBe(want === null ? undefined : want); });
}
test("attack parseSponsorship very long answer stays fast", () => {
  const t0 = performance.now();
  expect(parseSponsorship("a ".repeat(200000) + "require")).toBe(true);
  expect(performance.now() - t0).toBeLessThan(500);
});
