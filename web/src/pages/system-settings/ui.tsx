import { type LabelHTMLAttributes, type ReactNode } from "react";
import { Button as SemiButton, Switch as SemiSwitch } from "@douyinfe/semi-ui";
import { cn } from "@/lib/utils";

export function Switch({ checked, onChange, disabled, ariaLabel }: { checked: boolean; onChange: (v: boolean) => void; disabled?: boolean; ariaLabel?: string }) {
  return <SemiSwitch checked={checked} onChange={onChange} disabled={disabled} aria-label={ariaLabel} />;
}

export function Button({ children, onClick, loading, variant = "default", className, type = "button" }: {
  children: ReactNode; onClick?: () => void; loading?: boolean; variant?: "default" | "outline" | "ghost"; className?: string; type?: "button" | "submit";
}) {
  const theme = variant === "default" ? "solid" : variant === "outline" ? "light" : "borderless";
  return <SemiButton htmlType={type} onClick={onClick} loading={loading} disabled={loading} theme={theme} type="primary" className={className}>{children}</SemiButton>;
}

export function Card({ children }: { title?: string; description?: string; children: ReactNode; className?: string }) {
  return <div className="contents [&>.p-6]:p-0">{children}</div>;
}

export function Label({ children, className, ...props }: LabelHTMLAttributes<HTMLLabelElement>) {
  return <label className={cn("text-sm font-medium leading-none peer-disabled:cursor-not-allowed peer-disabled:opacity-70", className)} {...props}>{children}</label>;
}

export function FormDescription({ children }: { children: ReactNode }) {
  return <p className="text-[0.8rem] text-muted-foreground">{children}</p>;
}
