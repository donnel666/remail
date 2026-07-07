import { cn } from "@/lib/utils";

export function ProjectIcon({
  className,
  logoUrl,
  name,
  size = 28,
}: {
  className?: string;
  logoUrl?: string;
  name: string;
  size?: number;
}) {
  const shellSize = size + 6;

  if (logoUrl) {
    return (
      <span
        className={cn("workbench-project-icon", className)}
        style={{ height: shellSize, width: shellSize }}
      >
        <img alt={name} src={logoUrl} style={{ height: size, width: size }} />
      </span>
    );
  }

  return (
    <span
      className={cn("workbench-project-icon", className)}
      style={{ height: shellSize, width: shellSize }}
    >
      <span style={{ height: size, width: size }}>
        {name.slice(0, 2).toUpperCase()}
      </span>
    </span>
  );
}
