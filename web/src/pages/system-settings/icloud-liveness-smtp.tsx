import { useState } from "react";
import { Button, Input, Switch, Table, Toast } from "@douyinfe/semi-ui";
import { Mail, Plus, Save, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import { parseSettingsList, selectOptions } from "@/lib/system-settings-api";
import type { SectionProps } from "./index";
import { ICLOUD_LIVENESS_SMTP_KEYS } from "./email-service-keys";
import { SettingsAccessBoundary, SettingsCardHeader, SettingsNumberField, SettingsSection, SettingsSwitchField } from "./settings-layout";

interface SMTPServer {
  id: string;
  name: string;
  enabled: boolean;
  addr: string;
  username: string;
  password: string;
  from: string;
  hasPassword?: boolean;
}

export default function ICloudLivenessSMTPSection({ options, onBulkSave, canWrite, canSensitive }: SectionProps) {
  const { t } = useTranslation();
  const initial = selectOptions(canSensitive ? options : [], ICLOUD_LIVENESS_SMTP_KEYS);
  const [enabled, setEnabled] = useState(initial.icloud_liveness_smtp_enabled === "true");
  const [interval, setInterval] = useState(Number(initial.icloud_liveness_smtp_interval_seconds || 60));
  const [servers, setServers] = useState<SMTPServer[]>(() => parseSettingsList<SMTPServer>(initial.icloud_liveness_smtp_servers).map((server) => ({ ...server, password: "" })));
  const [saving, setSaving] = useState(false);
  const allowed = canWrite && canSensitive;
  const update = (id: string, key: keyof SMTPServer, value: string | boolean) => setServers((current) => current.map((server) => server.id === id ? { ...server, [key]: value } : server));
  const add = () => setServers((current) => [...current, { id: crypto.randomUUID(), name: "", enabled: true, addr: "smtp.qq.com:465", username: "", password: "", from: "" }]);
  const save = async () => {
    if (!allowed) return;
    if (!Number.isInteger(interval) || interval < 1 || interval > 86400) {
      Toast.error(t("SMTP sending interval must be a whole number from 1 to 86400 seconds."));
      return;
    }
    if (servers.some((server) => !server.name.trim() || !server.addr.trim() || !server.username.trim() || !server.from.trim() || server.enabled && !server.password && !server.hasPassword)) {
      Toast.error(t("Complete each SMTP service and provide an authorization code for new enabled services."));
      return;
    }
    setSaving(true);
    try {
      await onBulkSave([
        { key: "icloud_liveness_smtp_enabled", value: String(enabled) },
        { key: "icloud_liveness_smtp_servers", value: JSON.stringify(servers.map(({ hasPassword: _hasPassword, ...server }) => server)) },
        { key: "icloud_liveness_smtp_interval_seconds", value: String(interval) },
      ]);
      setServers((current) => current.map((server) => ({ ...server, hasPassword: Boolean(server.password || server.hasPassword), password: "" })));
    } finally { setSaving(false); }
  };
  const textColumn = (label: string, key: "name" | "addr" | "username" | "from" | "password", width = 190) => ({
    title: t(label), dataIndex: key, width,
    render: (_value: unknown, server: SMTPServer, index: number) => <Input
      aria-label={`${t(label)} ${index + 1}`} value={server[key]} disabled={!allowed || saving}
      mode={key === "password" ? "password" : undefined} type={key === "password" ? "password" : "text"}
      placeholder={key === "password" && server.hasPassword ? t("Leave blank to keep the saved authorization code") : undefined}
      onChange={(value) => update(server.id, key, value)}
    />,
  });
  const columns = [
    textColumn("SMTP service name", "name", 160),
    textColumn("SMTP server address", "addr"),
    textColumn("SMTP username", "username"),
    textColumn("Liveness sender email", "from"),
    textColumn("SMTP password or authorization code", "password", 230),
    { title: t("Enabled"), width: 90, render: (_value: unknown, server: SMTPServer, index: number) => <Switch aria-label={`${t("Enabled")} ${index + 1}`} checked={server.enabled} disabled={!allowed || saving} onChange={(value) => update(server.id, "enabled", value)} /> },
    { title: t("Actions"), width: 90, render: (_value: unknown, server: SMTPServer, index: number) => <Button aria-label={`${t("Delete SMTP service")} ${index + 1}`} icon={<Trash2 size={14} />} type="danger" theme="borderless" disabled={!allowed || saving} onClick={() => setServers((current) => current.filter((item) => item.id !== server.id))}>{t("Delete")}</Button> },
  ];

  return <SettingsSection title={<SettingsCardHeader icon={<Mail size={16} />} title={t("iCloud liveness SMTP services")} description={t("Enabled SMTP services take turns sending liveness emails. All services and retries share the configured sending interval.")} />}>
    <SettingsAccessBoundary canWrite={allowed}>
      <SettingsSwitchField label={t("Use dedicated liveness SMTP")} checked={enabled} onChange={setEnabled} disabled={!allowed || saving} description={t("When disabled, liveness uses the system mail transport.")} />
      <fieldset disabled={!allowed || saving} className="mt-4">
        <SettingsNumberField label={t("SMTP sending interval (seconds)")} value={interval} onChange={setInterval} min={1} max={86400} precision={0} step={1} description={t("Send at most one liveness email per interval across all SMTP services. Default: 60 seconds. Bulk checks wait in the queue; retries cannot bypass this limit.")} />
      </fieldset>
      <div className="my-4 flex justify-end"><Button icon={<Plus size={14} />} disabled={!allowed || saving || servers.length >= 50} onClick={add}>{t("Add SMTP service")}</Button></div>
      <Table rowKey="id" dataSource={servers} columns={columns} pagination={false} scroll={{ x: 1140 }} empty={t("No SMTP services configured.")} />
      <p className="mt-3 text-xs text-[var(--semi-color-text-2)]">{t("For QQ Mail, enable SMTP and use its authorization code. Use the same QQ address for username and sender. Port 465 uses SSL/TLS; port 587 uses STARTTLS.")}</p>
      <p className="mt-2 text-xs text-[var(--semi-color-text-2)]">{t("Save the list after editing. Disabled services are skipped; no available service stops the check without falling back to system mail.")}</p>
      <p className="mt-2 text-xs text-[var(--semi-color-text-2)]">{t("Dedicated SMTP checks succeed only when the forwarded test email reaches this platform. Delivery not confirmed within 10 minutes leaves account health unchanged.")}</p>
      <Button icon={<Save size={14} />} disabled={!allowed} loading={saving} onClick={() => void save().catch(() => undefined)} theme="solid" type="primary" className="mt-5">{t("保存设置")}</Button>
    </SettingsAccessBoundary>
    {!canSensitive && <p className="mt-3 text-sm text-[var(--semi-color-text-2)]">{t("需要敏感设置权限")}</p>}
  </SettingsSection>;
}
