import test from "node:test";
import assert from "node:assert/strict";
import { HttpTransport } from "../src/lib/transport.ts";

test("remote event and terminal sockets keep credentials out of URLs", (t) => {
  const sockets = [];
  t.mock.method(globalThis, "WebSocket", function (url, protocols) {
    sockets.push({ url, protocols });
    return {};
  });
  const transport = new HttpTransport({ baseURL: "https://trial.example.ts.net", token: "test-owner", platform: "test" });
  transport.openEvents({});
  transport.openTerminal("/api/agents/test/terminal", 80, 24, {});
  assert.equal(sockets.length, 2);
  for (const socket of sockets) {
    assert.ok(socket.url.startsWith("wss://trial.example.ts.net/"));
    assert.ok(!socket.url.includes("test-owner"));
    assert.equal(new URL(socket.url).searchParams.has("token"), false);
    assert.deepEqual(socket.protocols, ["tmx", "bearer.test-owner"]);
  }
  assert.equal(new URL(sockets[1].url).searchParams.get("cols"), "80");
});
