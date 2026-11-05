import { forwardRef, useImperativeHandle, useRef, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";
import { DictateButton } from "@/components/dictate-button";
import { api, dropApproval } from "@/lib/api";
import { beginCardSend, endCardSend, cardSendFailed, clearUnchangedCardWords, boundedCardRequest, cardAnswerKey, cardDictationDrafts, clearCardAnswer, saveCardAnswer, saveCardDraft, useCardAnswers, useCardDrafts } from "@/lib/card-answers";
import { useStore } from "@/lib/store";
import { paths } from "@/lib/router";
import type { Approval, DecideBody } from "@/lib/types";
import type { ApprovalActionsHandle } from "./approval";

/** Owner questions only: never used for harness permissions, guard or access cards. */
export const OwnerCardActions = forwardRef<ApprovalActionsHandle, { a: Approval; size?: "sm" | "default" }>(
  function OwnerCardActions({ a, size = "sm" }, ref) {
    const scope = useStore((s) => s.transport?.baseURL || "");
    const current = useStore((s) => s.approvals[a.id]);
    const key = cardAnswerKey(scope, a.id);
    const draft = useCardDrafts((s) => s.items[key]);
    const pending = useCardAnswers((s) => s.items[key]);
    const [busy, setBusy] = useState(false);
    const [failure, setFailure] = useState("");
    const [missing, setMissing] = useState(false);
    const input = useRef<HTMLTextAreaElement>(null);
    const text = draft?.text || "";
    const original = draft?.card || a;
    const changed = !!current && (pending?.request.version ?? original.version) !== current.version;
    const closed = !current || current.state !== "pending";

    function edit(value: string) {
      try { saveCardDraft(scope, original, value); }
      catch { toast.error("Browser storage is unavailable. Keep this page open and copy your answer."); }
    }

    async function send(decision: DecideBody["decision"], answer?: string, freeText = false) {
      if (!beginCardSend(key)) return;
      setBusy(true);
      setFailure("");
      try {
        const saved = useCardAnswers.getState().items[key];
        const request = saved?.request || { decision, answer, free_text: freeText || undefined,
          version: original.version, answer_id: crypto.randomUUID() };
        if (!saved) saveCardAnswer(key, { scope, card: original, request });
        const submittedDraft = useCardDrafts.getState().items[key];
        const result = await boundedCardRequest(api.decide(a.id, request));
        if (useCardAnswers.getState().items[key]?.request.answer_id === request.answer_id) clearCardAnswer(key);
        // Dictation can finish during a send; only clear the words actually sent.
        if (request.free_text && submittedDraft?.text === request.answer && useCardDrafts.getState().items[key] === submittedDraft) saveCardDraft(scope, original, "");
        if (useStore.getState().transport?.baseURL === scope) {
          if (result.state === "pending") useStore.setState((s) => ({ approvals: { ...s.approvals, [result.id]: result } }));
          else dropApproval(a.id);
        }
        toast(request.decision === "defer" ? "Saved for later" : request.decision === "reask" ? "Question reopened" : "Answer delivered to the agent's mailbox");
      } catch (error) {
        try { cardSendFailed(key, error); } catch { /* Keep the saved request visible when storage fails. */ }
        setMissing((error as { status?: number }).status === 404);
        setFailure((error as Error).message || "That did not go through. Your words are saved.");
      } finally { endCardSend(key); setBusy(false); }
    }

    async function copyAndClose() {
      try {
        const capturedPending = useCardAnswers.getState().items[key];
        const capturedDraft = useCardDrafts.getState().items[key];
        const submitted = capturedPending?.request.answer || "";
        const text = capturedDraft?.text || "";
        await navigator.clipboard.writeText(submitted && text && submitted !== text ? `${submitted}\n\nAdditional saved draft:\n${text}` : submitted || text);
        clearUnchangedCardWords(key, capturedPending, capturedDraft);
        toast("Answer copied");
      } catch { toast.error("Could not copy. Your answer is still saved here."); }
    }

    function reviewCurrent() {
      if (!current || current.state !== "pending") return;
      try {
        saveCardDraft(scope, current, text || pending?.request.answer || "", true);
        clearCardAnswer(key);
        setFailure("");
      } catch { toast.error("Could not save the reviewed draft. Copy your answer before leaving."); }
    }

    useImperativeHandle(ref, () => ({ allow() {}, deny() {}, pick(i) {
      if (!pending && !changed && !closed && !a.deferred && !text && a.options?.[i]) void send("answer", a.options[i]);
    } }));

    if (pending) return <div className="space-y-2 text-sm" role="status">
      <p>{busy ? "Sending…" : missing ? "This card is no longer available. Your answer is saved." : changed ? "The question changed. Your original answer is saved." : "Saved answer — delivery is not confirmed."}</p>
      {pending.request.answer && <p className="whitespace-pre-wrap">{pending.request.answer}</p>}
      {text && text !== pending.request.answer && <p className="whitespace-pre-wrap">Additional saved draft: {text}</p>}
      {(failure || pending.error) && <p role="alert">{failure || pending.error}</p>}
      {!missing && <Button size={size} disabled={busy} onClick={() => void send(pending.request.decision)}>Retry</Button>}
      {changed && !closed && <Button size={size} disabled={busy} onClick={reviewCurrent}>Review answer for this question</Button>}
      {(pending.request.answer || text) ? <Button size={size} variant="ghost" disabled={busy} onClick={() => void copyAndClose()}>Copy answer and close</Button>
        : <Button size={size} variant="ghost" disabled={busy} onClick={() => clearCardAnswer(key)}>Dismiss saved choice</Button>}
    </div>;

    if (a.deferred && !text) return <Button size={size} disabled={busy} onClick={() => void send("reask")}>Ask me again</Button>;

    return <div className="space-y-2">
      {changed && <p role="status" className="text-sm">The question changed. Your words are saved; review the new question before sending.</p>}
      {closed && <p role="status" className="text-sm">This card closed or left the list. Your words are saved. Sending delivers correspondence, without changing a closed decision.</p>}
      {!closed && !changed && !text && <div className="flex flex-wrap gap-1.5">{a.options?.map((option) => <Button key={option} variant="outline" size={size} disabled={busy} onClick={() => void send("answer", option)}>{option}</Button>)}</div>}
      <form className="space-y-2" onSubmit={(e) => { e.preventDefault(); if (text.trim() && !changed) void send("answer", text, true); }}>
        <Textarea ref={input} value={text} onChange={(e) => edit(e.target.value)} aria-label="Your answer" placeholder="Your answer…" disabled={busy} />
        <div className="flex flex-wrap gap-1.5">
          <DictateButton target={input} value={text} onChange={edit} disabled={busy} draft={{ id: `card:${key}:${original.version}`, label: a.question || "this question", path: paths.inbox(), drafts: cardDictationDrafts(scope, original), draftKey: key }} />
          <Button type="submit" size={size} disabled={busy || !text.trim() || changed}>{closed ? "Send saved answer" : "Send"}</Button>
          {changed && <Button type="button" size={size} onClick={reviewCurrent}>Review answer for this question</Button>}
          {text && <Button type="button" size={size} variant="ghost" onClick={() => void copyAndClose()}>Copy answer and close</Button>}
          {!text && !closed && <Button type="button" size={size} variant="ghost" disabled={busy} onClick={() => void send(a.deferred ? "reask" : "defer")}>{a.deferred ? "Ask me again" : "Later"}</Button>}
        </div>
      </form>
    </div>;
  },
);
