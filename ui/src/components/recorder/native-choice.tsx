// The start dialog's native option in the macOS app (DESIGN.md "Recorder", native capture):
// "Screen with sound" records a display, a window or an app, picked in the macOS picker, with
// what the Mac plays and the microphone. Shown only where the window can record natively.
import { MonitorSpeaker } from "lucide-react";
import { Switch } from "@/components/ui/switch";

/** What the dialog's choice row shows for the native option. */
export const nativeChoice = {
  icon: MonitorSpeaker,
  title: "Screen with sound",
  detail: "A display, a window or an app, picked next in the macOS picker. With what the Mac plays.",
};

export function NativeSwitches({
  deviceAudio,
  onDeviceAudio,
  mic,
  onMic,
}: {
  deviceAudio: boolean;
  onDeviceAudio: (v: boolean) => void;
  mic: boolean;
  onMic: (v: boolean) => void;
}) {
  return (
    <div className="grid gap-2.5" data-native-switches>
      <label className="flex items-start gap-2.5 text-[0.8125rem]">
        <Switch checked={deviceAudio} onCheckedChange={onDeviceAudio} aria-label="Device audio" className="mt-px" />
        <span className="min-w-0">
          <span className="block">Device audio</span>
          <span className="block text-xs text-muted-foreground">Everything the Mac plays, like the other side of a call. Mixed with your microphone and transcribed.</span>
        </span>
      </label>
      <label className="flex items-start gap-2.5 text-[0.8125rem]">
        <Switch checked={mic} onCheckedChange={onMic} aria-label="Microphone" className="mt-px" />
        <span className="min-w-0">
          <span className="block">Microphone</span>
          <span className="block text-xs text-muted-foreground">macOS asks the first time. Without permission, the screen and device audio are still recorded.</span>
        </span>
      </label>
    </div>
  );
}
