import test from 'node:test';
import assert from 'node:assert/strict';
import {build} from 'esbuild';
import {fileURLToPath} from 'node:url';
const result=await build({stdin:{resolveDir:fileURLToPath(new URL('../src/lib/',import.meta.url)),contents:`export * from './dictation-session';export {acceptDictation,useDictationOutbox} from './dictation-outbox';`},bundle:true,write:false,format:'esm',platform:'node',logLevel:'silent',plugins:[{name:'capture-and-network',setup(b){
 b.onResolve({filter:/^(sonner|\.\/(dictation|store|router|voice))$/},a=>({path:a.path,namespace:'fake'}));
 b.onLoad({filter:/.*/,namespace:'fake'},a=>({contents:{
  sonner:'export const toast=Object.assign(()=>{},{error:()=>{}});',
  './dictation':`export class DictationError extends Error {};export const MAX_DICTATION_MS=120000;export const startDictation=async()=>globalThis.capture;export const toBase64=()=>"wav";export const insertAtCursor=()=>{throw Error("must not modify draft")};`,
  './store':`export const useStore={getState:()=>({status:'ready',transport:{baseURL:'computer',request:async()=>globalThis.answer}})};`,
  './voice':`export const useVoice={getState:()=>({status:{settings:{language:'auto'}}})};`,
  './router':'export const navigate=()=>{};',
 }[a.path]}));
}}]});
globalThis.window={addEventListener(){}};globalThis.document={activeElement:{blur(){},focus(){assert.fail('no keyboard');}}};
globalThis.capture={elapsed:()=>1000,onEnded(){},stop:async()=>new Uint8Array(44),captureMetadata:()=>undefined,cancel(){}};
const {startDictating,stopDictating,acceptDictation,useDictationOutbox}=await import('data:text/javascript;base64,'+Buffer.from(result.outputFiles[0].text).toString('base64'));
const settle=()=>new Promise(r=>setImmediate(r));
test('stop automatically sends transcript to original destination without touching typed draft',async()=>{
 const drafts=new Map([['a','already typed']]),sent=[];
 const target={id:'chat:a',label:'A',path:'/chat/a',drafts,draftKey:'a',send:async(text,id)=>sent.push({text,id})};
 globalThis.answer={text:'spoken message'};
 await startDictating(target);await stopDictating();await settle();
 assert.equal(sent.length,1);assert.equal(sent[0].text,'spoken message');assert.ok(sent[0].id);assert.equal(drafts.get('a'),'already typed');
 assert.deepEqual(useDictationOutbox.getState().clips,[]);
});
test('unsure transcript stays inline until Use text anyway sends once; draft stays untouched',async()=>{
 const sent=[],drafts=new Map([['b','typed separately']]);let release;
 globalThis.answer={text:'uncertain words',held:true,held_reason:'suspect'};
 await startDictating({id:'chat:b',label:'B',path:'/chat/b',drafts,draftKey:'b',send:async text=>{sent.push(text);await new Promise(r=>release=r);}});
 await stopDictating();await settle();assert.deepEqual(sent,[]);
 const held=useDictationOutbox.getState().notices['chat:b'];assert.equal(held.held,true);assert.equal(held.text,'uncertain words');assert.equal(held.data,'');
 const first=acceptDictation(held);await acceptDictation(held);release();await first;
 assert.deepEqual(sent,['uncertain words']);assert.equal(drafts.get('b'),'typed separately');assert.equal(useDictationOutbox.getState().notices['chat:b'],undefined);
});
test('send failure shows text inline instead of losing it or silently retrying',async()=>{
 globalThis.answer={text:'unsent words'};let calls=0;
 await startDictating({id:'chat:c',label:'C',path:'/chat/c',drafts:new Map(),draftKey:'c',send:async()=>{calls++;throw Error('offline');}});
 await stopDictating();await settle();
 const held=useDictationOutbox.getState().notices['chat:c'];assert.equal(held.heldReason,'send_failed');assert.equal(held.text,'unsent words');assert.equal(held.data,'');assert.equal(calls,1);
});
