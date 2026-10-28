import { describe, expect, test } from "bun:test";
import { auditForm, hasValue, isInvalid, jumpToField } from "../src/formaudit";
import type { FieldRef } from "../src/extract";
import type { FieldInfo } from "../src/types";

function createMockEl(tagName: string = "input"): any {
  const attrs: Record<string, string> = {};
  const classes = new Set<string>();
  const style: Record<string, string> = {};
  let text = "";
  let val = "";
  let selIndex = -1;
  const opts: any[] = [];

  const el = {
    tagName: tagName.toUpperCase(),
    type: tagName === "select" ? "select-one" : "text",
    get value() { return val; },
    set value(v: string) { val = v; },
    get text() { return text; },
    set text(t: string) { text = t; },
    get textContent() { return text; },
    set textContent(t: string) { text = t; },
    checked: false,
    files: null,
    style,
    options: opts,
    get selectedIndex() { return selIndex; },
    set selectedIndex(i: number) { selIndex = i; },
    append(...children: any[]) {
      for (const c of children) opts.push(c);
    },
    classList: {
      contains: (c: string) => classes.has(c),
      add: (c: string) => classes.add(c),
      remove: (c: string) => classes.delete(c),
    },
    getAttribute: (name: string) => attrs[name] ?? null,
    setAttribute: (name: string, v: string) => {
      attrs[name] = v;
      if (name === "class") v.split(/\s+/).forEach(c => classes.add(c));
    },
    focus: () => {},
    scrollIntoView: () => {},
    remove: () => {},
    closest: (_selector: string) => null,
    querySelector: (_selector: string) => null,
  };
  return el;
}

if (typeof document === "undefined") {
  (globalThis as any).document = {
    createElement: (tag: string) => createMockEl(tag),
    body: { append: () => {} },
  };
}

function mockRef(info: Partial<FieldInfo>, el: HTMLElement): FieldRef {
  return {
    info: {
      kind: "text",
      label: "Field",
      name: "field",
      autocomplete: "",
      automationId: "",
      options: [],
      required: false,
      value: [],
      ...info,
    },
    els: [el],
    widget: "native",
    manual: "",
  };
}

describe("form audit & missed fillings", () => {
  test("hasValue correctly evaluates various form controls", () => {
    // Text input
    const inputEmpty = document.createElement("input");
    inputEmpty.value = "";
    expect(hasValue(mockRef({ kind: "text" }, inputEmpty))).toBe(false);

    const inputFilled = document.createElement("input");
    inputFilled.value = "Jane Doe";
    expect(hasValue(mockRef({ kind: "text" }, inputFilled))).toBe(true);

    // Checkbox
    const cb = document.createElement("input");
    cb.type = "checkbox";
    cb.checked = false;
    expect(hasValue(mockRef({ kind: "checkbox" }, cb))).toBe(false);
    cb.checked = true;
    expect(hasValue(mockRef({ kind: "checkbox" }, cb))).toBe(true);

    // Select
    const sel = document.createElement("select");
    const optDefault = document.createElement("option");
    optDefault.value = "";
    optDefault.text = "Select an option...";
    const optVal = document.createElement("option");
    optVal.value = "US";
    optVal.text = "United States";
    sel.append(optDefault, optVal);
    sel.selectedIndex = 0;
    expect(hasValue(mockRef({ kind: "select" }, sel))).toBe(false);
    sel.selectedIndex = 1;
    expect(hasValue(mockRef({ kind: "select" }, sel))).toBe(true);

    // File
    const file = document.createElement("input");
    file.type = "file";
    expect(hasValue(mockRef({ kind: "file" }, file))).toBe(false);
  });

  test("auditForm identifies empty required fields and missing files", () => {
    const el1 = document.createElement("input");
    el1.value = "";
    const ref1 = mockRef({ label: "Full Name", required: true }, el1);

    const el2 = document.createElement("input");
    el2.value = "jane@example.com";
    const ref2 = mockRef({ label: "Email", required: true }, el2);

    const el3 = document.createElement("input");
    el3.type = "file";
    const ref3 = mockRef({ label: "Resume / CV", kind: "file", required: true }, el3);

    const report = auditForm([ref1, ref2, ref3]);
    expect(report.totalFields).toBe(3);
    expect(report.missedCount).toBe(2);
    expect(report.isReadyToSubmit).toBe(false);
    expect(report.fileMissing).toBe(true);

    const reasons = report.missed.map(m => m.reason);
    expect(reasons).toContain("empty_required");
    expect(reasons).toContain("unattached_file");
  });

  test("auditForm detects invalid inputs", () => {
    const el = document.createElement("input");
    el.value = "not-an-email";
    el.setAttribute("aria-invalid", "true");
    const ref = mockRef({ label: "Work Email", required: false }, el);

    const report = auditForm([ref]);
    expect(report.missedCount).toBe(1);
    expect(report.missed[0]!.reason).toBe("invalid");
  });

  test("jumpToField focuses and applies highlight pulse", () => {
    const el = document.createElement("input");
    document.body.append(el);
    el.scrollIntoView = () => {}; // mock
    const ref = mockRef({ label: "Test Field" }, el);

    jumpToField(ref);
    expect(el.style.outline).toContain("solid");
    expect(el.style.boxShadow).toContain("rgba(245, 158, 11");
    el.remove();
  });

  test("auditForm identifies manual_action fields requiring human intervention", () => {
    const el = document.createElement("div");
    const ref = mockRef({ label: "Verification CAPTCHA", required: true }, el);
    ref.manual = "captcha";

    const report = auditForm([ref]);
    expect(report.missedCount).toBe(1);
    expect(report.missed[0]!.reason).toBe("manual_action");
    expect(report.missed[0]!.description).toContain("manual input (captcha)");
  });

  test("hasValue recognizes attached file pill or data-file-attached attribute", () => {
    const fileInput = document.createElement("input");
    fileInput.type = "file";
    fileInput.setAttribute("data-file-attached", "true");
    const ref = mockRef({ kind: "file", label: "Resume" }, fileInput);

    expect(hasValue(ref)).toBe(true);
  });

  test("hasValue does not falsely mark second file input as attached when container has multiple file inputs", () => {
    // Form with two file inputs
    const formContainer = document.createElement("div");
    const resumeInput = document.createElement("input");
    resumeInput.type = "file";
    const coverLetterInput = document.createElement("input");
    coverLetterInput.type = "file";

    formContainer.append(resumeInput, coverLetterInput);
    // Add mock querySelectorAll to find the two inputs
    formContainer.querySelectorAll = ((sel: string) => sel.includes("file") ? [resumeInput, coverLetterInput] : []) as any;
    // Simulate attached pill inside the shared form
    formContainer.querySelector = ((sel: string) => sel.includes("file-name") ? document.createElement("div") : null) as any;
    coverLetterInput.closest = () => formContainer;

    const coverLetterRef = mockRef({ kind: "file", label: "Cover Letter" }, coverLetterInput);
    expect(hasValue(coverLetterRef)).toBe(false);
  });

  test("jumpToField called consecutively preserves original styles cleanly", () => {
    const el = document.createElement("input");
    el.style.outline = "2px solid #2e8b57";
    document.body.append(el);
    el.scrollIntoView = () => {};
    const ref = mockRef({ label: "Outlined Field" }, el);

    // Call jumpToField twice rapidly
    jumpToField(ref);
    expect(el.style.outline).toBe("3px solid #f59e0b");
    jumpToField(ref);
    expect(el.style.outline).toBe("3px solid #f59e0b");
    el.remove();
  });

  test("characterization: a long value in a field with no limit is not a finding", () => {
    const el = document.createElement("input");
    el.value = "x".repeat(5000);
    const report = auditForm([mockRef({ label: "About you" }, el)]);
    expect(report.missedCount).toBe(0);
    expect(report.isReadyToSubmit).toBe(true);
  });
});

// The browser reports tooLong only for a value a person typed, so a value another tool (or this extension in an
// earlier version) wrote past the limit passes the invalid check. The audit counts the value itself, in UTF-16 code
// units as maxlength does, and only reads: it never changes a field.
describe("form audit: a value over the field's character limit", () => {
  const limited = (tag: string, limit: string | null, value: string): any => {
    const el = document.createElement(tag);
    if (limit !== null) el.setAttribute("maxlength", limit);
    el.value = value;
    return el;
  };
  const audit = (el: any, info: Partial<FieldInfo> = {}, widget: FieldRef["widget"] = "native") => {
    const ref = mockRef({ label: "About you", ...info }, el);
    ref.widget = widget;
    return auditForm([ref]);
  };

  test("a value longer than the limit is reported, with the limit and the length", () => {
    const el = limited("input", "5", "abcdefghij");
    const report = audit(el);
    expect(report.missedCount).toBe(1);
    expect(report.isReadyToSubmit).toBe(false);
    const m = report.missed[0]!;
    expect(m).toMatchObject({ id: "missed-0", label: "About you", kind: "text", reason: "too_long", refIndex: 0 });
    expect(m.description).toBe('Field "About you" holds 10 characters but allows 5. Shorten it by hand.');
  });
  test("it is read-only: value, attributes and style are as they were", () => {
    const el = limited("input", "5", "abcdefghij");
    el.style.outline = "2px solid red";
    audit(el);
    expect(el.value).toBe("abcdefghij");
    expect(el.getAttribute("maxlength")).toBe("5");
    expect(el.style).toEqual({ outline: "2px solid red" });
  });
  test("a value exactly at the limit, or under it, is not reported", () => {
    expect(audit(limited("input", "5", "abcde")).missedCount).toBe(0);
    expect(audit(limited("input", "5", "abcd")).missedCount).toBe(0);
  });
  test("limit 0 reports any value; an empty field is not over a limit of 0", () => {
    expect(audit(limited("input", "0", "x")).missed[0]!.reason).toBe("too_long");
    expect(audit(limited("input", "0", "")).missedCount).toBe(0);
  });
  test("no limit, a negative or a malformed one is no finding", () => {
    for (const lim of [null, "-1", "abc", ""]) expect(audit(limited("input", lim, "x".repeat(9000))).missedCount).toBe(0);
  });
  test("a textarea is checked the same way", () => {
    const over = audit(limited("textarea", "10", "x".repeat(11)), { kind: "textarea" });
    expect(over.missed[0]).toMatchObject({ reason: "too_long", kind: "textarea" });
    expect(audit(limited("textarea", "10", "x".repeat(10)), { kind: "textarea" }).missedCount).toBe(0);
  });
  test("a contenteditable has no maxlength concept and is never reported", () => {
    const el = document.createElement("div");
    el.setAttribute("maxlength", "3");
    el.textContent = "a much longer answer";
    expect(audit(el, {}, "contenteditable").missedCount).toBe(0);
  });
  test("a limit that changes after render is read at audit time", () => {
    const el = limited("input", "100", "abcdef");
    const ref = mockRef({ label: "About you" }, el);
    expect(auditForm([ref]).missedCount).toBe(0);
    el.setAttribute("maxlength", "3");
    expect(auditForm([ref]).missed[0]!.reason).toBe("too_long");
    el.setAttribute("maxlength", "6");
    expect(auditForm([ref]).missedCount).toBe(0);
  });
  test("a control that exposes maxLength is read from the property", () => {
    const el = limited("input", null, "abcdef");
    Object.defineProperty(el, "maxLength", { value: 3, configurable: true });
    expect(audit(el).missed[0]!.reason).toBe("too_long");
    Object.defineProperty(el, "maxLength", { value: -1, configurable: true });
    expect(audit(el).missedCount).toBe(0);
  });
  test("surrogate pairs and combining marks are counted as the browser counts them", () => {
    const emoji = "\u{1F600}";
    expect(audit(limited("input", "3", `a${emoji}`)).missedCount).toBe(0);
    expect(audit(limited("input", "3", `ab${emoji}`)).missed[0]!.description).toContain("holds 4 characters but allows 3");
    expect(audit(limited("input", "1", "é")).missed[0]!.reason).toBe("too_long");
    expect(audit(limited("input", "1", "é")).missedCount).toBe(0);
  });
  test("types maxlength does not apply to are not reported", () => {
    const num = limited("input", "2", "12345");
    num.type = "number";
    expect(audit(num).missedCount).toBe(0);
    const email = limited("input", "4", "a@example.com");
    email.type = "email";
    expect(audit(email).missed[0]!.reason).toBe("too_long");
  });
  test("every field is reported alongside the audit's other findings, in the same format", () => {
    const refs = [
      mockRef({ label: "Full Name", required: true }, limited("input", null, "")),
      mockRef({ label: "Bio" }, limited("textarea", "5", "abcdefghij")),
      mockRef({ label: "Email" }, (() => { const e = limited("input", null, "x"); e.setAttribute("aria-invalid", "true"); return e; })()),
      mockRef({ label: "Pitch" }, limited("textarea", "2", "abc")),
    ];
    const report = auditForm(refs);
    expect(report.totalFields).toBe(4);
    expect(report.missed.map(m => [m.refIndex, m.reason])).toEqual([[0, "empty_required"], [1, "too_long"], [2, "invalid"], [3, "too_long"]]);
    expect(report.missed.map(m => m.id)).toEqual(["missed-0", "missed-1", "missed-2", "missed-3"]);
  });
  test("a field that is both over its limit and flagged invalid is reported once, as too long", () => {
    const el = limited("input", "3", "abcdef");
    el.setAttribute("aria-invalid", "true");
    const report = audit(el);
    expect(report.missedCount).toBe(1);
    expect(report.missed[0]!.reason).toBe("too_long");
  });
  test("each control of a split field is checked", () => {
    const area = limited("input", "3", "555");
    const number = limited("input", "4", "0100123");
    const ref = mockRef({ label: "Phone" }, area);
    ref.els = [area, number];
    ref.widget = "split-phone";
    const report = auditForm([ref]);
    expect(report.missed).toHaveLength(1);
    expect(report.missed[0]!.reason).toBe("too_long");
    expect(report.missed[0]!.description).toContain("holds 7 characters but allows 4");
  });
});
