import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync,existsSync} from 'node:fs';
import {migrateDictationStores,DICTATION_DATABASES,DICTATION_WEB_KEYS} from '../src/lib/dictation-migration.ts';
import {MemoryDictations,DictationQueue} from '../src/lib/dictation-queue.ts';

// IndexedDB transaction harness: seeded records are removed only at commit.
function databaseFixture() {
 const databases=new Map([
 ['tmx-dictations',new Map([['clips',new Map(['blocked','failed','held','pending','delivered'].map((state,i)=>[state,{data:'wav',state,blocked:state==='blocked',held:state==='held',error:state==='failed'?'failed':undefined,delivered:state==='delivered',transcriptId:state==='pending'?'pending':undefined,target:{id:`chat:${i}`},scope:`computer:${i}`}]))]])],
 ['tmx-voice',new Map([['recs',new Map([['legacy-chat-a',{audio:'wav'}],['legacy-chat-b',{blocked:true}]])],['chunks',new Map([[1,{rec:'a',blob:'wav'}],[2,{rec:'b',blob:'wav'}]])]])],
 ]);
 const cleared=[];
 const indexedDB={open(name){
  const request={};queueMicrotask(()=>{
   const stores=databases.get(name);
   request.result={objectStoreNames:[...stores.keys()],close(){},transaction(names,mode){
    assert.equal(mode,'readwrite');const pending=[];const tx={objectStore(store){assert.ok(names.includes(store));return {clear(){pending.push(store);}};}};
    queueMicrotask(()=>{for(const store of pending){stores.get(store).clear();cleared.push(`${name}/${store}`);}tx.oncomplete();});return tx;
   }};request.onsuccess();
  });return request;
 }};
 return {databases,indexedDB,cleared};
}
const storage=()=>({rows:new Map(DICTATION_WEB_KEYS.map(k=>[k,'old state'])),removeItem(k){this.rows.delete(k);}});

test('first-load migration empties every named voice store and every chat, before new work',async()=>{
 const fixture=databaseFixture(),localStorage=storage(),sessionStorage=storage();
 localStorage.rows.set('owner-preference','keep');sessionStorage.rows.set('tmx.chat-composer-drafts','typed text');
 const cacheRows=new Map([['tmx-shell-v1',['old saved-recordings UI']],['tmx-shell-old',['older UI']],['unrelated',['keep']]]);
 const caches={keys:async()=>[...cacheRows.keys()],delete:async key=>cacheRows.delete(key)};
 const memory=new MemoryDictations();const queue=new DictationQueue(memory,{});
 for(const id of ['agent:a','chat:b','wa:c','companion','card:d'])await queue.enqueue({id,scope:'computer',target:{id},data:'wav',created:0,blocked:true});
 const view={clips:['saved'],notices:{a:'held text'},error:'old error'};
 await migrateDictationStores({...fixture,localStorage,sessionStorage,caches},async()=>{await queue.reset();memory.clear();view.clips=[];view.notices={};view.error='';});
 assert.deepEqual([...fixture.cleared].sort(),['tmx-dictations/clips','tmx-voice/chunks','tmx-voice/recs']);
 for(const stores of fixture.databases.values())for(const rows of stores.values())assert.equal(rows.size,0);
 assert.deepEqual(await memory.list(),[]);assert.deepEqual(view,{clips:[],notices:{},error:''});
 for(const key of DICTATION_WEB_KEYS){assert.equal(localStorage.rows.has(key),false);assert.equal(sessionStorage.rows.has(key),false);}
 assert.deepEqual([...cacheRows.keys()],['tmx-shell-v1','tmx-shell-old','unrelated']);
 assert.equal(localStorage.rows.get('owner-preference'),'keep');assert.equal(sessionStorage.rows.get('tmx.chat-composer-drafts'),'typed text');
 assert.deepEqual(DICTATION_DATABASES,['tmx-dictations','tmx-voice']);
});

test('local recovery stays in the composer without reviving legacy archives',()=>{
 const read=p=>readFileSync(new URL(p,import.meta.url),'utf8');
 const settings=read('../src/pages/settings.tsx');assert.doesNotMatch(settings,/DictationOutbox|saved dictation/i);
 assert.equal(existsSync(new URL('../src/components/dictation-outbox.tsx',import.meta.url)),false);
 const outbox=read('../src/lib/dictation-outbox.ts');assert.doesNotMatch(outbox,/IndexedDictations|retryDictation|restoreDictation|dismissDictation|captureSentDictations|navigator.locks/);
 const queue=read('../src/lib/dictation-queue.ts');assert.doesNotMatch(queue,/indexedDB|localStorage|sessionStorage/);
 const inline=read('../src/components/dictation-inline.tsx');assert.match(inline,/Retry transcription/);assert.doesNotMatch(inline,/Save audio|Restore to draft/);
 const sw=read('../public/push-sw.js');assert.ok(sw.includes("tmx-shell-v2-no-saved-dictation"));assert.match(sw,/caches.delete\(name\)/);
});

test('missing legacy database is not created by migration',async()=>{
 let aborted=0;
 const indexedDB={open(){const request={transaction:{abort(){aborted++;queueMicrotask(()=>request.onerror());}}};queueMicrotask(()=>request.onupgradeneeded({oldVersion:0}));return request;}};
 await migrateDictationStores({indexedDB},()=>{});assert.equal(aborted,2);
});
test('migration failure is reported rather than claiming old audio was cleared',async()=>{
 const indexedDB={open(){const request={error:new Error('blocked')};queueMicrotask(()=>request.onerror());return request;}};
 const warnings=[];await migrateDictationStores({indexedDB},()=>{},m=>warnings.push(m));assert.equal(warnings.length,2);
 const outbox=readFileSync(new URL('../src/lib/dictation-outbox.ts',import.meta.url),'utf8');assert.match(outbox,/void migrateDictationStores/);assert.doesNotMatch(outbox,/await migrateDictationStores/);
});
