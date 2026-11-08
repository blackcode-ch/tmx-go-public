import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';

function worker(windows = []) {
  const events = {}, shown = [], opened = [];
  const self = {
    location: { origin: 'https://phone.example.ts.net' },
    addEventListener: (name, fn) => { events[name] = fn; },
    registration: { showNotification: async (title, options) => shown.push({ title, options }) },
    clients: { matchAll: async () => windows, openWindow: async url => opened.push(url), claim: async () => {} },
    skipWaiting() {},
  };
  runInNewContext(readFileSync(new URL('../public/push-sw.js', import.meta.url), 'utf8'), { self, URL, crypto: { randomUUID: () => 'random' } });
  const fire = async (type, args = {}) => {
    let pending;
    events[type]({ ...args, waitUntil: p => { pending = p; } });
    await pending;
  };
  return { fire, shown, opened };
}

test('push shows only generic text and opaque incident; malformed payload still alerts', async () => {
  const w = worker();
  const id = 'a'.repeat(64);
  await w.fire('push', { data: { json: () => ({ title: 'secret command', category: 'approvals', incident: id, url: 'https://evil.test' }) } });
  assert.equal(w.shown[0].title, 'Approval needed');
  assert.equal(w.shown[0].options.tag, id);
  assert.equal(w.shown[0].options.body, 'Open Tiamatrix to review');
  assert.equal(JSON.stringify(w.shown).includes('secret command'), false);
  await w.fire('push', { data: { json: () => { throw Error('invalid'); } } });
  assert.equal(w.shown.length, 2);
});

test('click opens only this daemon inbox; renewal does not enroll without owner', async () => {
  let focused = false, navigated = '', renewal;
  const w = worker([{ url: 'https://phone.example.ts.net/#/settings', navigate: async url => { navigated = url; }, focus: async () => { focused = true; }, postMessage: data => { renewal = data; } }]);
  await w.fire('notificationclick', { notification: { close() {}, data: { path: 'https://evil.test' } } });
  assert.equal(navigated, 'https://phone.example.ts.net/#/inbox');
  assert.equal(focused, true);
  assert.equal(w.opened.length, 0);
  await w.fire('pushsubscriptionchange');
  assert.equal(renewal.type, 'push-renewal-needed');
  const empty = worker();
  await empty.fire('notificationclick', { notification: { close() {} } });
  assert.deepEqual(empty.opened, ['https://phone.example.ts.net/#/inbox']);
});
