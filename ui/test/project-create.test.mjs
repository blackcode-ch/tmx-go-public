import test from "node:test";
import assert from "node:assert/strict";
import { createProjectPayload, projectLeadHarnesses } from "../src/lib/project-create.ts";

test("project creation sends the selected harness and keeps optional fields optional", () => {
  assert.deepEqual(createProjectPayload({ name: "billing", harness: "codex" }), {
    name: "billing",
    harness: "codex",
  });
});

test("project creation preserves a custom folder and brief with the selected harness", () => {
  assert.deepEqual(createProjectPayload({
    name: "billing",
    harness: "codex",
    folder: "D:/work/billing",
    brief: "Build the billing portal",
  }), {
    name: "billing",
    harness: "codex",
    folder: "D:/work/billing",
    brief: "Build the billing portal",
  });
});

test("project lead choices include chat harnesses and exclude terminal-only harnesses", () => {
  const harnesses = [
    { name: "claude", modes: ["chat", "terminal"], installed: true },
    { name: "codex", modes: ["chat", "terminal"], installed: false },
    { name: "shell", modes: ["terminal"], installed: true },
  ];
  assert.deepEqual(projectLeadHarnesses(harnesses).map((h) => h.name), ["claude", "codex"]);
});
