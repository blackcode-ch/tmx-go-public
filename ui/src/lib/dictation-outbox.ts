import { create } from "zustand";
import { migrateDictationStores } from "./dictation-migration";
import { useStore } from "./store";
import { DictationQueue, MemoryDictations, deliverDictation, DictationSendTimeout, type DictationDelivery, type QueuedDictation } from "./dictation-queue";
import { DictationRecovery, type SavedDictationInfo } from "./dictation-recovery";

export const useDictationOutbox = create<{
  clips: QueuedDictation[];
  notices: Record<string, QueuedDictation>;
  saved: SavedDictationInfo[];
  error: string;
}>(() => ({ clips: [], notices: {}, saved: [], error: "" }));
const storage = new MemoryDictations();
const recovery = new DictationRecovery();

let deliver: DictationDelivery = () => false;
export function setDictationDelivery(fn: typeof deliver) { deliver = fn; }
export function dictationScope() { return useStore.getState().transport?.baseURL ?? ""; }
function recoveryAllowed() { return useStore.getState().status === "ready"; }

async function refreshRecovery() {
  const scope = dictationScope();
  if (!scope || !recoveryAllowed()) {
    useDictationOutbox.setState({ saved: [] });
    return;
  }
  const saved = (await recovery.list()).filter(clip => clip.scope === scope).sort((a, b) => a.created - b.created)
    .map(({ data: _audio, ...info }) => info);
  if (dictationScope() === scope && recoveryAllowed()) useDictationOutbox.setState({ saved });
}

async function saveRecovery(clip: QueuedDictation) {
  const { id, scope, target, data, language, created, capture } = clip;
  await recovery.put({ id, scope, target, data, language, created, capture, reason: "Transcription was interrupted. Play or retry this recording." });
}

async function request<T>(scope: string, method: string, path: string, body?: unknown): Promise<T> {
  const t = useStore.getState().transport;
  if (!t || t.baseURL !== scope) throw new Error("Not connected to this recording's computer yet.");
  if (useStore.getState().status === "locked") throw Object.assign(new Error("The phone is locked."), { status: 423 });
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    return await Promise.race([
      t.request<T>(method, path, body),
      new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error("Transcription timed out.")), 150_000); }),
    ]);
  } finally { clearTimeout(timer); }
}

const queue = new DictationQueue(storage, {
  transcribe: c => request(c.scope, "POST", `/api/transcribe?request_id=${encodeURIComponent(c.requestId ?? c.id)}`, {
    data: c.data, ...(c.language ? { language: c.language } : {}), ...(c.capture ? { capture: c.capture } : {}),
  }),
  transcript: (id, c) => request(c.scope, "GET", `/api/transcripts/${encodeURIComponent(id)}`),
}, clip => {
  useDictationOutbox.setState(s => ({ notices: { ...s.notices, [clip.target.id]: clip } }));
}, async clip => {
  if (!clip.recoverable) return;
  try {
    if (clip.blocked || clip.held) {
      const saved = await recovery.get(clip.id);
      if (saved?.scope === clip.scope) {
        const reason = (clip.error || "The transcript needs review.").replace(/\s*Recording discarded\.$/, "");
        clip.error = reason;
        await recovery.put({ ...saved, reason });
      }
    } else await recovery.remove(clip.id);
    await refreshRecovery();
  } catch {
    useDictationOutbox.setState({ error: "Could not update the saved dictation on this device." });
  }
});

export function clearDictationNotice(target: string) {
  useDictationOutbox.setState(s => {
    const notices = { ...s.notices }; delete notices[target]; return { notices };
  });
}

let migrationStarted = false;
export function initializeDictationStorage() {
  if (migrationStarted) return;
  migrationStarted = true;
  // Cleanup of legacy stores stays independent from the new recovery database.
  try {
    void migrateDictationStores({
      indexedDB: globalThis.indexedDB, localStorage: globalThis.localStorage,
      sessionStorage: globalThis.sessionStorage,
    }, () => {}).catch(() => console.warn("Legacy dictation cleanup failed; new dictation remains available."));
  } catch { console.warn("Legacy dictation cleanup unavailable; new dictation remains available."); }
}

export async function refreshDictationOutbox() {
  const scope = dictationScope();
  await initializeDictationStorage();
  const clips = (await storage.list()).filter(c => c.scope === scope).sort((a, b) => a.created - b.created);
  useDictationOutbox.setState({ clips: clips.map(clip => ({ ...clip, data: "" })) });
}

export async function flushDictationOutbox() {
  try { await initializeDictationStorage(); }
  catch { useDictationOutbox.setState({ error: "Could not clear old dictations. Please reload." }); return; }
  const scope = dictationScope();
  if (!scope) return;
  const run = async () => {
    await queue.flush(scope, (c, signal) => dictationScope() === c.scope && deliver(c, signal));
    await refreshDictationOutbox();
  };
  try { await run(); }
  catch { useDictationOutbox.setState({ error: "Dictation failed. Please try again." }); }
}

export async function keepDictation(clip: QueuedDictation, recover = false) {
  await initializeDictationStorage();
  clearDictationNotice(clip.target.id);
  if (recover && clip.scope && clip.scope === dictationScope()) {
    try {
      await saveRecovery(clip);
      clip.recoverable = true;
    } catch { useDictationOutbox.setState({ error: "This device could not save the dictation for recovery." }); }
  }
  try { await queue.enqueue(clip); }
  catch {
    useDictationOutbox.setState(s => ({ notices: { ...s.notices, [clip.target.id]: { ...clip, data: "", error: "Dictation failed. Please try again." } } }));
    return;
  }
  await refreshDictationOutbox();
  if (clip.recoverable) void refreshRecovery().catch(() => {});
  void flushDictationOutbox().catch(() => useDictationOutbox.setState({ error: "Could not clear old dictations. Please reload." }));
}

async function readSavedDictation(clip: SavedDictationInfo) {
  if (!recoveryAllowed() || dictationScope() !== clip.scope) throw new Error("Sign in to access this recording.");
  const saved = await recovery.get(clip.id);
  if (!saved || saved.scope !== dictationScope() || !recoveryAllowed()) throw new Error("This recording is unavailable.");
  return saved;
}

export async function savedDictationAudio(clip: SavedDictationInfo): Promise<string> {
  return "data:audio/wav;base64," + (await readSavedDictation(clip)).data;
}

const recoveryActions = new Set<string>();
export async function retrySavedDictation(clip: SavedDictationInfo) {
  if (recoveryActions.has(clip.id)) return;
  recoveryActions.add(clip.id);
  try {
    if ((await storage.list()).some(row => row.id === clip.id)) return;
    const saved = await readSavedDictation(clip);
    clearDictationNotice(clip.target.id);
    await queue.enqueue({ ...saved, requestId: crypto.randomUUID(), recoverable: true, review: true });
    await refreshDictationOutbox();
    void flushDictationOutbox();
  } finally { recoveryActions.delete(clip.id); }
}

export async function deleteSavedDictation(clip: SavedDictationInfo) {
  if (!recoveryAllowed() || dictationScope() !== clip.scope || recoveryActions.has(clip.id)) return;
  recoveryActions.add(clip.id);
  try {
    if ((await storage.list()).some(row => row.id === clip.id)) return;
    await recovery.remove(clip.id);
    const notice = useDictationOutbox.getState().notices[clip.target.id];
    if (notice?.id === clip.id) clearDictationNotice(clip.target.id);
    await refreshRecovery();
  } finally { recoveryActions.delete(clip.id); }
}

/** Explicit review sends these words once, without touching the composer draft. */
const accepting = new Set<string>();
export async function acceptDictation(clip: QueuedDictation) {
  if (accepting.has(clip.id) || useDictationOutbox.getState().notices[clip.target.id]?.id !== clip.id
    || dictationScope() !== clip.scope || !clip.held || !clip.text?.trim()) return;
  accepting.add(clip.id);
  try {
    if (await deliverDictation(clip, deliver) && useDictationOutbox.getState().notices[clip.target.id]?.id === clip.id) {
      clearDictationNotice(clip.target.id);
      if (clip.recoverable) {
        await recovery.remove(clip.id);
        await refreshRecovery();
      }
    }
  }
  catch (error) {
    if (error instanceof DictationSendTimeout && useDictationOutbox.getState().notices[clip.target.id]?.id === clip.id)
      useDictationOutbox.setState(s => ({ notices: { ...s.notices, [clip.target.id]: { ...clip, heldReason: "send_uncertain", error: error.message } } }));
    throw error;
  } finally { accepting.delete(clip.id); }
}

export function startDictationOutbox() {
  void flushDictationOutbox().catch(() => useDictationOutbox.setState({ error: "Could not clear old dictations. Please reload." }));
  const timer = setInterval(() => void flushDictationOutbox(), 5000);
  const online = () => { void flushDictationOutbox(); };
  window.addEventListener("online", online);
  const unsub = useStore.subscribe((s, prev) => {
    if (s.transport !== prev.transport) useDictationOutbox.setState({ notices: {} });
    if (s.transport !== prev.transport || s.status !== prev.status) void refreshRecovery().catch(() => {});
    if (s.transport !== prev.transport || (s.conn === "open" && prev.conn !== "open")) online();
  });
  void refreshRecovery().catch(() => {});
  return () => { clearInterval(timer); window.removeEventListener("online", online); unsub(); };
}
