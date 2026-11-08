import { useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { Section } from "@/components/page";
import { useStore } from "@/lib/store";
import { ownerAction, withConfirmation } from "@/lib/owner-gate";

type PushStatus = { public_key: string; enabled: boolean };

function applicationKey(value: string): Uint8Array<ArrayBuffer> {
  const decoded = atob(value.replace(/-/g, "+").replace(/_/g, "/"));
  return Uint8Array.from(decoded, c => c.charCodeAt(0));
}

export function PhoneNotifications() {
  const transport = useStore(s => s.transport);
  const [status, setStatus] = useState<PushStatus | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const supported = location.protocol === "https:" && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;

  useEffect(() => {
    if (!supported || !transport) return;
    let mounted = true;
    void (async () => {
      try {
        const next = await transport.request<PushStatus>("GET", "/api/push/key");
        const registration = await navigator.serviceWorker.getRegistration("/");
        const sub = await registration?.pushManager.getSubscription();
        if (mounted) {
          setStatus(next);
          setEnabled(next.enabled && !!sub && Notification.permission === "granted");
        }
      } catch { /* not a paired phone: enrollment stays unavailable */ }
    })();
    const renewal = (event: MessageEvent) => {
      if (event.data?.type === "push-renewal-needed") {
        setEnabled(false);
        setMessage("Enable notifications again to renew this phone's subscription.");
      }
    };
    navigator.serviceWorker.addEventListener("message", renewal);
    return () => { mounted = false; navigator.serviceWorker.removeEventListener("message", renewal); };
  }, [supported, transport]);

  async function enable() {
    if (!transport || !status) return;
    setBusy(true); setMessage("");
    try {
      // Request permission directly from the tap, before other asynchronous work.
      if (await Notification.requestPermission() !== "granted") {
        setMessage("Allow notifications in this phone's browser settings, then try again.");
        return;
      }
      await navigator.serviceWorker.register("/push-sw.js", { scope: "/" });
      const registration = await navigator.serviceWorker.ready;
      const key = applicationKey(status.public_key);
      let sub = await registration.pushManager.getSubscription();
      if (sub && (!sub.options.applicationServerKey || String(new Uint8Array(sub.options.applicationServerKey)) !== String(key))) {
        await sub.unsubscribe();
        sub = null;
      }
      sub ??= await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key });
      const subscription = sub.toJSON();
      await ownerAction(id => transport.request("POST", withConfirmation("/api/push/subscribe", id), { subscription }));
      setEnabled(true);
      setMessage("This phone will notify you when an approval needs you.");
    } catch {
      setMessage("Notifications could not be enabled. Check the connection and try again.");
    } finally { setBusy(false); }
  }

  async function disable() {
    if (!transport) return;
    setBusy(true); setMessage("");
    try {
      await transport.request("POST", "/api/push/unsubscribe", {});
      const registration = await navigator.serviceWorker.getRegistration("/");
      await (await registration?.pushManager.getSubscription())?.unsubscribe();
      setEnabled(false);
    } catch { setMessage("Notifications could not be disabled. Check the connection and try again."); }
    finally { setBusy(false); }
  }

  return <Section title="Phone notifications">
    <div className="space-y-3 rounded-xl border bg-card p-4">
      <p className="text-sm text-muted-foreground">Get a notification when an approval needs you. Open Tiamatrix to review it and approve with your owner PIN.</p>
      {supported && status
        ? <Button disabled={busy} variant={enabled ? "outline" : "default"} onClick={() => void (enabled ? disable() : enable())}>{enabled ? "Disable notifications" : "Enable notifications"}</Button>
        : <p className="text-sm">Open Tiamatrix on your paired phone to enable notifications. On iPhone, add it to your Home Screen first.</p>}
      {message && <p role="status" className="text-sm">{message}</p>}
    </div>
  </Section>;
}
