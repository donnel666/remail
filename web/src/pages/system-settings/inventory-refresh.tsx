import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Button, Select, Table, Tag, Toast, Tooltip, Typography } from "@douyinfe/semi-ui";
import { DatabaseZap, RefreshCw } from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  getInventoryRefreshes,
  triggerInventoryRefresh,
  type InventoryRefreshItem,
  type InventoryRefreshResponse,
} from "@/lib/inventory-refresh-api";
import { getIamErrorMessage } from "@/lib/iam-errors";
import { SettingsCardHeader, SettingsSection } from "./settings-layout";

const { Text } = Typography;

function formatTime(value: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString("zh-CN", { hour12: false });
}

const STATUS = {
  queued: { color: "blue" as const, label: "排队中" },
  running: { color: "amber" as const, label: "刷新中" },
  scheduled: { color: "green" as const, label: "已计划" },
  failed: { color: "red" as const, label: "失败" },
};

export default function InventoryRefreshSection({ canWrite }: { canWrite: boolean }) {
  const { t } = useTranslation();
  const [data, setData] = useState<InventoryRefreshResponse | null>(null);
  const [selectedProjectId, setSelectedProjectId] = useState<number>();
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState<number | "all" | null>(null);
  const requestSequence = useRef(0);

  const load = useCallback(async (signal?: AbortSignal, quiet = false) => {
    const sequence = ++requestSequence.current;
    if (!quiet) setLoading(true);
    try {
      const next = await getInventoryRefreshes(signal);
      if (!signal?.aborted && sequence === requestSequence.current) setData(next);
      return next;
    } catch (error) {
      if (!signal?.aborted && !quiet) Toast.error(getIamErrorMessage(t, error, "Inventory refresh state load failed."));
    } finally {
      if (!signal?.aborted && !quiet && sequence === requestSequence.current) setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  const active = data?.items.some((item) => item.status === "queued" || item.status === "running") ?? false;
  useEffect(() => {
    if (!active) return;
    const controller = new AbortController();
    let timer: number | undefined;
    const poll = async () => {
      const next = await load(controller.signal, true);
      if (!controller.signal.aborted && (!next || next.items.some((item) => item.status === "queued" || item.status === "running"))) {
        timer = window.setTimeout(poll, 2500);
      }
    };
    timer = window.setTimeout(poll, 2500);
    return () => {
      controller.abort();
      if (timer !== undefined) window.clearTimeout(timer);
    };
  }, [active, load]);

  const trigger = useCallback(async (projectId?: number) => {
    setRefreshing(projectId ?? "all");
    try {
      const projectIds = await triggerInventoryRefresh(projectId);
      Toast.success(projectId ? "项目库存刷新已提交。" : `已提交 ${projectIds.length} 个项目的库存刷新。`);
      await load(undefined, true);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Inventory refresh failed."));
    } finally {
      setRefreshing(null);
    }
  }, [load, t]);

  const projectOptions = useMemo(() => (data?.items ?? []).map((item) => ({
    label: `${item.projectName} (#${item.projectId})`, value: item.projectId,
  })), [data?.items]);

  return (
    <SettingsSection title={<SettingsCardHeader
      icon={<DatabaseZap size={16} />}
      title="库存刷新"
      description="按项目重建公共库存快照；状态来自现有 Redis 调度器"
    />}>
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <Select
          aria-label="库存刷新项目"
          filter
          onChange={(value) => setSelectedProjectId(value ? Number(value) : undefined)}
          optionList={projectOptions}
          placeholder="选择项目"
          showClear
          style={{ minWidth: 220, maxWidth: "100%" }}
          value={selectedProjectId}
        />
        <Button
          disabled={!canWrite || !selectedProjectId || refreshing !== null}
          icon={<RefreshCw size={14} />}
          loading={refreshing === selectedProjectId}
          onClick={() => void trigger(selectedProjectId)}
          theme="solid"
          type="primary"
        >刷新所选项目</Button>
        <Button
          disabled={!canWrite || refreshing !== null}
          icon={<RefreshCw size={14} />}
          loading={refreshing === "all"}
          onClick={() => void trigger()}
        >刷新全部</Button>
        <Tooltip content="重新加载状态">
          <Button
            aria-label="重新加载库存刷新状态"
            icon={<RefreshCw size={15} />}
            loading={loading}
            onClick={() => void load()}
          />
        </Tooltip>
      </div>
      <div className="mb-3 flex flex-wrap gap-x-5 gap-y-1 text-sm text-[var(--semi-color-text-2)]">
        <span>刷新周期：{data?.parameters.refreshIntervalMinutes ?? "-"} 分钟</span>
        <span>缓存 TTL：{data?.parameters.cacheHardTtlHours ?? "-"} 小时</span>
        <span>每批：{data?.parameters.batchSize ?? "-"} 个快照</span>
      </div>
      <Table
        columns={[
          { title: "项目", width: 220, render: (_value, row: InventoryRefreshItem) => <div><div className="font-medium">{row.projectName}</div><Text type="tertiary" size="small">#{row.projectId}</Text></div> },
          { title: "当前总库存", dataIndex: "totalAvailable", width: 140 },
          { title: "状态", width: 110, render: (_value, row: InventoryRefreshItem) => <Tag color={STATUS[row.status].color} shape="circle">{STATUS[row.status].label}</Tag> },
          { title: "上次更新", dataIndex: "lastRefreshedAt", width: 190, render: (value) => formatTime(value as string | null) },
          { title: "下次更新", dataIndex: "nextRefreshAt", width: 190, render: (value) => formatTime(value as string | null) },
          { title: "最近尝试", dataIndex: "lastAttemptAt", width: 190, render: (value) => formatTime(value as string | null) },
          { title: "错误", dataIndex: "lastError", width: 260, render: (value, row: InventoryRefreshItem) => row.status === "failed" ? <Tooltip content={String(value || "未知错误")}><span className="block max-w-[240px] truncate text-[var(--semi-color-danger)]">{String(value || "未知错误")}</span></Tooltip> : "-" },
          { title: "操作", fixed: "right" as const, width: 105, render: (_value, row: InventoryRefreshItem) => <Button disabled={!canWrite || refreshing !== null} icon={<RefreshCw size={13} />} loading={refreshing === row.projectId} onClick={() => void trigger(row.projectId)} size="small" theme="borderless">刷新</Button> },
        ]}
        dataSource={data?.items ?? []}
        empty={<div className="py-8 text-center text-[var(--semi-color-text-2)]">暂无可刷新的项目</div>}
        loading={loading}
        pagination={false}
        rowKey="projectId"
        scroll={{ x: 1405 }}
      />
    </SettingsSection>
  );
}
