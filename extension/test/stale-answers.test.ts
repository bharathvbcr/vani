// Fill writes the answer the user most recently gave for THAT question. Each test is one way it used to write
// something else: an older or more-used answer, another question's answer, a time-sensitive answer gone stale, or
// Vani's own fill read back as if the user had answered it.
import { describe, expect, test } from "bun:test";
import { type Answer, type Bank, type FieldInfo, emptyBank } from "../src/types";
import { BASICS_HOST, mergeCapture, mergeCaptureDelta } from "../src/bank";
import { mapChoice, matchField, whyNoMatch } from "../src/match";
import { contactOf, signature } from "../src/normalize";
import { intentOf } from "../src/intents";

function field(p: Partial<FieldInfo>): FieldInfo {
  return { kind: "text", label: "", name: "", autocomplete: "", automationId: "", options: [], required: false, value: [], ...p };
}
const at = (iso: string) => new Date(iso);
/** Save one field the way a flush does. */
function save(bank: Bank, f: FieldInfo, host: string, when: string): Bank {
  return mergeCapture(bank, [f], host, at(when)).bank;
}
/** "Now" for every match here: the volatile-answer rules read the clock. */
const NOW = Date.parse("2026-10-28T12:00:00Z");
/** The value Fill would write into `f` (null: nothing filled). */
function fillValue(bank: Bank, f: FieldInfo, host: string): string | null {
  const m = matchField({ ...f, value: [] }, bank, host, NOW);
  return m ? m.value.join(", ") : null;
}
const daysAgo = (n: number) => new Date(NOW - n * 86_400_000).toISOString();

const GH = "job-boards.greenhouse.io";

describe("recency beats frequency", () => {
  test("a newer correction saved under a reworded question beats the older, more-used answer (same stable field name)", () => {
    let b = emptyBank();
    // Old answer, confirmed on three forms (uses = 3).
    for (const d of ["2026-09-01", "2026-09-02", "2026-09-03"]) {
      b = save(b, field({ label: "Preferred name", name: "preferred_name", value: ["Bob"] }), GH, `${d}T10:00:00Z`);
    }
    // The user corrects it on a form that words the question differently but uses the same field name.
    b = save(b, field({ label: "What should we call you?", name: "preferred_name", value: ["Robert"] }), GH, "2026-10-20T10:00:00Z");
    expect(Object.keys(b.answers).length).toBe(2);
    expect(fillValue(b, field({ label: "What should we call you?", name: "preferred_name" }), GH)).toBe("Robert");
    // Back on the original wording, the id (now Robert's) and the exact wording (Bob's) disagree. That is the same
    // shape as a site giving its id to a different question, so neither is filled confidently: the exact wording
    // fills, yellow, saying why.
    const m = matchField(field({ label: "Preferred name", name: "preferred_name" }), b, GH, NOW);
    expect(m?.value).toEqual(["Bob"]);
    expect(m?.review).toBe(true);
    expect(m?.note).toContain("differently worded question");
    // The id's answer rides along, so a caller that can read both wordings can decide they are one question.
    expect(m?.alternative?.answer.key).toBe("what should we call you");
    expect(m?.alternative?.value).toEqual(["Robert"]);
    expect(m?.alternative?.wording).toBe("what should we call you");
    // The reworded wording, which the id and the exact wording agree on, has nothing to choose between.
    expect(matchField(field({ label: "What should we call you?", name: "preferred_name" }), b, GH, NOW)?.alternative).toBeUndefined();
  });
  test("at equal score the newest answer wins even when an older one was used more", () => {
    let b = emptyBank();
    b = save(b, field({ label: "Programming language", value: ["Go"] }), GH, "2026-09-01T10:00:00Z");
    b = save(b, field({ label: "Programming language", value: ["Go"] }), GH, "2026-09-02T10:00:00Z");
    b = { ...b, answers: { ...b.answers, "primary programming language": { ...b.answers["programming language"]!, key: "primary programming language", labels: ["primary programming language"], value: ["Rust"], uses: 1, updatedAt: "2026-10-20T10:00:00Z" } } };
    // Both answers hold the same field name; neither wording matches the field's label exactly.
    b = { ...b, answers: Object.fromEntries(Object.entries(b.answers).map(([k, a]) => [k, { ...a, signatures: ["greenhouse.io|lang_pref"] }])) };
    expect(fillValue(b, field({ label: "Which language do you use most?", name: "lang_pref" }), GH)).toBe("Rust");
  });
});

describe("signature() refuses ids that are positions or generic words", () => {
  const sig = (id: string) => signature(GH, { name: id, automationId: "" });
  test("framework-generated, positional and generic ids give no signature", () => {
    const ids = [
      ":r3:", ":R1a:", ":r0:", "react-select-3-input", "react-select-12-live-region", "input-4", "input_7", "mui-12", "12",
      "headlessui-listbox-button-7", "headlessui-combobox-input-:r5:", "downshift-0-input", "downshift-1-label", "radix-:r2:", "radix-3",
      "textInput", "input", "answer", "field", "field0", "text", "value", "select", "textarea", "question", "Input", "TEXT",
    ];
    expect(ids.filter(id => sig(id) !== "")).toEqual([]);
  });
  test("genuine stable ATS names still give one", () => {
    for (const id of ["first_name", "urls[LinkedIn]", "email", "job_application[first_name]", "phone", "q_editor", "preferred_name", "cover_letter_text", "input_email", "full-name"]) {
      expect(sig(id)).toBe(`greenhouse.io|${id}`);
    }
  });
  test("an automation id is judged the same way", () => {
    expect(signature(GH, { name: "", automationId: "react-select-4-input" })).toBe("");
    expect(signature(GH, { name: "", automationId: "legalNameSection_firstName" })).toBe("greenhouse.io|legalNameSection_firstName");
  });
});

describe("one signature names one question", () => {
  test("a reused element id (React useId ':r3:') cannot make question B fill question A's answer", () => {
    let b = emptyBank();
    b = save(b, field({ label: "Primary programming language", name: ":r3:", value: ["Rust"] }), GH, "2026-10-01T10:00:00Z");
    b = save(b, field({ label: "Largest team you have managed", name: ":r3:", value: ["12 people"] }), GH, "2026-10-02T10:00:00Z");
    expect(fillValue(b, field({ label: "Largest team you have managed", name: ":r3:" }), GH)).toBe("12 people");
    expect(fillValue(b, field({ label: "Primary programming language", name: ":r3:" }), GH)).toBe("Rust");
  });
  test("storing a signature on one answer takes it off every other, and both rows are upserted", () => {
    let b = emptyBank();
    b = save(b, field({ label: "Favourite editor", name: "q_editor", value: ["vim"] }), GH, "2026-09-01T10:00:00Z");
    const before = b;
    const delta = mergeCaptureDelta(before, [field({ label: "Favourite colour", name: "q_editor", value: ["blue"] })], GH, at("2026-09-05T10:00:00Z"));
    expect(delta.upsert.map(a => a.key).sort()).toEqual(["favourite colour", "favourite editor"]);
    const after = Object.fromEntries(delta.upsert.map(a => [a.key, a]));
    expect(after["favourite editor"]!.signatures).toEqual([]);
    expect(after["favourite colour"]!.signatures).toEqual(["greenhouse.io|q_editor"]);
    expect(after["favourite editor"]!.value).toEqual(["vim"]);
    expect(after["favourite editor"]!.updatedAt).toBe(before.answers["favourite editor"]!.updatedAt);
  });
  test("an exact-label answer the user just edited beats a different question's older signature", () => {
    let b = emptyBank();
    b = save(b, field({ label: "Favourite editor", name: "q_editor", value: ["vim"] }), GH, "2026-09-01T10:00:00Z");
    b = save(b, field({ label: "Favourite colour", name: "q_editor", value: ["blue"] }), GH, "2026-09-05T10:00:00Z");
    // The user fixes the editor answer from the popup (a popup edit keeps signatures and bumps updatedAt).
    const edited = { ...b.answers["favourite editor"]!, value: ["emacs"], updatedAt: "2026-10-25T10:00:00Z" };
    b = { ...b, answers: { ...b.answers, "favourite editor": edited } };
    expect(fillValue(b, field({ label: "Favourite colour", name: "q_editor" }), GH)).toBe("blue");
    expect(fillValue(b, field({ label: "Favourite editor", name: "q_editor" }), GH)).toBe("emacs");
  });
  test("a stable-looking id the site reused for another question does not vouch for that question's answer", () => {
    let b = emptyBank();
    b = save(b, field({ label: "Years of experience", name: "q1", value: ["7"] }), GH, "2026-09-01T10:00:00Z");
    b = save(b, field({ label: "Favorite color", name: "q1", value: ["blue"] }), GH, "2026-10-01T10:00:00Z");
    const years = matchField(field({ label: "Years of experience", name: "q1" }), b, GH, NOW);
    expect(years?.value).toEqual(["7"]);
    expect(years?.review).toBe(true);
    const colour = matchField(field({ label: "Favorite color", name: "q1" }), b, GH, NOW);
    expect(colour?.value).toEqual(["blue"]);
    expect(colour?.review).toBe(false);
  });
  test("the id still vouches when the field's wording is the answer's, reworded only slightly, or missing", () => {
    let b = emptyBank();
    b = save(b, field({ label: "Current employer", name: "current_company", value: ["Acme"] }), GH, "2026-09-01T10:00:00Z");
    const m = matchField(field({ label: "Current employer *", name: "current_company" }), b, GH, NOW);
    expect(m?.via).toBe("signature");
    expect(fillValue(b, field({ label: "", name: "current_company" }), GH)).toBe("Acme");
  });
});

describe("a bare 'Name' or 'Site' is not the applicant's own contact basic", () => {
  test("a reference block's Name and an office Site are not filled from the full name or website", () => {
    let b = emptyBank();
    b = save(b, field({ label: "Full name", value: ["Test Applicant"] }), GH, "2026-10-01T10:00:00Z");
    b = save(b, field({ label: "Website", value: ["https://example.invalid"] }), GH, "2026-10-01T10:00:00Z");
    expect(contactOf("Name")).toBe("");
    expect(contactOf("Site")).toBe("");
    expect(fillValue(b, field({ label: "Name" }), "jobs.example.com")).toBeNull();
    expect(fillValue(b, field({ label: "Site" }), "jobs.example.com")).toBeNull();
    expect(contactOf("Your full name")).toBe("full name");
    expect(contactOf("Personal site")).toBe("website");
  });
});

describe("a value that does not fit this form is not replaced by an older answer", () => {
  test("select without the user's latest choice stays empty instead of taking the superseded one", () => {
    let b = emptyBank();
    b = save(b, field({ label: "Work arrangement preference", value: ["Remote"] }), GH, "2026-09-01T10:00:00Z");
    b = save(b, field({ kind: "select", label: "Work arrangement", options: ["Hybrid", "Remote", "On-site"], value: ["Hybrid"] }), GH, "2026-10-20T10:00:00Z");
    const m = matchField(field({ kind: "select", label: "Work arrangement", options: ["Remote", "On-site"] }), b, GH, NOW);
    expect(m).toBeNull();
  });
  test("a confident candidate that cannot map ends the search; a fuzzy one does not", () => {
    let b = emptyBank();
    b = save(b, field({ kind: "select", label: "Country", name: "country", options: ["Germany", "France"], value: ["Germany"] }), GH, "2026-10-01T10:00:00Z");
    expect(matchField(field({ kind: "select", label: "Country", name: "country", options: ["Spain", "Italy"] }), b, GH, NOW)).toBeNull();
    b = save(b, field({ label: "Years of experience", value: ["8"] }), GH, "2026-10-01T10:00:00Z");
    expect(fillValue(b, field({ label: "Years of experience with Java" }), GH)).toBe("8"); // fuzzy: still found (yellow)
  });
});

describe("contact basics compete as one concept with the same question saved under a label", () => {
  test("new email typed in the popup is what a form's 'Email address' field gets, though the old one holds a site signature", () => {
    let b = emptyBank();
    b = save(b, field({ kind: "email", label: "Email address", name: "email", value: ["old@example.com"] }), "jobs.lever.co", "2026-09-01T10:00:00Z");
    b = mergeCapture(b, [field({ kind: "email", label: "Email", autocomplete: "email", value: ["new@example.com"] })], BASICS_HOST, at("2026-10-25T10:00:00Z")).bank;
    const m = matchField(field({ kind: "email", label: "Email address", name: "email" }), b, "jobs.lever.co", NOW);
    expect(m?.value).toEqual(["new@example.com"]);
    expect(m?.review).toBe(false);
  });
  test("the same holds when the form declares autocomplete, and when the label is the only hint", () => {
    let b = emptyBank();
    b = save(b, field({ label: "Phone number", name: "phone", value: ["555-0100"] }), "jobs.lever.co", "2026-09-01T10:00:00Z");
    b = mergeCapture(b, [field({ kind: "tel", label: "Phone", autocomplete: "tel", value: ["555-0199"] })], BASICS_HOST, at("2026-10-25T10:00:00Z")).bank;
    expect(fillValue(b, field({ kind: "tel", label: "Mobile number", name: "phone" }), "jobs.lever.co")).toBe("555-0199");
    expect(fillValue(b, field({ kind: "tel", label: "Your phone", autocomplete: "tel" }), GH)).toBe("555-0199");
  });
  test("a newer form-captured answer still beats an older basic", () => {
    let b = emptyBank();
    b = mergeCapture(b, [field({ kind: "email", label: "Email", autocomplete: "email", value: ["basic@example.com"] })], BASICS_HOST, at("2026-09-01T10:00:00Z")).bank;
    b = save(b, field({ kind: "email", label: "Email address", name: "email", value: ["typed@example.com"] }), "jobs.lever.co", "2026-10-25T10:00:00Z");
    expect(fillValue(b, field({ kind: "email", label: "Email address", name: "email" }), "jobs.lever.co")).toBe("typed@example.com");
  });

  const hits: Array<[string, string]> = [
    ["First name", "first name"], ["Legal first name", "first name"], ["Given name", "first name"], ["Last name", "last name"], ["Surname", "last name"],
    ["Family name", "last name"], ["Full name", "full name"], ["Your name", "full name"], ["Email", "email"], ["E-mail address", "email"],
    ["Email Address *", "email"], ["Phone", "phone"], ["Phone number", "phone"], ["Mobile number", "phone"], ["Cell phone", "phone"], ["Telephone", "phone"],
    ["City", "city"], ["State", "state"], ["State / Province", "state"], ["Country", "country"], ["Country of residence", "country"],
    ["Zip code", "postal code"], ["Postal code", "postal code"], ["Postcode", "postal code"], ["LinkedIn", "linkedin"], ["LinkedIn Profile URL", "linkedin"],
    ["GitHub", "github"], ["Github profile", "github"], ["Website", "website"], ["Personal website", "website"],
  ];
  test.each(hits)("%p reads as the contact basic %p", (label, want) => {
    expect(contactOf(label)).toBe(want);
  });
  // Ported from the reference autofill-pro suite's expectNull / expectNot lists: look-alikes that are not contact fields.
  const misses = [
    "cell_biology Cell Biology Lab", "cell_biology", "mobile_os Mobile Operating System", "sales_region Sales Region", "business_unit Business Unit",
    "clearance_sale Clearance Items", "bonus_eligibility Bonus Eligibility", "statement_of_purpose Statement of Purpose", "estate_details Estate Value",
    "interstate_travel Interstate Travel", "financial_statement Financial Statement", "overstated Overstated Experience", "capacity_field Seating Capacity",
    "electricity_type Electricity Provider", "publicity Publicity Rights", "duplicity_check Duplicate Check", "CandidateCity", "CandidateState",
    "ethnicity", "Employment status", "United States", "Email confirmation code", "Country code", "Phone type", "State of incorporation", "Name of your manager",
    "GitHub handle", "GitHub username", "LinkedIn handle", "Reference name", "wholesale_price Wholesale Price", "blog_url", "Website design experience", "City council experience", "Zip file", "Postal service",
  ];
  test("look-alike labels do not read as a contact basic", () => {
    expect(misses.filter(l => contactOf(l) !== "")).toEqual([]);
  });
  test("and so a look-alike field never takes the contact answer", () => {
    let b = emptyBank();
    b = mergeCapture(b, [field({ label: "Phone", autocomplete: "tel", kind: "tel", value: ["555-0199"] }), field({ label: "State", autocomplete: "address-level1", value: ["CA"] })], BASICS_HOST, at("2026-10-25T10:00:00Z")).bank;
    expect(fillValue(b, field({ label: "Cell Biology Lab" }), GH)).toBeNull();
    expect(fillValue(b, field({ label: "Sales Region" }), GH)).toBeNull();
  });
});

describe("intents do not overreach", () => {
  const US = "Are you legally authorized to work in the United States?";
  test("an authorization answer for another country neither overwrites nor fills the US answer", () => {
    let b = emptyBank();
    b = save(b, field({ kind: "radio", label: US, options: ["Yes", "No"], value: ["Yes"] }), GH, "2026-10-01T10:00:00Z");
    b = save(b, field({ kind: "radio", label: "Are you authorized to work in the United Kingdom?", options: ["Yes", "No"], value: ["No"] }), "jobs.lever.co", "2026-10-20T10:00:00Z");
    expect(matchField(field({ kind: "radio", label: US, options: ["Yes", "No"] }), b, GH, NOW)?.value).toEqual(["Yes"]);
    // The UK question is its own answer, and the UK form still gets it.
    expect(matchField(field({ kind: "radio", label: "Are you authorized to work in the United Kingdom?", options: ["Yes", "No"] }), b, GH, NOW)?.value).toEqual(["No"]);
  });
  test("a work-authorization, sponsorship or relocation label that names another country reads as no intent", () => {
    for (const q of [
      "Are you authorized to work in the UK?", "Are you legally authorized to work in Canada?", "Do you have the right to work in the EU?",
      "Will you require visa sponsorship to work in India?", "Do you require sponsorship for Germany?", "Are you willing to relocate to Singapore?",
      "Are you open to relocation to the U.K.?", "Do you require sponsorship in the United States or Canada?",
    ]) expect([q, intentOf(q)]).toEqual([q, ""]);
  });
  test("the US wordings still read as their intents", () => {
    expect(intentOf(US)).toBe("authorized to work");
    expect(intentOf("Will you now or in the future require sponsorship?")).toBe("visa sponsorship");
    expect(intentOf("Do you require visa sponsorship to work in the US?")).toBe("visa sponsorship");
    expect(intentOf("Are you willing to relocate to San Francisco?")).toBe("open to relocation");
  });
  test("a sponsorship question phrased in the negative reads as no intent", () => {
    for (const q of [
      "Can you work for us without needing sponsorship?", "Are you able to work without requiring visa sponsorship?", "I do not require sponsorship",
      "Do you not require sponsorship now or in the future?", "I will no longer need sponsorship", "No sponsorship required", "Visa sponsorship not required",
    ]) expect([q, intentOf(q)]).toEqual([q, ""]);
  });
  test("'No' to 'Do you require sponsorship?' is not filled into the negative wording", () => {
    let b = emptyBank();
    b = save(b, field({ kind: "radio", label: "Do you require visa sponsorship?", options: ["Yes", "No"], value: ["No"] }), GH, "2026-10-01T10:00:00Z");
    expect(matchField(field({ kind: "radio", label: "Can you work for us without needing sponsorship?", options: ["Yes", "No"] }), b, GH, NOW)).toBeNull();
  });
  test("an authorization label phrased in the negative reads as no intent", () => {
    for (const q of ["Are you not legally authorized to work in the United States?", "Do you lack work authorization without sponsorship?"]) {
      expect([q, intentOf(q)]).toEqual([q, ""]);
    }
  });
});

describe("answers that go stale", () => {
  const start = (value: string, savedDaysAgo: number, label = "Earliest start date", kind: FieldInfo["kind"] = "text"): Bank =>
    save(emptyBank(), field({ kind, label, value: [value] }), GH, daysAgo(savedDaysAgo));
  const ask = (b: Bank, label: string, kind: FieldInfo["kind"] = "text") => matchField(field({ kind, label }), b, GH, NOW);

  test("an earliest start date older than 21 days is filled yellow, with its age", () => {
    const m = ask(start("Immediately", 30), "Earliest start date");
    expect(m?.value).toEqual(["Immediately"]);
    expect(m?.review).toBe(true);
    expect(m?.note).toContain("30 days");
  });
  test("a start date of today is not past in a US evening, when UTC has already moved to tomorrow", () => {
    const tz = process.env.TZ;
    process.env.TZ = "America/Los_Angeles";
    try {
      const evening = Date.parse("2026-10-29T03:00:00Z"); // 8 pm on 28 October in Los Angeles
      const b = save(emptyBank(), field({ label: "Earliest start date", value: ["2026-10-28"] }), GH, "2026-10-28T20:00:00Z");
      expect(matchField(field({ label: "Earliest start date" }), b, GH, evening)?.value).toEqual(["2026-10-28"]);
      expect(matchField(field({ label: "Earliest start date" }), b, GH, Date.parse("2026-10-30T03:00:00Z"))).toBeNull();
    } finally {
      if (tz === undefined) delete process.env.TZ; else process.env.TZ = tz;
    }
  });
  test("a fresh start date is a confident fill with no note", () => {
    const m = ask(start("Immediately", 5), "Earliest start date");
    expect(m?.review).toBe(false);
    expect(m?.note).toBeUndefined();
  });
  test("thresholds: deadlines 21, salary 90, location 180", () => {
    const cases: Array<[string, string, number, boolean]> = [
      ["Deadlines or timeline considerations", "none", 22, true], ["Deadlines or timeline considerations", "none", 20, false],
      ["Salary expectations", "150k", 91, true], ["Salary expectations", "150k", 60, false],
      ["Where will you be working from?", "Berlin", 181, true], ["Where will you be working from?", "Berlin", 100, false],
    ];
    for (const [label, value, age, review] of cases) {
      const m = ask(save(emptyBank(), field({ label, value: [value] }), GH, daysAgo(age)), label);
      expect([label, age, m?.review]).toEqual([label, age, review]);
    }
  });
  test("it is the answer's own intent, whichever wording the form uses", () => {
    const b = save(emptyBank(), field({ label: "Expected salary", name: "salary_expectation", value: ["150k"] }), GH, daysAgo(100));
    expect(ask(b, "Compensation range?")?.review).toBe(true);
  });
  test("a start date already in the past is not filled at all", () => {
    expect(ask(start("2026-09-15", 40, "Earliest start date", "date"), "Earliest start date", "date")).toBeNull();
    expect(ask(start("09/15/2026", 40), "Earliest start date")).toBeNull();
  });
  test("a start date still ahead is yellow once it is old, confident while new", () => {
    expect(ask(start("2026-12-01", 27, "Earliest start date", "date"), "Earliest start date", "date")?.review).toBe(true);
    expect(ask(start("2026-12-01", 3, "Earliest start date", "date"), "Earliest start date", "date")?.review).toBe(false);
  });
  test("a signature date saved on one form is not filled on the next", () => {
    let b = emptyBank();
    b = save(b, field({ kind: "date", label: "Date", name: "signature_date", value: ["2026-10-01"] }), GH, "2026-10-01T10:00:00Z");
    expect(matchField(field({ kind: "date", label: "Date", name: "signature_date" }), b, GH, NOW)).toBeNull();
  });
  test("a past date that is not about today or availability is still an answer (graduation, an old job's start)", () => {
    for (const label of ["Graduation date", "Start date"]) {
      const b = save(emptyBank(), field({ kind: "date", label, value: ["2019-05-15"] }), GH, daysAgo(400));
      const m = matchField(field({ kind: "date", label }), b, GH, NOW);
      expect([label, m?.value, m?.review]).toEqual([label, ["2019-05-15"], false]);
    }
  });
  test("junk that is not a calendar date is not read as one", () => {
    for (const v of ["1", "2026", "asap", "13/45/2026", "2026-13-40"]) {
      const m = ask(start(v, 5), "Earliest start date");
      expect([v, m?.value]).toEqual([v, [v]]);
    }
  });
});

describe("a fill read back is not the user answering", () => {
  const filled = (f: Partial<FieldInfo>, key: string, review: boolean) => field({ ...f, filledBy: { key, review } });
  test("a yellow fuzzy fill, submitted untouched, stays yellow for the new wording", () => {
    let b = emptyBank();
    b = save(b, field({ label: "Years of experience", value: ["8"] }), GH, "2026-10-01T10:00:00Z");
    const f = field({ label: "Years of experience with Java" });
    const m = matchField(f, b, GH, NOW);
    expect(m?.review).toBe(true);
    b = save(b, { ...f, value: ["8"], filledBy: { key: m!.answer.key, review: m!.review } }, GH, "2026-10-28T10:00:00Z");
    expect(Object.keys(b.answers)).toEqual(["years of experience"]);
    expect(matchField(f, b, GH, NOW)?.review).toBe(true);
  });
  test("the same fill read back from the same answer bumps nothing", () => {
    let b = emptyBank();
    b = save(b, field({ label: "Preferred name", name: "preferred_name", value: ["Bob"] }), GH, "2026-10-01T10:00:00Z");
    const before = b.answers["preferred name"]!;
    const r = mergeCapture(b, [filled({ label: "What should we call you?", name: "nick_name", value: ["Bob"] }, "preferred name", false)], "jobs.lever.co", at("2026-10-28T10:00:00Z"));
    expect(r.bank.answers).toEqual(b.answers);
    const same = mergeCapture(b, [filled({ label: "Preferred name", name: "preferred_name", value: ["Bob"] }, "preferred name", false)], GH, at("2026-10-28T10:00:00Z"));
    expect(same.bank.answers["preferred name"]).toEqual(before);
    expect(same.added + same.updated).toBe(0);
  });
  test("a review fill read back keeps the answer marked for review, and gains nothing else", () => {
    let b = emptyBank();
    b = save(b, field({ label: "Preferred name", name: "preferred_name", value: ["Bob"] }), GH, "2026-10-01T10:00:00Z");
    const r = mergeCapture(b, [filled({ label: "Preferred name", name: "other_id", value: ["Bob"] }, "preferred name", true)], "jobs.lever.co", at("2026-10-28T10:00:00Z"));
    const a = r.bank.answers["preferred name"]!;
    expect(a.review).toBe(true);
    expect([a.uses, a.labels, a.signatures, a.updatedAt, a.value]).toEqual([1, ["preferred name"], ["greenhouse.io|preferred_name"], b.answers["preferred name"]!.updatedAt, ["Bob"]]);
  });
  test("a fill that lands on another key never overwrites that answer", () => {
    let b = emptyBank();
    b = save(b, field({ label: "Phone", autocomplete: "tel", kind: "tel", value: ["555-0100"] }), GH, "2026-10-01T10:00:00Z");
    const r = mergeCapture(b, [filled({ label: "Phone", autocomplete: "tel", kind: "tel", value: ["555-0100"] }, "some other answer", false)], GH, at("2026-10-28T10:00:00Z"));
    expect(r.bank.answers).toEqual(b.answers);
  });
  test("a fill read back does not propagate through its intent", () => {
    let b = emptyBank();
    b = save(b, field({ kind: "radio", label: "Do you require visa sponsorship?", options: ["Yes", "No"], value: ["No"] }), GH, "2026-10-01T10:00:00Z");
    b = { ...b, answers: { ...b.answers, "do you need sponsorship": { key: "do you need sponsorship", labels: ["do you need sponsorship"], kind: "radio", value: ["Yes"], sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: "2026-10-10T10:00:00Z", review: false } } };
    const r = mergeCapture(b, [filled({ kind: "radio", label: "Do you require visa sponsorship?", options: ["Yes", "No"], value: ["No"] }, "do you require visa sponsorship", false)], GH, at("2026-10-28T10:00:00Z"));
    expect(r.bank.answers["do you need sponsorship"]).toEqual(b.answers["do you need sponsorship"]!);
  });
  test("a capture that is not a read-back still entrenches, as before", () => {
    let b = emptyBank();
    b = save(b, field({ label: "Preferred name", name: "preferred_name", value: ["Bob"] }), GH, "2026-10-01T10:00:00Z");
    b = save(b, field({ label: "Preferred name", name: "preferred_name", value: ["Bob"] }), GH, "2026-10-02T10:00:00Z");
    expect(b.answers["preferred name"]!.uses).toBe(2);
  });
});

describe("a popup edit reaches the sibling wordings of an intent", () => {
  test("editing the basic is seen by Fill even when a sibling wording had more uses", () => {
    let b = emptyBank();
    b = mergeCapture(b, [field({ label: "Visa sponsorship", kind: "select", options: ["Yes", "No"], value: ["No"] })], BASICS_HOST, at("2026-09-01T10:00:00Z")).bank;
    b = { ...b, answers: { ...b.answers, "do you need sponsorship": { key: "do you need sponsorship", labels: ["do you need sponsorship"], kind: "radio", value: ["No"], sensitivity: "standard", signatures: [], uses: 9, fills: 9, updatedAt: "2026-09-02T10:00:00Z", review: false } } };
    b = mergeCapture(b, [field({ label: "Visa sponsorship", kind: "select", options: ["Yes", "No"], value: ["Yes"] })], BASICS_HOST, at("2026-10-25T10:00:00Z")).bank;
    expect(matchField(field({ kind: "radio", label: "Do you need sponsorship?", options: ["Yes", "No"] }), b, GH, NOW)?.value).toEqual(["Yes"]);
  });
});

describe("an id that names another answer than the field's exact wording offers it as the alternative", () => {
  /** An answer by hand: mergeCapture would take a shared signature off every other row. */
  function row(key: string, over: Partial<Answer>): Answer {
    return { key, labels: [key], kind: "text", value: ["v"], sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: "2026-09-01T10:00:00Z", review: false, ...over };
  }
  const SIG = "greenhouse.io|q_ide";
  const bankOf = (...as: Answer[]): Bank => ({ ...emptyBank(), answers: Object.fromEntries(as.map(a => [a.key, a])) });
  const ask = (b: Bank, over: Partial<FieldInfo> = {}) => matchField(field({ label: "Primary IDE", name: "q_ide", ...over }), b, GH, NOW);
  const y = row("primary ide", { value: ["vim"] });

  test("several holders of the id: the newest one is the alternative, in its newest wording", () => {
    const older = row("favourite editor", { value: ["emacs"], signatures: [SIG], updatedAt: "2026-09-01T10:00:00Z" });
    const newer = row("favourite colour", { value: ["blue"], labels: ["favourite colour", "hue"], signatures: [SIG], updatedAt: "2026-10-01T10:00:00Z" });
    for (const b of [bankOf(y, older, newer), bankOf(newer, y, older), bankOf(older, newer, y)]) {
      const m = ask(b);
      expect(m?.answer.key).toBe("primary ide");
      expect(m?.value).toEqual(["vim"]);
      expect(m?.review).toBe(true);
      expect(m?.alternative).toEqual({ answer: newer, value: ["blue"], wording: "hue" });
    }
  });
  test("a wording-less holder is offered under its key", () => {
    const x = row("favourite colour", { value: ["blue"], labels: [], signatures: [SIG] });
    expect(ask(bankOf(y, x))?.alternative?.wording).toBe("favourite colour");
  });
  test("the alternative's value is mapped onto this field's options, and none is offered when it does not fit", () => {
    const x = row("favourite colour", { value: ["Emacs"], signatures: [SIG] });
    const options = ["Vim", "Emacs"];
    const m = ask(bankOf(row("primary ide", { value: ["Vim"] }), x), { kind: "select", options });
    expect(m?.value).toEqual(["Vim"]);
    expect(m?.alternative?.value).toEqual(["Emacs"]);
    const none = ask(bankOf(row("primary ide", { value: ["Vim"] }), row("favourite colour", { value: ["blue"], signatures: [SIG] })), { kind: "select", options });
    expect(none?.value).toEqual(["Vim"]);
    expect(none?.alternative).toBeUndefined();
  });
  test("none is offered for an answer that would itself be stale, nor from a self-ID answer that is switched off", () => {
    const aged = row("where will you be working from", { value: ["Berlin"], signatures: [SIG], updatedAt: daysAgo(200) });
    expect(ask(bankOf(y, aged))?.alternative).toBeUndefined();
    const selfId = row("favourite colour", { value: ["blue"], signatures: [SIG], sensitivity: "self_id" });
    const m = ask(bankOf(y, selfId));
    expect(m?.review).toBe(false); // an unusable holder is no ambiguity either
    expect(m?.alternative).toBeUndefined();
  });
  test("an id and a wording that agree, or an id with no exact-wording rival, carry no alternative", () => {
    const x = row("primary ide", { value: ["vim"], signatures: [SIG] });
    expect(ask(bankOf(x))?.alternative).toBeUndefined();
    expect(ask(bankOf(row("favourite colour", { value: ["blue"], signatures: [SIG] })))?.alternative).toBeUndefined();
    expect(ask(bankOf(y))?.alternative).toBeUndefined();
  });
});

describe("whyNoMatch says what was refused, and only that", () => {
  const dateField = (label: string) => field({ kind: "date", label });
  const saved = (label: string, value: string, kind: FieldInfo["kind"] = "date") => save(emptyBank(), field({ kind, label, value: [value] }), GH, daysAgo(40));
  const why = (b: Bank, f: FieldInfo) => whyNoMatch(f, b, GH, NOW);

  test("a start date that has gone by names the date as it was saved", () => {
    expect(why(saved("Earliest start date", "2026-09-15"), dateField("Earliest start date"))).toBe("Your saved start date (2026-09-15) has passed: enter a new one.");
    expect(why(saved("Earliest start date", "09/15/2026", "text"), field({ label: "Earliest start date" }))).toBe("Your saved start date (09/15/2026) has passed: enter a new one.");
    expect(why(saved("Earliest start date", "Oct 5, 2026", "text"), field({ label: "When can you start?" }))).toBe("Your saved start date (Oct 5, 2026) has passed: enter a new one.");
  });
  test("a deadline, and a date asked for today, are named for what they are", () => {
    expect(why(saved("Deadlines or timeline considerations", "2026-09-15", "text"), field({ label: "Any deadlines we should know about?" }))).toBe("Your saved deadline (2026-09-15) has passed: enter a new one.");
    const sign = save(emptyBank(), field({ kind: "date", label: "Date", name: "signature_date", value: ["2026-10-01"] }), GH, "2026-10-01T10:00:00Z");
    expect(why(sign, field({ kind: "date", label: "Date", name: "signature_date" }))).toBe("Your saved date (2026-10-01) has passed: enter a new one.");
  });
  test("a start date of today or ahead is filled, so there is nothing to say", () => {
    for (const d of ["2026-10-28", "2026-12-01"]) {
      const f = dateField("Earliest start date");
      expect(why(saved("Earliest start date", d), f)).toBeNull();
      expect(matchField(f, saved("Earliest start date", d), GH, NOW)).not.toBeNull();
    }
  });
  test("a saved choice that is not among this form's options is named, clipped to 40 characters", () => {
    const b = save(emptyBank(), field({ kind: "select", label: "Country", name: "country", options: ["Germany", "France"], value: ["Germany"] }), GH, "2026-10-01T10:00:00Z");
    const f = field({ kind: "select", label: "Country", name: "country", options: ["Spain", "Italy"] });
    expect(matchField(f, b, GH, NOW)).toBeNull();
    expect(why(b, f)).toBe(`Your saved answer "Germany" is not one of this form's options.`);
    const long = "The Federal Democratic Republic of Something Very Long Indeed";
    const lb = save(emptyBank(), field({ kind: "select", label: "Country", name: "country", options: [long, "France"], value: [long] }), GH, "2026-10-01T10:00:00Z");
    const msg = why(lb, f)!;
    expect(msg).toBe(`Your saved answer "The Federal Democratic Republic of Some…" is not one of this form's options.`);
    expect([...msg.slice(msg.indexOf('"') + 1, msg.lastIndexOf('"'))]).toHaveLength(40);
  });
  test("a multi-select that cannot take any saved value is named too", () => {
    const b = save(emptyBank(), field({ kind: "select", multi: true, label: "Languages", options: ["Go", "Rust"], value: ["Go", "Rust"] }), GH, "2026-10-01T10:00:00Z");
    expect(why(b, field({ kind: "select", multi: true, label: "Languages", options: ["Python", "Java"] }))).toBe(`Your saved answer "Go, Rust" is not one of this form's options.`);
  });
  test("nothing refused, nothing said: no answer, a filled field, a weak resemblance, a field that is not touched", () => {
    const years = save(emptyBank(), field({ label: "Years of experience", value: ["8"] }), GH, "2026-10-01T10:00:00Z");
    expect(why(emptyBank(), field({ label: "Anything" }))).toBeNull();
    expect(why(years, field({ label: "Years of experience" }))).toBeNull(); // filled
    // A fuzzy answer that maps to no option is skipped silently: it was never a confident claim on the question.
    const f = field({ kind: "select", label: "Years of experience with Java", options: ["Yes", "No"] });
    expect(matchField(f, years, GH, NOW)).toBeNull();
    expect(why(years, f)).toBeNull();
    expect(why(years, field({ kind: "password", label: "Years of experience" }))).toBeNull();
    const secret = save(emptyBank(), field({ label: "Favourite colour", value: ["blue"] }), GH, "2026-10-01T10:00:00Z");
    expect(why(secret, field({ label: "Social security number", kind: "select", options: ["a"] }))).toBeNull();
  });
  test("a single checkbox whose saved answer is neither yes nor no is not called a missing option", () => {
    const b = save(emptyBank(), field({ kind: "checkbox", label: "Subscribe to updates", options: ["Yes"], value: ["maybe"] }), GH, "2026-10-01T10:00:00Z");
    const f = field({ kind: "checkbox", label: "Subscribe to updates", options: ["Yes"] });
    expect(matchField(f, b, GH, NOW)).toBeNull();
    expect(why(b, f)).toBeNull();
  });
});

describe("a choice is never written as a different option that only reads alike", () => {
  test("C, C++ and C#: the exact option, or nothing", () => {
    const opts = ["C", "C++", "C#", "Go"];
    expect(mapChoice("C#", opts)).toBe("C#");
    expect(mapChoice("C++", opts)).toBe("C++");
    expect(mapChoice(" C ", opts)).toBe("C");
    expect(mapChoice("c#", opts)).toBeNull(); // reads as "c", which three different options also read as
    expect(mapChoice("Go", opts)).toBe("Go");
  });
  test("the same option shown twice in different case is still one option", () => {
    expect(mapChoice("yes", ["Yes", "YES", "No"])).toBe("Yes");
  });
  test("a token tie between different options is not guessed", () => {
    expect(mapChoice("Senior engineer", ["Engineer senior", "Senior engineer II"])).not.toBe("Senior engineer II");
    expect(mapChoice("senior engineer role", ["Role: senior engineer", "Engineer (senior) role"])).toBeNull();
    expect(mapChoice("engineer senior", ["Senior Engineer", "Engineer, Senior"])).toBe("Engineer, Senior"); // one reads exactly so
  });
});
