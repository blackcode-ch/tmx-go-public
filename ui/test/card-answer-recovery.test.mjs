import test from "node:test";
import assert from "node:assert/strict";
import { recoverCardAnswers, canRecoverCardAnswer } from "../src/lib/card-answer-recovery.ts";
import { useCardAnswers, useCardDrafts, saveCardAnswer, saveCardDraft, cardAnswerKey, cardDictationDrafts, clearUnchangedCardWords, beginCardSend, endCardSend } from "../src/lib/card-answers.ts";

const scope = "https://original.test";
const card = { id: "question", kind: "question", version: 4, state: "pending", card: { type: "decision", category: "naming" }, question: "Which name?" };
const key = cardAnswerKey(scope, card.id);
function setup(extra = {}) {
  const data = new Map();
  globalThis.sessionStorage = { getItem: k => data.get(k) ?? null, setItem: (k,v) => data.set(k,v) };
  useCardAnswers.setState({items:{}}); useCardDrafts.setState({items:{}});
  const saved = {scope, card, request: {decision:"answer", version:4, answer_id:"stable-id", answer:"Full answer", free_text:true}, ...extra};
  saveCardAnswer(key,saved); saveCardDraft(scope,card,saved.request.answer);
  return saved;
}
function io(overrides = {}) { return {scope,active:()=>true,list:async()=>[card],post:async()=>({...card,state:"answered"}),accepted:()=>{},now:()=>100, ...overrides}; }

test("reconnect retries the exact saved request after a fresh read and clears only delivered words",async()=>{
  const saved=setup(); const calls=[];
  await recoverCardAnswers(io({list:async()=>{calls.push("list");return [card]},post:async(id,req)=>{
    calls.push("post"); assert.equal(id,card.id); assert.equal(req,saved.request);
    cardDictationDrafts(scope,card).set(key,"Full answer\nLate dictation");
    return {...card,state:"answered"};
  },accepted:()=>calls.push("accepted")}));
  assert.deepEqual(calls,["list","post","accepted"]);
  assert.equal(useCardAnswers.getState().items[key],undefined);
  assert.equal(useCardDrafts.getState().items[key].text,"Full answer\nLate dictation");
});

test("changed, closed, parked, missing, risky and permission cards never auto-submit",async()=>{
  const saved=setup();
  for(const current of [undefined,{...card,version:5},{...card,state:"answered"},{...card,deferred:true},{...card,kind:"permission"},{...card,tool:"terminal"},...['money','security','outward','deletion','legal','tax','strategy'].map(category=>({...card,card:{type:"decision",category}})),{...card,card:{type:"human_action"}},{...card,card:{type:"decision",action:{}}}]) {
    assert.equal(canRecoverCardAnswer(saved,current),false);
    await recoverCardAnswers(io({list:async()=>current?[current]:[],post:async()=>assert.fail("must remain manual")}));
    assert.equal(useCardAnswers.getState().items[key],saved);
  }
  assert.equal(canRecoverCardAnswer({...saved,request:{...saved.request,decision:"allow"}},card),false);
});

test("failed list, changed daemon and concurrent manual send cannot trigger recovery",async()=>{
  setup(); const post=async()=>assert.fail("must not post");
  await recoverCardAnswers(io({list:async()=>{throw Error("offline")},post}));
  let active=true;
  await recoverCardAnswers(io({active:()=>active,list:async()=>{active=false;return [card]},post}));
  await recoverCardAnswers(io({scope:"https://other.test",post}));
  assert.equal(beginCardSend(key),true);
  try { await recoverCardAnswers(io({post})); } finally { endCardSend(key); }
  assert.ok(useCardAnswers.getState().items[key]);
});

test("authentication rejection is retained and stops background retry across reload",async()=>{
  setup(); let posts=0;
  const request=io({post:async()=>{posts++;throw Object.assign(Error("Owner confirmation required"),{status:428})}});
  await recoverCardAnswers(request); await recoverCardAnswers({...request,now:()=>100000});
  assert.equal(posts,1);
  const saved=useCardAnswers.getState().items[key];
  assert.equal(saved.blocked,true); assert.equal(saved.request.answer_id,"stable-id");
  const reloaded=await import("../src/lib/card-answers.ts?blocked");
  assert.equal(reloaded.useCardAnswers.getState().items[key].blocked,true);
});

test("transient failures back off without changing request identity",async()=>{
  setup(); let posts=0;
  const request=io({post:async()=>{posts++;throw Error("offline")}});
  await recoverCardAnswers(request); await recoverCardAnswers(request);
  assert.equal(posts,1);
  assert.equal(useCardAnswers.getState().items[key].retryAt,5100);
  await recoverCardAnswers({...request,now:()=>5100});
  assert.equal(posts,2);
  assert.equal(useCardAnswers.getState().items[key].request.answer_id,"stable-id");
});

test("slow copy clears only its captured draft, preserving arriving dictation",()=>{
  const saved=setup(); const draft=useCardDrafts.getState().items[key];
  cardDictationDrafts(scope,card).set(key,"Full answer plus late words");
  clearUnchangedCardWords(key,saved,draft);
  assert.equal(useCardAnswers.getState().items[key],undefined);
  assert.equal(useCardDrafts.getState().items[key].text,"Full answer plus late words");
  clearUnchangedCardWords(key,undefined,useCardDrafts.getState().items[key]);
  assert.equal(useCardDrafts.getState().items[key],undefined);
});

test("an answer response cannot erase a draft reviewed against a newer question",async()=>{
  setup();
  await recoverCardAnswers(io({post:async()=>{
    saveCardDraft(scope,{...card,version:5,question:"Revised question"},"Full answer",true);
    return {...card,state:"answered"};
  }}));
  assert.equal(useCardDrafts.getState().items[key].card.version,5);
  assert.equal(useCardDrafts.getState().items[key].text,"Full answer");
});

test("successful recovery clears the unchanged submitted draft",async()=>{
  setup();
  await recoverCardAnswers(io());
  assert.equal(useCardAnswers.getState().items[key],undefined);
  assert.equal(useCardDrafts.getState().items[key],undefined);
});
