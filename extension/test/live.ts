// Live check in a real Chrome (test only; loopback only; a throwaway profile):
//   bun run /Users/bharath/Code/apps/Vani/extension/test/live.ts [--chrome PATH]
//
// What `bun test` cannot show, because its chrome is a fake:
//   1. A form in a cross-origin iframe on a host the manifest matches (a Greenhouse
//      embed on a company page) is found by frames.ts's scripting.executeScript
//      probe, and Save, Fill and Scan from the popup's code path count its fields.
//   2. With a content script in the top frame too, a whole-tab message is answered
//      by the first frame (the old popup's bug) while toFrames counts both.
//   3. The popup's import input fires again for the same file (after Cancel too).
//   4. test/dom.html passes.
//
// Chrome is driven over its DevTools protocol on a pipe (branded Chrome ignores
// --load-extension; Extensions.loadUnpacked needs --remote-debugging-pipe and
// --enable-unsafe-extension-debugging). No dependency. The extension under test is
// dist-chrome with three changes: the background bundle also exposes toFrames
// (test/live-bg.ts); no `key` and no nativeMessaging, so it can never reach the
// Vani Apply Mac app or its saved answers (it uses this profile's storage only);
// and a host permission for http://127.0.0.1, standing in for the activeTab grant
// the popup gets on the company page (activeTab cannot be granted by automation).
// boards.greenhouse.io is mapped to 127.0.0.1 with a self-signed certificate.
// Test values only: no personal data.
import { type ChildProcess, spawn, spawnSync } from "node:child_process";
import { cp, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, normalize } from "node:path";
import type { Readable, Writable } from "node:stream";
import { emptyBank } from "../src/types";
import { exportBank, mergeCapture } from "../src/bank";

const root = normalize(join(import.meta.dir, ".."));
const argv = process.argv.slice(2);
const chromeArg = argv.indexOf("--chrome");
const chromePath = (chromeArg >= 0 ? argv[chromeArg + 1] : undefined) ?? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const STEP_MS = 15_000;
const EXT_NAME = "Vani Apply (live check)";
const VALUES = { fn: "Test", ln: "Applicant", em: "applicant@example.invalid", ph: "555-0100" };

type Json = Record<string, unknown>;
interface Check { name: string; ok: boolean; detail: string }
const checks: Check[] = [];
function check(name: string, ok: boolean, detail: unknown): void {
  checks.push({ name, ok, detail: typeof detail === "string" ? detail : JSON.stringify(detail) });
  console.log(`${ok ? "ok  " : "FAIL"} ${name}: ${ok ? checks.at(-1)!.detail.slice(0, 600) : checks.at(-1)!.detail}`);
}

async function bounded<T>(p: Promise<T>, ms: number, what: string): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([p, new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error(`${what}: timed out after ${ms} ms`)), ms); })]);
  } finally {
    clearTimeout(timer);
  }
}

async function until<T>(fn: () => Promise<T | undefined | null | false>, ms: number, what: string): Promise<T> {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn().catch(() => undefined);
    if (v) return v;
    if (Date.now() > end) throw new Error(`${what}: not reached in ${ms} ms`);
    await Bun.sleep(100);
  }
}

/** The DevTools protocol over --remote-debugging-pipe: NUL-separated JSON, fd 3 in, fd 4 out. */
class Cdp {
  private next = 0;
  private readonly pending = new Map<number, { resolve: (v: Json) => void; reject: (e: Error) => void }>();
  private readonly events: Json[] = [];
  private buf = "";
  constructor(private readonly w: Writable, r: Readable) {
    r.setEncoding("utf8");
    r.on("data", (chunk: string) => {
      this.buf += chunk;
      for (let i = this.buf.indexOf("\0"); i >= 0; i = this.buf.indexOf("\0")) {
        const msg = JSON.parse(this.buf.slice(0, i)) as Json;
        this.buf = this.buf.slice(i + 1);
        if (typeof msg.id === "number") {
          const p = this.pending.get(msg.id);
          this.pending.delete(msg.id);
          if (msg.error) p?.reject(new Error(JSON.stringify(msg.error)));
          else p?.resolve((msg.result ?? {}) as Json);
        } else {
          this.events.push(msg);
        }
      }
    });
  }
  send(method: string, params: Json = {}, sessionId?: string): Promise<Json> {
    const id = ++this.next;
    const p = new Promise<Json>((resolve, reject) => this.pending.set(id, { resolve, reject }));
    this.w.write(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }) + "\0");
    return bounded(p, STEP_MS, method);
  }
  event(pred: (m: Json) => boolean, ms: number, what: string): Promise<Json> {
    return until(async () => this.events.find(pred), ms, what);
  }
  seen(pred: (m: Json) => boolean): Json[] {
    return this.events.filter(pred);
  }
  /** Evaluate in a session's default context (or `contextId`); throws on a page exception. */
  async eval<T>(session: string, expression: string, contextId?: number): Promise<T> {
    const r = await this.send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true, ...(contextId ? { contextId } : {}) }, session);
    if (r.exceptionDetails) throw new Error(JSON.stringify(r.exceptionDetails).slice(0, 500));
    return (r.result as { value: T }).value;
  }
}

function sh(cmd: string, args: string[]): void {
  const r = spawnSync(cmd, args, { stdio: ["ignore", "ignore", "pipe"] });
  if (r.status !== 0) throw new Error(`${cmd} failed: ${r.stderr?.toString().slice(0, 500)}`);
}

const FORM = `<!doctype html><title>Apply</title><form id="app" action="#">
<label for="fn">First Name</label><input id="fn" name="first_name">
<label for="ln">Last Name</label><input id="ln" name="last_name">
<label for="em">Email</label><input id="em" name="email" type="email">
<label for="ph">Phone</label><input id="ph" name="phone" type="tel">
<label for="why">Why do you want to work at Acme?</label><textarea id="why" name="why"></textarea>
</form>`;

const setValues = (v: Record<string, string>) => `(() => {
  for (const [id, value] of Object.entries(${JSON.stringify(v)})) {
    const el = document.getElementById(id);
    el.focus(); el.value = value;
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new Event("change", { bubbles: true }));
    el.blur();
  }
  return true;
})()`;
const readValues = `({ fn: fn.value, ln: ln.value, em: em.value, ph: ph.value, why: why.value })`;
const EMPTY = { fn: "", ln: "", em: "", ph: "" };

async function main(): Promise<void> {
  const tmp = await mkdtemp(join(tmpdir(), "vani-live-"));
  let chrome: ChildProcess | undefined;
  const servers: Array<{ stop(force?: boolean): void }> = [];
  try {
    // --- the extension under test ---
    sh(process.execPath, ["run", join(root, "build.ts")]);
    const ext = join(tmp, "ext");
    await cp(join(root, "dist-chrome"), ext, { recursive: true });
    const bg = await Bun.build({ entrypoints: [join(root, "test/live-bg.ts")], outdir: ext, naming: "background.js", target: "browser", format: "iife" });
    if (!bg.success) throw new Error(`live-bg build failed: ${bg.logs.join("\n")}`);
    const manifest = JSON.parse(await readFile(join(ext, "manifest.json"), "utf8")) as Json & { permissions: string[] };
    delete manifest.key;
    manifest.name = EXT_NAME;
    manifest.permissions = manifest.permissions.filter(p => p !== "nativeMessaging");
    manifest.host_permissions = ["http://127.0.0.1/*"];
    await writeFile(join(ext, "manifest.json"), JSON.stringify(manifest, null, 2));

    // --- pages: a company page on 127.0.0.1 embedding a "Greenhouse" form over https ---
    sh("openssl", ["req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", "/CN=boards.greenhouse.io",
      "-keyout", join(tmp, "key.pem"), "-out", join(tmp, "cert.pem")]);
    const gh = Bun.serve({
      hostname: "127.0.0.1", port: 0,
      tls: { cert: Bun.file(join(tmp, "cert.pem")), key: Bun.file(join(tmp, "key.pem")) },
      fetch: () => new Response(FORM, { headers: { "content-type": "text/html" } }),
    });
    servers.push(gh);
    const frameUrl = `https://boards.greenhouse.io:${gh.port}/embed/job_app?for=acme&token=1`;
    const site = Bun.serve({
      hostname: "127.0.0.1", port: 0,
      async fetch(req) {
        const path = decodeURIComponent(new URL(req.url).pathname);
        if (path === "/company.html") {
          return new Response(`<!doctype html><title>Acme careers</title><h1>Acme careers</h1><iframe src="${frameUrl}" width="800" height="600"></iframe>`, { headers: { "content-type": "text/html" } });
        }
        const file = normalize(join(root, path)); // test/dom.html and dist-test/, as test/serve.ts serves them
        if (!file.startsWith(root + "/")) return new Response("forbidden", { status: 403 });
        const f = Bun.file(file);
        return (await f.exists()) ? new Response(f) : new Response("not found", { status: 404 });
      },
    });
    servers.push(site);
    const companyUrl = `http://127.0.0.1:${site.port}/company.html`;

    // --- Chrome ---
    chrome = spawn(chromePath, [
      "--headless=new", "--remote-debugging-pipe", "--enable-unsafe-extension-debugging",
      `--user-data-dir=${join(tmp, "profile")}`, "--no-first-run", "--no-default-browser-check", "--disable-sync",
      "--site-per-process", "--ignore-certificate-errors", "--host-resolver-rules=MAP boards.greenhouse.io 127.0.0.1",
      "about:blank",
    ], { stdio: ["ignore", "ignore", "pipe", "pipe", "pipe"] });
    let stderr = "";
    chrome.stderr!.on("data", (d: Buffer) => { stderr = (stderr + d.toString()).slice(-4000); });
    const cdp = new Cdp(chrome.stdio[3] as Writable, chrome.stdio[4] as Readable);
    const version = await cdp.send("Browser.getVersion").catch(e => { throw new Error(`Chrome did not answer on the pipe: ${e}\n${stderr}`); });
    console.log(`Chrome ${String(version.product)}, pid ${chrome.pid}`);
    await cdp.send("Target.setDiscoverTargets", { discover: true });

    const { id: extId } = await cdp.send("Extensions.loadUnpacked", { path: ext }) as { id: string };
    /** Attach to the extension's running service worker (a new one after a stop). */
    const attachWorker = async (): Promise<{ targetId: string; session: string }> => {
      const t = await until(async () => {
        const { targetInfos } = await cdp.send("Target.getTargets") as { targetInfos: Array<{ targetId: string; type: string; url: string }> };
        return targetInfos.find(i => i.type === "service_worker" && i.url.startsWith(`chrome-extension://${extId}/`));
      }, STEP_MS, "extension service worker");
      const { sessionId } = await cdp.send("Target.attachToTarget", { targetId: t.targetId, flatten: true }) as { sessionId: string };
      await until(() => cdp.eval<boolean>(sessionId, "typeof vaniLive === 'object'"), STEP_MS, "vaniLive on the worker");
      return { targetId: t.targetId, session: sessionId };
    };
    let worker = await attachWorker();
    let sw = worker.session;

    // The company page, with every frame auto-attached.
    const { targetInfos } = await cdp.send("Target.getTargets") as { targetInfos: Array<{ targetId: string; type: string }> };
    const pageTarget = targetInfos.find(t => t.type === "page")!;
    const { sessionId: pg } = await cdp.send("Target.attachToTarget", { targetId: pageTarget.targetId, flatten: true }) as { sessionId: string };
    await cdp.send("Runtime.enable", {}, pg);
    await cdp.send("Target.setAutoAttach", { autoAttach: true, waitForDebuggerOnStart: false, flatten: true }, pg);
    await cdp.send("Page.enable", {}, pg);
    await cdp.send("Page.navigate", { url: companyUrl }, pg);
    // An out-of-process iframe attaches before its navigation commits, so its URL is empty here: match the
    // child of this page, then wait for the document it loads.
    const attached = await cdp.event(m => m.method === "Target.attachedToTarget" && m.sessionId === pg &&
      ((m.params as Json).targetInfo as Json).type === "iframe", STEP_MS, "the cross-origin iframe as its own (out-of-process) target");
    const fr = (attached.params as Json).sessionId as string;
    await cdp.send("Runtime.enable", {}, fr);
    const frameHref = await until(() => cdp.eval<string>(fr, "document.readyState === 'complete' && location.href"), STEP_MS, "the iframe's document");
    check("the iframe is the https Greenhouse form", frameHref === frameUrl, frameHref);
    const isVani = (s: string) => (m: Json) => m.method === "Runtime.executionContextCreated" && m.sessionId === s &&
      ((m.params as Json).context as Json).name === EXT_NAME;
    const frameCtx = await cdp.event(isVani(fr), STEP_MS, "the content script (from the manifest match) in the iframe");
    const ctxId = ((frameCtx.params as Json).context as Json).id as number;
    check("manifest content script runs in the cross-origin Greenhouse iframe", await cdp.eval<boolean>(fr, "window.__vaniApply === true", ctxId), "window.__vaniApply in its isolated world");
    await until(() => cdp.eval<boolean>(pg, "document.readyState === 'complete'"), STEP_MS, "company page loaded");
    await Bun.sleep(500);
    check("no content script in the unmatched top frame", cdp.seen(isVani(pg)).length === 0, `${cdp.seen(isVani(pg)).length} Vani contexts in the top frame`);

    // 1. The probe toFrames uses lists the iframe.
    const probe = await cdp.eval<Json>(sw, `(async () => {
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
      const rs = await chrome.scripting.executeScript({ target: { tabId: tab.id, allFrames: true }, func: () => window.__vaniApply === true });
      return { url: tab.url, rs };
    })()`);
    // Chrome 155: the probe lists only frame 0. A frame the content script reaches through a manifest
    // match is not scriptable by executeScript, which is why content scripts register (frames.ts).
    console.log(`info executeScript probe lists frames ${JSON.stringify((probe.rs as Array<{ frameId: number }>).map(r => r.frameId))}`);

    // 2. Save, then Fill, then Scan through toFrames.
    await cdp.eval(fr, setValues(VALUES));
    const save = await cdp.eval<Json>(sw, `vaniLive.toFrames({ type: "capture" }).then(r => ({ r, sum: vaniLive.sumCaptures(r) }))`);
    const sum = save.sum as { added: number };
    const saveFan = save.r as { answered: Array<{ frameId: number }>; failed: unknown[]; asked: number };
    check("Save counts the iframe's answers", sum.added === 4 && saveFan.failed.length === 0 && saveFan.answered.every(a => a.frameId !== 0), save);

    await cdp.eval(fr, setValues(EMPTY));
    const fill = await cdp.eval<Json>(sw, `vaniLive.toFrames({ type: "fill" }).then(r => ({ r, outcomes: vaniLive.mergeFill(r) }))`);
    const filled = (fill.outcomes as Array<{ label: string; filled: boolean }>).filter(o => o.filled).map(o => o.label).sort();
    check("Fill reports the iframe's filled fields", JSON.stringify(filled) === JSON.stringify(["Email", "First Name", "Last Name", "Phone"]), fill);
    const after = await cdp.eval<Json>(fr, readValues);
    check("the iframe's fields hold the saved values", after.fn === VALUES.fn && after.ln === VALUES.ln && after.em === VALUES.em && after.ph === VALUES.ph && after.why === "", after);

    const scan = await cdp.eval<Json>(sw, `vaniLive.toFrames({ type: "scan" })`);
    const scanFrames = scan.answered as Array<{ frameId: number; reply: { fields: number; matched: number } }>;
    check("Scan counts the iframe's fields", scanFrames.length === 1 && scanFrames[0]!.reply.fields === 5 && scanFrames[0]!.reply.matched === 4, scan);

    // 3. A content script in the top frame as well (the popup injects one on a non-ATS page).
    await cdp.eval(sw, `(async () => {
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
      await chrome.scripting.executeScript({ target: { tabId: tab.id, frameIds: [0] }, files: ["content.js"] });
      return true;
    })()`);
    await cdp.event(isVani(pg), STEP_MS, "content script injected into the top frame");
    await cdp.eval(fr, setValues(EMPTY));
    const broadcast = await cdp.eval<Json>(sw, `(async () => {
      const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
      return chrome.tabs.sendMessage(tab.id, { type: "fill" });
    })()`);
    const firstReply = (broadcast.outcomes as unknown[]).length;
    console.log(`info whole-tab message (the old popup): first reply has ${firstReply} outcomes (the top frame has no fields)`);
    await cdp.eval(fr, setValues(EMPTY));
    const both = await cdp.eval<Json>(sw, `vaniLive.toFrames({ type: "fill" }).then(r => ({ r, outcomes: vaniLive.mergeFill(r) }))`);
    const bothFan = both.r as { asked: number; answered: Array<{ frameId: number }>; failed: unknown[] };
    const bothFilled = (both.outcomes as Array<{ filled: boolean }>).filter(o => o.filled).length;
    check("with scripts in both frames, toFrames asks both and counts the iframe", bothFan.asked === 2 && bothFan.answered.length === 2 && bothFan.failed.length === 0 && bothFilled === 4,
      { asked: bothFan.asked, answered: bothFan.answered.map(a => a.frameId), failed: bothFan.failed, filled: bothFilled });

    // The frame list outlives the worker (chrome.storage.session): stop it; the iframe's content script asking
    // for its own tab's frames wakes a new worker, which still lists the iframe.
    await cdp.send("Target.detachFromTarget", { sessionId: sw }); // an attached debugger keeps the worker alive
    await cdp.send("ServiceWorker.enable", {}, pg);
    await cdp.send("ServiceWorker.stopAllWorkers", {}, pg);
    await cdp.event(m => m.method === "Target.targetDestroyed" && (m.params as Json).targetId === worker.targetId, STEP_MS, "the worker stopped");
    const kept = await cdp.eval<Json>(fr, `chrome.runtime.sendMessage({ type: "frames" })`, ctxId);
    check("registered frames survive the worker being stopped", Array.isArray(kept.frames) && (kept.frames as number[]).includes(5), kept);
    worker = await attachWorker();
    sw = worker.session;

    // A frame that went away is dropped as gone, not reported as one that did not answer.
    await cdp.eval(pg, `document.querySelector("iframe").remove(); true`);
    const afterRemove = await cdp.eval<Json>(sw, `vaniLive.toFrames({ type: "scan" })`);
    check("a removed iframe is gone, not a frame that did not answer", JSON.stringify(afterRemove.gone) === "[5]" && afterRemove.asked === 1 && (afterRemove.failed as unknown[]).length === 0,
      { asked: afterRemove.asked, gone: afterRemove.gone, failed: afterRemove.failed });

    // 4. The popup's import input: Cancel, then the same file again.
    const bank = exportBank(mergeCapture(emptyBank(), [{ kind: "text", label: "City", name: "city", autocomplete: "", automationId: "", options: [], required: false, value: ["Testville"] }], "x.example", new Date()).bank);
    const bankFile = join(tmp, "answers.json");
    await writeFile(bankFile, bank);
    const { targetId: popupTarget } = await cdp.send("Target.createTarget", { url: `chrome-extension://${extId}/popup.html` }) as { targetId: string };
    const { sessionId: pp } = await cdp.send("Target.attachToTarget", { targetId: popupTarget, flatten: true }) as { sessionId: string };
    await until(() => cdp.eval<boolean>(pp, "document.readyState === 'complete' && !!document.querySelector('#import')"), STEP_MS, "popup loaded");
    await cdp.eval(pp, `window.__changes = 0; window.confirm = () => false; document.querySelector("#import").addEventListener("change", () => window.__changes++); true`);
    const { root: doc } = await cdp.send("DOM.getDocument", {}, pp) as { root: { nodeId: number } };
    const { nodeId } = await cdp.send("DOM.querySelector", { nodeId: doc.nodeId, selector: "#import" }, pp) as { nodeId: number };
    await cdp.send("DOM.setFileInputFiles", { nodeId, files: [bankFile] }, pp);
    const cancelled = await until(() => cdp.eval<Json>(pp, `window.__changes === 1 && { changes: __changes, value: document.querySelector("#import").value }`), STEP_MS, "first change");
    check("import: Cancel leaves the input empty", cancelled.value === "", cancelled);
    await cdp.eval(pp, "window.confirm = () => true; true");
    await cdp.send("DOM.setFileInputFiles", { nodeId, files: [bankFile] }, pp);
    const imported = await until(() => cdp.eval<Json>(pp, `window.__changes === 2 && document.querySelector("#status").textContent.startsWith("Import") && { changes: __changes, value: document.querySelector("#import").value, status: document.querySelector("#status").textContent }`), STEP_MS, "the same file chosen again fires change");
    check("import: the same file chosen again after Cancel imports", imported.status === "Imported 1 answers." && imported.value === "", imported);
    await cdp.send("DOM.setFileInputFiles", { nodeId, files: [bankFile] }, pp);
    const again = await until(() => cdp.eval<Json>(pp, `window.__changes === 3 && { changes: __changes, value: document.querySelector("#import").value }`), STEP_MS, "the same file chosen a third time fires change");
    check("import: the same file again after an import fires change again", again.value === "", again);
    // 5. The DOM test page.
    await cdp.send("Page.navigate", { url: `http://127.0.0.1:${site.port}/test/dom.html` }, pg);
    const title = await until(() => cdp.eval<string>(pg, `/^(PASS|FAIL)/.test(document.title) && document.title`), 60_000, "test/dom.html result");
    const results = await cdp.eval<string>(pg, `document.querySelector("#results").textContent`);
    check("test/dom.html", title.startsWith("PASS"), title + (title.startsWith("PASS") ? "" : `\n${results}`));
  } finally {
    if (chrome?.pid !== undefined && chrome.exitCode === null) {
      chrome.kill("SIGTERM");
      await bounded(new Promise(r => chrome!.once("exit", r)), 5000, "Chrome exit").catch(() => chrome!.kill("SIGKILL"));
    }
    for (const s of servers) s.stop(true);
    await rm(tmp, { recursive: true, force: true });
  }
}

try {
  await main();
} catch (e) {
  check("live run", false, e instanceof Error ? e.message : String(e));
}
const failed = checks.filter(c => !c.ok).length;
console.log(failed ? `FAIL ${failed} of ${checks.length}` : `PASS ${checks.length}/${checks.length}`);
process.exit(failed ? 1 : 0);
