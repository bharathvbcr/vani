import { expect, test } from "bun:test";
import { parseSponsorship } from "../src/relevance";
const cases: [string, boolean | undefined][] = [["No.",false],["Nope",false],["N/A",null],["Not required",false],["Will not need",false],["I am a US citizen",null],["NO",false],["YES",true],["Yes, I will require sponsorship",true],["I require sponsorship",true],["I will need sponsorship in future",true],["I do not know",null],["I cannot work without sponsorship",true],["I require sponsorship, no exceptions",true],["Not yet decided",null],["Not applicable",null],["none",false],["Non",null],["Sí",null],["nein",null],["<p>No</p>",false],["<b>Yes</b>",true],["<p>Yes</p>",true],["no sponsorship needed",false],["Yes, I do not hold work authorization",true],["Yes - no one else needs to sponsor",true],["N",null],["false",false],["TRUE",true],["no, but I will require it after OPT",true],["  \n No \n",false],["Nobody",null],["unknown",null],["Not sure",null],["I don't require sponsorship",false],["I do require sponsorship",true],["Yes, I don't have a green card",true],["nō",null],["NO​",false],["no sponsorship",false]];
for (const [s, want] of cases) {
  test("parseSponsorship " + JSON.stringify(s), () => { expect(parseSponsorship(s)).toBe(want === null ? undefined : want); });
}
test("very long answer is fast and decided", () => {
  const t0 = performance.now();
  expect(parseSponsorship("a ".repeat(200000) + "require")).toBe(true);
  expect(performance.now() - t0).toBeLessThan(500);
});
test("unclosed tag flood is fast and undecided", () => {
  const t0 = performance.now();
  expect(parseSponsorship("<".repeat(200000))).toBeUndefined();
  expect(performance.now() - t0).toBeLessThan(500);
});
