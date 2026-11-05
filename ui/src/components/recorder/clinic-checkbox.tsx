// The one "clinic patient data" checkbox (lib/clinic.ts): in the recording options, and on a recording while
// it runs and on its report. Owner only (the window is the owner's).
import { Checkbox } from "@/components/ui/checkbox";
import { CLINIC_HINT, CLINIC_LABEL } from "@/lib/clinic";

export function ClinicCheckbox({ checked, onChange, disabled, id = "rec-clinic" }: {
  checked: boolean;
  onChange: (on: boolean) => void;
  disabled?: boolean;
  id?: string;
}) {
  return (
    <label htmlFor={id} className="flex items-start gap-2 text-sm" data-clinic>
      <Checkbox id={id} checked={checked} disabled={disabled} onCheckedChange={(v) => onChange(v === true)} className="mt-0.5" />
      <span className="grid gap-0.5">
        <span>{CLINIC_LABEL}</span>
        <span className="text-xs text-muted-foreground">{CLINIC_HINT}</span>
      </span>
    </label>
  );
}
