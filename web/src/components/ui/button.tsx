import * as React from "react";
import { cn } from "@/lib/utils";

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "default" | "ghost" | "outline" | "destructive";
  size?: "sm" | "default" | "icon";
  render?: React.ReactElement;
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant = "default", size = "default", children, render, ...props }, ref) => {
    const baseClass = cn(
      "inline-flex items-center justify-center whitespace-nowrap rounded-lg text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:pointer-events-none disabled:opacity-50 gap-2 [&_svg]:pointer-events-none [&_svg]:size-4 [&_svg]:shrink-0",
      {
        "bg-primary text-primary-foreground shadow hover:bg-primary/90": variant === "default",
        "text-foreground hover:bg-accent hover:text-accent-foreground": variant === "ghost",
        "border border-input bg-background shadow-sm hover:bg-accent hover:text-accent-foreground": variant === "outline",
        "bg-destructive text-destructive-foreground shadow-sm hover:bg-destructive/90": variant === "destructive",
      },
      {
        "h-8 rounded-lg px-3 text-xs": size === "sm",
        "h-9 px-4 py-2": size === "default",
        "h-9 w-9": size === "icon",
      },
      className
    );

    if (render) {
      return React.cloneElement(render as React.DetailedReactHTMLElement<any, HTMLElement>, {
        ...props,
        ref,
        className: cn(baseClass, (render.props as any).className),
      }, children);
    }

    return (
      <button className={baseClass} ref={ref} {...props}>
        {children}
      </button>
    );
  }
);
Button.displayName = "Button";

export { Button };
