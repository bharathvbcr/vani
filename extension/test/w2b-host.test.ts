// Slice w2b (232): the disabled-host comparison.
import { describe, expect, test } from "bun:test";
import { hostDisabled } from "../src/bank";
import { type Bank, emptyBank } from "../src/types";

describe("232: hostDisabled", () => {
  const bank = (hosts: string[]): Bank => ({ ...emptyBank(), settings: { captureSelfId: false, disabledHosts: hosts } });
  test("matches ignoring case, spaces and trailing dots; an empty host or list matches nothing", () => {
    expect(hostDisabled(bank(["Jobs.Example."]), "jobs.example")).toBe(true);
    expect(hostDisabled(bank(["jobs.example"]), "JOBS.EXAMPLE..")).toBe(true);
    expect(hostDisabled(bank([" a.b "]), "a.b")).toBe(true);
    expect(hostDisabled(bank(["jobs.example"]), "")).toBe(false);
    expect(hostDisabled(bank([""]), "")).toBe(false);
    expect(hostDisabled(bank([]), "jobs.example")).toBe(false);
    expect(hostDisabled(bank(["jobs.example"]), "notjobs.example")).toBe(false);
    expect(hostDisabled(bank(["jobs.example"]), "sub.jobs.example")).toBe(false);
  });
});
