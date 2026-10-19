// Static server for the DOM test page (test only; loopback only):
//   bun run /Users/bharath/Code/apps/Vani/extension/test/serve.ts
// then open http://127.0.0.1:4789/test/dom.html
import { join, normalize } from "node:path";

const root = normalize(join(import.meta.dir, ".."));
Bun.serve({
  hostname: "127.0.0.1",
  port: 4789,
  async fetch(req) {
    const path = normalize(join(root, decodeURIComponent(new URL(req.url).pathname)));
    if (!path.startsWith(root + "/")) return new Response("forbidden", { status: 403 });
    const f = Bun.file(path);
    return (await f.exists()) ? new Response(f) : new Response("not found", { status: 404 });
  },
});
console.log("serving", root, "on http://127.0.0.1:4789/test/dom.html");
