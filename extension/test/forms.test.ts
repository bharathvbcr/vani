import { describe, expect, test } from "bun:test";
import { type FieldInfo, emptyBank } from "../src/types";
import { mergeCapture } from "../src/bank";
import { matchField } from "../src/match";
import { fieldDigest, pendingDiff } from "../src/capture";
import { MAX_LISTED, bucketOf, coverageLine, coverageOf, questionRows, tabCoverage } from "../src/coverage";
import { DRAFT_EMPTY_NOTE } from "../src/suggest";
import {
  assignSections, codeDigits, composeDate, composePhone, datePartRole, entryReadings, isPhoneCode, legacyEntryKey, monthIndex, parseDate, parsePhone, sectionLabel, sectionOf,
} from "../src/sections";
import { normalizeLabel } from "../src/normalize";

const now = new Date("2026-10-18T12:00:00Z");

function field(p: Partial<FieldInfo>): FieldInfo {
  return { kind: "text", label: "", name: "", autocomplete: "", automationId: "", options: [], required: false, value: [], ...p };
}

describe("repeated sections", () => {
  test("the first entry keeps its label; later ones are numbered and lose autocomplete", () => {
    const fs = [
      field({ label: "Job Title", name: "title", value: ["Engineer"] }),
      field({ label: "Company", autocomplete: "organization", value: ["Acme"] }),
      field({ label: "Job Title", name: "title", value: ["Lead"] }),
      field({ label: "Company", autocomplete: "organization", value: ["Globex"] }),
      field({ label: "Job Title", automationId: "jobTitle", value: ["Intern"] }),
    ];
    assignSections(fs);
    expect(fs.map(f => f.label)).toEqual(["Job Title", "Company", "Job Title (vani section 2)", "Company (vani section 2)", "Job Title (vani section 3)"]);
    expect(fs[1]!.autocomplete).toBe("organization");
    expect(fs[3]!.autocomplete).toBe("");
    expect(fs[2]!.name).toBe("title@2");
    expect(fs[4]!.automationId).toBe("jobTitle@3");
  });

  test("a different kind with the same wording is not a repeat", () => {
    const fs = [field({ label: "Country", kind: "select" }), field({ label: "Country", kind: "text" })];
    assignSections(fs);
    expect(fs.map(f => f.label)).toEqual(["Country", "Country"]);
  });

  test("sectionOf reads only the namespaced marker from a normalized key", () => {
    expect(sectionOf("job title vani section 2")).toBe(2);
    expect(sectionOf("job title")).toBe(1);
    expect(sectionOf("section 2 summary")).toBe(1);
    // A genuine label that ends in "section 2" is not entry 2 of anything.
    expect(sectionOf("project section 2")).toBe(1);
    expect(sectionLabel("School", 1)).toBe("School");
    expect(sectionLabel("School", 3)).toBe("School (vani section 3)");
  });

  test("a stored key may also be read the old way, a page label never", () => {
    expect(entryReadings("job title section 2", false)).toEqual([{ base: "job title section 2", n: 1 }]);
    expect(entryReadings("job title section 2", true)).toEqual([{ base: "job title section 2", n: 1 }, { base: "job title", n: 2 }]);
    expect(entryReadings("job title vani section 2", true)).toEqual([{ base: "job title", n: 2 }]);
    expect(legacyEntryKey("job title vani section 2")).toBe("job title section 2");
    expect(legacyEntryKey("job title")).toBe("");
  });

  test("a genuine label ending in 'section 2' does not collide with entry 2 of the same question", () => {
    const fs = [
      field({ label: "Project", value: ["Alpha"] }),
      field({ label: "Project", value: ["Beta"] }),
      field({ label: "Project Section 2", value: ["Gamma"] }),
    ];
    assignSections(fs);
    expect(fs.map(f => f.label)).toEqual(["Project", "Project (vani section 2)", "Project Section 2"]);
    const bank = mergeCapture(emptyBank(), fs, "x.com", now).bank;
    expect(Object.keys(bank.answers).sort()).toEqual(["project", "project section 2", "project vani section 2"]);
    expect(bank.answers["project section 2"]!.value).toEqual(["Gamma"]);
    expect(bank.answers["project vani section 2"]!.value).toEqual(["Beta"]);
    expect(matchField(field({ label: "Project Section 2" }), bank, "y.com")?.value).toEqual(["Gamma"]);
    expect(matchField(field({ label: "Project (vani section 2)" }), bank, "y.com")?.value).toEqual(["Beta"]);
  });

  test("a page cannot forge the entry marker, and a long question keeps it", () => {
    const fs = [field({ label: "Employer (vani section 2)" })];
    assignSections(fs);
    expect(fs.map(f => f.label)).toEqual(["Employer"]); // a lone forged label is entry 1, not entry 2 of another answer
    const long = "Describe in as much detail as you can the most demanding project you have owned end to end including the team the constraints and what you would change ".repeat(2);
    const two = [field({ label: long }), field({ label: long })];
    assignSections(two);
    expect(sectionOf(normalizeLabel(two[1]!.label))).toBe(2);
    expect(normalizeLabel(two[1]!.label)).not.toBe(normalizeLabel(two[0]!.label));
  });

  test("a bank saved with the old 'section n' keys still fills entry n", () => {
    const old = emptyBank();
    for (const [key, v] of [["job title", "Engineer"], ["job title section 2", "Lead"]] as const) {
      old.answers[key] = { key, labels: [key], kind: "text", value: [v], sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: "2026-10-01T00:00:00.000Z", review: false };
    }
    const m = matchField(field({ label: "Job Title (vani section 2)" }), old, "y.com");
    expect(m?.value).toEqual(["Lead"]);
    expect(m?.review).toBe(false); // an exact old key is as confident as before
    expect(matchField(field({ label: "Job Title" }), old, "y.com")?.value).toEqual(["Engineer"]);
    // A reworded entry 2 still finds the old entry 2 and not entry 1.
    expect(matchField(field({ label: "Previous Job Title (vani section 2)" }), old, "y.com")?.value).toEqual(["Lead"]);
    // Re-saving entry 2 writes the new key, and the newer answer wins over its old twin.
    const resaved = mergeCapture(old, [field({ label: "Job Title (vani section 2)", value: ["Director"] })], "x.com", new Date("2026-10-18T12:00:00Z")).bank;
    expect(resaved.answers["job title section 2"]!.value).toEqual(["Lead"]); // nothing migrated or deleted
    expect(matchField(field({ label: "Job Title (vani section 2)" }), resaved, "y.com")?.value).toEqual(["Director"]);
  });

  test("entry n is saved under its own key and entry 2 never fuzzy-fills from entry 1", () => {
    const entries = [
      field({ label: "Job Title", value: ["Engineer"] }),
      field({ label: "Job Title (vani section 2)", value: ["Lead"] }),
    ];
    const both = mergeCapture(emptyBank(), entries, "x.com", now).bank;
    expect(Object.keys(both.answers).sort()).toEqual(["job title", "job title vani section 2"]);
    expect(matchField(field({ label: "Job Title" }), both, "y.com")?.value).toEqual(["Engineer"]);
    expect(matchField(field({ label: "Job Title (vani section 2)" }), both, "y.com")?.value).toEqual(["Lead"]);

    const only1 = mergeCapture(emptyBank(), [entries[0]!], "x.com", now).bank;
    // Token overlap of the marked label with "job title" is above the fuzzy floor; the guard must still refuse.
    expect(matchField(field({ label: "Job Title (vani section 2)" }), only1, "y.com")).toBeNull();
    // And entry 1 does not borrow entry 2's answer either.
    const only2 = mergeCapture(emptyBank(), [entries[1]!], "x.com", now).bank;
    expect(matchField(field({ label: "Job Title" }), only2, "y.com")).toBeNull();
    expect(matchField(field({ label: "Job Title (vani section 3)" }), only2, "y.com")).toBeNull();
  });

  test("entry 2 has its own per-site signature", () => {
    const b = mergeCapture(emptyBank(), [
      field({ label: "Employer", name: "employer", value: ["Acme"] }),
      field({ label: "Employer (vani section 2)", name: "employer@2", value: ["Globex"] }),
    ], "acme.myworkdayjobs.com", now).bank;
    const m = matchField(field({ label: "Previous employer", name: "employer@2" }), b, "other.myworkdayjobs.com");
    expect(m?.via).toBe("signature");
    expect(m?.value).toEqual(["Globex"]);
  });
});

describe("a widget whose options are not on the page yet", () => {
  test("the saved choice passes through to be mapped when the list opens", () => {
    const b = mergeCapture(emptyBank(), [field({ label: "Country", kind: "select", options: ["US", "CA"], value: ["United States"] })], "x.com", now).bank;
    const m = matchField(field({ label: "Country", kind: "select", options: [] }), b, "y.com");
    expect(m?.value).toEqual(["United States"]);
  });
  test("known options still decide: a value with no acceptable option is left alone", () => {
    const b = mergeCapture(emptyBank(), [field({ label: "Country", kind: "select", options: ["US"], value: ["Narnia"] })], "x.com", now).bank;
    expect(matchField(field({ label: "Country", kind: "select", options: ["Canada", "Mexico"] }), b, "y.com")).toBeNull();
  });
});

describe("multi-select answers", () => {
  const skills = (extra: Partial<FieldInfo>) => field({ label: "Skills", kind: "checkbox", multi: true, ...extra });
  const saved = mergeCapture(emptyBank(), [skills({ options: ["Go", "Rust", "Python"], value: ["Go", "Rust"] })], "x.com", now).bank;

  test("every saved value is offered, mapped onto the options of the new page", () => {
    expect(matchField(skills({ options: ["Rust", "Java", "Go"] }), saved, "y.com")?.value).toEqual(["Go", "Rust"]);
    // A value with no option is dropped; the rest still fill.
    const part = matchField(skills({ options: ["Go", "Java"] }), saved, "y.com");
    expect(part?.value).toEqual(["Go"]);
    expect(part?.review).toBe(true); // a partial fill is flagged
    expect(matchField(skills({ options: ["Rust", "Java", "Go"] }), saved, "y.com")?.review).toBe(false);
  });

  test("a multi-select with one option is not mistaken for a single checkbox", () => {
    expect(matchField(skills({ options: ["Go"] }), saved, "y.com")?.value).toEqual(["Go"]);
    // The same field as a plain checkbox still reads its answer as yes/no.
    expect(matchField(field({ label: "Skills", kind: "checkbox", options: ["Go"] }), saved, "y.com")).toBeNull();
  });

  test("a widget whose options are not on the page yet passes every value through", () => {
    expect(matchField(field({ label: "Skills", kind: "select", multi: true, options: [] }), saved, "y.com")?.value).toEqual(["Go", "Rust"]);
    expect(matchField(field({ label: "Skills", kind: "select", options: [] }), saved, "y.com")?.value).toEqual(["Go"]);
  });
});

describe("a value is sent once per page", () => {
  test("the digest of a field changes with its value, wording and site, and is empty for a secret", () => {
    const f = field({ label: "First Name", name: "first_name", value: ["Sam"] });
    expect(fieldDigest(f, "boards.greenhouse.io", false)).toBe(fieldDigest(field({ ...f }), "boards.greenhouse.io", false));
    expect(fieldDigest(f, "boards.greenhouse.io", false)).not.toBe(fieldDigest({ ...f, value: ["Samuel"] }, "boards.greenhouse.io", false));
    expect(fieldDigest(f, "boards.greenhouse.io", false)).not.toBe(fieldDigest(f, "jobs.lever.co", false));
    expect(fieldDigest(field({ label: "Password", kind: "password", value: ["hunter2"] }), "x.com", false)).toBe("");
    expect(fieldDigest(field({ label: "Gender", kind: "select", value: ["Female"] }), "x.com", false)).toBe(""); // unopted self-ID carries nothing
    expect(fieldDigest(field({ label: "Gender", kind: "select", value: ["Female"] }), "x.com", true)).not.toBe("");
  });
});

describe("coverage across frames", () => {
  const cov = (p: Partial<ReturnType<typeof coverageOf>>) => ({ ...coverageOf([], null, "x"), ...p });
  const top = { frameId: 0, result: { host: "careers.example", disabled: false, fields: 2, coverage: cov({ found: 2, fillable: 1, unknown: 1, known: 1, unknownLabels: ["Pet"] }) } };
  const embed = { frameId: 7, result: { host: "boards.greenhouse.io", disabled: false, fields: 12, coverage: cov({ found: 12, fillable: 9, unknown: 2, manual: 1, known: 10, unknownLabels: ["A", "B"], manualLabels: ["Skills"], capped: true, unread: 40 }) } };

  test("the totals add up and each frame is listed, top frame first", () => {
    const t = tabCoverage([embed, { frameId: 3, result: null }, top]);
    expect(t.total).toMatchObject({ found: 14, fillable: 10, unknown: 3, manual: 1, known: 11, capped: true, unread: 40 });
    expect(t.total.unknownLabels).toEqual(["Pet", "A", "B"]);
    expect(t.frames.map(f => [f.frameId, f.host, f.coverage.found])).toEqual([[0, "careers.example", 2], [7, "boards.greenhouse.io", 12]]);
    expect([t.asked, t.answered]).toEqual([3, 2]);
  });

  test("a form that lives only in a frame is not reported as empty", () => {
    const t = tabCoverage([{ frameId: 0, result: { host: "careers.example", disabled: false, fields: 0 } }, embed]);
    expect(t.total.found).toBe(12);
    expect(t.frames).toHaveLength(1);
    expect(t.answered).toBe(2);
  });

  test("nobody answering is not the same as no fields", () => {
    const t = tabCoverage([{ frameId: 0, result: null }, { frameId: 4 }]);
    expect([t.asked, t.answered, t.frames.length, t.disabled]).toEqual([2, 0, 0, false]);
  });

  test("the combined label lists stay bounded", () => {
    const many = (n: number) => cov({ found: n, unknown: n, unknownLabels: Array.from({ length: Math.min(n, MAX_LISTED) }, (_, i) => `q${i}`) });
    const t = tabCoverage([{ frameId: 0, result: { host: "a", disabled: false, fields: 80, coverage: many(80) } }, { frameId: 1, result: { host: "b", disabled: false, fields: 80, coverage: many(80) } }]);
    expect(t.total.unknown).toBe(160);
    expect(t.total.unknownLabels).toHaveLength(MAX_LISTED);
  });

  test("disabled only when every frame with fields is switched off", () => {
    const off = (frameId: number, disabled: boolean) => ({ frameId, result: { host: "h", disabled, fields: 3, coverage: cov({ found: 3 }) } });
    expect(tabCoverage([off(0, true), off(1, true)]).disabled).toBe(true);
    expect(tabCoverage([off(0, true), off(1, false)]).disabled).toBe(false);
  });
});

describe("split dates", () => {
  test("parts are recognised by whole words and stable ids only", () => {
    expect(datePartRole({ label: "Month", ids: [] })).toBe("month");
    expect(datePartRole({ label: "YYYY", ids: [] })).toBe("year");
    expect(datePartRole({ label: "", ids: ["", "", "dateSectionDay-input"] })).toBe("day");
    expect(datePartRole({ label: "Years of experience", ids: ["yearsOfExperience"] })).toBe("");
    expect(datePartRole({ label: "Birthday", ids: [] })).toBe("");
  });
  test("compose and parse round-trip", () => {
    expect(composeDate({ y: "2024", m: "May", d: "1" })).toBe("2024-05-01");
    expect(composeDate({ y: "2024", m: "5" })).toBe("2024-05");
    expect(composeDate({ y: "24", m: "5" })).toBe("");
    expect(composeDate({ y: "2024", m: "" })).toBe("");
    expect(parseDate("2024-05-01")).toEqual({ y: "2024", m: "05", d: "01" });
    expect(parseDate("05/2024")).toEqual({ y: "2024", m: "05", d: "" });
    expect(parseDate("13/45/2024")).toBeNull();
    expect(parseDate("soon")).toBeNull();
    expect(monthIndex("Sept")).toBe(0); // only 3-letter or full names
    expect(monthIndex("Sep")).toBe(9);
  });
});

describe("split phone", () => {
  test("compose from code and number, number alone when the code is unreadable", () => {
    expect(composePhone("United States (+1)", "555 0100")).toBe("+1 555 0100");
    expect(composePhone("United States", "555 0100")).toBe("555 0100");
    expect(codeDigits("+44")).toBe("44");
    expect(codeDigits("1")).toBe("1");
    expect(codeDigits("Section 12")).toBe("");
  });
  test("parse splits only when unambiguous", () => {
    expect(parsePhone("+1 555 0100")).toEqual({ cc: "1", national: "555 0100" });
    expect(parsePhone("555-0100")).toEqual({ cc: "", national: "555-0100" });
    expect(parsePhone("+15550100")).toBeNull(); // cannot know where the code ends
    expect(parsePhone("+15550100", ["1", "44"])).toEqual({ cc: "1", national: "5550100" });
    expect(parsePhone("+15550100", ["1", "15"])).toBeNull(); // two prefixes fit
    expect(parsePhone("")).toBeNull();
  });
  test("code controls are told from number controls", () => {
    expect(isPhoneCode({ label: "Country Phone Code", autocomplete: "", ids: [] })).toBe(true);
    expect(isPhoneCode({ label: "Phone", autocomplete: "tel-country-code", ids: [] })).toBe(true);
    expect(isPhoneCode({ label: "Phone Number", autocomplete: "tel", ids: ["phone-number"] })).toBe(false);
    expect(isPhoneCode({ label: "Country", autocomplete: "country", ids: [] })).toBe(false);
  });
});

describe("pending capture is idempotent and never carries secrets", () => {
  const fields = [
    field({ label: "First Name", name: "first_name", autocomplete: "given-name", value: ["Sam"] }),
    field({ label: "Email", kind: "email", name: "email", value: ["sam@example.invalid"] }),
    field({ label: "Password", kind: "password", value: ["hunter2"] }),
    field({ label: "Social Security Number", value: ["000-00-0000"] }),
    field({ label: "Gender", kind: "select", options: ["Male", "Female", "Other"], value: ["Prefer not to say"] }),
    field({ label: "Resume", kind: "file", value: ["cv.pdf"] }),
    field({ label: "Website", value: [] }),
  ];

  test("the first flush sends the answers; the same page again sends nothing", () => {
    const first = pendingDiff(emptyBank(), fields, "boards.greenhouse.io");
    expect(first.send.map(f => f.label)).toEqual(["First Name", "Email", "Password", "Social Security Number", "Gender"]);
    const saved = mergeCapture(emptyBank(), first.send, "boards.greenhouse.io", now).bank;
    const again = pendingDiff(saved, fields, "boards.greenhouse.io");
    expect(again.send).toEqual([]);
    expect(again.unchanged).toBe(2);
    // Merging what the second diff would send changes no `uses` count.
    expect(saved.answers["first name"]!.uses).toBe(1);
  });

  test("a changed value, or the same value learned on a new site, is sent again", () => {
    const saved = mergeCapture(emptyBank(), [fields[0]!], "boards.greenhouse.io", now).bank;
    expect(pendingDiff(saved, [field({ ...fields[0]!, value: ["Samuel"] })], "boards.greenhouse.io").send).toHaveLength(1);
    expect(pendingDiff(saved, [fields[0]!], "jobs.lever.co").send).toHaveLength(1); // new signature to learn
    expect(pendingDiff(saved, [field({ ...fields[0]!, label: "FIRST NAME *" })], "boards.greenhouse.io").unchanged).toBe(1); // same wording once normalized
    expect(pendingDiff(saved, [field({ ...fields[0]!, label: "Given name" })], "boards.greenhouse.io").send).toHaveLength(1); // a new wording is worth learning
  });

  test("password, SSN and unopted self-ID leave the page without their value", () => {
    const json = JSON.stringify(pendingDiff(emptyBank(), fields, "x.com").send);
    for (const secret of ["hunter2", "000-00-0000", "Prefer not to say", "cv.pdf"]) expect(json).not.toContain(secret);
  });

  test("self-ID travels once the user has turned it on", () => {
    const b = emptyBank();
    b.settings.captureSelfId = true;
    expect(JSON.stringify(pendingDiff(b, fields, "x.com").send)).toContain("Prefer not to say");
  });

  test("sensitive-only pages send nothing unless the user asked to save", () => {
    const only = [fields[2]!, fields[3]!];
    expect(pendingDiff(emptyBank(), only, "x.com").send).toEqual([]);
    expect(pendingDiff(emptyBank(), only, "x.com", true).send).toHaveLength(2);
  });

  test("empty page and empty values", () => {
    expect(pendingDiff(emptyBank(), [], "x.com")).toEqual({ send: [], unchanged: 0, sensitive: 0 });
    expect(pendingDiff(emptyBank(), [field({ label: "Name", value: ["  ", ""] })], "x.com").send).toEqual([]);
  });
});

describe("coverage", () => {
  const bank = (() => {
    const b = emptyBank();
    b.settings.captureSelfId = true;
    const saved = mergeCapture(b, [
      field({ label: "First Name", autocomplete: "given-name", value: ["Sam"] }),
      field({ label: "Gender", kind: "select", options: ["A", "B"], value: ["A"] }),
    ], "x.com", now).bank;
    saved.settings.captureSelfId = false; // the user turned it back off
    return saved;
  })();

  const refs = [
    { info: field({ label: "First Name", autocomplete: "given-name" }), manual: "" },
    { info: field({ label: "Favourite colour" }), manual: "" },
    { info: field({ label: "Start date", kind: "date" }), manual: "date or dialog picker" },
    { info: field({ label: "Resume", kind: "file" }), manual: "" },
    { info: field({ label: "Password", kind: "password" }), manual: "" },
    { info: field({ label: "Social Security Number", value: ["000-00-0000"] }), manual: "" },
    { info: field({ label: "Gender", kind: "select", options: ["A", "B"] }), manual: "" },
  ];

  test("every field lands in exactly one bucket, and the buckets sum to found", () => {
    const c = coverageOf(refs, bank, "y.com");
    expect(c.found).toBe(7);
    expect(c.fillable + c.unknown + c.manual + c.skipped).toBe(c.found);
    expect(c).toMatchObject({ fillable: 1, unknown: 1, manual: 1, skipped: 4, known: 1 });
    expect(c.unknownLabels).toEqual(["Favourite colour"]);
    expect(c.manualLabels).toEqual(["Start date"]);
    expect(c.manualQuestions.map(q => [q.label, q.offer, q.ordinal])).toEqual([["Start date", "manual", 2]]);
    expect(c.held.map(q => [q.label, q.ordinal])).toEqual([["Password", 4], ["Social Security Number", 5], ["Gender", 6]]);
    expect(c.held.every(q => q.note === "Vani does not draft this." && q.offer === "other")).toBe(true);
    expect(JSON.stringify(c.held)).not.toContain("000-00-0000");
    expect(c.held.some(q => q.label === "Resume")).toBe(false);
    expect(c.capped).toBe(false);
  });

  test("self-ID is skipped when off and counted when on", () => {
    const gender = { info: field({ label: "Gender", kind: "select", options: ["A", "B"] }), manual: "" };
    expect(bucketOf(gender.info, "", bank, "y.com").bucket).toBe("skipped");
    const on = { ...bank, settings: { ...bank.settings, captureSelfId: true } };
    expect(bucketOf(gender.info, "", on, "y.com")).toEqual({ bucket: "fillable", known: true });
  });

  test("no bank: nothing is known, nothing is guessed", () => {
    const c = coverageOf(refs, null, "y.com");
    expect(c.known).toBe(0);
    expect(c.unknown).toBe(2);
  });

  test("a capped scan says so and the list is cut with the count kept", () => {
    const many = Array.from({ length: 80 }, (_, i) => ({ info: field({ label: `Question number ${i} here` }), manual: "" }));
    const c = coverageOf(many, bank, "y.com", { capped: true, unread: 700 });
    expect(c.unknown).toBe(80);
    expect(c.unknownLabels).toHaveLength(MAX_LISTED);
    expect(coverageLine(c)).toContain("700 more controls were not read");
    expect(c.questions).toHaveLength(MAX_LISTED);
    expect(coverageLine(coverageOf([], bank, "y.com"))).toBe("0 fields, 0 ready, 0 new.");
  });

  test("each new question says whether it can be drafted or regenerated, and the field text stays out of the row", () => {
    const posting = "We build inference systems for robots in production. ".repeat(3);
    const written = "I build the inference stack.";
    const refs = [
      { info: field({ label: "Why this team?", value: [written], drafted: true, draftText: written }), manual: "" },
      { info: field({ label: "Years of Go" }), manual: "" },
      { info: field({ label: "Office", kind: "select", options: ["Berlin"] }), manual: "" },
      { info: field({ label: "Phone", kind: "tel" }), manual: "" },
    ];
    const c = coverageOf(refs, null, "y.com", undefined, undefined, posting.length);
    expect(c.questions.map(q => [q.label, q.offer])).toEqual([
      ["Why this team?", "regenerate"],
      ["Years of Go", "draft"],
      ["Office", "choice"],
      ["Phone", "other"],
    ]);
    expect(c.unknownLabels).toEqual(c.questions.map(q => q.label));
    expect(c.questions.map(q => q.ordinal)).toEqual([0, 1, 2, 3]);
    expect(JSON.stringify(c.questions)).not.toContain(written);
    const edited = "I changed the draft.";
    const changed = coverageOf(
      [{ info: field({ label: "Why this team?", value: [edited], drafted: true, draftText: written }), manual: "" }],
      null, "y.com", undefined, undefined, posting.length,
    );
    expect(changed.questions[0]?.offer).toBe("replace");
    expect(JSON.stringify(changed.questions)).not.toContain(edited);
    expect(JSON.stringify(changed.questions)).not.toContain(written);
    expect(coverageOf([refs[1]!], null, "y.com", undefined, undefined, 39).questions[0]?.offer).toBe("needs-posting");
  });

  test("questions from every frame are listed in frame order and cut at the same cap as labels", () => {
    const block = (host: string, n: number) => coverageOf(
      Array.from({ length: n }, (_, i) => ({ info: field({ label: `${host} question ${i} here` }), manual: "" })),
      null, host, undefined, undefined, 80,
    );
    const t = tabCoverage([
      { frameId: 1, result: { host: "b", disabled: false, fields: 40, coverage: block("b", 40) } },
      { frameId: 0, result: { host: "a", disabled: false, fields: 40, coverage: block("a", 40) } },
    ]);
    expect(t.total.questions).toHaveLength(MAX_LISTED);
    expect(t.total.questions[0]?.frameId).toBe(0);
    expect(t.total.questions[40]?.frameId).toBe(1);
    expect(t.total.questions[0]?.label.startsWith("a ")).toBe(true);
    expect(t.total.questions[39]?.label.startsWith("a ")).toBe(true);
    expect(t.total.questions[40]?.label.startsWith("b ")).toBe(true);
    expect(t.total.unknownLabels).toEqual(t.total.questions.map(q => q.label));
  });

  test("a coverage that lists labels but no questions still offers Draft", () => {
    expect(questionRows({ unknownLabels: ["Pet"], questions: [] })).toEqual([{ label: "Pet", offer: "draft", note: DRAFT_EMPTY_NOTE }]);
    expect(questionRows({ questions: [{ label: "Why", offer: "regenerate", note: "n" }], unknownLabels: ["Why"] })[0]?.offer).toBe("regenerate");
    expect(questionRows({ unknownLabels: null, questions: null })).toEqual([]);
  });
});
