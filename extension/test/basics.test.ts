import { describe, expect, test } from "bun:test";
import { type FieldInfo, emptyBank } from "../src/types";
import { MAX_VALUE_CHARS, mergeCapture } from "../src/bank";
import { matchField } from "../src/match";
import { BASICS, BASICS_HOST, basicsFields, basicsSaveOutcome, basicsValues } from "../src/basics";
import { intentOf } from "../src/intents";

const now = new Date("2026-10-19T12:00:00Z");

function field(p: Partial<FieldInfo>): FieldInfo {
  return { kind: "text", label: "", name: "", autocomplete: "", automationId: "", options: [], required: false, value: [], ...p };
}

// Test values only.
const ENTERED = {
  first: "Test", last: "Applicant", email: "applicant@example.invalid", phone: "+1 555 555 0100",
  city: "Testville", country: "United States", linkedin: "https://www.linkedin.com/in/test-applicant",
  github: "https://github.com/test-applicant", website: "https://example.invalid",
};

describe("basics", () => {
  const saved = mergeCapture(emptyBank(), basicsFields(ENTERED), BASICS_HOST, now);

  test("every basic is saved, under the key a form's field resolves to", () => {
    expect(saved.added).toBe(Object.keys(ENTERED).length);
    expect(saved.skippedSecret + saved.skippedSelfId + saved.skippedEmpty).toBe(0);
    for (const k of ["first name", "last name", "email", "phone", "city", "country", "linkedin", "github", "website"]) {
      expect(saved.bank.answers[k]?.value.length).toBe(1);
    }
  });

  test("a basic carries no per-site signature and is never a draft", () => {
    for (const a of Object.values(saved.bank.answers)) {
      expect(a.signatures).toEqual([]);
      expect(a.review).toBe(false);
    }
  });

  test("the basics fill job-boards.greenhouse.io's generic fields, confidently", () => {
    const host = "job-boards.greenhouse.io";
    const form: Array<[FieldInfo, string[]]> = [
      [field({ label: "First Name*", name: "first_name", autocomplete: "given-name" }), ["Test"]],
      [field({ label: "Last Name*", name: "last_name", autocomplete: "family-name" }), ["Applicant"]],
      [field({ label: "Email*", name: "email", autocomplete: "email" }), [ENTERED.email]],
      [field({ label: "Phone", name: "phone", kind: "tel", autocomplete: "off" }), [ENTERED.phone]],
      [field({ label: "LinkedIn Profile", name: "question_13562049008" }), [ENTERED.linkedin]],
      [field({ label: "Website", name: "question_13562040008" }), [ENTERED.website]],
      // The Country combobox's options are only on the page once it is opened.
      [field({ label: "Country", name: "country", kind: "select", autocomplete: "off" }), ["United States"]],
      [field({ label: "Country", kind: "select", options: ["United Kingdom +44", "United States +1"] }), ["United States +1"]],
    ];
    for (const [f, want] of form) {
      const m = matchField(f, saved.bank, host);
      expect({ label: f.label, value: m?.value, review: m?.review }).toEqual({ label: f.label, value: want, review: false });
    }
  });

  test("basic ids are unique, and every question basic is keyed where its intent is", () => {
    expect(new Set(BASICS.map(b => b.id)).size).toBe(BASICS.length);
    for (const b of BASICS.filter(x => x.group !== "contact")) expect(intentOf(b.label)).not.toBe("");
  });

  test("the question basics fill the form's own wordings", () => {
    const work = mergeCapture(saved.bank, basicsFields({
      "visa-sponsorship": "No", "open-to-working-in-person": "Yes", "open-to-relocation": "Yes",
      "interviewed-here-before": "No", "earliest-start-date": "In two weeks", "ai-policy-for-applications": "Yes",
    }), BASICS_HOST, now);
    expect(work.added).toBe(6);
    const yn = (label: string) => field({ label, kind: "select", options: ["Yes", "No"] });
    const host = "job-boards.greenhouse.io";
    expect(matchField(yn("Will you now or will you in the future require employment visa sponsorship to work in the country in which the job you're applying for is located?"), work.bank, host)?.value).toEqual(["No"]);
    expect(matchField(yn("Are you open to working in-person in one of our offices 25% of the time?*"), work.bank, host)?.value).toEqual(["Yes"]);
    expect(matchField(yn("Have you ever interviewed at Anthropic before?*"), work.bank, host)?.value).toEqual(["No"]);
    expect(matchField(field({ label: "When is the earliest you would want to start working with us?" }), work.bank, host)?.value).toEqual(["In two weeks"]);
    const ai = matchField(yn("AI Policy for Application*"), work.bank, host);
    expect([ai?.value, ai?.review]).toEqual([["Yes"], true]);
  });

  test("a question answered on a form shows in its basic, as Fill would use it", () => {
    const b = mergeCapture(emptyBank(), [
      field({ label: "Do you require visa sponsorship?", kind: "select", options: ["Yes", "No"], value: ["No"] }),
    ], "job-boards.greenhouse.io", now).bank;
    expect(basicsValues(b)["visa-sponsorship"]).toBe("No");
    // Unchanged in the popup: nothing to send.
    expect(basicsFields(basicsValues(b), basicsValues(b))).toEqual([]);
  });

  test("a saved self-ID answer is not shown while self-ID is off", () => {
    const b = emptyBank();
    b.settings.captureSelfId = true;
    const on = mergeCapture(b, basicsFields({ gender: "Decline to self-identify" }), BASICS_HOST, now).bank;
    expect(basicsValues(on).gender).toBe("Decline to self-identify");
    expect(basicsValues({ ...on, settings: { ...on.settings, captureSelfId: false } }).gender).toBe("");
  });

  test("self-identification basics are refused while self-ID is off", () => {
    const r = mergeCapture(emptyBank(), basicsFields({ gender: "Decline to self-identify" }), BASICS_HOST, now);
    expect([r.added, r.skippedSelfId]).toEqual([0, 1]);
  });

  test("an empty box saves nothing and clears nothing", () => {
    const again = mergeCapture(saved.bank, basicsFields({ first: "  ", email: "" }), BASICS_HOST, now);
    expect(again.added + again.updated).toBe(0);
    expect(again.bank.answers["first name"]?.value).toEqual(["Test"]);
  });

  test("saving a basic supersedes older specific question wordings for the same intent", () => {
    const olderDate = new Date("2024-01-01T00:00:00Z");
    const newerDate = new Date("2026-10-19T12:00:00Z");
    const specificLabel = "Will you now or will you in the future require employment visa sponsorship to work in the country in which the job you're applying for is located?";
    // Old capture from greenhouse with "Yes"
    const prior = mergeCapture(emptyBank(), [
      field({ label: specificLabel, kind: "select", options: ["Yes", "No"], value: ["Yes"] }),
    ], "job-boards.greenhouse.io", olderDate).bank;

    // User updates their basic in the popup to "No"
    const updated = mergeCapture(prior, basicsFields({ "visa-sponsorship": "No" }), BASICS_HOST, newerDate).bank;

    // A form asking the specific question wording must now fill "No"
    const yn = field({ label: specificLabel, kind: "select", options: ["Yes", "No"] });
    const m = matchField(yn, updated, "job-boards.greenhouse.io");
    expect(m?.value).toEqual(["No"]);
  });

  test("clearing a previously saved basic removes it from the bank", () => {
    const savedValues = basicsValues(saved.bank);
    expect(savedValues.phone).toBe(ENTERED.phone);
    // User clears phone in the basics popup
    const clearedFields = basicsFields({ ...savedValues, phone: "" }, savedValues);
    expect(clearedFields.length).toBeGreaterThan(0);
    const after = mergeCapture(saved.bank, clearedFields, BASICS_HOST, now).bank;
    expect(basicsValues(after).phone).toBe("");
    expect(after.answers["phone"]).toBeUndefined();
  });

  test("clearing a previously saved intent basic removes it and its wordings from the bank", () => {
    const work = mergeCapture(emptyBank(), basicsFields({ "visa-sponsorship": "No" }), BASICS_HOST, now).bank;
    const workValues = basicsValues(work);
    expect(workValues["visa-sponsorship"]).toBe("No");
    const cleared = basicsFields({ ...workValues, "visa-sponsorship": "" }, workValues);
    const after = mergeCapture(work, cleared, BASICS_HOST, now).bank;
    expect(basicsValues(after)["visa-sponsorship"]).toBe("");
  });

  test("a basic that still shows the saved value is not sent again", () => {
    const shown = basicsValues(saved.bank);
    expect(basicsFields(shown, shown)).toEqual([]);
    const fs = basicsFields({ ...shown, email: "new@example.invalid" }, shown);
    expect(fs.map(f => [f.label, f.value])).toEqual([["Email", ["new@example.invalid"]]]);
  });

  test("unknown ids and oversized values are not carried", () => {
    const fs = basicsFields({ first: "x".repeat(MAX_VALUE_CHARS * 2), password: "hunter2" } as Record<string, string>);
    expect(fs.map(f => f.label)).toEqual(["First name"]);
    expect(fs[0]!.value[0]!.length).toBe(MAX_VALUE_CHARS);
  });

  test("the popup shows what the bank holds for each basic, whatever form taught it", () => {
    const fromForm = mergeCapture(emptyBank(), [
      field({ label: "Legal first name", autocomplete: "given-name", value: ["Sam"] }),
      field({ label: "LinkedIn", value: ["https://linkedin.example/sam"] }),
    ], "jobs.lever.co", now).bank;
    const v = basicsValues(fromForm);
    expect(v.first).toBe("Sam");
    expect(v.linkedin).toBe("https://linkedin.example/sam");
    expect(v.email).toBe("");
    expect(basicsValues(null).first).toBe("");
  });
});

describe("basicsSaveOutcome", () => {
  const counts = { added: 0, updated: 0, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 0 };

  test("a save that landed says how many, and what was left out and why", () => {
    expect(basicsSaveOutcome({ ...counts, added: 2, updated: 1 })).toEqual({ saved: true, status: "Saved 3.", toast: "Saved 3 basics." });
    expect(basicsSaveOutcome({ ...counts, added: 1 }).toast).toBe("Saved 1 basic.");
    const o = basicsSaveOutcome({ ...counts, added: 1, skippedSecret: 1, skippedSelfId: 2, skippedEmpty: 1 });
    expect(o.saved).toBe(true);
    expect(o.status).toContain("1 refused as sensitive");
    expect(o.status).toContain("2 kept out while self-ID is off");
    expect(o.status).toContain("1 not saved");
  });

  test("everything skipped is not dressed up as a save", () => {
    const o = basicsSaveOutcome({ ...counts, skippedSecret: 2 });
    expect(o.status).toBe("Nothing saved; 2 refused as sensitive.");
    expect(o.toast).toBe("No basics were saved.");
  });

  test("a failure carries its reason into the status and the toast", () => {
    const o = basicsSaveOutcome({ error: "this browser did not save it (the Mac app is not reachable): storage is read-only" });
    expect(o.saved).toBe(false);
    expect(o.status).toBe("Could not save: this browser did not save it (the Mac app is not reachable): storage is read-only");
    expect(o.toast.startsWith("Basics not saved: this browser did not save it")).toBe(true);
    expect(o.toast.length).toBeLessThanOrEqual("Basics not saved: ".length + 90);
  });

  test("no reply, an empty error, or a malformed reply is a failure, never a pass", () => {
    for (const r of [undefined, null, {}, { error: "" }, { error: "   " }, { error: 3 }, { added: 1 }, { ...counts, added: -1 }, { ...counts, added: Number.NaN }, "ok", []]) {
      const o = basicsSaveOutcome(r);
      expect(o.saved).toBe(false);
      expect(o.status).toStartWith("Could not save: ");
    }
    expect(basicsSaveOutcome(undefined).status).toBe("Could not save: no reply from the extension");
  });

  test("an oversized error is cut for the toast, kept whole in the status", () => {
    const long = "x".repeat(5000);
    const o = basicsSaveOutcome({ error: long });
    expect(o.status).toBe(`Could not save: ${long}`);
    expect(o.toast.endsWith("…")).toBe(true);
    expect(o.toast.length).toBe("Basics not saved: ".length + 90);
  });
});
