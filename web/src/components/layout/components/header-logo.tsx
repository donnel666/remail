import { cn } from "@/lib/utils";

export function HeaderLogo({
  src = "/logo.png",
  alt = "Remail",
  className,
}: {
  src?: string;
  alt?: string;
  className?: string;
}) {
  return (
    <img
      src={src}
      alt={alt}
      className={cn("size-full rounded-lg object-contain", className)}
    />
  );
}
