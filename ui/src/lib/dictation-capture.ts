export interface DictationCapture {
  path: "browser" | "installed" | "desktop" | "unknown";
  processor: "worklet" | "script_processor";
  permission?: PermissionState;
  track_muted?: boolean;
  track_ended?: boolean;
  interruption?: "mute" | "ended";
  hidden_at_start: boolean;
  hidden_events: number;
  visible_events: number;
  context_state: "running" | "suspended" | "interrupted" | "closed" | "unknown";
  context_interruptions: number;
  samples: number;
  duration_ms: number;
}

export function dictationCapturePath(): DictationCapture["path"] {
  if (typeof window === "undefined") return "unknown";
  if (window.go?.desktop?.App && window.runtime) return "desktop";
  if (window.matchMedia?.("(display-mode: standalone)").matches ||
      (navigator as Navigator & { standalone?: boolean }).standalone === true) return "installed";
  return window.matchMedia?.("(display-mode: browser)").matches ? "browser" : "unknown";
}
