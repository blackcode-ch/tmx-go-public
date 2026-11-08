// People / Clients / Promises pages (src/lib/people-format.ts): filtering, grouping, parsing.
import test from "node:test";
import assert from "node:assert/strict";
import { bucketOf, forPage, groupPromises, keyFromName, parseWhen, validKey, whenLabel } from "../src/lib/people-format.ts";

const people = [
  { key: "bo", name: "Bo", kind: "client", updated_at: 0 },
  { key: "ana", name: "Ana", kind: "team", updated_at: 0 },
  { key: "cy", kind: "external", updated_at: 0 },
];

test("People shows team and external, Clients only clients", () => {
  assert.deepEqual(forPage(people, "people").map((p) => p.key), ["ana", "cy"]);
  assert.deepEqual(forPage(people, "clients").map((p) => p.key), ["bo"]);
});

test("a key from a name", () => {
  assert.equal(keyFromName("Anna Müller"), "anna-muller");
  assert.equal(keyFromName("  -Zoë O'Brien "), "zoe-o-brien");
  assert.ok(validKey(keyFromName("Jean-Luc Picard")));
  assert.ok(!validKey("Bad Key"));
});

test("promises grouped by urgency", () => {
  const today = "2026-11-08";
  const p = (id, due, status = "open") => ({ id, person_key: "ana", title: id, due, status, created_at: 0, updated_at: 0 });
  assert.equal(bucketOf(p("a", "2026-11-01"), today), "overdue");
  assert.equal(bucketOf(p("a", today), today), "today");
  assert.equal(bucketOf(p("a", "2026-11-14"), today), "soon");
  assert.equal(bucketOf(p("a", "2026-12-24"), today), "later");
  assert.equal(bucketOf(p("a", ""), today), "nodate");
  assert.equal(bucketOf(p("a", "2026-11-01", "done"), today), "closed");
  const groups = groupPromises([p("x", ""), p("y", "2026-11-01"), p("z", "2026-10-01"), p("w", "", "dropped")], today);
  assert.deepEqual(groups.map((g) => g.label), ["Overdue", "No date", "Closed"]);
  assert.deepEqual(groups[0].items.map((i) => i.id), ["z", "y"]);
});

test("reminder times", () => {
  const now = new Date(2026, 10, 8, 12, 0).getTime();
  assert.equal(parseWhen("30m", now), now + 30 * 60_000);
  assert.equal(parseWhen("2h", now), now + 2 * 3_600_000);
  assert.equal(parseWhen("3d", now), now + 3 * 86_400_000);
  assert.equal(parseWhen("2026-11-09", now), new Date(2026, 10, 9, 9, 0).getTime());
  assert.equal(parseWhen("2026-11-09 14:30", now), new Date(2026, 10, 9, 14, 30).getTime());
  assert.equal(parseWhen("2026-02-30", now), null);
  assert.equal(parseWhen("2020-01-01", now), null);
  assert.equal(parseWhen("soon", now), null);
  assert.equal(parseWhen("0m", now), null);
  assert.equal(whenLabel(now + 2 * 3_600_000, now), "in 2 h");
  assert.equal(whenLabel(now - 5 * 60_000, now), "5 min ago");
});
