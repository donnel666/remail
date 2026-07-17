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
import { CopyableTableText } from "@/components/semi/copyable-table-text";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import {
  createAdminMicrosoftExplicitAlias,
  fetchAdminMicrosoftMail,
  getAdminMicrosoftBindingMessage,
  getAdminMicrosoftMessage,
  listAdminMicrosoftAliases,
  listAdminMicrosoftBindingMessages,
  listAdminMicrosoftMessages,
  listAdminMicrosoftTasks,
  refreshAdminMicrosoftToken,
  scanAdminMicrosoftProjects,
  validateAdminMicrosoftResource,
} from "@/lib/admin-microsoft-api";
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
  formatRemainingTime,
  formatTime,
  renderMailboxTag,
  renderMessageStatusTag,
  renderProtocolTag,
  renderStatusTag,
  renderTaskStatusTag,
  renderTokenTag,
  taskKindLabel,
} from "./microsoft-meta";
import type {
  AdminMicrosoftAliasListResponse,
  AdminMicrosoftAliasSample,
  AdminMicrosoftAllocation,
  AdminMicrosoftAllocationStatus,
  AdminMicrosoftAsyncTask,
  AdminMicrosoftAsyncTaskKind,
  AdminMicrosoftAsyncTaskStatus,
  AdminMicrosoftAuxiliaryMessageDetail,
  AdminMicrosoftAuxiliaryMessageSummary,
  AdminMicrosoftMailboxKind,
  AdminMicrosoftMessageDetail,
  AdminMicrosoftMessageCursor,
  AdminMicrosoftMessageSummary,
  AdminMicrosoftResourceDetail,
  AdminMicrosoftSupplyScope,
  AdminMicrosoftTaskListResponse,
} from "./admin-microsoft-types";
import { useAdminMicrosoftAllocationPage } from "./use-admin-microsoft-allocation-page";

const { Text } = Typography;

function ResourceOverview({
  detail,
  t,
}: {
  detail: AdminMicrosoftResourceDetail;
  t: TFunction;
}) {
  return (
    <div className="space-y-5">
      <section>
        <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">
          {t("Basic info")}
        </div>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <InfoItem label="ID" value={<span className="font-mono">#{detail.id}</span>} />
          <InfoItem
            label={t("Email")}
            value={<CopyableTableText copiedText={t("Copied")} text={detail.emailAddress} />}
          />
          <InfoItem label={t("Suffix")} value={detail.suffix} />
          <InfoItem
            label={t("Auxiliary email")}
            value={
              detail.bindingAddress ? (
                <CopyableTableText copiedText={t("Copied")} text={detail.bindingAddress} />
              ) : (
                <span className="text-[var(--semi-color-text-2)]">
                  {t("Not configured")}
                </span>
              )
            }
          />
          <InfoItem
            label={t("Resource proxy")}
            value={
              detail.proxyBindings.length > 0 ? (
                <div className="space-y-1 text-xs">
                  {detail.proxyBindings.map((binding) => (
                    <div key={`${binding.proxyId}-${binding.ipVersion}`}>
                      <span className="font-mono">#{binding.proxyId}</span>{" "}
                      {binding.host || "-"} · {binding.ipVersion} · {binding.status}
                      {binding.outboundIp ? ` · ${binding.outboundIp}` : ""}
                    </div>
                  ))}
                </div>
              ) : (
                <span className="text-[var(--semi-color-text-2)]">{t("Not configured")}</span>
              )
            }
          />
          <InfoItem
            label={t("Expires at")}
            value={
              detail.proxyBindings.length > 0 ? (
                <div className="space-y-1 text-xs">
                  {detail.proxyBindings.map((binding) => (
                    <div key={`${binding.proxyId}-${binding.ipVersion}`}>
                      {binding.ipVersion}: {formatTime(binding.expireAt)}
                    </div>
                  ))}
                </div>
              ) : (
                <span className="text-[var(--semi-color-text-2)]">-</span>
              )
            }
          />
          <InfoItem
            label={t("Owner")}
            value={<OwnerIdentity owner={detail.owner} t={t} />}
          />
          <InfoItem
            label={t("Status")}
            value={renderStatusTag(detail.status, t, detail.lastSafeError ?? undefined)}
          />
          <InfoItem
            label={t("Private")}
            value={
              <Tag color={!detail.forSale ? "green" : "grey"} shape="circle" size="small">
                {!detail.forSale ? t("Yes") : t("No")}
              </Tag>
            }
          />
          <InfoItem
            label={t("Long-lived")}
            value={
              <Tag color={detail.longLived ? "green" : "grey"} shape="circle" size="small">
                {detail.longLived ? t("Yes") : t("No")}
              </Tag>
            }
          />
          <InfoItem label={t("Mail protocol")} value={renderProtocolTag(detail, t)} />
          <InfoItem label={t("Quality score")} value={`${detail.qualityScore}/100`} />
          <InfoItem label={t("Created at")} value={formatTime(detail.createdAt)} />
          <InfoItem label={t("Updated at")} value={formatTime(detail.updatedAt)} />
          <InfoItem label={t("Last allocated")} value={formatTime(detail.lastAllocatedAt)} />
        </div>
      </section>

      <section>
        <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">
          {t("Operational summary")}
        </div>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <InfoItem
            label={t("Refresh token health")}
            value={renderTokenTag(detail.tokenHealth, t)}
          />
          <InfoItem
            label={t("Refresh token expires at")}
            value={formatTime(detail.rtExpireAt)}
          />
          <InfoItem
            label={t("Refresh token remaining")}
            value={formatRemainingTime(detail.rtExpireAt, t)}
          />
          <InfoItem
            label={t("Aliases")}
            value={`${detail.aliasCounts.explicit} / ${detail.aliasCounts.dot} / ${detail.aliasCounts.plus}`}
          />
          <InfoItem
            label={t("Latest task")}
            value={
              detail.activeTask ? (
                renderTaskStatusTag(detail.activeTask.status, t)
              ) : (
                <Tag color="grey" shape="circle" size="small">
                  {t("Idle")}
                </Tag>
              )
            }
          />
        </div>
      </section>

      {detail.lastSafeError ? (
        <section>
          <div className="mb-2 text-sm font-semibold text-[var(--semi-color-text-0)]">
            {t("Diagnostics")}
          </div>
          <div className="rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">
            {detail.lastSafeError}
          </div>
        </section>
      ) : null}
    </div>
  );
}

function CredentialDiagnostics({
  detail,
  onReplace,
  t,
}: {
  detail: AdminMicrosoftResourceDetail;
  onReplace: () => void;
  t: TFunction;
}) {
  return (
    <div className="space-y-5">
      <section>
        <div className="mb-3 flex items-center justify-between gap-3">
          <div>
            <div className="text-sm font-semibold text-[var(--semi-color-text-0)]">
              {t("Credential configuration")}
            </div>
            <div className="mt-0.5 text-xs text-[var(--semi-color-text-2)]">
              {t("Only safe configuration flags are visible. Credential values are never returned.")}
            </div>
          </div>
          <Button onClick={onReplace} size="small" type="primary">
            {t("Replace credentials")}
          </Button>
        </div>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <InfoItem
            label={t("Password")}
            value={<ConfiguredTag configured={detail.credentials.passwordConfigured} t={t} />}
          />
          <InfoItem
            label={t("OAuth client ID")}
            value={<ConfiguredTag configured={detail.credentials.clientIdConfigured} t={t} />}
          />
          <InfoItem
            label={t("Refresh token")}
            value={<ConfiguredTag configured={detail.credentials.refreshTokenConfigured} t={t} />}
          />
          <InfoItem label={t("Credential revision")} value={detail.credentials.revision} />
          <InfoItem
            label={t("Credential updated at")}
            value={formatTime(detail.credentials.updatedAt)}
          />
        </div>
      </section>

      <section>
        <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">
          {t("Token diagnostics")}
        </div>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          <InfoItem label={t("Health")} value={renderTokenTag(detail.token.health, t)} />
          <InfoItem
            label={t("Refresh token expires at")}
            value={formatTime(detail.token.rtExpireAt)}
          />
          <InfoItem
            label={t("Refresh token remaining")}
            value={formatRemainingTime(detail.token.rtExpireAt, t)}
          />
          <InfoItem
            label={t("Last refreshed at")}
            value={formatTime(detail.token.lastRefreshedAt)}
          />
          <InfoItem
            label={t("Last refresh request ID")}
            value={
              detail.token.lastRefreshRequestId ? (
                <span className="break-all font-mono text-xs">
                  {detail.token.lastRefreshRequestId}
                </span>
              ) : (
                "-"
              )
            }
          />
          <InfoItem
            label={t("OAuth scopes")}
            value={
              detail.token.scopes.length > 0 ? (
                <Space spacing={4} wrap>
                  {detail.token.scopes.map((scope) => (
                    <Tag color="blue" key={scope} size="small">
                      {scope}
                    </Tag>
                  ))}
                </Space>
              ) : (
                "-"
              )
            }
          />
        </div>
        {detail.token.lastSafeError ? (
          <div className="mt-3 rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">
            {detail.token.lastSafeError}
          </div>
        ) : null}
      </section>
    </div>
  );
}

function ServerPaginatedDrawerTable({
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

function AliasPanel({
  kind,
  resourceId,
  t,
}: {
  kind: "explicit" | "other";
  resourceId: number;
  t: TFunction;
}) {
  const [pageSize, setPageSize] = useSharedPageSize();
  const [page, setPage] = useState(1);
  const [response, setResponse] = useState<AdminMicrosoftAliasListResponse>({
    items: [],
    limit: pageSize,
    offset: 0,
    schedule: null,
    total: 0,
  });
  const [loading, setLoading] = useState(true);

  useEffect(() => setPage(1), [kind, pageSize, resourceId]);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    void listAdminMicrosoftAliases(
      resourceId,
      kind,
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
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          Toast.error(getIamErrorMessage(t, error, "Microsoft alias load failed."));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [kind, page, pageSize, resourceId, t]);

  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "emailAddress",
          title: kind === "explicit" ? t("Explicit alias") : t("Alias"),
          render: (value: unknown, record: AdminMicrosoftAliasSample) =>
            kind === "explicit" ? (
              <CopyableTableText copiedText={t("Copied")} text={String(value)} />
            ) : (
              <Text
                className="font-mono-data"
                copyable={createCopyableConfig(String(value), t("Copied"))}
              >
                <span
                  style={{
                    color: MAILBOX_TEXT_COLOR[record.kind === "dot" ? "dot" : "plus"],
                  }}
                >
                  {String(value)}
                </span>
              </Text>
            ),
        },
        {
          dataIndex: "createdAt",
          title: t("Created at"),
          width: 220,
          render: (value: unknown) => formatTime(String(value)),
        },
      ] as any[],
    [kind, t]
  );

  const schedule = response.schedule;
  const extraOffset = kind === "explicit" ? 88 : 0;
  return (
    <div>
      {kind === "explicit" ? (
        <div className="mb-4 grid gap-3 sm:grid-cols-3">
          <InfoItem
            label={t("Weekly quota")}
            value={schedule ? `${schedule.weekCreated}/${schedule.weekLimit}` : "-"}
          />
          <InfoItem
            label={t("Yearly quota")}
            value={schedule ? `${schedule.yearCreated}/${schedule.yearLimit}` : "-"}
          />
          <InfoItem
            label={t("Next run at")}
            value={formatTime(schedule?.nextRunAt)}
          />
        </div>
      ) : null}
      <ServerPaginatedDrawerTable
        columns={columns}
        dataSource={response.items}
        emptyDescription={t("No aliases yet")}
        extraOffset={extraOffset}
        loading={loading}
        onPageChange={setPage}
        onPageSizeChange={(size) => {
          setPageSize(size);
          setPage(1);
        }}
        page={page}
        pageSize={pageSize}
        t={t}
        total={response.total}
      />
    </div>
  );
}

function RelatedOrdersTable({ resourceId, t }: { resourceId: number; t: TFunction }) {
  const pageState = useAdminMicrosoftAllocationPage(resourceId);

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
          render: (_: unknown, record: AdminMicrosoftAllocation) => (
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
          render: (value: unknown, record: AdminMicrosoftAllocation) => (
            <Text
              className="font-mono-data"
              copyable={createCopyableConfig(String(value), t("Copied"))}
            >
              <span style={{ color: MAILBOX_TEXT_COLOR[record.mailbox as AdminMicrosoftMailboxKind] }}>
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
            const meta = SUPPLY_SCOPE_META[value as AdminMicrosoftSupplyScope];
            return <Tag color={meta.color} shape="circle" size="small">{t(meta.label)}</Tag>;
          },
        },
        {
          dataIndex: "serviceMode",
          title: t("Service mode"),
          width: 130,
          render: (_: unknown, record: AdminMicrosoftAllocation) =>
            renderServiceModeTag(record.serviceMode, t),
        },
        {
          dataIndex: "orderStatus",
          title: t("Status"),
          width: 130,
          render: (_: unknown, record: AdminMicrosoftAllocation) =>
            renderOrderStatusTag(record.orderStatus, t),
        },
        {
          dataIndex: "status",
          title: t("Allocated"),
          width: 110,
          render: (value: unknown) => {
            const meta = ALLOCATION_STATUS_META[value as AdminMicrosoftAllocationStatus];
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
          title: t("Verification code"),
          width: 130,
          render: (_: unknown, record: AdminMicrosoftAllocation) =>
            record.verificationCode ? (
              <Text
                className="font-mono-data"
                copyable={createCopyableConfig(record.verificationCode, t("Copied"))}
                style={{ color: "var(--semi-color-success)" }}
              >
                {record.verificationCode}
              </Text>
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

type TaskActionKey = "validate" | "token" | "alias" | "history" | "fetch";

function TaskDiagnostics({
  detail,
  onRefresh,
  t,
}: {
  detail: AdminMicrosoftResourceDetail;
  onRefresh: () => void | Promise<void>;
  t: TFunction;
}) {
  const [busy, setBusy] = useState<TaskActionKey | null>(null);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [page, setPage] = useState(1);
  const [refreshKey, setRefreshKey] = useState(0);
  const [loading, setLoading] = useState(true);
  const [response, setResponse] = useState<AdminMicrosoftTaskListResponse>({
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
    void listAdminMicrosoftTasks(
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
          Toast.error(getIamErrorMessage(t, error, "Microsoft task load failed."));
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
      Toast.error(getIamErrorMessage(t, error, "Microsoft resource operation failed."));
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
        getIamErrorMessage(t, error, "Admin Microsoft resources load failed.")
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
          render: (value: unknown) => t(taskKindLabel(value as AdminMicrosoftAsyncTaskKind)),
        },
        {
          dataIndex: "status",
          title: t("Status"),
          width: 110,
          render: (value: unknown) =>
            renderTaskStatusTag(value as AdminMicrosoftAsyncTaskStatus, t),
        },
        {
          dataIndex: "remainingAttempts",
          title: t("Remaining attempts"),
          width: 120,
          render: (_: unknown, record: AdminMicrosoftAsyncTask) => (
            <span className="font-mono tabular-nums">{record.remainingAttempts}</span>
          ),
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
      action: validateAdminMicrosoftResource,
      successKey: "Resource validation submitted.",
    },
    {
      key: "token",
      label: "Refresh RT",
      action: refreshAdminMicrosoftToken,
      successKey: "Token refresh submitted.",
    },
    {
      key: "alias",
      label: "Create explicit alias",
      action: createAdminMicrosoftExplicitAlias,
      successKey: "Explicit alias creation submitted.",
    },
    {
      key: "history",
      label: "Scan projects",
      action: scanAdminMicrosoftProjects,
      successKey: "Project history scan submitted.",
    },
    {
      key: "fetch",
      label: "Mail fetch",
      action: fetchAdminMicrosoftMail,
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
              disabled={deleted || (busy !== null && busy !== item.key)}
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

type MailSummary =
  | AdminMicrosoftMessageSummary
  | AdminMicrosoftAuxiliaryMessageSummary;
type MailDetail =
  | AdminMicrosoftMessageDetail
  | AdminMicrosoftAuxiliaryMessageDetail;

function mailboxOf(message: MailSummary): AdminMicrosoftMailboxKind {
  return "mailbox" in message ? message.mailbox : "main";
}

function ResourceMailsPanel({
  auxiliary = false,
  emptyDescription,
  extraOffset = 0,
  hideMailboxMeta = false,
  resourceId,
  t,
}: {
  auxiliary?: boolean;
  emptyDescription?: string;
  extraOffset?: number;
  hideMailboxMeta?: boolean;
  resourceId: number;
  t: TFunction;
}) {
  const [search, setSearch] = useState("");
  const [debouncedSearch] = useDebouncedValue(search);
  const [pageSize] = useSharedPageSize();
  const [addressFilter, setAddressFilter] = useState("all");
  const [messages, setMessages] = useState<MailSummary[]>([]);
  const [total, setTotal] = useState(0);
  const [cursor, setCursor] = useState<AdminMicrosoftMessageCursor | null>(null);
  const [nextCursor, setNextCursor] =
    useState<AdminMicrosoftMessageCursor | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [listLoading, setListLoading] = useState(true);
  const [listError, setListError] = useState<string | null>(null);
  const [retryKey, setRetryKey] = useState(0);
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const [selectedDetail, setSelectedDetail] = useState<MailDetail | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const listScope = `${auxiliary}\u0000${resourceId}\u0000${debouncedSearch}\u0000${pageSize}`;
  const listScopeRef = useRef(listScope);

  useEffect(() => {
    setMessages([]);
    setTotal(0);
    setCursor(null);
    setNextCursor(null);
    setHasMore(false);
    setListError(null);
    setSelectedId(null);
    setSelectedDetail(null);
    setAddressFilter("all");
  }, [auxiliary, debouncedSearch, pageSize, resourceId]);

  useEffect(() => {
    if (listScopeRef.current !== listScope) {
      listScopeRef.current = listScope;
      if (cursor) return;
    }
    const controller = new AbortController();
    setListLoading(true);
    setListError(null);
    const request = auxiliary
      ? listAdminMicrosoftBindingMessages(
          resourceId,
          debouncedSearch,
          pageSize,
          cursor ?? undefined,
          controller.signal
        )
      : listAdminMicrosoftMessages(
          resourceId,
          debouncedSearch,
          pageSize,
          cursor ?? undefined,
          controller.signal
        );
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
            "Microsoft mail load failed."
          );
          setListError(message);
          Toast.error(message);
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setListLoading(false);
      });
    return () => controller.abort();
  }, [auxiliary, cursor, debouncedSearch, listScope, pageSize, resourceId, retryKey, t]);

  const addresses = useMemo(() => {
    const map = new Map<string, AdminMicrosoftMailboxKind>();
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
    setSelectedId((current) =>
      current && filtered.some((message) => message.id === current)
        ? current
        : filtered[0]?.id ?? null
    );
  }, [filtered]);

  const selected = filtered.find((message) => message.id === selectedId) ?? null;

  useEffect(() => {
    if (!selectedId) {
      setSelectedDetail(null);
      return;
    }
    const controller = new AbortController();
    setSelectedDetail(null);
    setDetailLoading(true);
    const request = auxiliary
      ? getAdminMicrosoftBindingMessage(resourceId, selectedId, controller.signal)
      : getAdminMicrosoftMessage(resourceId, selectedId, controller.signal);
    void request
      .then((message) => {
        if (!controller.signal.aborted) setSelectedDetail(message);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          Toast.error(getIamErrorMessage(t, error, "Microsoft mail detail load failed."));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setDetailLoading(false);
      });
    return () => controller.abort();
  }, [auxiliary, resourceId, selectedId, t]);

  const loadMore = useCallback(
    (element: HTMLDivElement) => {
      if (listLoading || listError || !hasMore || !nextCursor) return;
      const remaining = element.scrollHeight - element.scrollTop - element.clientHeight;
      if (remaining < 80) setCursor(nextCursor);
    },
    [hasMore, listError, listLoading, nextCursor]
  );

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
          <div className="flex items-center justify-between text-xs text-[var(--semi-color-text-2)]">
            <span>{t("Mail count")}</span>
            <span className="font-mono tabular-nums">{total}</span>
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
          data-testid={
            auxiliary
              ? "admin-microsoft-auxiliary-message-list"
              : "admin-microsoft-message-list"
          }
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
                className={`block w-full border-b border-[var(--semi-color-border)] px-3 py-2.5 text-left transition-colors ${
                  selected?.id === message.id
                    ? "bg-[var(--semi-color-primary-light-default)]"
                    : "hover:bg-[var(--semi-color-fill-0)]"
                }`}
                key={message.id}
                onClick={() => setSelectedId(message.id)}
                type="button"
              >
                <div className="flex items-center justify-between gap-2">
                  <span className="min-w-0 flex-1 truncate text-sm font-medium text-[var(--semi-color-text-0)]">
                    {message.subject}
                  </span>
                  {message.verificationCode ? (
                    <span className="shrink-0 font-mono text-xs font-semibold text-[var(--semi-color-success)]">
                      {message.verificationCode}
                    </span>
                  ) : (
                    <span className="shrink-0">{renderMessageStatusTag(message.status, t)}</span>
                  )}
                </div>
                <div className="mt-1 flex min-w-0 items-center gap-1.5">
                  {hideMailboxMeta ? null : renderMailboxTag(mailboxOf(message), t)}
                  <span className="min-w-0 flex-1 truncate text-xs text-[var(--semi-color-text-2)]">
                    {message.recipient}
                  </span>
                </div>
                <div className="mt-1 flex items-center justify-between gap-2 text-xs text-[var(--semi-color-text-2)]">
                  <span className="min-w-0 flex-1 truncate">{message.sender}</span>
                  <span className="shrink-0">{formatTime(message.receivedAt)}</span>
                </div>
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
                label={t("Verification code")}
                value={
                  selected.verificationCode ? (
                    <CopyableTableText copiedText={t("Copied")} text={selected.verificationCode} />
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
            <div className="whitespace-pre-wrap break-words rounded-lg bg-[var(--semi-color-fill-0)] p-3 text-sm text-[var(--semi-color-text-0)]">
              {detailLoading ? <Spin /> : selectedDetail?.body ?? selected.preview}
            </div>
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

export function MicrosoftDetailSheet({
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
  detail: AdminMicrosoftResourceDetail | null;
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
          ? `${t("Microsoft resource detail")} #${detail.id}`
          : t("Microsoft resource detail")
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
              <Tabs.TabPane itemKey="other" tab={t("Other aliases")} />
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
            {activeTab === "explicit" ? <AliasPanel kind="explicit" resourceId={detail.id} t={t} /> : null}
            {activeTab === "other" ? <AliasPanel kind="other" resourceId={detail.id} t={t} /> : null}
            {activeTab === "tasks" ? (
              <TaskDiagnostics detail={detail} onRefresh={onRefresh} t={t} />
            ) : null}
            {activeTab === "mails" ? (
              <ResourceMailsPanel hideMailboxMeta resourceId={detail.id} t={t} />
            ) : null}
            {activeTab === "auxiliary" ? (
              detail.bindingAddress ? (
                <>
                  <div className="mb-3 flex flex-wrap items-center gap-2 text-sm">
                    <span className="text-[var(--semi-color-text-2)]">{t("Auxiliary email")}</span>
                    <CopyableTableText copiedText={t("Copied")} text={detail.bindingAddress} />
                  </div>
                  <ResourceMailsPanel
                    auxiliary
                    emptyDescription={t("No auxiliary mail yet")}
                    extraOffset={40}
                    hideMailboxMeta
                    resourceId={detail.id}
                    t={t}
                  />
                </>
              ) : (
                <Empty description={t("No auxiliary mailbox configured")} style={{ padding: 32 }} />
              )
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
