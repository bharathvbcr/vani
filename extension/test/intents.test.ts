import { describe, expect, test } from "bun:test";
import { type FieldInfo, emptyBank } from "../src/types";
import { mergeCapture } from "../src/bank";
import { mapChoice, matchField } from "../src/match";
import { INTENTS, intentOf, motivationEssay } from "../src/intents";

const now = new Date("2026-10-19T12:00:00Z");

function field(p: Partial<FieldInfo>): FieldInfo {
  return { kind: "text", label: "", name: "", autocomplete: "", automationId: "", options: [], required: false, value: [], ...p };
}

// Real wordings: job-boards.greenhouse.io/anthropic (2026-10) first, then common Lever, Ashby and Workday phrasings.
const POSITIVE: Array<[string, string]> = [
  ["Are you open to working in-person in one of our offices 25% of the time?*", "open to working in person"],
  ["Are you able to work onsite 3 days a week?", "open to working in person"],
  ["This is a hybrid role. Are you willing to work from our San Francisco office?", "open to working in person"],
  ["When is the earliest you would want to start working with us?", "earliest start date"],
  ["What is your notice period?", "earliest start date"],
  ["How soon could you start?", "earliest start date"],
  ["Do you have any deadlines or timeline considerations we should be aware of?", "deadlines or timeline considerations"],
  ["Are you currently interviewing elsewhere or holding competing offers?", "deadlines or timeline considerations"],
  ["AI Policy for Application*", "ai policy for applications"],
  ["Do you require visa sponsorship? *", "visa sponsorship"],
  ["Will you now or will you in the future require employment visa sponsorship to work in the country in which the job you're applying for is located?", "visa sponsorship"],
  ["Will you now, or in the future, require sponsorship for employment visa status (e.g. H-1B visa status)?", "visa sponsorship"],
  ["Are you legally authorized to work in the United States?", "authorized to work"],
  ["Are you eligible to work in the UK?", "authorized to work"],
  ["Are you open to relocation for this role? *", "open to relocation"],
  ["Would you be willing to relocate to New York?", "open to relocation"],
  ["Have you ever interviewed at Anthropic before?*", "interviewed here before"],
  ["Have you previously interviewed with us?", "interviewed here before"],
  ["Have you previously applied to Acme?", "applied here before"],
  ["How did you hear about this job?", "how did you hear about us"],
  ["Where did you learn about this position?", "how did you hear about us"],
  ["What are your salary expectations?", "salary expectations"],
  ["Desired compensation", "salary expectations"],
  ["Gender", "gender"],
  ["Are you Hispanic/Latino?", "hispanic or latino"],
  ["Please identify your race", "race"],
  ["Veteran Status", "veteran status"],
  ["Disability Status", "disability status"],
  ["What is the address from which you plan on working? If you would need to relocate, please type \"relocating\".", "where youll work from"],
  ["Where are you currently based?", "where youll work from"],
  ["What is your current location?", "where youll work from"],
  ["Please read the arbitration agreement below*", "arbitration agreement"],
  ["Agreement to Arbitrate*", "arbitration agreement"],
];

// Each of these must resolve to no intent: a wrong intent fills a confident wrong answer.
const NEGATIVE = [
  "Street address",
  "Address line 1",
  "Which office location would you prefer?",
  "Have you worked with Kubernetes before?",
  "Have you interviewed candidates before?",
  "Have you ever worked for a startup?",
  "Please read our privacy policy",
  "Why Anthropic?*",
  "Start date", // a past job's start date in a work-history block
  "(Optional) Personal Preferences",
  "First Name",
  "Website",
  "Additional Information",
  // Two intents with opposite answers: ambiguous, so neither.
  "Are you legally authorized to work in the US without the need for visa sponsorship?",
  // A repeated section's later entry keeps its own key.
  "What is your notice period? (vani section 2)",
];

describe("intents", () => {
  test.each(POSITIVE)("%s → %s", (label, key) => {
    expect(intentOf(label)).toBe(key);
  });

  test.each(NEGATIVE.map(l => [l]))("no intent: %s", label => {
    expect(intentOf(label)).toBe("");
  });

  test("every intent's own label resolves to it, and keys are unique", () => {
    for (const i of INTENTS) expect(intentOf(i.label)).toBe(i.key);
    expect(new Set(INTENTS.map(i => i.key)).size).toBe(INTENTS.length);
  });
});

describe("intent matching", () => {
  const host = "job-boards.greenhouse.io";
  // Answers saved from the popup's basics, under their canonical labels (test values only).
  const bank = mergeCapture(emptyBank(), [
    field({ label: "Visa sponsorship", kind: "select", value: ["No"] }),
    field({ label: "Open to working in person", kind: "select", value: ["Yes"] }),
    field({ label: "AI policy for applications", kind: "select", value: ["Yes"] }),
    field({ label: "Earliest start date", value: ["In two weeks"] }),
    field({ label: "Deadlines or timeline considerations", value: ["None at the moment"] }),
  ], "vani-basics", now).bank;

  test("one saved answer fills every wording of the question, confidently", () => {
    for (const [label, want] of [
      ["Do you require visa sponsorship? *", "No"],
      ["Will you now or will you in the future require employment visa sponsorship to work in the country in which the job you're applying for is located?", "No"],
      ["Are you open to working in-person in one of our offices 25% of the time?*", "Yes"],
    ] as const) {
      const m = matchField(field({ label, kind: "select", options: ["Yes", "No"] }), bank, host);
      expect({ label, via: m?.via, value: m?.value, review: m?.review }).toEqual({ label, via: "intent", value: [want], review: false });
    }
    expect(matchField(field({ label: "When is the earliest you would want to start working with us?" }), bank, host)?.value).toEqual(["In two weeks"]);
    expect(matchField(field({ label: "Do you have any deadlines or timeline considerations we should be aware of?" }), bank, host)?.value).toEqual(["None at the moment"]);
  });

  test("an acknowledgement always fills for review (yellow)", () => {
    const m = matchField(field({ label: "AI Policy for Application*", kind: "select", options: ["Yes", "No"] }), bank, host);
    expect(m?.value).toEqual(["Yes"]);
    expect(m?.review).toBe(true);
  });

  test("an answer to the exact wording outranks the intent's answer", () => {
    const b = mergeCapture(bank, [field({ label: "Do you require visa sponsorship?", kind: "select", value: ["Yes"] })], "jobs.lever.co", now).bank;
    expect(matchField(field({ label: "Do you require visa sponsorship?", kind: "select", options: ["Yes", "No"] }), b, host)?.value).toEqual(["Yes"]);
  });

  test("across wordings the latest answer wins", () => {
    const later = new Date(now.getTime() + 60_000);
    const b = mergeCapture(bank, [field({ label: "Will you require sponsorship now or in the future?", kind: "radio", options: ["Yes", "No"], value: ["Yes"] })], "jobs.lever.co", later).bank;
    const m = matchField(field({ label: "Do you need a visa sponsor to work here?", kind: "select", options: ["Yes", "No"] }), b, host);
    expect(m?.value).toEqual(["Yes"]);
  });

  test("an arbitration agreement is filled only once you opt in, and always for review", () => {
    const opted = mergeCapture(bank, [field({ label: "Arbitration agreement", kind: "select", value: ["I agree"] })], "vani-basics", now).bank;
    for (const [label, only] of [
      ["Please read the arbitration agreement below*", "I will read the arbitration agreement below."],
      ["Agreement to Arbitrate*", "I understand and agree to the terms of the Agreement to Arbitrate set forth above."],
    ] as const) {
      const m = matchField(field({ label, kind: "select", options: [only] }), opted, host);
      expect({ label, value: m?.value, review: m?.review }).toEqual({ label, value: [only], review: true });
    }
  });

  test("where you will work from fills the address question", () => {
    const b = mergeCapture(bank, [field({ label: "Where youll work from", value: ["Testville, TX"] })], "vani-basics", now).bank;
    expect(matchField(field({ label: "What is the address from which you plan on working? If you would need to relocate, please type \"relocating\"." }), b, host)?.value).toEqual(["Testville, TX"]);
  });

  test("ambiguous and unrelated questions borrow nothing", () => {
    expect(matchField(field({ label: "Are you legally authorized to work in the US without the need for visa sponsorship?", kind: "select", options: ["Yes", "No"] }), bank, host)).toBe(null);
    expect(matchField(field({ label: "Agreement to Arbitrate*", kind: "select", options: ["I understand and agree to the terms of the Agreement to Arbitrate set forth above."] }), bank, host)).toBe(null);
  });

  test("self-identification intents stay off until self-ID is turned on", () => {
    const b = emptyBank();
    b.settings.captureSelfId = true;
    const on = mergeCapture(b, [field({ label: "Gender", kind: "select", value: ["Decline to self-identify"] })], "vani-basics", now).bank;
    const gh = field({ label: "Gender", name: "gender", kind: "select", options: ["Male", "Female", "Decline To Self Identify"] });
    expect(matchField(gh, on, host)?.value).toEqual(["Decline To Self Identify"]);
    const off = { ...on, settings: { ...on.settings, captureSelfId: false } };
    expect(matchField(gh, off, host)).toBe(null);
  });
});

describe("declining to answer", () => {
  test("one saved 'decline' maps onto each form's own wording", () => {
    expect(mapChoice("Decline to self-identify", ["Male", "Female", "Decline To Self Identify"])).toBe("Decline To Self Identify");
    expect(mapChoice("Decline to self-identify", ["I am a protected veteran", "I am not a protected veteran", "I don't wish to answer"])).toBe("I don't wish to answer");
    expect(mapChoice("Decline to self-identify", ["Yes, I have a disability", "No, I do not have a disability", "I do not want to answer"])).toBe("I do not want to answer");
    expect(mapChoice("Prefer not to say", ["Yes", "No", "Decline to self identify"])).toBe("Decline to self identify");
    // job-boards.greenhouse.io/anthropic (2026-10), as the form lists them.
    const ghVeteran = ["I am not a protected veteran", "I identify as one or more of the classifications of a protected veteran", "I don't wish to answer"];
    expect(mapChoice("Decline to self-identify", ghVeteran)).toBe("I don't wish to answer");
    expect(mapChoice("I identify as one or more of the classifications of protected veteran", ghVeteran)).toBe(ghVeteran[1]!);
    expect(mapChoice("I am not a protected veteran", ghVeteran)).toBe(ghVeteran[0]!);
    expect(mapChoice("Decline to self-identify", ["Yes", "No", "Decline To Self Identify"])).toBe("Decline To Self Identify");
    // No decline option on the form: left alone, never mapped to Yes or No.
    expect(mapChoice("Decline to self-identify", ["Yes", "No"])).toBe(null);
    // Agreeing takes the one acknowledgement a form offers, never a refusal.
    expect(mapChoice("I agree", ["I will read the arbitration agreement below."])).toBe("I will read the arbitration agreement below.");
    expect(mapChoice("I agree", ["I acknowledge", "I do not acknowledge"])).toBe("I acknowledge");
    expect(mapChoice("I agree", ["I agree", "I disagree"])).toBe("I agree");
    expect(mapChoice("I agree", ["I accept", "I also accept the newsletter"])).toBe(null); // two acknowledgements: ambiguous
    expect(mapChoice("Yes", ["I am authorized", "I am not authorized"])).toBe(null);
    // A real answer is not read as declining.
    expect(mapChoice("No", ["Yes", "No", "I don't wish to answer"])).toBe("No");
  });
});

describe("company-motivation essays", () => {
  test.each([
    ["Why Anthropic?*"], ["Why us?"], ["Why do you want to work here?"], ["Why do you want to join Acme?"],
    ["What excites you about this role?"], ["Why are you interested in this position?"], ["What draws you to our mission?"],
  ])("is one: %s", label => {
    expect(motivationEssay(label)).toBe(true);
  });

  test.each([
    ["Why are you leaving your current role?"], ["Additional Information"], ["Describe a project you are proud of"],
    ["Do you require visa sponsorship?"], ["What is your notice period?"],
  ])("is not one: %s", label => {
    expect(motivationEssay(label)).toBe(false);
  });

  test("a saved essay is never filled into a motivation question, here or at another company", () => {
    const essay = "x ".repeat(300);
    const bank = mergeCapture(emptyBank(), [
      field({ label: "Why Anthropic?", kind: "textarea", value: [essay] }),
      field({ label: "Why do you want to work here?", kind: "textarea", value: [essay] }),
      field({ label: "Why are you leaving your current role?", kind: "textarea", value: ["Growth"] }),
    ], "job-boards.greenhouse.io", now).bank;
    const host = "job-boards.greenhouse.io";
    expect(matchField(field({ label: "Why Anthropic?*", kind: "textarea" }), bank, host)).toBe(null);
    expect(matchField(field({ label: "Why do you want to work here?", kind: "textarea" }), bank, host)).toBe(null);
    // Not about the company: the saved answer still fills.
    expect(matchField(field({ label: "Why are you leaving your current role?", kind: "textarea" }), bank, host)?.value).toEqual(["Growth"]);
  });
});
