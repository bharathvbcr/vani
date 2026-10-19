import { describe, expect, test } from "bun:test";
import { type Answer, type Bank, emptyBank } from "../src/types";
import { MAX_ANSWERS, MAX_BANK_BYTES, bankBytes, mergeBanks, parseBank } from "../src/bank";
import { type LocalArea, type Transport, createStore } from "../src/store";
import { classify } from "../src/classify";
import { normalizeLabel } from "../src/normalize";
import vectors from "./vectors/classify.json";

function answer(key: string, value: string, updatedAt: string, extra: Partial<Answer> = {}): Answer {
  return { key, labels: [key], kind: "text", value: [value], sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt, review: false, ...extra };
}

function bankOf(...as: Answer[]): Bank {
  const b = emptyBank();
  for (const a of as) b.answers[a.key] = a;
  return b;
}

describe("mergeBanks", () => {
  test("later updatedAt wins per key; ties keep base", () => {
    const base = bankOf(answer("city", "Old", "2026-01-01T00:00:00Z"), answer("tie", "base", "2026-02-01T00:00:00Z"), answer("newer", "base", "2026-03-01T00:00:00Z"));
    const inc = bankOf(answer("city", "New", "2026-02-01T00:00:00Z"), answer("tie", "inc", "2026-02-01T00:00:00Z"), answer("newer", "inc", "2026-01-01T00:00:00Z"));
    const m = mergeBanks(base, inc);
    expect(m.answers.city!.value).toEqual(["New"]);
    expect(m.answers.tie!.value).toEqual(["base"]);
    expect(m.answers.newer!.value).toEqual(["base"]);
  });
  test("keys on one side only are kept", () => {
    const m = mergeBanks(bankOf(answer("aa", "1", "2026-01-01")), bankOf(answer("bb", "2", "2026-01-01")));
    expect(Object.keys(m.answers).sort()).toEqual(["aa", "bb"]);
  });
  test("MAX_ANSWERS is respected, base first", () => {
    const base = emptyBank();
    for (let i = 0; i < MAX_ANSWERS - 1; i++) base.answers[`b${i}`] = answer(`b${i}`, "x", "2026-01-01");
    const inc = bankOf(answer("extra1", "x", "2026-01-01"), answer("extra2", "x", "2026-01-01"));
    const m = mergeBanks(base, inc);
    expect(Object.keys(m.answers).length).toBe(MAX_ANSWERS);
    expect(m.answers.extra1).toBeDefined();
    expect(m.answers.extra2).toBeUndefined();
    expect(m.answers.b0).toBeDefined();
  });
  test("settings: captureSelfId from base, disabledHosts union capped at 500, inputs untouched", () => {
    const base = emptyBank();
    base.settings = { captureSelfId: false, disabledHosts: ["a.com", "b.com"] };
    const inc = emptyBank();
    inc.settings = { captureSelfId: true, disabledHosts: ["b.com", ...Array.from({ length: 600 }, (_, i) => `h${i}.com`)] };
    const m = mergeBanks(base, inc);
    expect(m.settings.captureSelfId).toBe(false);
    expect(m.settings.disabledHosts.length).toBe(500);
    expect(m.settings.disabledHosts.slice(0, 3)).toEqual(["a.com", "b.com", "h0.com"]);
    expect(base.settings.disabledHosts).toEqual(["a.com", "b.com"]);
    expect(inc.settings.disabledHosts.length).toBe(601);
  });
});

describe("bank size cap", () => {
  test("bankBytes counts UTF-8 bytes of the compact JSON", () => {
    const b = bankOf(answer("city", "Zürich ✓", "2026-01-01"));
    expect(bankBytes(b)).toBe(new TextEncoder().encode(JSON.stringify(b)).length);
    expect(bankBytes(b)).toBeGreaterThan(JSON.stringify(b).length); // multi-byte chars
  });
  function bigBank(valueChars: number, n: number): Bank {
    const b = emptyBank();
    for (let i = 0; i < n; i++) b.answers[`key${i}`] = answer(`key${i}`, "v".repeat(valueChars), "2026-01-01");
    return b;
  }
  test("parseBank refuses a bank over MAX_BANK_BYTES and accepts one under it", () => {
    const over = bigBank(9_000, 110);
    expect(bankBytes(over)).toBeGreaterThan(MAX_BANK_BYTES);
    expect(() => parseBank(JSON.stringify(over))).toThrow(/max 900000/);
    const ok = bigBank(9_000, 90);
    expect(bankBytes(ok)).toBeLessThan(MAX_BANK_BYTES);
    expect(Object.keys(parseBank(JSON.stringify(ok)).bank.answers).length).toBe(90);
  });
});

// ---- store with a fake native host and fake chrome.storage ----

class FakeHost {
  rev = 0;
  bank: Bank = emptyBank();
  log: object[] = [];
  down = false;
  errorOn: string | null = null;
  conflicts = 0; // number of upcoming puts to answer with a conflict (a concurrent writer bumps rev)
  failPut = false;
  calls: Array<Record<string, unknown>> = [];
  transport: Transport = async msg => {
    const m = msg as Record<string, unknown>;
    this.calls.push(m);
    if (this.down) throw new Error("Specified native messaging host not found.");
    if (this.errorOn === m.op) return { error: "boom" };
    switch (m.op) {
      case "ping": return { ok: true, protocol: 1, rev: this.rev };
      case "get": return { rev: this.rev, bank: this.bank };
      case "put": {
        if (this.failPut) return { error: "disk full" };
        if (this.conflicts > 0) {
          this.conflicts--;
          this.rev++;
          this.bank = { ...this.bank, answers: { ...this.bank.answers, [`other${this.rev}`]: answer(`other${this.rev}`, "x", "2026-01-01") } };
          return { conflict: true, rev: this.rev, bank: this.bank };
        }
        if (m.baseRev !== this.rev) return { conflict: true, rev: this.rev, bank: this.bank };
        this.rev++;
        this.bank = m.bank as Bank;
        return { rev: this.rev, droppedSecret: 0 };
      }
      case "log": this.log.push(m.entry as object); return { ok: true };
      default: return { error: "unknown op" };
    }
  };
  puts(): number { return this.calls.filter(c => c.op === "put").length; }
}

class FakeLocal implements LocalArea {
  data: Record<string, unknown> = {};
  events: string[] = [];
  async get(k: string) { return k in this.data ? { [k]: this.data[k] } : {}; }
  async set(i: Record<string, unknown>) { Object.assign(this.data, i); this.events.push("set"); }
  async remove(k: string) { delete this.data[k]; this.events.push("remove"); }
}

function setup() {
  const host = new FakeHost();
  const local = new FakeLocal();
  const clock = { t: 1_000 };
  const store = createStore({ transport: host.transport, local, timeoutMs: 50, now: () => clock.t });
  return { host, local, store, clock };
}

const addCity = (b: Bank): Bank => ({ ...b, answers: { ...b.answers, city: answer("city", "Berlin", "2026-05-01T00:00:00Z") } });

describe("store: native mode", () => {
  test("get and put happy path", async () => {
    const { host, store, local } = setup();
    expect(await store.storageMode()).toBe("mac-app");
    const next = await store.updateBank(addCity);
    expect(next.answers.city!.value).toEqual(["Berlin"]);
    expect(host.rev).toBe(1);
    expect((await store.loadBank()).answers.city!.value).toEqual(["Berlin"]);
    expect(local.data.vaniBank).toBeUndefined();
    expect(host.calls.find(c => c.op === "put")!.baseRev).toBe(0);
  });

  test("a conflict re-runs fn on the returned snapshot and succeeds", async () => {
    const { host, store } = setup();
    host.conflicts = 1;
    let runs = 0;
    const next = await store.updateBank(b => { runs++; return addCity(b); });
    expect(runs).toBe(2);
    expect(next.answers.other1).toBeDefined(); // the concurrent writer's answer survived
    expect(host.bank.answers.city).toBeDefined();
    expect(host.bank.answers.other1).toBeDefined();
  });

  test("three conflicts in a row throw", async () => {
    const { host, store } = setup();
    host.conflicts = 3;
    let runs = 0;
    await expect(store.updateBank(b => { runs++; return addCity(b); })).rejects.toThrow("answer bank changed concurrently; try again");
    expect(runs).toBe(3);
    expect(host.puts()).toBe(3);
  });

  test("replaceBank puts with the current rev and retries on conflict", async () => {
    const { host, store } = setup();
    host.rev = 4;
    host.conflicts = 1;
    await store.replaceBank(bankOf(answer("imported", "1", "2026-01-01")));
    expect(Object.keys(host.bank.answers)).toEqual(["imported"]);
    const baseRevs = host.calls.filter(c => c.op === "put").map(c => c.baseRev);
    expect(baseRevs).toEqual([4, 5]);
  });

  test("an error reply is surfaced and does not fall back to local", async () => {
    const { host, store, local } = setup();
    host.errorOn = "get";
    await expect(store.updateBank(addCity)).rejects.toThrow("boom");
    expect(local.data.vaniBank).toBeUndefined();
    expect(local.events).toEqual([]);
    host.errorOn = null;
    host.failPut = true;
    await expect(store.updateBank(addCity)).rejects.toThrow("disk full");
    expect(local.events).toEqual([]);
  });

  test("an error reply to ping is surfaced, not treated as unreachable", async () => {
    const { host, store, local } = setup();
    host.errorOn = "ping";
    await expect(store.loadBank()).rejects.toThrow("boom");
    expect(local.events).toEqual([]);
  });

  test("over-cap update is refused before it is sent", async () => {
    const { host, store } = setup();
    const huge = (b: Bank): Bank => ({ ...b, answers: { ...b.answers, big: answer("big", "x".repeat(MAX_BANK_BYTES), "2026-01-01") } });
    await expect(store.updateBank(huge)).rejects.toThrow(/delete some answers/);
    expect(host.puts()).toBe(0);
  });

  test("logActivity is sent in native mode and swallows errors", async () => {
    const { host, store } = setup();
    await store.updateBank(addCity);
    await store.logActivity({ event: "capture", host: "x.com", counts: { added: 1 } });
    expect(host.log).toEqual([{ event: "capture", host: "x.com", counts: { added: 1 } }]);
    host.errorOn = "log";
    await store.logActivity({ event: "edit" }); // resolves, does not throw
    host.down = true;
    await store.logActivity({ event: "edit" });
  });

  test("the mode is cached for 30 s", async () => {
    const { host, store, clock } = setup();
    await store.loadBank();
    await store.loadBank();
    expect(host.calls.filter(c => c.op === "ping").length).toBe(1);
    clock.t += 31_000;
    await store.loadBank();
    expect(host.calls.filter(c => c.op === "ping").length).toBe(2);
  });
});

describe("store: browser (local) mode", () => {
  test("a transport error falls back to chrome.storage.local", async () => {
    const { host, store, local } = setup();
    host.down = true;
    expect(await store.storageMode()).toBe("browser");
    await store.updateBank(addCity);
    expect((await store.loadBank()).answers.city!.value).toEqual(["Berlin"]);
    expect(local.data.vaniBank).toBeDefined();
    expect(host.puts()).toBe(0);
  });

  test("a hung host times out and falls back", async () => {
    const local = new FakeLocal();
    const store = createStore({ transport: () => new Promise(() => undefined), local, timeoutMs: 20 });
    expect(await store.storageMode()).toBe("browser");
  });

  test("a missing reply (host closed) falls back", async () => {
    const local = new FakeLocal();
    const store = createStore({ transport: async () => undefined, local, timeoutMs: 20 });
    expect(await store.storageMode()).toBe("browser");
  });

  test("over-cap update is refused and nothing is written", async () => {
    const { host, store, local } = setup();
    host.down = true;
    const huge = (b: Bank): Bank => ({ ...b, answers: { ...b.answers, big: answer("big", "x".repeat(MAX_BANK_BYTES), "2026-01-01") } });
    await expect(store.updateBank(huge)).rejects.toThrow(/delete some answers/);
    expect(local.events).toEqual([]);
  });

  test("logActivity does nothing in browser mode", async () => {
    const { host, store } = setup();
    host.down = true;
    await store.storageMode();
    host.down = false;
    await store.logActivity({ event: "edit" });
    expect(host.log).toEqual([]);
  });
});

describe("store: migration from chrome.storage.local", () => {
  test("merges local into native and removes local only after the put succeeded", async () => {
    const { host, local } = setup();
    host.rev = 2;
    host.bank = bankOf(answer("city", "Native", "2026-03-01T00:00:00Z"), answer("onlynative", "n", "2026-01-01T00:00:00Z"));
    local.data.vaniBank = bankOf(answer("city", "Local", "2026-04-01T00:00:00Z"), answer("onlylocal", "l", "2026-01-01T00:00:00Z"));
    const order: string[] = [];
    const inner = host.transport;
    const spy = createStore({
      transport: async m => { const r = await inner(m); if ((m as { op: string }).op === "put") order.push("put"); return r; },
      local: { get: k => local.get(k), set: i => local.set(i), remove: async k => { order.push("remove"); await local.remove(k); } },
      timeoutMs: 50,
    });
    const b = await spy.loadBank();
    expect(order).toEqual(["put", "remove"]);
    expect(b.answers.city!.value).toEqual(["Local"]);
    expect(Object.keys(b.answers).sort()).toEqual(["city", "onlylocal", "onlynative"]);
    expect(local.data.vaniBank).toBeUndefined();
    expect(host.rev).toBe(3);
  });

  test("a failed migration put leaves the local bank intact and reports it", async () => {
    const { host, local, store } = setup();
    const localBank = bankOf(answer("onlylocal", "l", "2026-01-01T00:00:00Z"));
    local.data.vaniBank = localBank;
    host.failPut = true;
    const b = await store.loadBank();
    expect(local.data.vaniBank).toEqual(localBank);
    expect(local.events).not.toContain("remove");
    expect(b.answers.onlylocal).toBeUndefined();
    expect(store.migrationNote()).toMatch(/left in place/);
    // Retries on the next operation and succeeds once the host recovers.
    host.failPut = false;
    await store.loadBank();
    expect(local.data.vaniBank).toBeUndefined();
    expect(host.bank.answers.onlylocal).toBeDefined();
    expect(store.migrationNote()).toBeNull();
  });

  test("a corrupt local bank is reported and never deleted", async () => {
    const { host, local, store } = setup();
    local.data.vaniBank = { version: 1, answers: "garbage" };
    await store.loadBank();
    expect(local.data.vaniBank).toEqual({ version: 1, answers: "garbage" });
    expect(local.events).toEqual([]);
    expect(host.puts()).toBe(0);
    expect(store.migrationNote()).toMatch(/could not be read/);
  });

  test("migration happens once per worker lifetime", async () => {
    const { host, local, store, clock } = setup();
    local.data.vaniBank = bankOf(answer("onlylocal", "l", "2026-01-01T00:00:00Z"));
    await store.loadBank();
    clock.t += 60_000;
    const getsBefore = host.calls.filter(c => c.op === "get").length;
    await store.loadBank();
    expect(host.calls.filter(c => c.op === "get").length).toBe(getsBefore + 1); // just the load itself
    expect(host.puts()).toBe(1);
  });

  test("answers written during a host outage move over when the host returns", async () => {
    const { host, local, store, clock } = setup();
    await store.loadBank(); // native, nothing to migrate
    host.down = true;
    clock.t += 31_000;
    await store.updateBank(addCity); // lands in chrome.storage.local
    expect(local.data.vaniBank).toBeDefined();
    host.down = false;
    clock.t += 31_000;
    const b = await store.loadBank();
    expect(b.answers.city!.value).toEqual(["Berlin"]);
    expect(host.bank.answers.city).toBeDefined();
    expect(local.data.vaniBank).toBeUndefined();
  });
});

describe("shared classifier vectors", () => {
  test("normalizeLabel reproduces every vector", () => {
    expect(vectors.normalize.length).toBeGreaterThan(0);
    for (const v of vectors.normalize) expect(normalizeLabel(v.in)).toBe(v.want);
  });
  test("classify reproduces every vector", () => {
    expect(vectors.classify.length).toBeGreaterThan(0);
    for (const v of vectors.classify) {
      const got = classify({ kind: v.kind as Answer["kind"], label: v.label, name: v.name, autocomplete: v.autocomplete });
      expect({ label: v.label, got }).toEqual({ label: v.label, got: v.want });
    }
  });
});
