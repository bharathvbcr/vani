import type { FieldInfo, Sensitivity } from "./types";
import { normalizeLabel } from "./normalize";

// Fields whose values must never be stored or filled by this extension.
const SECRET_LABEL = new RegExp(
  "\\b(" + [
    "ssn", "social security", "social insurance", "national insurance", "passport", "tax id", "tin",
    "itin", "ein", "drivers? licen[cs]e", "driving licen[cs]e", "bank", "routing", "iban", "swift",
    "account number", "credit card", "debit card", "card number", "cvv", "cvc", "expiry", "expiration",
    "date of birth", "birth date", "birthdate", "dob", "password", "passcode", "pin", "captcha",
    "security question", "mother s maiden", "maiden name", "one time code", "verification code",
  ].join("|") + ")\\b",
);

// Voluntary self-identification: stored and filled only when the user opts in.
const SELF_ID_LABEL = new RegExp(
  "\\b(" + [
    "gender", "sex", "race", "racial", "ethnicity", "ethnic", "hispanic", "latino", "latina", "latinx",
    "veteran", "military status", "disability", "disabled", "sexual orientation", "transgender",
    "pronouns?", "lgbtq", "self identify", "self identification", "eeo", "voluntary",
  ].join("|") + ")\\b",
);

export function classify(f: Pick<FieldInfo, "kind" | "label" | "name" | "autocomplete">): Sensitivity {
  if (f.kind === "password") return "secret";
  const ac = f.autocomplete;
  if (ac.startsWith("cc-") || /\b(cc-|current-password|new-password|one-time-code|bday)/.test(ac)) return "secret";
  const text = normalizeLabel(`${f.label} ${f.name.replace(/[_\-[\]]+/g, " ")}`);
  if (SECRET_LABEL.test(text)) return "secret";
  if (SELF_ID_LABEL.test(text)) return "self_id";
  return "standard";
}

/** Kinds the extension never reads values from or writes values to. */
export function untouchable(kind: FieldInfo["kind"]): boolean {
  return kind === "file" || kind === "password" || kind === "hidden" || kind === "other";
}
