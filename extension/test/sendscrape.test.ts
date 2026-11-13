import { describe, expect, test } from "bun:test";
import { MAX_BATCH_BYTES, describeSend, sendCrawl, sendScrape, wireBytes } from "../src/crawler";
import { MAX_INGEST_JOBS, describeIngest, leftOutText, prepareIngest, sendable } from "../src/ingest";

// Save (popup and drawer) used to send one message of at most MAX_INGEST_JOBS: a 669-job page sent
// 200 and said "469 not sent (at most 200 at a time)", and a page of 200 full postings could pass the
// Mac host's 1 MiB bound in that one message. Save now goes through the crawl's batched sender.

type Msg = { type: string; page: { url: string; title: string }; saveSearch: boolean; jobs: Array<{ url: string; snippet: string }> };

const job = (i: number, over: Record<string, string> = {}) => ({
  title: `Role ${i}`, company: "Acme", location: "Remote", url: `https://careers.acme.com/jobs/${i}`, snippet: "x", ...over,
});
const scrapeOf = (n: number, pageUrl = "https://careers.acme.com/search") => ({
  pageUrl, pageTitle: "Jobs", jobs: Array.from({ length: n }, (_, i) => job(i)),
});

/** A host that takes every batch and records what it was sent. */
function host(answer: (m: Msg, n: number) => unknown = m => ({ queued: m.jobs.length })) {
  const sent: Msg[] = [];
  return { sent, send: async (m: unknown) => { sent.push(m as Msg); return answer(m as Msg, sent.length); } };
}

describe("sendScrape: Save sends every job, in bounded batches", () => {
  test("the old one-message path really did stop at 200 (the bug this replaces)", () => {
    const p = prepareIngest(scrapeOf(669), false);
    if (!("req" in p)) throw new Error(p.error);
    expect(p.req.jobs.length).toBe(MAX_INGEST_JOBS);
    expect(p.leftOut).toBe("469 not sent (at most 200 at a time)");
  });

  test("669 jobs go as four messages, all delivered, the search saved by the first only", async () => {
    const h = host();
    const r = await sendScrape(scrapeOf(669), true, h.send);
    expect(h.sent.map(m => m.jobs.length)).toEqual([200, 200, 200, 69]);
    expect(h.sent.map(m => m.saveSearch)).toEqual([true, false, false, false]);
    expect(h.sent.every(m => m.type === "ingestJobs" && m.page.url === "https://careers.acme.com/search")).toBe(true);
    expect(new Set(h.sent.flatMap(m => m.jobs.map(j => j.url))).size).toBe(669);
    expect(r.ok).toBe(true);
    expect(r.text).toBe("4 batches: 200 queued; 200 queued; 200 queued; 69 queued");
    expect(r.text).not.toContain("not sent");
  });

  test("200 full postings never pass the host's byte bound in one message", async () => {
    const long = (i: number) => job(i, { url: `https://acme.wd5.myworkdayjobs.com/External/job/US-CA/Senior_JR${i}`, description: "a/b ".repeat(999) });
    const scrape = { pageUrl: "https://acme.com/s", pageTitle: "", jobs: Array.from({ length: 200 }, (_, i) => long(i)) };
    // The old single message: every job fits its own bounds, the whole is over the host's.
    const old = prepareIngest(scrape, false);
    if (!("req" in old)) throw new Error(old.error);
    expect(wireBytes({ op: "jobsIngest", page: old.req.page, save_search: false, jobs: old.req.jobs })).toBeGreaterThan(MAX_BATCH_BYTES);

    const h = host();
    const r = await sendScrape(scrape, false, h.send);
    expect(h.sent.length).toBeGreaterThan(1);
    for (const m of h.sent) expect(wireBytes({ op: "jobsIngest", page: m.page, save_search: m.saveSearch, jobs: m.jobs })).toBeLessThanOrEqual(MAX_BATCH_BYTES);
    expect(h.sent.reduce((a, m) => a + m.jobs.length, 0)).toBe(200);
    expect(r.ok).toBe(true);
  });

  test("a refused second batch ends the send, says what was not sent, and is not ok", async () => {
    const h = host((m, n) => (n === 2 ? { error: "disk full" } : { queued: m.jobs.length }));
    const r = await sendScrape(scrapeOf(669), false, h.send);
    expect(h.sent.length).toBe(2);
    expect(r.ok).toBe(false);
    expect(r.text).toBe("2 batches: 200 queued; Vani refused the jobs: disk full. 469 not sent");
  });

  test("a host that cannot be reached on the first batch is not ok, and nothing reads as queued", async () => {
    const r = await sendScrape(scrapeOf(3), false, async () => { throw new Error("port closed"); });
    expect(r.ok).toBe(false);
    expect(r.text).toBe("Could not reach Vani: port closed. 3 not sent");
  });

  test("a thrown send with no message still reads as not reached, never as an answer not understood", async () => {
    for (const thrown of [new Error(""), "", undefined]) {
      const r = await sendScrape(scrapeOf(1), false, async () => { throw thrown; });
      expect(r).toEqual({ ok: false, text: "Could not reach Vani: the message was not delivered. 1 not sent" });
    }
  });

  test("progress counts only the jobs a batch can carry, as the note before sending does", async () => {
    const jobs = [...scrapeOf(250).jobs, job(999, { title: "" })];
    const seen: string[] = [];
    await sendScrape({ pageUrl: "https://a.com/s", pageTitle: "", jobs }, false, host().send, (from, to, total) => seen.push(`${from}-${to}/${total}`));
    expect(seen).toEqual(["1-200/250", "201-250/250"]);
    expect(sendable(jobs).count).toBe(250);
  });

  test("one job (Track) reads exactly as the host's answer, as before", async () => {
    const h = host(() => ({ queued: 1, analysis: "daemon" }));
    const r = await sendScrape({ pageUrl: "", pageTitle: "", jobs: [job(1)] }, false, h.send);
    expect(r).toEqual({ ok: true, text: describeIngest({ queued: 1, analysis: "daemon" }).text });
    expect(h.sent[0]!.page.url).toBe("https://careers.acme.com/jobs/1");
  });

  test("an unusable job is counted as left out, the rest are sent", async () => {
    const jobs = [job(1), job(2, { url: "javascript:alert(1)" }), job(3)];
    const h = host();
    const r = await sendScrape({ pageUrl: "https://a.com/s", pageTitle: "", jobs }, false, h.send);
    expect(h.sent.flatMap(m => m.jobs).length).toBe(2);
    expect(r).toEqual({ ok: true, text: `2 queued. ${leftOutText({ skipped: 1, overLimit: 0 })}` });
  });

  test("what cannot be sent at all is refused before anything goes, in prepareIngest's words", async () => {
    let calls = 0;
    const send = async () => { calls++; return { queued: 1 }; };
    const http = await sendScrape(scrapeOf(2, "http://a.com/s"), true, send);
    expect(http).toEqual({ ok: false, text: (prepareIngest(scrapeOf(2, "http://a.com/s"), true) as { error: string }).error });
    const none = await sendScrape({ pageUrl: "https://a.com/s", pageTitle: "", jobs: [] }, false, send);
    expect(none.ok).toBe(false);
    const bad = await sendScrape({ pageUrl: "https://a.com/s", pageTitle: "", jobs: [job(1, { url: "" })] }, false, send);
    expect(bad.ok).toBe(false);
    expect(calls).toBe(0);
  });

  test("every duplicate is not an error, but is not ok either, as one batch was before", async () => {
    const r = await sendScrape(scrapeOf(450), false, async m => ({ queued: 0, duplicates: (m as Msg).jobs.length }));
    expect(r.ok).toBe(false);
    expect(r.text).toBe("3 batches: 0 queued, 200 duplicates; 0 queued, 200 duplicates; 0 queued, 50 duplicates");
  });

  test("progress names the jobs in flight and the whole", async () => {
    const seen: string[] = [];
    await sendScrape(scrapeOf(450), false, host().send, (from, to, total) => seen.push(`${from}-${to}/${total}`));
    expect(seen).toEqual(["1-200/450", "201-400/450", "401-450/450"]);
  });

  test("stopped is set when a batch fails or the send is aborted, and only then", async () => {
    const all = await sendCrawl({ jobs: scrapeOf(450).jobs }, { url: "https://a.com", title: "" }, false, host().send);
    expect(all.stopped).toBe(false);
    const failed = await sendCrawl({ jobs: scrapeOf(450).jobs }, { url: "https://a.com", title: "" }, false, async () => ({ error: "x" }));
    expect(failed.stopped).toBe(true);
    const ac = new AbortController();
    const aborted = await sendCrawl({ jobs: scrapeOf(450).jobs }, { url: "https://a.com", title: "" }, false, async m => { ac.abort(); return { queued: (m as Msg).jobs.length }; }, ac.signal);
    expect(aborted.stopped).toBe(true);
    expect(describeSend(aborted).ok).toBe(false);
  });
});

describe("sendable: what Save says before it sends", () => {
  test("past 200 nothing is left out: Save sends them all", () => {
    expect(sendable(scrapeOf(669).jobs)).toEqual({ count: 669, leftOut: "" });
  });
  test("only unusable jobs are left out", () => {
    const jobs = [...scrapeOf(250).jobs, job(999, { title: "" })];
    expect(sendable(jobs)).toEqual({ count: 250, leftOut: leftOutText({ skipped: 1, overLimit: 0 }) });
  });
  test("an empty scrape sends nothing", () => {
    expect(sendable([])).toEqual({ count: 0, leftOut: "" });
  });
});
