import { Button } from "@douyinfe/semi-ui";

import { useIsMobile } from "@/hooks/use-is-mobile";

interface CompactModeToggleProps {
  compactMode: boolean;
  setCompactMode: (value: boolean) => void;
  t: (key: string) => string;
  size?: "small" | "default" | "large";
  type?: "primary" | "secondary" | "tertiary" | "warning" | "danger";
  className?: string;
}

export function CompactModeToggle({
  compactMode,
  setCompactMode,
  t,
  size = "small",
  type = "tertiary",
  className = "",
}: CompactModeToggleProps) {
  const isMobile = useIsMobile();

  if (isMobile) return null;

  return (
    <Button
      className={`w-full md:w-auto ${className}`}
      onClick={() => setCompactMode(!compactMode)}
      size={size}
      type={type}
    >
      {compactMode ? t("Adaptive list") : t("Compact list")}
    </Button>
  );
}
