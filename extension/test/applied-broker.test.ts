import { describe, expect, test } from "bun:test";
import { MAX_OFFERS, OFFER_TTL_MS, type OfferStore, type StoredOffer, createBroker } from "../src/applied";

const job = { found: true, url: "https://x.example/a", matches: 1, job: { key: "gh:acme:1", company: "Acme", title: "Inference Engineer", status: "digested", matchedOn: "url" } };
const OFFER = { key: "gh:acme:1", company: "Acme", title: "Inference Engineer" };

function setup(reply: (m: any) => unknown = () => job, store?: OfferStore, startAt = 1_000_000) {
  const calls: any[] = [];
  let t = startAt;
  const broker = createBroker({ native: async m => { calls.push(m); return reply(m); }, now: () => t, store });
  return { broker, calls, advance: (ms: number) => { t += ms; }, clock: () => t };
}

describe("broker", () => {
  test("a signal looks the page up with its query dropped and keeps the offer for that tab only", async () => {
    const { broker, calls } = setup();
    expect(await broker.signal(7, "https://X.example/a/?token=SECRET#app")).toEqual({ offer: OFFER });
    expect(calls).toEqual([{ op: "jobFind", url: "https://x.example/a" }]);
    expect((await broker.pending(7))?.key).toBe("gh:acme:1");
    expect(await broker.pending(8)).toBeNull();
    expect(await broker.pending(undefined)).toBeNull();
  });

  test("a job-naming parameter goes to the host, the rest of the query does not", async () => {
    const { broker, calls } = setup();
    await broker.signal(7, "https://www.example.com/careers?gh_jid=4012&session=SECRET");
    expect(calls).toEqual([{ op: "jobFind", url: "https://www.example.com/careers?gh_jid=4012" }]);
  });

  test("finding never marks", async () => {
    const { broker, calls } = setup();
    await broker.signal(7, "https://x.example/a");
    await broker.signal(7, "https://x.example/a");
    expect(calls.every(c => c.op === "jobFind")).toBe(true);
  });

  test("a page that is not a usable URL, or has no tab, makes no native call", async () => {
    const { broker, calls } = setup();
    for (const u of ["javascript:alert(1)", "", "file:///x", 5, undefined, "https://x.example/a b"]) {
      expect(await broker.signal(7, u)).toEqual({ offer: null });
    }
    expect(await broker.signal(undefined, "https://x.example/a")).toMatchObject({ code: "bad_request" });
    expect(await broker.signal(1.5, "https://x.example/a")).toMatchObject({ code: "bad_request" });
    expect(calls).toEqual([]);
  });

  test("mark writes exactly the job that was offered, then forgets it", async () => {
    const { broker, calls } = setup(m => (m.op === "jobMark" ? { ok: true, key: m.key, mark: "applied" } : job));
    await broker.signal(7, "https://x.example/a");
    expect(await broker.mark(7)).toEqual({ ok: true, offer: OFFER });
    expect(calls.at(-1)).toEqual({ op: "jobMark", key: "gh:acme:1" });
    expect(await broker.pending(7)).toBeNull();
    expect(await broker.mark(7)).toMatchObject({ code: "no_offer" });
    expect(calls.filter(c => c.op === "jobMark").length).toBe(1);
  });

  test("mark with no offer for the tab marks nothing", async () => {
    const { broker, calls } = setup();
    await broker.signal(7, "https://x.example/a");
    expect(await broker.mark(8)).toMatchObject({ code: "no_offer" });
    expect(await broker.mark(undefined)).toMatchObject({ code: "no_offer" });
    expect(calls.some(c => c.op === "jobMark")).toBe(false);
  });

  test("a refused or unconfirmed mark keeps the offer for another try", async () => {
    let answer: unknown = { error: "jobs: no posting", code: "jobs" };
    const { broker } = setup(m => (m.op === "jobMark" ? answer : job));
    await broker.signal(7, "https://x.example/a");
    expect(await broker.mark(7)).toEqual({ error: "jobs: no posting", code: "jobs" });
    expect(await broker.pending(7)).not.toBeNull();
    answer = { ok: true, key: "someone:else:9", mark: "applied" }; // not the job that was asked for
    expect(await broker.mark(7)).toMatchObject({ code: "error" });
    answer = { ok: true, key: "gh:acme:1", mark: "saved" };
    expect(await broker.mark(7)).toMatchObject({ code: "error" });
    answer = { ok: true, key: "gh:acme:1", mark: "applied" };
    expect(await broker.mark(7)).toMatchObject({ ok: true });
  });

  test("a newer offer for the tab survives an older mark finishing", async () => {
    let release: (v: unknown) => void = () => {};
    const gate = new Promise(r => { release = r; });
    let first = true;
    const { broker } = setup(m => {
      if (m.op === "jobMark") return gate;
      if (first) { first = false; return job; }
      return { ...job, job: { ...job.job, key: "lv:acme:2", title: "Platform" } };
    });
    await broker.signal(7, "https://x.example/a");
    const marking = broker.mark(7); // asks the host to mark gh:acme:1
    await broker.signal(7, "https://x.example/b"); // the tab moves on to another job
    release({ ok: true, key: "gh:acme:1", mark: "applied" });
    await marking;
    expect((await broker.pending(7))?.key).toBe("lv:acme:2");
  });

  test("an unreachable host is a coded answer, not a throw", async () => {
    const broker = createBroker({ native: async () => { throw new Error("no host"); } });
    expect(await broker.signal(7, "https://x.example/a")).toEqual({ error: "the Vani Apply Mac app isn't reachable", code: "unreachable" });
    const down = createBroker({ native: async () => ({ error: "the Vani Apply Mac app isn't reachable", code: "unreachable" }) });
    expect(await down.signal(7, "https://x.example/a")).toMatchObject({ code: "unreachable" });
  });

  test("an offer expires", async () => {
    const { broker, advance, calls } = setup();
    await broker.signal(7, "https://x.example/a");
    advance(OFFER_TTL_MS - 1);
    expect(await broker.pending(7)).not.toBeNull();
    advance(2);
    expect(await broker.pending(7)).toBeNull();
    expect(await broker.mark(7)).toMatchObject({ code: "no_offer" });
    expect(calls.some(c => c.op === "jobMark")).toBe(false);
  });

  test("a newer signal replaces the offer; a page with nothing to offer clears it", async () => {
    let reply: unknown = job;
    const { broker } = setup(() => reply);
    await broker.signal(7, "https://x.example/a");
    reply = { ...job, job: { ...job.job, key: "lv:acme:2", title: "Platform" } };
    await broker.signal(7, "https://x.example/b");
    expect((await broker.pending(7))?.key).toBe("lv:acme:2");
    reply = { found: false, url: "x", matches: 0 };
    await broker.signal(7, "https://x.example/c");
    expect(await broker.pending(7)).toBeNull();
    reply = { ...job, job: { ...job.job, mark: "applied" } };
    await broker.signal(7, "https://x.example/a");
    expect(await broker.pending(7)).toBeNull();
  });

  test("dismiss forgets the offer", async () => {
    const { broker } = setup();
    await broker.signal(7, "https://x.example/a");
    await broker.dismiss(8);
    expect(await broker.pending(7)).not.toBeNull();
    await broker.dismiss(7);
    expect(await broker.pending(7)).toBeNull();
  });

  test("the table is bounded", async () => {
    const { broker, advance } = setup();
    for (let tab = 1; tab <= 100; tab++) { await broker.signal(tab, "https://x.example/a"); advance(1); }
    let held = 0;
    for (let tab = 1; tab <= 100; tab++) if (await broker.pending(tab)) held++;
    expect(held).toBe(MAX_OFFERS);
    expect(await broker.pending(1)).toBeNull();
    expect(await broker.pending(100)).not.toBeNull();
  });

  test("simultaneous signals for different tabs all land", async () => {
    const { broker } = setup();
    await Promise.all(Array.from({ length: 20 }, (_, i) => broker.signal(i + 1, "https://x.example/a")));
    for (let tab = 1; tab <= 20; tab++) expect(await broker.pending(tab)).not.toBeNull();
  });
});

describe("broker with persistent storage", () => {
  function sessionStore() {
    let data: Record<string, StoredOffer> = {};
    const store: OfferStore = { load: async () => structuredClone(data), save: async v => { data = structuredClone(v); } };
    return { store, peek: () => data, poison: (v: unknown) => { data = v as Record<string, StoredOffer>; } };
  }

  test("a worker that was stopped still has the offer when the confirmation page loads", async () => {
    const s = sessionStore();
    const before = setup(() => job, s.store);
    await before.broker.signal(7, "https://x.example/a");
    // A new worker: fresh broker, same storage.
    const after = setup(m => (m.op === "jobMark" ? { ok: true, key: m.key, mark: "applied" } : job), s.store, before.clock());
    expect((await after.broker.pending(7))?.key).toBe("gh:acme:1");
    expect(await after.broker.mark(7)).toMatchObject({ ok: true });
    expect(await createBroker({ native: async () => job, store: s.store, now: after.clock }).pending(7)).toBeNull();
  });

  test("an expired stored offer is not offered or marked", async () => {
    const s = sessionStore();
    const a = setup(() => job, s.store);
    await a.broker.signal(7, "https://x.example/a");
    const b = setup(() => job, s.store, a.clock() + OFFER_TTL_MS + 1);
    expect(await b.broker.pending(7)).toBeNull();
    expect(await b.broker.mark(7)).toMatchObject({ code: "no_offer" });
    expect(b.calls).toEqual([]);
  });

  test("malformed stored entries are ignored, not trusted", async () => {
    const s = sessionStore();
    const exp = 9_999_999_999;
    s.poison({
      "1": null, "2": { offer: null, expires: exp }, "3": { offer: { key: 5 }, expires: exp }, "4": { offer: OFFER, expires: "never" },
      "5": "x", "6": { offer: OFFER, expires: exp },
    });
    const { broker, calls } = setup(m => (m.op === "jobMark" ? { ok: true, key: m.key, mark: "applied" } : job), s.store);
    for (const tab of [1, 2, 3, 4, 5]) expect(await broker.pending(tab)).toBeNull();
    expect(await broker.pending(6)).toEqual(OFFER);
    await broker.mark(6);
    expect(calls).toEqual([{ op: "jobMark", key: "gh:acme:1" }]);
  });

  test("storage that fails does not lose the offers this worker holds, or break a signal", async () => {
    let failing = false;
    const store: OfferStore = {
      load: async () => { if (failing) throw new Error("storage gone"); return {}; },
      save: async () => { if (failing) throw new Error("storage gone"); },
    };
    const { broker } = setup(() => job, store);
    await broker.signal(7, "https://x.example/a");
    failing = true;
    expect((await broker.pending(7))?.key).toBe("gh:acme:1");
    expect(await broker.signal(8, "https://x.example/a")).toEqual({ offer: OFFER });
    await broker.dismiss(7);
    expect(await broker.pending(7)).toBeNull();
  });

  test("what is stored is the offer and its expiry, nothing about the page", async () => {
    const s = sessionStore();
    const { broker } = setup(() => job, s.store);
    await broker.signal(7, "https://x.example/a?token=SECRET");
    expect(JSON.stringify(s.peek())).not.toContain("SECRET");
    expect(Object.keys(s.peek()["7"]!).sort()).toEqual(["expires", "offer"]);
  });
});
