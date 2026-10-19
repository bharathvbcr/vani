// Popup: act on the current page and manage saved answers.
import type { Answer, Bank } from "./types";
import { MAX_BANK_BYTES, bankBytes, exportBank } from "./bank";
import type { ScanReply, ToBackground, ToContent } from "./messages";
import { type Coverage, type FrameAnswer, type TabCoverage, coverageLine, tabCoverage } from "./coverage";

const $ = <T extends HTMLElement>(sel: string) => document.querySelector(sel) as T;

function bg<T>(msg: ToBackground): Promise<T> {
  return chrome.runtime.sendMessage<T>(msg);
}

async function toTab<T>(msg: ToContent): Promise<T> {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (tab?.id === undefined) throw new Error("no active tab");
  try {
    return await chrome.tabs.sendMessage<T>(tab.id, msg);
  } catch {
    // Not an ATS host the content script runs on by default: inject it on
    // this page only (activeTab grants this tab, for this click).
    await chrome.scripting.executeScript({ target: { tabId: tab.id, allFrames: true }, files: ["content.js"] });
    return chrome.tabs.sendMessage<T>(tab.id, msg);
  }
}

/**
 * Ask every frame of the tab for its own coverage and add them up. A message to
 * the tab is answered by whichever frame replies first, so a form inside a
 * frame was reported as 0 fields; scripting.executeScript with allFrames
 * returns one result per frame instead (activeTab covers it, no new
 * permission). The content script is injected first where it is not running.
 */
async function scanTab(): Promise<TabCoverage> {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  if (tab?.id === undefined) throw new Error("no active tab");
  const target = { tabId: tab.id, allFrames: true };
  const ask = () => chrome.scripting.executeScript<ScanReply | null>({ target, func: () => (window.__vaniScan ? window.__vaniScan() : null) });
  const answers = (rs: Array<{ frameId: number; result?: ScanReply | null }>): FrameAnswer[] => rs.map(r => ({ frameId: r.frameId, result: r.result }));
  let rs = await ask().catch((): Array<{ frameId: number; result?: ScanReply | null }> => []);
  if (!rs.some(r => r.result)) {
    await chrome.scripting.executeScript({ target, files: ["content.js"] });
    rs = await ask();
  }
  return tabCoverage(answers(rs));
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
  ($("#selfid") as HTMLInputElement).checked = current.settings.captureSelfId;
  ($("#disabled") as HTMLInputElement).checked = current.settings.disabledHosts.includes(host);
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
    li.textContent = `${unanswered} of ${t.asked} frames did not answer (nothing to read, or Vani cannot enter them).`;
    list.append(li);
  }
  $("#framebox").hidden = list.childElementCount === 0;
}

async function init(): Promise<void> {
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  host = tab?.url ? new URL(tab.url).hostname : "";
  $("#host").textContent = host || "(no page)";
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
    const r = await toTab<{ outcomes: Array<{ filled: boolean; review?: boolean; reason?: string }> }>({ type: "fill", overwrite: ($("#overwrite") as HTMLInputElement).checked, typeahead: ($("#typeahead") as HTMLInputElement).checked });
    const f = r.outcomes.filter(o => o.filled);
    const manual = r.outcomes.filter(o => o.reason === "manual").length;
    status(`Filled ${f.length} (${f.filter(o => o.review).length} to review)` + (manual ? `, ${manual} to do by hand` : "") + ". Check the page, then submit yourself.");
    await refresh();
  } catch (e) { status(String(e)); }
};
$("#save").onclick = async () => {
  try {
    const r = await toTab<{ added?: number; updated?: number; skippedSecret?: number; unchanged?: number }>({ type: "capture" });
    status(`Saved ${(r.added ?? 0) + (r.updated ?? 0)} answers` + (r.unchanged ? ` (${r.unchanged} already saved)` : "") + (r.skippedSecret ? `; ${r.skippedSecret} sensitive fields not saved.` : "."));
    await refresh();
  } catch (e) { status(String(e)); }
};
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
  const file = (ev.target as HTMLInputElement).files?.[0];
  if (!file) return;
  if (!confirm("Replace all saved answers with this file?")) return;
  const r = await bg<{ ok?: boolean; answers?: number; droppedSecret?: number; error?: string }>({ type: "replaceBank", json: await file.text() });
  status(r.error ? `Import refused: ${r.error}` : `Imported ${r.answers} answers` + (r.droppedSecret ? `; ${r.droppedSecret} sensitive entries dropped.` : "."));
  await refresh();
};

void init();
