import { create } from "zustand";
import type { Approval, DecideBody } from "./types";

export interface PendingCardAnswer {
  scope: string; card: Approval; request: DecideBody;
  blocked?: boolean; retryAt?: number; retries?: number; error?: string;
}
const storageKey = "tmx.pending-card-answers.v1";
function load(): Record<string, PendingCardAnswer> {
  try {
    const parsed = JSON.parse(sessionStorage.getItem(storageKey) || "{}");
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const items: Record<string, PendingCardAnswer> = {};
    for (const [key, value] of Object.entries(parsed)) {
      const p = value as PendingCardAnswer | null;
      if (p && typeof p.scope === "string" && typeof p.card?.id === "string"
        && typeof p.request?.answer_id === "string" && typeof p.request?.version === "number") items[key] = p;
    }
    return items;
  } catch { return {}; }
}
export const useCardAnswers = create<{ items: Record<string, PendingCardAnswer> }>(() => ({ items: load() }));
export const cardAnswerKey = (scope: string, id: string) => JSON.stringify([scope, id]);
export function saveCardAnswer(key: string, pending: PendingCardAnswer) {
  const items = { ...useCardAnswers.getState().items, [key]: pending };
  sessionStorage.setItem(storageKey, JSON.stringify(items)); // persist before sending; fail visibly if unavailable
  useCardAnswers.setState({ items });
}
export function clearCardAnswer(key: string) {
  const items = { ...useCardAnswers.getState().items };
  delete items[key];
  sessionStorage.setItem(storageKey, JSON.stringify(items));
  useCardAnswers.setState({ items });
}

export const useCardSends = create<{ items: Record<string, boolean> }>(() => ({ items: {} }));
export function beginCardSend(key: string): boolean {
  if (useCardSends.getState().items[key]) return false;
  useCardSends.setState((s) => ({ items: { ...s.items, [key]: true } }));
  return true;
}
export function endCardSend(key: string) {
  const items = { ...useCardSends.getState().items };
  delete items[key];
  useCardSends.setState({ items });
}

export function cardSendFailed(key: string, error: unknown, now = Date.now()) {
  const pending = useCardAnswers.getState().items[key];
  if (!pending) return;
  const status = (error as { status?: number })?.status;
  const retries = (pending.retries || 0) + 1;
  saveCardAnswer(key, { ...pending, retries,
    blocked: status !== undefined && status >= 400 && status < 500 && status !== 408 && status !== 429,
    retryAt: now + Math.min(60_000, 5_000 * 2 ** Math.min(retries - 1, 4)),
    error: (error as Error)?.message || "Delivery is not confirmed. Your answer is saved.",
  });
}

export interface CardDraft { scope: string; card: Approval; text: string }
const draftStorageKey = "tmx.card-drafts.v1";
function loadDrafts(): Record<string, CardDraft> {
  try {
    const parsed = JSON.parse(sessionStorage.getItem(draftStorageKey) || "{}");
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    return Object.fromEntries(Object.entries(parsed).filter(([key, value]) => {
      const d = value as CardDraft;
      return d && typeof d.scope === "string" && typeof d.card?.id === "string"
        && typeof d.text === "string" && key === cardAnswerKey(d.scope, d.card.id);
    })) as Record<string, CardDraft>;
  } catch { return {}; }
}
export const useCardDrafts = create<{ items: Record<string, CardDraft> }>(() => ({ items: loadDrafts() }));
export function saveCardDraft(scope: string, card: Approval, text: string, reviewed = false) {
  const key = cardAnswerKey(scope, card.id);
  const items = { ...useCardDrafts.getState().items };
  // Keep the question/version the words were composed against until reviewed.
  if (text) items[key] = { scope, card: (!reviewed && items[key]?.card) || card, text };
  else delete items[key];
  // Keep words visible even when browser storage is unavailable; report the failure.
  useCardDrafts.setState({ items });
  sessionStorage.setItem(draftStorageKey, JSON.stringify(items));
}

/** A slow clipboard/POST must not erase dictation arriving while it was awaiting. */
export function clearUnchangedCardWords(key: string, pending: PendingCardAnswer | undefined, draft: CardDraft | undefined) {
  if (pending && useCardAnswers.getState().items[key] === pending) clearCardAnswer(key);
  if (draft && useCardDrafts.getState().items[key] === draft) saveCardDraft(draft.scope, draft.card, "");
}

/** The app dictation session writes here even after the original card unmounts. */
export function cardDictationDrafts(scope: string, card: Approval): Map<string, string> {
  return new class extends Map<string, string> {
    override get(key: string) { return useCardDrafts.getState().items[key]?.text; }
    override set(_key: string, text: string) { saveCardDraft(scope, card, text); return this; }
  }();
}

/** Current questions win; absent cards with words still have a visible recovery row. */
export function cardsWithSavedAnswers(approvals: Record<string, Approval>, scope: string,
  pending: Record<string, PendingCardAnswer>, drafts: Record<string, CardDraft>): Record<string, Approval> {
  const merged = { ...approvals };
  for (const saved of [...Object.values(drafts), ...Object.values(pending)]) {
    if (saved.scope !== scope) continue;
    const current = merged[saved.card.id];
    merged[saved.card.id] = { ...(current || saved.card), state: "pending", deferred: false };
  }
  return merged;
}

/** A hung transport must leave a retryable request, with its original identity. */
export async function boundedCardRequest<T>(request: Promise<T>, ms = 20_000): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([request, new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new Error("Sending timed out. Your answer is saved; retry when connected.")), ms);
    })]);
  } finally { clearTimeout(timer); }
}
