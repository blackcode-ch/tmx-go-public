// The camera in the recorder (DESIGN.md "Recorder"): the start dialog's live preview with the
// camera and microphone pickers (shown when there is more than one), and the small mirrored
// tile that shows the camera while a camera clip records in this window. The preview's stream
// is handed to the recorder at Start, so the camera opens once.
import { useCallback, useEffect, useRef, useState } from "react";
import { Video, X } from "lucide-react";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { cameraError, openCamera, useCapture } from "@/lib/recorder";
import { cn } from "@/lib/utils";

/** Radix's Select has no empty value: this one means the system's default device. */
export const DEFAULT_DEVICE = "default";

export interface Devices {
  cameras: MediaDeviceInfo[];
  mics: MediaDeviceInfo[];
}

/** The cameras and microphones (their names once the camera may be used); again on every change. */
export function useMediaDevices(on: boolean, refresh: unknown): Devices {
  const [d, setD] = useState<Devices>({ cameras: [], mics: [] });
  useEffect(() => {
    const md = navigator.mediaDevices;
    if (!on || !md?.enumerateDevices) return;
    let gone = false;
    const load = () =>
      md
        .enumerateDevices()
        .then((list) => {
          if (gone) return;
          // without the pseudo-devices "default" and "communications", and nameless entries before permission
          const real = (k: MediaDeviceKind) => list.filter((x) => x.kind === k && x.deviceId && x.deviceId !== "default" && x.deviceId !== "communications");
          setD({ cameras: real("videoinput"), mics: real("audioinput") });
        })
        .catch(() => {});
    void load();
    md.addEventListener?.("devicechange", load);
    return () => {
      gone = true;
      md.removeEventListener?.("devicechange", load);
    };
  }, [on, refresh]);
  return d;
}

/**
 * The camera (and the microphone when mic is set) while the dialog shows the camera. Changing a
 * device opens it again. handOver() gives the stream to the recorder: it is then not stopped
 * when the dialog closes.
 */
export function useCameraPreview(on: boolean, o: { cameraId?: string; micId?: string; mic: boolean }) {
  const [stream, setStream] = useState<MediaStream | null>(null);
  const [error, setError] = useState("");
  const kept = useRef<MediaStream | null>(null);
  const { cameraId, micId, mic } = o;
  useEffect(() => {
    if (!on) return;
    let gone = false;
    let opened: MediaStream | null = null;
    setError("");
    openCamera({ cameraId, micId, mic })
      .then((s) => {
        if (gone) return s.getTracks().forEach((t) => t.stop());
        opened = s;
        setStream(s);
      })
      .catch((e) => !gone && (setError(cameraError(e)), setStream(null)));
    return () => {
      gone = true;
      if (opened && opened !== kept.current) opened.getTracks().forEach((t) => t.stop());
      setStream(null);
    };
  }, [on, cameraId, micId, mic]);
  const handOver = useCallback(() => {
    kept.current = stream;
    return stream;
  }, [stream]);
  return { stream, error, handOver };
}

/** A mirrored live picture of a camera stream (what a mirror shows; the recording is not mirrored). */
export function CameraPreview({ stream, className }: { stream: MediaStream | null; className?: string }) {
  const ref = useRef<HTMLVideoElement>(null);
  useEffect(() => {
    const v = ref.current;
    if (!v) return;
    v.srcObject = stream;
    if (stream) void v.play().catch(() => {});
  }, [stream]);
  return <video ref={ref} autoPlay muted playsInline className={cn("-scale-x-100 bg-black object-cover", className)} data-camera-preview />;
}

/** A device picker, shown only when there is a choice. */
export function DevicePicker({
  label,
  devices,
  value,
  onChange,
  disabled,
}: {
  label: string;
  devices: MediaDeviceInfo[];
  value: string;
  onChange: (id: string) => void;
  disabled?: boolean;
}) {
  if (devices.length < 2) return null;
  return (
    <label className="grid gap-1 text-xs text-muted-foreground">
      {label}
      <Select value={value} onValueChange={onChange} disabled={disabled}>
        <SelectTrigger size="sm" className="w-full" aria-label={label}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={DEFAULT_DEVICE}>The system's default</SelectItem>
          {devices.map((d, i) => (
            <SelectItem key={d.deviceId} value={d.deviceId}>
              {d.label || `${label} ${i + 1}`}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </label>
  );
}

/** The camera while a camera clip records in this window: small, mirrored, in a corner; it can be hidden. */
export function CameraTile() {
  const preview = useCapture((s) => s.preview);
  const [hiddenFor, setHiddenFor] = useState("");
  if (!preview) return null;
  if (hiddenFor === preview.id)
    return (
      <button
        type="button"
        onClick={() => setHiddenFor("")}
        className="fixed top-24 right-4 z-40 flex items-center gap-1.5 rounded-full border bg-card px-2.5 py-1 text-xs shadow-md outline-none hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Video className="size-3.5" />
        Show camera
      </button>
    );
  return (
    <div className="fixed top-24 right-4 z-40 w-44 overflow-hidden rounded-xl border bg-black shadow-lg sm:w-56" data-camera-tile>
      <CameraPreview stream={preview} className="aspect-video w-full" />
      <button
        type="button"
        aria-label="Hide the camera preview"
        onClick={() => setHiddenFor(preview.id)}
        className="absolute top-1.5 right-1.5 rounded-md bg-black/55 p-1 text-white outline-none hover:bg-black/75 focus-visible:ring-2 focus-visible:ring-ring"
      >
        <X className="size-3.5" />
      </button>
      <span className="absolute bottom-1.5 left-1.5 flex items-center gap-1 rounded bg-black/55 px-1.5 py-0.5 text-2xs text-white">
        <span className="size-1.5 animate-pulse rounded-full bg-state-error" aria-hidden />
        Recording
      </span>
    </div>
  );
}
