import { describe, expect, test } from "bun:test";
import { MAX_DETAIL_DEPTH, MAX_DETAIL_NODES, extractDetail } from "../src/detail";
import { MAX_SNIPPET } from "../src/ingest";

const ELEMENT = 1;
const TEXT = 3;

class N {
  parent: N | null = null;
  childNodes: N[] = [];
  constructor(
    readonly nodeType: number,
    readonly localName: string,
    readonly attrs: Record<string, string>,
    readonly data: string,
  ) {}
  get tagName(): string { return this.localName.toUpperCase(); }
  get children(): N[] { return this.childNodes.filter(c => c.nodeType === ELEMENT); }
  get parentElement(): N | null { return this.parent; }
  get textContent(): string {
    return this.nodeType === TEXT ? this.data : this.childNodes.map(c => c.textContent).join("");
  }
  getAttribute(name: string): string | null { return this.attrs[name.toLowerCase()] ?? null; }
  querySelectorAll(sel: string): N[] {
    const out: N[] = [];
    const walk = (n: N): void => {
      for (const c of n.childNodes) {
        if (c.nodeType !== ELEMENT) continue;
        if (matches(c, sel)) out.push(c);
        walk(c);
      }
    };
    walk(this);
    return out;
  }
  querySelector(sel: string): N | null { return this.querySelectorAll(sel)[0] ?? null; }
}

function matches(n: N, sel: string): boolean {
  return sel.split(",").some(part => {
    let rest = part.trim();
    const tag = /^[a-zA-Z][a-zA-Z0-9-]*/.exec(rest)?.[0];
    if (tag) {
      if (n.localName !== tag.toLowerCase()) return false;
      rest = rest.slice(tag.length);
    }
    if (!rest && !tag) return false;
    while (rest) {
      const cls = /^\.([\w-]+)/.exec(rest);
      const at = /^\[([\w-]+)(?:([*]?=)['"]?([^'"\]]*)['"]?)?\]/.exec(rest);
      if (cls) {
        if (!(n.attrs["class"] ?? "").split(/\s+/).includes(cls[1]!)) return false;
        rest = rest.slice(cls[0].length);
      } else if (at) {
        const v = n.getAttribute(at[1]!);
        if (v === null) return false;
        if (at[2] === "=" && v !== at[3]) return false;
        if (at[2] === "*=" && !v.includes(at[3]!)) return false;
        rest = rest.slice(at[0].length);
      } else {
        return false;
      }
    }
    return true;
  });
}

const VOID = new Set(["img", "br", "hr", "input", "meta", "link", "wbr", "source"]);
const ENTITIES: Record<string, string> = { amp: "&", lt: "<", gt: ">", quot: '"', apos: "'", nbsp: " " };

function decode(s: string): string {
  return s.replace(/&(?:#x([0-9a-f]+)|#(\d+)|(\w+));/gi, (m, hex, dec, name) => {
    if (hex) return String.fromCodePoint(parseInt(hex, 16));
    if (dec) return String.fromCodePoint(parseInt(dec, 10));
    return ENTITIES[name as string] ?? m;
  });
}

function parse(html: string): N {
  const root = new N(ELEMENT, "#root", {}, "");
  const stack: N[] = [root];
  const top = (): N => stack[stack.length - 1]!;
  const add = (n: N): void => { n.parent = top(); top().childNodes.push(n); };
  const re = /<!--[\s\S]*?-->|<!doctype[^>]*>|<\/([a-zA-Z][\w-]*)\s*>|<([a-zA-Z][\w-]*)((?:"[^"]*"|'[^']*'|[^'">])*)>|([^<]+|<)/gi;
  let m: RegExpExecArray | null;
  while ((m = re.exec(html)) !== null) {
    if (m[1]) {
      const name = m[1].toLowerCase();
      const at = stack.map(s => s.localName).lastIndexOf(name);
      if (at > 0) stack.length = at;
    } else if (m[2]) {
      const name = m[2].toLowerCase();
      const attrs: Record<string, string> = {};
      for (const a of (m[3] ?? "").matchAll(/([^\s=/>]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+)))?/g)) {
        const k = a[1]!.toLowerCase();
        if (!(k in attrs)) attrs[k] = decode(a[2] ?? a[3] ?? a[4] ?? "");
      }
      const el = new N(ELEMENT, name, attrs, "");
      add(el);
      if (name === "script" || name === "style") {
        const close = html.toLowerCase().indexOf("</" + name, re.lastIndex);
        const end = close < 0 ? html.length : close;
        const body = html.slice(re.lastIndex, end);
        if (body) { const t = new N(TEXT, "#text", {}, body); t.parent = el; el.childNodes.push(t); }
        re.lastIndex = end;
      } else if (!VOID.has(name) && !(m[3] ?? "").endsWith("/")) {
        stack.push(el);
      }
    } else if (m[4]) {
      add(new N(TEXT, "#text", {}, decode(m[4])));
    }
  }
  return root;
}

function docOf(html: string): Document {
  const root = parse(html);
  const body = root.querySelector("body") as unknown as HTMLElement | null;
  const documentElement = root.querySelector("html") as unknown as HTMLElement | null;
  return {
    title: root.querySelector("title")?.textContent ?? "",
    body,
    documentElement,
    querySelector: (s: string) => root.querySelector(s),
    querySelectorAll: (s: string) => root.querySelectorAll(s),
  } as unknown as Document;
}

const bytes = (s: string) => new TextEncoder().encode(s).length;
const PAGE = "https://careers.acme.com/jobs/123";

describe("extractDetail: JSON-LD", () => {
  test("extracts HTML description, PostalAddress location, and datePosted from matching JobPosting", () => {
    const ld = {
      "@context": "https://schema.org",
      "@type": "JobPosting",
      title: "Staff Backend Engineer",
      url: "https://careers.acme.com/jobs/123/?utm_source=board",
      datePosted: "2026-11-15",
      jobLocation: [
        { "@type": "Place", address: { "@type": "PostalAddress", addressLocality: "San Francisco", addressRegion: "CA", addressCountry: "US" } },
        { "@type": "Place", name: "New York, NY" },
      ],
      description: "<p>Build distributed systems in <strong>Go</strong> services.</p><ul><li>Own storage</li><li>Mentor peers</li></ul><script>alert(1)</script>",
    };
    const doc = docOf(`<html><head><script type="application/ld+json">${JSON.stringify(ld).replace(/<\//g, "<\\/")}</script></head><body><main><p>Ignored DOM text</p></main></body></html>`);
    const res = extractDetail(doc, PAGE);
    expect(res.description).toBe("Build distributed systems in Go services.\nOwn storage\nMentor peers");
    expect(res.location).toBe("San Francisco, CA, US; New York, NY");
    expect(res.posted).toBe("2026-11-15");
    expect(res.shortened).toBe(false);
  });

  test("disambiguates multiple JobPostings by matching pageUrl and rejects unrelated multi-posting lists", () => {
    const graph = {
      "@graph": [
        { "@type": "JobPosting", url: "https://careers.acme.com/jobs/999", description: "<p>Wrong role</p>", datePosted: "2026-10-01" },
        { "@type": "JobPosting", url: "/jobs/123", description: "<p>Matching role in Rust and Go.</p>", jobLocationType: "TELECOMMUTE" },
      ],
    };
    const res = extractDetail(docOf(`<script type="application/ld+json">${JSON.stringify(graph)}</script>`), PAGE);
    expect(res.description).toBe("Matching role in Rust and Go.");
    expect(res.location).toBe("Remote");

    // Two postings and neither matches pageUrl -> falls through to DOM
    const other = {
      "@graph": [
        { "@type": "JobPosting", url: "https://careers.acme.com/jobs/1", description: "<p>Other 1</p>" },
        { "@type": "JobPosting", url: "https://careers.acme.com/jobs/2", description: "<p>Other 2</p>" },
      ],
    };
    const domRes = extractDetail(
      docOf(`<script type="application/ld+json">${JSON.stringify(other)}</script><main><p>Real page DOM description for this posting.</p></main>`),
      PAGE,
    );
    expect(domRes.description).toBe("Real page DOM description for this posting.");
  });

  test("single JobPosting with description wins even without url, and ignores invalid/oversized JSON-LD blocks", () => {
    const bad = `<script type="application/ld+json">{not json</script>`.repeat(3);
    const single = `<script type="application/ld+json">${JSON.stringify({ "@type": "JobPosting", description: "<p>Single posting description.</p>" })}</script>`;
    expect(extractDetail(docOf(bad + single), PAGE)).toEqual({ description: "Single posting description.", shortened: false });

    // Bounded to 20 blocks: a 21st block is not read, falling back to DOM
    const twenty = `<script type="application/ld+json">{"@type":"Organization","name":"Acme"}</script>`.repeat(20);
    const twentyFirst = `<script type="application/ld+json">${JSON.stringify({ "@type": "JobPosting", description: "<p>Too late</p>" })}</script>`;
    const bounded = extractDetail(docOf(`${twenty}${twentyFirst}<main><p>From DOM instead of block 21.</p></main>`), PAGE);
    expect(bounded.description).toBe("From DOM instead of block 21.");
  });

  test("caps over-long JSON-LD location/posted and excerpts long descriptions at MAX_SNIPPET", () => {
    const manyLocs = Array.from({ length: 30 }, (_, i) => ({ "@type": "Place", name: `Very Long City Name Number ${i}` }));
    const boiler = `<p>${"Acme builds enterprise infrastructure globally. ".repeat(50)}</p>`;
    const role = `<p>What you will do:\n${"Design storage engines in Rust and Go. ".repeat(140)}</p>`;
    const ld = {
      "@type": "JobPosting",
      url: PAGE,
      jobLocation: manyLocs,
      datePosted: "x".repeat(100),
      description: boiler + role,
    };
    const res = extractDetail(docOf(`<script type="application/ld+json">${JSON.stringify(ld)}</script>`), PAGE);
    expect(bytes(res.description)).toBeLessThanOrEqual(MAX_SNIPPET);
    expect(res.shortened).toBe(true);
    expect(res.description.startsWith("What you will do:")).toBe(true);
    expect(bytes(res.location!)).toBeLessThanOrEqual(200);
    expect(res.location).toContain("(+29 more)");
    expect(res.posted).toBeUndefined();
  });
});

describe("extractDetail: boilerplate-pruned HTML", () => {
  test("prunes site chrome, hidden elements, landmarks, cookie/sidebar/modal classes, and link-dense containers", () => {
    const html = `
      <html><body>
        <header><a href="/">Acme Home</a> <a href="/jobs">All Jobs</a></header>
        <nav><ul><li><a href="/about">About</a></li><li><a href="/blog">Blog</a></li></ul></nav>
        <div class="cookie-consent-banner">We use cookies to improve your experience. Accept all cookies.</div>
        <div id="onetrust-consent-sdk">Cookie Preferences</div>
        <div class="breadcrumb"><a href="/">Home</a> &gt; <a href="/jobs">Jobs</a></div>
        <div role="search">Search roles</div>
        <main>
          <div class="nav-links"><a href="/a">Engineering</a> <a href="/b">Design</a> <a href="/c">Product</a> <a href="/d">Sales</a></div>
          <div hidden>Secret hidden honeypot text</div>
          <div aria-hidden="true">Screen-reader hidden noise</div>
          <div style="display: none">CSS hidden noise</div>
          <article>
            <header><h1>Senior Systems Engineer</h1></header>
            <p>You will design high-throughput Go services and read our <a href="/eng">engineering handbook</a> with teammates across San Francisco and Berlin.</p>
            <h2>Responsibilities</h2>
            <ul>
              <li>Build fault-tolerant storage systems.</li>
              <li>Collaborate across infrastructure teams.</li>
            </ul>
            <aside>Related jobs sidebar text</aside>
            <form><label>Apply now</label><input type="text"></form>
            <dialog>Sign in to continue</dialog>
            <script>window.leak = "never";</script>
            <style>.x { color: red; }</style>
            <noscript>Enable JS</noscript>
            <template>Template text</template>
            <svg><text>SVG icon text</text></svg>
            <iframe>Frame fallback</iframe>
          </article>
        </main>
        <div class="modal-backdrop">Subscribe to newsletter</div>
        <footer>Copyright 2026 Acme Corp</footer>
      </body></html>
    `;
    const res = extractDetail(docOf(html), PAGE);
    expect(res.description).toContain("Senior Systems Engineer");
    expect(res.description).toContain("You will design high-throughput Go services and read our engineering handbook with teammates across San Francisco and Berlin.");
    expect(res.description).toContain("Responsibilities\nBuild fault-tolerant storage systems.\nCollaborate across infrastructure teams.");
    for (const noise of [
      "Acme Home", "All Jobs", "About", "Blog", "cookies", "Cookie Preferences", "Search roles",
      "Engineering Design", "honeypot", "Screen-reader hidden", "CSS hidden", "Related jobs",
      "Apply now", "Sign in", "never", "color: red", "Enable JS", "Template text", "SVG icon",
      "Frame fallback", "newsletter", "Copyright 2026",
    ]) {
      expect(res.description).not.toContain(noise);
    }
  });

  test("keeps boilerplate-named containers when class or id also names job|posting|description|content|role|career", () => {
    const html = `
      <body>
        <div class="cookie-banner">Dismiss cookies</div>
        <div class="job-modal-content">
          <div class="role-sidebar">
            <h2>Principal Rust Engineer</h2>
            <p>Own the core indexing engine and query planner.</p>
          </div>
        </div>
        <div id="posting-banner">Full-time • Remote</div>
      </body>
    `;
    const res = extractDetail(docOf(html), PAGE);
    expect(res.description).toContain("Principal Rust Engineer");
    expect(res.description).toContain("Own the core indexing engine and query planner.");
    expect(res.description).toContain("Full-time • Remote");
    expect(res.description).not.toContain("Dismiss cookies");
  });

  test("selects [role='main'], [class*='job-description'], and [class*='posting-description'] when <main> is absent or empty", () => {
    const roleMain = docOf(`<body><nav>Nav links</nav><div role="main"><p>Staff Platform Engineer working on Kubernetes clusters and Go tooling.</p></div></body>`);
    expect(extractDetail(roleMain, PAGE).description).toBe("Staff Platform Engineer working on Kubernetes clusters and Go tooling.");

    const jobDesc = docOf(`<body><main></main><div class="job-description"><p>Build reliable payment APIs with Go and Postgres.</p></div><footer>Legal</footer></body>`);
    expect(extractDetail(jobDesc, PAGE).description).toBe("Build reliable payment APIs with Go and Postgres.");

    const postDesc = docOf(`<body><div class="posting-description-wrap"><p>Lead ML infrastructure architecture.</p></div></body>`);
    expect(extractDetail(postDesc, PAGE).description).toBe("Lead ML infrastructure architecture.");
  });

  test("pruned link-dense child never poisons parent container holding real prose", () => {
    const html = `
      <body>
        <main>
          <div class="wrapper">
            <ul class="top-links">
              <li><a href="/1">Link One Long Anchor Text</a></li>
              <li><a href="/2">Link Two Long Anchor Text</a></li>
              <li><a href="/3">Link Three Long Anchor Text</a></li>
              <li><a href="/4">Link Four Long Anchor Text</a></li>
            </ul>
            <p>Core systems role.</p>
          </div>
          <p>You will design distributed consensus and storage services in Go and Rust, collaborating with infrastructure engineers across teams.</p>
        </main>
      </body>
    `;
    const res = extractDetail(docOf(html), PAGE);
    expect(res.description).toContain("Core systems role.");
    expect(res.description).toContain("distributed consensus and storage services");
    expect(res.description).not.toContain("Link One Long Anchor Text");
  });

  test("applies >65% text-loss safety fallback to raw body text when pruning strips the whole JD", () => {
    const prose = "You will architect low-latency distributed storage services in Rust and Go, lead design reviews, and mentor engineers across core teams. ".repeat(3).trim();
    const html = `<html><body><form id="aspnetForm"><script>bad()</script><h2>Systems Engineer</h2><p>${prose}</p></form></body></html>`;
    const res = extractDetail(docOf(html), PAGE);
    expect(res.description).toContain("Systems Engineer");
    expect(res.description).toContain("low-latency distributed storage services");
    expect(res.description).not.toContain("bad()");
  });

  test("does not trigger >65% fallback when outer boilerplate is larger than a clean <main>", () => {
    const bigNav = `<nav>${'<a href="/x">Navigation Menu Item</a> '.repeat(80)}</nav>`;
    const bigFooter = `<footer>${"<p>Legal copyright disclaimer block.</p>".repeat(40)}</footer>`;
    const html = `<body>${bigNav}<main><p>Build Go microservices for data pipelines and distributed tracing systems.</p></main>${bigFooter}</body>`;
    const res = extractDetail(docOf(html), PAGE);
    expect(res.description).toBe("Build Go microservices for data pipelines and distributed tracing systems.");
  });
});

describe("extractDetail: adversarial bounds", () => {
  test("deeply nested markup beyond MAX_DETAIL_DEPTH stops safely without stack overflow", () => {
    const depth = MAX_DETAIL_DEPTH + 500;
    const html = `<main><p>Top role summary</p>${"<div>".repeat(depth)}Too deep${"</div>".repeat(depth)}</main>`;
    const res = extractDetail(docOf(html), PAGE);
    expect(res.description).toBe("Top role summary");
  });

  test("giant DOM beyond MAX_DETAIL_NODES stops within the node budget", () => {
    const many = Array.from({ length: MAX_DETAIL_NODES + 500 }, (_, i) => `<span>n${i}</span>`).join("");
    const html = `<main><p>Lead Engineer</p>${many}<p>After node limit</p></main>`;
    const res = extractDetail(docOf(html), PAGE);
    expect(res.description).toContain("Lead Engineer");
    expect(res.description).not.toContain("After node limit");
  });

  test("empty or missing document returns empty detail without throwing", () => {
    expect(extractDetail(docOf(""), PAGE)).toEqual({ description: "", shortened: false });
    expect(extractDetail(null as unknown as Document, PAGE)).toEqual({ description: "", shortened: false });
  });
});
