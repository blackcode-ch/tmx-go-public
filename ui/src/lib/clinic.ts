// "Clinic patient data" (the owner, 3 Nov: patient data of the clinics stays local, nLPD): a recording tagged
// sensitivity "clinic" has its report written by local models only. The Options checkbox sets it for the next
// recordings (kept in this window); the recording page sets it while recording and on the report.
import type { Recording } from "@/lib/recording-types";

export const CLINIC_LABEL = "Clinic patient data: summarize locally only";
export const CLINIC_HINT = "The report is written on our own machines, never by a cloud model. Without a local model it fails and says why.";

const KEY = "tmx.rec.clinic";

type Store = Pick<Storage, "getItem" | "setItem" | "removeItem">;
function store(): Store | null {
  try {
    return globalThis.localStorage ?? null;
  } catch {
    return null;
  }
}

/** whether new recordings are tagged clinic (the Options checkbox) */
export function clinicDefault(s: Store | null = store()): boolean {
  try {
    return s?.getItem(KEY) === "1";
  } catch {
    return false;
  }
}

export function setClinicDefault(on: boolean, s: Store | null = store()): void {
  try {
    if (on) s?.setItem(KEY, "1");
    else s?.removeItem(KEY);
  } catch {
    /* a window without storage: the checkbox applies to this recording only */
  }
}

/** the API value for the checkbox */
export function sensitivityOf(on: boolean): "clinic" | "" {
  return on ? "clinic" : "";
}

export function isClinic(r: Pick<Recording, "sensitivity"> | null | undefined): boolean {
  return r?.sensitivity === "clinic";
}
