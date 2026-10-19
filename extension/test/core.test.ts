import { describe, expect, test } from "bun:test";
import { type Bank, type FieldInfo, emptyBank } from "../src/types";
import { autocompleteKey, jaccard, normalizeLabel, signature, siteKey, tokens } from "../src/normalize";
import { classify } from "../src/classify";
import { MAX_ANSWERS, exportBank, mergeCapture, parseBank } from "../src/bank";
import { mapChoice, matchField } from "../src/match";

const now = new Date("2026-10-18T12:00:00Z");

function field(p: Partial<FieldInfo>): FieldInfo {
  return { kind: "text", label: "", name: "", autocomplete: "", automationId: "", options: [], required: false, value: [], ...p };
}

describe("normalize", () => {
  test("required markers, punctuation and case fold away", () => {
    expect(normalizeLabel("First Name *")).toBe("first name");
    expect(normalizeLabel("LinkedIn Profile (optional):")).toBe("linkedin profile");
    expect(normalizeLabel("  What’s your   e-mail?  ")).toBe("whats your e mail");
  });
  test("stems let wordings meet", () => {
    const a = tokens("Will you now or in the future require sponsorship?");
    const b = tokens("Do you require visa sponsorship now or in the future");
    expect(jaccard(a, b)).toBeGreaterThanOrEqual(0.6);
  });
  test("common rewordings meet", () => {
    expect(jaccard(tokens("LinkedIn Profile"), tokens("LinkedIn URL"))).toBe(1);
    expect(jaccard(tokens("Are you legally authorized to work in the United States?"), tokens("Are you authorized to work in the US?"))).toBeGreaterThanOrEqual(0.6);
    expect(jaccard(tokens("Mobile phone number"), tokens("Phone"))).toBe(1);
    // Different questions stay apart.
    expect(jaccard(tokens("Why do you want to work here?"), tokens("Why do you want to join us?"))).toBeLessThan(0.6);
    expect(jaccard(tokens("GitHub URL"), tokens("LinkedIn URL"))).toBe(0);
  });
  test("autocomplete keys use the last token", () => {
    expect(autocompleteKey("section-a given-name")).toBe("first name");
    expect(autocompleteKey("on")).toBe("");
  });
  test("signatures skip per-job generated names", () => {
    expect(signature("boards.greenhouse.io", { name: "first_name", automationId: "" })).toBe("greenhouse.io|first_name");
    expect(signature("boards.greenhouse.io", { name: "question_8123456", automationId: "" })).toBe("");
    expect(signature("x.io", { name: "job_application[answers_attributes][3][text_value]", automationId: "" })).toBe("");
    expect(signature("x.io", { name: "", automationId: "" })).toBe("");
    expect(siteKey("acme.wd5.myworkdayjobs.com")).toBe("myworkdayjobs.com");
  });
  test("site keys keep the company under a two-part country suffix", () => {
    // Two companies under .co.uk used to share the key "co.uk", so one's
    // learned field signatures filled the other's forms.
    expect(siteKey("jobs.acme.co.uk")).toBe("acme.co.uk");
    expect(siteKey("careers.other.co.uk")).toBe("other.co.uk");
    expect(signature("jobs.acme.co.uk", { name: "first_name", automationId: "" })).not.toBe(
      signature("careers.other.co.uk", { name: "first_name", automationId: "" }),
    );
    expect(siteKey("apply.example.com.au")).toBe("example.com.au");
    expect(siteKey("Jobs.Example.CO.JP.")).toBe("example.co.jp");
    expect(siteKey("x.ac.uk")).toBe("x.ac.uk");
    expect(siteKey("acme.co.uk")).toBe("acme.co.uk");
    // The suffix alone, and ordinary hosts, are unchanged.
    expect(siteKey("co.uk")).toBe("co.uk");
    expect(siteKey("boards.greenhouse.io")).toBe("greenhouse.io");
    expect(siteKey("jobs.lever.co")).toBe("lever.co");
    expect(siteKey("www.acme.de")).toBe("acme.de");
    // Only a two-letter country code takes three labels.
    expect(siteKey("jobs.acme.co.com")).toBe("co.com");
  });
});

describe("classify", () => {
  test("secrets are recognised by kind, autocomplete and label", () => {
    expect(classify(field({ kind: "password", label: "Create password" }))).toBe("secret");
    expect(classify(field({ label: "Card", autocomplete: "cc-number" }))).toBe("secret");
    expect(classify(field({ label: "Social Security Number" }))).toBe("secret");
    expect(classify(field({ label: "Date of Birth" }))).toBe("secret");
    expect(classify(field({ label: "x", name: "ssn" }))).toBe("secret");
  });
  test("self-id is separate from secrets", () => {
    expect(classify(field({ kind: "select", label: "Gender" }))).toBe("self_id");
    expect(classify(field({ kind: "radio", label: "Are you a protected veteran?" }))).toBe("self_id");
    expect(classify(field({ label: "Email" }))).toBe("standard");
    expect(classify(field({ label: "Pinterest profile" }))).toBe("standard"); // "pin" is a whole word only
  });
});

describe("capture", () => {
  const fields = [
    field({ label: "First Name *", name: "first_name", autocomplete: "given-name", value: ["Sam"] }),
    field({ label: "Email", kind: "email", name: "email", value: ["sam@example.invalid"] }),
    field({ label: "Password", kind: "password", value: ["hunter2"] }),
    field({ label: "SSN", value: ["000-00-0000"] }),
    field({ label: "Gender", kind: "select", options: ["Male", "Female", "Decline"], value: ["Decline"] }),
    field({ label: "Resume", kind: "file", value: ["cv.pdf"] }),
    field({ label: "Website", value: [] }),
    field({ label: "Why us?", kind: "textarea", value: ["x".repeat(400)] }),
  ];

  test("stores standard answers and refuses secrets and unopted self-id", () => {
    const r = mergeCapture(emptyBank(), fields, "boards.greenhouse.io", now);
    expect(Object.keys(r.bank.answers).sort()).toEqual(["email", "first name", "why us"]);
    expect(r.added).toBe(3);
    expect(r.skippedSecret).toBe(2);
    expect(r.skippedSelfId).toBe(1);
    expect(r.skippedEmpty).toBe(1);
    expect(JSON.stringify(r.bank)).not.toContain("hunter2");
    expect(JSON.stringify(r.bank)).not.toContain("000-00-0000");
    expect(r.bank.answers["first name"]!.signatures).toEqual(["greenhouse.io|first_name"]);
    expect(r.bank.answers["why us"]!.review).toBe(true);
  });

  test("self-id is stored once enabled", () => {
    const b = emptyBank();
    b.settings.captureSelfId = true;
    const r = mergeCapture(b, fields, "boards.greenhouse.io", now);
    expect(r.bank.answers["gender"]!.sensitivity).toBe("self_id");
  });

  test("a second capture updates, keeps wordings, and the latest value wins", () => {
    const first = mergeCapture(emptyBank(), [field({ label: "Phone number", value: ["111"] })], "a.com", now).bank;
    const second = mergeCapture(first, [field({ label: "Phone Number:", value: ["222"] })], "b.com", now);
    expect(second.updated).toBe(1);
    expect(second.bank.answers["phone number"]!.value).toEqual(["222"]);
    expect(second.bank.answers["phone number"]!.uses).toBe(2);
    expect(first.answers["phone number"]!.value).toEqual(["111"]); // the input bank is not mutated
  });

  test("the bank is capped", () => {
    let b: Bank = emptyBank();
    const many = Array.from({ length: MAX_ANSWERS + 5 }, (_, i) => field({ label: `question ${i} text`, value: ["v"] }));
    b = mergeCapture(b, many, "a.com", now).bank;
    expect(Object.keys(b.answers).length).toBe(MAX_ANSWERS);
  });
});

describe("match", () => {
  const bank = (() => {
    const b = emptyBank();
    return mergeCapture(b, [
      field({ label: "First Name", name: "first_name", autocomplete: "given-name", value: ["Sam"] }),
      field({ label: "LinkedIn Profile", value: ["https://linkedin.example/sam"] }),
      field({ label: "Will you now or in the future require sponsorship?", kind: "radio", options: ["Yes", "No"], value: ["No"] }),
      field({ label: "Are you legally authorized to work in the United States?", kind: "select", options: ["Yes", "No"], value: ["Yes"] }),
      field({ label: "Why do you want to work here?", kind: "textarea", value: ["y".repeat(400)] }),
      field({ label: "I agree to the privacy notice", kind: "checkbox", options: [], value: ["yes"] }),
    ], "boards.greenhouse.io", now).bank;
  })();

  test("same site, same field name: signature match", () => {
    const m = matchField(field({ label: "Legal given name", name: "first_name" }), bank, "job-boards.greenhouse.io");
    expect(m?.via).toBe("signature");
    expect(m?.value).toEqual(["Sam"]);
    expect(m?.review).toBe(false);
  });

  test("another site: autocomplete meaning", () => {
    const m = matchField(field({ label: "Prénom", autocomplete: "given-name" }), bank, "jobs.lever.co");
    expect(m?.via).toBe("autocomplete");
  });

  test("reworded question: fuzzy match, filled for review", () => {
    const m = matchField(field({ label: "Do you require visa sponsorship now or in the future?", kind: "select", options: ["Select...", "Yes", "No"] }), bank, "jobs.ashbyhq.com");
    expect(m?.via).toBe("fuzzy");
    expect(m?.value).toEqual(["No"]);
    expect(m?.review).toBe(true);
  });

  test("choices map across option wordings", () => {
    expect(mapChoice("No", ["Yes, I will", "No, I will not"])).toBe("No, I will not");
    expect(mapChoice("Yes", ["I am authorized", "I am not authorized"])).toBe(null); // no yes/no meaning: left alone
    expect(mapChoice("United States", ["Canada", "United States of America"])).toBe("United States of America");
  });

  test("long free text is always a draft", () => {
    const m = matchField(field({ label: "Why do you want to work here?", kind: "textarea" }), bank, "jobs.lever.co");
    expect(m?.via).toBe("label");
    expect(m?.review).toBe(true);
  });

  test("single checkbox follows the yes/no meaning", () => {
    const m = matchField(field({ label: "I agree to the privacy notice", kind: "checkbox" }), bank, "x.com");
    expect(m?.value).toEqual(["yes"]);
  });

  test("never matches secrets, files, or unrelated questions", () => {
    expect(matchField(field({ label: "Password", kind: "password" }), bank, "x.com")).toBeNull();
    expect(matchField(field({ label: "Social Security Number" }), bank, "x.com")).toBeNull();
    expect(matchField(field({ label: "Resume", kind: "file" }), bank, "x.com")).toBeNull();
    expect(matchField(field({ label: "Favourite colour" }), bank, "x.com")).toBeNull();
  });

  test("self-id is not filled unless enabled", () => {
    const b = emptyBank();
    b.settings.captureSelfId = true;
    const withSelf = mergeCapture(b, [field({ label: "Gender", kind: "select", options: ["Male", "Female", "Decline"], value: ["Decline"] })], "a.com", now).bank;
    const g = field({ label: "Gender", kind: "select", options: ["Male", "Female", "Decline"] });
    expect(matchField(g, withSelf, "b.com")?.value).toEqual(["Decline"]);
    withSelf.settings.captureSelfId = false;
    expect(matchField(g, withSelf, "b.com")).toBeNull();
  });
});

describe("import/export", () => {
  test("round trip", () => {
    const b = mergeCapture(emptyBank(), [field({ label: "Email", value: ["e@example.invalid"] })], "a.com", now).bank;
    const { bank, droppedSecret } = parseBank(exportBank(b));
    expect(droppedSecret).toBe(0);
    expect(bank.answers["email"]!.value).toEqual(["e@example.invalid"]);
  });

  test("malformed files are refused whole", () => {
    expect(() => parseBank("{")).toThrow();
    expect(() => parseBank(JSON.stringify({ version: 2, answers: {} }))).toThrow(/version/);
    expect(() => parseBank(JSON.stringify({ version: 1, answers: [] }))).toThrow();
    expect(() => parseBank(JSON.stringify({ version: 1, answers: { ab: { key: "different" } } }))).toThrow(/malformed/);
    expect(() => parseBank(JSON.stringify({ version: 1, answers: { ab: { key: "ab", kind: "weird", value: [], labels: [], signatures: [] } } }))).toThrow(/kind/);
    expect(() => parseBank("x".repeat(8_000_001))).toThrow(/8 MB/);
  });

  test("a hand-edited secret is dropped on import", () => {
    const evil = { version: 1, answers: { "social security number": { key: "social security number", kind: "text", value: ["000"], labels: [], signatures: [] } } };
    const { bank, droppedSecret } = parseBank(JSON.stringify(evil));
    expect(droppedSecret).toBe(1);
    expect(Object.keys(bank.answers)).toEqual([]);
  });
});
