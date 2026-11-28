import test from 'node:test';
import assert from 'node:assert/strict';
import { build } from 'esbuild';
import { fileURLToPath } from 'node:url';

// Real recorder code against controlled Web Audio objects; no microphone/browser/model.
const result = await build({
  entryPoints: [fileURLToPath(new URL('../src/lib/dictation.ts', import.meta.url))],
  bundle: true, write: false, format: 'esm', platform: 'node', logLevel: 'silent',
  plugins: [{ name: 'recorder-dependencies', setup(b) {
    b.onResolve({ filter: /^\.\/(voice|audio-capture-lease)$/ }, a => ({ path: a.path, namespace: 'fake' }));
    b.onLoad({ filter: /.*/, namespace: 'fake' }, a => ({ contents: a.path.endsWith('/voice')
      ? 'export const voiceApi = {};'
      : 'export const audioCaptureLease = async () => globalThis.captureLease();' }));
  } }],
});
const { startDictation } = await import(`data:text/javascript;base64,${Buffer.from(result.outputFiles[0].text).toString('base64')}`);

function environment(options = {}) {
  const contexts = [], streams = [], worklets = [];
  let leases = 0;
  globalThis.captureLease = () => { leases++; return () => { leases--; }; };
  const doc = new EventTarget();
  Object.assign(doc, { baseURI: 'https://phone.example/', hidden: false });
  globalThis.document = doc;
  Object.defineProperty(globalThis, 'navigator', { configurable: true, value: { mediaDevices: {
    async getUserMedia() {
      if (options.denied) throw new Error('denied');
      const tracks = [new EventTarget(), new EventTarget()];
      for (const tr of tracks) { tr.stopped = 0; tr.stop = () => { tr.stopped++; }; }
      const stream = { getTracks: () => tracks, getAudioTracks: () => tracks };
      streams.push(stream);
      return stream;
    },
  } } });
  class Node {
    disconnected = false;
    connect() { if (options.connectError) throw new Error('connect failed'); }
    disconnect() { this.disconnected = true; }
  }
  globalThis.AudioContext = class {
    sampleRate = 16000;
    state = 'suspended';
    destination = {};
    nodes = [];
    closeCalls = 0;
    audioWorklet = options.fallback ? undefined : { addModule: async () => {} };
    constructor() { if (options.contextError) throw new Error('context failed'); contexts.push(this); }
    async resume() { if (options.resumeError) throw new Error('resume failed'); this.state = 'running'; }
    node() { const n = new Node(); this.nodes.push(n); return n; }
    createMediaStreamSource() { return this.node(); }
    createGain() { return Object.assign(this.node(), { gain: { value: 1 } }); }
    createScriptProcessor() { return this.processor = this.node(); }
    async close() {
      this.closeCalls++;
      if (options.holdClose) await new Promise(resolve => { this.finishClose = resolve; });
      this.state = 'closed';
      if (options.closeError) throw new Error('already closed');
    }
  };
  globalThis.AudioWorkletNode = class extends Node {
    constructor(ctx) {
      super();
      this.port = { onmessage: null, closed: false,
        postMessage: () => {
          if (options.flushError) throw new Error('worklet died');
          if (!options.noFlushAck) this.port.onmessage?.({ data: 'flushed' });
        },
        close: () => { this.port.closed = true; },
      };
      ctx.nodes.push(this);
      worklets.push(this);
    }
  };
  return { contexts, streams, worklets, leases: () => leases,
    clean() {
      assert.equal(leases, 0, 'capture lease released');
      for (const s of streams) for (const tr of s.getTracks()) assert.equal(tr.stopped, 1, 'every track stopped once');
      for (const c of contexts) {
        assert.equal(c.closeCalls, 1, 'each AudioContext closed once');
        for (const n of c.nodes) assert.ok(n.disconnected, 'audio node disconnected');
      }
      for (const w of worklets) { assert.equal(w.port.onmessage, null); assert.ok(w.port.closed); }
    },
  };
}
const settle = () => new Promise(resolve => setImmediate(resolve));
const samples = seconds => new Float32Array(seconds * 16000).fill(0.1);
const duration = wav => (wav.length - 44) / 32000;

test('composer reports actual WAV samples for normal and silent captures before teardown', async () => {
  for (const signal of [0.1, 0]) {
    const env = environment();
    const rec = await startDictation();
    const track = env.streams[0].getAudioTracks()[0];
    Object.assign(track, { muted: false, readyState: 'live' });
    document.hidden = true;
    document.dispatchEvent(new Event('visibilitychange'));
    document.hidden = false;
    document.dispatchEvent(new Event('visibilitychange'));
    env.worklets[0].port.onmessage({ data: new Float32Array(16000).fill(signal) });
    if (!signal) {
      track.muted = true;
      track.dispatchEvent(new Event('mute'));
    }
    const wav = await rec.stop();
    assert.equal(duration(wav), 1);
    assert.equal(wav.subarray(44).some(value => value !== 0), signal !== 0);
    assert.deepEqual(JSON.parse(JSON.stringify(rec.captureMetadata())), {
      path: 'unknown', processor: 'worklet', track_muted: signal === 0, track_ended: false,
      ...(signal === 0 ? { interruption: 'mute' } : {}),
      hidden_at_start: false, hidden_events: 1, visible_events: 1,
      context_state: 'running', context_interruptions: 0, samples: 16000, duration_ms: 1000,
    });
    env.clean();
  }
});

test('recorder releases resources on every failure and next captures start fresh', async t => {
  for (const kind of ['denied', 'contextError', 'resumeError', 'connectError']) {
    await t.test(kind, async () => {
      const env = environment({ [kind]: true });
      await assert.rejects(startDictation());
      env.clean();
    });
  }
  await t.test('flush rejection cannot leave a green microphone', async () => {
    const env = environment({ flushError: true });
    const rec = await startDictation();
    await assert.rejects(rec.stop(), /worklet died/);
    rec.cancel(); // session's defensive finally is safe after a failed stop
    env.clean();
  });
  await t.test('no flush acknowledgement still closes', async () => {
    const env = environment({ noFlushAck: true });
    const rec = await startDictation();
    await rec.stop();
    env.clean();
  });
  await t.test('empty clip followed by two long clips does not reuse samples or end early', async () => {
    const env = environment();
    const empty = await startDictation();
    assert.equal(duration(await empty.stop()), 0); // no-voice result is processed after this
    env.clean();
    for (const seconds of [10, 15]) {
      const rec = await startDictation();
      assert.equal(rec.level(), 0, 'fresh level meter');
      const w = env.worklets.at(-1);
      w.port.onmessage({ data: samples(seconds) });
      assert.equal(env.streams.at(-1).getTracks()[0].stopped, 0, 'capture stays live until explicit stop');
      assert.equal(duration(await rec.stop()), seconds);
      env.clean();
    }
  });
  await t.test('cancel waits for native closure before opening another capture', async () => {
    const options = { holdClose: true };
    const env = environment(options);
    const rec = await startDictation();
    rec.cancel();
    const next = startDictation();
    await settle();
    assert.equal(env.streams.length, 1, 'no new stream during previous teardown');
    options.holdClose = false;
    env.contexts[0].finishClose();
    const second = await next;
    second.cancel();
    await settle();
    env.clean();
  });
  await t.test('fallback processor and rejected context close are cleaned', async () => {
    const env = environment({ fallback: true, closeError: true });
    const rec = await startDictation();
    const sp = env.contexts[0].processor;
    sp.onaudioprocess({ inputBuffer: { getChannelData: () => samples(10) } });
    assert.equal(duration(await rec.stop()), 10);
    assert.equal(sp.onaudioprocess, null);
    env.clean();
  });
});

test('mute then ended notifies once, stops all tracks and releases the context', async () => {
 const env=environment();const rec=await startDictation();let calls=0, stopped;
 rec.onEnded(()=>{calls++;stopped=rec.stop();});
 const track=env.streams[0].getAudioTracks()[0];
 track.dispatchEvent(new Event('mute'));track.dispatchEvent(new Event('ended'));
 await stopped;assert.equal(calls,1);env.clean();
 track.dispatchEvent(new Event('mute'));assert.equal(calls,1);
});
