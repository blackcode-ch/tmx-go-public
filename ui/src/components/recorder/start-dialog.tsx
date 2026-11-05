// Starting a recording: the owner's own screen (whole, or one window: the OS picker decides)
// with or without their microphone and the device's own sound, a clip from the camera (with a
// live preview and the device pickers), or the microphone only. The click on Start is what
// lets the browser show its picker.
import { useEffect, useState } from "react";
import { Loader2, Mic, Monitor, Video } from "lucide-react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { CameraPreview, DEFAULT_DEVICE, DevicePicker, useCameraPreview, useMediaDevices } from "@/components/recorder/camera-preview";
import { canCaptureCamera, canCaptureMic, canCaptureScreen, deviceAudioLikely, openRecordDialog, startRecording, useCapture } from "@/lib/recorder";
import { canBubble } from "@/lib/rec-bubble";
import { NativeSwitches, nativeChoice } from "@/components/recorder/native-choice";
import { startNativeRecording, useNativeAvailable } from "@/lib/native-recorder";
import { recApi, useRecordings } from "@/lib/recordings";
import type { RecorderSettings } from "@/lib/recording-types";
import { DEFAULT_RECORDER_SETTINGS } from "@/lib/record-now";
import { isLive } from "@/lib/recording-types";
import { cn } from "@/lib/utils";
import { clinicDefault, setClinicDefault } from "@/lib/clinic";
import { ClinicCheckbox } from "@/components/recorder/clinic-checkbox";

export function StartRecordingDialog() {
  const open = useCapture((s) => s.dialog);
  const phase = useCapture((s) => s.phase);
  const active = useRecordings((s) => s.active);
  const screenOK = canCaptureScreen();
  const micOK = canCaptureMic();
  const cameraOK = canCaptureCamera();
  const deviceLikely = deviceAudioLikely();
  const [what, setWhat] = useState<"screen" | "camera" | "audio" | "native">("screen");
  const [mic, setMic] = useState(true);
  const [deviceAudio, setDeviceAudio] = useState(deviceLikely);
  const bubbleOK = canBubble() && cameraOK;
  const [bubble, setBubble] = useState(bubbleOK); // on, as with the one-tap Record
  const [cameraId, setCameraId] = useState(DEFAULT_DEVICE);
  const [micId, setMicId] = useState(DEFAULT_DEVICE);
  const [title, setTitle] = useState("");
  const [clinic, setClinic] = useState(clinicDefault);
  const [settings, setSettings] = useState<RecorderSettings | null>(null);
  const [error, setError] = useState("");
  const camOn = open && what === "camera" && cameraOK;
  const preview = useCameraPreview(camOn, {
    cameraId: cameraId === DEFAULT_DEVICE ? undefined : cameraId,
    micId: micId === DEFAULT_DEVICE ? undefined : micId,
    mic,
  });
  const devices = useMediaDevices(camOn, preview.stream);
  const nativeOK = useNativeAvailable(); // the macOS app on macOS 15+: ScreenCaptureKit, with device audio
  const [nativeSound, setNativeSound] = useState(true);

  useEffect(() => {
    if (!open) return;
    setTitle("");
    setError("");
    setClinic(clinicDefault());
    setDeviceAudio(deviceLikely);
    setWhat(screenOK ? "screen" : "audio");
    let gone = false;
    recApi
      .active()
      .then((a) => {
        if (gone) return;
        setSettings(a.settings);
        useRecordings.setState({ active: isLive(a.recording) ? a.recording : null, settings: a.settings });
      })
      .catch((e) => !gone && setError((e as Error).message));
    return () => {
      gone = true;
    };
  }, [open, screenOK, deviceLikely]);

  const running = isLive(active) || phase !== "idle";
  const canStart = !running && (what === "screen" ? screenOK : what === "camera" ? cameraOK && !preview.error : micOK);

  function start() {
    if (what === "native") {
      if (running || !nativeOK) return;
      openRecordDialog(false);
      startNativeRecording({ mic, system_audio: nativeSound, title: title.trim() || undefined }).catch((e) =>
        toast.error("The recording didn't start", { description: (e as Error).message, duration: 12000 }),
      );
      return;
    }
    if (!canStart) return;
    const cameraStream = what === "camera" ? preview.handOver() : null;
    openRecordDialog(false);
    // No await before this call: the picker needs the click's user gesture.
    startRecording({
      what,
      mic: what === "audio" || mic,
      deviceAudio: what === "screen" && deviceAudio,
      bubble: what === "screen" && bubbleOK && bubble,
      cameraId: cameraId === DEFAULT_DEVICE ? undefined : cameraId,
      micId: micId === DEFAULT_DEVICE ? undefined : micId,
      cameraStream,
      title: title.trim() || undefined,
      settings: settings ?? DEFAULT_RECORDER_SETTINGS,
    }).catch((e) => toast.error("The recording didn't start", { description: (e as Error).message, duration: 12000 }));
  }

  // In the macOS app the native option comes first (it records device audio).
  useEffect(() => {
    if (open && nativeOK) setWhat("native");
  }, [open, nativeOK]);
  const nativeReady = what === "native" && nativeOK && !running;

  return (
    <Dialog open={open} onOpenChange={(v) => openRecordDialog(v)}>
      <DialogContent className="sm:max-w-[30rem]">
        <DialogHeader>
          <DialogTitle>Recording options</DialogTitle>
          <DialogDescription>Your screen, camera, microphone and device audio, saved on this machine. Agents can read your recordings.</DialogDescription>
        </DialogHeader>
        <div className="grid gap-2" role="radiogroup" aria-label="What to record">
          {nativeOK && <Choice {...nativeChoice} selected={what === "native"} onSelect={() => setWhat("native")} />}
          <Choice
            icon={Monitor}
            title="Screen or a window"
            detail={screenOK ? "You pick the whole screen or one window next." : "This window can't record the screen."}
            selected={what === "screen"}
            disabled={!screenOK}
            onSelect={() => setWhat("screen")}
          />
          <Choice
            icon={Video}
            title="Camera"
            detail={cameraOK ? "A clip of you, from the camera and microphone." : "This window can't use the camera."}
            selected={what === "camera"}
            disabled={!cameraOK}
            onSelect={() => setWhat("camera")}
          />
          <Choice
            icon={Mic}
            title="Microphone only"
            detail={micOK ? "A voice recording." : "This window can't use the microphone."}
            selected={what === "audio"}
            disabled={!micOK}
            onSelect={() => setWhat("audio")}
          />
        </div>
        {what === "native" && <NativeSwitches deviceAudio={nativeSound} onDeviceAudio={setNativeSound} mic={mic} onMic={setMic} />}
        {what === "camera" && (
          <div className="grid gap-2.5">
            <div className="relative overflow-hidden rounded-lg border bg-black">
              <CameraPreview stream={preview.stream} className="aspect-video max-h-[13.75rem] w-full" />
              {!preview.stream && (
                <p className="absolute inset-0 flex items-center justify-center px-6 text-center text-xs text-white/75">
                  {preview.error || "Opening the camera…"}
                </p>
              )}
            </div>
            {(devices.cameras.length > 1 || (mic && devices.mics.length > 1)) && (
              <div className="grid gap-2 sm:grid-cols-2">
                <DevicePicker label="Camera" devices={devices.cameras} value={cameraId} onChange={setCameraId} />
                {mic && <DevicePicker label="Microphone" devices={devices.mics} value={micId} onChange={setMicId} />}
              </div>
            )}
          </div>
        )}
        {(what === "screen" || what === "camera") && (
          <div className="grid gap-2.5">
            <label className="flex items-center gap-2.5 text-[0.8125rem]">
              <Switch checked={mic} onCheckedChange={setMic} disabled={!micOK} aria-label="Record my microphone too" />
              Record my microphone too
            </label>
            {what === "screen" && (
              <label className="flex items-start gap-2.5 text-[0.8125rem]">
                <Switch checked={deviceAudio} onCheckedChange={setDeviceAudio} aria-label="Device audio" className="mt-px" />
                <span className="min-w-0">
                  <span className="block">Device audio</span>
                  <span className="block text-xs text-muted-foreground">
                    {deviceLikely
                      ? "What this device plays, like the other side of a call. Mixed with your microphone and transcribed."
                      : "This window probably gives none (Safari, the macOS app's web view); it records without it."}
                  </span>
                </span>
              </label>
            )}
            {what === "screen" && bubbleOK && (
              <label className="flex items-start gap-2.5 text-[0.8125rem]">
                <Switch checked={bubble} onCheckedChange={setBubble} aria-label="Camera bubble" className="mt-px" />
                <span className="min-w-0">
                  <span className="block">Camera bubble</span>
                  <span className="block text-xs text-muted-foreground">You, from the camera, in a circle in the corner of the recording.</span>
                </span>
              </label>
            )}
          </div>
        )}
        <div className="grid gap-1.5">
          <Label htmlFor="rec-title">Title</Label>
          <Input
            id="rec-title"
            value={title}
            onChange={(e) => setTitle(e.target.value)}
            placeholder={what === "audio" ? "Voice recording" : what === "camera" ? "Camera clip" : "Screen recording"}
            maxLength={120}
            autoComplete="off"
            onKeyDown={(e) => e.key === "Enter" && start()}
          />
        </div>
        <ClinicCheckbox checked={clinic} onChange={(on) => (setClinic(on), setClinicDefault(on))} />
        <div className="grid gap-1 rounded-md border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
          <p className="font-medium text-foreground">Recording other people? Ask them first.</p>
          <p>Only what you pick here is recorded. A red bar shows on every page while it runs.</p>
        </div>
        {running && <p className="text-xs text-state-error">A recording is already running on this machine.</p>}
        {error && <p className="text-xs text-state-error">{error}</p>}
        <DialogFooter>
          <Button variant="ghost" onClick={() => openRecordDialog(false)}>
            Cancel
          </Button>
          <Button variant="destructive" onClick={start} disabled={!canStart && !nativeReady} data-start-recording>
            {phase === "starting" ? <Loader2 className="animate-spin" /> : <span className="size-2 rounded-full bg-white" aria-hidden />}
            Start recording
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function Choice({
  icon: Icon,
  title,
  detail,
  selected,
  disabled,
  onSelect,
}: {
  icon: React.ComponentType<{ className?: string }>;
  title: string;
  detail: string;
  selected: boolean;
  disabled?: boolean;
  onSelect: () => void;
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={selected}
      disabled={disabled}
      onClick={onSelect}
      className={cn(
        "flex items-center gap-3 rounded-lg border px-3 py-2.5 text-left outline-none transition-colors hover:bg-accent/60 focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50",
        selected && "border-border-strong bg-accent/60",
      )}
    >
      <span className="flex size-8 shrink-0 items-center justify-center rounded-md border bg-background">
        <Icon className="size-4 text-muted-foreground" />
      </span>
      <span className="min-w-0">
        <span className="block text-[0.8125rem] font-medium">{title}</span>
        <span className="block text-xs text-muted-foreground">{detail}</span>
      </span>
      <span className={cn("ml-auto size-3.5 shrink-0 rounded-full border", selected && "border-[4px] border-foreground")} aria-hidden />
    </button>
  );
}
