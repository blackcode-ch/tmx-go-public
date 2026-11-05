import test from "node:test";
import assert from "node:assert/strict";
import { HttpTransport, BridgeTransport } from "../src/lib/transport.ts";

for (const kind of ["http", "bridge"]) {
  test(`${kind} keeps grace in one transport instance and clears it on lock`, async (t) => {
    const calls = [];
    const answer = (path, nonce, credential) => {
      calls.push({ path, nonce, credential });
      return path.startsWith("/api/gate/confirmations/")
        ? JSON.stringify({ state: "confirmed", grace_credential: "synthetic-proof" }) : "{}";
    };
    if (kind === "http") t.mock.method(globalThis, "fetch", async (url, opts) => {
      return new Response(answer(new URL(url).pathname, opts.headers["X-Tmx-UI-Session"], opts.headers["X-Tmx-Grace"]));
    });
    const make = () => kind === "http"
      ? new HttpTransport({ baseURL: "https://example.invalid", token: "test-owner", platform: "test" })
      : new BridgeTransport({ RequestWithSession: async (_method, path, _body, nonce, credential) => answer(path, nonce, credential) }, {}, { url: "", token: "test-owner", platform: "test" });
    const first = make();
    await first.request("GET", "/api/gate/confirmations/test?wait=1");
    await first.request("PUT", "/api/gate/grace", { grace_minutes: 60 });
    assert.equal(calls.at(-1).credential, "synthetic-proof");
    const nonce = calls.at(-1).nonce;
    assert.match(nonce, /^[a-f0-9]{64}$/);
    await make().request("GET", "/api/gate");
    assert.equal(calls.at(-1).credential, "");
    assert.notEqual(calls.at(-1).nonce, nonce);
    await first.request("POST", "/api/gate/lock");
    await first.request("GET", "/api/gate");
    assert.equal(calls.at(-1).credential, "");
    assert.equal(calls.at(-1).nonce, nonce);
    assert.ok(calls.every(c => !c.path.includes("synthetic-proof")));
  });
}
