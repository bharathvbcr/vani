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
});
