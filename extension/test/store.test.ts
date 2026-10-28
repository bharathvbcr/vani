import { describe, expect, test } from "bun:test";
import { type Answer, type Bank, emptyBank } from "../src/types";
import { MAX_ANSWERS, MAX_BANK_BYTES, bankBytes, mergeBanks, parseBank } from "../src/bank";
import { ChunkIntegrityError, type LocalArea, type Transport, createStore } from "../src/store";
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
  test("parseBank accepts a bank over 1 MB and enforces MAX_BANK_BYTES", () => {
    const big = bigBank(9_000, 110);
    expect(bankBytes(big)).toBeGreaterThan(1_000_000);
    expect(Object.keys(parseBank(JSON.stringify(big)).bank.answers).length).toBe(110);
    const huge = { ...emptyBank(), answers: { big: answer("big", "x".repeat(MAX_BANK_BYTES + 1), "2026-01-01") } };
    expect(() => parseBank(JSON.stringify(huge))).toThrow(/larger than 50000000 bytes/);
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
      case "patch": {
        if (m.baseRev !== undefined && m.baseRev !== this.rev) return { conflict: true, rev: this.rev };
        const upsert = Array.isArray(m.upsert) ? (m.upsert as Answer[]) : [];
        const del = Array.isArray(m.delete) ? (m.delete as string[]) : [];
        let dropped = 0;
        const answers = { ...this.bank.answers };
        for (const k of del) delete answers[k];
        for (const a of upsert) {
          if (a.sensitivity === "secret") { dropped++; continue; }
          answers[a.key] = a;
        }
        this.rev++;
        this.bank = { ...this.bank, answers };
        return { op: "patch", rev: this.rev, droppedSecret: dropped };
      }
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

  test("handles chunked get transparently across multiple parts", async () => {
    const local = new FakeLocal();
    const testBank: Bank = {
      ...emptyBank(),
      answers: { testKey: answer("testKey", "chunkedValue", "2026-01-01") },
    };
    const fullJSON = JSON.stringify(testBank);
    const mid = Math.floor(fullJSON.length / 2);
    const chunk0 = fullJSON.slice(0, mid);
    const chunk1 = fullJSON.slice(mid);

    const transport: Transport = async msg => {
      const m = msg as Record<string, unknown>;
      if (m.op === "ping") return { ok: true, protocol: 1, rev: 3 };
      if (m.op === "get") {
        return {
          rev: 3,
          chunked: true,
          totalBytes: fullJSON.length,
          chunkIndex: 0,
          chunkCount: 2,
          chunk: chunk0,
        };
      }
      if (m.op === "getChunk") {
        expect(m.rev).toBe(3);
        expect(m.chunkIndex).toBe(1);
        return {
          rev: 3,
          chunkIndex: 1,
          chunkCount: 2,
          chunk: chunk1,
        };
      }
      return { error: "unexpected" };
    };

    const store = createStore({ transport, local });
    const loaded = await store.loadBank();
    expect(loaded.answers.testKey?.value).toEqual(["chunkedValue"]);
  });

  describe("chunked get integrity", () => {
    // A chunked host: `declared` is the totalBytes it reports; `damage` rewrites the joined text.
    function chunkedHost(bank: Bank, declared: (actual: number) => unknown, damage: (s: string) => string = s => s) {
      const text = damage(JSON.stringify(bank));
      const mid = Math.floor(text.length / 2);
      const parts = [text.slice(0, mid), text.slice(mid)];
      const calls: string[] = [];
      const transport: Transport = async msg => {
        const m = msg as Record<string, unknown>;
        calls.push(String(m.op));
        if (m.op === "ping") return { ok: true, protocol: 1, rev: 3 };
        if (m.op === "get") return { rev: 3, chunked: true, totalBytes: declared(new TextEncoder().encode(JSON.stringify(bank)).length), chunkIndex: 0, chunkCount: 2, chunk: parts[0] };
        if (m.op === "getChunk") return { rev: 3, chunkIndex: 1, chunkCount: 2, chunk: parts[1] };
        return { error: "unexpected" };
      };
      return { transport, calls };
    }
    const bank = () => bankOf(answer("kk", "café € \u{1F600}", "2026-01-01"));

    test("a joined bank whose byte length differs from totalBytes is refused, and nothing is written back", async () => {
      for (const [name, declared] of [["host says more", (n: number) => n + 1], ["host says fewer", (n: number) => n - 1], ["no total", () => undefined], ["total as a string", (n: number) => String(n)]] as const) {
        const { transport, calls } = chunkedHost(bank(), declared);
        const store = createStore({ transport, local: new FakeLocal() });
        await expect(store.loadBank()).rejects.toBeInstanceOf(ChunkIntegrityError);
        await expect(store.replaceBank(bank())).rejects.toBeInstanceOf(ChunkIntegrityError);
        expect(calls.filter(c => c === "put" || c === "patch"), name).toEqual([]);
      }
    });

    test("a chunk cut inside a character (U+FFFD in the join) no longer matches totalBytes and is refused", async () => {
      const { transport } = chunkedHost(bank(), n => n, s => s.replace("€", "��"));
      await expect(createStore({ transport, local: new FakeLocal() }).loadBank()).rejects.toBeInstanceOf(ChunkIntegrityError);
    });

    test("totalBytes counts UTF-8 bytes, not UTF-16 units: a multi-byte bank loads when it matches", async () => {
      const { transport } = chunkedHost(bank(), n => n);
      const loaded = await createStore({ transport, local: new FakeLocal() }).loadBank();
      expect(loaded.answers.kk?.value).toEqual(["café € \u{1F600}"]);
    });
  });

  test("patchAnswers updates and deletes answers atomically", async () => {
    const { host, store } = setup();
    host.bank = bankOf(answer("toDelete", "del", "2026-01-01"), answer("toKeep", "keep", "2026-01-01"));
    const patchRes = await store.patchAnswers(
      [answer("newAnswer", "val", "2026-06-01"), answer("secretField", "pass", "2026-06-01", { sensitivity: "secret" })],
      ["toDelete"]
    );
    expect(patchRes.rev).toBe(1);
    expect(patchRes.droppedSecret).toBe(1);
    expect(host.bank.answers.toDelete).toBeUndefined();
    expect(host.bank.answers.toKeep).toBeDefined();
    expect(host.bank.answers.newAnswer?.value).toEqual(["val"]);
    expect(host.bank.answers.secretField).toBeUndefined();
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

  test("concurrent updateBank in browser mode loses no answers", async () => {
    const { host, store } = setup();
    host.down = true;
    expect(await store.storageMode()).toBe("browser");
    const writers = 10;
    await Promise.all(
      Array.from({ length: writers }, (_, i) =>
        store.updateBank(b => ({
          ...b,
          answers: { ...b.answers, [`w${i}`]: answer(`w${i}`, `val${i}`, "2026-01-01") },
        }))
      )
    );
    const final = await store.loadBank();
    expect(Object.keys(final.answers).length).toBe(writers);
    for (let i = 0; i < writers; i++) {
      expect(final.answers[`w${i}`]?.value).toEqual([`val${i}`]);
    }
  });

  test("serializeWrite error does not break subsequent operations", async () => {
    const { host, store } = setup();
    host.down = true;
    await expect(
      store.updateBank(() => {
        throw new Error("simulated update failure");
      })
    ).rejects.toThrow("simulated update failure");

    // Next operation succeeds cleanly
    const next = await store.updateBank(addCity);
    expect(next.answers.city?.value).toEqual(["Berlin"]);
  });

  test("corrupt local bank in browser mode does not crash loadBank", async () => {
    const { host, local, store } = setup();
    host.down = true;
    local.data.vaniBank = { version: 1, answers: "corrupt_data" };
    const b = await store.loadBank();
    expect(b.answers).toEqual({});
    expect(local.data.vaniBank).toEqual({ version: 1, answers: "corrupt_data" });
    expect(store.migrationNote()).toMatch(/could not be read/);
  });

  test("patchAnswers in browser mode updates and deletes in local storage", async () => {
    const { host, store, local } = setup();
    host.down = true;
    local.data.vaniBank = bankOf(answer("toDelete", "del", "2026-01-01"), answer("toKeep", "keep", "2026-01-01"));
    const patchRes = await store.patchAnswers(
      [answer("newAnswer", "val", "2026-06-01"), answer("secretField", "pass", "2026-06-01", { sensitivity: "secret" })],
      ["toDelete"]
    );
    expect(patchRes.droppedSecret).toBe(1);
    const stored = (await store.loadBank()).answers;
    expect(stored.toDelete).toBeUndefined();
    expect(stored.toKeep).toBeDefined();
    expect(stored.newAnswer?.value).toEqual(["val"]);
    expect(stored.secretField).toBeUndefined();
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

describe("store: the bank memo and patch with a baseRev", () => {
  const gets = (host: FakeHost): number => host.calls.filter(c => c.op === "get").length;

  async function seeded() {
    const s = setup();
    s.host.rev = 5;
    s.host.bank = bankOf(answer("city", "Berlin", "2026-05-01T00:00:00Z"), answer("zip", "10115", "2026-05-01T00:00:00Z"));
    return s;
  }

  test("ten reads inside the window make one native get; the first read after it makes one more", async () => {
    const { host, store, clock } = await seeded();
    for (let i = 0; i < 10; i++) expect((await store.loadSnapshot()).rev).toBe(5);
    expect(gets(host)).toBe(1);
    clock.t += 1_999;
    await store.loadSnapshot();
    expect(gets(host)).toBe(1);
    clock.t += 1;
    await store.loadBank();
    expect(gets(host)).toBe(2);
  });

  test("concurrent reads share one get", async () => {
    const { host, store } = await seeded();
    const all = await Promise.all(Array.from({ length: 8 }, () => store.loadSnapshot()));
    expect(gets(host)).toBe(1);
    expect(new Set(all.map(r => r.rev))).toEqual(new Set([5]));
  });

  test("fresh always asks", async () => {
    const { host, store } = await seeded();
    await store.loadSnapshot();
    await store.loadSnapshot({ fresh: true });
    expect(gets(host)).toBe(2);
  });

  test("a write drops the memo, so the next read sees it", async () => {
    const { host, store } = await seeded();
    await store.loadSnapshot();
    await store.updateBank(addCity);
    const after = await store.loadSnapshot();
    expect(after.rev).toBe(6);
    expect(after.bank.answers.city!.value).toEqual(["Berlin"]);
    expect(gets(host)).toBe(3); // read, the update's own read, read after
  });

  test("a patch against the held revision updates the memo without another get", async () => {
    const { host, store } = await seeded();
    const { bank, rev } = await store.loadSnapshot();
    const row = { ...bank.answers.city!, fills: 1 };
    const out = await store.patchAnswers([row], [], rev!);
    expect(out).toEqual({ rev: 6, droppedSecret: 0 });
    const again = await store.loadSnapshot();
    expect(gets(host)).toBe(1);
    expect(again.rev).toBe(6);
    expect(again.bank.answers.city!.fills).toBe(1);
    expect(again.bank.answers.zip).toEqual(bank.answers.zip);
    expect(host.bank.answers.city!.fills).toBe(1);
    expect(host.calls.filter(c => c.op === "patch").map(c => c.upsert as Answer[]).flat().map(a => a.key)).toEqual(["city"]); // only the affected row
  });

  test("a patch against a stale revision writes nothing, reports the conflict, and drops the memo", async () => {
    const { host, store } = await seeded();
    const { bank } = await store.loadSnapshot();
    host.rev = 9; // the Mac app moved on
    const out = await store.patchAnswers([{ ...bank.answers.city!, fills: 1 }], [], 5);
    expect(out).toEqual({ rev: 9, droppedSecret: 0, conflict: true });
    expect(host.bank.answers.city!.fills).toBe(0);
    expect((await store.loadSnapshot()).rev).toBe(9);
    expect(gets(host)).toBe(2);
  });

  test("a patch that drops a secret row is not trusted to equal the held bank", async () => {
    const { host, store } = await seeded();
    const { bank, rev } = await store.loadSnapshot();
    const out = await store.patchAnswers([{ ...bank.answers.city!, sensitivity: "secret" }], [], rev!);
    expect(out.droppedSecret).toBe(1);
    await store.loadSnapshot();
    expect(gets(host)).toBe(2);
  });

  test("a failed read is not memoised", async () => {
    const { host, store } = await seeded();
    await store.storageMode();
    host.errorOn = "get";
    await expect(store.loadSnapshot()).rejects.toThrow();
    host.errorOn = null;
    expect((await store.loadSnapshot()).rev).toBe(5);
  });

  test("in the browser there is no revision and nothing is served from a memo", async () => {
    const { host, store, local } = setup();
    host.down = true;
    local.data.vaniBank = bankOf(answer("city", "Berlin", "2026-05-01T00:00:00Z"));
    const first = await store.loadSnapshot();
    expect(first.rev).toBeNull();
    local.data.vaniBank = bankOf(answer("city", "Paris", "2026-05-01T00:00:00Z"));
    expect((await store.loadSnapshot()).bank.answers.city!.value).toEqual(["Paris"]);
  });
});

describe("store: native transport selection", () => {
  const g = globalThis as Record<string, unknown>;
  let n = 0;
  // The choice is made when the module loads, so each case loads a fresh copy under its own query string.
  async function load(env: { chrome?: unknown; browser?: unknown }) {
    const saved = { chrome: g.chrome, browser: g.browser };
    g.chrome = env.chrome;
    g.browser = env.browser;
    try {
      return await import(`../src/store.ts?selection-${n++}`) as typeof import("../src/store");
    } finally {
      g.chrome = saved.chrome;
      g.browser = saved.browser;
    }
  }

  test("without chrome.runtime.connectNative it sends one message per call", async () => {
    const chromeStub = { runtime: { sendNativeMessage() {} } };
    g.chrome = chromeStub;
    g.browser = undefined;
    const m = await load({ chrome: chromeStub });
    try {
      expect(m.nativeTransport).toBe(m.spawnTransport);
      expect(m.currentNativeTransport()).toBe(m.spawnTransport);
    } finally {
      delete g.chrome;
      delete g.browser;
    }
  });

  test("Safari's promise API wins over connectNative", async () => {
    const chromeStub = { runtime: { connectNative() { throw new Error("must not connect"); } } };
    const browserStub = { runtime: { sendNativeMessage: async () => ({ ok: true }) } };
    g.chrome = chromeStub;
    g.browser = browserStub;
    const m = await load({ chrome: chromeStub, browser: browserStub });
    try {
      expect(m.nativeTransport).toBe(m.spawnTransport);
      expect(m.currentNativeTransport()).toBe(m.spawnTransport);
    } finally {
      delete g.chrome;
      delete g.browser;
    }
  });

  test("Chrome with connectNative keeps one port for bank ops and sends slow ops per message", async () => {
    const connected: string[] = [];
    const sent: unknown[] = [];
    let onMsg: (m: unknown) => void = () => {};
    const chromeStub = {
      runtime: {
        lastError: undefined,
        connectNative(host: string) {
          connected.push(host);
          return {
            postMessage: (msg: { op: string }) => queueMicrotask(() => onMsg({ ok: true, echo: msg.op })),
            disconnect() {},
            onMessage: { addListener: (cb: (m: unknown) => void) => { onMsg = cb; } },
            onDisconnect: { addListener() {} },
          };
        },
        sendNativeMessage(_host: string, msg: unknown, cb: (r: unknown) => void) { sent.push(msg); cb({ via: "spawn" }); },
      },
    };
    g.chrome = chromeStub; // the transport reads the global when it runs, not only when the module loads
    const m = await load({ chrome: chromeStub });
    try {
      expect(m.nativeTransport).not.toBe(m.spawnTransport);
      expect(m.currentNativeTransport()).toBe(m.nativeTransport);
      expect(await m.nativeTransport({ op: "ping" })).toEqual({ ok: true, echo: "ping" });
      expect(await m.nativeTransport({ op: "get" })).toEqual({ ok: true, echo: "get" });
      expect(connected).toEqual(["dev.vbcr.vani_apply"]);
      expect(await m.nativeTransport({ op: "jobFind" })).toEqual({ via: "spawn" });
      expect(sent).toEqual([{ op: "jobFind" }]);
    } finally {
      delete g.chrome;
    }
  });

  test("a module loaded before connectNative exists uses one port once Chrome gains it, and a different connectNative does not reuse that port", async () => {
    const plain = { runtime: { sendNativeMessage() {} } };
    g.chrome = plain;
    g.browser = undefined;
    const m = await load({ chrome: plain });
    expect(m.nativeTransport).toBe(m.spawnTransport);
    expect(m.currentNativeTransport()).toBe(m.spawnTransport);

    let onMsg: (msg: unknown) => void = () => {};
    function install(tag: string) {
      const connected: string[] = [];
      const connectNative = (host: string) => {
        connected.push(`${tag}:${host}`);
        return {
          postMessage: (msg: { op: string }) => queueMicrotask(() => onMsg({ ok: true, echo: msg.op })),
          disconnect() {},
          onMessage: { addListener: (cb: (msg: unknown) => void) => { onMsg = cb; } },
          onDisconnect: { addListener() {} },
        };
      };
      g.chrome = { runtime: { lastError: undefined, connectNative, sendNativeMessage() { throw new Error("slow op only"); } } };
      return connected;
    }
    const firstHost = install("a");
    try {
      const first = m.currentNativeTransport();
      expect(first).not.toBe(m.spawnTransport);
      expect(m.currentNativeTransport()).toBe(first);
      expect(m.nativeTransport).toBe(m.spawnTransport);
      expect(await first({ op: "ping" })).toEqual({ ok: true, echo: "ping" });
      expect(await first({ op: "get" })).toEqual({ ok: true, echo: "get" });
      expect(firstHost).toEqual(["a:dev.vbcr.vani_apply"]);

      const secondHost = install("b");
      const second = m.currentNativeTransport();
      expect(second).not.toBe(first);
      expect(m.currentNativeTransport()).toBe(second);
      expect(await second({ op: "ping" })).toEqual({ ok: true, echo: "ping" });
      expect(secondHost).toEqual(["b:dev.vbcr.vani_apply"]);
      expect(firstHost).toEqual(["a:dev.vbcr.vani_apply"]);
    } finally {
      delete g.chrome;
      delete g.browser;
    }
  });
});

// A Mac app built before the patch op answers "unknown op patch": the store makes the same change
// with get + put, so captures and fill counts still land.
describe("store: a host without the patch op", () => {
  function oldHost() {
    const host = new FakeHost();
    const inner = host.transport;
    const transport: Transport = async msg => ((msg as Record<string, unknown>).op === "patch" ? (host.calls.push(msg as Record<string, unknown>), { error: "unknown op patch" }) : inner(msg));
    const store = createStore({ transport, local: new FakeLocal(), timeoutMs: 50, now: () => 1_000 });
    return { host, store };
  }
  test("an upsert and a delete land through one put", async () => {
    const { host, store } = oldHost();
    host.bank = { ...host.bank, answers: { gone: answer("gone", "x", "2026-01-01") } };
    const out = await store.patchAnswers([answer("city", "Berlin", "2026-05-01T00:00:00Z")], ["gone"]);
    expect(out.conflict).toBeUndefined();
    expect(out.rev).toBe(1);
    expect(Object.keys(host.bank.answers)).toEqual(["city"]);
    expect(host.puts()).toBe(1);
  });
  test("a moved bank is a conflict when a baseRev is given, and nothing is written", async () => {
    const { host, store } = oldHost();
    host.rev = 5;
    const out = await store.patchAnswers([answer("city", "Berlin", "2026-05-01T00:00:00Z")], [], 4);
    expect(out).toEqual({ rev: 5, droppedSecret: 0, conflict: true });
    expect(host.puts()).toBe(0);
  });
  test("any other host error is still an error", async () => {
    const { host, store } = oldHost();
    host.errorOn = "get";
    await expect(store.patchAnswers([answer("city", "Berlin", "2026-05-01T00:00:00Z")], [])).rejects.toThrow();
  });
});
