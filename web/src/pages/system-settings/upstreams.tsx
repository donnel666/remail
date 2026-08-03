import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  Checkbox,
  Input,
  Select,
  Spin,
  Switch,
  Table,
  Tag,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { Cloud, Link2, RefreshCw, Save, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  deleteGmailUpstreamMapping,
  getSMSBowerStatus,
  listGmailUpstreamMappings,
  listSMSBowerServices,
  saveGmailUpstreamMapping,
  syncSMSBower,
  type GmailUpstreamMapping,
  type SMSBowerAccountStatus,
  type SMSBowerService,
} from "@/lib/gmail-upstream-api";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { parseOption } from "@/lib/system-settings-api";

import type { SectionProps } from "./index";
import { buildSMSBowerSettingsUpdates } from "./upstream-settings-values";
import {
  SettingsAccessBoundary,
  SettingsCardHeader,
  SettingsFormGrid,
  SettingsNumberField,
  SettingsSection,
  SettingsSwitchField,
  SettingsTextField,
} from "./settings-layout";

const { Text } = Typography;
const SETTINGS_DEFAULTS = {
  smsbower_enabled: false,
  smsbower_sync_interval_minutes: 5,
  smsbower_balance_warning_threshold: 0,
  smsbower_points_per_unit: 1,
  smsbower_min_margin_rate: 0.1,
};

const DEFAULT_ROUTE_SOURCES = ["smsbower", "local"];
type RouteDraft = {
  codeEnabled: boolean;
  enabled: boolean;
  providerServiceCode: string;
  purchaseEnabled: boolean;
  source: string;
};
type RouteRow = GmailUpstreamMapping & { source: string };

function routeKey(projectId: number, source: string) {
  return `${projectId}:${source}`;
}

function formatTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString();
}

function formatPoints(value?: string) {
  const number = Number(value ?? 0);
  return Number.isFinite(number)
    ? number.toFixed(6).replace(/\.?0+$/, "")
    : value ?? "0";
}

function formatRate(value?: string) {
  const number = Number(value ?? 0);
  return Number.isFinite(number) ? `${(number * 100).toFixed(2)}%` : "-";
}

function unsafeReasonLabel(reason?: string) {
  const labels: Record<string, string> = {
    insufficient_upstream_balance: "上游余额不足",
    invalid_price: "价格配置无效",
    local_supply_not_configured: "自有 Gmail 尚未接入",
    margin_below_floor: "低于最低毛利率",
    mode_disabled: "该渠道未参与此模式",
    out_of_stock: "上游暂无库存",
    product_missing: "项目尚未配置 Gmail 商品",
    provider_mode_unsupported: "该渠道暂不支持此履约模式",
    quote_stale: "价格或账户状态已过期",
    route_disabled: "渠道已停用",
    route_missing: "尚未配置渠道",
    service_inactive: "上游项目已下线",
  };
  return reason ? labels[reason] ?? reason : "可用";
}

function SafetyTag({ safe, reason, rate }: { safe: boolean; reason?: string; rate?: string }) {
  return (
    <div className="flex flex-col items-start gap-1">
      <Tag color={safe ? "green" : reason === "mode_disabled" ? "grey" : "red"} shape="circle" size="small">
        {safe ? "安全" : unsafeReasonLabel(reason)}
      </Tag>
      {reason !== "mode_disabled" ? <Text size="small" type="tertiary">毛利 {formatRate(rate)}</Text> : null}
    </div>
  );
}

export default function UpstreamsSection({
  canSensitive,
  canWrite,
  onBulkSave,
  options,
}: SectionProps) {
  const { t } = useTranslation();
  const initial = parseOption(options, SETTINGS_DEFAULTS);
  const [form, setForm] = useState({
    ...initial,
    smsbower_api_key: "",
    smsbower_min_margin_percent: initial.smsbower_min_margin_rate * 100,
  });
  const [status, setStatus] = useState<SMSBowerAccountStatus | null>(null);
  const [services, setServices] = useState<SMSBowerService[]>([]);
  const [mappings, setMappings] = useState<GmailUpstreamMapping[]>([]);
  const [drafts, setDrafts] = useState<Record<string, RouteDraft>>({});
  const [newRoute, setNewRoute] = useState({ projectId: 0, providerServiceCode: "", source: "" });
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [savingRoute, setSavingRoute] = useState<string | null>(null);
  const loadRequestRef = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    loadRequestRef.current?.abort();
    const controller = new AbortController();
    loadRequestRef.current = controller;
    setLoading(true);
    try {
      const [nextStatus, nextServices, nextMappings] = await Promise.all([
        getSMSBowerStatus(controller.signal),
        listSMSBowerServices(controller.signal),
        listGmailUpstreamMappings(controller.signal),
      ]);
      if (controller.signal.aborted) return;
      setStatus(nextStatus);
      setServices(nextServices);
      setMappings(nextMappings);
      const firstService = nextServices.find((item) => item.active)?.code ?? "";
      const nextDrafts: Record<string, RouteDraft> = {};
      const projectIds = Array.from(new Set(nextMappings.map((item) => item.projectId)));
      for (const projectId of projectIds) {
        const sources = new Set(DEFAULT_ROUTE_SOURCES);
        for (const item of nextMappings) {
          if (item.projectId === projectId && item.source) sources.add(item.source);
        }
        for (const source of sources) {
          const route = nextMappings.find((item) => item.projectId === projectId && item.source === source);
          nextDrafts[routeKey(projectId, source)] = {
            codeEnabled: route?.codeEnabled ?? false,
            enabled: route?.enabled ?? false,
            providerServiceCode: source === "local" ? "" : route?.providerServiceCode ?? (source === "smsbower" ? firstService : ""),
            purchaseEnabled: route?.purchaseEnabled ?? false,
            source,
          };
        }
      }
      setDrafts(nextDrafts);
      setNewRoute((current) => ({
        ...current,
        projectId: projectIds.includes(current.projectId) ? current.projectId : projectIds[0] ?? 0,
      }));
    } catch (error) {
      if (controller.signal.aborted) return;
      Toast.error(getIamErrorMessage(t, error, "Upstream settings load failed."));
    } finally {
      if (loadRequestRef.current === controller) {
        loadRequestRef.current = null;
        setLoading(false);
      }
    }
  }, [t]);

  useEffect(() => {
    void load();
    return () => loadRequestRef.current?.abort();
  }, [load]);

  const routeRows = useMemo<RouteRow[]>(() => {
    const projects = new Map<number, GmailUpstreamMapping>();
    for (const item of mappings) {
      if (!projects.has(item.projectId)) projects.set(item.projectId, item);
    }
    return Array.from(projects.values()).flatMap((project) => {
      const sources = new Set(DEFAULT_ROUTE_SOURCES);
      for (const item of mappings) {
        if (item.projectId === project.projectId && item.source) sources.add(item.source);
      }
      return Array.from(sources).map((source) => {
        const existing = mappings.find((item) => item.projectId === project.projectId && item.source === source);
        if (existing) return { ...existing, source };
        return {
          ...project,
          codeEnabled: false,
          codeMarginRate: "0",
          codeSafe: false,
          codeUnsafeReason: project.productId ? "route_missing" as const : "product_missing" as const,
          costPoints: "0",
          enabled: false,
          providerServiceCode: "",
          providerServiceName: "",
          purchaseEnabled: false,
          purchaseMarginRate: "0",
          purchaseSafe: false,
          purchaseUnsafeReason: project.productId ? "route_missing" as const : "product_missing" as const,
          source,
          upstreamPrice: "0",
        };
      });
    });
  }, [mappings]);

  const updateDraft = (projectId: number, source: string, patch: Partial<RouteDraft>) => {
    const key = routeKey(projectId, source);
    setDrafts((current) => ({ ...current, [key]: { ...current[key], source, ...patch } }));
  };

  const saveSettings = async () => {
    if (form.smsbower_enabled && !status?.configured && !form.smsbower_api_key.trim()) {
      Toast.warning("首次启用 SMSBower 时必须填写 API Key。");
      return;
    }
    if (
      form.smsbower_sync_interval_minutes < 1 ||
      form.smsbower_sync_interval_minutes > 1440 ||
      form.smsbower_balance_warning_threshold < 0 ||
      form.smsbower_points_per_unit <= 0 ||
      form.smsbower_min_margin_percent < 0 ||
      form.smsbower_min_margin_percent >= 100
    ) {
      Toast.warning("请检查同步周期、余额阈值、换算率和最低毛利率。");
      return;
    }
    setSaving(true);
    try {
      await onBulkSave(buildSMSBowerSettingsUpdates(form, canSensitive));
      setForm((current) => ({ ...current, smsbower_api_key: "" }));
      await load();
    } finally {
      setSaving(false);
    }
  };

  const queueSync = async () => {
    setSyncing(true);
    try {
      await syncSMSBower();
      Toast.success("已提交上游余额、项目和价格同步任务。");
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Upstream sync failed."));
    } finally {
      setSyncing(false);
    }
  };

  const addRoute = async () => {
    const source = newRoute.source.trim().toLowerCase();
    const providerServiceCode = newRoute.providerServiceCode.trim();
    if (!newRoute.projectId || !source || source.length > 64 || !providerServiceCode || providerServiceCode.length > 64) {
      Toast.warning("请选择系统项目，并填写不超过 64 个字符的渠道标识和上游项目代码。");
      return;
    }
    if (routeRows.some((row) => row.projectId === newRoute.projectId && row.source === source)) {
      Toast.warning("该项目已存在此渠道，请直接编辑对应行。");
      return;
    }
    const key = `new:${newRoute.projectId}:${source}`;
    setSavingRoute(key);
    try {
      await saveGmailUpstreamMapping(newRoute.projectId, {
        codeEnabled: false,
        enabled: false,
        providerServiceCode,
        purchaseEnabled: false,
        source,
      });
      setNewRoute((current) => ({ ...current, providerServiceCode: "", source: "" }));
      Toast.success("第三方 Gmail 渠道已添加。");
      await load();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Mapping save failed."));
    } finally {
      setSavingRoute(null);
    }
  };

  const saveRoute = async (row: RouteRow) => {
    const key = routeKey(row.projectId, row.source);
    const draft = drafts[key];
    if (!draft || (draft.enabled && !draft.codeEnabled && !draft.purchaseEnabled)) {
      Toast.warning("启用渠道时至少选择参与接码或参与购买。");
      return;
    }
    if (draft.source !== "local" && !draft.providerServiceCode.trim()) {
      Toast.warning("请选择或填写上游项目代码。");
      return;
    }
    setSavingRoute(key);
    try {
      await saveGmailUpstreamMapping(row.projectId, draft);
      Toast.success("Gmail 渠道路由已保存。");
      await load();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Mapping save failed."));
    } finally {
      setSavingRoute(null);
    }
  };

  const removeRoute = async (row: RouteRow) => {
    const key = routeKey(row.projectId, row.source);
    setSavingRoute(key);
    try {
      await deleteGmailUpstreamMapping(row.projectId, row.source);
      Toast.success("Gmail 渠道路由已删除。");
      await load();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Mapping delete failed."));
    } finally {
      setSavingRoute(null);
    }
  };

  const statusColor = status?.healthStatus === "healthy"
    ? "green"
    : status?.healthStatus === "disabled"
      ? "grey"
      : "red";

  return (
    <div className="space-y-6">
      <SettingsSection title={<SettingsCardHeader icon={<Cloud size={16} />} title="SMSBower 上游" description="固定连接官方 API；监控余额、服务目录、库存、价格和同步健康状态" />}>
        <Spin spinning={loading}>
          <div className="mb-5 grid gap-3 sm:grid-cols-2 xl:grid-cols-5">
            <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><Text type="tertiary">账户余额</Text><div className="mt-1 text-xl font-semibold">{formatPoints(status?.balance)} <small>上游单位</small></div></div>
            <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><Text type="tertiary">健康状态</Text><div className="mt-2"><Tag color={statusColor} shape="circle">{status?.healthStatus ?? "-"}</Tag></div></div>
            <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><Text type="tertiary">API Key</Text><div className="mt-1 font-medium">{status?.configured ? "已配置（不回显）" : "未配置"}</div></div>
            <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><Text type="tertiary">最近成功同步</Text><div className="mt-1 font-medium">{formatTime(status?.lastSuccessAt)}</div></div>
            <div className="rounded-lg bg-[var(--semi-color-fill-0)] p-3"><Text type="tertiary">连续失败</Text><div className="mt-1 text-xl font-semibold">{status?.consecutiveFailures ?? 0}</div></div>
          </div>
          {status?.lastSafeError ? <div className="mb-4 rounded-lg bg-[var(--semi-color-danger-light-default)] px-3 py-2 text-sm text-[var(--semi-color-danger)]">{status.lastSafeError}</div> : null}
          <SettingsAccessBoundary canWrite={canWrite}>
            <SettingsSwitchField checked={form.smsbower_enabled} onChange={(value) => setForm((current) => ({ ...current, smsbower_enabled: value }))} label="启用 SMSBower" description="仅控制是否参与库存统计和新订单分配；同步、余额、价格及健康监控不受影响" />
            <SettingsFormGrid className="mt-4">
              <SettingsNumberField label="同步间隔（分钟）" value={form.smsbower_sync_interval_minutes} onChange={(value) => setForm((current) => ({ ...current, smsbower_sync_interval_minutes: value }))} min={1} max={1440} precision={0} />
              <SettingsNumberField label="余额预警阈值" value={form.smsbower_balance_warning_threshold} onChange={(value) => setForm((current) => ({ ...current, smsbower_balance_warning_threshold: value }))} min={0} precision={6} />
              <SettingsNumberField label="1 上游单位折合积分" value={form.smsbower_points_per_unit} onChange={(value) => setForm((current) => ({ ...current, smsbower_points_per_unit: value }))} min={0.000001} precision={6} />
              <SettingsNumberField label="最低毛利率（%）" value={form.smsbower_min_margin_percent} onChange={(value) => setForm((current) => ({ ...current, smsbower_min_margin_percent: value }))} min={0} max={99.999999} precision={6} />
              {canSensitive ? <SettingsTextField label="API Key" value={form.smsbower_api_key} onChange={(value) => setForm((current) => ({ ...current, smsbower_api_key: value }))} type="password" placeholder="已保存密钥不会回显；留空保持不变" /> : null}
            </SettingsFormGrid>
            <Button className="mt-5" icon={<Save size={14} />} loading={saving} onClick={() => void saveSettings().catch(() => undefined)} theme="solid" type="primary">保存上游设置</Button>
          </SettingsAccessBoundary>
          <div className="mt-5 flex flex-wrap gap-2">
            <Button disabled={!canWrite} icon={<RefreshCw size={14} />} loading={syncing} onClick={() => void queueSync()} theme="light" type="primary">立即同步</Button>
            <Button icon={<RefreshCw size={14} />} onClick={() => void load()}>刷新状态</Button>
          </div>
        </Spin>
      </SettingsSection>

      <SettingsSection title={<SettingsCardHeader icon={<Link2 size={16} />} title="Gmail 商品与渠道映射" description="由管理员决定每个渠道是否启用及参与接码、购买；同一模式的多个可履约渠道会随机分配" />}>
        <Spin spinning={loading}>
          <div className="mb-4 grid gap-2 md:grid-cols-[minmax(180px,1fr)_minmax(180px,1fr)_minmax(220px,1.2fr)_auto]">
            <Select aria-label="系统项目" disabled={!canWrite} filter onChange={(value) => setNewRoute((current) => ({ ...current, projectId: Number(value ?? 0) }))} optionList={Array.from(new Map(mappings.map((item) => [item.projectId, { label: item.projectName, value: item.projectId }])).values())} placeholder="选择系统项目" value={newRoute.projectId || undefined} />
            <Input aria-label="第三方渠道标识" disabled={!canWrite} maxLength={64} onChange={(source) => setNewRoute((current) => ({ ...current, source }))} placeholder="渠道标识，如 provider-x" value={newRoute.source} />
            <Input aria-label="第三方上游项目代码" disabled={!canWrite} maxLength={64} onChange={(providerServiceCode) => setNewRoute((current) => ({ ...current, providerServiceCode }))} placeholder="该渠道的上游项目代码" value={newRoute.providerServiceCode} />
            <Button disabled={!canWrite || mappings.length === 0} loading={savingRoute?.startsWith("new:")} onClick={() => void addRoute()} theme="light" type="primary">新增渠道</Button>
          </div>
          <Table
            columns={[
              { title: "系统项目", dataIndex: "projectName", width: 180, render: (_value, row: RouteRow) => <div><div className="font-medium">{row.projectName}</div><Text type="tertiary" size="small">#{row.projectId}</Text></div> },
              { title: "渠道", dataIndex: "source", width: 150, render: (value: string) => <Tag color={value === "smsbower" ? "blue" : value === "local" ? "green" : "grey"} shape="circle">{value === "smsbower" ? "SMSBower" : value === "local" ? "自有 Gmail" : value}</Tag> },
              { title: "上游项目", width: 210, render: (_value, row: RouteRow) => {
                const draft = drafts[routeKey(row.projectId, row.source)];
                if (row.source === "local") return <Text type="tertiary">本地 Gmail 资源池</Text>;
                if (row.source === "smsbower") return <Select disabled={!canWrite} filter optionList={services.map((item) => ({ label: `${item.name} (${item.code}) · ${formatPoints(item.gmailPrice)} / 库存 ${item.gmailStock}`, value: item.code }))} onChange={(value) => updateDraft(row.projectId, row.source, { providerServiceCode: String(value ?? "") })} style={{ width: "100%" }} value={draft?.providerServiceCode} />;
                return <Input aria-label={`${row.source} 上游项目代码`} disabled={!canWrite} maxLength={64} onChange={(providerServiceCode) => updateDraft(row.projectId, row.source, { providerServiceCode })} value={draft?.providerServiceCode} />;
              } },
              { title: "参与模式", width: 220, render: (_value, row: RouteRow) => {
                const draft = drafts[routeKey(row.projectId, row.source)];
                return <div className="flex flex-wrap items-center gap-3"><Switch checked={draft?.enabled ?? false} disabled={!canWrite} onChange={(enabled) => updateDraft(row.projectId, row.source, { enabled })} size="small" /><Checkbox checked={draft?.codeEnabled ?? false} disabled={!canWrite} onChange={(event) => updateDraft(row.projectId, row.source, { codeEnabled: event.target.checked })}>接码</Checkbox><Checkbox checked={draft?.purchaseEnabled ?? false} disabled={!canWrite} onChange={(event) => updateDraft(row.projectId, row.source, { purchaseEnabled: event.target.checked })}>购买</Checkbox></div>;
              } },
              { title: "接码防亏", width: 160, render: (_value, row: RouteRow) => <SafetyTag safe={row.codeSafe} reason={row.codeUnsafeReason} rate={row.codeMarginRate} /> },
              { title: "购买防亏", width: 180, render: (_value, row: RouteRow) => <SafetyTag safe={row.purchaseSafe} reason={row.purchaseUnsafeReason} rate={row.purchaseMarginRate} /> },
              { title: "成本", width: 130, render: (_value, row: RouteRow) => <div><div>{formatPoints(row.costPoints)} 积分</div><Text type="tertiary" size="small">上游 {formatPoints(row.upstreamPrice)}</Text></div> },
              { title: "操作", fixed: "right" as const, width: 130, render: (_value, row: RouteRow) => {
                const key = routeKey(row.projectId, row.source);
                const exists = mappings.some((item) => item.projectId === row.projectId && item.source === row.source);
                return <div className="flex gap-1"><Button disabled={!canWrite} loading={savingRoute === key} onClick={() => void saveRoute(row)} size="small" theme="solid" type="primary">保存</Button>{exists ? <Button disabled={!canWrite} icon={<Trash2 size={13} />} onClick={() => void removeRoute(row)} size="small" theme="borderless" type="danger" /> : null}</div>;
              } },
            ]}
            dataSource={routeRows}
            empty={<div className="py-10 text-center text-[var(--semi-color-text-2)]">暂无系统项目</div>}
            pagination={false}
            rowKey={(row) => row ? routeKey(row.projectId, row.source) : ""}
            scroll={{ x: 1350 }}
          />
        </Spin>
      </SettingsSection>

      <SettingsSection title={<SettingsCardHeader icon={<Cloud size={16} />} title="SMSBower 服务目录" description="同步得到的可映射项目、Gmail 实时价格和库存；上游删除的项目会保留并标记为下线" />}>
        <Table
          columns={[
            { title: "服务", dataIndex: "name", render: (value, row: SMSBowerService) => <div><div className="font-medium">{String(value)}</div><Text type="tertiary" size="small">{row.code}</Text></div> },
            { title: "Gmail 价格", dataIndex: "gmailPrice", render: (value) => formatPoints(String(value)) },
            { title: "库存", dataIndex: "gmailStock" },
            { title: "上次价格", dataIndex: "previousPrice", render: (value) => value ? formatPoints(String(value)) : "-" },
            { title: "状态", dataIndex: "active", render: (value) => <Tag color={value ? "green" : "grey"} shape="circle">{value ? "可用" : "已下线"}</Tag> },
            { title: "最近发现", dataIndex: "lastSeenAt", render: (value) => formatTime(String(value)) },
          ]}
          dataSource={services}
          loading={loading}
          pagination={{ pageSize: 20 }}
          rowKey="code"
        />
      </SettingsSection>
    </div>
  );
}
