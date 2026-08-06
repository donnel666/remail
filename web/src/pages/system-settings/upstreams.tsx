import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  Modal,
  Select,
  Spin,
  Switch,
  Table,
  Tag,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { Cloud, Link2, Pencil, Plus, RefreshCw, Save, Trash2 } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  deleteGmailUpstreamMapping,
  getSMSBowerConfig,
  getSMSBowerStatus,
  listGmailUpstreamMappings,
  listSMSBowerServices,
  saveGmailUpstreamMapping,
  syncSMSBower,
  updateSMSBowerConfig,
  type GmailUpstreamMapping,
  type SMSBowerAccountStatus,
  type SMSBowerConfig,
  type SMSBowerService,
  type SMSBowerStrategy,
} from "@/lib/gmail-upstream-api";
import { getIamErrorMessage } from "@/lib/iam-errors";
import type { ProjectItem } from "@/lib/projects-api";

import type { SectionProps } from "./index";
import { loadAllProjects } from "./upstream-settings-values";
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
const EMPTY_FORM = {
  apiKey: "",
  balanceWarningThreshold: 0,
  enabled: false,
  minMarginPercent: 10,
  pointsPerUnit: 1,
  strategy: "local_first" as SMSBowerStrategy,
  syncIntervalMinutes: 5,
  noCodeRefundTimeoutMinutes: 10,
};

type MappingDraft = {
  projectId: number;
  providerServiceCode: string;
  enabled: boolean;
};

const EMPTY_MAPPING_DRAFT: MappingDraft = {
  projectId: 0,
  providerServiceCode: "",
  enabled: true,
};

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

export default function UpstreamsSection({
  canSensitive,
  canWrite,
}: SectionProps) {
  const { t } = useTranslation();
  const [form, setForm] = useState(EMPTY_FORM);
  const [config, setConfig] = useState<SMSBowerConfig | null>(null);
  const [status, setStatus] = useState<SMSBowerAccountStatus | null>(null);
  const [services, setServices] = useState<SMSBowerService[]>([]);
  const [projects, setProjects] = useState<ProjectItem[]>([]);
  const [mappings, setMappings] = useState<GmailUpstreamMapping[]>([]);
  const [mappingDraft, setMappingDraft] = useState<MappingDraft>(EMPTY_MAPPING_DRAFT);
  const [editingProjectId, setEditingProjectId] = useState<number | null>(null);
  const [mappingModalOpen, setMappingModalOpen] = useState(false);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [savingMapping, setSavingMapping] = useState<string | null>(null);
  const loadRequestRef = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    loadRequestRef.current?.abort();
    const controller = new AbortController();
    loadRequestRef.current = controller;
    setLoading(true);
    try {
      const [nextConfig, nextStatus, nextServices, nextMappings, nextProjects] = await Promise.all([
        getSMSBowerConfig(controller.signal),
        getSMSBowerStatus(controller.signal),
        listSMSBowerServices(controller.signal),
        listGmailUpstreamMappings(controller.signal),
        loadAllProjects(),
      ]);
      if (controller.signal.aborted) return;
      setConfig(nextConfig);
      setForm({
        apiKey: "",
        balanceWarningThreshold: Number(nextConfig.balanceWarningThreshold),
        enabled: nextConfig.enabled,
        minMarginPercent: Number(nextConfig.minMarginRate) * 100,
        pointsPerUnit: Number(nextConfig.pointsPerUnit),
        strategy: nextConfig.strategy,
        syncIntervalMinutes: nextConfig.syncIntervalMinutes,
        noCodeRefundTimeoutMinutes: nextConfig.noCodeRefundTimeoutMinutes,
      });
      setStatus(nextStatus);
      setServices(nextServices);
      setMappings(nextMappings);
      setProjects(nextProjects);
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

  const mappedProjectIds = useMemo(
    () => new Set(mappings.map((item) => item.projectId)),
    [mappings],
  );
  const projectOptions = useMemo(
    () => projects
      .filter((project) => project.id === editingProjectId || !mappedProjectIds.has(project.id))
      .map((project) => ({ label: `${project.name} (#${project.id})`, value: project.id })),
    [editingProjectId, mappedProjectIds, projects],
  );
  const serviceOptions = useMemo(
    () => services.map((service) => ({
      disabled: !service.active && service.code !== mappingDraft.providerServiceCode,
      label: `${service.name} (${service.code}) · ${formatPoints(service.gmailPrice)} / 库存 ${service.gmailStock}${service.active ? "" : " · 已下线"}`,
      value: service.code,
    })),
    [mappingDraft.providerServiceCode, services],
  );

  const saveSettings = async () => {
    if (form.enabled && !config?.configured && !form.apiKey.trim()) {
      Toast.warning("首次启用 SMSBower 时必须填写 API Key。");
      return;
    }
    if (
      form.syncIntervalMinutes < 1 ||
      form.syncIntervalMinutes > 1440 ||
      form.noCodeRefundTimeoutMinutes < 1 ||
      form.noCodeRefundTimeoutMinutes > 25 ||
      form.balanceWarningThreshold < 0 ||
      form.pointsPerUnit <= 0 ||
      form.minMarginPercent < 0 ||
      form.minMarginPercent >= 100
    ) {
      Toast.warning("请检查同步周期、无首码退款时间、余额阈值、换算率和最低毛利率。");
      return;
    }
    setSaving(true);
    try {
      await updateSMSBowerConfig({
        enabled: form.enabled,
        strategy: form.strategy,
        syncIntervalMinutes: form.syncIntervalMinutes,
        noCodeRefundTimeoutMinutes: form.noCodeRefundTimeoutMinutes,
        balanceWarningThreshold: String(form.balanceWarningThreshold),
        pointsPerUnit: String(form.pointsPerUnit),
        minMarginRate: String(form.minMarginPercent / 100),
        ...(canSensitive && form.apiKey.trim() ? { apiKey: form.apiKey.trim() } : {}),
      });
      Toast.success("SMSBower 上游设置已保存。");
      await load();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Upstream settings save failed."));
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

  const openCreateMapping = () => {
    setEditingProjectId(null);
    setMappingDraft(EMPTY_MAPPING_DRAFT);
    setMappingModalOpen(true);
  };

  const openEditMapping = (mapping: GmailUpstreamMapping) => {
    setEditingProjectId(mapping.projectId);
    setMappingDraft({
      projectId: mapping.projectId,
      providerServiceCode: mapping.providerServiceCode ?? "",
      enabled: mapping.enabled,
    });
    setMappingModalOpen(true);
  };

  const saveMapping = async () => {
    const providerServiceCode = mappingDraft.providerServiceCode.trim();
    if (!mappingDraft.projectId || !providerServiceCode) {
      Toast.warning("请选择系统项目和 SMSBower 服务。");
      return;
    }
    if (editingProjectId === null && mappedProjectIds.has(mappingDraft.projectId)) {
      Toast.warning("该系统项目已经建立 SMSBower 映射。");
      return;
    }
    const key = `save:${mappingDraft.projectId}`;
    setSavingMapping(key);
    try {
      await saveGmailUpstreamMapping(mappingDraft.projectId, { providerServiceCode, enabled: mappingDraft.enabled });
      setMappingModalOpen(false);
      Toast.success(editingProjectId === null ? "SMSBower 项目映射已创建。" : "SMSBower 项目映射已更新。");
      await load();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Mapping save failed."));
    } finally {
      setSavingMapping(null);
    }
  };

  const toggleMapping = async (mapping: GmailUpstreamMapping, enabled: boolean) => {
    const key = `toggle:${mapping.projectId}`;
    setSavingMapping(key);
    try {
      await saveGmailUpstreamMapping(mapping.projectId, {
        providerServiceCode: mapping.providerServiceCode,
        enabled,
      });
      setMappings((current) => current.map((item) => (
        item.projectId === mapping.projectId ? { ...item, enabled } : item
      )));
      Toast.success(enabled ? "SMSBower 项目映射已启用。" : "SMSBower 项目映射已禁用。");
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Mapping save failed."));
    } finally {
      setSavingMapping(null);
    }
  };

  const removeMapping = async (mapping: GmailUpstreamMapping) => {
    const key = `delete:${mapping.projectId}`;
    setSavingMapping(key);
    try {
      await deleteGmailUpstreamMapping(mapping.projectId);
      Toast.success("SMSBower 项目映射已删除。");
      await load();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Mapping delete failed."));
      throw error;
    } finally {
      setSavingMapping(null);
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
            <div className="space-y-3">
              <SettingsSwitchField checked={form.enabled} onChange={(value) => setForm((current) => ({ ...current, enabled: value }))} label="启用 SMSBower" description="启用后，已映射服务参与 Gmail 接码库存和订单路由；SMSBower 不参与完整邮箱购买" />
            </div>
            <SettingsFormGrid className="mt-4">
              <div>
                <div className="mb-2 text-sm font-medium">履约优先策略</div>
                <Select
                  aria-label="SMSBower 履约优先策略"
                  onChange={(value) => setForm((current) => ({ ...current, strategy: String(value) as SMSBowerStrategy }))}
                  optionList={[
                    { label: "本地优先：本地 Gmail 无库存后使用 SMSBower", value: "local_first" },
                    { label: "上游优先：SMSBower 无库存后使用本地 Gmail", value: "upstream_first" },
                  ]}
                  style={{ width: "100%" }}
                  value={form.strategy}
                />
              </div>
              <SettingsNumberField label="同步间隔（分钟）" value={form.syncIntervalMinutes} onChange={(value) => setForm((current) => ({ ...current, syncIntervalMinutes: value }))} min={1} max={1440} precision={0} />
              <SettingsNumberField label="无首码自动退款（分钟）" description="超过该时间仍未收到首个验证码时，先取消上游订单，再给用户退款。默认 10 分钟。" value={form.noCodeRefundTimeoutMinutes} onChange={(value) => setForm((current) => ({ ...current, noCodeRefundTimeoutMinutes: value }))} min={1} max={25} precision={0} />
              <SettingsNumberField label="余额预警阈值" value={form.balanceWarningThreshold} onChange={(value) => setForm((current) => ({ ...current, balanceWarningThreshold: value }))} min={0} precision={6} />
              <SettingsNumberField label="1 上游单位折合积分" value={form.pointsPerUnit} onChange={(value) => setForm((current) => ({ ...current, pointsPerUnit: value }))} min={0.000001} precision={6} />
              <SettingsNumberField label="最低毛利率（%）" value={form.minMarginPercent} onChange={(value) => setForm((current) => ({ ...current, minMarginPercent: value }))} min={0} max={99.999999} precision={6} />
              {canSensitive ? <SettingsTextField label="API Key" value={form.apiKey} onChange={(value) => setForm((current) => ({ ...current, apiKey: value }))} type="password" placeholder="已保存密钥不会回显；留空保持不变" /> : null}
            </SettingsFormGrid>
            <Button className="mt-5" icon={<Save size={14} />} loading={saving} onClick={() => void saveSettings().catch(() => undefined)} theme="solid" type="primary">保存上游设置</Button>
          </SettingsAccessBoundary>
          <div className="mt-5 flex flex-wrap gap-2">
            <Button disabled={!canWrite} icon={<RefreshCw size={14} />} loading={syncing} onClick={() => void queueSync()} theme="light" type="primary">立即同步</Button>
            <Button icon={<RefreshCw size={14} />} onClick={() => void load()}>刷新状态</Button>
          </div>
        </Spin>
      </SettingsSection>

      <SettingsSection title={<SettingsCardHeader icon={<Link2 size={16} />} title="SMSBower 项目映射" description="建立系统项目与 SMSBower 服务的对应关系；仅 Gmail 接码商品参与库存和履约" />}>
        <Spin spinning={loading}>
          <div className="mb-4 flex justify-end">
            <Button disabled={!canWrite} icon={<Plus size={14} />} onClick={openCreateMapping} theme="solid" type="primary">新建映射</Button>
          </div>
          <Table
            columns={[
              { title: "系统项目", dataIndex: "projectName", width: 200, render: (_value, row: GmailUpstreamMapping) => <div><div className="font-medium">{row.projectName}</div><Text type="tertiary" size="small">#{row.projectId}</Text></div> },
              { title: "SMSBower 服务", width: 250, render: (_value, row: GmailUpstreamMapping) => <div><div className="font-medium">{row.providerServiceName || services.find((service) => service.code === row.providerServiceCode)?.name || "-"}</div><Text type="tertiary" size="small">{row.providerServiceCode || "-"}</Text></div> },
              { title: "上游成本", width: 170, render: (_value, row: GmailUpstreamMapping) => <div><div>{formatPoints(row.costPoints)} 积分</div><Text type="tertiary" size="small">{formatPoints(row.upstreamPrice)} 上游单位</Text></div> },
              { title: "系统出售价格", width: 190, render: (_value, row: GmailUpstreamMapping) => <div><div>接码 {formatPoints(row.codePrice)} 积分</div><Text type="tertiary" size="small">购买 {formatPoints(row.purchasePrice)} 积分</Text></div> },
              { title: "状态", width: 110, render: (_value, row: GmailUpstreamMapping) => <Switch aria-label={`${row.projectName} SMSBower 映射`} checked={row.enabled} disabled={!canWrite} loading={savingMapping === `toggle:${row.projectId}`} onChange={(enabled) => void toggleMapping(row, enabled)} /> },
              { title: "操作", fixed: "right" as const, width: 130, render: (_value, row: GmailUpstreamMapping) => <div className="flex gap-1"><Button disabled={!canWrite} icon={<Pencil size={13} />} onClick={() => openEditMapping(row)} size="small" theme="borderless">编辑</Button><Button disabled={!canWrite} icon={<Trash2 size={13} />} loading={savingMapping === `delete:${row.projectId}`} onClick={() => Modal.confirm({ title: "删除 SMSBower 项目映射", content: `确认删除“${row.projectName}”的 SMSBower 映射吗？`, okButtonProps: { type: "danger" }, onOk: () => removeMapping(row) })} size="small" theme="borderless" type="danger" /></div> },
            ]}
            dataSource={mappings}
            empty={<div className="py-10 text-center text-[var(--semi-color-text-2)]">暂无 SMSBower 项目映射</div>}
            pagination={false}
            rowKey="projectId"
            scroll={{ x: 1050 }}
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

      <Modal
        cancelText="取消"
        confirmLoading={savingMapping?.startsWith("save:")}
        okText="保存"
        onCancel={() => setMappingModalOpen(false)}
        onOk={() => void saveMapping()}
        title={editingProjectId === null ? "新建 SMSBower 项目映射" : "编辑 SMSBower 项目映射"}
        visible={mappingModalOpen}
        width={520}
      >
        <div className="space-y-4 py-2">
          <div>
            <div className="mb-2 text-sm font-medium">系统项目</div>
            <Select
              aria-label="系统项目"
              disabled={!canWrite || editingProjectId !== null}
              emptyContent="暂无可映射的系统项目"
              filter
              onChange={(value) => setMappingDraft((current) => ({ ...current, projectId: Number(value ?? 0) }))}
              optionList={projectOptions}
              placeholder="选择系统项目"
              style={{ width: "100%" }}
              value={mappingDraft.projectId || undefined}
            />
          </div>
          <div>
            <div className="mb-2 text-sm font-medium">SMSBower 服务</div>
            <Select
              aria-label="SMSBower 服务"
              disabled={!canWrite}
              emptyContent="暂无已同步的 SMSBower 服务"
              filter
              onChange={(value) => setMappingDraft((current) => ({ ...current, providerServiceCode: String(value ?? "") }))}
              optionList={serviceOptions}
              placeholder="选择 SMSBower 服务"
              style={{ width: "100%" }}
              value={mappingDraft.providerServiceCode || undefined}
            />
          </div>
        </div>
      </Modal>
    </div>
  );
}
