// In-browser DOM tests for extract, fill and the capture tracker, run by opening
// test/dom.html. Writes PASS/FAIL lines into #results and sets document.title.
import { MAX_LABEL, OWN_TEXT_SKIP, collectFields, ownText, scanForm } from "../src/extract";
import { clearMarks, fillAll, fillRef, insertDraft } from "../src/fill";
import { currentText, draftable, suggest } from "../src/suggest";
import { readJobInfo } from "../src/jobinfo";
import { mergeCapture } from "../src/bank";
import { coverageLine, coverageOf } from "../src/coverage";
import { createTracker } from "../src/tracker";
import { type Bank, type FieldInfo, emptyBank } from "../src/types";
import { intentOf, motivationEssay } from "../src/intents";
import { BASICS_HOST, basicsFields } from "../src/basics";
import { matchField } from "../src/match";

const lines: string[] = [];
let failed = 0;
function check(name: string, ok: boolean, detail = ""): void {
  if (!ok) failed++;
  lines.push(`${ok ? "PASS" : "FAIL"} ${name}${ok || !detail ? "" : ` :: ${detail}`}`);
}
const sleep = (ms: number) => new Promise<void>(r => setTimeout(r, ms));

const gh = document.getElementById("gh") as HTMLFormElement;
const lv = document.getElementById("lv") as HTMLFormElement;
let submits = 0;
for (const f of [gh, lv]) f.addEventListener("submit", e => { submits++; e.preventDefault(); });
// Anything that submits anywhere, or presses a submit button, is a failure of the "never submits" rule.
let anySubmit = 0, submitClicks = 0;
document.addEventListener("submit", e => { anySubmit++; e.preventDefault(); }, true);
document.addEventListener("click", e => { if ((e.target as Element).closest?.("button[type=submit], input[type=submit]")) submitClicks++; }, true);

const q = <T extends Element>(root: ParentNode, sel: string) => root.querySelector(sel) as T;
const labels = (root: ParentNode) => scanForm(root).refs.map(r => r.info.label);
const NOW = () => new Date();
const learn = (root: ParentNode, host = "x.example"): Bank => mergeCapture(emptyBank(), scanForm(root).refs.map(r => r.info), host, NOW()).bank;

async function core(): Promise<void> {
  // 1. Extraction on the first form.
  const a = collectFields(gh);
  const byLabel = new Map(a.map(r => [r.info.label, r.info]));
  check("first form: field count", a.length === 14, `got ${a.length}: ${a.map(r => r.info.label).join(" | ")}`);
  check("label via <label for>", byLabel.get("First Name *")?.autocomplete === "given-name");
  check("label via question container", byLabel.has("LinkedIn Profile"));
  const spons = byLabel.get("Will you now or in the future require sponsorship?");
  check("radio group via legend", spons?.kind === "radio" && spons.options.join("|") === "Yes|No", JSON.stringify(spons));
  check("single checkbox label", byLabel.get("I agree to the privacy notice")?.kind === "checkbox");
  check("select options", byLabel.get("Are you legally authorized to work in the United States?")?.options.length === 3);

  // 2. The user fills the first application by hand.
  const set = (sel: string, v: string) => { const el = q<HTMLInputElement>(gh, sel); el.value = v; };
  set("#first_name", "Sam"); set("#last_name", "Rivera"); set("#email", "sam@example.invalid"); set("#phone", "555-0100");
  set("input[name='job_application[answers_attributes][0][text_value]']", "https://linkedin.example/in/sam");
  q<HTMLInputElement>(gh, "input[name=q_spons][value='0']").checked = true;
  q<HTMLSelectElement>(gh, "#auth").value = "y";
  set("#yrs", "6");
  set("#why", "Because ".repeat(50));
  set("#pw", "hunter2"); set("#ssn", "000-00-0000");
  q<HTMLInputElement>(gh, "input[name=privacy]").checked = true;
  q<HTMLSelectElement>(gh, "#gender").selectedIndex = 3;

  // 3. Capture on submit.
  const cap = mergeCapture(emptyBank(), collectFields(gh).map(r => r.info), "boards.greenhouse.io", new Date());
  const json = JSON.stringify(cap.bank);
  check("capture stores answers", cap.added >= 8, `added ${cap.added}`);
  check("password and SSN never stored", !json.includes("hunter2") && !json.includes("000-00-0000") && cap.skippedSecret === 2, `skippedSecret ${cap.skippedSecret}`);
  check("self-ID not stored by default", cap.skippedSelfId === 1 && !json.includes("Decline"));
  check("file input never read", !json.includes("resume"));

  // 4. Second application, different site and wording: fill.
  let reactEvents = 0;
  q<HTMLInputElement>(lv, "#react-like").addEventListener("input", () => reactEvents++);
  const outcomes = await fillAll(collectFields(lv), cap.bank, "jobs.lever.co");
  const val = (sel: string) => q<HTMLInputElement>(lv, sel).value;
  check("first name via autocomplete", val("input[name=firstName]") === "Sam", val("input[name=firstName]"));
  check("email via autocomplete", val("input[name=email]") === "sam@example.invalid");
  check("phone via reworded label", val("input[name=phone]") === "555-0100", JSON.stringify(outcomes.find(o => o.label === "Mobile phone")));
  check("LinkedIn URL vs LinkedIn Profile", val("#react-like") === "https://linkedin.example/in/sam");
  check("input events fire (React-style listeners)", reactEvents >= 1, `events ${reactEvents}`);
  check("radio: 'No' mapped to 'No, I will not'", q<HTMLInputElement>(lv, "input[value=b]").checked && !q<HTMLInputElement>(lv, "input[value=a]").checked);
  check("select: US vs United States", q<HTMLSelectElement>(lv, "select[name='cards[auth]']").value === "1");
  check("checkbox agreed", q<HTMLInputElement>(lv, "#pp").checked);
  check("job-specific essay not guessed", val("textarea[name=comments]") === "");
  check("SSN left empty", val("input[name=ssn2]") === "");
  check("self-ID left empty", q<HTMLSelectElement>(lv, "select[name='eeo[gender]']").value === "");
  check("fuzzy fills marked for review", val("input[name='cards[yrs]']") === "6" && q(lv, "input[name='cards[yrs]']").getAttribute("data-vani-fill") === "review",
    JSON.stringify(outcomes.find(o => o.label.startsWith("How many years"))));
  const reworded = outcomes.find(o => o.label.startsWith("Do you require visa sponsorship"));
  check("a reworded sponsorship question fills from the other wording by intent, confidently", reworded?.filled === true && reworded.via === "intent" && reworded.review === false, JSON.stringify(reworded));
  check("confident fills marked ok", document.querySelectorAll("[data-vani-fill=ok]").length >= 2);
  check("never submitted", submits === 0, `submits ${submits}`);

  // 5. Typed values are not overwritten.
  q<HTMLInputElement>(lv, "input[name=firstName]").value = "Alex";
  await fillAll(collectFields(lv), cap.bank, "jobs.lever.co");
  check("typed value kept", val("input[name=firstName]") === "Alex");
  await fillAll(collectFields(lv), cap.bank, "jobs.lever.co", { overwrite: true });
  check("overwrite when asked", val("input[name=firstName]") === "Sam");
  clearMarks();
  check("clearMarks removes the outlines", document.querySelectorAll("[data-vani-fill]").length === 0);
}

async function ariaWidgets(): Promise<void> {
  const src = scanForm(q(document, "#aria-src")).refs;
  const by = (l: string) => src.find(r => r.info.label === l);
  const country = by("Country of residence");
  check("combobox: found as a select with its shown value", country?.widget === "combobox" && country.info.kind === "select" && country.info.value.join() === "United States of America", JSON.stringify(country?.info));
  const reloc = by("Are you willing to relocate?");
  check("aria radiogroup: options and checked value", reloc?.widget === "aria-radio" && reloc.info.options.join("|") === "Yes|No" && reloc.info.value.join() === "No", JSON.stringify(reloc?.info));
  const sw = by("Email me about similar roles");
  check("aria switch: boolean with aria-label", sw?.info.kind === "checkbox" && sw.info.value.join() === "yes", JSON.stringify(sw?.info));
  const ce = by("Notice period");
  check("contenteditable: text via aria-labelledby", ce?.widget === "contenteditable" && ce.info.value.join() === "Two weeks", JSON.stringify(ce?.info));
  const skills = by("Skills");
  check("multi-select combobox: every chip is read and the field is marked multi, not manual", skills?.widget === "combobox" && !skills.manual && skills.info.multi === true && skills.info.value.join() === "Go,Rust", JSON.stringify(skills?.info));
  check("combobox inner input is not a second field", src.length === 5, `got ${src.length}: ${src.map(r => r.info.label).join(" | ")}`);

  const bank = learn(q(document, "#aria-src"));
  const dstRoot = q<HTMLElement>(document, "#aria-dst");
  const dst = scanForm(dstRoot).refs;
  const cov = coverageOf(dst, bank, "x.example");
  check("coverage: buckets on the ARIA form", cov.found === 6 && cov.fillable === 5 && cov.unknown === 1 && cov.manual === 0 && cov.skipped === 0 && cov.unknownLabels.join() === "Hear about us", JSON.stringify(cov));
  const out = await fillAll(dst, bank, "x.example");
  const shown = (sel: string) => q(document, sel).parentElement!.querySelector(".select__single-value")!.textContent;
  check("combobox filled by choosing the mapped option", shown("#rs2") === "United States", String(shown("#rs2")));
  check("combobox popup closed again", q<HTMLElement>(document, "#rs2-list").hidden);
  const radios = Array.from(dstRoot.querySelectorAll("[role=radio]"));
  check("aria radio: 'No' chosen from the other wording's intent, confident", radios[1]!.getAttribute("aria-checked") === "true" && radios[0]!.getAttribute("aria-checked") === "false" && out.find(o => o.label.startsWith("Are you willing"))?.review === false);
  check("aria switch turned on", q(dstRoot, "[role=switch]").getAttribute("aria-checked") === "true");
  check("contenteditable filled", q<HTMLElement>(document, "#ce2").textContent === "Two weeks", String(q<HTMLElement>(document, "#ce2").textContent));
  const chips = () => Array.from(dstRoot.querySelectorAll(".select__multi-value__label")).map(n => n.textContent).join();
  const sk = out.find(o => o.label === "Skills");
  check("multi-select combobox: each saved value chosen through its option, widget shows both", sk?.filled === true && sk.review === false && chips() === "Go,Rust", `${JSON.stringify(sk)} chips ${chips()}`);
  check("multi-select combobox: menu closed again", q<HTMLElement>(document, "#rs4-list").hidden);
  check("unknown question left alone", out.find(o => o.label === "Hear about us")?.reason === "no_match" && shown("#rs3") === "LinkedIn");
  const again = await fillAll(scanForm(dstRoot).refs, bank, "x.example");
  check("widgets already answered are left alone", again.filter(o => o.reason === "has_value").length === 5 && shown("#rs2") === "United States" && chips() === "Go,Rust", JSON.stringify(again.map(o => o.reason)));
  check("filling ARIA widgets never submits", anySubmit === 0 && submitClicks === 0, `submit ${anySubmit} clicks ${submitClicks}`);
}

async function workday(): Promise<void> {
  const srcRoot = q<HTMLElement>(document, "#wd-src");
  const src = scanForm(srcRoot).refs;
  const l = src.map(r => `${r.info.label}=${r.info.value.join()}`).join(" | ");
  check("button dropdown read as a combobox", src[0]?.widget === "combobox" && src[0].info.value.join() === "United States of America", l);
  check("split date (inputs) is one field with an ISO value", src[1]?.widget === "split-date" && src[1].info.label === "Start date" && src[1].info.value.join() === "2023-05-01", l);
  check("split date (select + year) is one field", src[2]?.widget === "split-date" && src[2].info.label === "End date" && src[2].info.value.join() === "2024-02", l);
  check("split phone is one field", src[3]?.widget === "split-phone" && src[3].info.value.join() === "+1 555 0100", l);
  check("five controls became four fields", src.length === 4, l);

  const bank = learn(srcRoot);
  const dstRoot = q<HTMLElement>(document, "#wd-dst");
  const dst = scanForm(dstRoot).refs;
  let fileEvents = 0;
  const file = q<HTMLInputElement>(document, "#wd-file");
  for (const t of ["input", "change", "click", "focus"]) file.addEventListener(t, () => fileEvents++);
  const out = await fillAll(dst, bank, "x.example");
  const v = (sel: string) => q<HTMLInputElement>(dstRoot, sel).value;
  check("button dropdown filled by clicking the popup option", q<HTMLElement>(document, "#wd-c2").textContent === "United States of America", String(q<HTMLElement>(document, "#wd-c2").textContent));
  check("popup removed afterwards", document.getElementById("wd-c2-pop") === null);
  check("split date written to month/day/year", v("[data-automation-id=dateSectionMonth-input]") === "05" && v("[data-automation-id=dateSectionDay-input]") === "01" && v("[data-automation-id=dateSectionYear-input]") === "2023");
  const endSel = dstRoot.querySelectorAll<HTMLSelectElement>("select")[0]!;
  check("split date with a month select", endSel.value === "2" && dstRoot.querySelectorAll<HTMLInputElement>("input[placeholder=YYYY]")[0]!.value === "2024");
  check("split phone writes code and number", v("#wd-pc2") === "us" && v("#wd-pn2") === "555 0100", `${v("#wd-pc2")} ${v("#wd-pn2")}`);
  check("file input untouched: no events, no value, reported untouchable", fileEvents === 0 && file.value === "" && out.some(o => o.reason === "untouchable"), `events ${fileEvents}`);
  check("Save and Continue is never clicked by fill", anySubmit === 0 && submitClicks === 0);

  // A date with no day leaves the day alone and says to check it.
  const partial = mergeCapture(emptyBank(), [{ ...src[1]!.info, value: ["2022-09"] }], "x.example", NOW()).bank;
  const d2 = scanForm(dstRoot).refs;
  await fillAll(d2, partial, "x.example", { overwrite: true });
  check("partial date fills month and year, flagged for review", v("[data-automation-id=dateSectionMonth-input]") === "09" && v("[data-automation-id=dateSectionYear-input]") === "2022" && document.querySelector("[data-vani-fill=review]") !== null);
}

async function shadowAndFrames(): Promise<void> {
  const sh = scanForm(q(document, "#sh-src"));
  check("open shadow root: field found", sh.refs.length === 1 && sh.refs[0]!.info.label === "Preferred pronunciation" && sh.refs[0]!.info.value.join() === "SAM", JSON.stringify(sh.refs.map(r => r.info)));
  check("open shadow root: walked as a root", sh.roots.length >= 2);
  const bank = learn(q(document, "#sh-src"));
  await fillAll(scanForm(q(document, "#sh-dst")).refs, bank, "x.example");
  check("open shadow root: filled", (q(document, "#sh-dst").shadowRoot!.getElementById("sf") as HTMLInputElement).value === "SAM");

  const fsrc = q<HTMLIFrameElement>(document, "#if-src"), fdst = q<HTMLIFrameElement>(document, "#if-dst");
  for (let i = 0; i < 60 && !(fsrc.contentDocument?.getElementById("g") && fdst.contentDocument?.getElementById("g")); i++) await sleep(50);
  const wrapSrc = q<HTMLElement>(document, "#if-wrap-src"), wrapDst = q<HTMLElement>(document, "#if-wrap-dst");
  const s = scanForm(wrapSrc).refs;
  check("same-origin iframe: fields found through the iframe element", s.length === 2 && s[0]!.info.label === "GitHub URL" && s[0]!.info.value.join() === "https://github.example/sam", `${s.length}: ${labels(wrapSrc).join(" | ")}`);
  const fb = learn(wrapSrc);
  const out = await fillAll(scanForm(wrapDst).refs, fb, "x.example");
  const d = fdst.contentDocument!;
  check("same-origin iframe: filled across the frame boundary", (d.getElementById("g") as HTMLInputElement).value === "https://github.example/sam" && (d.getElementById("nm") as HTMLInputElement).value === "Sammy", out.map(o => `${o.label}:${o.reason ?? "ok"}`).join(" "));
  check("same-origin iframe: file input untouched", (d.getElementById("f") as HTMLInputElement).value === "" && out.some(o => o.reason === "untouchable"));
}

async function repeated(): Promise<void> {
  const srcRoot = q<HTMLElement>(document, "#wh-src");
  check("repeated sections: entries are numbered, entry 1 unchanged", labels(srcRoot).join("|") === "Employer|Job title|Employer (vani section 2)|Job title (vani section 2)", labels(srcRoot).join("|"));
  const bank = learn(srcRoot);
  check("repeated sections: saved under distinct keys", ["employer", "employer vani section 2", "job title", "job title vani section 2"].every(k => k in bank.answers), Object.keys(bank.answers).join());
  const dstRoot = q<HTMLElement>(document, "#wh-dst");
  await fillAll(scanForm(dstRoot).refs, bank, "other.example");
  const v = (id: string) => (document.getElementById(id) as HTMLInputElement).value;
  check("repeated sections: entry n filled from saved entry n", v("e3") === "Acme" && v("t3") === "Engineer" && v("e4") === "Globex" && v("t4") === "Lead", [v("e3"), v("t3"), v("e4"), v("t4")].join());
  check("repeated sections: an unsaved third entry is left blank", v("e5") === "" && v("t5") === "", [v("e5"), v("t5")].join());
}

async function tracking(): Promise<void> {
  let mem: Bank = emptyBank();
  let sends = 0;
  const mk = (extra: { onDom?: () => void; domMs?: number; domMaxWaitMs?: number } = {}) => createTracker({
    doc: document, host: "x.example", commitMs: 20, domMs: 30, domMaxWaitMs: 100, ...extra,
    getBank: async () => mem,
    send: async fields => {
      sends++;
      const { bank, ...counts } = mergeCapture(mem, fields, "x.example", new Date());
      mem = bank;
      return counts;
    },
  });
  const t = mk();
  const sp1 = q<HTMLInputElement>(document, "#sp1");
  sp1.value = "Initech"; sp1.dispatchEvent(new Event("change", { bubbles: true }));
  q<HTMLInputElement>(document, "#sp3").value = "hunter3";
  await sleep(80); // the committed value is snapshotted
  check("committed value is pending before any flush", t.pendingCount() > 0 && sends === 0, `pending ${t.pendingCount()} sends ${sends}`);

  // The step is replaced and the route changes with no submit and no click we catch.
  q(document, "#spa-step").replaceWith(Object.assign(document.createElement("div"), { id: "spa-step", innerHTML: '<div class="field"><label for="sp4">Expected salary</label><input id="sp4"></div>' }));
  history.pushState({}, "", "#step2");
  await sleep(300);
  const a1 = mem.answers["current company"];
  check("SPA step change without submit still captured the old step", a1?.value.join() === "Initech" && a1.uses === 1, JSON.stringify(a1));
  check("password never captured or sent", !JSON.stringify(mem).includes("hunter3"));

  const r2 = await t.flush("again", true);
  check("flushing the same page again saves nothing twice", mem.answers["current company"]!.uses === 1 && r2 !== null && r2.added === 0 && r2.updated === 0 && r2.unchanged >= 1, JSON.stringify(r2));

  const sp4 = q<HTMLInputElement>(document, "#sp4");
  sp4.value = "120000"; // typed, never blurred, no change event
  const sendsBeforeHide = sends;
  window.dispatchEvent(new Event("pagehide"));
  check("pagehide sends within the event, without awaiting a bank read", sends === sendsBeforeHide + 1, `sends ${sends - sendsBeforeHide}`);
  await sleep(120);
  check("pagehide flushes a value that never fired change", mem.answers["expected salary"]?.value.join() === "120000", JSON.stringify(mem.answers["expected salary"]));

  sp4.value = "130000";
  q<HTMLElement>(document, "#wd-next").click(); // "Save and Continue", a button outside any form
  await sleep(120);
  check("advance button outside a form flushes", mem.answers["expected salary"]?.value.join() === "130000");

  sp4.value = "140000";
  sp4.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, composed: true }));
  await sleep(300);
  check("Enter flushes", mem.answers["expected salary"]?.value.join() === "140000");

  const pron = q(document, "#sh-dst").shadowRoot!.getElementById("sf") as HTMLInputElement;
  pron.value = "ZED";
  pron.dispatchEvent(new FocusEvent("focusout", { bubbles: true, composed: true }));
  await sleep(120);
  await t.flush("check");
  // (the page holds two such fields, so the second one is entry 2 of the same question)
  check("focusout inside a shadow root commits", mem.answers["preferred pronunciation vani section 2"]?.value.join() === "ZED" && t.observedRoots() >= 3, `roots ${t.observedRoots()}`);
  check("tracking never submitted anything", anySubmit === 0 && submitClicks === 0, `submit ${anySubmit} clicks ${submitClicks}`);
  t.dispose();
  const before = sends;
  window.dispatchEvent(new Event("pagehide"));
  await sleep(80);
  check("a disposed tracker stays silent", sends === before);
}

async function adversarial(): Promise<void> {
  const box = q<HTMLElement>(document, "#stress");
  const empty = scanForm(box);
  check("empty form: no fields, not capped", empty.refs.length === 0 && !empty.capped);
  check("empty form: coverage line", coverageLine(coverageOf([], null, "x")) === "0 fields, 0 ready, 0 new.");

  box.innerHTML = '<label for="w1"> \n </label><input id="w1"><label for="w2">  </label><input id="w2" name="favourite_pet"><label for="d1">First</label><input id="d1"><label for="d1">Second</label><input id="d1">';
  let r: ReturnType<typeof scanForm> | null = null;
  let threw = "";
  try { r = scanForm(box); } catch (e) { threw = String(e); }
  check("whitespace labels and duplicate ids do not throw", r !== null && r.refs.length === 4, threw || String(r?.refs.length));
  check("whitespace-only label falls back to the field name", r?.refs[1]?.info.label === "favourite pet", r?.refs.map(x => JSON.stringify(x.info.label)).join());

  const refs = scanForm(box).refs;
  refs[1]!.els[0]!.remove();
  const out = await fillAll(refs, mergeCapture(emptyBank(), [{ ...refs[1]!.info, value: ["dog"] }], "x", NOW()).bank, "x");
  check("a node removed after the scan is reported detached, not crashed on", out[1]?.reason === "detached", JSON.stringify(out[1]));

  box.innerHTML = Array.from({ length: 1200 }, (_, i) => `<div class="field"><label for="b${i}">Big question ${i} text</label><input id="b${i}"></div>`).join("");
  const t0 = performance.now();
  const big = scanForm(box);
  const ms = performance.now() - t0;
  check("1200 fields: scan is capped at 500 and says how many it left", big.refs.length === 500 && big.capped && big.unread === 700, `refs ${big.refs.length} capped ${big.capped} unread ${big.unread}`);
  check("1200 fields: bounded work", ms < 4000, `${Math.round(ms)} ms`);
  const cov = coverageOf(big.refs, null, "x", { capped: big.capped, unread: big.unread });
  check("1200 fields: coverage never claims completeness", cov.capped && cov.found === 500 && coverageLine(cov).includes("700 more controls were not read"));

  box.textContent = "";
  // A virtual clock, so the result does not depend on how the browser schedules
  // real timers. Time only moves when the test moves it, and a timer is due only
  // once the clock is past it: a "fire at once" timer set during a mutation burst
  // is therefore still pending when the next burst arrives, as it is in a browser.
  let vnow = 0;
  let vseq = 0;
  const vtimers = new Map<number, { at: number; fn: () => void }>();
  const clock = {
    now: () => vnow,
    setTimer: (fn: () => void, ms: number) => { const id = ++vseq; vtimers.set(id, { at: vnow + ms, fn }); return id; },
    clearTimer: (h: unknown) => { vtimers.delete(h as number); },
  };
  const runDue = (): void => {
    for (;;) {
      const due = [...vtimers].filter(([, x]) => x.at < vnow).sort((a, b) => a[1].at - b[1].at)[0];
      if (!due) return;
      vtimers.delete(due[0]);
      due[1].fn();
    }
  };
  let calls = 0;
  const t = createTracker({ doc: document, host: "x", getBank: async () => emptyBank(), send: async () => null, onDom: () => calls++, domMs: 50, domMaxWaitMs: 200, clock });
  for (let step = 0; step < 200; step++) { // 1000 virtual ms, 50 nodes every 5 ms: 10000 mutations
    vnow += 5;
    for (let i = 0; i < 50; i++) box.append(document.createElement("i"));
    await Promise.resolve(); // the observer's microtask runs before this continues
    await Promise.resolve();
    runDue();
  }
  const during = calls;
  vnow += 60; // the storm stops: the trailing debounce fires once
  runDue();
  check("mutation storm: never starved (a burst fires at least every domMaxWait), bounded (not once per mutation)", during === 4 && calls === 5, `onDom ran ${during} times during the storm and ${calls} after, for 10000 mutations`);
  check("mutation storm: nothing left scheduled once it settles", vtimers.size === 0, `${vtimers.size} timers pending`);
  t.dispose();
  let quiet = 0;
  const real = createTracker({ doc: document, host: "x", getBank: async () => emptyBank(), send: async () => null, onDom: () => quiet++, domMs: 20, domMaxWaitMs: 100 });
  box.append(document.createElement("i"));
  await sleep(200);
  check("one mutation on the real clock reaches onDom", quiet >= 1, `onDom ran ${quiet} times`);
  real.dispose();
  box.innerHTML = '<label for="f1">Failing question</label><input id="f1" value="v">';
  const failing = createTracker({ doc: document, host: "x", getBank: async () => emptyBank(), send: async () => null });
  const fr = await failing.flush("saved", true);
  check("a save the background refused is reported as failed, not as zero saved", fr?.failed === true && fr.added === 0, JSON.stringify(fr));
  failing.dispose();
  box.textContent = "";
}

async function liveHelp(): Promise<void> {
  // The posting, as JSON-LD: one malformed block (skipped), one @graph with the JobPosting.
  const bad = document.createElement("script");
  bad.type = "application/ld+json";
  bad.textContent = "{not json";
  const ld = document.createElement("script");
  ld.type = "application/ld+json";
  ld.textContent = JSON.stringify({ "@graph": [{ "@type": "Organization", name: "Ignore" }, {
    "@type": "JobPosting", title: "ML Inference Engineer", hiringOrganization: { "@type": "Organization", name: "Acme Robotics" },
    description: "<p>Build <b>GPU</b> inference.</p><img src=x onerror=\"window.__vaniPwned=1\"><ul><li>Rust</li><li>CUDA</li></ul>",
  }] });
  document.head.append(bad, ld);
  const job = readJobInfo(document);
  await sleep(50);
  check("job info: JobPosting found inside @graph past a malformed block", job.title === "ML Inference Engineer" && job.company === "Acme Robotics", JSON.stringify(job));
  check("job info: description flattened to text", job.description.includes("Build GPU inference.") && job.description.includes("Rust") && !job.description.includes("<"), job.description);
  check("job info: description HTML is inert (no handler ran)", (window as unknown as { __vaniPwned?: number }).__vaniPwned === undefined);

  const box = document.createElement("form");
  box.innerHTML = `
    <label for="lh-go">Years of experience with Go</label><input id="lh-go" name="lh-go">
    <label for="lh-why">Why are you interested in Acme Robotics?</label><textarea id="lh-why" name="lh-why"></textarea>
    <label for="lh-off">Preferred office</label><select id="lh-off" name="lh-off"><option value=""></option><option>Berlin</option></select>`;
  document.body.append(box);
  const bank = emptyBank();
  bank.answers["years of python experience"] = {
    key: "years of python experience", labels: ["years of python experience"], kind: "text", value: ["6"], sensitivity: "standard",
    signatures: [], uses: 2, fills: 0, updatedAt: "2026-10-01T00:00:00.000Z", review: false,
  };
  const refs = scanForm(box).refs;
  const go = refs.find(r => r.info.label === "Years of experience with Go")!;
  const why = refs.find(r => r.info.label.startsWith("Why are you"))!;
  const office = refs.find(r => r.info.label === "Preferred office")!;

  const sugg = suggest(go.info, bank, "x.example");
  check("live help: a similar saved answer is offered for a new question", sugg.length === 1 && sugg[0]!.match.value[0] === "6", JSON.stringify(sugg.map(s => s.question)));
  const o = await fillRef(go, sugg[0]!.match);
  check("live help: a picked suggestion fills yellow", o.filled && q<HTMLInputElement>(box, "#lh-go").value === "6" && q<HTMLElement>(box, "#lh-go").getAttribute("data-vani-fill") === "review", JSON.stringify(o));

  check("draft: offered for an empty free-text field only", draftable(why) && !draftable(office), `why ${draftable(why)} office ${draftable(office)}`);
  const typed = document.createElement("div");
  typed.innerHTML = '<input id="lh-typed" value="jane@example.invalid">';
  box.append(typed);
  const typedRef = scanForm(typed).refs[0]!;
  check("draft: never offered on a field the user typed into (nothing typed can reach the request)", !draftable(typedRef) && currentText(typedRef) === "jane@example.invalid", JSON.stringify(typedRef.info));
  typed.remove();
  const d1 = insertDraft(why, "I want to build the inference stack for Acme's robots.");
  const ta = q<HTMLTextAreaElement>(box, "#lh-why");
  check("draft: inserted, marked yellow and tagged as a draft", d1.filled && ta.value.startsWith("I want") && ta.getAttribute("data-vani-fill") === "review" && ta.getAttribute("data-vani-draft") === "1", JSON.stringify(d1));
  const d2 = insertDraft(why, "Replacement text.");
  check("draft: never overwrites text without the user's say-so", !d2.filled && d2.reason === "has_value" && ta.value.startsWith("I want"));
  const d3 = insertDraft(why, "Replacement text.", { overwrite: true });
  check("draft: overwrites when the user confirms", d3.filled && ta.value === "Replacement text.");
  check("draft: never into a choice field", !insertDraft(office, "Berlin").filled && q<HTMLSelectElement>(box, "#lh-off").value === "");
  const rescanned = scanForm(box).refs.find(r => r.info.label.startsWith("Why are you"))!;
  const saved = mergeCapture(emptyBank(), [rescanned.info], "x.example", NOW()).bank;
  const savedWhy = Object.values(saved.answers)[0];
  check("draft: a kept draft is saved for review, never as a confident answer", rescanned.info.drafted === true && savedWhy?.review === true, JSON.stringify(savedWhy));

  clearMarks();
  box.remove();
  bad.remove();
  ld.remove();
}

const info = (label: string, kind: FieldInfo["kind"], value: string[], extra: Partial<FieldInfo> = {}): FieldInfo =>
  ({ kind, label, name: "", autocomplete: "", automationId: "", options: [], required: false, value, ...extra });
const bankOf = (...fs: FieldInfo[]): Bank => mergeCapture(emptyBank(), fs, "x.example", NOW()).bank;

async function multiSelect(): Promise<void> {
  const srcRoot = q<HTMLElement>(document, "#ms-src");
  const src = scanForm(srcRoot).refs;
  const by = (l: string) => src.find(r => r.info.label === l);
  const langs = by("Languages"), certs = by("Certifications"), interests = by("Interests"), tools = by("Tools");
  check("select multiple: every selected value is read", langs?.info.multi === true && langs.info.value.join() === "Rust,Java" && langs.manual === "", JSON.stringify(langs?.info));
  check("listbox with aria-multiselectable: every selected option is read, and it is not manual", certs?.info.multi === true && certs.info.value.join() === "AWS,Azure" && certs.manual === "", JSON.stringify(certs?.info));
  check("chip picker (react-select style): every chip is read", interests?.info.multi === true && interests.info.value.join() === "Chess,Climbing", JSON.stringify(interests?.info));
  check("chip picker (MUI style): chip labels are read, not their delete icons", tools?.info.multi === true && tools.info.value.join() === "Git,Make", JSON.stringify(tools?.info));

  const bank = learn(srcRoot);
  check("a multi-select is saved as one answer holding every value", bank.answers["languages"]?.value.join() === "Rust,Java" && bank.answers["interests"]?.value.join() === "Chess,Climbing", JSON.stringify(bank.answers["languages"]));

  const dstRoot = q<HTMLElement>(document, "#ms-dst");
  const sel = q<HTMLSelectElement>(dstRoot, "#ms1d");
  const picked = () => Array.from(sel.selectedOptions).map(o => o.text).join();
  const certsOn = () => Array.from(dstRoot.querySelectorAll("#ms2d [aria-selected=true]")).map(o => o.textContent).join();
  const interestsInput = q<HTMLInputElement>(dstRoot, "#ms3d");
  let interestEvents = 0;
  for (const t of ["input", "change"]) interestsInput.addEventListener(t, () => interestEvents++); // text never entered; focus, clicks and Escape are how a list is opened and closed
  const out = await fillAll(scanForm(dstRoot).refs, bank, "x.example");
  const o = (l: string) => out.find(x => x.label === l);
  check("select multiple: every saved value selected, others left unselected", o("Languages")?.filled === true && picked() === "Java,Rust", `${picked()} ${JSON.stringify(o("Languages"))}`);
  check("listbox multi: each option clicked and shown selected", o("Certifications")?.filled === true && certsOn() === "AWS,Azure", `${certsOn()} ${JSON.stringify(o("Certifications"))}`);
  check("chip picker with no option list stays manual: nothing typed, no chip added", o("Interests")?.reason === "manual" && !o("Interests")?.filled && interestEvents === 0 && interestsInput.value === "" && dstRoot.querySelector("#ms3d")!.parentElement!.querySelectorAll(".select__multi-value").length === 0, JSON.stringify(o("Interests")));

  // A selection the user made is not replaced without overwrite; with it the selection becomes exactly the saved one.
  sel.selectedIndex = 2;
  const kept = await fillAll(scanForm(dstRoot).refs, bank, "x.example");
  check("select multiple: the user's own selection is kept without overwrite", kept.find(x => x.label === "Languages")?.reason === "has_value" && picked() === "Python", picked());
  await fillAll(scanForm(dstRoot).refs, bank, "x.example", { overwrite: true });
  check("select multiple: overwrite makes the selection exactly the saved values", picked() === "Java,Rust", picked());

  // A saved value with no option: the others still fill, and the field is flagged for review.
  const part = bankOf(info("Languages", "checkbox", ["Go", "Haskell"], { multi: true, options: ["Go", "Python"] }), info("Skills", "select", ["Go", "Haskell"], { multi: true }));
  sel.selectedIndex = -1;
  const p1 = await fillAll(scanForm(dstRoot).refs.filter(r => r.info.label === "Languages"), part, "x.example");
  check("select multiple: a value with no option is skipped and the fill is flagged for review", p1[0]?.filled === true && p1[0].review === true && picked() === "Go", `${picked()} ${JSON.stringify(p1[0])}`);
  const ariaDst = q<HTMLElement>(document, "#aria-dst");
  ariaDst.querySelectorAll(".select__multi-value").forEach(n => n.remove());
  const p2 = await fillAll(scanForm(ariaDst).refs.filter(r => r.info.label === "Skills"), part, "x.example");
  const chipsNow = Array.from(ariaDst.querySelectorAll(".select__multi-value__label")).map(n => n.textContent).join();
  check("combobox multi: a value with no option is skipped and the fill is flagged for review", p2[0]?.filled === true && p2[0].review === true && chipsNow === "Go", `${chipsNow} ${JSON.stringify(p2[0])}`);
  check("multi-select filling never submits", anySubmit === 0 && submitClicks === 0);
  // leave the ARIA form as the earlier tests found it
  ariaDst.querySelectorAll(".select__multi-value").forEach(n => n.remove());
}

async function dates(): Promise<void> {
  const src = scanForm(q(document, "#ms-src")).refs;
  const d1 = src.find(r => r.info.label === "Available from"), d2 = src.find(r => r.info.label === "Graduation month");
  check("native date and month inputs are read as ISO values", d1?.info.kind === "date" && d1.info.value.join() === "2024-05-01" && d2?.info.value.join() === "2023-06" && d1.manual === "" && d2.manual === "", JSON.stringify([d1?.info, d2?.info]));

  const dstRoot = q<HTMLElement>(document, "#ms-dst");
  const dt1 = q<HTMLInputElement>(dstRoot, "#dt1d"), dt2 = q<HTMLInputElement>(dstRoot, "#dt2d"), dt3 = q<HTMLInputElement>(dstRoot, "#dt3d"), dt4 = q<HTMLInputElement>(dstRoot, "#dt4d");
  dt1.value = ""; dt2.value = ""; // the multi-select test above filled these from the same saved form
  // The page's own setter trap: a write through the instance (el.value = ...) is counted; the native prototype setter is not.
  let direct = 0, inputs = 0, changes = 0;
  const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!;
  Object.defineProperty(dt1, "value", { configurable: true, get() { return proto.get!.call(this); }, set(v: string) { direct++; proto.set!.call(this, v); } });
  dt1.addEventListener("input", () => inputs++);
  dt1.addEventListener("change", () => changes++);
  // Saved in other shapes than the control accepts: US-order text for a date, a full date for a month.
  const bank = bankOf(info("Available from", "text", ["05/01/2024"]), info("Graduation month", "text", ["2024-05-01"]), info("Week of arrival", "text", ["2024-W20"]), info("Preferred start", "text", ["05/01/2024"]));
  const out = await fillAll(scanForm(dstRoot).refs, bank, "x.example");
  const o = (l: string) => out.find(x => x.label === l);
  check("date input: a saved US-order date is written as ISO through the native setter, with input and change events", dt1.value === "2024-05-01" && o("Available from")?.filled === true && direct === 0 && inputs === 1 && changes === 1, `value ${dt1.value} direct ${direct} input ${inputs} change ${changes} ${JSON.stringify(o("Available from"))}`);
  check("month input: a full saved date is written as YYYY-MM", dt2.value === "2024-05" && o("Graduation month")?.filled === true, `value ${dt2.value} ${JSON.stringify(o("Graduation month"))}`);
  check("week input is reported manual, never written", o("Week of arrival")?.reason === "manual" && dt3.value === "", JSON.stringify(o("Week of arrival")));
  check("read-only picker input is reported manual, never written", o("Preferred start")?.reason === "manual" && dt4.value === "", JSON.stringify(o("Preferred start")));

  delete (dt1 as unknown as { value?: string }).value;
  dt1.value = "";
  const noDay = await fillAll(scanForm(dstRoot).refs.filter(r => r.info.label === "Available from"), bankOf(info("Available from", "text", ["2024-05"])), "x.example");
  check("date input: a month with no day is not guessed", noDay[0]?.filled === false && noDay[0].reason === "no_option" && dt1.value === "", `${dt1.value} ${JSON.stringify(noDay[0])}`);
  const impossible = await fillAll(scanForm(dstRoot).refs.filter(r => r.info.label === "Available from"), bankOf(info("Available from", "text", ["2024-02-30"])), "x.example");
  check("date input: an impossible date is reported, not claimed as filled", impossible[0]?.filled === false && dt1.value === "", `${dt1.value} ${JSON.stringify(impossible[0])}`);
  const typed = "2022-01-02";
  dt1.value = typed;
  const keep = await fillAll(scanForm(dstRoot).refs.filter(r => r.info.label === "Available from"), bankOf(info("Available from", "text", ["2024-05-01"])), "x.example");
  check("date input: a date the user chose is kept", keep[0]?.reason === "has_value" && dt1.value === typed);
  dt1.value = ""; dt2.value = "";
}

async function typeahead(): Promise<void> {
  const root = q<HTMLElement>(document, "#ta-dst");
  const input = q<HTMLInputElement>(root, "#ta1");
  let typedEvents = 0;
  input.addEventListener("input", () => typedEvents++);
  const shown = () => root.querySelector(".select__single-value")!.textContent;
  const bank = bankOf(info("School", "select", ["Stanford University"]));
  const off = await fillAll(scanForm(root).refs, bank, "x.example");
  check("typeahead is off by default: reported manual, nothing typed, nothing chosen", off[0]?.reason === "manual" && !off[0].filled && typedEvents === 0 && input.value === "" && shown() === "", `events ${typedEvents} ${JSON.stringify(off[0])}`);
  const on = await fillAll(scanForm(root).refs, bank, "x.example", { typeahead: true });
  check("typeahead opted in: typed once, the option appears and is chosen", on[0]?.filled === true && typedEvents === 1 && shown() === "Stanford University", `events ${typedEvents} shown ${shown()} ${JSON.stringify(on[0])}`);
  root.querySelector(".select__single-value")!.textContent = "";
  typedEvents = 0;
  const none = await fillAll(scanForm(root).refs, bankOf(info("School", "select", ["Nonexistent Academy of Arts"])), "x.example", { typeahead: true });
  check("typeahead opted in, nothing matches: at most one query and one clear, field left empty, reported manual", none[0]?.reason === "manual" && typedEvents <= 2 && input.value === "" && shown() === "", `events ${typedEvents} ${JSON.stringify(none[0])}`);
  check("typeahead never submits", anySubmit === 0 && submitClicks === 0);
}

async function oncePerPage(): Promise<void> {
  const box = document.createElement("div");
  box.innerHTML = '<label for="op1">Favourite editor</label><input id="op1" value="vim"><label for="op2">Favourite shell</label><input id="op2" value="fish">';
  document.body.append(box);
  const stale = emptyBank(); // the tracker's bank reads never see the saves
  let mem = emptyBank();
  let sends = 0, refuse = false;
  const t = createTracker({
    doc: document, host: "x.example", getBank: async () => stale,
    send: async fields => {
      sends++;
      if (refuse) return null;
      const { bank, ...counts } = mergeCapture(mem, fields, "x.example", new Date());
      mem = bank;
      return counts;
    },
  });
  await t.flush("first");
  const after1 = sends;
  const r2 = await t.flush("second");
  check("same value on the same page is sent once even when the bank read is stale", after1 === 1 && sends === 1 && mem.answers["favourite editor"]?.uses === 1 && r2 !== null && r2.unchanged >= 2, `sends ${sends} uses ${mem.answers["favourite editor"]?.uses} ${JSON.stringify(r2)}`);
  window.dispatchEvent(new Event("pagehide"));
  document.dispatchEvent(new Event("visibilitychange"));
  await sleep(50);
  check("pagehide after a flush does not send it again", sends === 1 && mem.answers["favourite editor"]?.uses === 1, `sends ${sends} uses ${mem.answers["favourite editor"]?.uses}`);

  refuse = true;
  q<HTMLInputElement>(box, "#op1").value = "emacs";
  const refused = await t.flush("refused");
  check("a save the background refused is reported failed", refused?.failed === true, JSON.stringify(refused));
  refuse = false;
  await t.flush("retry");
  check("a refused value is sent again, and a changed value goes out once", mem.answers["favourite editor"]?.value.join() === "emacs" && mem.answers["favourite editor"]?.uses === 2 && mem.answers["favourite shell"]?.uses === 1, JSON.stringify([mem.answers["favourite editor"], mem.answers["favourite shell"]]));
  await t.flush("again");
  check("and then not again", mem.answers["favourite editor"]?.uses === 2, String(mem.answers["favourite editor"]?.uses));
  t.dispose();
  box.remove();
}

// The tracker tells the page script when a button that sends the application is pressed, so it can
// offer to mark the job Applied. Steps along the way (Next, Save, Review) and Apply do not count.
// The buttons are type=button: nothing here may submit a form.
async function submitSignal(): Promise<void> {
  const box = document.createElement("div");
  box.innerHTML = '<button type="button" id="ss-next">Next</button><button type="button" id="ss-save">Save and continue</button>' +
    '<button type="button" id="ss-review">Review and submit</button><button type="button" id="ss-apply">Apply</button>' +
    '<button type="button" id="ss-submit"><span id="ss-inner">Submit application</span></button>' +
    '<div role="button" id="ss-send">Send application</div>';
  document.body.append(box);
  let signals = 0;
  const t = createTracker({ doc: document, host: "x.example", getBank: async () => emptyBank(), send: async () => null, onSubmit: () => { signals++; } });
  for (const id of ["ss-next", "ss-save", "ss-review", "ss-apply"]) q<HTMLElement>(box, `#${id}`).click();
  await sleep(30);
  check("Next, Save, Review and Apply are not a submit", signals === 0, `signals ${signals}`);
  q<HTMLElement>(box, "#ss-inner").click();
  check("a click inside a Submit application button is a submit", signals === 1, `signals ${signals}`);
  q<HTMLElement>(box, "#ss-send").click();
  check("Send application is a submit", signals === 2, `signals ${signals}`);
  t.dispose();
  q<HTMLElement>(box, "#ss-submit").click();
  check("a disposed tracker stays silent about submits", signals === 2, `signals ${signals}`);
  box.remove();
}

// job-boards.greenhouse.io (React, 2026-10): each react-select dropdown keeps a second
// <input required tabindex=-1 aria-hidden=true> beside its combobox until a value is
// chosen, only for the browser's required check. No person answers it: it is not a
// question, and counting it doubled every dropdown and left "detached" rows after Fill.
// Search boxes are not questions either.
async function notQuestions(): Promise<void> {
  const box = document.createElement("div");
  box.innerHTML =
    '<div class="select"><label id="rs-l" for="rs">Have you ever interviewed here before?<span aria-hidden="true">*</span></label>' +
    '<div class="select-shell"><div class="select__control"><div class="select__value-container"><div class="select__placeholder">Select...</div>' +
    '<input id="rs" class="select__input" role="combobox" aria-labelledby="rs-l" aria-expanded="false" aria-haspopup="true" aria-required="true" autocomplete="off" value=""></div></div>' +
    '<input required tabindex="-1" aria-hidden="true" class="x-requiredInput" value="" style="opacity:0;width:100%;height:20px"></div></div>' +
    '<label><input type="checkbox" name="nq-agree" tabindex="-1" aria-hidden="true"> I agree to the terms</label>' +
    '<label for="nq-s1">Search jobs</label><input id="nq-s1" type="search">' +
    '<div role="search"><label for="nq-s2">Find</label><input id="nq-s2"></div>' +
    '<search><input id="nq-s3" aria-label="Site search"></search>' +
    '<input id="nq-s4" type="search" role="combobox" aria-label="Search" aria-expanded="false">' +
    '<label for="nq-loc">Location</label><input id="nq-loc" type="search" role="combobox" aria-expanded="false">' +
    '<label for="nq-q">Why this team?</label><input id="nq-q">';
  document.body.append(box);
  const got = labels(box);
  check("react-select's hidden required input is not a field", got.filter(l => l === "" || l.startsWith("Have you ever interviewed")).length === 1, JSON.stringify(got));
  check("a custom checkbox hidden the same way is still read", got.includes("I agree to the terms"), JSON.stringify(got));
  check("search boxes are not questions", !got.some(l => /search|find/i.test(l)) && got.includes("Why this team?"), JSON.stringify(got));
  check("exactly the four questions (a search-type combobox labelled with a question is one)", JSON.stringify(got) === JSON.stringify(["Have you ever interviewed here before?", "I agree to the terms", "Location", "Why this team?"]), JSON.stringify(got));
  box.remove();

  // A page that is not a form (a search page the popup injected Vani into): pressing Enter, leaving
  // or hiding it saves nothing. Saving it on purpose still does.
  const frame = document.createElement("iframe");
  frame.srcdoc = '<!doctype html><form action="#" onsubmit="return false"><input name="q" aria-label="Search" value=""></form>';
  document.body.append(frame);
  await new Promise<void>(r => frame.addEventListener("load", () => r(), { once: true }));
  const doc = frame.contentDocument!;
  let mem: Bank = emptyBank();
  let sends = 0;
  const t = createTracker({
    doc, host: "www.search.example", commitMs: 20,
    getBank: async () => mem,
    send: async fields => {
      sends++;
      const { bank, ...counts } = mergeCapture(mem, fields, "www.search.example", new Date());
      mem = bank;
      return counts;
    },
  });
  const qbox = doc.querySelector("input")!;
  qbox.value = "a private query";
  qbox.dispatchEvent(new Event("change", { bubbles: true }));
  qbox.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, composed: true }));
  await sleep(300);
  frame.contentWindow!.dispatchEvent(new Event("pagehide"));
  await t.flush("route change");
  await sleep(50);
  check("one search box: Enter, route change and pagehide save nothing", sends === 0 && Object.keys(mem.answers).length === 0, `sends ${sends} ${JSON.stringify(Object.keys(mem.answers))}`);
  await t.flush("saved", true);
  check("Save on purpose still saves it", sends === 1 && mem.answers["search"]?.value.join() === "a private query", `sends ${sends}`);
  t.dispose();
  frame.remove();
}

// job-boards.greenhouse.io's Country (2026-10): options read "United States +1" beside a flag, and once one
// is chosen the box shows only the flag and "+1", which many countries share. Fill must check the flag:
// the right one is a confident fill, a different one is not a fill, and no flag at all is a fill to review.
async function dialPicker(): Promise<void> {
  const box = document.createElement("div");
  const opts = [["Canada", "ca", "+1"], ["United States", "us", "+1"], ["United Kingdom", "gb", "+44"]] as const;
  const mk = (id: string, label: string, flags: boolean, liar: boolean): string =>
    `<div class="select"><label id="${id}-l" for="${id}">${label}</label><div class="select__control"><div class="select__value-container">` +
    `<div class="select__single-value"></div><input id="${id}" role="combobox" aria-controls="${id}-list" aria-labelledby="${id}-l" aria-expanded="false"></div></div>` +
    `<div id="${id}-list" role="listbox" hidden data-flags="${flags ? 1 : 0}" data-liar="${liar ? 1 : 0}"></div></div>`;
  box.innerHTML = mk("dp1", "Country", true, false) + mk("dp2", "Country of citizenship", true, true) + mk("dp3", "Country code country", false, false);
  document.body.append(box);
  for (const id of ["dp1", "dp2", "dp3"]) {
    const input = q<HTMLInputElement>(box, `#${id}`);
    const list = q<HTMLElement>(box, `#${id}-list`);
    const single = input.parentElement!.querySelector<HTMLElement>(".select__single-value")!;
    const flags = list.dataset.flags === "1", liar = list.dataset.liar === "1";
    const flag = (cc: string) => (flags ? `<div class="iti__flag iti__${cc}"></div>` : "");
    input.addEventListener("mousedown", () => {
      list.innerHTML = opts.map(([name, cc, code]) => `<div role="option" data-cc="${cc}" data-code="${code}">${flag(cc)}${name} ${code}</div>`).join("");
      list.hidden = false;
      list.querySelectorAll<HTMLElement>("[role=option]").forEach(o => o.addEventListener("click", () => {
        single.innerHTML = `${flag(liar ? "ca" : o.dataset.cc!)}<span>${o.dataset.code}</span>`;
        list.hidden = true; list.textContent = "";
      }));
    });
  }
  const answer = mergeCapture(emptyBank(), [{ kind: "text", label: "Country", name: "", autocomplete: "", automationId: "", options: [], required: false, value: ["United States"] }], "x", NOW()).bank.answers["country"]!;
  const m = { answer, via: "label" as const, score: 0.9, value: ["United States"], review: false };
  const refs = scanForm(box).refs;
  const ref = (id: string) => refs.find(r => r.els[0]!.id === id)!;
  const shownFlag = (id: string) => q(box, `#${id}`).parentElement!.querySelector(".iti__flag")?.className ?? "";
  const honest = await fillRef(ref("dp1"), m);
  check("dial picker: the flag shown is the one chosen, a confident fill", honest.filled && honest.review === false && shownFlag("dp1").includes("iti__us"), JSON.stringify(honest));
  const liar = await fillRef(ref("dp2"), m);
  check("dial picker: a different flag shown is not a fill", !liar.filled, JSON.stringify(liar));
  const bare = await fillRef(ref("dp3"), m);
  check("dial picker: only \"+1\" and no flag is a fill to review", bare.filled && bare.review === true, JSON.stringify(bare));
  box.remove();
}

// The real job-boards.greenhouse.io application form (test/fixtures, captured 2026-10 from the Anthropic
// posting: markup only). Static, so React does not run, but what Fill reads is all here: react-select
// comboboxes with their hidden required proxies, the phone picker with its 244-country list and search
// box, and the questions as worded. In its own frame so its ids do not meet this page's.
async function greenhouseFixture(): Promise<void> {
  const frame = document.createElement("iframe");
  frame.style.cssText = "width:900px;height:600px";
  frame.srcdoc = "<!doctype html><meta charset=utf-8><body>" + await (await fetch("fixtures/greenhouse-anthropic-2026-10.html")).text();
  document.body.append(frame);
  await new Promise<void>(r => frame.addEventListener("load", () => r(), { once: true }));
  const doc = frame.contentDocument!;
  const refs = scanForm(doc).refs;
  const labels = refs.map(r => r.info.label);
  check("greenhouse form: 25 fields, every one labelled, no hidden proxies or search boxes", refs.length === 25 && !labels.includes("") && !labels.some(l => /^search$/i.test(l)),
    `${refs.length}: ${labels.join(" | ")}`);
  const want: Record<string, string> = {
    "Are you open to working in-person in one of our offices 25% of the time?": "open to working in person",
    "When is the earliest you would want to start working with us?": "earliest start date",
    "Do you have any deadlines or timeline considerations we should be aware of?": "deadlines or timeline considerations",
    "AI Policy for Application": "ai policy for applications",
    "Do you require visa sponsorship?": "visa sponsorship",
    "Will you now or will you in the future require employment visa sponsorship": "visa sponsorship",
    "Are you open to relocation for this role?": "open to relocation",
    "What is the address from which you plan on working?": "where youll work from",
    "Have you ever interviewed at Anthropic before?": "interviewed here before",
    "Please read the arbitration agreement below": "arbitration agreement",
    "Agreement to Arbitrate": "arbitration agreement",
  };
  const wrong: string[] = [];
  for (const [start, key] of Object.entries(want)) {
    const l = labels.find(x => x.startsWith(start));
    if (!l || intentOf(l) !== key) wrong.push(`${start} → ${l ? intentOf(l) || "(none)" : "(no field)"}`);
  }
  check("greenhouse form: each recurring question reads as its intent", wrong.length === 0, wrong.join("; "));
  const whyLabel = labels.find(l => l.startsWith("Why Anthropic"));
  check("greenhouse form: Why Anthropic is a motivation essay, left to Draft", !!whyLabel && motivationEssay(whyLabel), String(whyLabel));

  // Every basic answered (test values), as the popup saves them.
  const bank = mergeCapture(emptyBank(), basicsFields({
    first: "Test", last: "Applicant", email: "applicant@example.invalid", phone: "+1 555 555 0100", country: "United States",
    linkedin: "https://www.linkedin.com/in/test-applicant", website: "https://example.invalid",
    "visa-sponsorship": "No", "open-to-relocation": "Yes", "open-to-working-in-person": "Yes", "interviewed-here-before": "No",
    "earliest-start-date": "In two weeks", "deadlines-or-timeline-considerations": "None", "ai-policy-for-applications": "Yes",
    "where-youll-work-from": "Testville, TX", "arbitration-agreement": "I agree",
  }), BASICS_HOST, NOW()).bank;
  const unmatched = refs.filter(r => r.info.kind !== "file" && !matchField(r.info, bank, "job-boards.greenhouse.io")).map(r => r.info.label);
  check("greenhouse form: with every basic saved, only the optional, essay and self-ID questions are left",
    JSON.stringify(unmatched) === JSON.stringify(["(Optional) Personal Preferences", "Why Anthropic?", "Additional Information", "Gender", "Are you Hispanic/Latino?", "Veteran Status"]),
    JSON.stringify(unmatched));
  frame.remove();
}

// ownText walks the text in place; it must read exactly what the old
// clone-and-delete version read, on every element of this page and on
// adversarial shapes.
function clonedOwnText(el: Element): string {
  const clone = el.cloneNode(true) as Element;
  clone.querySelectorAll(OWN_TEXT_SKIP).forEach(n => n.remove());
  return (clone.textContent ?? "").replace(/\s+/g, " ").trim().slice(0, MAX_LABEL); // as clean() does
}

async function ownTextParity(): Promise<void> {
  const box = document.createElement("div");
  box.innerHTML = `
    <label id="ot1">Name <input value="x"> <span>first</span><!-- a comment --></label>
    <button id="ot2">Press <b>me</b> <span aria-hidden="true">hidden</span></button>
    <div id="ot3">Q <select><option>one</option><option>two</option></select> <span aria-hidden="TRUE">kept: the value is case-sensitive</span>
      <div><textarea>t</textarea><p>deep <script>s()</script><style>.a{}</style>text</p></div></div>
    <fieldset id="ot4"><legend>Legend <em>here</em></legend><input type="radio"> Yes</fieldset>
    <span id="ot5" aria-hidden="true">root is hidden but is the element asked about</span>
    <div id="ot6"></div>`;
  document.body.appendChild(box);
  let mismatches = 0, compared = 0;
  const detail: string[] = [];
  for (const el of [...document.querySelectorAll("*")]) {
    compared++;
    const want = clonedOwnText(el), got = ownText(el);
    if (got !== want) {
      mismatches++;
      if (detail.length < 3) detail.push(`${el.localName}#${el.id}: got ${JSON.stringify(got.slice(0, 80))} want ${JSON.stringify(want.slice(0, 80))}`);
    }
  }
  check(`ownText matches the clone-based read on all ${compared} elements of the page`, mismatches === 0 && compared > 100, detail.join(" | "));
  check("ownText skips controls, options, scripts and aria-hidden parts", ownText(box.querySelector("#ot3")!) === "Q kept: the value is case-sensitive deep text", ownText(box.querySelector("#ot3")!));
  check("ownText reads an element that itself matches what it skips", ownText(box.querySelector("#ot2")!) === "Press me" && ownText(box.querySelector("#ot5")!) !== "");
  box.remove();
}

async function main(): Promise<void> {
  for (const [name, fn] of [["own text", ownTextParity], ["core", core], ["aria", ariaWidgets], ["workday", workday], ["shadow+frames", shadowAndFrames], ["repeated", repeated], ["tracking", tracking], ["adversarial", adversarial], ["live help", liveHelp], ["multi-select", multiSelect], ["dates", dates], ["typeahead", typeahead], ["once per page", oncePerPage], ["submit signal", submitSignal], ["not questions", notQuestions], ["dial picker", dialPicker], ["greenhouse fixture", greenhouseFixture]] as const) {
    try { await fn(); } catch (e) { check(`${name} crashed`, false, e instanceof Error ? `${e.message}\n${e.stack}` : String(e)); }
  }
  check("nothing in the whole run submitted a form or pressed a submit button", anySubmit === 0 && submitClicks === 0 && submits === 0, `submit ${anySubmit} clicks ${submitClicks}`);
  const total = lines.length;
  (document.getElementById("results") as HTMLElement).textContent = `${failed ? "FAIL" : "PASS"} ${total - failed}/${total}\n` + lines.join("\n");
  document.title = `${failed ? "FAIL" : "PASS"} ${total - failed}/${total}`;
}

void main();
