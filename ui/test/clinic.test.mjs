import test from "node:test";
import assert from "node:assert/strict";
import { CLINIC_LABEL, clinicDefault, isClinic, sensitivityOf, setClinicDefault } from "../src/lib/clinic.ts";

function memory() {
  const m = new Map();
  return { getItem: (k) => (m.has(k) ? m.get(k) : null), setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k) };
}

test("clinic: the Options checkbox is kept for the next recordings and sent as sensitivity", () => {
  const s = memory();
  assert.equal(clinicDefault(s), false, "off by default: any model may write the report");
  setClinicDefault(true, s);
  assert.equal(clinicDefault(s), true);
  assert.equal(sensitivityOf(clinicDefault(s)), "clinic");
  setClinicDefault(false, s);
  assert.equal(clinicDefault(s), false);
  assert.equal(sensitivityOf(false), "");
  assert.equal(clinicDefault(null), false, "no storage: off");
  const broken = { getItem: () => { throw new Error("denied"); }, setItem: () => { throw new Error("denied"); }, removeItem: () => {} };
  assert.doesNotThrow(() => setClinicDefault(true, broken));
  assert.equal(clinicDefault(broken), false);
});

test("clinic: a recording is clinic only when tagged so, in plain words", () => {
  assert.equal(isClinic({ sensitivity: "clinic" }), true);
  assert.equal(isClinic({ sensitivity: "" }), false);
  assert.equal(isClinic({}), false);
  assert.equal(isClinic(null), false);
  assert.match(CLINIC_LABEL, /^Clinic patient data: summarize locally only$/);
});
