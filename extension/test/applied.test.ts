import { describe, expect, test } from "bun:test";
import {
  DISMISS_QUIET_MS, ID_PARAMS, MAX_URL_BYTES, type AppliedUi, type Offer,
  createAppliedFlow, isFinalSubmit, offerSubject, pageKey, parseFind, postingKey,
} from "../src/applied";
import type { ToBackground } from "../src/messages";
import vectors from "./vectors/joburl.json";

describe("pageKey", () => {
  // internal/joburl replays the same file, so the two reductions cannot drift apart.
  for (const c of vectors.cases) {
    test(`${JSON.stringify(c.in)} -> ${JSON.stringify(c.out)}`, () => {
      expect(pageKey(c.in)).toBe(c.out);
    });
  }

  test("bounds the raw URL, in bytes, before dropping the query", () => {
    const base = "https://example.com/";
    expect(pageKey(base + "a".repeat(MAX_URL_BYTES - base.length))).not.toBeNull();
    expect(pageKey(base + "a".repeat(MAX_URL_BYTES - base.length + 1))).toBeNull();
    expect(pageKey(base + "?" + "q".repeat(MAX_URL_BYTES))).toBeNull();
    // 2 bytes a character: 1,100 of them fit the character count but not the byte count.
    expect(pageKey(base + "é".repeat(1100))).toBeNull();
  });

  test("refuses what is not a string", () => {
    for (const x of [undefined, null, 42, {}, ["https://example.com/"], true]) expect(pageKey(x)).toBeNull();
  });

  test("never lets the query or fragment through, except the parameters that name a job", () => {
    const k = pageKey("https://boards.greenhouse.io/acme/jobs/1?token=SECRET&email=a@b.c#frag");
    expect(k).toBe("https://boards.greenhouse.io/acme/jobs/1");
    expect(k).not.toContain("SECRET");
    const j = pageKey("https://careers.example.com/open?gh_jid=42&session=SECRET&email=a@b.c");
    expect(j).toBe("https://careers.example.com/open?gh_jid=42");
  });

  test("the job-naming parameters are the ones internal/joburl keeps", () => {
    expect(ID_PARAMS).toEqual(["gh_jid", "jid", "job_id", "jobid", "req_id"]);
  });

  // pageKey is what jobFind sends to internal/joburl, so it stays route-specific on Workday;
  // postingKey (below) is the one-per-posting key the per-job resume is stored under.
  test("keeps Workday's routes apart: it is the Go twin, and Go compares it with the ledger's own URL", () => {
    const H = "https://utaustin.wd1.myworkdayjobs.com";
    const keys = new Set([`${H}/UTstaff/job/AUSTIN-TX/FDE_R_1`, `${H}/en-US/UTstaff/job/AUSTIN-TX/FDE_R_1`, `${H}/en-US/UTstaff/details/FDE_R_1?q=x`].map(pageKey));
    expect(keys.size).toBe(3);
  });
});

describe("postingKey", () => {
  const H = "https://utaustin.wd1.myworkdayjobs.com";
  const SLUG = "Forward-Deployed-Engineer_R_00049483";
  const ONE = `${H}/UTstaff/job/${SLUG}`;

  test("one key for one Workday posting: direct, locale-prefixed, details and search-result routes", () => {
    const shapes = [
      `${H}/UTstaff/job/AUSTIN-TX/${SLUG}`,
      `${H}/en-US/UTstaff/job/AUSTIN-TX/${SLUG}`,
      `${H}/en-US/UTstaff/details/${SLUG}?q=Forward+Deployed+Engineer`,
      `${H}/en-US/UTstaff/details/${SLUG}`,
      `${H}/fr-FR/UTstaff/job/AUSTIN-TX/${SLUG}/`,
      `${H}/UTstaff/job/${SLUG}`,
      `${H.toUpperCase().replace("HTTPS", "https")}/UTstaff/job/AUSTIN-TX/${SLUG}#x`,
    ];
    for (const s of shapes) expect(postingKey(s)).toBe(ONE);
  });

  test("the apply pages share the posting's key, however the location is encoded", () => {
    const apply = [
      `${H}/en-US/UTstaff/job/AUSTIN%2C-TX/${SLUG}/apply/applyManually`,
      `${H}/en-US/UTstaff/job/AUSTIN-TX/${SLUG}/apply`,
      `${H}/en-US/UTstaff/job/AUSTIN-TX/${SLUG}/apply/autofillWithResume`,
      `${H}/UTstaff/job/AUSTIN-TX/${SLUG}/Apply/applyManually?source=x`,
    ];
    for (const s of apply) expect(postingKey(s)).toBe(ONE);
  });

  test("a different requisition, site or tenant is a different posting", () => {
    const k = postingKey(`${H}/UTstaff/job/AUSTIN-TX/${SLUG}`);
    expect(postingKey(`${H}/UTstaff/job/AUSTIN-TX/Forward-Deployed-Engineer_R_00049484`)).not.toBe(k);
    expect(postingKey(`${H}/Other/job/AUSTIN-TX/${SLUG}`)).not.toBe(k);
    expect(postingKey(`https://other.wd1.myworkdayjobs.com/UTstaff/job/AUSTIN-TX/${SLUG}`)).not.toBe(k);
    expect(postingKey(`${H}/en-US/UTstaff/job/${SLUG}`)).toBe(k); // no location segment
  });

  test("a Workday page that is not a posting keeps its pageKey", () => {
    for (const s of [`${H}/UTstaff?q=engineer`, `${H}/en-US/UTstaff`, `${H}/en-US/UTstaff/login`, `${H}/en-US/Jobs`, `${H}/UTstaff/job`, `${H}/UTstaff/job/apply`, `${H}/en-US/UTstaff/details`, H]) {
      expect(postingKey(s)).toBe(pageKey(s));
    }
  });

  test("on Workday's shared host the site is recruiting/{tenant}/{site}, the twin of joburl.WorkdayPosting", () => {
    const S = "https://wd1.myworkdaysite.com";
    const one = `${S}/recruiting/snapchat/snap/job/ML_R1-1`;
    for (const s of [
      `${S}/recruiting/snapchat/snap/job/Paris-France/ML_R1-1`,
      `${S}/en-US/recruiting/snapchat/snap/job/Paris-France/ML_R1-1`,
      `${S}/fr-FR/recruiting/snapchat/snap/details/ML_R1-1`,
      `${S}/recruiting/snapchat/snap/job/Paris-France/ML_R1-1/apply/applyManually?x=1`,
    ]) expect(postingKey(s)).toBe(one);
    expect(postingKey(`${S}/recruiting/othertenant/snap/job/Paris-France/ML_R1-1`)).not.toBe(one);
    expect(postingKey(`${S}/recruiting/snapchat/other/job/Paris-France/ML_R1-1`)).not.toBe(one);
    for (const s of [`${S}/recruiting/snapchat/snap`, `${S}/recruiting/snapchat/snap/login`, `${S}/snap/job/NY/ML_R1-1`,
      `${S}/careers/snapchat/snap/job/NY/ML_R1-1`, `${S}/recruiting/snapchat/snap/job/apply`, `https://snapchat.wd1.myworkdaysite.com/recruiting/a/b/job/NY/R1`]) {
      expect(postingKey(s)).toBe(pageKey(s));
    }
  });

  test("a site that merely looks like a locale or a segment name is not stripped", () => {
    expect(postingKey(`${H}/ext/job/NY/${SLUG}`)).toBe(`${H}/ext/job/${SLUG}`);
    expect(postingKey(`${H}/details/job/NY/${SLUG}`)).toBe(`${H}/details/job/${SLUG}`);
  });

  test("is never longer than the page's own key allows, and refuses what pageKey refuses", () => {
    for (const x of [undefined, null, 42, {}, "", "file:///x", "https://", "x".repeat(MAX_URL_BYTES + 1)]) expect(postingKey(x)).toBeNull();
    const long = `${H}/UTstaff/job/AUSTIN-TX/${"a".repeat(MAX_URL_BYTES)}`;
    expect(postingKey(long)).toBeNull();
  });

  test("Greenhouse, Lever, Ashby and every other board keep the pageKey the vectors pin", () => {
    for (const c of vectors.cases) expect(postingKey(c.in)).toBe(c.out);
    expect(postingKey("https://jobs.lever.co/acme/abc-1/apply")).toBe("https://jobs.lever.co/acme/abc-1/apply");
    expect(postingKey("https://jobs.ashbyhq.com/acme/uuid/application")).toBe("https://jobs.ashbyhq.com/acme/uuid/application");
  });

  test("a host that only contains myworkdayjobs.com is not Workday", () => {
    for (const h of ["https://myworkdayjobs.com.evil.example", "https://evilmyworkdayjobs.com", "https://x.myworkdayjobs.com.evil.example"]) {
      const u = `${h}/en-US/UTstaff/job/AUSTIN-TX/${SLUG}`;
      expect(postingKey(u)).toBe(pageKey(u));
    }
  });
});

describe("isFinalSubmit", () => {
  test("only buttons that send the application", () => {
    for (const t of ["Submit", "submit", "Submit Application", "Submit application", " Submit  application ", "Send application", "Send my application", "Submit & Apply"]) {
      expect(isFinalSubmit(t)).toBe(true);
    }
    for (const t of ["Next", "Continue", "Save and continue", "Save", "Review", "Review and submit", "Apply", "Apply for this job", "Apply now", "Back",
      "Resubmit", "Unsubscribe", "Send message", "", "   ", "Submit ".repeat(10)]) {
      expect(isFinalSubmit(t)).toBe(false);
    }
  });
});

describe("offerSubject", () => {
  test("names the job and the company, and clips posting text", () => {
    expect(offerSubject({ key: "k", company: "Acme", title: "ML Engineer" })).toBe("“ML Engineer” at Acme");
    expect(offerSubject({ key: "k", company: "", title: "" })).toBe("this job");
    expect(offerSubject({ key: "k", company: "Acme", title: "" })).toBe("this job at Acme");
    const long = offerSubject({ key: "k", company: "C".repeat(200), title: "T".repeat(200) });
    expect(long.length).toBeLessThan(160);
    expect(long).toContain("…");
  });
});

const job = { found: true, url: "https://x.example/a", matches: 1, job: { key: "gh:acme:1", company: "Acme", title: "Inference Engineer", status: "digested", matchedOn: "url" } };

describe("parseFind", () => {
  test("a tracked job becomes an offer", () => {
    expect(parseFind(job)).toEqual({ ok: true, why: "match", offer: { key: "gh:acme:1", company: "Acme", title: "Inference Engineer" } });
  });

  test("a job already Applied, an untracked page and an ambiguous one offer nothing", () => {
    expect(parseFind({ ...job, job: { ...job.job, mark: "applied" } })).toEqual({ ok: true, why: "applied", offer: null });
    // Saved or hidden is still not applied: the user may have just done it.
    expect(parseFind({ ...job, job: { ...job.job, mark: "saved" } })).toMatchObject({ ok: true, why: "match" });
    expect(parseFind({ found: false, url: "x", matches: 0 })).toEqual({ ok: true, why: "none", offer: null });
    expect(parseFind({ found: false, url: "x", matches: 2, ambiguous: true })).toEqual({ ok: true, why: "ambiguous", offer: null });
  });

  test("a coded error and a malformed reply are failures, not offers", () => {
    expect(parseFind({ error: "the Vani Apply Mac app isn't reachable", code: "unreachable" })).toEqual({ ok: false, error: "the Vani Apply Mac app isn't reachable", code: "unreachable" });
    for (const r of [undefined, null, "x", 3, {}, { found: "yes" }, { found: true }, { found: true, job: null },
      { found: true, job: { key: "" } }, { found: true, job: { key: 5 } }, { found: true, job: { key: "k".repeat(513) } }]) {
      expect(parseFind(r).ok).toBe(false);
    }
  });

  test("clips and cleans posting text", () => {
    const r = parseFind({ ...job, job: { ...job.job, company: "A".repeat(500), title: "T\u001b[2J\nx" } });
    expect(r.ok && r.offer?.company.length).toBe(200);
    expect(r.ok && r.offer?.title).toBe("T [2J x");
  });
});

describe("flow", () => {
  const offer: Offer = { key: "gh:acme:1", company: "Acme", title: "Inference Engineer" };

  function setup(opts: { href?: string; reply?: (m: ToBackground) => unknown } = {}) {
    const sent: ToBackground[] = [];
    const ui: string[] = [];
    let t = 5_000_000;
    const u: AppliedUi = {
      offer: o => ui.push(`offer:${o.key}`),
      working: () => ui.push("working"),
      done: o => ui.push(`done:${o.key}`),
      failed: (_o, m) => ui.push(`failed:${m}`),
      hide: () => ui.push("hide"),
    };
    const reply = opts.reply ?? ((m: ToBackground) => (m.type === "appliedSignal" || m.type === "appliedPending" ? { offer } : { ok: true }));
    const flow = createAppliedFlow({
      send: async m => { sent.push(m); return reply(m); },
      ui: u,
      href: () => opts.href ?? "https://x.example/a?token=SECRET#app",
      now: () => t,
    });
    return { flow, sent, ui, advance: (ms: number) => { t += ms; } };
  }

  test("a submit asks about the job and sends the page without its query", async () => {
    const { flow, sent, ui } = setup();
    await flow.submitted();
    expect(sent).toEqual([{ type: "appliedSignal", url: "https://x.example/a" }]);
    expect(ui).toEqual(["offer:gh:acme:1"]);
  });

  test("it never marks until the user accepts", async () => {
    const { flow, sent } = setup();
    await flow.submitted();
    await flow.submitted();
    await flow.resume();
    await flow.dismiss();
    expect(sent.some(m => m.type === "appliedMark")).toBe(false);
  });

  test("accept marks once and says so", async () => {
    const { flow, sent, ui } = setup();
    await flow.submitted();
    await flow.accept();
    await flow.accept(); // nothing left to accept
    expect(sent.filter(m => m.type === "appliedMark").length).toBe(1);
    expect(ui).toEqual(["offer:gh:acme:1", "working", "done:gh:acme:1"]);
  });

  test("a failed mark shows why and can be retried", async () => {
    let ok = false;
    const { flow, sent, ui } = setup({ reply: m => (m.type === "appliedMark" ? (ok ? { ok: true } : { error: "jobs: no posting", code: "jobs" }) : { offer }) });
    await flow.submitted();
    await flow.accept();
    expect(ui.at(-1)).toBe("failed:jobs: no posting");
    ok = true;
    await flow.accept();
    expect(ui.at(-1)).toBe("done:gh:acme:1");
    expect(sent.filter(m => m.type === "appliedMark").length).toBe(2);
  });

  test("a throwing background reads as a failed mark", async () => {
    const f = createAppliedFlow({
      send: async m => { if (m.type === "appliedMark") throw new Error("gone"); return { offer }; },
      ui: { offer() {}, working() {}, done() { throw new Error("must not be done"); }, failed: (_o, m) => { expect(m).toContain("isn't reachable"); }, hide() {} },
      href: () => "https://x.example/a",
    });
    await f.submitted();
    await f.accept();
  });

  test("Not now hides, tells the background, and quiets the same job for a while", async () => {
    const { flow, sent, ui, advance } = setup();
    await flow.submitted();
    await flow.dismiss();
    expect(ui).toEqual(["offer:gh:acme:1", "hide"]);
    expect(sent.at(-1)).toEqual({ type: "appliedDismiss" });
    await flow.submitted();
    expect(ui.length).toBe(2); // asked again too soon
    advance(DISMISS_QUIET_MS + 1);
    await flow.submitted();
    expect(ui.at(-1)).toBe("offer:gh:acme:1");
  });

  test("nothing to offer, or a background that fails, is silent", async () => {
    for (const reply of [() => ({ offer: null }), () => ({ error: "x", code: "unreachable" }), () => undefined, () => "x", () => ({ offer: { key: 1 } })]) {
      const { flow, ui } = setup({ reply });
      await flow.submitted();
      await flow.resume();
      expect(ui).toEqual([]);
    }
    const f = createAppliedFlow({ send: async () => { throw new Error("no background"); }, ui: { offer() { throw new Error("no"); }, working() {}, done() {}, failed() {}, hide() {} }, href: () => "https://x.example/a" });
    await f.submitted();
    await f.resume();
  });

  test("a page that is not http(s) sends nothing", async () => {
    for (const href of ["about:blank", "chrome-extension://x/popup.html", "file:///a", "javascript:1"]) {
      const { flow, sent } = setup({ href });
      await flow.submitted();
      expect(sent).toEqual([]);
    }
  });

  test("a submit while marking is ignored", async () => {
    let release: (v: unknown) => void = () => {};
    const gate = new Promise(r => { release = r; });
    const { flow, sent } = setup({ reply: m => (m.type === "appliedMark" ? gate : { offer }) });
    await flow.submitted();
    const marking = flow.accept();
    await flow.submitted();
    await flow.dismiss();
    expect(sent.filter(m => m.type === "appliedSignal").length).toBe(1);
    release({ ok: true });
    await marking;
  });

  test("the offer a previous page left is shown on load, and does not replace one already shown", async () => {
    const { flow, sent, ui } = setup();
    await flow.resume();
    expect(sent).toEqual([{ type: "appliedPending" }]);
    expect(ui).toEqual(["offer:gh:acme:1"]);
    await flow.resume();
    expect(ui.length).toBe(1);
  });

  test("a form in a frame stays quiet when the top frame shows the question", async () => {
    const { flow, ui } = setup({ reply: m => (m.type === "appliedSignal" ? { offer, shownInTop: true } : { ok: true }) });
    await flow.submitted();
    expect(ui).toEqual([]);
    // And shows it itself when the top frame could not.
    const alone = setup({ reply: m => (m.type === "appliedSignal" ? { offer, shownInTop: false } : { ok: true }) });
    await alone.flow.submitted();
    expect(alone.ui).toEqual(["offer:gh:acme:1"]);
  });

  test("two signals for one press (a click and the submit it causes) ask once", async () => {
    let release: (v: unknown) => void = () => {};
    const gate = new Promise(r => { release = r; });
    const { flow, sent, ui } = setup({ reply: m => (m.type === "appliedSignal" ? gate : { ok: true }) });
    const first = flow.submitted();
    await flow.submitted();
    release({ offer });
    await first;
    expect(sent.filter(m => m.type === "appliedSignal").length).toBe(1);
    expect(ui).toEqual(["offer:gh:acme:1"]);
    await flow.submitted(); // a later press asks again
    expect(sent.filter(m => m.type === "appliedSignal").length).toBe(2);
    expect(ui.length).toBe(1); // the same job is already on screen
  });
});
