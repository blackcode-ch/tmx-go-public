// The owner PIN field: digits only, 6 to 12, the phone's number pad, one-time-code autofill off.
import { Input } from "@/components/ui/input";

export const PIN_MIN = 6;
export const PIN_MAX = 12;
export const validPin = (pin: string) => new RegExp(`^[0-9]{${PIN_MIN},${PIN_MAX}}$`).test(pin);

export function PinInput(props: { value: string; onChange: (v: string) => void; autoFocus?: boolean; id?: string; label?: string; disabled?: boolean }) {
  return (
    <Input
      id={props.id}
      aria-label={props.label ?? "Owner PIN"}
      type="password"
      inputMode="numeric"
      autoComplete="off"
      pattern="[0-9]*"
      maxLength={PIN_MAX}
      autoFocus={props.autoFocus}
      disabled={props.disabled}
      className="text-center font-mono text-lg tracking-[0.4em]"
      value={props.value}
      onChange={(e) => props.onChange(e.target.value.replace(/[^0-9]/g, "").slice(0, PIN_MAX))}
    />
  );
}
