// Popup: act on the current page and manage saved answers.
import type { Answer, Bank } from "./types";
import { MAX_BANK_BYTES, bankBytes, exportBank } from "./bank";
import type { CaptureReply, FillReply, ScanReply, ToBackground } from "./messages";
import { sumCaptures, mergeFill, notAnswered, toFrames } from "./frames";
import { type Coverage, type FrameAnswer, type TabCoverage, coverageLine, tabCoverage } from "./coverage";
import { type Offer, offerSubject } from "./applied";
import { BASICS, BASICS_HOST, basicsFields, basicsValues } from "./basics";

const $ = <T extends HTMLElement>(sel: string) => document.querySelector(sel) as T;

function bg<T>(msg: ToBackground): Promise<T> {
  return chrome.runtime.sendMessage<T>(msg);
}

/** The frames of a tab whose content script registered with the background (frames.ts). */
async function registered(tabId: number): Promise<number[]> {
  const r = await bg<{ frames?: unknown }>({ type: "frames", tabId });
  return Array.isArray(r?.frames) ? r.frames.filter((f): f is number => typeof f === "number") : [];
}

/** Every frame's own coverage, added up (frames.ts asks each frame on its own; one that does not answer is counted). */
async function scanTab(): Promise<TabCoverage> {
  const r = await toFrames<ScanReply>({ type: "scan" }, { registered });
  const answers: FrameAnswer[] = [
    ...r.answered.map(a => ({ frameId: a.frameId, result: a.reply })),
    ...r.failed.map(f => ({ frameId: f.frameId, result: null })),
  ];
  return tabCoverage(answers);
}

let current: Bank | null = null;
let host = "";
let mode: "mac-app" | "browser" = "browser";
let note: string | null = null;

function status(t: string): void { $("#status").textContent = t; }

function preview(a: Answer): string {
  if (a.sensitivity === "self_id") return "••• (self-ID)";
  const v = a.value.join(", ");
  return v.length > 80 ? v.slice(0, 77) + "…" : v;
}

function render(): void {
  if (!current) return;
  const q = ($("#filter") as HTMLInputElement).value.toLowerCase();
  const rows = Object.values(current.answers)
    .filter(a => !q || a.key.includes(q) || a.value.join(" ").toLowerCase().includes(q))
    .sort((x, y) => y.updatedAt.localeCompare(x.updatedAt));
  const tbody = $("#answers");
  tbody.textContent = "";
  for (const a of rows) {
    const tr = document.createElement("tr");
    const k = document.createElement("td"); k.textContent = a.key; k.title = a.labels.join("\n");
    const v = document.createElement("td"); v.textContent = preview(a); if (a.review) v.className = "review";
    const n = document.createElement("td"); n.textContent = `${a.uses}/${a.fills}`;
    const act = document.createElement("td");
    const edit = document.createElement("button"); edit.textContent = "Edit";
    edit.onclick = async () => {
      const next = prompt(`New answer for "${a.key}" (comma-separated for several choices)`, a.value.join(", "));
      if (next === null) return;
      await bg({ type: "editAnswer", key: a.key, value: a.kind === "checkbox" || a.kind === "select" || a.kind === "radio" ? next.split(",").map(s => s.trim()) : [next] });
      await refresh();
    };
    const del = document.createElement("button"); del.textContent = "Delete";
    del.onclick = async () => { if (confirm(`Forget "${a.key}"?`)) { await bg({ type: "deleteAnswer", key: a.key }); await refresh(); } };
    act.append(edit, del);
    tr.append(k, v, n, act);
    tbody.append(tr);
  }
  $("#count").textContent = `${Object.keys(current.answers).length} saved answers`;
  $("#storage").textContent = (mode === "mac-app" ? "Stored by the Vani Apply Mac app." : "Stored in this browser only.") + (note ? ` ${note}` : "");
  const bytes = bankBytes(current);
  $("#size").textContent = `${Math.round(bytes / 1000)} KB of ${MAX_BANK_BYTES / 1000} KB used.`;
  renderBasics();
  ($("#selfid") as HTMLInputElement).checked = current.settings.captureSelfId;
  ($("#disabled") as HTMLInputElement).checked = current.settings.disabledHosts.includes(host);
}

// --- your basics ---------------------------------------------------------------------
// Typed once here, saved through the same capture as a hand-filled form, so the
// first application fills them too. A box you have typed into is not refilled
// from the bank until it is saved.

let basicsTyped = false;

function basicInput(id: string): HTMLInputElement | HTMLSelectElement {
  return $(`#basic-${id}`) as HTMLInputElement | HTMLSelectElement;
}

function option(value: string, text = value): HTMLOptionElement {
  const o = document.createElement("option");
  o.value = value;
  o.textContent = text;
  return o;
}

/** One row per basic, in its group: a dropdown where the answer is a choice, else a text box. */
function buildBasics(): void {
  for (const b of BASICS) {
    const label = document.createElement("label");
    label.htmlFor = `basic-${b.id}`;
    label.textContent = b.label;
    let box: HTMLInputElement | HTMLSelectElement;
    if (b.options) {
      box = document.createElement("select");
      box.append(option("", "—"), ...b.options.map(o => option(o)));
      box.onchange = () => { basicsTyped = true; };
    } else {
      box = document.createElement("input");
      box.type = b.kind === "email" || b.kind === "tel" || b.kind === "url" ? b.kind : "text";
      box.setAttribute("autocomplete", b.autocomplete || "off");
      box.oninput = () => { basicsTyped = true; };
    }
    box.id = `basic-${b.id}`;
    $(`#basic-${b.group}`).append(label, box);
  }
}

function renderBasics(): void {
  const v = basicsValues(current);
  if (!basicsTyped) {
    for (const b of BASICS) {
      const box = basicInput(b.id);
      const saved = v[b.id] ?? "";
      // An answer saved from a form's own wording ("No, I will not") is shown as it is, not lost.
      if (box instanceof HTMLSelectElement && saved && !Array.from(box.options).some(o => o.value === saved)) box.append(option(saved));
      box.value = saved;
    }
  }
  $("#basic-selfid-box").hidden = !current?.settings.captureSelfId;
  // Open while the bank still lacks the basics every form asks for.
  if (!v.first || !v.email) ($("#basics") as HTMLDetailsElement).open = true;
}

async function saveBasics(): Promise<void> {
  const out = $("#basic-status");
  const values: Record<string, string> = {};
  for (const b of BASICS) values[b.id] = basicInput(b.id).value;
  const fields = basicsFields(values, basicsValues(current));
  if (fields.length === 0) { out.textContent = "Nothing new to save."; return; }
  try {
    const r = await bg<CaptureReply | { error?: string } | undefined>({ type: "capture", host: BASICS_HOST, fields });
    if (!r || !("added" in r)) throw new Error((r && "error" in r && r.error) || "no reply from the extension");
    basicsTyped = false;
    out.textContent = `Saved ${r.added + r.updated}` + (r.skippedSecret ? `; ${r.skippedSecret} refused as sensitive` : "") + ".";
    await refresh();
  } catch (e) {
    out.textContent = `Could not save: ${e instanceof Error ? e.message : String(e)}`;
  }
}

async function refresh(): Promise<void> {
  const r = await bg<{ bank?: Bank; mode?: "mac-app" | "browser"; note?: string | null; error?: string }>({ type: "getBank" });
  if (r.error || !r.bank) { status(`Could not read saved answers: ${r.error ?? "unknown"}`); return; }
  current = r.bank;
  mode = r.mode ?? "browser";
  note = r.note ?? null;
  render();
}

/** Per-page breakdown and the questions with no saved answer; page text is set as text, never markup. */
function renderCoverage(c: Coverage): void {
  const list = $("#unknown");
  list.textContent = "";
  for (const label of c.unknownLabels) {
    const li = document.createElement("li");
    li.textContent = label;
    list.append(li);
  }
  if (c.unknown > c.unknownLabels.length) {
    const li = document.createElement("li");
    li.textContent = `+${c.unknown - c.unknownLabels.length} more not listed`;
    list.append(li);
  }
  for (const label of c.manualLabels) {
    const li = document.createElement("li");
    li.textContent = `${label} (do by hand)`;
    list.append(li);
  }
  $("#coverage").hidden = c.unknown === 0 && c.manual === 0;
}

/** Which frame found what, when the form is not all in the top frame. Page text is set as text. */
function renderFrames(t: TabCoverage): void {
  const list = $("#frames");
  list.textContent = "";
  const unanswered = t.asked - t.answered;
  if (t.frames.length > 1) {
    for (const f of t.frames) {
      const li = document.createElement("li");
      li.textContent = `${f.frameId === 0 ? "This page" : "Frame"} (${f.host || "?"}): ${coverageLine(f.coverage)}`;
      list.append(li);
    }
  }
  if (unanswered > 0 && t.frames.length > 0) {
    const li = document.createElement("li");
    li.textContent = `${unanswered} of ${t.asked} frames running Vani did not answer in time; their fields are not counted.`;
    list.append(li);
  }
  $("#framebox").hidden = list.childElementCount === 0;
}

/**
 * A page Vani tracks as a job: offer to mark it Applied, on any host (the popup is the one place the
 * extension is allowed to look at the page's URL whether or not a content script runs there). Also shows
 * the question a Submit left for this tab. Posting text is set as text; nothing is marked until the click.
 */
async function appliedRow(tabId: number, url: string | undefined): Promise<void> {
  const row = $("#applied");
  const text = $("#applied-text");
  const yes = $("#applied-yes") as HTMLButtonElement;
  const no = $("#applied-no") as HTMLButtonElement;
  let offer = (await bg<{ offer?: Offer | null }>({ type: "appliedPending", tabId }).catch(() => undefined))?.offer ?? null;
  if (!offer && url) {
    const r = await bg<{ offer?: Offer | null }>({ type: "appliedSignal", url, tabId }).catch(() => undefined);
    offer = r?.offer ?? null;
  }
  if (!offer) return;
  const subject = offerSubject(offer);
  text.textContent = `Tracked job on this page: ${subject}.`;
  yes.hidden = no.hidden = false;
  yes.disabled = no.disabled = false;
  row.hidden = false;
  yes.onclick = async () => {
    yes.disabled = no.disabled = true;
    const r: { ok?: boolean; error?: string } | undefined = await bg<{ ok?: boolean; error?: string }>({ type: "appliedMark", tabId })
      .catch((e: unknown) => ({ error: e instanceof Error ? e.message : String(e) }));
    if (r?.ok) {
      text.textContent = `Marked ${subject} as Applied.`;
      yes.hidden = no.hidden = true;
    } else {
      text.textContent = `Could not mark it Applied: ${r?.error ?? "no reply"}`;
      yes.disabled = no.disabled = false;
    }
  };
  no.onclick = async () => {
    await bg({ type: "appliedDismiss", tabId }).catch(() => undefined);
    row.hidden = true;
  };
}

async function init(): Promise<void> {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  host = tab?.url ? new URL(tab.url).hostname : "";
  $("#host").textContent = host || "(no page)";
  if (tab?.id !== undefined) void appliedRow(tab.id, tab.url);
  await refresh();
  try {
    const t = await scanTab();
    renderFrames(t);
    if (t.disabled) status("Disabled on this site.");
    else if (t.frames.length === 0) status(`No fields found in ${t.answered} of ${t.asked} frames that answered.`);
    else {
      status(coverageLine(t.total) + (t.frames.length > 1 ? ` Across ${t.frames.length} frames.` : ""));
      renderCoverage(t.total);
    }
  } catch (e) {
    status(`Cannot read this page: ${e instanceof Error ? e.message : String(e)}`);
  }
}

$("#fill").onclick = async () => {
  try {
    const r = await toFrames<FillReply>({ type: "fill", overwrite: ($("#overwrite") as HTMLInputElement).checked, typeahead: ($("#typeahead") as HTMLInputElement).checked }, { registered });
    const outcomes = mergeFill(r);
    const f = outcomes.filter(o => o.filled);
    const manual = outcomes.filter(o => o.reason === "manual").length;
    status(`Filled ${f.length} (${f.filter(o => o.review).length} to review)` + (manual ? `, ${manual} to do by hand` : "") +
      (r.answered.length > 1 ? ` across ${r.answered.length} frames` : "") + ". Check the page, then submit yourself." + notAnswered(r));
    await refresh();
  } catch (e) { status(String(e)); }
};
$("#save").onclick = async () => {
  try {
    const r = await toFrames<CaptureReply>({ type: "capture" }, { registered });
    const c = sumCaptures(r);
    status(`Saved ${c.added + c.updated} answers` + (c.unchanged ? ` (${c.unchanged} already saved)` : "") + (c.skippedSecret ? `; ${c.skippedSecret} sensitive fields not saved.` : ".") + notAnswered(r));
    await refresh();
  } catch (e) { status(String(e)); }
};
$("#basic-save").onclick = () => { void saveBasics(); };
$("#filter").oninput = render;
$("#selfid").onchange = async () => {
  await bg({ type: "setSettings", settings: { captureSelfId: ($("#selfid") as HTMLInputElement).checked } });
  await refresh();
};
$("#disabled").onchange = async () => {
  if (!current || !host) return;
  const on = ($("#disabled") as HTMLInputElement).checked;
  const hosts = current.settings.disabledHosts.filter(h => h !== host);
  if (on) hosts.push(host);
  await bg({ type: "setSettings", settings: { disabledHosts: hosts } });
  await refresh();
};
$("#export").onclick = () => {
  if (!current) return;
  const url = URL.createObjectURL(new Blob([exportBank(current)], { type: "application/json" }));
  const a = document.createElement("a");
  a.href = url; a.download = `vani-answers-${new Date().toISOString().slice(0, 10)}.json`; a.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
};
$("#import").onchange = async ev => {
  const input = ev.target as HTMLInputElement;
  const file = input.files?.[0];
  // Cleared at once, so choosing the same file again (after Cancel, a refusal or an import) fires change again.
  input.value = "";
  if (!file) return;
  if (!confirm("Replace all saved answers with this file?")) return;
  try {
    const r = await bg<{ ok?: boolean; answers?: number; droppedSecret?: number; error?: string }>({ type: "replaceBank", json: await file.text() });
    status(r.error ? `Import refused: ${r.error}` : `Imported ${r.answers} answers` + (r.droppedSecret ? `; ${r.droppedSecret} sensitive entries dropped.` : "."));
    await refresh();
  } catch (e) { status(`Import failed: ${e instanceof Error ? e.message : String(e)}`); }
};

buildBasics();
void init();
