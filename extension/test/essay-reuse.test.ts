import { describe, expect, test } from "bun:test";
import { mergeCapture } from "../src/bank";
import { emptyBank } from "../src/types";
import { heldReview, matchField } from "../src/match";
import type { FieldInfo } from "../src/types";

const NOW = new Date("2026-11-08T12:00:00Z");
const SAME = "https://boards.example/acme/jobs/1";
const OTHER = "https://boards.example/other/jobs/2";

function field(label: string, value: string[] = []): FieldInfo {
  return { kind: "textarea", label, name: "", autocomplete: "", automationId: "", options: [], required: false, value };
}

describe("a review answer stays on its posting", () => {
  test("the same posting key fills yellow, a different key stays empty and can be used", () => {
    const text = "s".repeat(300);
    const bank = mergeCapture(emptyBank(), [field("Describe a project you built", [text])], "boards.example", NOW, SAME).bank;
    const saved = Object.values(bank.answers)[0]!;
    expect(saved.review).toBe(true);
    expect(saved.postingKey).toBe(SAME);

    const again = matchField(field("Describe a project you built"), bank, "boards.example", NOW.getTime(), SAME);
    expect(again?.value).toEqual([text]);
    expect(again?.review).toBe(true);

    const elsewhere = matchField(field("Describe a project you built"), bank, "boards.example", NOW.getTime(), OTHER);
    expect(elsewhere).toBeNull();
    const offer = heldReview(field("Describe a project you built"), bank, "boards.example", OTHER, NOW.getTime());
    expect(offer?.value).toEqual([text]);
    expect(offer?.review).toBe(true);
  });

  test("a page with no posting key keeps today's yellow fill", () => {
    const text = "s".repeat(300);
    const bank = mergeCapture(emptyBank(), [field("Describe a project you built", [text])], "boards.example", NOW, SAME).bank;
    const open = matchField(field("Describe a project you built"), bank, "boards.example", NOW.getTime(), null);
    expect(open?.value).toEqual([text]);
    expect(open?.review).toBe(true);
    expect(heldReview(field("Describe a project you built"), bank, "boards.example", null)).toBeNull();
  });

  test("an empty posting key on the answer does not invent a hold", () => {
    const text = "short note";
    const bank = mergeCapture(emptyBank(), [field("Nickname", [text])], "boards.example", NOW, SAME).bank;
    const saved = Object.values(bank.answers)[0]!;
    expect(saved.review).toBe(false);
    expect(saved.postingKey).toBeUndefined();
    expect(matchField(field("Nickname"), bank, "boards.example", NOW.getTime(), OTHER)?.value).toEqual([text]);
  });
});
