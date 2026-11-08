import { createContext, useContext, useEffect, useState, type ReactNode } from "react";
import { phoneNotificationPrompt } from "@/lib/phone-notifications";
import { Button } from "@/components/ui/button";
import { Section } from "@/components/page";
import { useStore } from "@/lib/store";
import { ownerAction, withConfirmation } from "@/lib/owner-gate";

type PushStatus = { public_key: string; enabled: boolean };

function applicationKey(value: string): Uint8Array<ArrayBuffer> {
  const decoded = atob(value.replace(/-/g, "+").replace(/_/g, "/"));
  return Uint8Array.from(decoded, c => c.charCodeAt(0));
}

function usePhoneNotifications() {
  const transport = useStore(s => s.transport);
  const appStatus = useStore(s => s.status);
  const [status, setStatus] = useState<PushStatus | null>(null);
  const [enabled, setEnabled] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const ios = /iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.platform === "MacIntel" && navigator.maxTouchPoints > 1);
  const standalone = window.matchMedia("(display-mode: standalone)").matches || (navigator as Navigator & { standalone?: boolean }).standalone === true;
  const supported = (!ios || standalone) && location.protocol === "https:" && "serviceWorker" in navigator && "PushManager" in window && "Notification" in window;

  useEffect(() => {
    if (!supported || !transport || appStatus !== "ready") return;
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
  }, [supported, transport, appStatus]);

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

  return { supported, ios, status, enabled, busy, message, enable, disable };
}

const PhoneContext = createContext<ReturnType<typeof usePhoneNotifications> | null>(null);

export function PhoneNotificationsProvider({ children }: { children: ReactNode }) {
  const value = usePhoneNotifications();
  return <PhoneContext.Provider value={value}>{children}</PhoneContext.Provider>;
}

function usePhone() {
  const value = useContext(PhoneContext);
  if (!value) throw new Error("PhoneNotificationsProvider is missing");
  return value;
}

export function PhoneNotificationBanner() {
  const { supported, ios, status, enabled, busy, message, enable } = usePhone();
  // Deliberately in memory: Later only lasts for this open, never persisted.
  const [dismissed, setDismissed] = useState(false);
  useEffect(() => {
    const reopen = () => { if (document.visibilityState === "visible") setDismissed(false); };
    document.addEventListener("visibilitychange", reopen);
    return () => document.removeEventListener("visibilitychange", reopen);
  }, []);
  const prompt = phoneNotificationPrompt({ supported, ready: !!status, enabled, dismissed, ios });
  if (prompt === "hidden") return null;
  if (prompt === "home-screen") return <p className="m-0 shrink-0 bg-[#151e19] px-4 py-3 text-[18px] text-white md:hidden">iPhone: add to Home Screen first</p>;
  return <section aria-label="Phone notifications" className="shrink-0 space-y-3 border-b border-white/30 bg-[#151e19] p-4 text-[18px] text-white md:hidden">
    <p className="font-semibold">Get approvals on this phone</p>
    <div className="flex gap-3">
      <button type="button" disabled={busy} onClick={() => void enable()} className="min-h-12 flex-1 rounded-lg bg-[#b7d3a3] px-5 py-2 text-[18px] font-bold text-[#151e19] disabled:opacity-60">{busy ? "Enabling..." : "Enable"}</button>
      <button type="button" disabled={busy} onClick={() => setDismissed(true)} className="min-h-12 rounded-lg border border-white px-5 py-2 text-[18px]">Later</button>
    </div>
    {message && <p role="status">{message}</p>}
  </section>;
}

export function PhoneNotifications() {
  const { supported, status, enabled, busy, message, enable, disable } = usePhone();
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
