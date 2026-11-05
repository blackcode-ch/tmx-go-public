// Native screen recording in the macOS app (DESIGN.md "Recorder", native capture). The WebView
// cannot record what the Mac plays. ScreenCaptureKit, in the app's window process, records a
// display, a window or an app with device audio and the microphone (macOS 15 or later). This
// page only asks for it and shows it:
//   - the Go side (internal/desktop/nativerec) shows the system picker;
//   - it writes the files and tells the daemon, which owns the recording;
//   - it follows a pause or stop made in another window.
// Its state arrives as the Wails event "tmx:native-rec". In a browser, on Windows or on an
// older macOS there is no bridge (or it says unsupported), and the WebView recorder is used.
import { create } from "zustand";
import { toast } from "sonner";
import { mockRequested } from "./transport";
import { loadActive, onRecordingUpdated, upsertRecording } from "./recordings";
import type { Recording } from "./recording-types";
import { isLive } from "./recording-types";

export type NativePhase = "idle" | "picking" | "starting" | "recording" | "paused" | "stopping";

/** internal/desktop/nativerec.Status */
export interface NativeStatus {
  supported: boolean;
  /** why not, when unsupported */
  reason?: string;
  phase: NativePhase;
  /** the recording this window captures */
  id?: string;
  warning?: string;
  /** set once, when a capture ended */
  ended?: { recording?: Recording | null; error?: string };
}

export interface NativeStartRequest {
  mic: boolean;
  system_audio: boolean;
  title?: string;
}

export interface NativeStartResult {
  recording?: Recording;
  cancelled?: boolean;
  warning?: string;
}

/** The App methods of internal/desktop/app_native.go (window.go.desktop.App). */
export interface NativeBridge {
  NativeRecordStatus(): Promise<NativeStatus>;
  NativeRecordStart(req: NativeStartRequest): Promise<NativeStartResult>;
  NativeRecordPause(): Promise<void>;
  NativeRecordResume(): Promise<void>;
  NativeRecordStop(): Promise<Recording | null>;
  NativeRecordSync(): Promise<void>;
}

interface NativeState {
  /** null until asked (and in a browser, where there is no bridge) */
  status: NativeStatus | null;
}

export const useNative = create<NativeState>(() => ({ status: null }));

let mock: NativeBridge | null = null;

function bridge(): NativeBridge | null {
  if (mock) return mock;
  const app = window.go?.desktop?.App as unknown as Partial<NativeBridge> | undefined;
  return app?.NativeRecordStatus ? (app as NativeBridge) : null;
}

/** Whether this window records natively (the macOS app on macOS 15+). */
export function useNativeAvailable(): boolean {
  return useNative((s) => !!s.status?.supported);
}

/** The recording this window captures natively, if any. */
export function useNativeId(): string | undefined {
  return useNative((s) => (s.status && s.status.phase !== "idle" ? s.status.id : undefined));
}

function onStatus(st: NativeStatus) {
  useNative.setState({ status: { ...st, ended: undefined } });
  if (!st.ended) return;
  void loadActive();
  const r = st.ended.recording;
  if (r) {
    upsertRecording(r);
    const failed = r.state === "failed";
    const note = [r.title, failed ? "" : r.error, st.ended.error].filter(Boolean).join(" · "); // a failed one says it in the title
    (failed ? toast.error : toast.success)(failed ? "Nothing was recorded" : "Recording saved", {
      description: note,
      action: failed ? undefined : { label: "Open", onClick: () => (window.location.hash = `/recordings/${encodeURIComponent(r.id)}`) },
      duration: 10000,
    });
  } else {
    toast.error("Recording ended", { description: st.ended.error || "The capture stopped.", duration: 15000 });
  }
}

/** Asks the window for its native capture's state (once at load; again after a reconnect). */
export async function loadNativeStatus(): Promise<void> {
  const b = bridge();
  if (!b) return;
  try {
    onStatus(await b.NativeRecordStatus());
  } catch {
    /* an older app without native capture */
  }
}

/**
 * Starts a native recording: the macOS picker shows next (the Go side waits for the owner's
 * choice), then the capture begins. Resolves when it runs, or when the picker was closed.
 */
export async function startNativeRecording(req: NativeStartRequest): Promise<void> {
  const b = bridge();
  if (!b) throw new Error("This window can't record natively.");
  const res = await b.NativeRecordStart(req);
  if (res.cancelled || !res.recording) return;
  upsertRecording(res.recording);
  if (res.warning) toast.warning("Recording without the microphone", { description: res.warning, duration: 12000 });
}

export const nativeCapture = {
  pause: async () => {
    await bridge()?.NativeRecordPause();
  },
  resume: async () => {
    await bridge()?.NativeRecordResume();
  },
  stop: async () => {
    await bridge()?.NativeRecordStop();
  },
};

// A pause, resume or stop made in another window reaches this one as recording.updated: the
// Go side follows it at once (otherwise at its next heartbeat, within 5 s).
onRecordingUpdated((r) => {
  const st = useNative.getState().status;
  if (!st || st.phase === "idle" || st.id !== r.id) return;
  const differs = !isLive(r) || !!r.stopping || (r.state === "paused") !== (st.phase === "paused");
  if (differs) void bridge()?.NativeRecordSync().catch(() => {});
});

function init() {
  if (mockRequested() && new URLSearchParams(window.location.search).get("native") === "1") {
    void import("./mock/native").then((m) => {
      mock = m.mockNativeBridge(onStatus);
      void loadNativeStatus();
    });
    return;
  }
  if (!bridge()) {
    if (!/^wails:|wails\.localhost/.test(window.location.href)) return; // a browser: no app
    // The Wails bindings appear a moment after the page; ask once they are there.
    let tries = 0;
    const id = setInterval(() => {
      if (bridge() || ++tries > 40) {
        clearInterval(id);
        if (bridge()) start();
      }
    }, 50);
    return;
  }
  start();
}

function start() {
  window.runtime?.EventsOn("tmx:native-rec", (d) => onStatus(d as NativeStatus));
  void loadNativeStatus();
}

init();
