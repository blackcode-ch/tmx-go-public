import test from "node:test";
import assert from "node:assert/strict";
import { ownerDeliveryLabel, withPendingOwnerMessages } from "../src/lib/owner-delivery.ts";

test("an owner bubble stays queued until submission is confirmed, then shows delivery time", () => {
  const m = { state: "queued", receipt: "queued", created_at: 1 };
  const time = (at) => at === 3 ? "20:17" : "wrong time";
  assert.equal(ownerDeliveryLabel(m, time, true), "queued");
  assert.equal(ownerDeliveryLabel({ ...m, state: "delivered", receipt: "dispatching", delivered_at: 2 }, time), "sending");
  assert.equal(ownerDeliveryLabel({ ...m, state: "delivered", receipt: "submitted", delivered_at: 2, receipt_at: 3 }, time), "delivered 20:17");
  assert.equal(ownerDeliveryLabel({ ...m, state: "delivered", receipt: "unconfirmed" }, time), "delivery uncertain");
});

test("a timed-out owner dispatch surfaces the retry warning until real submission", () => {
 const m = {state:"delivered",receipt:"stalled",created_at:1,delivered_at:2,receipt_at:62000};
 assert.equal(ownerDeliveryLabel(m,String),"not delivered, retry");
 assert.equal(ownerDeliveryLabel({...m,receipt:"submitted",receipt_at:63000},String),"delivered 63000");
});


test("a menu hold is distinguished from offline queueing", () => {
 assert.equal(ownerDeliveryLabel({ state: "delivered", receipt: "menu_wait", created_at: 1 }, String), "waiting for menu to close");
});

test("unconfirmed claims never advertise delivery", () => {
 for (const receipt of [undefined, "queued", "dispatching", "menu_wait", "accepted", "unconfirmed", "unexpected"]) {
  assert.doesNotMatch(ownerDeliveryLabel({state:"delivered",receipt,created_at:1,delivered_at:2},String), /^delivered /);
 }
});
test("pending messages appear once in the same conversation and retain their status", () => {
 const message = {id:"m1",from:"owner",to:"head",body:"hello",created_at:2,state:"queued",receipt:"queued"};
 const entries = [{seq:1,agent_id:"head",at:1,kind:"assistant",text:"working"}];
 const visible = withPendingOwnerMessages(entries,[message],"head");
 assert.equal(visible.length,2);
 assert.equal(visible[1].text,"hello");
 assert.equal(visible[1].meta.message_id,"m1");
 assert.equal(withPendingOwnerMessages(visible,[message],"head").length,2);
 assert.equal(withPendingOwnerMessages(entries,[{...message,to:"other"}],"head").length,1);
});

test("a ready agent's unclaimed message remains queued", () => {
 const m = {state:"queued",receipt:"queued",created_at:1};
 assert.equal(ownerDeliveryLabel(m,String),"queued");
 assert.equal(ownerDeliveryLabel(m,String,true),"queued");
});

test("the API sending state is never displayed as delivered on phone or desktop", () => {
 const pending = {id:"m",from:"owner",to:"a",body:"hi",created_at:1,state:"sending",receipt:"dispatching",delivered_at:0};
 for (const unavailable of [false,true]) assert.equal(ownerDeliveryLabel(pending,String,unavailable),"sending");
 assert.equal(withPendingOwnerMessages([], [pending], "a").length,1);
 assert.equal(ownerDeliveryLabel({...pending,state:"delivered",receipt:"submitted",receipt_at:12,delivered_at:12},String),"delivered 12");
});
