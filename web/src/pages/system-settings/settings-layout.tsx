// NewAPI-style settings layout components

import { type ReactNode, useId } from "react";
import { Card as SemiCard, Form as SemiForm, Input, InputNumber, Select, Switch as SemiSwitch, TextArea, Typography } from "@douyinfe/semi-ui";
import { cn } from "@/lib/utils";
import { Switch as Sw, Label, FormDescription } from "./ui";

const { Text } = Typography;

export function SettingsCardHeader({ icon, title, description, enabled, onToggle, statusText }: {
  icon: ReactNode;
  title: string;
  description: string;
  enabled?: boolean;
  onToggle?: (enabled: boolean) => void;
  statusText?: string;
}) {
  return <div className="flex w-full items-start justify-between gap-4">
    <div className="flex min-w-0 items-start">
      <span className="mr-2 mt-0.5 shrink-0">{icon}</span>
      <div className="min-w-0">
        <Text>{title}</Text>
        <div><Text type="secondary" size="small">{description}</Text></div>
      </div>
    </div>
    {enabled !== undefined && onToggle ? <div className="flex shrink-0 items-center gap-2">
      <SemiSwitch aria-label={title} checked={enabled} onChange={onToggle} />
      <Text>{statusText}</Text>
    </div> : null}
  </div>;
}

// ---- SettingsSection — simple section with h3 heading ----
export function SettingsSection({ title, children, className }: { title: ReactNode; children: ReactNode; className?: string }) {
  return <SemiCard className={className} style={{ marginTop: 10 }}>
    <SemiForm.Section text={title}>{children}</SemiForm.Section>
  </SemiCard>;
}

export function SettingsAccessBoundary({ canWrite, children }: { canWrite: boolean; children: ReactNode }) {
  return (
    <fieldset
      aria-disabled={!canWrite}
      className="contents"
      disabled={!canWrite}
      onClickCapture={(event) => {
        if (!canWrite) {
          event.preventDefault();
          event.stopPropagation();
        }
      }}
      onKeyDownCapture={(event) => {
        if (!canWrite) {
          event.preventDefault();
          event.stopPropagation();
        }
      }}
    >
      {children}
    </fieldset>
  );
}

// ---- SettingsFormGrid — 2-column responsive grid (matches NewAPI exactly) ----
export function SettingsFormGrid({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("grid min-w-0 gap-x-4 gap-y-4 md:grid-cols-2 xl:grid-cols-3", "md:[&>[data-settings-form-span=full]]:col-span-2", "xl:[&>[data-settings-form-span=full]]:col-span-3", "[&>[data-slot=form-item]]:min-w-0", "md:[&>[data-slot=form-item]:has(textarea)]:col-span-2", "xl:[&>[data-slot=form-item]:has(textarea)]:col-span-3", className)}>
    {children}
  </div>;
}

export function SettingsInvalidValuesNotice({ keys, message }: { keys: readonly string[]; message: string }) {
  if (keys.length === 0) return null;
  return <p role="alert" aria-live="polite" className="mt-3 text-sm text-[var(--semi-color-danger)]">
    {message}: {keys.join(", ")}
  </p>;
}

// ---- FormItem — wraps a single form field ----
export function FormItem({ children, spanFull }: { children: ReactNode; spanFull?: boolean }) {
  return <div data-settings-form-span={spanFull ? "full" : undefined} data-slot="form-item" className="flex flex-col gap-1.5 min-w-0">
    {children}
  </div>;
}

// ---- FormLabel ----
export function FormLabel({ children }: { children: ReactNode }) {
  return <Label>{children}</Label>;
}

// ---- SettingsSwitchField — full-width switch row with label + description ----
export function SettingsSwitchField({ checked, onChange, label, description, disabled }: {
  checked: boolean; onChange: (v: boolean) => void; label: string; description?: string; disabled?: boolean;
}) {
  return (
    <div data-slot="form-item" className="flex min-h-14 min-w-0 flex-row items-center justify-between gap-4 rounded-lg px-2 py-2 transition-colors hover:bg-surface-sunken/70">
      <div className="min-w-0 space-y-0.5">
        <Label>{label}</Label>
        {description && <FormDescription>{description}</FormDescription>}
      </div>
      <Sw checked={checked} onChange={onChange} disabled={disabled} ariaLabel={label} />
    </div>
  );
}

// ---- SettingsNumberField — number input with label in 2-col grid ----
export function SettingsNumberField({ label, value, onChange, min, max, precision, step }: {
  label: string; value: number | undefined; onChange: (v: number) => void; min?: number; max?: number; precision?: number; step?: number;
}) {
  const id = useId();
  return (
    <FormItem>
      <Label htmlFor={id}>{label}</Label>
      <InputNumber id={id} value={value} onNumberChange={onChange} min={min} max={max} precision={precision} step={step} style={{ width: "100%" }} />
    </FormItem>
  );
}

// ---- SettingsTextField — text input with label ----
export function SettingsTextField({ label, value, onChange, placeholder, type, disabled }: {
  label: string; value: string | undefined; onChange: (v: string) => void; placeholder?: string; type?: string; disabled?: boolean;
}) {
  const id = useId();
  return (
    <FormItem>
      <Label htmlFor={id}>{label}</Label>
      <Input id={id} type={type ?? "text"} mode={type === "password" ? "password" : undefined} value={value} onChange={onChange} placeholder={placeholder} disabled={disabled} />
    </FormItem>
  );
}

// ---- SettingsTextareaField — textarea with label, spans full width ----
export function SettingsTextareaField({ label, value, onChange, rows, placeholder }: {
  label: string; value: string; onChange: (v: string) => void; rows?: number; placeholder?: string;
}) {
  const id = useId();
  return (
    <FormItem spanFull>
      <Label htmlFor={id}>{label}</Label>
      <TextArea id={id} value={value} onChange={onChange} rows={rows ?? 3} placeholder={placeholder} />
    </FormItem>
  );
}

// ---- SettingsSelectField — select/dropdown with label ----
export function SettingsSelectField({ label, value, onChange, options, placeholder }: {
  label: string; value: string; onChange: (v: string) => void; options: { label: string; value: string }[]; placeholder?: string;
}) {
  const id = useId();
  return (
    <FormItem>
      <Label>{label}</Label>
      <Select
        id={id}
        aria-label={label}
        value={value}
        onChange={(next) => onChange(String(next ?? ""))}
        optionList={options}
        placeholder={placeholder}
        style={{ width: "100%" }}
      />
    </FormItem>
  );
}
