// The repeat-apply notice: parsed defensively off the host's jobFind reply, drawn as a passive note,
// and never in the way of Fill. No DOM and no chrome: the elements are fakes.
import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { type NoticeEl, createRepeatNotice, parseFindRepeatApply, parseRepeatApply } from "../src/repeatapply";
import { parseFindRow } from "../src/jobrow";

const NOTE = {
  count: 2,
  window_days: 60,
  latest_key: "ab:acme:2",
  latest_title: "Staff Data Engineer",
  latest_at: "2026-10-13T12:00:00Z",
  text: "You applied to 2 other roles at Acme in the last 60 days (most recent: Staff Data Engineer, 2026-10-13). Some application systems silently reject a repeat application to one company.",
};

const GOLDEN = JSON.parse(readFileSync(join(import.meta.dir, "../../internal/jobview/testdata/rows.golden.json"), "utf8")) as { rows: Array<Record<string, unknown>> };

class El implements NoticeEl<El> {
  textContent: string | null = "";
  title = "";
  style = { display: "", cssText: "" };
  children: El[] = [];
  private clicks: Array<() => void> = [];
  append(...c: El[]): void { this.children.push(...c); }
  replaceChildren(...c: El[]): void { this.children = [...c]; }
  addEventListener(_t: "click", fn: () => void): void { this.clicks.push(fn); }
  click(): void { for (const f of this.clicks) f(); }
  text(): string { return [this.textContent ?? "", ...this.children.map(c => c.text())].join(""); }
  find(pred: (e: El) => boolean): El | undefined {
    if (pred(this)) return this;
    for (const c of this.children) { const f = c.find(pred); if (f) return f; }
    return undefined;
  }
}
const doc = { createElement: (_tag: string) => new El() };

describe("parsing the note", () => {
  test("a well-formed note is read", () => {
    expect(parseRepeatApply(NOTE)).toEqual({ count: 2, windowDays: 60, latestTitle: "Staff Data Engineer", latestAt: "2026-10-13T12:00:00Z", text: NOTE.text });
  });

  test("a malformed note shows nothing", () => {
    for (const bad of [null, undefined, [], "x", 3, {}, { ...NOTE, text: "" }, { ...NOTE, text: 5 }, { ...NOTE, count: 0 }, { ...NOTE, count: -1 }, { ...NOTE, count: 1.5 },
      { ...NOTE, count: "2" }, { ...NOTE, window_days: 0 }, { ...NOTE, window_days: 99999 }, { ...NOTE, count: NaN }, { ...NOTE, text: "   \n\u0000 " }]) {
      expect(parseRepeatApply(bad)).toBeNull();
    }
  });

  test("text off the wire is cleaned and bounded", () => {
    const n = parseRepeatApply({ ...NOTE, text: "a\u001b[31m\nb" + "z".repeat(2000), latest_title: "t".repeat(900) });
    expect(n).not.toBeNull();
    expect(n!.text).not.toMatch(/[\u0000-\u001f]/);
    expect(n!.text.length).toBeLessThanOrEqual(600);
    expect(n!.latestTitle.length).toBeLessThanOrEqual(200);
  });

  test("the reply's note is read from job.repeatApply, and only for a found job", () => {
    expect(parseFindRepeatApply({ found: true, job: { key: "k", repeatApply: NOTE } })?.count).toBe(2);
    expect(parseFindRepeatApply({ found: false, job: { key: "k", repeatApply: NOTE } })).toBeNull();
    expect(parseFindRepeatApply({ found: true })).toBeNull();
    expect(parseFindRepeatApply(undefined)).toBeNull();
    // An older vani (no field) and the old reply shape give no notice.
    expect(parseFindRepeatApply({ found: true, job: { key: "k", row: GOLDEN.rows[0] } })).toBeNull();
  });
});

describe("old and new replies", () => {
  test("a reply without the note parses exactly as before", () => {
    const row = GOLDEN.rows[0]!;
    expect(parseFindRow({ found: true, job: { key: "k", row } })).toEqual({ row });
    expect(parseFindRow({ found: true, job: { key: "k" } })).toEqual({ row: null });
    expect("repeatApply" in (parseFindRow({ found: true, job: { key: "k", row } }) as object)).toBe(false);
  });

  test("the note rides beside the row, and beside no row", () => {
    const row = GOLDEN.rows[0]!;
    const withRow = parseFindRow({ found: true, job: { key: "k", row, repeatApply: NOTE } });
    expect("row" in withRow && withRow.row).toEqual(row);
    expect("repeatApply" in withRow && withRow.repeatApply?.count).toBe(2);
    const noRow = parseFindRow({ found: true, job: { key: "k", repeatApply: NOTE } });
    expect("row" in noRow && noRow.row).toBeNull();
    expect("repeatApply" in noRow && noRow.repeatApply?.windowDays).toBe(60);
  });

  test("a malformed note leaves the row as it was", () => {
    const row = GOLDEN.rows[0]!;
    expect(parseFindRow({ found: true, job: { key: "k", row, repeatApply: { count: "many" } } })).toEqual({ row });
  });
});

describe("the notice", () => {
  const note = parseRepeatApply(NOTE)!;

  test("renders the sentence as text, never as markup", () => {
    const drawer = new El();
    const n = createRepeatNotice(doc, drawer);
    const hostile = { ...note, text: "<img src=x onerror=alert(1)> applied" };
    n.show(hostile, "https://x.example/a");
    expect(drawer.style.display).toBe("block");
    expect(drawer.text()).toBe("<img src=x onerror=alert(1)> applied" + "x");
    expect(drawer.children.length).toBe(1);
  });

  test("no note hides the drawer, and clears what was shown", () => {
    const drawer = new El();
    const n = createRepeatNotice(doc, drawer);
    n.show(note, "https://x.example/a");
    n.show(undefined, "https://x.example/a");
    expect(drawer.style.display).toBe("none");
    expect(drawer.children.length).toBe(0);
  });

  test("dismissing hides it for that page across rescans, and not for another page", () => {
    const drawer = new El();
    const n = createRepeatNotice(doc, drawer);
    n.show(note, "https://x.example/a");
    drawer.find(e => e.title === "Dismiss")!.click();
    expect(drawer.style.display).toBe("none");
    n.show(note, "https://x.example/a"); // a page-activity rescan
    expect(drawer.style.display).toBe("none");
    n.show(note, "https://x.example/b");
    expect(drawer.style.display).toBe("block");
  });

  test("showing it more than once does not stack copies", () => {
    const drawer = new El();
    const n = createRepeatNotice(doc, drawer);
    for (let i = 0; i < 5; i++) n.show(note, "https://x.example/a");
    expect(drawer.children.length).toBe(1);
  });
});

// The notice is a passive drawer in the panel. It must stay out of the way of filling a form: fill()
// never reads it, awaits it or branches on it, and the one place it is drawn is the scan's panel update.
describe("it never blocks Fill", () => {
  const content = readFileSync(join(import.meta.dir, "../src/content.ts"), "utf8");
  function between(start: string, end: string): string {
    const a = content.indexOf(start);
    const b = content.indexOf(end, a + start.length);
    if (a < 0 || b < 0) throw new Error(`missing slice ${start} .. ${end}`);
    return content.slice(a, b);
  }

  test("fill() does not touch the notice or the cached note", () => {
    const fill = between("async function fill(", "// --- floating panel");
    for (const word of ["repeatNotice", "repeatApply", "vaniRepeatApply", "repeat-drawer"]) expect(fill).not.toContain(word);
  });

  test("the Fill button is wired straight to fill(), with no check of the notice in front of it", () => {
    const line = content.split("\n").find(l => l.includes('panel.querySelector(".f")'))!;
    expect(line).toContain("void fill()");
    expect(line).not.toContain("repeat");
  });

  test("the notice is drawn from the scan reply only, and the scan does not wait on it", () => {
    const shows = content.split("\n").filter(l => l.includes("repeatNotice?.show("));
    expect(shows.length).toBe(2);
    const scanFn = between("async function scan(", "/** Watches the page");
    // The note is read from the answer the relevance lookup already fetched; no extra await, no throw.
    expect(scanFn).toContain("vaniRepeatApply()");
    expect(scanFn).not.toMatch(/await[^\n]*repeat/i);
  });
});
