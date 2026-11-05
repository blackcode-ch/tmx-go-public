// A stand-in for the macOS app's native capture under ?mock=…&native=1, so the start dialog's
// "Screen with sound" and the recording bar can be seen without the app. It "picks" a display
// at once and drives the mock recorder API; there is no media.
import { recApi, upsertRecording } from "../recordings";
import type { NativeBridge, NativeStatus } from "../native-recorder";

export function mockNativeBridge(notify: (st: NativeStatus) => void): NativeBridge {
  let st: NativeStatus = { supported: true, phase: "idle" };
  const set = (p: Partial<NativeStatus>) => {
    st = { ...st, ...p };
    notify(st);
  };
  return {
    NativeRecordStatus: async () => st,
    NativeRecordStart: async (req) => {
      set({ phase: "picking" });
      await new Promise((r) => setTimeout(r, 400)); // the picker
      const rec = await recApi.begin({ kind: "screen", mic: req.mic, mime: "video/mp4;codecs=avc1,mp4a.40.2", title: req.title });
      const shown = { ...rec, source: "native" as const, system_audio: req.system_audio };
      upsertRecording(shown);
      set({ phase: "recording", id: rec.id });
      return { recording: shown };
    },
    NativeRecordPause: async () => {
      if (st.id) upsertRecording(await recApi.setState(st.id, "paused"));
      set({ phase: "paused" });
    },
    NativeRecordResume: async () => {
      if (st.id) upsertRecording(await recApi.setState(st.id, "recording"));
      set({ phase: "recording" });
    },
    NativeRecordStop: async () => {
      if (!st.id) return null;
      set({ phase: "stopping" });
      const r = await recApi.setState(st.id, "stopped");
      st = { supported: true, phase: "idle" };
      notify({ ...st, ended: { recording: r } });
      return r;
    },
    NativeRecordSync: async () => {},
  };
}
