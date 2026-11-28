import test from 'node:test';
import assert from 'node:assert/strict';
import { build } from 'esbuild';
import { fileURLToPath } from 'node:url';

// Session logic only. Captures and storage are controlled; no real mic or API.
const result = await build({
  entryPoints: [fileURLToPath(new URL('../src/lib/dictation-session.ts', import.meta.url))],
  bundle: true, write: false, format: 'esm', platform: 'node', logLevel: 'silent',
  plugins: [{ name: 'session-dependencies', setup(b) {
    b.onResolve({ filter: /^(sonner|\.\/(dictation|dictation-outbox|router|store|voice))$/ }, a => ({ path: a.path, namespace: 'fake' }));
    b.onLoad({ filter: /.*/, namespace: 'fake' }, a => ({ contents: {
      'sonner': 'export const toast = Object.assign(() => {}, {error: () => {}});',
      './dictation': `export class DictationError extends Error {};
        export const MAX_DICTATION_MS=120000;
        export const startDictation=async()=>globalThis.fakeCapture;
        export const insertAtCursor=()=>({value:'',caret:0});
        export const toBase64=bytes=>Buffer.from(bytes).toString('base64');`,
      './dictation-outbox': `export const dictationScope=()=> 'https://phone.example';
        export const flushDictationOutbox=async()=>{};
        export const keepDictation=async clip=>globalThis.keepCapture(clip);
        export const setDictationDelivery=()=>{};`,
      './router': 'export const navigate=()=>{};',
      './store': 'export const useStore={getState:()=>({status:"ready"})};',
      './voice': 'export const useVoice={getState:()=>({status:{settings:{language:"auto"}}})};',
    }[a.path] }));
  } }],
});
let blurs=0;
globalThis.document={activeElement:{blur(){blurs++;}}};
globalThis.window = { addEventListener() {} };
const { startDictating, stopDictating, useDictation } = await import(`data:text/javascript;base64,${Buffer.from(result.outputFiles[0].text).toString('base64')}`);
const target = { id: 'agent:a', label: 'A', path: '/agents/a', drafts: new Map(), draftKey: 'a' };

test('short taps are logged and discarded, mic start blurs input, failed stop cleans up', async () => {
  const infos = [], saved = [];
  const original = console.info;
  console.info = (...args) => infos.push(args);
  try {
    let cancellations = 0;
    const wav = new Uint8Array(100); // no client energy gate, even for zero samples
    globalThis.fakeCapture = { elapsed: () => 100, onEnded() {}, stop: async () => wav,
      cancel: () => { cancellations++; } };
    globalThis.keepCapture = async clip => saved.push(clip);
    await startDictating(target);
    await stopDictating();
    assert.equal(saved.length, 0, '100ms tap is never saved');
    assert.equal(blurs, 1);
    assert.deepEqual(infos[0][1], { elapsed_ms: 100, audio_bytes: 100 }, 'only duration/size are logged');
    assert.equal(cancellations, 1);
    assert.equal(useDictation.getState().phase, 'idle');
    globalThis.fakeCapture.stop = async () => { throw new Error('flush failed'); };
    await startDictating(target);
    await stopDictating();
    assert.equal(cancellations, 2, 'failed stop still releases recorder');
    assert.equal(useDictation.getState().phase, 'idle');
    assert.equal(saved.length, 0);
  } finally { console.info = original; }
});

test('an already interrupted microphone does not leave the session recording or arm its limit', async () => {
 let cancellations=0;
 globalThis.fakeCapture={elapsed:()=>1000,onEnded:fn=>fn(),stop:async()=>new Uint8Array(44),captureMetadata:()=>undefined,cancel:()=>cancellations++};
 globalThis.keepCapture=async()=>{};
 await startDictating(target);
 await new Promise(resolve=>setImmediate(resolve));
 assert.equal(useDictation.getState().phase,'idle');assert.equal(cancellations,1);
});
