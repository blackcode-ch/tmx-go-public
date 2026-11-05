// Features page: grouping, filtering, counting (src/lib/features-format.ts).
import test from "node:test";
import assert from "node:assert/strict";
import { areaLabel, cleanFeatures, filterFeatures, groupByArea, statusCounts } from "../src/lib/features-format.ts";

const f = (id, area, status, extra = {}) => ({ id, title: `T ${id}`, area, status, evidence: "", updated: "2026-11-04", ...extra });
const rows = [
  f("F-10", "Chat", "works", { evidence: "PR #12" }),
  f("F-2", "Chat", "missing"),
  f("F-3", "Agents", "partial"),
  f("F-1", "Chat", "partial", { evidence: "test run 3 Nov" }),
  f("F-4", "", "works"),
];

test("statusCounts counts each status", () => {
  assert.deepEqual(statusCounts(rows), { works: 2, partial: 2, missing: 1, untested: 0, blocked: 0 });
});

test("filterFeatures: status and search combine; search is case-insensitive over evidence too", () => {
  assert.equal(filterFeatures(rows, "all", "").length, 5);
  assert.deepEqual(filterFeatures(rows, "partial", "").map((r) => r.id), ["F-3", "F-1"]);
  assert.deepEqual(filterFeatures(rows, "all", " pr #12 ").map((r) => r.id), ["F-10"]);
  assert.deepEqual(filterFeatures(rows, "works", "chat").map((r) => r.id), ["F-10"]);
  assert.deepEqual(filterFeatures(rows, "missing", "zzz"), []);
});

test("groupByArea: areas A-Z, blank area is Other, missing first then numeric id order", () => {
  const g = groupByArea(rows);
  assert.deepEqual(g.map((x) => x.area), ["Agents", "Chat", "Other"]);
  assert.deepEqual(g[1].features.map((r) => r.id), ["F-2", "F-1", "F-10"]);
});

test("cleanFeatures drops bad rows and fills missing text", () => {
  const out = cleanFeatures([null, 5, { id: "a", title: "x", status: "bogus" }, { id: "b", title: "y", status: "works" }]);
  assert.deepEqual(out, [{ id: "b", title: "y", area: "", status: "works", evidence: "", updated: "" }]);
  assert.equal(cleanFeatures([{ id: 122, title: "n", status: "blocked" }])[0].id, "122");
  assert.deepEqual(cleanFeatures({ not: "a list" }), []);
});

test("untested and blocked are statuses of their own and sort between missing and works", () => {
  const mixed = [f("F-1", "A", "works"), f("F-2", "A", "untested"), f("F-3", "A", "blocked"), f("F-4", "A", "partial"), f("F-5", "A", "missing")];
  assert.deepEqual(statusCounts(mixed), { works: 1, partial: 1, missing: 1, untested: 1, blocked: 1 });
  assert.deepEqual(groupByArea(mixed)[0].features.map((r) => r.id), ["F-5", "F-3", "F-4", "F-2", "F-1"]);
  assert.deepEqual(filterFeatures(mixed, "blocked", "").map((r) => r.id), ["F-3"]);
});

test("areaLabel makes a slug readable", () => {
  assert.equal(areaLabel("messages-and-decisions"), "Messages and decisions");
  assert.equal(areaLabel("core"), "Core");
  assert.equal(areaLabel("  "), "Other");
});
