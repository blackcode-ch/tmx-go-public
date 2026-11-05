import { beginCardSend, boundedCardRequest, cardSendFailed, clearCardAnswer, endCardSend, saveCardDraft, useCardAnswers, useCardDrafts, type PendingCardAnswer } from "./card-answers.ts";
import type { Approval, DecideBody } from "./types";

const deliberate = new Set(["money", "security", "outward", "deletion", "legal", "tax", "strategy"]);

/** Only a previously submitted answer to the same open, ordinary question may retry. */
export function canRecoverCardAnswer(saved: PendingCardAnswer, current: Approval | undefined): boolean {
  if (saved.blocked || saved.request.decision !== "answer" || !saved.request.answer?.trim()) return false;
  return !!current && current.id === saved.card.id && current.version === saved.request.version
    && current.state === "pending" && !current.deferred
    && [saved.card, current].every((card) => card.kind === "question" && !card.tool
      && card.card?.type === "decision" && !card.card.action && !deliberate.has(card.card.category || ""));
}

export interface CardRecoveryIO {
  scope: string;
  active(): boolean;
  list(): Promise<Approval[]>;
  // Raw transport only: a 401/403/428 stays saved, never opens a background PIN prompt.
  post(id: string, request: DecideBody): Promise<Approval>;
  accepted(card: Approval): void;
  now?: () => number;
}

export async function recoverCardAnswers(io: CardRecoveryIO): Promise<void> {
  if (!io.active()) return;
  const now = io.now?.() ?? Date.now();
  const candidates = Object.entries(useCardAnswers.getState().items).filter(([, p]) =>
    p.scope === io.scope && !p.blocked && (p.retryAt || 0) <= now);
  if (!candidates.length) return;
  let fresh: Approval[];
  try {
    fresh = await boundedCardRequest(io.list());
    if (!Array.isArray(fresh)) return;
  } catch { return; } // Never infer a closed card from a failed list read.
  for (const [key, saved] of candidates) {
    if (!io.active()) return;
    if (useCardAnswers.getState().items[key] !== saved) continue;
    if (!canRecoverCardAnswer(saved, fresh.find((a) => a.id === saved.card.id)) || !beginCardSend(key)) continue;
    const submittedDraft = useCardDrafts.getState().items[key];
    try {
      const result = await boundedCardRequest(io.post(saved.card.id, saved.request));
      // Same request identity even if the previous POST committed without its response.
      if (useCardAnswers.getState().items[key]?.request.answer_id === saved.request.answer_id) clearCardAnswer(key);
      const draft = useCardDrafts.getState().items[key];
      if (saved.request.free_text && draft && draft === submittedDraft && draft.text === saved.request.answer) saveCardDraft(io.scope, draft.card, "");
      if (io.active()) io.accepted(result);
    } catch (error) { cardSendFailed(key, error, io.now?.() ?? Date.now()); }
    finally { endCardSend(key); }
  }
}
