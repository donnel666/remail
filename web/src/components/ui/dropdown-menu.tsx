import * as React from "react";
import { cn } from "@/lib/utils";

interface DropdownMenuContextValue {
  closeOnOutsideClick: boolean;
  open: boolean;
  openOnHover: boolean;
  onOpenChange: (v: boolean) => void;
}

const DropdownMenuContext = React.createContext<DropdownMenuContextValue>({
  closeOnOutsideClick: true,
  open: false,
  openOnHover: false,
  onOpenChange: () => {},
});

const DROPDOWN_OPEN_EVENT = "remail-dropdown-open";

export function DropdownMenu({
  children,
  open: controlledOpen,
  onOpenChange,
  openOnHover = false,
  hoverCloseDelay = 120,
  closeOnOutsideClick,
  modal: _modal,
}: {
  children: React.ReactNode;
  open?: boolean;
  onOpenChange?: (v: boolean) => void;
  openOnHover?: boolean;
  hoverCloseDelay?: number;
  closeOnOutsideClick?: boolean;
  modal?: boolean;
}) {
  const [internalOpen, setInternalOpen] = React.useState(false);
  const open = controlledOpen ?? internalOpen;
  const setOpen = onOpenChange ?? setInternalOpen;
  const menuId = React.useId();
  const closeTimerRef = React.useRef<number | null>(null);
  const shouldCloseOnOutsideClick = closeOnOutsideClick ?? !openOnHover;

  const clearCloseTimer = React.useCallback(() => {
    if (closeTimerRef.current === null) return;
    window.clearTimeout(closeTimerRef.current);
    closeTimerRef.current = null;
  }, []);

  const setMenuOpen = React.useCallback(
    (nextOpen: boolean) => {
      if (nextOpen) {
        window.dispatchEvent(
          new CustomEvent(DROPDOWN_OPEN_EVENT, { detail: menuId })
        );
      }
      setOpen(nextOpen);
    },
    [menuId, setOpen]
  );

  const handleMouseEnter = React.useCallback(() => {
    if (!openOnHover) return;
    clearCloseTimer();
    setMenuOpen(true);
  }, [clearCloseTimer, openOnHover, setMenuOpen]);

  const handleMouseLeave = React.useCallback(() => {
    if (!openOnHover) return;
    clearCloseTimer();
    closeTimerRef.current = window.setTimeout(() => {
      setMenuOpen(false);
      closeTimerRef.current = null;
    }, hoverCloseDelay);
  }, [clearCloseTimer, hoverCloseDelay, openOnHover, setMenuOpen]);

  React.useEffect(() => {
    if (!open) return;

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setMenuOpen(false);
    };

    window.addEventListener("keydown", handleKeyDown);
    return () => window.removeEventListener("keydown", handleKeyDown);
  }, [open, setMenuOpen]);

  React.useEffect(() => clearCloseTimer, [clearCloseTimer]);

  React.useEffect(() => {
    const handleDropdownOpen = (event: Event) => {
      if (!(event instanceof CustomEvent)) return;
      if (event.detail !== menuId) setOpen(false);
    };

    window.addEventListener(DROPDOWN_OPEN_EVENT, handleDropdownOpen);
    return () => {
      window.removeEventListener(DROPDOWN_OPEN_EVENT, handleDropdownOpen);
    };
  }, [menuId, setOpen]);

  return (
    <DropdownMenuContext.Provider
      value={{
        closeOnOutsideClick: shouldCloseOnOutsideClick,
        open,
        openOnHover,
        onOpenChange: setMenuOpen,
      }}
    >
      <div
        className="relative inline-block text-left"
        onMouseEnter={handleMouseEnter}
        onMouseLeave={handleMouseLeave}
      >
        {children}
      </div>
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
  const { open, openOnHover, onOpenChange } = React.useContext(DropdownMenuContext);

  const handleClick = (e: React.MouseEvent) => {
    e.stopPropagation();
    if (openOnHover && open) return;
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
  const { closeOnOutsideClick, open, onOpenChange } = React.useContext(DropdownMenuContext);
  const close = React.useCallback(() => onOpenChange(false), [onOpenChange]);

  if (!open) return null;

  return (
    <>
      {closeOnOutsideClick ? <div className="fixed inset-0 z-40" onClick={close} /> : null}
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
        className,
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
