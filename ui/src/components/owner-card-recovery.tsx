import { useEffect } from "react";
import { toast } from "sonner";
import { recoverCardAnswers } from "@/lib/card-answer-recovery";
import { useStore } from "@/lib/store";
import type { Approval } from "@/lib/types";

/** Recovery only while Your turn is visible and connected to the original daemon. */
export function OwnerCardRecovery() {
  const transport = useStore((s) => s.transport);
  const status = useStore((s) => s.status);
  const connection = useStore((s) => s.conn);
  useEffect(() => {
    if (!transport || status !== "ready" || connection !== "open") return;
    let alive = true, recovering = false;
    const active = () => alive && !document.hidden && navigator.onLine
      && useStore.getState().transport === transport && useStore.getState().status === "ready"
      && useStore.getState().conn === "open";
    const recover = async () => {
      if (recovering || !active()) return;
      recovering = true;
      try {
        await recoverCardAnswers({ scope: transport.baseURL, active,
          list: () => transport.request<Approval[]>("GET", "/api/approvals"),
          post: (id, request) => transport.request<Approval>("POST", `/api/approvals/${encodeURIComponent(id)}`, request),
          accepted: (card) => {
            useStore.setState((s) => {
              const approvals = { ...s.approvals };
              if (card.state === "pending") approvals[card.id] = card;
              else delete approvals[card.id];
              return { approvals };
            });
            toast("Saved answer delivered to the agent's mailbox");
          },
        });
      } catch { /* Browser storage unavailable: keep the visible in-memory words. */ }
      finally { recovering = false; }
    };
    void recover();
    const timer = setInterval(() => void recover(), 15_000);
    window.addEventListener("online", recover);
    document.addEventListener("visibilitychange", recover);
    return () => {
      alive = false;
      clearInterval(timer);
      window.removeEventListener("online", recover);
      document.removeEventListener("visibilitychange", recover);
    };
  }, [transport, status, connection]);
  return null;
}
