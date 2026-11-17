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
  position: string;
  localName: string;
  type: string;
  scrollLeft: number;
  scrollTop: number;
  ownerDocument: unknown;
}

const view = { scrollX: 0, scrollY: 0, getComputedStyle: (p: Fake) => ({ overflowX: p.overflow, overflowY: p.overflow, position: p.position }) };
const body = {};
const doc = { defaultView: view, body, documentElement: { scrollWidth: 1200, clientWidth: 1200, scrollHeight: 3000, clientHeight: 800 } };

function el(box: Box, extra: { visible?: boolean; parent?: Fake; overflow?: string; labels?: Fake[]; noCheck?: boolean; position?: string; type?: string; scroll?: [number, number] } = {}): Fake {
  const f: Fake = {
    getBoundingClientRect: () => ({ left: box.left, top: box.top, right: box.left + box.width, bottom: box.top + box.height, width: box.width, height: box.height }),
    parentElement: extra.parent ?? null,
    overflow: extra.overflow ?? "visible",
    position: extra.position ?? "static",
    localName: "input",
    type: extra.type ?? "text",
    scrollLeft: extra.scroll?.[0] ?? 0,
    scrollTop: extra.scroll?.[1] ?? 0,
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
  test("a hidden native checkbox or radio counts through its visible label, and not through a hidden one", () => {
    const label = el(shown);
    for (const type of ["checkbox", "radio"]) {
      expect(ok(el({ ...shown, width: 0 }, { labels: [label], type }))).toBe(true);
      expect(ok(el({ ...shown, width: 0 }, { labels: [el(shown, { visible: false })], type }))).toBe(false);
    }
  });
  test("a hidden text input does not count because a page gave it a visible label", () => {
    const label = el(shown);
    for (const type of ["text", "email", "tel", "hidden"]) expect(ok(el({ ...shown, width: 0 }, { labels: [label], type }))).toBe(false);
  });
  test("a position:fixed control, or one in a fixed wrapper, is not clipped by a zero-size overflow:hidden ancestor", () => {
    const zero = el({ left: 0, top: 0, width: 0, height: 0 }, { overflow: "hidden" });
    expect(ok(el(shown, { parent: zero, position: "fixed" }))).toBe(true);
    const fixedWrapper = el(shown, { parent: zero, position: "fixed" });
    expect(ok(el(shown, { parent: fixedWrapper }))).toBe(true);
    expect(ok(el(shown, { parent: zero }))).toBe(false);
  });
  test("an absolute control is clipped by its containing block, not by an ancestor between it and that block", () => {
    const zero = (position: string, parent?: Fake) => el({ left: 0, top: 0, width: 0, height: 0 }, { overflow: "hidden", position, ...(parent ? { parent } : {}) });
    const block = el({ left: 0, top: 100, width: 500, height: 200 }, { position: "relative" });
    expect(ok(el(shown, { parent: block, position: "absolute" }))).toBe(true);
    const inner = zero("static", block);                   // between the control and its containing block: no clip for an absolute
    expect(ok(el(shown, { parent: inner, position: "absolute" }))).toBe(true);
    expect(ok(el(shown, { parent: inner }))).toBe(false);  // while a static control in it is clipped
    const clippingBlock = zero("relative");                // the containing block itself clips its absolute children
    expect(ok(el(shown, { parent: clippingBlock, position: "absolute" }))).toBe(false);
  });
  test("body and the root never clip: body is 0 px tall when its children are absolutely positioned", () => {
    const zeroBody = el({ left: 0, top: 0, width: 800, height: 0 }, { overflow: "hidden" });
    (doc as { body: unknown }).body = zeroBody;
    try {
      expect(ok(el(shown, { parent: zeroBody, position: "absolute" }))).toBe(true);
      expect(ok(el(shown, { parent: zeroBody }))).toBe(true);
    } finally {
      (doc as { body: unknown }).body = body;
    }
  });
  test("a control below the fold of an inner scroller is on the page; one parked before its content is not", () => {
    const scroller = el({ left: 0, top: 0, width: 600, height: 400 }, { overflow: "auto" });
    expect(ok(el({ ...shown, top: 90000 }, { parent: scroller }))).toBe(true);
    expect(ok(el({ ...shown, left: 90000 }, { parent: scroller }))).toBe(true);
    expect(ok(el({ ...shown, top: -5000 }, { parent: scroller }))).toBe(false);
    expect(ok(el({ ...shown, left: -9999 }, { parent: scroller }))).toBe(false);
    const scrolled = el({ left: 0, top: 0, width: 600, height: 400 }, { overflow: "auto", scroll: [0, 2100] });
    expect(ok(el({ ...shown, top: -1800 }, { parent: scrolled })), "scrolled past: still inside the content").toBe(true);
    const hidden = el({ left: 0, top: 0, width: 600, height: 400 }, { overflow: "hidden" });
    expect(ok(el({ ...shown, top: 90000 }, { parent: hidden })), "overflow:hidden is not a scroller").toBe(false);
  });
});
