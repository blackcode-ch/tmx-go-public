import { useState } from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  }
}

export function CopyButton({ text, label = "Copy", className }: { text: string; label?: string; className?: string }) {
  const [done, setDone] = useState(false);
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          variant="ghost"
          size="icon-xs"
          className={cn("text-muted-foreground hover:text-foreground", className)}
          aria-label={label}
          onClick={async (e) => {
            e.stopPropagation();
            if (await copyText(text)) {
              setDone(true);
              setTimeout(() => setDone(false), 1400);
            }
          }}
        >
          {done ? <Check className="text-state-ok" /> : <Copy />}
        </Button>
      </TooltipTrigger>
      <TooltipContent>{done ? "Copied" : label}</TooltipContent>
    </Tooltip>
  );
}
