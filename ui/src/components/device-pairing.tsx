import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Section } from "@/components/page";
import { useStore } from "@/lib/store";
import { ownerAction, withConfirmation } from "@/lib/owner-gate";
import { PinInput, validPin } from "@/components/pin-input";

async function pairRequest<T>(path: string, body: unknown = {}): Promise<T> {
  const r = await fetch(path, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  const data = await r.json();
  if (!r.ok) throw new Error(data.error || "Pairing failed.");
  return data as T;
}

export function PairDevice() {
  const [code, setCode] = useState("");
  const [deadline, setDeadline] = useState(0);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [paired, setPaired] = useState(false);
  const [recovery, setRecovery] = useState<{ id: string; runbook: string; expires: string } | null>(null);
  const [name, setName] = useState("My phone");
  const [pin, setPin] = useState("");
  const recoveryID = recovery?.id;
  useEffect(() => {
    let live = true;
    async function check() {
      try {
        const response = await fetch("/api/devices/recovery", { credentials: "same-origin" });
        if (!response.ok) return;
        const data = await response.json();
        if (!live) return;
        if (data.recovery) setRecovery(data);
        else if (recoveryID) { setRecovery(null); setPaired(true); }
      } catch { /* Pairing and expiry show their own errors. */ }
    }
    void check();
    const timer = setInterval(() => void check(), 2000);
    return () => { live = false; clearInterval(timer); };
  }, [recoveryID]);
  async function start() {
    setBusy(true); setError("");
    try {
      const p = await pairRequest<{ code: string; expires_in: number }>("/api/pair/start", { name });
      setCode(p.code); setDeadline(Date.now() + p.expires_in * 1000);
    } catch (e) { setError(String(e)); } finally { setBusy(false); }
  }
  useEffect(() => {
    if (!code) return;
    let cancelled = false;
    const timer = setInterval(() => {
      if (Date.now() >= deadline) { setCode(""); setError("Pairing expired. Start again."); return; }
      void pairRequest<{ pending?: boolean; recovery?: boolean }>("/api/pair/claim").then(p => {
        if (!cancelled && !p.pending) { setPaired(!p.recovery); setCode(""); }
      }).catch(e => { if (!cancelled) { setCode(""); setError(String(e)); } });
    }, 2000);
    return () => { cancelled = true; clearInterval(timer); };
  }, [code, deadline]);
  // QR -> PIN -> done: the owner PIN approves this device's own pairing; the claim loop above
  // then picks up the device session.
  async function pairWithPin() {
    setBusy(true); setError("");
    try {
      await pairRequest("/api/pair/pin", { pin });
      setPin("");
    } catch (e) { setPin(""); setError(String(e)); } finally { setBusy(false); }
  }
  if (recovery) return <div className="w-full max-w-md space-y-4 rounded-xl border p-6">
    <h1 className="text-xl font-semibold">Restricted recovery</h1>
    <p>{recovery.runbook}</p><p>Expires: {new Date(recovery.expires).toLocaleString()}</p>
    <Button onClick={() => void fetch(`/api/devices/${recovery.id}`, { method: "DELETE", credentials: "same-origin" }).then(() => window.location.reload())}>Revoke this recovery session</Button>
  </div>;
  if (paired) return <div className="w-full max-w-md space-y-4 rounded-xl border p-6">
    <h1 className="text-xl font-semibold">Device paired</h1>
    <p className="text-sm">Owner actions on this device ask for your owner PIN.</p>
    <Button onClick={() => window.location.reload()}>Continue</Button>
  </div>;
  return <div className="w-full max-w-md space-y-4 rounded-xl border p-6">
    <h1 className="text-xl font-semibold">Pair this device</h1>
    {!code && <Input aria-label="Device name" value={name} onChange={e => setName(e.target.value)} maxLength={80} />}
    <p className="text-sm text-muted-foreground">Keep this page open. Type your owner PIN, or approve the code in Settings → Owner devices on the host computer.</p>
    {code ? <form className="space-y-3" onSubmit={e => { e.preventDefault(); if (validPin(pin)) void pairWithPin(); }}>
      <PinInput label="Owner PIN" value={pin} onChange={setPin} autoFocus disabled={busy} />
      <Button type="submit" className="w-full" disabled={busy || !validPin(pin)}>Pair with my PIN</Button>
      <div className="text-center font-mono text-xl tracking-widest text-muted-foreground" aria-label="Pairing code">{code}</div>
      <p className="text-xs text-muted-foreground">Expires in two minutes. At the host, only approve a code shown on your own device.</p>
    </form> : <Button disabled={busy} onClick={() => void start()}>Pair this device</Button>}
    {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
  </div>;
}

type Device = { id: string; name: string; host: string; recovery?: boolean };
export function OwnerDevices() {
  const tr = useStore(s => s.transport);
  const remote = !tr?.desktop && window.location.protocol === "https:";
  const [devices, setDevices] = useState<Device[]>([]);
  const [code, setCode] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const [bootstrapHost, setBootstrapHost] = useState("");
  useEffect(() => { let live = true; void tr?.request<Device[]>("GET", "/api/devices").then(v => { if (live) setDevices(v); }).catch(() => {}); return () => { live = false; }; }, [tr]);
  async function act(run: () => Promise<unknown>) {
    setBusy(true); setMessage("");
    try { await run(); setMessage("Done."); if (tr) setDevices(await tr.request("GET", "/api/devices")); }
    catch (e) { setMessage(String(e)); } finally { setBusy(false); }
  }
  return <Section title="Owner devices">
    <div className="space-y-3">
      {!remote && <div className="space-y-2 rounded border p-3">
        <p>Create a private one-pair link for another computer. Normal issuance requires fresh host confirmation. Restricted recovery requires unavailable host authentication and this UI's surviving proof session.</p>
        <Input aria-label="Trusted HTTPS authority" placeholder="Trusted HTTPS hostname and port" value={bootstrapHost} onChange={e => setBootstrapHost(e.target.value)} />
        <Button disabled={busy || !tr || !bootstrapHost} onClick={() => void act(() => ownerAction(id => tr!.request("POST", withConfirmation("/api/devices/bootstrap", id), { host: bootstrapHost, recovery: false })))}>Create one-pair link</Button>
        <Button variant="outline" disabled={busy || !tr || !bootstrapHost} onClick={() => void act(() => tr!.request("POST", "/api/devices/bootstrap", { host: bootstrapHost, recovery: true }))}>Create restricted recovery link</Button>
        <p>The private link is written to the host's owner-bootstrap-link file. Deliver it over a channel you control, never chat. It expires in ten minutes.</p>
        {devices.filter(d => d.recovery).map(d => <Button key={d.id} disabled={busy || !tr} onClick={() => void act(() => ownerAction(id => tr!.request("POST", withConfirmation(`/api/devices/${d.id}/upgrade`, id), {})))}>Upgrade recovery device: {d.name}</Button>)}
      </div>}
      <p className="text-sm text-muted-foreground">A new device pairs itself with your owner PIN. Or approve here only the code visible on your own device.</p>
      {remote ? <p className="text-sm">Approve new devices on the host computer.</p> : <div className="flex gap-2"><Input aria-label="Pairing code to approve" value={code} onChange={e => setCode(e.target.value)} maxLength={8} placeholder="Pairing code" /><Button disabled={busy || !code || !tr} onClick={() => void act(async () => { await ownerAction(id => tr!.request("POST", withConfirmation("/api/devices/approve", id), { code })); setCode(""); })}>Approve</Button></div>}
      {devices.map(d => <div key={d.id} className="flex items-center justify-between gap-2 rounded border p-3"><div className="min-w-0"><div>{d.name || "Browser device"}</div><div className="break-all text-xs text-muted-foreground">{d.host}{d.recovery ? " · restricted recovery" : ""}</div></div><Button variant="outline" disabled={busy} onClick={() => void act(() => ownerAction(id => tr!.request("DELETE", withConfirmation(`/api/devices/${d.id}`, id))))}>Revoke</Button></div>)}
      {message && <p role="status" className="text-sm">{message}</p>}
    </div>
  </Section>;
}
