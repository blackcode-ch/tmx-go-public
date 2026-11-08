/* Notifications contain no private details or approval capability. */
self.addEventListener("push", (event) => {
  let payload = {};
  try { payload = event.data?.json() || {}; } catch { /* generic alert */ }
  const incident = typeof payload.incident === "string" && /^[a-f0-9]{64}$/.test(payload.incident) ? payload.incident : null;
  event.waitUntil(self.registration.showNotification("Approval needed", {
    body: "Open Tiamatrix to review",
    icon: "/apple-touch-icon.png",
    tag: incident || "tmx-" + crypto.randomUUID(),
    renotify: true,
    requireInteraction: true,
    silent: false,
  }));
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  event.waitUntil((async () => {
    const target = new URL("/#/inbox", self.location.origin).href;
    const windows = await self.clients.matchAll({ type: "window", includeUncontrolled: true });
    const existing = windows.find(client => new URL(client.url).origin === self.location.origin);
    if (existing) {
      await existing.navigate(target);
      return existing.focus();
    }
    return self.clients.openWindow(target);
  })());
});

// Renewal needs the paired phone's live owner session; never store credentials
// in this worker or silently enroll a new endpoint without that confirmation.
self.addEventListener("pushsubscriptionchange", (event) => {
  event.waitUntil((async () => {
    for (const client of await self.clients.matchAll({ type: "window", includeUncontrolled: true })) {
      client.postMessage({ type: "push-renewal-needed" });
    }
  })());
});

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", event => event.waitUntil(self.clients.claim()));
