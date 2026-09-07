import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  Empty,
  Input,
  Select,
  SideSheet,
  Space,
  Spin,
  Table,
  Tabs,
  Tag,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import type { TFunction } from "i18next";
import { useTranslation } from "react-i18next";

import { createCardProPagination } from "@/components/semi/card-pro-pagination";
import { createCopyableConfig } from "@/components/semi/copyable-config";
import {
  CopyableEllipsisText,
  mailExtractionLabelKey,
} from "@/components/semi/copyable-ellipsis-text";
import { CopyableTableText } from "@/components/semi/copyable-table-text";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import {
  fetchAdminProtoMail,
  getAdminProtoMessage,
  getAdminProtoTask,
  listAdminProtoMessages,
  listAdminProtoTasks,
  scanAdminProtoProjects,
  validateAdminProtoResource,
} from "@/lib/admin-proto-api";
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
  renderMessageStatusTag,
  renderStatusTag,
  renderTaskStatusTag,
  taskKindLabel,
} from "./proto-meta";
import type {
  AdminProtoAllocation,
  AdminProtoAllocationStatus,
  AdminProtoAsyncTask,
  AdminProtoAsyncTaskKind,
  AdminProtoAsyncTaskStatus,
  AdminProtoMailboxKind,
  AdminProtoMessageDetail,
  AdminProtoMessageCursor,
  AdminProtoMessageSummary,
  AdminProtoResourceDetail,
  AdminProtoSupplyScope,
  AdminProtoTaskListResponse,
} from "./admin-proto-types";
import { useAdminProtoAllocationPage } from "./use-admin-proto-allocation-page";

const { Text } = Typography;

function ResourceOverview({ detail, t }: { detail: AdminProtoResourceDetail; t: TFunction }) {
  return <div className="space-y-5">
    <section>
      <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">{t("Basic info")}</div>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        <InfoItem label={t("Resource ID")} value={<span className="font-mono">#{detail.id}</span>} />
        <InfoItem label={t("Email")} value={<CopyableTableText copiedText={t("Copied")} text={detail.emailAddress} />} />
        <InfoItem label={t("Suffix")} value={detail.suffix} />
        <InfoItem label={t("Owner")} value={<OwnerIdentity owner={detail.owner} ownerId={detail.ownerUserId} t={t} />} />
        <InfoItem label={t("Status")} value={renderStatusTag(detail.status, t, detail.lastSafeError)} />
        <InfoItem label={t("Private")} value={<Tag color={!detail.forSale ? "green" : "grey"} shape="circle" size="small">{t(detail.forSale ? "No" : "Yes")}</Tag>} />
        <InfoItem label={t("Long-lived")} value={<Tag color={detail.longLived ? "green" : "grey"} shape="circle" size="small">{t(detail.longLived ? "Yes" : "No")}</Tag>} />
        <InfoItem label={t("Quality score")} value={detail.qualityScore + "/100"} />
        <InfoItem label={t("Created at")} value={formatTime(detail.createdAt)} />
        <InfoItem label={t("Updated at")} value={formatTime(detail.updatedAt)} />
        <InfoItem label={t("Last allocated")} value={formatTime(detail.lastAllocatedAt)} />
        <InfoItem label={t("Version")} value={detail.version} />
      </div>
    </section>
    {detail.lastSafeError ? <section>
      <div className="mb-2 text-sm font-semibold text-[var(--semi-color-text-0)]">{t("Diagnostics")}</div>
      <div className="rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">{detail.lastSafeError}</div>
    </section> : null}
  </div>;
}
function CredentialDiagnostics({ detail, onReplace, t }: { detail: AdminProtoResourceDetail; onReplace: () => void; t: TFunction }) {
  return <section>
    <div className="mb-3 flex items-center justify-between gap-3">
      <div>
        <div className="text-sm font-semibold text-[var(--semi-color-text-0)]">{t("Credential configuration")}</div>
        <div className="mt-0.5 text-xs text-[var(--semi-color-text-2)]">{t("Only safe configuration flags are visible. Credential values are never returned.")}</div>
      </div>
      <Button disabled={detail.status === "deleted"} onClick={onReplace} size="small" type="primary">{t("Replace credentials")}</Button>
    </div>
    <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
      <InfoItem label={t("Password")} value={<ConfiguredTag configured={detail.credentials.passwordConfigured} t={t} />} />
      <InfoItem label={t("Credential revision")} value={detail.credentials.revision} />
      <InfoItem label={t("Credential updated at")} value={formatTime(detail.credentials.updatedAt)} />
      <InfoItem label={t("Validation generation")} value={detail.validationGeneration} />
      <InfoItem label={t("Validation failures")} value={detail.validationFailures} />
      <InfoItem label={t("Last checked at")} value={formatTime(detail.lastCheckedAt)} />
    </div>
  </section>;
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
  const pageState = useAdminProtoAllocationPage(resourceId);

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
          render: (_: unknown, record: AdminProtoAllocation) => (
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
          render: (value: unknown, record: AdminProtoAllocation) => (
            <Text
              className="font-mono-data"
              copyable={createCopyableConfig(String(value), t("Copied"))}
            >
              <span style={{ color: MAILBOX_TEXT_COLOR[record.mailbox as AdminProtoMailboxKind] }}>
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
            const meta = SUPPLY_SCOPE_META[value as AdminProtoSupplyScope];
            return <Tag color={meta.color} shape="circle" size="small">{t(meta.label)}</Tag>;
          },
        },
        {
          dataIndex: "serviceMode",
          title: t("Service mode"),
          width: 130,
          render: (_: unknown, record: AdminProtoAllocation) =>
            renderServiceModeTag(record.serviceMode, t),
        },
        {
          dataIndex: "orderStatus",
          title: t("Status"),
          width: 130,
          render: (_: unknown, record: AdminProtoAllocation) =>
            renderOrderStatusTag(record.orderStatus, t),
        },
        {
          dataIndex: "status",
          title: t("Allocated"),
          width: 110,
          render: (value: unknown) => {
            const meta = ALLOCATION_STATUS_META[value as AdminProtoAllocationStatus];
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
          render: (_: unknown, record: AdminProtoAllocation) =>
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
  detail: AdminProtoResourceDetail;
  onRefresh: () => void | Promise<void>;
  t: TFunction;
}) {
  const [busy, setBusy] = useState<TaskActionKey | null>(null);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [page, setPage] = useState(1);
  const [refreshKey, setRefreshKey] = useState(0);
  const [loading, setLoading] = useState(true);
  const [response, setResponse] = useState<AdminProtoTaskListResponse>({
    items: [],
    limit: pageSize,
    offset: 0,
    succeeded: 0,
    total: 0,
  });

  useEffect(() => setPage(1), [detail.id, pageSize]);
  useEffect(() => {
    const controller = new AbortController();
    let pollTimer: ReturnType<typeof globalThis.setTimeout> | null = null;
    setLoading(true);
    void listAdminProtoTasks(
      detail.id,
      (page - 1) * pageSize,
      pageSize,
      controller.signal
    )
      .then((next) => {
        if (controller.signal.aborted) return;
        const lastPage = Math.max(1, Math.ceil(next.total / pageSize));
        if (page > lastPage) {
          setPage(lastPage);
          return;
        }
        setResponse(next);
        if (
          next.items.some(
            (task) => task.status === "queued" || task.status === "running"
          )
        ) {
          pollTimer = globalThis.setTimeout(() => {
            setRefreshKey((value) => value + 1);
          }, 1_500);
        }
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          Toast.error(getIamErrorMessage(t, error, "Proto task load failed."));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => {
      controller.abort();
      if (pollTimer) globalThis.clearTimeout(pollTimer);
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
    setBusy(key);
    try {
      await action(detail.id);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Proto resource operation failed."));
      setBusy(null);
      return;
    }
    Toast.success(t(successKey));
    setPage(1);
    setRefreshKey((value) => value + 1);
    try {
      await onRefresh();
    } catch (error) {
      Toast.error(
        getIamErrorMessage(t, error, "Admin Proto resources load failed.")
      );
    } finally {
      setBusy(null);
    }
  };

  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "kind",
          title: t("Type"),
          width: 140,
          render: (value: unknown) => t(taskKindLabel(value as AdminProtoAsyncTaskKind)),
        },
        {
          dataIndex: "status",
          title: t("Status"),
          width: 110,
          render: (value: unknown) =>
            renderTaskStatusTag(value as AdminProtoAsyncTaskStatus, t),
        },
        {
          dataIndex: "remainingAttempts",
          title: t("Remaining attempts"),
          width: 120,
          render: (_: unknown, record: AdminProtoAsyncTask) => (
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
      action: (id) => validateAdminProtoResource(id, detail.version),
      successKey: "Resource validation submitted.",
    },
    {
      key: "history",
      label: "Scan projects",
      action: (id) => scanAdminProtoProjects(id, detail.version),
      successKey: "Project history scan submitted.",
    },
    {
      key: "fetch",
      label: "Mail fetch",
      action: fetchAdminProtoMail,
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

type MailSummary = AdminProtoMessageSummary;
type MailDetail = AdminProtoMessageDetail;

function mailboxOf(message: MailSummary): AdminProtoMailboxKind {
  return "mailbox" in message ? message.mailbox : "main";
}

export function ResourceMailsPanel({
  emptyDescription,
  extraOffset = 0,
  fetchEnabled = true,
  fetchDisabled = false,
  hideMailboxMeta = false,
  onRefresh,
  resourceId,
  t,
}: {
  emptyDescription?: string;
  extraOffset?: number;
  fetchEnabled?: boolean;
  fetchDisabled?: boolean;
  hideMailboxMeta?: boolean;
  onRefresh?: () => void | Promise<void>;
  resourceId: number;
  t: TFunction;
}) {
  const resourceType = "proto";
  const [search, setSearch] = useState("");
  const [debouncedSearch] = useDebouncedValue(search);
  const [pageSize] = useSharedPageSize();
  const [addressFilter, setAddressFilter] = useState("all");
  const [messages, setMessages] = useState<MailSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [cursor, setCursor] = useState<AdminProtoMessageCursor | null>(null);
  const [nextCursor, setNextCursor] =
    useState<AdminProtoMessageCursor | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [listLoading, setListLoading] = useState(true);
  const [listError, setListError] = useState<string | null>(null);
  const [retryKey, setRetryKey] = useState(0);
  const [fetchLoading, setFetchLoading] = useState(false);
  const fetchInFlightRef = useRef(false);
  const fetchPollAbortRef = useRef<AbortController | null>(null);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [selectedDetail, setSelectedDetail] = useState<MailDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [htmlPreviewKey, setHtmlPreviewKey] = useState("");
  const listScope = `${resourceType}\u0000${resourceId}\u0000${debouncedSearch}\u0000${pageSize}`;
  const listScopeRef = useRef(listScope);

  useEffect(() => {
    setMessages([]);
    setTotal(0);
    setCursor(null);
    setNextCursor(null);
    setHasMore(false);
    setListError(null);
    setHtmlPreviewKey("");
    setSelectedId(null);
    setSelectedDetail(null);
    setAddressFilter("all");
  }, [debouncedSearch, pageSize, resourceId, resourceType]);

  useEffect(
    () => () => {
      fetchPollAbortRef.current?.abort();
    },
    [resourceId]
  );

  useEffect(() => {
    if (listScopeRef.current !== listScope) {
      listScopeRef.current = listScope;
      if (cursor) return;
    }
    const controller = new AbortController();
    setListLoading(true);
    setListError(null);
    const request = listAdminProtoMessages(resourceId, debouncedSearch, pageSize, cursor ?? undefined, controller.signal);
    void request
      .then((response) => {
        if (controller.signal.aborted) return;
        if (response.total !== undefined) setTotal(response.total);
        const next =
          response.hasMore &&
          response.nextBeforeReceivedAt &&
          response.nextBeforeId
            ? {
                beforeReceivedAt: response.nextBeforeReceivedAt,
                beforeId: response.nextBeforeId,
              }
            : null;
        setNextCursor(next);
        setHasMore(next !== null);
        setMessages((current) => {
          const nextItems = cursor
            ? [...current, ...response.items]
            : response.items;
          return Array.from(
            new Map(nextItems.map((message) => [message.id, message])).values()
          );
        });
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          const message = getIamErrorMessage(
            t,
            error,
            "Proto mail load failed."
          );
          setListError(message);
          Toast.error(message);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setListLoading(false);
      });
    return () => controller.abort();
  }, [cursor, debouncedSearch, listScope, pageSize, resourceId, resourceType, retryKey, t]);

  const addresses = useMemo(() => {
    const map = new Map<string, AdminProtoMailboxKind>();
    for (const message of messages) {
      if (!map.has(message.recipient)) map.set(message.recipient, mailboxOf(message));
    }
    return Array.from(map.entries());
  }, [messages]);

  const filtered = useMemo(
    () =>
      addressFilter === "all"
        ? messages
        : messages.filter((message) => message.recipient === addressFilter),
    [addressFilter, messages]
  );

  useEffect(() => {
    const nextId =
      selectedId && filtered.some((message) => message.id === selectedId)
        ? selectedId
        : filtered[0]?.id ?? null;
    if (nextId === selectedId) return;
    setHtmlPreviewKey("");
    setSelectedId(nextId);
  }, [filtered, selectedId]);

  const selected = filtered.find((message) => message.id === selectedId) ?? null;
  const selectedMessageKey = selectedId
    ? `${resourceType}\0${resourceId}\0${selectedId}`
    : "";
  const previewHtml = htmlPreviewKey === selectedMessageKey;

  useEffect(() => {
    if (!selectedId) {
      setSelectedDetail(null);
      return;
    }
    const controller = new AbortController();
    setSelectedDetail(null);
    setDetailLoading(true);
    const request = getAdminProtoMessage(resourceId, selectedId, controller.signal);
    void request
      .then((message) => {
        if (!controller.signal.aborted) setSelectedDetail(message);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          Toast.error(
            getIamErrorMessage(
              t,
              error,
              "Proto mail detail load failed."
            )
          );
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setDetailLoading(false);
      });
    return () => controller.abort();
  }, [resourceId, resourceType, selectedId, t]);

  const loadMore = useCallback(
    (element: HTMLDivElement) => {
      if (listLoading || listError || !hasMore || !nextCursor) return;
      const remaining = element.scrollHeight - element.scrollTop - element.clientHeight;
      if (remaining < 80) setCursor(nextCursor);
    },
    [hasMore, listError, listLoading, nextCursor]
  );

  const fetchMail = async () => {
    if (fetchInFlightRef.current) return;
    fetchInFlightRef.current = true;
    setFetchLoading(true);
    const controller = new AbortController();
    fetchPollAbortRef.current?.abort();
    fetchPollAbortRef.current = controller;
    try {
      const accepted = await fetchAdminProtoMail(resourceId);
      Toast.success(t("Mail fetch submitted."));
      let task = accepted.task;
      let lastPollError: unknown = null;
      for (
        let attempt = 0;
        attempt < 20 && (task.status === "queued" || task.status === "running");
        attempt += 1
      ) {
        if (attempt > 0) {
          await new Promise((resolve) => globalThis.setTimeout(resolve, 1_500));
        }
        if (controller.signal.aborted) return;
        try {
          task = await getAdminProtoTask(task.taskId, controller.signal);
          lastPollError = null;
        } catch (error) {
          if (controller.signal.aborted) return;
          lastPollError = error;
        }
      }
      if (controller.signal.aborted) return;
      if (
        lastPollError &&
        (task.status === "queued" || task.status === "running")
      ) {
        Toast.error(
          getIamErrorMessage(
            t,
            lastPollError,
            "Proto task load failed."
          )
        );
      }
      if (["failed", "uncertain", "canceled"].includes(task.status)) {
        Toast.error(t("Fetch failed"));
      }
      setMessages([]);
      setCursor(null);
      setRetryKey((value) => value + 1);
      try {
        await onRefresh?.();
      } catch (error) {
        Toast.error(
          getIamErrorMessage(
            t,
            error,
            "Admin Proto resources load failed."
          )
        );
      }
    } catch (error) {
      if (!controller.signal.aborted) {
        Toast.error(getIamErrorMessage(t, error, "Fetch failed"));
      }
    } finally {
      if (fetchPollAbortRef.current === controller) {
        fetchPollAbortRef.current = null;
        fetchInFlightRef.current = false;
        setFetchLoading(false);
      }
    }
  };

  return (
    <div
      className="flex flex-col overflow-hidden rounded-xl border border-[var(--semi-color-border)] md:grid md:grid-cols-[320px_minmax(0,1fr)]"
      style={{
        height: extraOffset
          ? `calc(${DRAWER_PANEL_HEIGHT} - ${extraOffset}px)`
          : DRAWER_PANEL_HEIGHT,
      }}
    >
      <aside className="flex min-h-0 flex-col border-b border-[var(--semi-color-border)] md:border-b-0 md:border-r">
        <div className="space-y-2 border-b border-[var(--semi-color-border)] p-2.5">
          <Input
            onChange={(value) => setSearch(String(value))}
            placeholder={t("Search sender, recipient, subject or body")}
            prefix={<IconSearch />}
            showClear
            size="small"
            value={search}
          />
          <div className="flex items-center justify-between gap-2">
            <div className="flex items-center gap-1 text-xs text-[var(--semi-color-text-2)]">
              <span>{t("Mail count")}</span>
              <span className="font-mono tabular-nums">{total}</span>
            </div>
            {!fetchEnabled ? null : (
              <Button
                disabled={fetchDisabled || fetchLoading}
                loading={fetchLoading}
                onClick={() => void fetchMail()}
                size="small"
                type="primary"
              >
                {t("Fetch mail")}
              </Button>
            )}
          </div>
          {hideMailboxMeta ? null : (
            <Select
              onChange={(value) => setAddressFilter(String(value))}
              size="small"
              style={{ width: "100%" }}
              value={addressFilter}
            >
              <Select.Option value="all">{t("All")}</Select.Option>
              {addresses.map(([address, mailbox]) => (
                <Select.Option key={address} value={address}>
                  {`${t(MAILBOX_META[mailbox].label)} · ${address}`}
                </Select.Option>
              ))}
            </Select>
          )}
        </div>
        <div
          className="min-h-0 flex-1 overflow-auto"
          data-testid="admin-proto-message-list"
          onScroll={(event) => loadMore(event.currentTarget)}
        >
          {listError && messages.length === 0 ? (
            <div className="flex flex-col items-center gap-2 p-6 text-center text-sm text-[var(--semi-color-text-2)]">
              <span>{listError}</span>
              <Button onClick={() => setRetryKey((value) => value + 1)} size="small">
                {t("Try again")}
              </Button>
            </div>
          ) : listLoading && messages.length === 0 ? (
            <div className="flex justify-center p-6"><Spin /></div>
          ) : filtered.length === 0 ? (
            <Empty
              description={
                debouncedSearch.trim()
                  ? t("No matched mail")
                  : emptyDescription ?? t("No mails yet")
              }
              style={{ padding: 24 }}
            />
          ) : (
            filtered.map((message) => (
              <button
                aria-pressed={selected?.id === message.id}
                className={`block w-full border-b border-[var(--semi-color-border)] px-3 py-2.5 text-left transition-colors ${
                  selected?.id === message.id
                    ? "bg-[var(--semi-color-primary-light-default)]"
                    : "hover:bg-[var(--semi-color-fill-0)]"
                }`}
                key={message.id}
                onClick={() => {
                  if (message.id !== selectedId) setHtmlPreviewKey("");
                  setSelectedId(message.id);
                }}
                type="button"
              >
                <span className="flex items-center justify-between gap-2">
                  <span className="min-w-0 flex-1 truncate text-sm font-medium text-[var(--semi-color-text-0)]">
                    {message.subject}
                  </span>
                  {message.verificationCode ? (
                    <span
                      className="min-w-0 max-w-[45%] shrink-0 truncate font-mono text-xs font-semibold text-[var(--semi-color-success)]"
                      title={message.verificationCode}
                    >
                      {message.verificationCode}
                    </span>
                  ) : (
                    <span className="shrink-0">{renderMessageStatusTag(message.status, t)}</span>
                  )}
                </span>
                <span className="mt-1 flex min-w-0 items-center gap-1.5">
                  {hideMailboxMeta ? null : renderMailboxTag(mailboxOf(message), t)}
                  <span className="min-w-0 flex-1 truncate text-xs text-[var(--semi-color-text-2)]">
                    {message.recipient}
                  </span>
                </span>
                <span className="mt-1 flex items-center justify-between gap-2 text-xs text-[var(--semi-color-text-2)]">
                  <span className="min-w-0 flex-1 truncate">{message.sender}</span>
                  <span className="shrink-0">{formatTime(message.receivedAt)}</span>
                </span>
              </button>
            ))
          )}
          {listError && messages.length > 0 ? (
            <div className="flex items-center justify-center gap-2 p-3 text-xs text-[var(--semi-color-text-2)]">
              <span>{listError}</span>
              <Button onClick={() => setRetryKey((value) => value + 1)} size="small">
                {t("Try again")}
              </Button>
            </div>
          ) : listLoading && messages.length > 0 ? (
            <div className="flex justify-center p-3"><Spin /></div>
          ) : null}
        </div>
      </aside>

      <main className="min-h-0 flex-1 overflow-auto p-4">
        {selected ? (
          <div className="space-y-3">
            <div>
              <div className="text-base font-semibold text-[var(--semi-color-text-0)]">
                {selected.subject}
              </div>
              <div className="mt-1 flex flex-wrap items-center gap-2 text-xs text-[var(--semi-color-text-2)]">
                {hideMailboxMeta ? null : renderMailboxTag(mailboxOf(selected), t)}
                {renderMessageStatusTag(selected.status, t)}
                <span>{selected.sender}</span>
                <span>·</span>
                <span>{formatTime(selected.receivedAt)}</span>
              </div>
            </div>
            <div className="grid gap-2 sm:grid-cols-2">
              <InfoItem
                label={t("Recipient")}
                value={<CopyableTableText copiedText={t("Copied")} text={selected.recipient} />}
              />
              <InfoItem
                label={t(
                  selected.verificationCode
                    ? mailExtractionLabelKey(selected.verificationCode)
                    : "Verification code"
                )}
                value={
                  selected.verificationCode ? (
                    <CopyableEllipsisText text={selected.verificationCode} />
                  ) : (
                    "-"
                  )
                }
              />
              {selected.orderNo ? (
                <InfoItem
                  label={t("Order No")}
                  value={<CopyableTableText copiedText={t("Copied")} text={selected.orderNo} />}
                />
              ) : null}
            </div>
            {selectedDetail?.matchDiagnostic ? (
              <div className="rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs text-[var(--semi-color-text-1)]">
                {t("Match diagnostic")}: {selectedDetail.matchDiagnostic}
              </div>
            ) : null}
            <div className="flex justify-end">
              <Button
                aria-pressed={previewHtml}
                onClick={() => setHtmlPreviewKey(previewHtml ? "" : selectedMessageKey)}
                size="small"
                theme="borderless"
              >
                {t(previewHtml ? "Show source" : "Preview HTML")}
              </Button>
            </div>
            {previewHtml ? (
              <iframe
                className="block min-h-64 w-full rounded-lg border border-[var(--semi-color-border)] bg-white"
                referrerPolicy="no-referrer"
                sandbox=""
                srcDoc={selectedDetail?.body ?? ""}
                style={{ height: "min(480px, calc(100vh - 420px))" }}
                title={t("HTML email preview")}
              />
            ) : (
              <div className="whitespace-pre-wrap break-words rounded-lg bg-[var(--semi-color-fill-0)] p-3 text-sm text-[var(--semi-color-text-0)]">
                {detailLoading ? <Spin /> : selectedDetail?.body ?? ""}
              </div>
            )}
          </div>
        ) : (
          <div className="flex h-full items-center justify-center">
            <Empty description={t("No selected mail")} />
          </div>
        )}
      </main>
    </div>
  );
}

export function ProtoDetailSheet({
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
  detail: AdminProtoResourceDetail | null;
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
          ? `${t("Proto resource detail")} #${detail.id}`
          : t("Proto resource detail")
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
              <Tabs.TabPane itemKey="tasks" tab={t("Task details")} />
              <Tabs.TabPane itemKey="mails" tab={t("Mailbox")} />
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
            {activeTab === "tasks" ? (
              <TaskDiagnostics detail={detail} onRefresh={onRefresh} t={t} />
            ) : null}
            {activeTab === "mails" ? (
              <ResourceMailsPanel
                fetchDisabled={detail.status === "deleted"}
                hideMailboxMeta
                onRefresh={onRefresh}
                resourceId={detail.id}
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
