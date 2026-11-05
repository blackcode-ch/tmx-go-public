import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

const html = readFileSync(new URL('../../internal/api/owner_bootstrap.html', import.meta.url), 'utf8');
const script = html.match(/<script>([\s\S]*?)<\/script>/)[1];

test('private link leaves history before exchange and requires explicit approval', async () => {
  const events = [];
  const elements = { status: {}, scope: {}, approve: { disabled: true }, code: { value: 'TESTCODE' } };
  runInNewContext(script, {
    URLSearchParams,
    location: { hash: '#bootstrap=synthetic-capability', pathname: '/owner-bootstrap', host: 'trial.example.ts.net' },
    history: { replaceState: (...args) => events.push(['history', ...args]) },
    document: { getElementById: id => elements[id] },
    fetch: async (path, options) => { events.push(['fetch', path, options]); return { ok: true, json: async () => ({}) }; },
  });
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(events[0][0], 'history');
  assert.equal(events[0][3], '/owner-bootstrap');
  assert.equal(events.length, 2);
  assert.equal(events[1][1], '/api/pair/bootstrap/exchange');
  assert.equal(JSON.parse(events[1][2].body).token, 'synthetic-capability');
  assert.equal(elements.approve.disabled, false);
  await elements.approve.onclick();
  assert.equal(events[2][1], '/api/pair/bootstrap/approve');
  assert.equal(JSON.parse(events[2][2].body).code, 'TESTCODE');
  assert.equal(elements.approve.disabled, true);
  assert.match(elements.status.textContent, /spent/);
});
