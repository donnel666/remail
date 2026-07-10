import { Tooltip } from "@douyinfe/semi-ui";
import {
  useLayoutEffect,
  useRef,
  useState,
  type ComponentProps,
  type ReactNode,
} from "react";

import { cn } from "@/lib/utils";

interface OverflowTooltipProps {
  children: ReactNode;
  className?: string;
  content?: ReactNode;
  force?: boolean;
  mouseEnterDelay?: number;
  position?: ComponentProps<typeof Tooltip>["position"];
  wrapperClassName?: string;
}

export function OverflowTooltip({
  children,
  className,
  content,
  force = false,
  mouseEnterDelay = 0,
  position = "top",
  wrapperClassName,
}: OverflowTooltipProps) {
  const targetRef = useRef<HTMLSpanElement>(null);
  const [overflowed, setOverflowed] = useState(false);

  useLayoutEffect(() => {
    const target = targetRef.current;
    if (!target) return;

    const updateOverflow = () => {
      setOverflowed(
        target.scrollWidth - target.clientWidth > 1 ||
          target.scrollHeight - target.clientHeight > 1
      );
    };

    updateOverflow();

    if (typeof ResizeObserver === "undefined") {
      if (typeof window === "undefined") return;
      window.addEventListener("resize", updateOverflow);
      return () => window.removeEventListener("resize", updateOverflow);
    }

    const observer = new ResizeObserver(updateOverflow);
    observer.observe(target);
    if (target.parentElement) observer.observe(target.parentElement);

    return () => observer.disconnect();
  }, [children, content]);

  return (
    <Tooltip
      className="remail-overflow-tooltip"
      condition={force || overflowed}
      content={content ?? children}
      mouseEnterDelay={mouseEnterDelay}
      position={position}
      showArrow
      wrapperClassName={cn("remail-overflow-tooltip-wrapper", wrapperClassName)}
    >
      <span
        className={cn("remail-overflow-tooltip-target", className)}
        ref={targetRef}
      >
        {children}
      </span>
    </Tooltip>
  );
}
