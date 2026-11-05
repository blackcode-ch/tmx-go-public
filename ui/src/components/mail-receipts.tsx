import { useStore } from "@/lib/store";
import { absoluteTime } from "@/lib/format";

const labels: Record<string, string> = {
  queued: "Waiting for a safe input box",
  dispatching: "Waiting for submission confirmation",
  accepted: "Accepted by the agent process; pickup not confirmed",
  submitted: "Written to the terminal; pickup not confirmed",
  unconfirmed: "Submission uncertain; not automatically resent",
  read: "Read through the agent's inbox",
  owner: "Available in your mailbox",
};

/** Local delivery effects, updated by the same message events as the mailbox. */
export function MailReceipts({ agentId }: { agentId: string }) {
  const messages = useStore((s) => s.messages);
  const agents = useStore((s) => s.agents);
  const recent = messages.filter((m) => m.receipt && (m.to === agentId || m.from === agentId)).slice(-5).reverse();
  if (!recent.length) return null;
  return (
    <details className="shrink-0 border-b px-4 py-2 text-xs">
      <summary className="cursor-pointer text-muted-foreground">Mail delivery · {labels[recent[0].receipt!] ?? recent[0].state}</summary>
      <ul className="mt-2 space-y-2" aria-label="Recent mail delivery receipts">
        {recent.map((m) => (
          <li key={m.id}>
            <div>{m.from === "owner" ? "You" : agents[m.from]?.name ?? m.from} → {m.to === "owner" ? "you" : agents[m.to]?.name ?? m.to}: {labels[m.receipt!] ?? m.state}</div>
            <div className="truncate text-muted-foreground">{m.body.slice(0, 120)}</div>
            <div className="text-muted-foreground">{m.id} · {absoluteTime(m.receipt_at ?? m.created_at)}</div>
          </li>
        ))}
      </ul>
    </details>
  );
}
