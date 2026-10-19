// Build the extension with Bun (no npm dependencies):
//   bun run /Users/bharath/Code/apps/Vani/extension/build.ts
// Writes dist-chrome/ (load unpacked in Chrome), dist-safari/ (input for the
// Safari web extension converter / Xcode project) and dist-test/ (DOM test page).
import { cp, mkdir, rm } from "node:fs/promises";
import { join } from "node:path";

const root = import.meta.dir;
const distChrome = join(root, "dist-chrome");
const distSafari = join(root, "dist-safari");
const distTest = join(root, "dist-test");

type Manifest = Record<string, unknown> & { permissions: string[] };

async function bundle(entries: string[], outdir: string): Promise<void> {
  const r = await Bun.build({ entrypoints: entries.map(e => join(root, e)), outdir, target: "browser", format: "iife", minify: false });
  if (!r.success) {
    for (const log of r.logs) console.error(log);
    throw new Error(`build failed for ${entries.join(", ")}`);
  }
}

async function readJson<T>(rel: string): Promise<T> {
  return (await Bun.file(join(root, rel)).json()) as T;
}

/** Bundle the scripts, copy static/ (minus the key file) and write this target's manifest. */
async function buildTarget(outdir: string, manifest: Manifest): Promise<void> {
  await rm(outdir, { recursive: true, force: true });
  await mkdir(outdir, { recursive: true });
  await bundle(["src/content.ts", "src/background.ts", "src/popup.ts"], outdir);
  await cp(join(root, "static"), outdir, { recursive: true });
  await rm(join(outdir, "chrome-key.json"), { force: true });
  await Bun.write(join(outdir, "manifest.json"), JSON.stringify(manifest, null, 2) + "\n");
}

const base = await readJson<Manifest>("static/manifest.json");
const { key } = await readJson<{ key: string; id: string }>("static/chrome-key.json");
if (typeof key !== "string" || key.length < 100) throw new Error("static/chrome-key.json has no key");
const permissions = [...base.permissions, "nativeMessaging"];

await rm(join(root, "dist"), { recursive: true, force: true }); // retired output directory
await buildTarget(distChrome, { ...base, permissions, key, background: { service_worker: "background.js" } });
await buildTarget(distSafari, { ...base, permissions, background: { scripts: ["background.js"] } });

await rm(distTest, { recursive: true, force: true });
await bundle(["test/dom-runner.ts"], distTest);
console.log(`built ${distChrome}, ${distSafari} and ${distTest}`);
