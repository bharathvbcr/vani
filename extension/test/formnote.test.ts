// The application-form note: parsed defensively off the host's jobFind reply, joined into the one
// notice drawer beside the repeat-apply sentence, and never in the way of Fill.
import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { noticeOf, parseFindFormNote } from "../src/formnote";
import { parseFindRow } from "../src/jobrow";
import { type NoticeEl, createRepeatNotice } from "../src/repeatapply";

const NOTE = "Application form: 4 of 6 questions required; sponsorship: only a yes/no on being authorized without it.";
const GOLDEN = JSON.parse(readFileSync(join(import.meta.dir, "../../internal/jobview/testdata/rows.golden.json"), "utf8")) as { rows: Array<Record<string, unknown>> };

class El implements NoticeEl<El> {
  textContent: string | null = "";
  title = "";
  style = { display: "", cssText: "" };
  children: El[] = [];
  append(...c: El[]): void { this.children.push(...c); }
  replaceChildren(...c: El[]): void { this.children = [...c]; }
  addEventListener(_t: "click", _fn: () => void): void {}
  text(): string { return [this.textContent ?? "", ...this.children.map(c => c.text())].join(""); }
}
const doc = { createElement: (_tag: string) => new El() };

describe("parsing the note", () => {
  test("a well-formed note is read, only for a found job", () => {
    expect(parseFindFormNote({ found: true, job: { key: "k", applyFormNote: NOTE } })).toBe(NOTE);
    expect(parseFindFormNote({ found: false, job: { key: "k", applyFormNote: NOTE } })).toBeNull();
    expect(parseFindFormNote({ found: true })).toBeNull();
    expect(parseFindFormNote(undefined)).toBeNull();
    // An older vani (no field) gives no note.
    expect(parseFindFormNote({ found: true, job: { key: "k", row: GOLDEN.rows[0] } })).toBeNull();
  });

  test("a malformed note shows nothing", () => {
    for (const bad of [null, undefined, 7, [], {}, { text: "x" }, "", "   \n\u0000 "]) {
      expect(parseFindFormNote({ found: true, job: { key: "k", applyFormNote: bad } })).toBeNull();
    }
  });

  test("text off the wire is cleaned and bounded", () => {
    const n = parseFindFormNote({ found: true, job: { key: "k", applyFormNote: "a\u001b[31m\nb" + "z".repeat(2000) } });
    expect(n).not.toBeNull();
    expect(n!).not.toMatch(/[\u0000-\u001f]/);
    expect(n!.length).toBeLessThanOrEqual(600);
  });
});

describe("old and new replies", () => {
  test("a reply without the note parses exactly as before", () => {
    const row = GOLDEN.rows[0]!;
    expect(parseFindRow({ found: true, job: { key: "k", row } })).toEqual({ row });
    expect("formNote" in (parseFindRow({ found: true, job: { key: "k", row } }) as object)).toBe(false);
  });

  test("the note rides beside the row, and beside no row", () => {
    const row = GOLDEN.rows[0]!;
    const withRow = parseFindRow({ found: true, job: { key: "k", row, applyFormNote: NOTE } });
    expect("row" in withRow && withRow.row).toEqual(row);
    expect("formNote" in withRow && withRow.formNote).toBe(NOTE);
    const noRow = parseFindRow({ found: true, job: { key: "k", applyFormNote: NOTE } });
    expect("row" in noRow && noRow.row).toBeNull();
    expect("formNote" in noRow && noRow.formNote).toBe(NOTE);
  });

  test("a malformed note leaves the row as it was", () => {
    const row = GOLDEN.rows[0]!;
    expect(parseFindRow({ found: true, job: { key: "k", row, applyFormNote: { x: 1 } } })).toEqual({ row });
  });
});

describe("the one drawer", () => {
  test("joins the two notes in order, and shows nothing for neither", () => {
    expect(noticeOf(undefined, undefined)).toBeUndefined();
    expect(noticeOf(undefined, "")).toBeUndefined();
    expect(noticeOf({ text: "Applied lately." }, undefined)).toEqual({ text: "Applied lately." });
    expect(noticeOf(undefined, NOTE)).toEqual({ text: NOTE });
    expect(noticeOf({ text: "Applied lately." }, NOTE)).toEqual({ text: `Applied lately. ${NOTE}` });
  });

  test("renders the sentence as text, never as markup", () => {
    const drawer = new El();
    createRepeatNotice(doc, drawer).show(noticeOf(undefined, "<img src=x onerror=alert(1)>"), "https://x.example/a");
    expect(drawer.style.display).toBe("block");
    expect(drawer.text()).toBe("<img src=x onerror=alert(1)>" + "x");
  });
});

// The note is drawn in the scan's panel update only; fill() never reads it.
describe("it never blocks Fill", () => {
  const content = readFileSync(join(import.meta.dir, "../src/content.ts"), "utf8");
  test("nothing but the scan reads the form note", () => {
    const uses = content.split("\n").filter(l => /formNote|vaniFormNote|noticeOf/.test(l)).map(l => l.trim());
    expect(uses.every(l => !/fill|submit|click/i.test(l))).toBe(true);
    expect(uses.length).toBeGreaterThan(0);
  });
});
