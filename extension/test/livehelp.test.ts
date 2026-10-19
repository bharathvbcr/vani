import { afterEach, describe, expect, test } from "bun:test";
import { type Answer, type Bank, type FieldInfo, emptyBank } from "../src/types";
import { mergeCapture } from "../src/bank";
import { matchField } from "../src/match";
import { MAX_SUGGESTIONS, suggest } from "../src/suggest";
import { createDrafter, draftTransport } from "../src/draft";

const now = new Date("2026-10-18T12:00:00Z");

function field(p: Partial<FieldInfo>): FieldInfo {
  return { kind: "text", label: "", name: "", autocomplete: "", automationId: "", options: [], required: false, value: [], ...p };
}

function ans(key: string, value: string[], extra: Partial<Answer> = {}): Answer {
  return {
    key, labels: [key], kind: "text", value, sensitivity: "standard", signatures: [], uses: 1, fills: 0,
    updatedAt: "2026-10-01T00:00:00.000Z", review: false, ...extra,
  };
}

function bankOf(...as: Answer[]): Bank {
  const b = emptyBank();
  for (const a of as) b.answers[a.key] = a;
  return b;
}

describe("suggest (live help without a model)", () => {
  const bank = bankOf(
    ans("years of python experience", ["6"]),
    ans("years of rust experience", ["3"]),
    ans("desired salary", ["150000"]),
    ans("how did you hear about us", ["LinkedIn"]),
    ans("why do you want to work here", ["x".repeat(400)], { kind: "textarea", review: true }),
    ans("gender", ["Prefer not to say"], { sensitivity: "self_id" }),
  );

  test("offers near-miss answers, below the fill threshold, always for review", () => {
    const f = field({ label: "Years of experience with Go" });
    expect(matchField(f, bank, "x.com")).toBeNull(); // fill would leave it blank
    const s = suggest(f, bank, "x.com");
    expect(s.length).toBeGreaterThan(0);
    expect(s.length).toBeLessThanOrEqual(MAX_SUGGESTIONS);
    expect(s.map(x => x.question)).toContain("years of python experience");
    for (const x of s) expect(x.match.review).toBe(true);
  });

  test("never suggests what fill would already use, nor unrelated answers", () => {
    expect(suggest(field({ label: "Desired salary" }), bank, "x.com")).toEqual([]); // exact: fill handles it
    expect(suggest(field({ label: "Favourite colour" }), bank, "x.com")).toEqual([]);
    expect(suggest(field({ label: "" }), bank, "x.com")).toEqual([]);
  });

  test("never for secret fields, and self-id only when turned on", () => {
    expect(suggest(field({ label: "Social security number" }), bankOf(ans("social insurance id", ["1"])), "x.com")).toEqual([]);
    expect(suggest(field({ label: "Password" , kind: "password" }), bank, "x.com")).toEqual([]);
    expect(suggest(field({ label: "Gender identity (optional)" }), bank, "x.com")).toEqual([]);
  });

  test("a choice field only gets answers that map onto its options", () => {
    const b = bankOf(ans("are you willing to relocate to berlin", ["Yes"]), ans("relocation city preference", ["Lisbon"]));
    const f = field({ label: "Are you open to relocation?", kind: "select", options: ["Yes", "No"] });
    const s = suggest(f, b, "x.com");
    expect(s.every(x => ["Yes", "No"].includes(x.match.value[0]!))).toBe(true);
  });

  test("the same value from two saved questions shows once", () => {
    const b = bankOf(ans("python years experience", ["6"]), ans("experience years python programming", ["6"]));
    const s = suggest(field({ label: "Years of experience with Python libraries and Go" }), b, "x.com");
    expect(s.filter(x => x.match.value[0] === "6").length).toBeLessThanOrEqual(1);
  });

  test("a repeated-section entry never borrows another entry's answer", () => {
    const b = bankOf(ans("job title", ["Engineer"]), ans("job title vani section 2", ["Lead"]), ans("job title section 2", ["Old lead"]));
    for (const s of suggest(field({ label: "Previous job title (vani section 3)" }), b, "x.com")) {
      expect(s.question).not.toBe("job title");
      expect(s.question).not.toBe("job title vani section 2");
      expect(s.question).not.toBe("job title section 2");
    }
    // Entry 2 is offered entry 2's answers (new key and old key alike), never entry 1's.
    const two = suggest(field({ label: "Most recent job title held (vani section 2)" }), b, "x.com");
    expect(two.map(s => s.match.value[0]).sort()).toEqual(["Lead", "Old lead"]);
    expect(two.every(s => s.question !== "job title")).toBe(true);
  });
});

describe("a drafted answer is saved for review, never as confident", () => {
  test("short drafted text gets review=true; the same text typed by hand does not", () => {
    const drafted = mergeCapture(emptyBank(), [field({ label: "Why us?", kind: "textarea", value: ["Because of the team."], drafted: true })], "x.com", now);
    expect(drafted.bank.answers["why us"]!.review).toBe(true);
    const typed = mergeCapture(emptyBank(), [field({ label: "Why us?", kind: "textarea", value: ["Because of the team."] })], "x.com", now);
    expect(typed.bank.answers["why us"]!.review).toBe(false);
  });
});

describe("createDrafter", () => {
  test("one draft at a time: a second request while one runs is refused, not queued", async () => {
    let release!: (v: unknown) => void;
    const transport = () => new Promise<unknown>(r => { release = r; });
    const draft = createDrafter(transport, 5_000);
    const job = { company: "Acme", title: "Eng", description: "d".repeat(60) };
    const first = draft("Why us?", job);
    const second = await draft("Why us?", job);
    expect(!second.ok && second.code).toBe("busy");
    release({ draft: "ok", model: "m", evidence_items: 0 });
    expect((await first).ok).toBe(true);
    // Free again afterwards, including after a failure.
    const fail = createDrafter(async () => { throw new Error("down"); }, 5_000);
    expect((await fail("Why us?", job)).ok).toBe(false);
    expect(((await fail("Why us?", job)) as { code: string }).code).toBe("unreachable");
  });
});

describe("draftTransport (Chrome native port)", () => {
  const g = globalThis as Record<string, unknown>;
  const saved = { chrome: g.chrome, browser: g.browser };
  afterEach(() => { g.chrome = saved.chrome; g.browser = saved.browser; });

  function fakePort(onPost: (port: FakePort, msg: object) => void) {
    const state = { disconnected: 0, posted: [] as object[] };
    const port: FakePort = {
      msgL: [], discL: [],
      postMessage(m) { state.posted.push(m); onPost(port, m); },
      disconnect() { state.disconnected++; },
      onMessage: { addListener(cb) { port.msgL.push(cb); } },
      onDisconnect: { addListener(cb) { port.discL.push(cb); } },
    };
    g.browser = undefined;
    g.chrome = { runtime: { connectNative: (name: string) => { expect(name).toBe("dev.vbcr.vani_apply"); return port; }, lastError: undefined } };
    return state;
  }
  interface FakePort {
    msgL: Array<(m: unknown) => void>; discL: Array<() => void>;
    postMessage(m: object): void; disconnect(): void;
    onMessage: { addListener(cb: (m: unknown) => void): void };
    onDisconnect: { addListener(cb: () => void): void };
  }

  test("posts once, resolves with the first reply and closes the port", async () => {
    const st = fakePort((p, _m) => queueMicrotask(() => { for (const l of p.msgL) l({ draft: "hi" }); for (const l of p.msgL) l({ draft: "late" }); }));
    expect(await draftTransport({ op: "draft" })).toEqual({ draft: "hi" });
    expect(st.posted.length).toBe(1);
    expect(st.disconnected).toBe(1);
  });

  test("a port that closes before replying rejects (host missing or crashed)", async () => {
    fakePort(p => queueMicrotask(() => { for (const l of p.discL) l(); }));
    await expect(draftTransport({ op: "draft" })).rejects.toThrow(/disconnected/);
  });
});
