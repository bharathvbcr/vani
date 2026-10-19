// The popup's "Resume for this job" row: shows which PDF is bound to the active
// tab's job page, binds one (Choose PDF) and unbinds it (Remove). The bytes go to the
// Mac app, which keeps a copy for both browsers; the popup never gets them back.
import type { ToBackground } from "./messages";
import { MAX_RESUME_BYTES, parseResumeInfo, resumeKey, toBase64 } from "./resume";

export interface ResumeRowEls {
  row: HTMLElement;
  name: HTMLElement;
  pick: HTMLInputElement;
  remove: HTMLButtonElement;
}

type Send = <T>(msg: ToBackground) => Promise<T>;

const kb = (n: number) => `${Math.max(1, Math.round(n / 1024))} KB`;

export async function resumeRow(tabUrl: string | undefined, els: ResumeRowEls, send: Send): Promise<void> {
  if (!tabUrl || !resumeKey(tabUrl)) { els.row.hidden = true; return; }
  els.row.hidden = false;
  const url = tabUrl;

  async function show(): Promise<void> {
    let info;
    try { info = parseResumeInfo(await send<unknown>({ type: "resumeInfo", url })); } catch (e) { info = { error: String(e) }; }
    els.remove.hidden = true;
    if ("error" in info) {
      els.name.textContent = /reachable/.test(info.error)
        ? "needs the Vani Apply Mac app, which keeps the resume"
        : `could not be read: ${info.error}`;
    } else if (!info.found) {
      els.name.textContent = "none chosen; Fill leaves the resume box empty";
    } else {
      els.name.textContent = `${info.name} (${kb(info.bytes)}${info.pickedAt ? `, chosen ${info.pickedAt.slice(0, 10)}` : ""})`;
      els.remove.hidden = false;
    }
  }

  els.pick.onchange = async () => {
    const file = els.pick.files?.[0];
    els.pick.value = ""; // so the same file can be chosen again
    if (!file) return;
    if (file.size > MAX_RESUME_BYTES) { els.name.textContent = `${file.name} is over ${MAX_RESUME_BYTES / 1024} KB; not chosen`; return; }
    try {
      const pdf = toBase64(new Uint8Array(await file.arrayBuffer()));
      const r = await send<{ ok?: boolean; error?: string }>({ type: "resumePut", url, name: file.name, pdf });
      if (r?.error) { els.name.textContent = `not chosen: ${r.error}`; return; }
    } catch (e) {
      els.name.textContent = `not chosen: ${e instanceof Error ? e.message : String(e)}`;
      return;
    }
    await show();
  };

  els.remove.onclick = async () => {
    try {
      const r = await send<{ ok?: boolean; error?: string }>({ type: "resumeDelete", url });
      if (r?.error) { els.name.textContent = `not removed: ${r.error}`; return; }
    } catch (e) {
      els.name.textContent = `not removed: ${e instanceof Error ? e.message : String(e)}`;
      return;
    }
    await show();
  };

  await show();
}
