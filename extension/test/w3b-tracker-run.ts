// Helper for w3b-tracker.test.ts (not collected: no .test.). Run as its own `bun` process because it replaces
// ../src/extract with a fake form scan, which must not leak into other test files. Prints one JSON line.
// A "page" here is a fake document whose fields are plain objects; `fire` dispatches a real Event whose
// composedPath names the target, so isTrusted is false exactly as for a page script's dispatchEvent.
import { mock } from "bun:test";
import type { Bank, FieldInfo } from "../src/types";
import { emptyBank } from "../src/types";

const info = (label: string, value: string[] = ["blue"]): FieldInfo => ({ kind: "text", label, name: "", autocomplete: "", automationId: "", options: [], required: false, value });
type Fake = Record<string, unknown>;
let fields: Array<{ info: FieldInfo; els: Fake[]; widget: string; manual: string }> = [];
let strictSeen: boolean[] = [];
const real = await import("../src/extract");
mock.module("../src/extract", () => ({ ...real, readRefValue: (r: { info: FieldInfo }) => r.info.value, scanForm: (_r: unknown, o?: { strict?: boolean }) => { strictSeen.push(o?.strict === true); return { roots: [], refs: fields }; } }));
(globalThis as Record<string, unknown>).MutationObserver = class { observe() {} disconnect() {} };
const { createTracker } = await import("../src/tracker");

function target() {
  const l = new Map<string, Array<(e: Event) => void>>();
  return {
    addEventListener(t: string, f: (e: Event) => void) { l.set(t, [...(l.get(t) ?? []), f]); },
    removeEventListener() {},
    fire(t: string, path: Fake[] = []) { const e = Object.assign(new Event(t), { composedPath: () => path }); for (const f of l.get(t) ?? []) f(e); return e; },
  };
}
const sleep = (ms: number) => new Promise(r => setTimeout(r, ms));
const el = (name: string): Fake => ({ nodeType: 1, name, parentElement: null, getRootNode: () => ({}) });
const NAMES = ["Favourite colour", "Favourite editor", "Favourite shell"];
const page = () => { const els = NAMES.map(n => el(n)); fields = NAMES.map((n, i) => ({ info: info(n), els: [els[i]!], widget: "native", manual: "" })); return els; };

interface Run { sends: number; sent: string[]; filled: boolean[]; values: string[]; submits: number; doc: ReturnType<typeof target>; win: ReturnType<typeof target>; t: ReturnType<typeof createTracker> }
let vt = 0;
function make(opts: { trusted?: boolean; bank?: Bank } = {}): Run {
  const win = Object.assign(target(), { location: { href: "https://jobs.example/apply" } });
  const doc = Object.assign(target(), { nodeType: 9, defaultView: win, visibilityState: "visible" });
  const run = { sends: 0, sent: [] as string[], filled: [] as boolean[], values: [] as string[], submits: 0 } as Run;
  run.doc = doc; run.win = win;
  run.t = createTracker({
    doc: doc as unknown as Document, host: "jobs.example", getBank: async () => opts.bank ?? emptyBank(), commitMs: 10,
    ...(opts.trusted ? { isTrusted: () => true } : {}),
    clock: { now: () => vt, setTimer: (fn, ms) => setTimeout(fn, ms), clearTimer: h => clearTimeout(h as ReturnType<typeof setTimeout>) },
    send: async f => { run.sends++; run.sent.push(...f.map(x => x.label)); run.values.push(...f.map(x => x.value.join(","))); run.filled.push(...f.map(x => x.filledBy !== undefined)); return { added: f.length, updated: 0, skippedSecret: 0, skippedSelfId: 0, skippedEmpty: 0 }; },
    onSubmit: () => { run.submits++; },
  });
  return run;
}
const button = (text: string): Fake => ({ nodeType: 1, localName: "button", textContent: text, getAttribute: () => null, closest: () => null });
button("x");
const clickable = (text: string): Fake => { const b: Fake = { nodeType: 1, localName: "button", textContent: text, getAttribute: () => null }; b.closest = () => b; return b; };

const out: Record<string, number> = {};
// H1: a hostile page script sets values and dispatches synthetic input, change, focusout, keydown(Enter), click and submit.
{
  const els = page(); const r = make();
  for (const e of els) for (const t of ["input", "change", "focusout", "keydown", "pointerdown"]) r.doc.fire(t, [e]);
  const b = clickable("Submit application");
  r.doc.fire("click", [b]); r.doc.fire("submit", [els[0]!]); r.win.fire("pagehide");
  await sleep(80);
  r.t.snapshot(); await r.t.flush("hostile");
  out.hostileSends = r.sends; out.hostilePending = r.t.pendingCount(); out.hostileSubmitPrompts = r.submits;
  r.t.dispose();
}
// H2: the same page, real (trusted) typing in two of the three fields: only those two are captured.
{
  const els = page(); const r = make({ trusted: true });
  r.doc.fire("input", [els[0]!]); r.doc.fire("change", [els[1]!]);
  await sleep(60);
  await r.t.flush("typed");
  out.typedSent = r.sent.length; out.typedNames = r.sent.sort().join("|") === "Favourite colour|Favourite editor" ? 1 : 0;
  r.t.dispose();
}
// H3: an explicit Save captures untouched, script-set values (the owner pressed Save), and asks the scan for strict visibility.
{
  page(); const r = make(); strictSeen = [];
  const res = await r.t.flush("saved", true);
  out.explicitSent = r.sent.length; out.explicitAdded = res?.added ?? -1; out.explicitStrict = strictSeen.length > 0 && strictSeen.every(s => s) ? 1 : 0;
  r.t.dispose();
}
// H4: a trusted click on a submit button prompts Applied and flushes; an untrusted one does not.
{
  const els = page(); const trusted = make({ trusted: true });
  trusted.doc.fire("input", [els[0]!]);
  trusted.doc.fire("click", [clickable("Submit application")]);
  await sleep(60);
  out.trustedSubmitPrompts = trusted.submits; out.trustedSubmitSends = trusted.sends;
  trusted.t.dispose();
}
// H5: a custom combobox. The field is the wrapper; the user clicks an option in the popup (a listbox elsewhere in the page,
// owned through aria-controls on the inner input), and the widget then sets the value with script events. Trusted: captured.
// The same sequence from a page script: not.
function comboPage() {
  const wrapper: Fake = { nodeType: 1, name: "wrapper", parentElement: null, getRootNode: () => ({}) };
  const input: Fake = { nodeType: 1, name: "input", parentElement: wrapper, getAttribute: (a: string) => (a === "aria-controls" ? "lb1" : a === "role" ? "combobox" : null) };
  const listbox: Fake = { nodeType: 1, name: "listbox", id: "lb1", parentElement: null, getRootNode: () => ({ querySelectorAll: () => [input] }) };
  const option: Fake = { nodeType: 1, name: "option", parentElement: listbox, closest: (s: string) => (s === "[role=listbox]" ? listbox : null) };
  const others = ["Favourite editor", "Favourite shell"].map(n => el(n));
  fields = [
    { info: info("Favourite pet"), els: [wrapper], widget: "combobox", manual: "" },
    ...others.map((e, i) => ({ info: info(["Favourite editor", "Favourite shell"][i]!), els: [e], widget: "native", manual: "" })),
  ];
  return { option, wrapper };
}
{
  const { option } = comboPage(); const r = make({ trusted: true });
  r.doc.fire("pointerdown", [option]); r.doc.fire("click", [option]);
  await sleep(60); await r.t.flush("combo");
  out.comboTrusted = r.sent.length === 1 && r.sent[0] === "Favourite pet" ? 1 : 0;
  r.t.dispose();
}
{
  const { option } = comboPage(); const r = make();
  r.doc.fire("pointerdown", [option]); r.doc.fire("click", [option]); r.doc.fire("change", [option]);
  await sleep(60); await r.t.flush("combo");
  out.comboHostile = r.sent.length;
  r.t.dispose();
}
// H6: a click on a label marks the control it names (a custom checkbox whose input the page hides).
{
  const input = el("Favourite colour"); const lab: Fake = { nodeType: 1, control: input, parentElement: null };
  input.parentElement = null; input.getRootNode = () => ({});
  const label: Fake = { ...lab, closest: (s: string) => (s === "label" ? lab : null) };
  page(); fields[0]!.els = [input]; const r = make({ trusted: true });
  r.doc.fire("click", [label]); await sleep(60); await r.t.flush("label");
  out.labelControl = r.sent.length === 1 && r.sent[0] === "Favourite colour" ? 1 : 0;
  r.t.dispose();
}
// H7: which comboboxes a click in a listbox may mark. `claim` builds one page-made listbox and the elements that name it.
type Claim = { role?: string; haspopup?: string; inside?: boolean };
function claimPage(claims: Claim[]) {
  const listbox: Fake = { nodeType: 1, name: "listbox", id: "lbx", parentElement: null, getRootNode: () => ({ querySelectorAll: () => owners }) };
  const owners: Fake[] = claims.map((c, i) => ({
    nodeType: 1, name: `owner${i}`, parentElement: c.inside ? listbox : null, getRootNode: () => ({}),
    getAttribute: (a: string) => (a === "aria-controls" ? "lbx" : a === "role" ? (c.role ?? null) : a === "aria-haspopup" ? (c.haspopup ?? null) : null),
  }));
  const option: Fake = { nodeType: 1, name: "option", parentElement: listbox, closest: (s: string) => (s === "[role=listbox]" ? listbox : null), getRootNode: () => ({}) };
  fields = [...owners.map((o, i) => ({ info: info(`Owner ${i}`), els: [o], widget: "combobox", manual: "" })),
    ...["Favourite editor", "Favourite shell"].map(n => ({ info: info(n), els: [el(n)], widget: "native", manual: "" }))];
  return option;
}
async function claimed(claims: Claim[]): Promise<string[]> {
  const option = claimPage(claims); const r = make({ trusted: true });
  for (const t of ["pointerdown", "mousedown", "click"]) r.doc.fire(t, [option]);
  await sleep(40); await r.t.flush("claim");
  r.t.dispose();
  return r.sent.sort();
}
out.ownerPlain = (await claimed([{}, {}, {}])).length;                                       // no role: a page-made claim
out.ownerAbove = (await claimed([{ role: "combobox", inside: true }])).length;                // the listbox contains the owner
out.ownerTwo = (await claimed([{ role: "combobox" }, { haspopup: "listbox" }])).length;       // two claim the popup: none
out.ownerOneRole = (await claimed([{ role: "combobox" }, {}, {}])).join("|") === "Owner 0" ? 1 : 0;
out.ownerOnePopup = (await claimed([{ haspopup: "listbox" }])).join("|") === "Owner 0" ? 1 : 0;
out.ownerWrongPopup = (await claimed([{ haspopup: "menu" }])).length;                         // aria-haspopup=menu is not a listbox

// H8: nested fields. A real click inside the innermost marks it and none of the fields around it.
{
  const mk = (name: string, parent: Fake | null): Fake => ({ nodeType: 1, name, parentElement: parent, getRootNode: () => ({}) });
  const a = mk("A", null), b = mk("B", a), c = mk("C", b);
  fields = [["Field A", a], ["Field B", b], ["Field C", c], ["Field D", mk("D", null)]].map(([n, e]) => ({ info: info(n as string), els: [e as Fake], widget: "combobox", manual: "" }));
  const r = make({ trusted: true });
  r.doc.fire("pointerdown", [c, b, a]); // composedPath: the target, then every ancestor
  await sleep(40); await r.t.flush("nested");
  out.nestedOnlyInner = r.sent.join("|") === "Field C" ? 1 : 0;
  r.t.dispose();
}

// H9: what a user's flush may read. The tracker's clock is `vt`; the field's DOM value is fields[0].info.value
// (readRefValue is faked to read it). The user types "typed" at t=0; with no event, a script (or a slow widget) sets
// "rewritten" at t=rewriteAt; something flushes at t=at. A plain field keeps what the user's own event absorbed
// whatever the clock says; a combobox the user operated is read again at the user's flush, however late.
type Kind = "plain" | "combo";
async function windowRun(rewriteAt: number, at: number, how: "user" | "page" | "blur-then-user", kind: Kind = "plain"): Promise<string> {
  const els = page(); const r = make({ trusted: true });
  if (kind === "combo") { els[0]!.matches = (s: string) => s.includes("role=combobox"); fields[0]!.widget = "combobox"; }
  else { els[0]!.localName = "input"; els[0]!.type = "text"; }
  vt = 0; fields[0]!.info.value = ["typed"];
  r.doc.fire("input", [els[0]!]);
  vt = rewriteAt; fields[0]!.info.value = ["rewritten"];
  vt = at;
  if (how === "blur-then-user") { r.doc.fire("focusout", [els[0]!]); r.doc.fire("change", [els[0]!]); }
  if (how === "page") await r.t.flush("route change"); else r.doc.fire("click", [clickable("Next")]);
  await sleep(40);
  r.t.dispose(); vt = 0;
  return r.values.filter((_, i) => r.sent[i] === "Favourite colour").join("|");
}
out.plainInside = (await windowRun(300, 600, "user")) === "typed" ? 1 : 0;                 // a plain field is not re-read at Next, even inside the old window
out.plainOutside = (await windowRun(1500, 2500, "user")) === "typed" ? 1 : 0;
out.plainBlur = (await windowRun(300, 600, "blur-then-user")) === "typed" ? 1 : 0;         // the blur's focusout and change do not re-read it
out.plainPage = (await windowRun(300, 600, "page")) === "typed" ? 1 : 0;
out.comboLate = (await windowRun(600, 2600, "user", "combo")) === "rewritten" ? 1 : 0;      // a late-setting combobox the user operated is read at Next
out.comboPage = (await windowRun(600, 2600, "page", "combo")) === "typed" ? 1 : 0;          // a flush the page can cause still reads nothing
// The shared-listbox case: two comboboxes name one listbox, so a click on its option marks neither, but the focusout on
// the combobox the user left does. The widget then sets that combobox's value late.
{
  const lb: Fake = { nodeType: 1, name: "listbox", id: "lb", parentElement: null, getRootNode: () => ({}) };
  const mk = (name: string): Fake => ({ nodeType: 1, name, parentElement: null, getRootNode: () => ({}), matches: () => true, getAttribute: (a: string) => (a === "aria-controls" ? "lb" : a === "role" ? "combobox" : null) });
  const c1 = mk("c1"), c2 = mk("c2");
  lb.getRootNode = () => ({ querySelectorAll: () => [c1, c2] });
  const option: Fake = { nodeType: 1, name: "option", parentElement: lb, closest: (s: string) => (s === "[role=listbox]" ? lb : null), getRootNode: () => ({}) };
  fields = [{ info: info("City", [""]), els: [c1], widget: "combobox", manual: "" }, { info: info("Region", [""]), els: [c2], widget: "combobox", manual: "" },
    ...["Favourite editor", "Favourite shell"].map(n => ({ info: info(n), els: [el(n)], widget: "native", manual: "" }))];
  const r = make({ trusted: true });
  vt = 0;
  r.doc.fire("pointerdown", [c1]); r.doc.fire("focusout", [c1]);          // the user picks the city, leaving the combobox for the option
  r.doc.fire("pointerdown", [option]); r.doc.fire("click", [option]);     // two claimants: nothing marked by the option itself
  vt = 600; fields[0]!.info.value = ["London"];                            // the widget sets it late
  vt = 2600; r.doc.fire("click", [clickable("Next")]);
  await sleep(40); r.t.dispose(); vt = 0;
  out.sharedLate = r.values.join("|") === "London" ? 1 : 0;                // only the combobox the user operated, not the Region one
}
// A user's edit over a field Vani filled: the scan that built the index saw the fill (filledBy), the value read at the
// user's event is no longer it, so it is saved as the user's answer and not dropped as the fill coming back.
{
  const els = page(); const r = make({ trusted: true });
  fields[0]!.info.filledBy = { key: "favourite colour", review: false };
  r.doc.fire("input", [els[0]!]);
  r.doc.fire("click", [clickable("Next")]);
  await sleep(40); r.t.dispose();
  const i = r.sent.indexOf("Favourite colour");
  out.editOverFill = i >= 0 && r.filled[i] === false ? 1 : 0;
}
console.log(JSON.stringify(out));
