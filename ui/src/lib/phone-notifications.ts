/** The banner waits for device/server subscription reconciliation before prompting. */
export function phoneNotificationPrompt({ supported, ready, enabled, dismissed, ios }: {
  supported: boolean; ready: boolean; enabled: boolean; dismissed: boolean; ios: boolean;
}): "hidden" | "enable" | "home-screen" {
  if (enabled || dismissed) return "hidden";
  if (!supported) return ios ? "home-screen" : "hidden";
  return ready ? "enable" : "hidden";
}
