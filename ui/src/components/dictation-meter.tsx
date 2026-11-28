import { useEffect, useState } from "react";
import { dictationLevel, dictationNoInput } from "@/lib/dictation-session";

export function DictationMeter() {
  const [input, setInput] = useState({ level: 0, missing: false });
  useEffect(() => {
    const sample = () => setInput({ level: dictationLevel(), missing: dictationNoInput() });
    sample();
    const id = setInterval(sample, 100);
    return () => clearInterval(id);
  }, []);
  return (
    <span className="inline-flex min-w-0 flex-wrap items-center gap-x-2">
      <meter className="h-2 w-14 shrink-0 accent-state-working" min={0} max={1} value={input.level} aria-label="Microphone input level" />
      {input.missing && <span role="alert" className="font-medium text-foreground">No microphone input</span>}
    </span>
  );
}
