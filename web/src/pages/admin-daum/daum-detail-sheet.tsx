import { useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  Empty,
  SideSheet,
  Space,
  Spin,
  Table,
  Tabs,
  Tag,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";

import { createCopyableConfig } from "@/components/semi/copyable-config";
import { CopyableEllipsisText } from "@/components/semi/copyable-ellipsis-text";
import { createCardProPagination } from "@/components/semi/card-pro-pagination";
import { CopyableTableText } from "@/components/semi/copyable-table-text";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import {
  fetchAdminDaumMail,
  listAdminDaumTasks,
  scanAdminDaumProjects,
  validateAdminDaumResource,
} from "@/lib/admin-daum-api";
import { IamApiError } from "@/lib/api-client";
import { getIamErrorMessage } from "@/lib/iam-errors";

import {
  formatLedgerAmount,
  renderOrderStatusTag,
  renderServiceModeTag,
} from "../orders/order-meta";
import { ProjectIcon } from "../workbench/project-icon";
import {
  ALLOCATION_STATUS_META,
  ConfiguredTag,
  DRAWER_PANEL_HEIGHT,
  DRAWER_TABLE_SCROLL_Y,
  InfoItem,
  MAILBOX_META,
  MAILBOX_TEXT_COLOR,
  OwnerIdentity,
  SUPPLY_SCOPE_META,
  formatTime,
  renderMailboxTag,
  renderStatusTag,
  renderTaskStatusTag,
  taskKindLabel,
} from "./daum-meta";
import type {
  AdminDaumAllocation,
  AdminDaumAllocationStatus,
  AdminDaumAsyncTask,
  AdminDaumAsyncTaskKind,
  AdminDaumAsyncTaskStatus,
  AdminDaumMailboxKind,
  AdminDaumResourceDetail,
  AdminDaumSupplyScope,
  AdminDaumTaskListResponse,
} from "./admin-daum-types";
import { ResourceMailsPanel, ResourceProxyDiagnostics } from "../admin-microsoft/microsoft-detail-sheet";
import { useAdminDaumAllocationPage } from "./use-admin-daum-allocation-page";

const { Text } = Typography;

function ResourceOverview({ detail, t }: { detail: AdminDaumResourceDetail; t: TFunction }) {
  return <div className="space-y-5">
    <section>
      <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">{t("Basic info")}</div>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <InfoItem label={t("Resource ID")} value={<span className="font-mono">#{detail.id}</span>} />
        <InfoItem label={t("Email")} value={<CopyableTableText copiedText={t("Copied")} text={detail.emailAddress} />} />
        <InfoItem label={t("Suffix")} value={detail.suffix} />
        <InfoItem label={t("Auxiliary pickup email")} value={detail.bindingAddress ? <CopyableTableText copiedText={t("Copied")} text={detail.bindingAddress} /> : <span className="text-[var(--semi-color-text-2)]">{t("Not configured")}</span>} />
        <InfoItem label={t("Kakao account email")} value={detail.kakaoEmail ? <CopyableTableText copiedText={t("Copied")} text={detail.kakaoEmail} /> : <span className="text-[var(--semi-color-text-2)]">{t("Not configured")}</span>} />
        <ResourceProxyDiagnostics bindings={detail.proxyBindings ?? []} t={t} />
        <InfoItem label={t("Owner")} value={<OwnerIdentity owner={detail.owner} ownerId={detail.ownerUserId} t={t} />} />
        <InfoItem label={t("Status")} value={renderStatusTag(detail.status, t, detail.lastSafeError)} />
        <InfoItem label={t("Private")} value={<Tag color={!detail.forSale ? "green" : "grey"} shape="circle" size="small">{t(detail.forSale ? "No" : "Yes")}</Tag>} />
        <InfoItem label={t("Long-lived")} value={<Tag color={detail.longLived ? "green" : "grey"} shape="circle" size="small">{t(detail.longLived ? "Yes" : "No")}</Tag>} />
        <InfoItem label={t("Mail protocol")} value={<Tag color="blue" shape="circle" size="small">{detail.protocol.toUpperCase()}</Tag>} />
        <InfoItem label={t("Quality score")} value={`${detail.qualityScore}/100`} />
        <InfoItem label={t("Created at")} value={formatTime(detail.createdAt)} />
        <InfoItem label={t("Updated at")} value={formatTime(detail.updatedAt)} />
        <InfoItem label={t("Last allocated")} value={formatTime(detail.lastAllocatedAt)} />
      </div>
    </section>
    <section>
      <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">{t("Operational summary")}</div>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <InfoItem label={t("Explicit aliases")} value={detail.aliases.length} />
        <InfoItem label={t("Latest task")} value={detail.activeTask ? renderTaskStatusTag(detail.activeTask.status, t) : <Tag color="grey" shape="circle" size="small">{t("Idle")}</Tag>} />
      </div>
    </section>
    {detail.lastSafeError ? <section>
      <div className="mb-2 text-sm font-semibold text-[var(--semi-color-text-0)]">{t("Diagnostics")}</div>
      <div className="rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">{detail.lastSafeError}</div>
    </section> : null}
  </div>;
}

function CredentialDiagnostics({ detail, onReplace, t }: { detail: AdminDaumResourceDetail; onReplace: () => void; t: TFunction }) {
  const bindingStatus = detail.bindingStatus === "linked" ? "Linked" : detail.bindingStatus === "conflict" ? "Binding conflict" : detail.bindingStatus === "unavailable" ? "Unavailable" : "Not linked";
  return <div className="space-y-5">
    <section>
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <div className="text-sm font-semibold text-[var(--semi-color-text-0)]">{t("Credential configuration")}</div>
          <div className="mt-0.5 text-xs text-[var(--semi-color-text-2)]">{t("Only safe configuration flags are visible. Credential values are never returned.")}</div>
        </div>
        <Button disabled={detail.status === "deleted"} onClick={onReplace} size="small" type="primary">{t("Replace credentials")}</Button>
      </div>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <InfoItem label={t("Login password")} value={<ConfiguredTag configured={detail.credentials.passwordConfigured} t={t} />} />
        <InfoItem label={t("APP password")} value={<ConfiguredTag configured={detail.credentials.appPasswordConfigured} t={t} />} />
        <InfoItem label={t("remail pickup URL")} value={<ConfiguredTag configured={detail.pickupUrlConfigured} t={t} />} />
        <InfoItem label={t("Credential revision")} value={detail.credentials.revision} />
        <InfoItem label={t("Credential updated at")} value={formatTime(detail.credentials.updatedAt)} />
        <InfoItem label={t("Binding status")} value={<Tag color={detail.bindingStatus === "linked" ? "green" : "grey"} shape="circle" size="small">{t(bindingStatus)}</Tag>} />
      </div>
      {detail.bindingLastSafeError ? <div className="mt-3 rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">{detail.bindingLastSafeError}</div> : null}
    </section>
  </div>;
}

export function ServerPaginatedDrawerTable({
  columns,
  dataSource,
  emptyDescription,
  extraOffset = 0,
  loading,
  onPageChange,
  onPageSizeChange,
  page,
  pageSize,
  rowKey = "id",
  scrollX,
  t,
  total,
}: {
  columns: any[];
  dataSource: any[];
  emptyDescription: string;
  extraOffset?: number;
  loading: boolean;
  onPageChange: (page: number) => void;
  onPageSizeChange: (pageSize: number) => void;
  page: number;
  pageSize: number;
  rowKey?: string;
  scrollX?: number;
  t: TFunction;
  total: number;
}) {
  const isMobile = useIsMobile();
  const panelHeight = extraOffset
    ? `calc(${DRAWER_PANEL_HEIGHT} - ${extraOffset}px)`
    : DRAWER_PANEL_HEIGHT;
  const tableScrollY = extraOffset
    ? `calc(${DRAWER_TABLE_SCROLL_Y} - ${extraOffset}px)`
    : DRAWER_TABLE_SCROLL_Y;

  return (
    <div className="flex flex-col" style={{ height: panelHeight }}>
      <div className="min-h-0 flex-1 overflow-hidden">
        {loading && dataSource.length === 0 ? (
          <div className="flex h-full items-center justify-center">
            <Spin size="large" />
          </div>
        ) : dataSource.length === 0 ? (
          <Empty description={emptyDescription} style={{ padding: 24 }} />
        ) : (
          <Table
            columns={columns}
            dataSource={dataSource}
            loading={loading}
            pagination={false}
            rowKey={rowKey}
            scroll={{ x: scrollX, y: tableScrollY }}
            size="small"
          />
        )}
      </div>
      {total > 0 ? (
        <div className="mt-3 flex flex-wrap items-center justify-end gap-3 border-t border-[var(--semi-color-border)] pt-3">
          {createCardProPagination({
            currentPage: page,
            isMobile,
            onPageChange,
            onPageSizeChange,
            pageSize,
            pageSizeOpts: [10, 20, 50, 100],
            showSizeChanger: true,
            t,
            total,
          })}
        </div>
      ) : null}
    </div>
  );
}

export function RelatedOrdersTable({
  resourceId,
  t,
}: {
  resourceId: number;
  t: TFunction;
}) {
  const pageState = useAdminDaumAllocationPage(resourceId);

  useEffect(() => {
    if (pageState.error) {
      Toast.error(getIamErrorMessage(t, pageState.error, "Related orders load failed."));
    }
  }, [pageState.error, t]);

  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "orderNo",
          title: t("Order No"),
          width: 150,
          render: (value: unknown) => (
            <CopyableTableText copiedText={t("Copied")} text={String(value)} />
          ),
        },
        {
          dataIndex: "projectName",
          title: t("Project"),
          width: 180,
          render: (_: unknown, record: AdminDaumAllocation) => (
            <div className="flex min-w-0 items-center gap-2">
              <ProjectIcon
                logoUrl={record.projectLogoUrl ?? undefined}
                name={record.projectName}
                size={18}
              />
              <span className="truncate text-sm text-[var(--semi-color-text-0)]">
                {record.projectName}
              </span>
            </div>
          ),
        },
        {
          dataIndex: "deliveryEmail",
          title: t("Delivery email"),
          width: 260,
          render: (value: unknown, record: AdminDaumAllocation) => (
            <Text
              className="font-mono-data"
              copyable={createCopyableConfig(String(value), t("Copied"))}
            >
              <span style={{ color: MAILBOX_TEXT_COLOR[record.mailbox as AdminDaumMailboxKind] }}>
                {String(value)}
              </span>
            </Text>
          ),
        },
        {
          dataIndex: "supplyScope",
          title: t("Supply scope"),
          width: 110,
          render: (value: unknown) => {
            const meta = SUPPLY_SCOPE_META[value as AdminDaumSupplyScope];
            return <Tag color={meta.color} shape="circle" size="small">{t(meta.label)}</Tag>;
          },
        },
        {
          dataIndex: "serviceMode",
          title: t("Service mode"),
          width: 130,
          render: (_: unknown, record: AdminDaumAllocation) =>
            renderServiceModeTag(record.serviceMode, t),
        },
        {
          dataIndex: "orderStatus",
          title: t("Status"),
          width: 130,
          render: (_: unknown, record: AdminDaumAllocation) =>
            renderOrderStatusTag(record.orderStatus, t),
        },
        {
          dataIndex: "status",
          title: t("Allocated"),
          width: 110,
          render: (value: unknown) => {
            const meta = ALLOCATION_STATUS_META[value as AdminDaumAllocationStatus];
            return <Tag color={meta.color} shape="circle" size="small">{t(meta.label)}</Tag>;
          },
        },
        {
          dataIndex: "buyerEmail",
          title: t("Buyer"),
          width: 210,
          render: (value: unknown) => (
            <CopyableTableText copiedText={t("Copied")} text={String(value)} />
          ),
        },
        {
          dataIndex: "payAmount",
          title: t("Pay amount"),
          width: 110,
          render: (value: unknown) => (
            <span className="whitespace-nowrap font-mono text-sm font-medium tabular-nums">
              {formatLedgerAmount(String(value))}
            </span>
          ),
        },
        {
          dataIndex: "verificationCode",
          title: t("Code / URL"),
          width: 130,
          render: (_: unknown, record: AdminDaumAllocation) =>
            record.verificationCode ? (
              <CopyableEllipsisText
                className="font-mono-data text-[var(--semi-color-success)]"
                text={record.verificationCode}
              />
            ) : record.orderStatus === "active" ? (
              <Tag color="grey" shape="circle" size="small">{t("Waiting")}</Tag>
            ) : (
              <span className="text-[var(--semi-color-text-3)]">-</span>
            ),
        },
        {
          dataIndex: "createdAt",
          title: t("Created at"),
          width: 170,
          render: (value: unknown) => formatTime(String(value)),
        },
        {
          dataIndex: "receiveUntil",
          title: t("Receive until"),
          width: 170,
          render: (value: unknown) => formatTime(value ? String(value) : undefined),
        },
      ] as any[],
    [t]
  );

  return (
    <ServerPaginatedDrawerTable
      columns={columns}
      dataSource={pageState.items}
      emptyDescription={t("No related orders")}
      loading={pageState.loading}
      onPageChange={pageState.setPage}
      onPageSizeChange={(size) => {
        pageState.setPageSize(size);
        pageState.setPage(1);
      }}
      page={pageState.page}
      pageSize={pageState.pageSize}
      scrollX={1720}
      t={t}
      total={pageState.total}
    />
  );
}

type TaskActionKey = "validate" | "history" | "fetch";

function TaskDiagnostics({
  detail,
  onRefresh,
  t,
}: {
  detail: AdminDaumResourceDetail;
  onRefresh: () => void | Promise<void>;
  t: TFunction;
}) {
  const [busy, setBusy] = useState<TaskActionKey | null>(null);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [page, setPage] = useState(1);
  const [refreshKey, setRefreshKey] = useState(0);
  const [loading, setLoading] = useState(true);
  const [response, setResponse] = useState<AdminDaumTaskListResponse>({
    items: [],
    limit: pageSize,
    offset: 0,
    succeeded: 0,
    total: 0,
  });

  const refreshRef = useRef(onRefresh);
  refreshRef.current = onRefresh;
  const actionScopeRef = useRef(new AbortController());
  useEffect(() => {
    const controller = new AbortController();
    actionScopeRef.current = controller;
    setBusy(null);
    return () => controller.abort();
  }, [detail.id]);

  useEffect(() => setPage(1), [detail.id, pageSize]);
  useEffect(() => {
    const controller = new AbortController();
    let pollTimer: ReturnType<typeof globalThis.setTimeout> | undefined;
    let activeTaskIds = new Set<string>();
    let firstRead = true;
    const poll = async () => {
      setLoading(true);
      try {
        const next = await listAdminDaumTasks(detail.id, (page - 1) * pageSize, pageSize, controller.signal);
        if (controller.signal.aborted) return;
        const lastPage = Math.max(1, Math.ceil(next.total / pageSize));
        if (page > lastPage) { setPage(lastPage); return; }
        const active = next.items.filter((task) => task.status === "queued" || task.status === "running");
        const nextActiveTaskIds = new Set(active.map((task) => task.taskId));
        const completed = [...activeTaskIds].some((id) => !nextActiveTaskIds.has(id));
        // A short task can finish before this panel ever observes it running.
        const initialTerminal = firstRead && active.length < next.items.length;
        firstRead = false;
        activeTaskIds = nextActiveTaskIds;
        setResponse(next);
        if (completed || initialTerminal) {
          try { await refreshRef.current(); }
          catch (error) {
            if (!controller.signal.aborted) Toast.error(getIamErrorMessage(t, error, "Admin Daum resources load failed."));
          }
        }
        if (!controller.signal.aborted && active.length > 0) pollTimer = globalThis.setTimeout(() => void poll(), 1_500);
      } catch (error) {
        if (!controller.signal.aborted) Toast.error(getIamErrorMessage(t, error, "Daum task load failed."));
      } finally {
        if (!controller.signal.aborted) setLoading(false);
      }
    };
    void poll();
    return () => {
      controller.abort();
      if (pollTimer !== undefined) globalThis.clearTimeout(pollTimer);
    };
  }, [detail.id, page, pageSize, refreshKey, t]);

  const total = response.total;
  const succeeded = response.succeeded;
  const successRate = total > 0 ? Math.round((succeeded / total) * 100) : 0;
  const deleted = detail.status === "deleted";

  const runAction = async (
    key: TaskActionKey,
    action: (id: number) => Promise<unknown>,
    successKey: string
  ) => {
    const controller = actionScopeRef.current;
    setBusy(key);
    try {
      await action(detail.id);
      if (controller.signal.aborted) return;
      Toast.success(t(successKey));
      setPage(1);
      setRefreshKey((value) => value + 1);
      try {
        await refreshRef.current();
      } catch (refreshError) {
        if (!controller.signal.aborted) Toast.error(getIamErrorMessage(t, refreshError, "Admin Daum resources load failed."));
      }
    } catch (error) {
      if (controller.signal.aborted) return;
      Toast.error(getIamErrorMessage(t, error, "Daum resource operation failed."));
      if (error instanceof IamApiError && error.status === 409) {
        try {
          await refreshRef.current();
          if (!controller.signal.aborted) setRefreshKey((value) => value + 1);
        } catch (refreshError) {
          if (!controller.signal.aborted) Toast.error(getIamErrorMessage(t, refreshError, "Admin Daum resources load failed."));
        }
      }
    } finally {
      if (!controller.signal.aborted) setBusy(null);
    }
  };

  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "kind",
          title: t("Type"),
          width: 140,
          render: (value: unknown) => t(taskKindLabel(value as AdminDaumAsyncTaskKind)),
        },
        {
          dataIndex: "status",
          title: t("Status"),
          width: 110,
          render: (value: unknown) =>
            renderTaskStatusTag(value as AdminDaumAsyncTaskStatus, t),
        },
        {
          dataIndex: "remainingAttempts",
          title: t("Remaining attempts"),
          width: 120,
          render: (_: unknown, record: AdminDaumAsyncTask) => (
            <span className="font-mono tabular-nums">{record.remainingAttempts}</span>
          ),
        },
        {
          dataIndex: "lastSafeError",
          title: t("Reason"),
          width: 240,
          render: (value: unknown) => value ? String(value) : "-",
        },
        {
          dataIndex: "queuedAt",
          title: t("Queued at"),
          width: 170,
          render: (value: unknown) => formatTime(value ? String(value) : undefined),
        },
        {
          dataIndex: "startedAt",
          title: t("Started at"),
          width: 170,
          render: (value: unknown) => formatTime(value ? String(value) : undefined),
        },
        {
          dataIndex: "finishedAt",
          title: t("Finished at"),
          width: 170,
          render: (value: unknown) => formatTime(value ? String(value) : undefined),
        },
        {
          dataIndex: "updatedAt",
          title: t("Updated at"),
          width: 170,
          render: (value: unknown) => formatTime(String(value)),
        },
      ] as any[],
    [t]
  );

  const actions: {
    key: TaskActionKey;
    label: string;
    action: (id: number) => Promise<unknown>;
    successKey: string;
  }[] = [
    {
      key: "validate",
      label: "Validate",
      action: validateAdminDaumResource,
      successKey: "Resource validation submitted.",
    },
    {
      key: "history",
      label: "Scan projects",
      action: scanAdminDaumProjects,
      successKey: "Project history scan submitted.",
    },
    {
      key: "fetch",
      label: "Mail fetch",
      action: fetchAdminDaumMail,
      successKey: "Mail fetch submitted.",
    },
  ];

  return (
    <div>
      <div className="mb-4">
        <div className="grid gap-3 sm:grid-cols-3">
          <InfoItem label={t("Total tasks")} value={<span className="font-mono tabular-nums">{total}</span>} />
          <InfoItem label={t("Succeeded tasks")} value={<span className="font-mono tabular-nums">{succeeded}</span>} />
          <InfoItem label={t("Success rate")} value={<span className="font-mono tabular-nums">{successRate}%</span>} />
        </div>
        <div className="mt-3 flex flex-wrap gap-2">
          {actions.map((item) => (
            <Button
              disabled={deleted || (item.key === "validate" && detail.status === "disabled") || (item.key === "history" && detail.status !== "normal" && detail.status !== "identifying") || (busy !== null && busy !== item.key)}
              key={item.key}
              loading={busy === item.key}
              onClick={() => void runAction(item.key, item.action, item.successKey)}
              size="small"
              type="tertiary"
            >
              {t(item.label)}
            </Button>
          ))}
        </div>
      </div>
      <ServerPaginatedDrawerTable
        columns={columns}
        dataSource={response.items}
        emptyDescription={t("No task records")}
        extraOffset={150}
        loading={loading}
        onPageChange={setPage}
        onPageSizeChange={(size) => {
          setPageSize(size);
          setPage(1);
        }}
        page={page}
        pageSize={pageSize}
        rowKey="taskId"
        scrollX={1050}
        t={t}
        total={response.total}
      />
    </div>
  );
}

function FixedAliasesPanel({ detail, t }: { detail: AdminDaumResourceDetail; t: TFunction }) {
  const [pageSize, setPageSize] = useSharedPageSize();
  const [page, setPage] = useState(1);
  const aliases = detail.aliases ?? [];
  return <ServerPaginatedDrawerTable
    columns={[
      { title: t("Explicit alias"), dataIndex: "email", render: (value: string) => <CopyableTableText copiedText={t("Copied")} text={value} /> },
      { title: t("Created at"), dataIndex: "createdAt", width: 220, render: (value: string) => formatTime(value) },
    ]}
    dataSource={aliases.slice((page - 1) * pageSize, page * pageSize)}
    emptyDescription={t("No aliases yet")}
    loading={false}
    onPageChange={setPage}
    onPageSizeChange={(size) => { setPageSize(size); setPage(1); }}
    page={page}
    pageSize={pageSize}
    t={t}
    total={aliases.length}
  />;
}

export function DaumDetailSheet({
  busy,
  detail,
  loading,
  onCancel,
  onDelete,
  onEdit,
  onRecover,
  onRefresh,
  onReplaceCredentials,
  onTogglePublish,
  onToggleDisabled,
  onMaintain,
}: {
  busy: boolean;
  detail: AdminDaumResourceDetail | null;
  loading: boolean;
  onCancel: () => void;
  onDelete: () => void;
  onEdit: () => void;
  onRecover: () => void;
  onRefresh: () => void | Promise<void>;
  onReplaceCredentials: () => void;
  onTogglePublish: () => void;
  onToggleDisabled: () => void;
  onMaintain: () => void;
}) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [activeTab, setActiveTab] = useState("basic");

  useEffect(() => {
    setActiveTab("basic");
  }, [detail?.id]);

  return (
    <SideSheet
      bodyStyle={{ padding: 0 }}
      onCancel={onCancel}
      placement="right"
      title={
        detail
          ? `${t("Daum resource detail")} #${detail.id}`
          : t("Daum resource detail")
      }
      visible={Boolean(detail) || loading}
      width={isMobile ? "100%" : 940}
    >
      {detail ? (
        <div className="flex min-h-full flex-col">
          <div className="sticky top-0 z-10 bg-[var(--semi-color-bg-2)] px-5 pt-2">
            <Tabs activeKey={activeTab} collapsible onChange={setActiveTab} type="line">
              <Tabs.TabPane itemKey="basic" tab={t("Basic info")} />
              <Tabs.TabPane itemKey="orders" tab={t("Orders")} />
              <Tabs.TabPane itemKey="explicit" tab={t("Explicit aliases")} />
              <Tabs.TabPane itemKey="tasks" tab={t("Task details")} />
              <Tabs.TabPane itemKey="mails" tab={t("Mailbox")} />
              <Tabs.TabPane itemKey="auxiliary" tab={t("Auxiliary mailbox")} />
            </Tabs>
          </div>

          <div className="flex-1 p-5">
            {activeTab === "basic" ? (
              <div className="space-y-6">
                <ResourceOverview detail={detail} t={t} />
                <CredentialDiagnostics detail={detail} onReplace={onReplaceCredentials} t={t} />
              </div>
            ) : null}
            {activeTab === "orders" ? <RelatedOrdersTable resourceId={detail.id} t={t} /> : null}
            {activeTab === "explicit" ? <FixedAliasesPanel detail={detail} t={t} /> : null}
            {activeTab === "auxiliary" ? (
              detail.bindingAddress ? (
                <>
                  <div className="mb-3 flex flex-wrap items-center gap-2 text-sm">
                    <span className="text-[var(--semi-color-text-2)]">{t("Auxiliary email")}</span>
                    <CopyableTableText copiedText={t("Copied")} text={detail.bindingAddress} />
                  </div>
                  <ResourceMailsPanel
                    key={`daum-auxiliary:${detail.id}:${detail.version}`}
                    auxiliary
                    emptyDescription={t("No auxiliary mail yet")}
                    extraOffset={40}
                    hideMailboxMeta
                    fetchEnabled={false}
                    resourceType="daum"
                    resourceId={detail.id}
                    t={t}
                  />
                </>
              ) : <Empty description={t("No auxiliary mailbox configured")} style={{ padding: 32 }} />
            ) : null}
            {activeTab === "tasks" ? (
              <TaskDiagnostics detail={detail} onRefresh={onRefresh} t={t} />
            ) : null}
            {activeTab === "mails" ? (
              <ResourceMailsPanel
                fetchDisabled={detail.status === "deleted"}
                hideMailboxMeta
                onRefresh={onRefresh}
                resourceId={detail.id}
                resourceType="daum"
                t={t}
              />
            ) : null}
          </div>

          <div className="sticky bottom-0 flex flex-wrap items-center justify-end gap-2 border-t border-[var(--semi-color-border)] bg-[var(--semi-color-bg-0)] px-5 py-3">
            {detail.status === "deleted" ? (
              <Button loading={busy} onClick={onRecover} type="primary">{t("Recover")}</Button>
            ) : (
              <>
                <Button disabled={busy} onClick={onMaintain} type="tertiary">{t("Maintenance")}</Button>
                <Button disabled={busy} onClick={onEdit} type="tertiary">{t("Edit")}</Button>
                <Button loading={busy} onClick={onTogglePublish} type="tertiary">
                  {detail.forSale ? t("Convert to private") : t("Put on sale")}
                </Button>
                <Button disabled={busy} onClick={onReplaceCredentials} type="tertiary">
                  {t("Replace credentials")}
                </Button>
                <Button loading={busy} onClick={onToggleDisabled} type="tertiary">
                  {detail.status === "disabled" ? t("Enable") : t("Disable")}
                </Button>
                <Button loading={busy} onClick={onDelete} type="danger">{t("Delete")}</Button>
              </>
            )}
          </div>
        </div>
      ) : (
        <div className="flex h-40 items-center justify-center"><Spin size="large" /></div>
      )}
    </SideSheet>
  );
}
