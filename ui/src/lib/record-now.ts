// ONE Record button (the owner, 2 Nov 17:22 / 3 Nov 00:31: "I gesticulate and highlight on screen"): one tap
// records what this device can give, with no choice to make first. A computer: its screen (the OS picker),
// with the camera drawn in the corner (rec-bubble.ts, Chromium), the microphone and the device's own sound.
// A phone (no screen capture): the camera and the microphone. The macOS app: its native capture. A part the
// device refuses (the camera, the microphone) is left out and the rest records; the recorder says which.
// The start dialog stays, as the options (a window only, a camera clip, the microphone only, a title).
import { toast } from "sonner";
import { canBubble } from "./rec-bubble";
import { canCaptureCamera, canCaptureMic, canCaptureScreen, deviceAudioLikely, startRecording, type StartOptions } from "./recorder";
import { startNativeRecording, useNative } from "./native-recorder";
import { useRecordings } from "./recordings";
import type { RecorderSettings } from "./recording-types";

export const DEFAULT_RECORDER_SETTINGS: RecorderSettings = {
  video_kbps: 1500,
  fps: 15,
  max_minutes: 0,
  segment_mb: 20,
  segment_minutes: 5,
};

/** What one tap records on this device (null: nothing can be recorded here). */
export function oneTapPlan(): Omit<StartOptions, "settings"> | null {
  if (canCaptureScreen()) {
    return {
      what: "screen",
      mic: canCaptureMic(),
      deviceAudio: deviceAudioLikely(),
      bubble: canCaptureCamera() && canBubble(),
    };
  }
  if (canCaptureCamera()) return { what: "camera", mic: true };
  if (canCaptureMic()) return { what: "audio", mic: true };
  return null;
}

/**
 * Starts recording at once. Call it straight from the tap: the browser shows its screen picker only for a
 * user gesture, so nothing is awaited before it.
 */
export function recordNow(): Promise<void> {
  if (useNative.getState().status?.supported) return startNativeRecording({ mic: true, system_audio: true });
  const plan = oneTapPlan();
  if (!plan) return Promise.reject(new Error("This window can't record: it has no screen capture, camera or microphone."));
  if (plan.what === "screen" && !plan.bubble && canCaptureCamera()) {
    toast("Recording without the camera", {
      description: "This browser can't draw the camera over the screen. Chrome or Edge can.",
      duration: 8000,
    });
  }
  return startRecording({
    ...plan,
    settings: useRecordings.getState().settings ?? DEFAULT_RECORDER_SETTINGS,
  });
}
