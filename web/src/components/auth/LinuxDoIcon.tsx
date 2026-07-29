import { cn } from "@/lib/utils";

export function LinuxDoIcon({ className }: { className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={cn("inline-grid size-4 shrink-0 overflow-hidden rounded-full ring-1 ring-black/15 dark:ring-white/20", className)}
      style={{ gridTemplateRows: "4.67fr 6.66fr 4.67fr" }}
    >
      <span className="bg-[#1d1d1f]" />
      <span className="bg-[#efefef]" />
      <span className="bg-[#feb005]" />
    </span>
  );
}
