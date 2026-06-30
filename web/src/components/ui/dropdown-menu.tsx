import * as React from "react";
import { cn } from "@/lib/utils";

interface DropdownMenuContextValue {
  open: boolean;
  onOpenChange: (v: boolean) => void;
}

const DropdownMenuContext = React.createContext<DropdownMenuContextValue>({
  open: false,
  onOpenChange: () => {},
});

export function DropdownMenu({
  children,
  open: controlledOpen,
  onOpenChange,
  modal: _modal,
}: {
  children: React.ReactNode;
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
  modal?: boolean;
}) {
  const [internalOpen, setInternalOpen] = React.useState(false);
  const open = controlledOpen ?? internalOpen;
  const setOpen = onOpenChange ?? setInternalOpen;

  React.useEffect(() => {
    if (!open) return;

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setOpen(false);
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [open, setOpen]);

  return (
    <DropdownMenuContext.Provider value={{ open, onOpenChange: setOpen }}>
      <div className="relative inline-block text-left">{children}</div>
    </DropdownMenuContext.Provider>
  );
}

export function DropdownMenuTrigger({
  children,
  className,
  render,
  onClick,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  render?: React.ReactElement;
}) {
  const { open, onOpenChange } = React.useContext(DropdownMenuContext);

  const handleClick = (e: React.MouseEvent) => {
    e.stopPropagation();
    onOpenChange(!open);
  };

  if (render) {
    return React.cloneElement(render as React.DetailedReactHTMLElement<any, HTMLElement>, {
      ...props,
      onClick: (e: React.MouseEvent) => {
        (render.props as any).onClick?.(e);
        onClick?.(e as unknown as React.MouseEvent<HTMLButtonElement>);
        handleClick(e);
      },
      "aria-expanded": open,
      "aria-haspopup": "menu",
      "data-state": open ? "open" : "closed",
    }, children);
  }

  return (
    <button
      type="button"
      aria-expanded={open}
      aria-haspopup="menu"
      onClick={(e) => {
        onClick?.(e);
        handleClick(e);
      }}
      data-state={open ? "open" : "closed"}
      className={cn("inline-flex items-center justify-center", className)}
      {...props}
    >
      {children}
    </button>
  );
}

export function DropdownMenuContent({
  children,
  className,
  align = "end",
  sideOffset = 4,
  ...props
}: React.HTMLAttributes<HTMLDivElement> & {
  align?: "start" | "end";
  sideOffset?: number;
}) {
  const { open, onOpenChange } = React.useContext(DropdownMenuContext);
  const [mounted, setMounted] = React.useState(open);
  const close = React.useCallback(() => onOpenChange(false), [onOpenChange]);

  React.useEffect(() => {
    if (open) setMounted(true);
    else {
      const timer = setTimeout(() => setMounted(false), 150);
      return () => clearTimeout(timer);
    }
  }, [open]);

  if (!mounted && !open) return null;

  return (
    <>
      <div className="fixed inset-0 z-40" onClick={close} />
      <div
        className={cn(
          "absolute z-50 min-w-[8rem] overflow-hidden rounded-lg border border-border bg-popover text-popover-foreground shadow-md",
          align === "end" ? "right-0" : "left-0",
          className
        )}
        style={{ marginTop: sideOffset }}
        {...props}
      >
        {children}
      </div>
    </>
  );
}

export function DropdownMenuItem({
  children,
  className,
  variant,
  render,
  onClick,
  ...props
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "default" | "destructive";
  render?: React.ReactElement;
}) {
  const { onOpenChange } = React.useContext(DropdownMenuContext);

  const handleClick = (e: React.MouseEvent<HTMLButtonElement>) => {
    onClick?.(e);
    onOpenChange(false);
  };

  if (render) {
    return React.cloneElement(render as React.DetailedReactHTMLElement<any, HTMLElement>, {
      ...props,
      onClick: (e: React.MouseEvent) => {
        (render.props as any).onClick?.(e);
        handleClick(e as unknown as React.MouseEvent<HTMLButtonElement>);
      },
      className: cn(
        "relative flex cursor-pointer select-none items-center rounded-sm px-2 py-1.5 text-sm outline-none transition-colors hover:bg-accent hover:text-accent-foreground data-[disabled]:pointer-events-none data-[disabled]:opacity-50",
        variant === "destructive" && "text-destructive focus:text-destructive",
        (render.props as any).className
      ),
    }, children);
  }

  return (
    <button
      type="button"
      onClick={handleClick}
      className={cn(
        "relative flex w-full cursor-pointer select-none items-center rounded-sm px-2 py-1.5 text-left text-sm outline-none transition-colors hover:bg-accent hover:text-accent-foreground focus:bg-accent focus:text-accent-foreground disabled:pointer-events-none disabled:opacity-50",
        variant === "destructive" && "text-destructive",
        className
      )}
      {...props}
    >
      {children}
    </button>
  );
}

export function DropdownMenuSeparator({ className }: { className?: string }) {
  return <div className={cn("-mx-1 my-1 h-px bg-border", className)} />;
}
