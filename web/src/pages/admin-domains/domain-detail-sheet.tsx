import { useEffect, useMemo, useState } from "react";
import {
  Button,
  Empty,
  Input,
  SideSheet,
  Table,
  Tabs,
  Tag,
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
  formatLedgerAmount,
  renderOrderStatusTag,
  renderServiceModeTag,
} from "../orders/order-meta";
import { ProjectIcon } from "../workbench/project-icon";
import type {
  AdminDomainDetail,
  AdminDomainMessage,
  AdminDomainOrder,
  AdminDomainTask,
  AdminGeneratedMailbox,
  AdminMailServer,
} from "./admin-domains-mock";
import {
  DomainInfoItem,
  domainOwnerRoleLabel,
  formatDomainTime,
  renderDomainPurposeTag,
  renderDomainStatusTag,
} from "./domain-meta";

const { Text } = Typography;
const DRAWER_TABLE_SCROLL_Y = "max(220px, calc(100vh - 337px))";
const DRAWER_PANEL_HEIGHT = "max(360px, calc(100vh - 237px))";

function serverStatusLabel(status: AdminMailServer["status"]) {
  switch (status) {
    case "online":
      return "Online";
    case "offline":
      return "Offline";
    default:
      return "Disabled";
  }
}

function DomainPagedTable({
  columns,
  items,
  emptyDescription,
  extraOffset = 0,
  page,
  pageSize,
  rowKey = "id",
  scrollX,
  setPage,
  setPageSize,
  t,
}: {
  columns: any[];
  items: any[];
  emptyDescription: string;
  extraOffset?: number;
  page: number;
  pageSize: number;
  rowKey?: string;
  scrollX?: number;
  setPage: (page: number) => void;
  setPageSize: (pageSize: number) => void;
  t: TFunction;
}) {
  const isMobile = useIsMobile();
  const totalPages = Math.max(1, Math.ceil(items.length / pageSize));
  const safePage = Math.min(page, totalPages);
  const pageItems = items.slice(
    (safePage - 1) * pageSize,
    safePage * pageSize
  );
  const panelHeight = extraOffset
    ? `calc(${DRAWER_PANEL_HEIGHT} - ${extraOffset}px)`
    : DRAWER_PANEL_HEIGHT;
  const tableScrollY = extraOffset
    ? `calc(${DRAWER_TABLE_SCROLL_Y} - ${extraOffset}px)`
    : DRAWER_TABLE_SCROLL_Y;

  useEffect(() => {
    if (page !== safePage) setPage(safePage);
  }, [page, safePage, setPage]);

  return (
    <div className="flex flex-col" style={{ height: panelHeight }}>
      <div className="min-h-0 flex-1 overflow-hidden">
        {items.length === 0 ? (
          <Empty description={emptyDescription} style={{ padding: 24 }} />
        ) : (
          <Table
            columns={columns}
            dataSource={pageItems}
            pagination={false}
            rowKey={rowKey}
            scroll={{ x: scrollX, y: tableScrollY }}
            size="small"
          />
        )}
      </div>
      {items.length > 0 ? (
        <div className="mt-3 flex flex-wrap items-center justify-end gap-3 border-t border-[var(--semi-color-border)] pt-3">
          {createCardProPagination({
            currentPage: safePage,
            isMobile,
            onPageChange: setPage,
            onPageSizeChange: (size) => {
              setPageSize(size);
              setPage(1);
            },
            pageSize,
            pageSizeOpts: [10, 20, 50, 100],
            showSizeChanger: true,
            t,
            total: items.length,
          })}
        </div>
      ) : null}
    </div>
  );
}

function DomainAliasPanel({
  aliases,
  t,
}: {
  aliases: AdminGeneratedMailbox[];
  t: TFunction;
}) {
  const [pageSize, setPageSize] = useSharedPageSize();
  const [page, setPage] = useState(1);
  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "email",
          title: t("Alias"),
          render: (value: unknown) => (
            <CopyableTableText copiedText={t("Copied")} text={String(value)} />
          ),
        },
        {
          dataIndex: "createdAt",
          title: t("Created at"),
          width: 220,
          render: (value: unknown) => formatDomainTime(String(value)),
        },
      ] as any[],
    [t]
  );

  return (
    <DomainPagedTable
      columns={columns}
      emptyDescription={t("No aliases yet")}
      items={aliases}
      page={page}
      pageSize={pageSize}
      setPage={setPage}
      setPageSize={setPageSize}
      t={t}
    />
  );
}

function taskLabel(kind: AdminDomainTask["kind"]) {
  if (kind === "validation") return "Validation";
  if (kind === "alias_replenishment") return "Alias replenishment";
  return "Mail fetch";
}

function taskStatusTag(status: AdminDomainTask["status"], t: TFunction) {
  return (
    <Tag
      color={
        status === "succeeded"
          ? "green"
          : status === "failed"
            ? "red"
            : status === "running"
              ? "orange"
              : "blue"
      }
      shape="circle"
      size="small"
    >
      {t(
        status === "succeeded"
          ? "Succeeded"
          : status === "failed"
            ? "Failed"
            : status === "running"
              ? "Running"
              : "Queued"
      )}
    </Tag>
  );
}

function DomainTaskPanel({
  onMailFetch,
  onValidate,
  tasks,
  t,
}: {
  onMailFetch?: () => void | Promise<void>;
  onValidate?: () => void | Promise<void>;
  tasks: AdminDomainTask[];
  t: TFunction;
}) {
  const [busy, setBusy] = useState<"validate" | "fetch" | null>(null);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [page, setPage] = useState(1);
  const succeeded = tasks.filter((task) => task.status === "succeeded").length;
  const successRate =
    tasks.length > 0 ? Math.round((succeeded / tasks.length) * 100) : 0;

  const runAction = async (
    action: "validate" | "fetch",
    operation?: () => void | Promise<void>
  ) => {
    if (!operation) return;
    setBusy(action);
    try {
      await operation();
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
          render: (value: unknown) =>
            t(taskLabel(value as AdminDomainTask["kind"])),
        },
        {
          dataIndex: "status",
          title: t("Status"),
          width: 110,
          render: (value: unknown) =>
            taskStatusTag(value as AdminDomainTask["status"], t),
        },
        {
          dataIndex: "remainingAttempts",
          title: t("Remaining attempts"),
          width: 120,
          render: (value: unknown) => (
            <span className="font-mono tabular-nums">{Number(value)}</span>
          ),
        },
        {
          dataIndex: "queuedAt",
          title: t("Queued at"),
          width: 170,
          render: (value: unknown) =>
            formatDomainTime(value ? String(value) : undefined),
        },
        {
          dataIndex: "startedAt",
          title: t("Started at"),
          width: 170,
          render: (value: unknown) =>
            formatDomainTime(value ? String(value) : undefined),
        },
        {
          dataIndex: "finishedAt",
          title: t("Finished at"),
          width: 170,
          render: (value: unknown) =>
            formatDomainTime(value ? String(value) : undefined),
        },
        {
          dataIndex: "updatedAt",
          title: t("Updated at"),
          width: 170,
          render: (value: unknown) => formatDomainTime(String(value)),
        },
      ] as any[],
    [t]
  );

  return (
    <div>
      <div className="mb-4">
        <div className="grid gap-3 sm:grid-cols-3">
          <DomainInfoItem
            label={t("Total tasks")}
            value={<span className="font-mono tabular-nums">{tasks.length}</span>}
          />
          <DomainInfoItem
            label={t("Succeeded tasks")}
            value={<span className="font-mono tabular-nums">{succeeded}</span>}
          />
          <DomainInfoItem
            label={t("Success rate")}
            value={<span className="font-mono tabular-nums">{successRate}%</span>}
          />
        </div>
        <div className="mt-3 flex flex-wrap gap-2">
          <Button
            disabled={!onValidate || (busy !== null && busy !== "validate")}
            loading={busy === "validate"}
            onClick={
              onValidate
                ? () => void runAction("validate", onValidate)
                : undefined
            }
            size="small"
            type="tertiary"
          >
            {t("Validate")}
          </Button>
          <Button
            disabled={!onMailFetch || (busy !== null && busy !== "fetch")}
            loading={busy === "fetch"}
            onClick={
              onMailFetch
                ? () => void runAction("fetch", onMailFetch)
                : undefined
            }
            size="small"
            type="tertiary"
          >
            {t("Mail fetch")}
          </Button>
        </div>
      </div>
      <DomainPagedTable
        columns={columns}
        emptyDescription={t("No task records")}
        extraOffset={150}
        items={tasks}
        page={page}
        pageSize={pageSize}
        setPage={setPage}
        setPageSize={setPageSize}
        t={t}
      />
    </div>
  );
}

function mailStatusTag(status: AdminDomainMessage["status"], t: TFunction) {
  return (
    <Tag
      color={status === "matched" ? "green" : status === "received" ? "blue" : "grey"}
      shape="circle"
      size="small"
    >
      {t(
        status === "matched"
          ? "Matched"
          : status === "received"
            ? "Received"
            : "Ignored"
      )}
    </Tag>
  );
}

function DomainMailsPanel({
  messages,
  t,
}: {
  messages: AdminDomainMessage[];
  t: TFunction;
}) {
  const [search, setSearch] = useState("");
  const [debouncedSearch] = useDebouncedValue(search);
  const filtered = useMemo(() => {
    const keyword = debouncedSearch.trim().toLowerCase();
    if (!keyword) return messages;
    return messages.filter((message) =>
      [
        message.sender,
        message.recipient,
        message.subject,
        message.body,
      ]
        .join(" ")
        .toLowerCase()
        .includes(keyword)
    );
  }, [debouncedSearch, messages]);
  const [selectedId, setSelectedId] = useState<number | null>(
    filtered[0]?.id ?? null
  );

  useEffect(() => {
    setSelectedId((current) =>
      current && filtered.some((message) => message.id === current)
        ? current
        : filtered[0]?.id ?? null
    );
  }, [filtered]);

  const selected =
    filtered.find((message) => message.id === selectedId) ?? null;

  if (messages.length === 0) {
    return <Empty description={t("No mails yet")} style={{ padding: 32 }} />;
  }

  return (
    <div
      className="flex flex-col overflow-hidden rounded-xl border border-[var(--semi-color-border)] md:grid md:grid-cols-[320px_minmax(0,1fr)]"
      style={{ height: DRAWER_PANEL_HEIGHT }}
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
        </div>
        <div className="min-h-0 flex-1 overflow-auto">
          {filtered.length === 0 ? (
            <Empty description={t("No matched mail")} style={{ padding: 24 }} />
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
                    <span className="shrink-0">
                      {mailStatusTag(message.status, t)}
                    </span>
                  )}
                </div>
                <div className="mt-1 min-w-0 truncate text-xs text-[var(--semi-color-text-2)]">
                  {message.recipient}
                </div>
                <div className="mt-1 flex items-center justify-between gap-2 text-xs text-[var(--semi-color-text-2)]">
                  <span className="min-w-0 flex-1 truncate">
                    {message.sender}
                  </span>
                  <span className="shrink-0">
                    {formatDomainTime(message.receivedAt)}
                  </span>
                </div>
              </button>
            ))
          )}
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
                {mailStatusTag(selected.status, t)}
                <span>{selected.sender}</span>
                <span>·</span>
                <span>{formatDomainTime(selected.receivedAt)}</span>
              </div>
            </div>
            <div className="grid gap-2 sm:grid-cols-2">
              <DomainInfoItem
                label={t("Recipient")}
                value={
                  <CopyableTableText
                    copiedText={t("Copied")}
                    text={selected.recipient}
                  />
                }
              />
              <DomainInfoItem
                label={t("Verification code")}
                value={
                  selected.verificationCode ? (
                    <CopyableTableText
                      copiedText={t("Copied")}
                      text={selected.verificationCode}
                    />
                  ) : (
                    "-"
                  )
                }
              />
              {selected.orderNo ? (
                <DomainInfoItem
                  label={t("Order No")}
                  value={
                    <CopyableTableText
                      copiedText={t("Copied")}
                      text={selected.orderNo}
                    />
                  }
                />
              ) : null}
            </div>
            <div className="whitespace-pre-wrap break-words rounded-lg bg-[var(--semi-color-fill-0)] p-3 text-sm text-[var(--semi-color-text-0)]">
              {selected.body}
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

function DomainOrdersPanel({
  orders,
  t,
}: {
  orders: AdminDomainOrder[];
  t: TFunction;
}) {
  const isMobile = useIsMobile();
  const [pageSize, setPageSize] = useSharedPageSize();
  const [page, setPage] = useState(1);
  const totalPages = Math.max(1, Math.ceil(orders.length / pageSize));
  const safePage = Math.min(page, totalPages);
  const pageItems = orders.slice(
    (safePage - 1) * pageSize,
    safePage * pageSize
  );

  useEffect(() => {
    if (page !== safePage) setPage(safePage);
  }, [page, safePage]);

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
          render: (_: unknown, record: AdminDomainOrder) => (
            <div className="flex min-w-0 items-center gap-2">
              <ProjectIcon
                logoUrl={record.projectLogoUrl}
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
          render: (value: unknown) => (
            <Text
              className="font-mono-data"
              copyable={createCopyableConfig(String(value), t("Copied"))}
              style={{ color: "var(--semi-color-primary)" }}
            >
              {String(value)}
            </Text>
          ),
        },
        {
          dataIndex: "supplyScope",
          title: t("Supply scope"),
          width: 110,
          render: (value: unknown) => (
            <Tag
              color={value === "public" ? "blue" : "grey"}
              shape="circle"
              size="small"
            >
              {value === "public" ? t("Public") : t("Owned")}
            </Tag>
          ),
        },
        {
          dataIndex: "serviceMode",
          title: t("Service mode"),
          width: 130,
          render: (value: unknown) =>
            renderServiceModeTag(value as "code" | "purchase", t),
        },
        {
          dataIndex: "orderStatus",
          title: t("Status"),
          width: 130,
          render: (value: unknown) =>
            renderOrderStatusTag(
              value as AdminDomainOrder["orderStatus"],
              t
            ),
        },
        {
          dataIndex: "allocationStatus",
          title: t("Allocated"),
          width: 110,
          render: (value: unknown) => (
            <Tag
              color={value === "allocated" ? "green" : "grey"}
              shape="circle"
              size="small"
            >
              {value === "allocated" ? t("Allocated") : t("Released")}
            </Tag>
          ),
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
          render: (value: unknown, record: AdminDomainOrder) =>
            value ? (
              <Text
                className="font-mono-data"
                copyable={createCopyableConfig(String(value), t("Copied"))}
                style={{ color: "var(--semi-color-success)" }}
              >
                {String(value)}
              </Text>
            ) : record.orderStatus === "active" ? (
              <Tag color="grey" shape="circle" size="small">
                {t("Waiting")}
              </Tag>
            ) : (
              <span className="text-[var(--semi-color-text-3)]">-</span>
            ),
        },
        {
          dataIndex: "createdAt",
          title: t("Created at"),
          width: 170,
          render: (value: unknown) => formatDomainTime(String(value)),
        },
        {
          dataIndex: "receiveUntil",
          title: t("Receive until"),
          width: 170,
          render: (value: unknown) =>
            formatDomainTime(value ? String(value) : undefined),
        },
      ] as any[],
    [t]
  );

  return (
    <div className="flex flex-col" style={{ height: DRAWER_PANEL_HEIGHT }}>
      <div className="min-h-0 flex-1 overflow-hidden">
        {orders.length === 0 ? (
          <Empty description={t("No related orders")} style={{ padding: 24 }} />
        ) : (
          <Table
            columns={columns}
            dataSource={pageItems}
            pagination={false}
            rowKey="id"
            scroll={{ x: 1720, y: DRAWER_TABLE_SCROLL_Y }}
            size="small"
          />
        )}
      </div>
      {orders.length > 0 ? (
        <div className="mt-3 flex flex-wrap items-center justify-end gap-3 border-t border-[var(--semi-color-border)] pt-3">
          {createCardProPagination({
            currentPage: safePage,
            isMobile,
            onPageChange: setPage,
            onPageSizeChange: (size) => {
              setPageSize(size);
              setPage(1);
            },
            pageSize,
            pageSizeOpts: [10, 20, 50, 100],
            showSizeChanger: true,
            t,
            total: orders.length,
          })}
        </div>
      ) : null}
    </div>
  );
}

export function DomainDetailSheet({
  detail,
  loading,
  onCancel,
  onValidate,
  onMailFetch,
  onRecover,
  onDelete,
  onEdit,
  onTogglePurpose,
  onToggleDisabled,
  busy,
}: {
  detail: AdminDomainDetail | null;
  loading: boolean;
  onCancel: () => void;
  onValidate?: () => void;
  onMailFetch?: () => void;
  onRecover?: () => void;
  onDelete?: () => void;
  onEdit?: () => void;
  onTogglePurpose?: () => void;
  onToggleDisabled?: () => void;
  busy: boolean;
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
      title={detail ? `${t("Domain detail")} #${detail.id}` : t("Domain detail")}
      visible={Boolean(detail) || loading}
      width={isMobile ? "100%" : 940}
    >
      {detail ? (
        <div className="flex min-h-full flex-col">
          <div className="sticky top-0 z-10 bg-[var(--semi-color-bg-2)] px-5 pt-2">
            <Tabs activeKey={activeTab} collapsible onChange={setActiveTab} type="line">
              <Tabs.TabPane itemKey="basic" tab={t("Basic info")} />
              <Tabs.TabPane itemKey="orders" tab={t("Orders")} />
              <Tabs.TabPane itemKey="aliases" tab={t("Aliases")} />
              <Tabs.TabPane itemKey="tasks" tab={t("Task details")} />
              <Tabs.TabPane itemKey="mails" tab={t("Mailbox")} />
            </Tabs>
          </div>
          <div className="flex-1 p-5">
            {activeTab === "basic" ? (
              <div className="space-y-6">
                <section>
                  <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">
                    {t("Basic info")}
                  </div>
                  <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                    <DomainInfoItem
                      label={t("Domain")}
                      value={
                        <CopyableTableText
                          copiedText={t("Copied")}
                          text={detail.domain}
                        />
                      }
                    />
                    <DomainInfoItem label={t("TLD")} value={detail.domainTld} />
                    <DomainInfoItem
                      label={t("Owner")}
                      value={
                        <div className="min-w-0">
                          <CopyableTableText
                            copiedText={t("Copied")}
                            text={detail.ownerEmail}
                          />
                          <div className="truncate text-xs text-[var(--semi-color-text-2)]">
                            {detail.ownerNickname} ·{" "}
                            {t(domainOwnerRoleLabel(detail.ownerRole))}
                          </div>
                        </div>
                      }
                    />
                    <DomainInfoItem label={t("Purpose")} value={renderDomainPurposeTag(detail.purpose, t)} />
                    <DomainInfoItem
                      label={t("Status")}
                      value={renderDomainStatusTag(detail.status, t, detail.lastSafeError)}
                    />
                    <DomainInfoItem label={t("Created at")} value={formatDomainTime(detail.createdAt)} />
                    <DomainInfoItem label={t("Updated at")} value={formatDomainTime(detail.updatedAt)} />
                  </div>
                </section>
                <section>
                  <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">
                    {t("Operational summary")}
                  </div>
                  <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
                    <DomainInfoItem label={t("Mailboxes")} value={detail.mailboxCount} />
                    <DomainInfoItem label={t("Last allocated")} value={formatDomainTime(detail.lastAllocatedAt)} />
                    <DomainInfoItem label={t("Orders")} value={detail.orders.length} />
                    <DomainInfoItem
                      label={t("Latest task")}
                      value={
                        detail.tasks[0] ? (
                          <Tag
                            color={
                              detail.tasks[0].status === "succeeded"
                                ? "green"
                                : detail.tasks[0].status === "failed"
                                  ? "red"
                                  : detail.tasks[0].status === "running"
                                    ? "orange"
                                    : "blue"
                            }
                            shape="circle"
                            size="small"
                          >
                            {t(
                              detail.tasks[0].status === "succeeded"
                                ? "Succeeded"
                                : detail.tasks[0].status === "failed"
                                  ? "Failed"
                                  : detail.tasks[0].status === "running"
                                    ? "Running"
                                    : "Queued"
                            )}
                          </Tag>
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
                <section>
                  <div className="mb-2 text-sm font-semibold text-[var(--semi-color-text-0)]">
                    {t("Mail server")}
                  </div>
                  <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                    <DomainInfoItem
                      label={t("Server address")}
                      value={
                        <CopyableTableText
                          copiedText={t("Copied")}
                          text={detail.mailServer.serverAddress}
                        />
                      }
                    />
                    <DomainInfoItem
                      label={t("MX Record")}
                      value={
                        <CopyableTableText
                          copiedText={t("Copied")}
                          text={detail.mailServer.mxRecord}
                        />
                      }
                    />
                    <DomainInfoItem
                      label={t("Server status")}
                      value={
                        <Tag
                          color={detail.mailServer.status === "online" ? "green" : detail.mailServer.status === "offline" ? "orange" : "grey"}
                          shape="circle"
                          size="small"
                        >
                          {t(serverStatusLabel(detail.mailServer.status))}
                        </Tag>
                      }
                    />
                    <DomainInfoItem
                      label="SPF"
                      value={
                        <CopyableTableText
                          copiedText={t("Copied")}
                          text={detail.mailServer.spf}
                        />
                      }
                    />
                    <DomainInfoItem
                      label="DKIM"
                      value={
                        <CopyableTableText
                          copiedText={t("Copied")}
                          text={detail.mailServer.dkim}
                        />
                      }
                    />
                    <DomainInfoItem
                      label="DMARC"
                      value={
                        <CopyableTableText
                          copiedText={t("Copied")}
                          text={detail.mailServer.dmarc}
                        />
                      }
                    />
                    <DomainInfoItem
                      label="PTR"
                      value={
                        <CopyableTableText
                          copiedText={t("Copied")}
                          text={detail.mailServer.ptr}
                        />
                      }
                    />
                  </div>
                </section>
              </div>
            ) : null}
            {activeTab === "aliases" ? (
              <DomainAliasPanel aliases={detail.mailboxes} t={t} />
            ) : null}
            {activeTab === "orders" ? (
              <DomainOrdersPanel orders={detail.orders} t={t} />
            ) : null}
            {activeTab === "tasks" ? (
              <DomainTaskPanel
                onMailFetch={onMailFetch}
                onValidate={onValidate}
                tasks={detail.tasks}
                t={t}
              />
            ) : null}
            {activeTab === "mails" ? (
              <DomainMailsPanel messages={detail.messages} t={t} />
            ) : null}
          </div>
          <div className="sticky bottom-0 flex flex-wrap items-center justify-end gap-2 border-t border-[var(--semi-color-border)] bg-[var(--semi-color-bg-0)] px-5 py-3">
            {detail.status === "deleted" ? (
              <Button
                disabled={!onRecover}
                loading={busy}
                onClick={onRecover}
                type="primary"
              >
                {t("Recover")}
              </Button>
            ) : (
              <>
                <Button
                  disabled={!onValidate}
                  loading={busy}
                  onClick={onValidate}
                  type="tertiary"
                >
                  {t("Check")}
                </Button>
                <Button
                  disabled={busy || !onEdit}
                  onClick={onEdit}
                  type="tertiary"
                >
                  {t("Edit")}
                </Button>
                <Button
                  disabled={busy || !onTogglePurpose}
                  onClick={onTogglePurpose}
                  type="tertiary"
                >
                  {detail.purpose === "sale" ? t("Convert to private") : t("Put on sale")}
                </Button>
                <Button
                  disabled={!onToggleDisabled}
                  loading={busy}
                  onClick={onToggleDisabled}
                  type="tertiary"
                >
                  {detail.status === "disabled" ? t("Enable") : t("Disable")}
                </Button>
                <Button
                  disabled={!onDelete}
                  loading={busy}
                  onClick={onDelete}
                  type="danger"
                >
                  {t("Delete")}
                </Button>
              </>
            )}
          </div>
        </div>
      ) : (
        <div className="flex h-40 items-center justify-center">
          <div className="h-6 w-6 animate-spin rounded-full border-2 border-[var(--semi-color-primary)] border-t-transparent" />
        </div>
      )}
    </SideSheet>
  );
}
