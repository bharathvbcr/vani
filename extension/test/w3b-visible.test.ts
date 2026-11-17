// Slice w3b (ft-bf5e): capture reads only controls a person could see. Fake elements stand in for the layout
// (bun has no DOM); each case is one way a page hides a control it wants to set a value in.
import { describe, expect, test } from "bun:test";
import { visibleForCapture } from "../src/extract";

type Box = { left: number; top: number; width: number; height: number };
interface Fake {
  getBoundingClientRect(): { left: number; top: number; right: number; bottom: number; width: number; height: number };
  checkVisibility?: (o: { checkOpacity: boolean; checkVisibilityCSS: boolean }) => boolean;
  parentElement: Fake | null;
  labels?: Fake[];
  overflow: string;
  ownerDocument: unknown;
}

const view = { scrollX: 0, scrollY: 0, getComputedStyle: (p: Fake) => ({ overflowX: p.overflow, overflowY: p.overflow }) };
const doc = { defaultView: view, documentElement: { scrollWidth: 1200, clientWidth: 1200, scrollHeight: 3000, clientHeight: 800 } };

function el(box: Box, extra: { visible?: boolean; parent?: Fake; overflow?: string; labels?: Fake[]; noCheck?: boolean } = {}): Fake {
  const f: Fake = {
    getBoundingClientRect: () => ({ left: box.left, top: box.top, right: box.left + box.width, bottom: box.top + box.height, width: box.width, height: box.height }),
    parentElement: extra.parent ?? null,
    overflow: extra.overflow ?? "visible",
    ownerDocument: doc,
    ...(extra.labels ? { labels: extra.labels } : {}),
  };
  if (!extra.noCheck) f.checkVisibility = () => extra.visible !== false;
  return f;
}
const ok = (f: Fake): boolean => visibleForCapture(f as unknown as HTMLElement);
const shown: Box = { left: 40, top: 120, width: 300, height: 32 };

describe("w3b: visibleForCapture", () => {
  test("a normal control counts, including one far down a long page", () => {
    expect(ok(el(shown))).toBe(true);
    expect(ok(el({ ...shown, top: 2500 }))).toBe(true);
  });
  test("a control checkVisibility rejects (opacity 0, visibility hidden, content-visibility) stores nothing", () => {
    expect(ok(el(shown, { visible: false }))).toBe(false);
  });
  test("a zero-size or sub-pixel control stores nothing", () => {
    expect(ok(el({ ...shown, width: 0 }))).toBe(false);
    expect(ok(el({ ...shown, height: 0 }))).toBe(false);
    expect(ok(el({ ...shown, width: 0.4, height: 0.4 }))).toBe(false);
    expect(ok(el({ ...shown, width: 1, height: 1 }))).toBe(true);
  });
  test("an off-screen control (left:-9999px, above the page, past its scrollable area) stores nothing", () => {
    expect(ok(el({ ...shown, left: -9999 }))).toBe(false);
    expect(ok(el({ ...shown, top: -5000 }))).toBe(false);
    expect(ok(el({ ...shown, left: 5000 }))).toBe(false);
    expect(ok(el({ ...shown, top: 90000 }))).toBe(false);
  });
  test("a control clipped to nothing by an overflow:hidden ancestor stores nothing", () => {
    const zero = el({ left: 0, top: 0, width: 0, height: 0 }, { overflow: "hidden" });
    expect(ok(el(shown, { parent: zero }))).toBe(false);
    const roomy = el({ left: 0, top: 100, width: 500, height: 200 }, { overflow: "hidden" });
    expect(ok(el(shown, { parent: roomy }))).toBe(true);
    const zeroVisibleOverflow = el({ left: 0, top: 0, width: 0, height: 0 }, { overflow: "visible" });
    expect(ok(el(shown, { parent: zeroVisibleOverflow })), "a zero-size ancestor that does not clip hides nothing").toBe(true);
  });
  test("a browser without checkVisibility falls back to the size and position tests", () => {
    expect(ok(el(shown, { noCheck: true }))).toBe(true);
    expect(ok(el({ ...shown, width: 0 }, { noCheck: true }))).toBe(false);
  });
  test("a hidden native input counts through its visible label, and not through a hidden one", () => {
    const label = el(shown);
    expect(ok(el({ ...shown, width: 0 }, { labels: [label] }))).toBe(true);
    expect(ok(el({ ...shown, width: 0 }, { labels: [el(shown, { visible: false })] }))).toBe(false);
  });
});
