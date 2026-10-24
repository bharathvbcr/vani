import { describe, expect, test } from "bun:test";
import type { FieldInfo } from "../src/types";
import { MIN_POSTING_CHARS, draftButton, draftEnabled, draftOffer, isDraftOffer, pickOrdinal } from "../src/suggest";

function field(p: Partial<FieldInfo>): FieldInfo {
  return { kind: "text", label: "Why this team?", name: "", autocomplete: "", automationId: "", options: [], required: false, value: [], ...p };
}

const POSTING = 80;

describe("draftOffer", () => {
  test("an empty free-text field with a posting can be drafted, and the offer holds no field text", () => {
    const o = draftOffer(field({ value: ["   "] }), "", POSTING);
    expect(o).toMatchObject({ offer: "draft", button: "Draft", chip: "Empty" });
    expect(o.note).toContain("question and the posting only");
    expect(JSON.stringify(o)).not.toContain("secret");
    expect(draftEnabled(o.offer)).toBe(true);
    expect(draftButton(o.offer)).toBe("Draft");
  });

  test("regenerate only when the stored wording still matches, and neither wording is in the offer", () => {
    const written = "I build the inference stack for this team.";
    const untracked = draftOffer(field({ value: [written], drafted: true }), "", POSTING);
    expect(untracked.offer).toBe("replace");
    expect(untracked.button).toBe("New draft");
    expect(untracked.note).toContain("original wording was not recorded");
    expect(JSON.stringify(untracked)).not.toContain(written);
    expect(JSON.stringify(untracked)).not.toContain("inference");

    const edited = "I rewrote this myself.";
    const changed = draftOffer(field({ value: [edited], drafted: true, draftText: written }), "", POSTING);
    expect(changed.offer).toBe("replace");
    expect(changed.note).toContain("edited this draft");
    expect(JSON.stringify(changed)).not.toContain(edited);
    expect(JSON.stringify(changed)).not.toContain(written);

    const same = draftOffer(field({ value: [`  ${written}  `], drafted: true, draftText: written }), "", POSTING);
    expect(same).toMatchObject({ offer: "regenerate", button: "Regenerate" });
    expect(same.note).toContain("does not send what is in the field");
    expect(JSON.stringify(same)).not.toContain("inference");
  });

  test("clearing a drafted field offers a fresh draft, and the user's own text asks before replacing", () => {
    expect(draftOffer(field({ value: [], drafted: true }), "", POSTING).offer).toBe("draft");
    const own = "I already wrote this myself.";
    const o = draftOffer(field({ value: [own] }), "", POSTING);
    expect(o.offer).toBe("replace");
    expect(o.button).toBe("New draft");
    expect(o.note).toContain("until you confirm");
    expect(JSON.stringify(o)).not.toContain(own);
  });

  test("a short, empty or non-finite posting blocks drafting and still explains why", () => {
    expect(MIN_POSTING_CHARS).toBe(40);
    for (const n of [0, 39, Number.NaN, Number.POSITIVE_INFINITY]) {
      const o = draftOffer(field({}), "", n);
      expect(o.offer).toBe("needs-posting");
      expect(o.button).toBe("Draft");
      expect(draftEnabled(o.offer)).toBe(false);
      expect(o.note.length).toBeGreaterThan(0);
    }
    expect(draftOffer(field({}), "", 40).offer).toBe("draft");
  });

  test("choice, other, secret, self-id and manual fields are not drafted", () => {
    expect(draftOffer(field({ kind: "select", options: ["A"] }), "", POSTING).offer).toBe("choice");
    expect(draftOffer(field({ kind: "radio", options: ["A"] }), "", POSTING).offer).toBe("choice");
    expect(draftOffer(field({ kind: "checkbox" }), "", POSTING).offer).toBe("choice");
    expect(draftOffer(field({ kind: "email" }), "", POSTING)).toMatchObject({ offer: "other", button: "" });
    expect(draftOffer(field({ kind: "tel" }), "", POSTING).offer).toBe("other");
    expect(draftOffer(field({ label: "Social Security Number", value: ["000"] }), "", POSTING).offer).toBe("other");
    expect(draftOffer(field({ label: "Gender" }), "", POSTING).offer).toBe("other");
    expect(JSON.stringify(draftOffer(field({ label: "Social Security Number", value: ["000-00-0000"] }), "", POSTING))).not.toContain("000");
    const manual = draftOffer(field({}), "date or dialog picker", POSTING);
    expect(manual).toMatchObject({ offer: "manual", button: "", chip: "By hand" });
    expect(manual.note).toBe("date or dialog picker.");
    expect(draftOffer(field({}), "Fill it yourself.", POSTING).note).toBe("Fill it yourself.");
  });

  test("an ordinal is trusted only while it still names that label", () => {
    const labels = ["Why us?", "Phone", "Why us?"];
    const at = (i: number) => labels[i] ?? "";
    expect(pickOrdinal(labels.length, "Why us?", 2, at)).toBe(2);
    expect(pickOrdinal(labels.length, "Why us?", undefined, at)).toBe("ambiguous");
    expect(pickOrdinal(labels.length, "Phone", undefined, at)).toBe(1);
    expect(pickOrdinal(labels.length, "Why us?", 1, at)).toBe("absent");
    expect(pickOrdinal(labels.length, "Nope", undefined, at)).toBe("absent");
    expect(pickOrdinal(labels.length, "Phone", 9, at)).toBe("absent");
    expect(pickOrdinal(labels.length, "Phone", 1.5, at)).toBe("absent");
    expect(pickOrdinal(labels.length, "Phone", -1, at)).toBe("absent");
  });

  test("only the six known offers pass the guard", () => {
    for (const o of ["draft", "regenerate", "replace", "needs-posting", "choice", "manual", "other"]) expect(isDraftOffer(o)).toBe(true);
    for (const bad of ["", "Draft", "regen", 1, null, undefined, "skipped"]) expect(isDraftOffer(bad)).toBe(false);
  });
});
