import nodeLocLogo from "@/assets/nodeloc.png";
import { cn } from "@/lib/utils";

export function NodeLocIcon({ className }: { className?: string }) {
  return <img alt="" aria-hidden="true" className={cn("size-4 shrink-0 rounded-[20%]", className)} src={nodeLocLogo} />;
}
