import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { stripTypeScriptTypes } from 'node:module';
import { bytesBase64 } from '../src/lib/rec-upload.ts';

const source = readFileSync(new URL('../src/lib/recorder.ts', import.meta.url), 'utf8');
const transport = readFileSync(new URL('../src/lib/transport.ts', import.meta.url), 'utf8');
const between = (start, end) => source.slice(source.indexOf(start), source.indexOf(end, source.indexOf(start)));
// Execute the production handlers without starting the browser application/store.
const code = stripTypeScriptTypes(`
let state = { phase: 'recording', base: 0 };
const useCapture = { getState: () => state };
const setLocal = (next) => Object.assign(state, next);
const localElapsed = () => 1000;
const recApi = { setState: async () => ({ id: 'r' }) };
const upsertRecording = () => {};
export let finishedNote = '';
const finish = (_rec, note) => { finishedNote = note; };
export const snapshot = () => state;
const MAX_BACKLOG_BYTES = 256 << 20;
const MAX_BACKLOG_MS = 10 * 60_000;
const MAX_FRAMES = 400;
const bytesBase64 = (bytes) => Buffer.from(bytes).toString('base64');
class ApiError extends Error {
  constructor(status, message, data) { super(message); this.status = status; this.data = data; }
}
const window = { location: { origin: 'https://phone.invalid' } };
const parseJSON = JSON.parse;
const errorFromBody = (status, text) => new ApiError(status, '', JSON.parse(text));
let lockedHook;
export class HttpTransport {
  constructor(token = '', baseURL = window.location.origin) {
    Object.assign(this, { token, baseURL, apiBase: baseURL });
  }
  ${transport.slice(transport.indexOf('  async recordingRequest'), transport.indexOf('  private wsURL'))}
}
export class Harness {
  constructor() {
    state = { phase: 'recording', base: 0 };
    Object.assign(this, { rec: { id: 'r', kind: 'audio' }, settings: {}, mime: '', stream: {},
      recorders: new Set(), seg: 1, segBytes: 0, maxChunk: 0, ended: false,
      pendingMedia: new Set(), mediaClosed: false, mediaReads: Promise.resolve(), captured: [], queue: [], frameQ: [], framesAt: new Set(), frameCount: 0 });
  }
  enqueue(n, track, data, _size, at) { this.captured.push({ n, track, data, at }); }
  abort(message) { this.ended = true; this.error = message; this.discardPendingMedia(); }
  clearTimers() {}
  flushPCM() {}
  release() { this.releasedAt = this.captured.length; this.discardPendingMedia(); }
  drainFrames() {}
  offUpdates() {}
  pumpFrames() {}
  ${between('  private newRecorder(', '  /** this segment')}
  ${between('  private updateBacklog()', '  private async pump()')}
  ${between('  private async send(c:', '  // ---- still frames:')}
  ${between('  private async frame(', '  /** Every 2 s:')}
  ${between('  async stop()', '  /** Ends the capture')}
}
${source.slice(source.indexOf('function blobBase64('))}
`);
const runtime = await import(`data:text/javascript;base64,${Buffer.from(code).toString('base64')}`);

const { Harness, HttpTransport } = runtime;

test('base64 handles empty, binary, block boundaries and large buffers without Blob/FileReader', () => {
  for (const length of [0, 1, 2, 3, 24575, 24576, 24577, 2 * 1024 * 1024 + 1]) {
    const bytes = Uint8Array.from({ length }, (_, i) => i % 256);
    assert.equal(bytesBase64(bytes), Buffer.from(bytes).toString('base64'));
  }
  assert.doesNotMatch(between('  private async send(c:', '  // ---- still frames:'), /new Blob/);
});

class FakeRecorder extends EventTarget {
  state = 'recording';
  stop() {
    if (this.finalBlob) this.ondataavailable({ data: this.finalBlob });
    this.state = 'inactive';
    this.onstop();
    this.dispatchEvent(new Event('stop'));
  }
}
const delay = () => new Promise((resolve) => setImmediate(resolve));

test('media conversion preserves event order and queues only bytes', async () => {
  const previous = globalThis.MediaRecorder;
  globalThis.MediaRecorder = FakeRecorder;
  try {
    const h = new Harness();
    const mr = h.newRecorder(1);
    let resolveFirst;
    let reads = 0;
    mr.ondataavailable({ data: { size: 1, arrayBuffer: () => { reads++; return new Promise((r) => { resolveFirst = r; }); } } });
    mr.ondataavailable({ data: { size: 1, arrayBuffer: async () => { reads++; return Uint8Array.of(2).buffer; } } });
    await delay();
    assert.equal(h.captured.length, 0);
    const arrived = Date.now() - 10_000;
    [...h.pendingMedia][0].at = arrived;
    resolveFirst(Uint8Array.of(1).buffer);
    await h.mediaReads;
    assert.equal(reads, 2);
    assert.equal(h.captured[0].at, arrived);
    assert.deepEqual(h.captured.map((x) => [...x.data]), [[1], [2]]);
    assert.ok(h.captured.every((x) => x.data instanceof Uint8Array));
  } finally { globalThis.MediaRecorder = previous; }
});

test('Stop waits for the final Blob conversion before releasing capture', async () => {
  const previous = globalThis.MediaRecorder;
  globalThis.MediaRecorder = FakeRecorder;
  try {
    const h = new Harness();
    const mr = h.newRecorder(1);
    let finishRead;
    mr.finalBlob = { size: 1, arrayBuffer: () => new Promise((resolve) => { finishRead = resolve; }) };
    const stopping = h.stop();
    await delay();
    assert.equal(h.releasedAt, undefined);
    finishRead(Uint8Array.of(9).buffer);
    await stopping;
    assert.equal(h.releasedAt, 1);
    assert.deepEqual([...h.captured[0].data], [9]);
  } finally { globalThis.MediaRecorder = previous; }
});

test('still frames queue bytes after one Blob read', async () => {
  const h = new Harness();
  let reads = 0;
  h.grabber = { grab: async () => ({ arrayBuffer: async () => { reads++; return Uint8Array.of(255, 216).buffer; } }) };
  await h.frame(1000);
  assert.equal(reads, 1);
  assert.deepEqual([...h.frameQ[0].data], [255, 216]);
  assert.equal(h.frameQ[0].blob, undefined);
});

test('HTTP uploads bytes directly and consumes success/error acknowledgement bodies', async () => {
  const previous = globalThis.fetch;
  const bytes = Uint8Array.of(1, 2, 3);
  try {
    for (const status of [200, 409]) {
      const response = new Response('{"error":"gap","size":12}', { status });
      globalThis.fetch = async (_url, options) => {
        assert.equal(options.body, bytes);
        assert.equal(options.headers.Authorization, undefined);
        return response;
      };
      const send = new HttpTransport().recordingRequest('PUT', '/chunk', bytes, 'application/octet-stream');
      if (status === 200) await send;
      else await assert.rejects(send, (e) => e.status === 409 && e.data.size === 12);
      assert.equal(response.bodyUsed, true);
    }
  } finally { globalThis.fetch = previous; }
});

test('recorder preserves bearer uploads and falls back to an authenticated same-origin cookie after a 401', async () => {
  const previous = globalThis.fetch;
  const headers = [];
  const bytes = Uint8Array.of(1, 2, 3);
  try {
    globalThis.fetch = async (_url, options) => {
      headers.push(options.headers.Authorization);
      assert.equal(options.body, bytes);
      return headers.length === 2 ? new Response('{}', { status: 401 }) : new Response('{}');
    };
    const t = new HttpTransport('fixture');
    for (let i = 0; i < 3; i++) await t.recordingRequest('PUT', '/chunk', bytes, 'application/octet-stream');
    assert.deepEqual(headers, ['Bearer fixture', 'Bearer fixture', undefined, undefined]);
  } finally { globalThis.fetch = previous; }
});

test('300 one-MiB chunks behind a stalled first read hit the shared cap and release pending Blobs', async () => {
  const previous = globalThis.MediaRecorder;
  globalThis.MediaRecorder = FakeRecorder;
  try {
    const h = new Harness();
    const mr = h.newRecorder(1);
    let resolveFirst;
    let laterReads = 0;
    mr.ondataavailable({ data: { size: 1 << 20, arrayBuffer: () => new Promise((r) => { resolveFirst = r; }) } });
    await delay();
    assert.equal(runtime.snapshot().backlog, 1 << 20);
    const holders = [...h.pendingMedia];
    for (let i = 1; i < 300; i++) {
      mr.ondataavailable({ data: { size: 1 << 20, arrayBuffer: async () => { laterReads++; return new ArrayBuffer(1); } } });
      for (const holder of h.pendingMedia) if (!holders.includes(holder)) holders.push(holder);
    }
    assert.equal(h.ended, true);
    assert.match(h.error, /What arrived before is kept/);
    assert.equal(runtime.snapshot().backlog, 257 * (1 << 20));
    assert.equal(h.pendingMedia.size, 0);
    assert.ok(holders.every((holder) => holder.blob === null));
    assert.equal(h.captured.length, 0);
    resolveFirst(new ArrayBuffer(1));
    await delay();
    assert.equal(laterReads, 0);
    assert.equal(h.captured.length, 0);
  } finally { globalThis.MediaRecorder = previous; }
});

test('pending and converted bytes share one cap, and pending age counts toward ten minutes', async () => {
  for (const expired of [false, true]) {
    const h = new Harness();
    const holder = { blob: {}, size: 129 << 20, at: Date.now() - (expired ? 600_001 : 0) };
    h.pendingMedia.add(holder);
    if (!expired) h.queue.push({ size: 128 << 20, at: Date.now() });
    h.updateBacklog();
    assert.equal(h.ended, true);
    assert.equal(holder.blob, null);
  }
});

test('Stop times out at twenty-five seconds and hands unread data to recovery', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const previous = globalThis.MediaRecorder;
  globalThis.MediaRecorder = FakeRecorder;
  try {
    const h = new Harness();
    const mr = h.newRecorder(1);
    mr.ondataavailable({ data: new Blob(['saved']) });
    await h.mediaReads;
    let resolveRead;
    mr.ondataavailable({ data: { size: 1024, arrayBuffer: () => new Promise((r) => { resolveRead = r; }) } });
    await delay();
    // Three stalled reads: the first two fall back at 10 s and 20 s;
    // Stop's deadline hands the third to recovery at 25 s.
    for (let i = 0; i < 2; i++) mr.ondataavailable({ data: { size: 1024, arrayBuffer: () => new Promise(() => {}) } });
    const holder = [...h.pendingMedia][2];
    const stopping = h.stop();
    await delay();
    t.mock.timers.tick(10_000);
    await delay();
    t.mock.timers.tick(10_000);
    await delay();
    t.mock.timers.tick(4_999);
    await delay();
    assert.equal(h.releasedAt, undefined);
    t.mock.timers.tick(1);
    await stopping;
    assert.equal(h.captured.length, 3);
    assert.equal(h.ended, true);
    assert.equal(holder.blob, null);
    assert.equal(h.pendingMedia.size, 0);
    assert.match(h.error, /Some media could not be read/);
    resolveRead(new ArrayBuffer(1024));
    await delay();
    assert.equal(h.captured.length, 3);
  } finally { globalThis.MediaRecorder = previous; }
});

test('a stalled conversion falls back to its original Blob after ten seconds, in order', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const previous = globalThis.MediaRecorder;
  globalThis.MediaRecorder = FakeRecorder;
  try {
    const h = new Harness();
    const mr = h.newRecorder(1);
    const blob = new Blob(['first']);
    let resolveRead;
    blob.arrayBuffer = () => new Promise((resolve) => { resolveRead = resolve; });
    mr.ondataavailable({ data: blob });
    mr.ondataavailable({ data: new Blob(['second']) });
    await delay();
    t.mock.timers.tick(9_999);
    await delay();
    assert.equal(h.captured.length, 0);
    t.mock.timers.tick(1);
    await h.mediaReads;
    assert.equal(h.captured[0].data, blob);
    assert.equal(new TextDecoder().decode(h.captured[1].data), 'second');
    assert.equal(h.pendingMedia.size, 0);
    resolveRead(new ArrayBuffer(5));
    await delay();
    assert.equal(h.captured.length, 2);
  } finally { globalThis.MediaRecorder = previous; }
});

test('fallback sends the original Blob over HTTP and the legacy FileReader bridge', async () => {
  const previousFetch = globalThis.fetch;
  const previousReader = globalThis.FileReader;
  const blob = new Blob(['fallback']);
  const chunk = { n: 1, track: 'media', offset: 0, dur: 1000, data: blob };
  try {
    const h = new Harness();
    let response;
    globalThis.fetch = async (_url, options) => {
      assert.equal(options.body, blob);
      response = new Response('{}');
      return response;
    };
    h.t = new HttpTransport();
    await h.send(chunk);
    assert.equal(response.bodyUsed, true);
    globalThis.FileReader = class {
      readAsDataURL(input) {
        assert.equal(input, blob);
        this.result = 'data:audio/webm;base64,ZmFsbGJhY2s=';
        queueMicrotask(() => this.onload());
      }
    };
    let sent;
    h.t = { kind: 'bridge', request: async (_method, _path, body) => { sent = body; } };
    await h.send(chunk);
    assert.equal(sent.data, 'ZmFsbGJhY2s=');
  } finally { globalThis.fetch = previousFetch; globalThis.FileReader = previousReader; }
});
