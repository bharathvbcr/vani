import { describe, expect, test } from "bun:test";
import { emptyBank, type Bank, type FieldInfo } from "../src/types";
import type { FieldRef } from "../src/extract";
import { fillAll } from "../src/fill";
import { mergeCapture } from "../src/bank";
import {
  ANSWER_KEY_BY_JARVIS, MAX_CONTACT_ASKS, NOT_RUN_NOTE, contactItems, jarvisKeyOf, mayAskContactKey,
  omitContactAnswers, readChoice, readGet, readImport,
} from "../src/contacts";
import { exportBank } from "../src/bank";

const secret = "contact-secret-example-9f3a";

function info(over: Partial<FieldInfo>): FieldInfo {
  return { kind: "text", label: "", name: "", autocomplete: "", automationId: "", options: [], required: false, value: [], ...over };
}

class Ev {
  constructor(public type: string) {}
}

class Doc {
  hidden = false;
  body = { append() {} };
  defaultView = {
    Event: Ev,
    InputEvent: Ev,
    HTMLInputElement: class {},
    HTMLTextAreaElement: class {},
    requestAnimationFrame: (cb: () => void) => { cb(); return 1; },
  };
}

class Box {
  localName = "input";
  type = "text";
  value = "";
  isConnected = true;
  style: Record<string, string> = {};
  attrs: Record<string, string> = {};
  ownerDocument: Doc;
  constructor(doc: Doc) { this.ownerDocument = doc; }
  getAttribute(name: string): string | null { return this.attrs[name] ?? null; }
  setAttribute(name: string, value: string): void { this.attrs[name] = value; }
  dispatchEvent(): boolean { return true; }
  focus(): void {}
  blur(): void {}
}

function ref(box: Box, field: Partial<FieldInfo>): FieldRef {
  return { info: info(field), els: [box as unknown as HTMLElement], widget: "native", manual: "" };
}

function bankWithEmail(): Bank {
  const b = emptyBank();
  const captured = mergeCapture(b, [info({ kind: "email", label: "Email", autocomplete: "email", value: [secret] })], "vani-basics", new Date());
  captured.bank.answers["visa sponsorship"] = {
    key: "visa sponsorship", labels: ["Will you require visa sponsorship?"], kind: "select", value: ["No"],
    sensitivity: "standard", signatures: [], uses: 1, fills: 0, updatedAt: new Date().toISOString(), review: false,
  };
  return captured.bank;
}

describe("catalog", () => {
  test("autocomplete and contact labels map to jarvis keys, and other labels do not", () => {
    expect(jarvisKeyOf(info({ autocomplete: "given-name", label: "Legal first name" }))).toBe("given_name");
    expect(jarvisKeyOf(info({ label: "Email address" }))).toBe("email");
    expect(jarvisKeyOf(info({ label: "GitHub profile" }))).toBe("github");
    expect(jarvisKeyOf(info({ label: "Sales region" }))).toBe("");
    expect(jarvisKeyOf(info({ label: "Will you require visa sponsorship?" }))).toBe("");
  });

  test("work authorization, consent, self-id and secrets are not asked", () => {
    expect(mayAskContactKey(info({ label: "Will you now or in the future require visa sponsorship?" }))).toBe(false);
    expect(mayAskContactKey(info({ label: "I agree to the privacy policy" }))).toBe(false);
    expect(mayAskContactKey(info({ label: "Gender" }))).toBe(false);
    expect(mayAskContactKey(info({ label: "Date of birth" }))).toBe(false);
    expect(mayAskContactKey(info({ label: "National ID" }))).toBe(false);
    expect(mayAskContactKey(info({ kind: "password", label: "Password" }))).toBe(false);
    expect(mayAskContactKey(info({ label: "Portfolio URL" }))).toBe(true);
  });

  test("a one-time copy lists bank basics, and an export after that omits them", () => {
    const bank = bankWithEmail();
    expect(contactItems(bank).map(i => i.key)).toContain("email");
    expect(contactItems(bank).some(i => i.text === secret)).toBe(true);
    const exported = exportBank(omitContactAnswers(bank));
    expect(exported).not.toContain(secret);
    expect(exported).toContain("visa sponsorship");
    expect(omitContactAnswers(bank).answers.email).toBeUndefined();
    expect(ANSWER_KEY_BY_JARVIS.email).toBe("email");
  });

  test("a missing bank is not an empty catalog or a filled value", () => {
    const missing = readGet({ error: "inputs read failed: bank executable is missing", code: "E_JARVIS" });
    expect(missing.status).toBe("not_run");
    expect(missing.note).toContain("bank executable is missing");
    expect(missing.text).toBeUndefined();
    const empty = readChoice({ status: "empty" });
    expect(empty.status).toBe("empty");
    const refused = readChoice({ status: "filled", key: "Ada", text: secret });
    expect(refused.status).toBe("not_run");
    expect(readChoice({ status: "filled", key: "website", text: secret, review: true })).toMatchObject({ status: "filled", review: true });
    expect(readImport({ error: "jarvis executable is missing", code: "unavailable" }).ok).toBe(false);
    expect(readImport({ stored: ["email"], failed: [{ key: "phone", code: "E_JARVIS", error: "inputs read failed: bank executable is missing" }] }).note)
      .toContain("bank executable is missing");
  });
});

describe("fill", () => {
  const logs: string[] = [];
  const orig = console.log;
  console.log = (...args: unknown[]) => { logs.push(args.map(String).join(" ")); orig(...args); };

  function box(): Box { return new Box(new Doc()); }

  test("a known contact label is filled from get, in code, and the value is not logged", async () => {
    logs.length = 0;
    const el = box();
    const calls: string[] = [];
    const out = await fillAll([ref(el, { label: "Email", kind: "email", autocomplete: "email" })], emptyBank(), "jobs.example", {
      contacts: {
        get: async key => { calls.push("get:" + key); return { status: "value", text: secret }; },
        choose: async () => { calls.push("choose"); return { status: "empty" }; },
      },
    });
    expect(calls).toEqual(["get:email"]);
    expect(out[0]).toMatchObject({ filled: true, review: false, key: "email" });
    expect(el.value).toBe(secret);
    expect(el.attrs["data-vani-why"]).toBe("contact");
    expect(el.attrs["data-vani-why"]).not.toContain(secret);
    expect(logs.join("\n")).not.toContain(secret);
  });

  test("a missing bank does not fill and does not look like no keys", async () => {
    const el = box();
    let chose = 0;
    const out = await fillAll([ref(el, { label: "Email", autocomplete: "email", kind: "email" })], bankWithEmail(), "jobs.example", {
      contacts: {
        get: async () => ({ status: "not_run", note: `${NOT_RUN_NOTE}: bank executable is missing` }),
        choose: async () => { chose++; return { status: "empty" }; },
      },
    });
    expect(out[0]).toMatchObject({ filled: false, reason: "not_run" });
    expect(out[0]?.detail).toContain("bank executable is missing");
    expect(el.value).toBe("");
    expect(chose).toBe(0);
  });

  test("an empty catalog does not ask the model, and a non-key reply stays empty", async () => {
    const portfolio = box();
    const calls: string[] = [];
    const out = await fillAll([ref(portfolio, { label: "Portfolio URL" })], emptyBank(), "jobs.example", {
      contacts: {
        get: async key => { calls.push(key); return { status: "missing" }; },
        choose: async () => { calls.push("choose"); return { status: "empty" }; },
      },
    });
    expect(calls).toEqual(["choose"]);
    expect(out[0]).toMatchObject({ filled: false, reason: "no_match" });
    expect(out[0]?.reason).not.toBe("not_run");

    const again = box();
    const refused = await fillAll([ref(again, { label: "Portfolio URL" })], emptyBank(), "jobs.example", {
      contacts: {
        get: async () => ({ status: "missing" }),
        choose: async () => ({ status: "refused" }),
      },
    });
    expect(refused[0]).toMatchObject({ filled: false, reason: "no_match" });
    expect(again.value).toBe("");
  });

  test("abstain-then-a-key is written yellow, and a model that cannot be asked is not a fill", async () => {
    logs.length = 0;
    const el = box();
    const out = await fillAll([ref(el, { label: "Where can we read your writing?" })], emptyBank(), "jobs.example", {
      contacts: {
        get: async () => ({ status: "missing" }),
        choose: async () => ({ status: "filled", key: "website", text: secret, review: true }),
      },
    });
    expect(out[0]).toMatchObject({ filled: true, review: true, key: "website" });
    expect(el.attrs["data-vani-fill"]).toBe("review");
    expect(el.attrs["data-vani-why"]).not.toContain(secret);
    expect(logs.join("\n")).not.toContain(secret);

    const down = box();
    const missed = await fillAll([ref(down, { label: "Where can we read your writing?" })], emptyBank(), "jobs.example", {
      contacts: {
        get: async () => ({ status: "missing" }),
        choose: async () => ({ status: "not_run", note: NOT_RUN_NOTE }),
      },
    });
    expect(missed[0]).toMatchObject({ filled: false, reason: "not_run" });
    expect(missed[0]?.detail).toContain(NOT_RUN_NOTE);
    expect(down.value).toBe("");
  });

  test("work authorization still fills from the saved answer and is not sent to the model", async () => {
    const el = box();
    let chose = 0;
    const bank = bankWithEmail();
    const out = await fillAll([ref(el, { label: "Will you require visa sponsorship?", kind: "text" })], bank, "jobs.example", {
      contacts: {
        get: async () => ({ status: "missing" }),
        choose: async () => { chose++; return { status: "filled", key: "email", text: secret, review: true }; },
      },
    });
    expect(chose).toBe(0);
    expect(out[0]?.filled).toBe(true);
    expect(out[0]?.key).not.toBe("email");
    expect(el.value).toBe("No");
  });

  test("a page stops asking after the cap, and the rest did not run", async () => {
    let n = 0;
    const refs = Array.from({ length: MAX_CONTACT_ASKS + 1 }, (_, i) => ref(box(), { label: `Hobby ${i}` }));
    const out = await fillAll(refs, emptyBank(), "jobs.example", {
      contacts: {
        get: async () => ({ status: "missing" }),
        choose: async () => { n++; return { status: "none" }; },
      },
    });
    expect(n).toBe(MAX_CONTACT_ASKS);
    expect(out[MAX_CONTACT_ASKS]).toMatchObject({ filled: false, reason: "not_run" });
    expect(out[MAX_CONTACT_ASKS]?.detail).toContain(NOT_RUN_NOTE);
    expect(out.slice(0, MAX_CONTACT_ASKS).every(o => o.reason === "no_match")).toBe(true);
  });
});
