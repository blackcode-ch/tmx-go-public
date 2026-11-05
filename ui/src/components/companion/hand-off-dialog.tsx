// "Hand to Companion": the owner writes a task (goal, context, steps, done-when, don't-touch),
// sees exactly the text tmx will send, and sends it. The create carries the preview's hash, so
// what is sent is what was shown.
import { useEffect, useState } from "react";
import { ArrowLeft, Fingerprint, Loader2, Send } from "lucide-react";
import { create } from "zustand";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { companionApi, lines, type CompanionPreview, type CompanionSpec } from "@/lib/companion";
import { useGateAsks } from "@/lib/owner-gate";
import { useStore } from "@/lib/store";
import { EffectsBadge } from "./bits";

interface Open {
  agentId?: string;
  sourceApprovalId?: string;
  goal?: string;
  context?: string;
}

const useHandOff = create<{ open: Open | null }>(() => ({ open: null }));

/** Opens the dialog; agentId gets Companion's report (default the head). */
export function openHandOff(o: Open = {}) {
  useHandOff.setState({ open: o });
}

export function HandOffDialog() {
  const open = useHandOff((s) => s.open);
  const agents = useStore((s) => s.agents);
  const headId = useStore((s) => s.headId);
  const gateOn = useGateAsks(); // the gate is on and no grace window is open
  const [goal, setGoal] = useState("");
  const [context, setContext] = useState("");
  const [steps, setSteps] = useState("");
  const [success, setSuccess] = useState("");
  const [dont, setDont] = useState("");
  const [where, setWhere] = useState("");
  const [preview, setPreview] = useState<CompanionPreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return;
    setGoal(open.goal ?? "");
    setContext(open.context ?? "");
    setSteps("");
    setSuccess("");
    setDont("");
    setWhere("");
    setPreview(null);
    setBusy(false);
    setError("");
  }, [open]);

  const close = () => !busy && useHandOff.setState({ open: null });
  const target = agents[open?.agentId ?? headId ?? ""];
  const spec = (): CompanionSpec => ({
    goal: goal.trim(),
    context: context.trim() || undefined,
    steps: lines(steps),
    success: success.trim() || undefined,
    do_not_touch: lines(dont),
    where: where.trim() || undefined,
  });
  const opts = { agent_id: open?.agentId, source_approval_id: open?.sourceApprovalId };

  async function showPreview(e: React.FormEvent) {
    e.preventDefault();
    if (!goal.trim() || busy) return;
    setBusy(true);
    setError("");
    try {
      setPreview(await companionApi.preview(spec(), opts));
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  }

  async function send() {
    if (!preview || busy) return;
    setBusy(true);
    setError("");
    try {
      await companionApi.create(spec(), { ...opts, text_hash: preview.text_hash });
      toast.success("Handed to Companion", { description: `Companion's report goes to ${preview.agent} and shows in Settings → Companion.` });
      useHandOff.setState({ open: null });
    } catch (err) {
      setError((err as Error).message || "The task was not sent.");
      setBusy(false);
    }
  }

  const gated = gateOn && preview && preview.effects !== "none";
  return (
    <Dialog open={!!open} onOpenChange={(v) => !v && close()}>
      <DialogContent className="sm:max-w-[37.5rem]">
        {!preview ? (
          <form onSubmit={showPreview} className="grid gap-4">
            <DialogHeader>
              <DialogTitle>Hand to Companion</DialogTitle>
              <DialogDescription>
                Companion, your desktop assistant, does it in your apps, browser and accounts. Its report goes to {target?.name ?? "the head"}
                {open?.sourceApprovalId ? " and shows on the Needs-you item" : ""}.
              </DialogDescription>
            </DialogHeader>
            <div className="grid gap-1.5">
              <Label htmlFor="ho-goal">Goal</Label>
              <Input id="ho-goal" value={goal} onChange={(e) => setGoal(e.target.value)} placeholder="Download the March invoice from the Swisscom portal" autoFocus autoComplete="off" />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="ho-context">Context</Label>
              <Textarea id="ho-context" value={context} onChange={(e) => setContext(e.target.value)} placeholder="What Companion needs to know. No passwords: tmx removes anything that looks like a key." className="min-h-16 resize-none" />
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <Label htmlFor="ho-steps">Steps</Label>
                <Textarea id="ho-steps" value={steps} onChange={(e) => setSteps(e.target.value)} placeholder={"One per line\nOpen the portal\nFind the March invoice"} className="min-h-20 resize-none" />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="ho-dont">Do not touch</Label>
                <Textarea id="ho-dont" value={dont} onChange={(e) => setDont(e.target.value)} placeholder={"One per line\nPayment settings"} className="min-h-20 resize-none" />
              </div>
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="grid gap-1.5">
                <Label htmlFor="ho-success">Done when</Label>
                <Input id="ho-success" value={success} onChange={(e) => setSuccess(e.target.value)} placeholder="The PDF is in Downloads" autoComplete="off" />
              </div>
              <div className="grid gap-1.5">
                <Label htmlFor="ho-where">Where</Label>
                <Input id="ho-where" value={where} onChange={(e) => setWhere(e.target.value)} placeholder="Site, app or account" autoComplete="off" />
              </div>
            </div>
            {error && <p className="rounded-md border border-state-error/30 bg-diff-del-bg px-3 py-2 text-xs text-foreground">{error}</p>}
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={close} disabled={busy}>
                Cancel
              </Button>
              <Button type="submit" disabled={!goal.trim() || busy}>
                {busy && <Loader2 className="animate-spin" />}
                Preview
              </Button>
            </DialogFooter>
          </form>
        ) : (
          <div className="grid gap-4">
            <DialogHeader>
              <DialogTitle>Send this to Companion?</DialogTitle>
              <DialogDescription>This is exactly what Companion receives. Its report goes to {preview.agent}.</DialogDescription>
            </DialogHeader>
            <div className="flex flex-wrap items-center gap-2">
              <EffectsBadge effects={preview.effects} />
              <span className="text-xs text-muted-foreground">{preview.effects_label}</span>
            </div>
            <pre
              data-companion-preview
              className="scroll-thin max-h-72 overflow-auto rounded-lg border bg-muted/40 px-3 py-2.5 font-mono text-[0.75rem] leading-relaxed whitespace-pre-wrap text-foreground/90"
            >
              {preview.text}
            </pre>
            <p className="text-2xs text-subtle-foreground">
              Companion's own rules still apply: it asks you before anything it gates (money, accounts, sending) and may refuse.
            </p>
            {error && <p className="rounded-md border border-state-error/30 bg-diff-del-bg px-3 py-2 text-xs text-foreground">{error}</p>}
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={() => setPreview(null)} disabled={busy}>
                <ArrowLeft />
                Edit
              </Button>
              <Button onClick={() => void send()} disabled={busy} title={gated ? "Needs your confirmation on this machine" : undefined}>
                {busy ? <Loader2 className="animate-spin" /> : gated ? <Fingerprint /> : <Send />}
                Send to Companion
              </Button>
            </DialogFooter>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
