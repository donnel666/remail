import * as React from "react";
import { Button, type ButtonProps } from "@/components/ui/button";
import { cn } from "@/lib/utils";

export const HEADER_ACTION_BUTTON_CLASS = cn(
  "hidden h-8 w-8 rounded-full bg-surface-sunken p-1.5 font-semibold text-foreground/80 shadow-none",
  "hover:bg-surface-hover hover:text-foreground focus-visible:ring-0 md:inline-flex",
  "[&_svg]:!size-[18px]"
);

export const HeaderActionButton = React.forwardRef<
  HTMLButtonElement,
  Omit<ButtonProps, "variant" | "size">
>(({ className, ...props }, ref) => (
  <Button
    ref={ref}
    variant="ghost"
    size="icon"
    className={cn(HEADER_ACTION_BUTTON_CLASS, className)}
    {...props}
  />
));

HeaderActionButton.displayName = "HeaderActionButton";
