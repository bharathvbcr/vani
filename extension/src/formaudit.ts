// Audit form fields for missed fillings, unattached required files, and invalid inputs.
// Allows applicants to immediately spot what is missing before submitting and jump to it.
import type { FieldRef } from "./extract";

export type MissedReason = "empty_required" | "invalid" | "unattached_file" | "manual_action";

export interface MissedFilling {
  id: string;
  label: string;
  kind: string;
  reason: MissedReason;
  description: string;
  refIndex: number;
  frameId?: number;
}

export interface FormAuditReport {
  totalFields: number;
  missedCount: number;
  missed: MissedFilling[];
  fileMissing: boolean;
  isReadyToSubmit: boolean;
}

/** Check if an HTML element currently has a non-empty, valid value. */
export function hasValue(ref: FieldRef): boolean {
  const el = ref.els[0];
  if (!el) return false;

  // If already extracted non-empty values into info (e.g. combobox, split-date, split-phone)
  if (ref.info.value && ref.info.value.length > 0 && ref.info.value.some(v => v.trim() !== "")) {
    return true;
  }

  if (ref.info.kind === "checkbox") {
    // If it's a checkbox group or single checkbox, check if at least one is checked
    return ref.els.some(
      e =>
        (e as HTMLInputElement).checked === true ||
        e.getAttribute("aria-checked") === "true" ||
        e.getAttribute("data-checked") === "true" ||
        e.classList?.contains("checked") ||
        e.classList?.contains("is-checked")
    );
  }

  if (ref.info.kind === "radio") {
    return ref.els.some(
      e =>
        (e as HTMLInputElement).checked === true ||
        e.getAttribute("aria-checked") === "true" ||
        e.getAttribute("data-checked") === "true"
    );
  }

  if (ref.info.kind === "file") {
    const input = el as HTMLInputElement;
    if (input.files && input.files.length > 0) return true;
    if (el.getAttribute("data-file-attached")) return true;
    // Check if immediate container shows an uploaded file pill / preview
    const container = typeof el.closest === "function"
      ? (el.closest(".file-upload, [data-automation-id*='file'], [class*='upload'], [class*='dropzone'], fieldset, [role='group']") ?? el.parentElement)
      : el.parentElement;
    if (container) {
      const otherInputs = container.querySelectorAll("input[type='file']");
      if (otherInputs.length <= 1 && container.querySelector(".attached-document, .file-name, [data-automation-id*='file-upload-item'], [class*='uploaded'], [class*='file-preview']")) {
        return true;
      }
    }
    return false;
  }

  if (ref.info.kind === "select") {
    const sel = el as HTMLSelectElement;
    if (!sel.options || sel.selectedIndex < 0) return false;
    const opt = sel.options[sel.selectedIndex];
    const val = opt?.value?.trim() ?? "";
    const txt = opt?.text?.trim() ?? "";
    return val !== "" && !/^(select|choose|pick|--+)/i.test(txt);
  }

  // Text, textarea, or contenteditable
  if (ref.widget === "contenteditable") {
    return (el.textContent ?? "").trim().length > 0;
  }

  const val = (el as HTMLInputElement).value ?? el.textContent ?? "";
  return val.trim().length > 0;
}

/** Check if the field is flagged invalid by HTML5 or ARIA formatting. */
export function isInvalid(ref: FieldRef): boolean {
  const el = ref.els[0];
  if (!el) return false;
  if (el.getAttribute("aria-invalid") === "true") return true;
  if (el.classList?.contains("invalid") || el.classList?.contains("is-invalid") || el.classList?.contains("error")) {
    return true;
  }
  if ("validity" in el && typeof (el as HTMLInputElement).validity === "object") {
    const v = (el as HTMLInputElement).validity;
    if (v.badInput || v.patternMismatch || v.typeMismatch || v.rangeOverflow || v.rangeUnderflow || v.stepMismatch || v.tooLong || v.tooShort || v.customError) {
      return true;
    }
  } else if ("checkValidity" in el && typeof (el as HTMLInputElement).checkValidity === "function") {
    try {
      if (hasValue(ref) && !(el as HTMLInputElement).checkValidity()) return true;
    } catch {}
  }
  return false;
}

/** Audit all fields from a form scan and compile a missed fillings report. */
export function auditForm(refs: FieldRef[]): FormAuditReport {
  const missed: MissedFilling[] = [];
  let fileMissing = false;

  refs.forEach((ref, idx) => {
    const label = ref.info.label.trim() || ref.info.name || `Field #${idx + 1}`;
    const required = ref.info.required;
    const filled = hasValue(ref);
    const invalid = isInvalid(ref);

    if (ref.info.kind === "file" && required && !filled) {
      fileMissing = true;
      missed.push({
        id: `missed-${idx}`,
        label,
        kind: ref.info.kind,
        reason: "unattached_file",
        description: `Required file "${label}" is not attached.`,
        refIndex: idx,
      });
      return;
    }

    if (ref.manual && required && !filled) {
      missed.push({
        id: `missed-${idx}`,
        label,
        kind: ref.info.kind,
        reason: "manual_action",
        description: `Widget "${label}" requires manual input (${ref.manual}).`,
        refIndex: idx,
      });
      return;
    }

    if (required && !filled) {
      missed.push({
        id: `missed-${idx}`,
        label,
        kind: ref.info.kind,
        reason: "empty_required",
        description: `Required field "${label}" is empty.`,
        refIndex: idx,
      });
      return;
    }

    if (invalid) {
      missed.push({
        id: `missed-${idx}`,
        label,
        kind: ref.info.kind,
        reason: "invalid",
        description: `Field "${label}" has an invalid value.`,
        refIndex: idx,
      });
      return;
    }
  });

  return {
    totalFields: refs.length,
    missedCount: missed.length,
    missed,
    fileMissing,
    isReadyToSubmit: missed.length === 0,
  };
}

const activePulses = new WeakMap<HTMLElement, {
  timer: ReturnType<typeof setTimeout>;
  origOutline: string;
  origBoxShadow: string;
  origTransition: string;
}>();

/** Smoothly scroll to a field and apply a temporary visual pulse to locate it. */
export function jumpToField(ref: FieldRef): void {
  const el = ref.els[0];
  if (!el) return;

  // If input element is invisible or 0x0 (e.g. styled file upload or hidden select), target its visible wrapper
  const isZeroSize = ("offsetWidth" in el && el.offsetWidth === 0 && el.offsetHeight === 0);
  const target: HTMLElement = (isZeroSize && el.parentElement)
    ? ((typeof el.closest === "function" ? el.closest("label, [class*='upload'], [class*='field'], [class*='container'], [data-automation-id]") as HTMLElement | null : null) ?? el.parentElement)
    : el;

  try {
    target.scrollIntoView({ behavior: "smooth", block: "center" });
    if ("focus" in target && typeof target.focus === "function") {
      target.focus({ preventScroll: true });
    } else if ("focus" in el && typeof el.focus === "function") {
      el.focus({ preventScroll: true });
    }
  } catch {
    target.scrollIntoView();
  }

  // Visual pulse highlight with state tracking to avoid stuck outline on rapid clicks
  const existing = activePulses.get(target);
  let origOutline = target.style.outline;
  let origBoxShadow = target.style.boxShadow;
  let origTransition = target.style.transition;

  if (existing) {
    clearTimeout(existing.timer);
    origOutline = existing.origOutline;
    origBoxShadow = existing.origBoxShadow;
    origTransition = existing.origTransition;
  }

  target.style.transition = "all 0.2s ease";
  target.style.outline = "3px solid #f59e0b";
  target.style.boxShadow = "0 0 16px rgba(245, 158, 11, 0.6)";

  const timer = setTimeout(() => {
    target.style.outline = origOutline;
    target.style.boxShadow = origBoxShadow;
    target.style.transition = origTransition;
    activePulses.delete(target);
  }, 2200);

  activePulses.set(target, { timer, origOutline, origBoxShadow, origTransition });
}
