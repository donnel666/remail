import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  DatePicker,
  Dropdown,
  Empty,
  Input,
  Modal,
  Select,
  SideSheet,
  Space,
  Tabs,
  Tag,
  TextArea,
  Toast,
  Tooltip,
  Typography,
} from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import {
  IllustrationNoResult,
  IllustrationNoResultDark,
} from "@douyinfe/semi-illustrations";
import { SlidersHorizontal, Smartphone } from "lucide-react";
import { useTranslation } from "react-i18next";

import { CardPro } from "@/components/semi/card-pro";
import { createCardProPagination } from "@/components/semi/card-pro-pagination";
import {
  CardTable,
  DESKTOP_TABLE_SCROLL_Y,
} from "@/components/semi/card-table";
import { CompactModeToggle } from "@/components/semi/compact-mode-toggle";
import { CopyableTableText } from "@/components/semi/copyable-table-text";
import { StatisticFilterOption } from "@/components/semi/statistic-filter-option";
import {
  hasPermissionKey,
  permissionKey,
  useAuth,
} from "@/context/auth-provider";
import { useBlockPagedList } from "@/hooks/use-block-paged-list";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import {
  deleteAdminKitesimPhones,
  disableAdminKitesimPhones,
  enableAdminKitesimPhones,
  importAdminKitesimAccounts,
  listAdminKitesimAccountTasks,
  listAdminKitesimMessages,
  listAdminKitesimPhones,
  syncAdminKitesimAccount,
  type AdminKitesimListFilter,
  type AdminKitesimMessage,
  type AdminKitesimPhoneFacets,
  type AdminKitesimPhoneItem,
  type AdminKitesimPhoneStatus,
  type AdminKitesimSyncRun,
  type AdminKitesimSyncRunList,
  type AdminKitesimSyncTaskStatus,
} from "@/lib/admin-kitesim-api";
import { copyText } from "@/lib/clipboard";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  listKitesimProducts,
  renewKitesimPhone,
  type KitesimProduct,
} from "@/lib/kitesim-upstream-api";

import { InfoItem } from "./admin-microsoft/microsoft-meta";
import { ServerPaginatedDrawerTable } from "./admin-microsoft/microsoft-detail-sheet";
import {
  DATE_RANGE_DROPDOWN_CLASS,
  createDateRangePresets,
  createdFromISOString,
  createdToISOString,
  normalizeDateRangeValue,
  type DateRangeValue,
} from "./resources/date-range-filter";
import { useSelectionNotification } from "./resources/use-selection-notification";

const { Text } = Typography;
const IMPORT_ENTRY_AREA_HEIGHT = 208;

type StatusFilter = "all" | AdminKitesimPhoneStatus;
type BooleanFilter = "all" | "yes" | "no";
type DetailTab = "basic" | "tasks" | "mails";
type LifecycleAction = "disable" | "enable" | "delete";

const STATUS_ORDER: AdminKitesimPhoneStatus[] = [
  "active",
  "pending",
  "activating",
  "expired",
  "refunded",
  "unsynced",
  "disabled",
  "exclusive",
  "blacklisted",
];

const STATUS_META: Record<
  AdminKitesimPhoneStatus,
  { color: "green" | "blue" | "orange" | "red" | "grey"; label: string }
> = {
  active: { color: "green", label: "In use" },
  pending: { color: "blue", label: "Pending payment" },
  activating: { color: "orange", label: "Pending activation" },
  expired: { color: "red", label: "Expired" },
  refunded: { color: "grey", label: "Refunded" },
  unsynced: { color: "grey", label: "Unsynced" },
  disabled: { color: "grey", label: "Disabled" },
  exclusive: { color: "orange", label: "Exclusive" },
  blacklisted: { color: "red", label: "Blacklisted" },
};

const SYNC_META: Record<
  AdminKitesimSyncTaskStatus,
  { color: "green" | "blue" | "orange" | "red" | "grey"; label: string }
> = {
  idle: { color: "grey", label: "Idle" },
  queued: { color: "blue", label: "Queued" },
  running: { color: "orange", label: "Running" },
  succeeded: { color: "green", label: "Succeeded" },
  failed: { color: "red", label: "Failed" },
};

function rowKey(item: AdminKitesimPhoneItem) {
  return `${item.accountId}:${item.phoneId ?? 0}`;
}

function phoneIDs(items: AdminKitesimPhoneItem[]) {
  return Array.from(new Set(items.flatMap((item) => item.phoneId ? [item.phoneId] : [])));
}

function deleteTargets(items: AdminKitesimPhoneItem[]) {
  return {
    accountIds: Array.from(new Set(items.flatMap((item) => item.phoneId ? [] : [item.accountId]))),
    phoneIds: phoneIDs(items),
  };
}

function formatTime(value?: string | null) {
  if (!value) return "-";
  const parsed = new Date(value.includes("T") ? value : value.replace(" ", "T"));
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString();
}

function formatMoney(currency?: string, value?: string) {
  if (!value) return "-";
  return [currency, value].filter(Boolean).join(" ");
}

function phoneCopyContent(value: string) {
  return value.replace(/^\+\d+\s+/, "");
}

function statusTag(status: AdminKitesimPhoneStatus, error: string | undefined, t: (key: string) => string) {
  const tag = (
    <Tag color={STATUS_META[status].color} shape="circle">
      {t(STATUS_META[status].label)}
    </Tag>
  );
  return error ? <Tooltip content={error}>{tag}</Tooltip> : tag;
}

function syncTag(status: AdminKitesimSyncTaskStatus, t: (key: string) => string) {
  const meta = SYNC_META[status];
  return (
    <Tag color={meta.color} shape="circle">
      {t(meta.label)}
    </Tag>
  );
}

function booleanTag(value: boolean, t: (key: string) => string) {
  return (
    <Tag color={value ? "green" : "grey"} shape="circle">
      {value ? t("Yes") : t("No")}
    </Tag>
  );
}

function ImportKitesimModal({
  onCancel,
  onImported,
  visible,
}: {
  onCancel: () => void;
  onImported: () => void | Promise<void>;
  visible: boolean;
}) {
  const { t } = useTranslation();
  const [content, setContent] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const previousVisible = useRef(false);

  useEffect(() => {
    const opened = visible && !previousVisible.current;
    previousVisible.current = visible;
    if (opened) setContent("");
  }, [visible]);

  const lines = useMemo(
    () => content.split(/\r?\n/).filter((line) => line.trim().length > 0),
    [content],
  );

  const submit = async () => {
    if (lines.length === 0) {
      Toast.warning(t("Please enter Kitesim accounts."));
      return;
    }
    setSubmitting(true);
    try {
      const response = await importAdminKitesimAccounts(content);
      Toast.success(t("Kitesim import queued.", { count: response.queued }));
		if (response.failed > 0) {
			Toast.warning(t("Kitesim import partial failure.", { count: response.failed }));
			Modal.error({
				title: t("Kitesim import failure details"),
				content: (
					<div className="max-h-72 space-y-2 overflow-auto whitespace-pre-wrap font-mono text-sm">
						{response.errors.map((failure) => (
							<div key={`${failure.account}:${failure.message}`}>
								{failure.account}: {failure.message}
							</div>
						))}
					</div>
				),
			});
			await onImported();
			return;
		}
		await onImported();
		onCancel();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Kitesim operation failed."));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      centered
      confirmLoading={submitting}
      onCancel={onCancel}
      onOk={() => void submit()}
      okText={t("Import")}
      title={t("Import Kitesim Accounts")}
      visible={visible}
      width="min(666px, calc(100vw - 32px))"
    >
      <div className="space-y-4 py-1">
        <label className="block">
          <span className="mb-1.5 flex items-center justify-between text-sm font-medium text-[var(--semi-color-text-0)]">
            <span>{t("Platform account entries")} *</span>
            <Text size="small" type="tertiary">
              {t("Parsed entries", { count: lines.length })}
            </Text>
          </span>
          <TextArea
            className="font-mono"
            onChange={(value) => setContent(value)}
            placeholder="account@example.com----password"
            rows={8}
            style={{ height: IMPORT_ENTRY_AREA_HEIGHT, resize: "none" }}
            value={content}
          />
        </label>

        <div className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
          <div className="mb-1 text-xs font-medium text-[var(--semi-color-text-0)]">
            {t("Supported format")}
          </div>
          <pre className="font-mono text-xs leading-relaxed text-[var(--semi-color-text-2)]">
            account@example.com----password
          </pre>
        </div>

        <div className="rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs leading-5 text-[var(--semi-color-text-2)]">
          {t(
            "Credentials are accepted as write-only input. Passwords and tokens are never returned by this page.",
          )}
        </div>
      </div>
    </Modal>
  );
}

function KitesimRenewalModal({
  item,
  onCancel,
  onQueued,
}: {
  item: AdminKitesimPhoneItem | null;
  onCancel: () => void;
  onQueued: () => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const [products, setProducts] = useState<KitesimProduct[]>([]);
  const [productId, setProductId] = useState(0);
  const [loading, setLoading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const requestRef = useRef<AbortController | null>(null);
  const productOptions = useMemo(() => products.map((product) => {
    const value = product.durationValue || 1;
    const duration = product.durationType === 1
      ? t("Kitesim duration months", { count: value })
      : product.durationType === 2
        ? t("Kitesim duration quarters", { count: value })
        : product.durationType === 3
          ? t("Kitesim duration half-years", { count: value })
          : `${value} / ${product.durationType}`;
    const price = product.buyPrice;
    return {
      label: `${product.countryCode} · ${duration} · ${product.currency} ${price}`,
      value: product.id,
    };
  }), [products, t]);

  useEffect(() => {
    requestRef.current?.abort();
    if (!item?.phoneId) {
      setProducts([]);
      setProductId(0);
      return undefined;
    }
    setProducts([]);
    setProductId(0);
    const controller = new AbortController();
    requestRef.current = controller;
    setLoading(true);
    void listKitesimProducts(controller.signal)
      .then((allProducts) => {
        if (controller.signal.aborted) return;
        const next = allProducts.filter((product) => (
          product.active && product.countryCode.toUpperCase() === (item.countryCode || "").toUpperCase()
        ));
        setProducts(next);
        setProductId(next.find((product) => product.packageId === item.packageId)?.id ?? next[0]?.id ?? 0);
      })
      .catch((error) => {
        if (!controller.signal.aborted) {
          Toast.error(getIamErrorMessage(t, error, "Kitesim products load failed."));
        }
      })
      .finally(() => {
        if (requestRef.current === controller) {
          requestRef.current = null;
          setLoading(false);
        }
      });
    return () => controller.abort();
  }, [item?.countryCode, item?.packageId, item?.phoneId, t]);

  const selectedProduct = products.find((product) => product.id === productId);

  const submit = async () => {
    if (!item?.phoneId || !selectedProduct) {
      Toast.warning(t("Please select a renewal package."));
      return;
    }
    setSubmitting(true);
    try {
      const maxUnitPrice = selectedProduct.buyPrice;
      const operation = await renewKitesimPhone(item.phoneId, productId, maxUnitPrice);
      Toast.success(t("Kitesim renewal queued.", { id: operation.id }));
      await onQueued();
      onCancel();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Kitesim operation failed."));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      centered
      confirmLoading={submitting}
      okButtonProps={{ disabled: loading || !selectedProduct }}
      okText={t("Submit renewal")}
      onCancel={onCancel}
      onOk={() => void submit()}
      title={t("Renew Kitesim phone")}
      visible={Boolean(item)}
      width="min(520px, calc(100vw - 32px))"
    >
      <div className="space-y-4 py-2">
        <div>
          <Text type="tertiary">{t("Phone number")}</Text>
          <div className="mt-1 font-medium">{item?.phoneNumber || "-"}</div>
        </div>
        <div>
          <div className="mb-2 text-sm font-medium">{t("Renewal package")}</div>
          <Select
            aria-label={t("Renewal package")}
            disabled={loading}
            emptyContent={t("No renewal package available")}
            filter
            loading={loading}
            onChange={(value) => setProductId(Number(value ?? 0))}
            optionList={productOptions}
            placeholder={t("Select renewal package")}
            style={{ width: "100%" }}
            value={productId || undefined}
          />
        </div>
      </div>
    </Modal>
  );
}

export function KitesimMessagesPanel({ item }: { item: AdminKitesimPhoneItem }) {
  const { t } = useTranslation();
  const [messages, setMessages] = useState<AdminKitesimMessage[]>([]);
  const [loading, setLoading] = useState(false);
  const requestRef = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    setMessages([]);
    requestRef.current?.abort();
    if (!item.phoneId) {
      setLoading(false);
      return;
    }
    const controller = new AbortController();
    requestRef.current = controller;
    setLoading(true);
    try {
      const next = await listAdminKitesimMessages(item.phoneId, controller.signal);
      if (!controller.signal.aborted) setMessages(next);
    } catch (error) {
      if (!controller.signal.aborted) {
        Toast.error(getIamErrorMessage(t, error, "SMS load failed."));
      }
    } finally {
      if (requestRef.current === controller) {
        requestRef.current = null;
        setLoading(false);
      }
    }
  }, [item.phoneId, t]);

  useEffect(() => {
    void load();
    return () => requestRef.current?.abort();
  }, [load]);

  const columns = useMemo(
    () => [
      {
        dataIndex: "caller",
        key: "caller",
        title: t("Sender"),
        width: 180,
        render: (value: unknown) => String(value || "-"),
      },
      {
        dataIndex: "content",
        key: "content",
        title: t("Message content"),
        width: 440,
        render: (value: unknown) => (
          <CopyableTableText copiedText={t("Copied")} text={String(value || "-")} />
        ),
      },
      {
        dataIndex: "time",
        key: "time",
        title: t("Time"),
        width: 190,
        render: (value: unknown) => formatTime(String(value || "")),
      },
    ],
    [t],
  );

  if (!item.phoneId) {
    return <Empty description={t("No phone number synchronized")} style={{ padding: 32 }} />;
  }

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <CopyableTableText copiedText={t("Copied")} copyContent={phoneCopyContent(item.phoneNumber)} text={item.phoneNumber} />
        <Button loading={loading} onClick={() => void load()} size="small" type="tertiary">
          {t("Refresh")}
        </Button>
      </div>
      <CardTable
        columns={columns}
        dataSource={messages}
        empty={
          <Empty description={t("No SMS messages yet")} style={{ padding: 32 }} />
        }
        hidePagination
        loading={loading}
        pagination={false}
        rowKey={(message: AdminKitesimMessage, index?: number) =>
          `${message.time}:${message.caller}:${index ?? 0}`
        }
        scroll={{ x: 810, y: "max(220px, calc(100vh - 337px))" }}
        size="middle"
      />
    </div>
  );
}

export function KitesimTaskDiagnostics({ item }: { item: AdminKitesimPhoneItem }) {
  const { t } = useTranslation();
  const [pageSize, setPageSize] = useSharedPageSize();
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(true);
  const [refreshKey, setRefreshKey] = useState(0);
  const [response, setResponse] = useState<AdminKitesimSyncRunList>({
    items: [],
    limit: pageSize,
    offset: 0,
    succeeded: 0,
    total: 0,
  });

  useEffect(() => setPage(1), [item.accountId, pageSize]);
  useEffect(() => {
    const controller = new AbortController();
    let pollTimer: ReturnType<typeof globalThis.setTimeout> | null = null;
    setLoading(true);
    void listAdminKitesimAccountTasks(
      item.accountId,
      (page - 1) * pageSize,
      pageSize,
      controller.signal,
    )
      .then((next) => {
        if (controller.signal.aborted) return;
        const lastPage = Math.max(1, Math.ceil(next.total / pageSize));
        if (page > lastPage) {
          setPage(lastPage);
          return;
        }
        setResponse(next);
        if (next.items.some((task) => task.status === "queued" || task.status === "running")) {
          pollTimer = globalThis.setTimeout(() => {
            setRefreshKey((value) => value + 1);
          }, 1_500);
        }
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          Toast.error(getIamErrorMessage(t, error, "Kitesim task load failed."));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => {
      controller.abort();
      if (pollTimer) globalThis.clearTimeout(pollTimer);
    };
  }, [item.accountId, page, pageSize, refreshKey, t]);

  const total = response.total;
  const succeeded = response.succeeded;
  const successRate = total > 0 ? Math.round((succeeded / total) * 100) : 0;
  const columns = useMemo(
    () => [
      {
        dataIndex: "taskId",
        title: t("Type"),
        width: 140,
        render: () => t("Synchronize"),
      },
      {
        dataIndex: "status",
        title: t("Status"),
        width: 110,
        render: (value: unknown, record: AdminKitesimSyncRun) => {
          const tag = syncTag(value as AdminKitesimSyncTaskStatus, t);
          return record.lastSafeError ? <Tooltip content={record.lastSafeError}>{tag}</Tooltip> : tag;
        },
      },
      {
        dataIndex: "attempts",
        title: t("Attempts"),
        width: 110,
        render: (value: unknown) => <span className="font-mono tabular-nums">{Number(value)}</span>,
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
        render: (value: unknown) => formatTime(value ? String(value) : undefined),
      },
    ],
    [t],
  );

  return (
    <div>
      <div className="mb-4">
        <div className="grid gap-3 sm:grid-cols-3">
          <InfoItem label={t("Total tasks")} value={<span className="font-mono tabular-nums">{total}</span>} />
          <InfoItem label={t("Succeeded tasks")} value={<span className="font-mono tabular-nums">{succeeded}</span>} />
          <InfoItem label={t("Success rate")} value={<span className="font-mono tabular-nums">{successRate}%</span>} />
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

function KitesimDetailSheet({
  busy,
  canOperate,
  canReadMessages,
  initialTab,
  item,
  onCancel,
  onDelete,
  onRefresh,
  onSync,
  onToggleDisabled,
}: {
  busy: boolean;
  canOperate: boolean;
  canReadMessages: boolean;
  initialTab: DetailTab;
  item: AdminKitesimPhoneItem | null;
  onCancel: () => void;
  onDelete: (item: AdminKitesimPhoneItem) => void;
  onRefresh: () => void | Promise<void>;
  onSync: (item: AdminKitesimPhoneItem) => void | Promise<void>;
  onToggleDisabled: (item: AdminKitesimPhoneItem) => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [activeTab, setActiveTab] = useState<DetailTab>(initialTab);

  useEffect(
    () => setActiveTab(initialTab === "mails" && !canReadMessages ? "basic" : initialTab),
    [canReadMessages, initialTab, item && rowKey(item)],
  );

  const copy = async (value: string) => {
    await copyText(value);
    Toast.success(t("Copied"));
  };

  return (
    <SideSheet
      bodyStyle={{ padding: 0 }}
      onCancel={onCancel}
      placement="right"
      title={item ? `${t("Kitesim phone detail")} #${item.phoneId ?? item.accountId}` : t("Kitesim phone detail")}
      visible={Boolean(item)}
      width={isMobile ? "100%" : 940}
    >
      {item ? (
        <div className="flex min-h-full flex-col">
          <div className="sticky top-0 z-10 bg-[var(--semi-color-bg-2)] px-5 pt-2">
            <Tabs
              activeKey={activeTab}
              collapsible
              onChange={(key) => {
                const nextTab = key as DetailTab;
                if (nextTab !== "mails" || canReadMessages) setActiveTab(nextTab);
              }}
              type="line"
            >
              <Tabs.TabPane itemKey="basic" tab={t("Basic info")} />
              <Tabs.TabPane itemKey="tasks" tab={t("Task details")} />
              <Tabs.TabPane itemKey="mails" disabled={!canReadMessages} tab={t("Inbox")} />
            </Tabs>
          </div>

          <div className="flex-1 p-5">
            {activeTab === "basic" ? (
              <div className="space-y-6">
                <section>
                  <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">{t("Phone status")}</div>
                  <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                    <InfoItem label={t("Phone number")} value={<CopyableTableText copiedText={t("Copied")} copyContent={phoneCopyContent(item.phoneNumber || "-")} text={item.phoneNumber || "-"} />} />
                    <InfoItem label={t("Country")} value={item.countryCode || "-"} />
                    <InfoItem label={t("Status")} value={statusTag(item.status, item.lastSafeError, t)} />
                    <InfoItem label={t("Phone available")} value={booleanTag(Boolean(item.phoneId), t)} />
                    <InfoItem label={t("Created at")} value={formatTime(item.createTime)} />
                    <InfoItem label={t("Expires at")} value={formatTime(item.expireTime)} />
                    <InfoItem label={t("Refund time")} value={formatTime(item.refundTime)} />
                  </div>
                </section>

                <section>
                  <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">{t("Orders")}</div>
                  <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                    <InfoItem label={t("Order ID")} value={item.providerOrderId || "-"} />
                    <InfoItem label={t("Order number")} value={item.orderNo || "-"} />
                    <InfoItem label={t("Order status")} value={item.orderStatus ?? "-"} />
                    <InfoItem label={t("Package ID")} value={item.packageId || "-"} />
                    <InfoItem label={t("Original amount")} value={formatMoney(item.currency, item.originalAmount)} />
                    <InfoItem label={t("Paid amount")} value={formatMoney(item.currency, item.paidAmount)} />
                    <InfoItem label={t("Payment time")} value={formatTime(item.paymentTime)} />
                  </div>
                </section>

                <section>
                  <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">{t("Renewal details")}</div>
                  <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                    <InfoItem label={t("Auto renew")} value={booleanTag(item.autoRenew, t)} />
                    <InfoItem label={t("Auto renew price")} value={formatMoney(item.currency, item.autoRenewPrice)} />
                    <InfoItem label={t("Duration")} value={item.durationValue ? `${item.durationValue} / ${item.durationType ?? 0}` : "-"} />
                    <InfoItem label={t("Latest renewal")} value={formatTime(item.latestRenewalTime)} />
                    <InfoItem label={t("Next renewal")} value={formatTime(item.nextRenewalDate)} />
                  </div>
                </section>

                <section>
                  <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">{t("Platform account")}</div>
                  <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                    <InfoItem label={t("Platform account")} value={<CopyableTableText copiedText={t("Copied")} text={item.account} />} />
                    <InfoItem label={t("Token available")} value={booleanTag(item.tokenAvailable, t)} />
                    <InfoItem label={t("Token updated at")} value={formatTime(item.tokenUpdatedAt)} />
                    <InfoItem label={t("Sync healthy")} value={booleanTag(item.syncHealthy, t)} />
                    <InfoItem label={t("Last synchronized")} value={formatTime(item.lastSyncedAt)} />
                    <InfoItem label={t("Account created at")} value={formatTime(item.createdAt)} />
                  </div>
                </section>
              </div>
            ) : null}

            {activeTab === "tasks" ? <KitesimTaskDiagnostics item={item} /> : null}

            {activeTab === "mails" && canReadMessages ? <KitesimMessagesPanel item={item} /> : null}
          </div>

          <div className="sticky bottom-0 flex flex-wrap items-center justify-end gap-2 border-t border-[var(--semi-color-border)] bg-[var(--semi-color-bg-0)] px-5 py-3">
            <Button disabled={!canOperate || !item.phoneId || busy} onClick={() => void onToggleDisabled(item)} type="tertiary">
              {item.status === "disabled" ? t("Enable") : t("Disable")}
            </Button>
            <Button disabled={!canOperate || busy} onClick={() => onDelete(item)} type="danger">
              {t("Delete")}
            </Button>
            <Button disabled={!canOperate} loading={busy} onClick={() => void onSync(item)} type="primary">
              {t("Synchronize")}
            </Button>
            <Button disabled={busy} onClick={() => void onRefresh()} type="tertiary">
              {t("Refresh")}
            </Button>
            <Button disabled={!canReadMessages || !item.phoneId} onClick={() => setActiveTab("mails")} type="tertiary">
              {t("Inbox")}
            </Button>
            <Button disabled={!item.phoneNumber} onClick={() => void copy(phoneCopyContent(item.phoneNumber))} type="tertiary">
              {t("Copy phone number")}
            </Button>
            <Button onClick={() => void copy(item.account)} type="tertiary">
              {t("Copy platform account")}
            </Button>
            <Button onClick={onCancel} type="tertiary">
              {t("Close")}
            </Button>
          </div>
        </div>
      ) : null}
    </SideSheet>
  );
}

export default function AdminKitesim() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const isMobile = useIsMobile();
  const [activeStatus, setActiveStatus] = useState<StatusFilter>("all");
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [autoRenewFilter, setAutoRenewFilter] = useState<BooleanFilter>("all");
  const [tokenFilter, setTokenFilter] = useState<BooleanFilter>("all");
  const [syncFilter, setSyncFilter] = useState<BooleanFilter>("all");
  const [phoneFilter, setPhoneFilter] = useState<BooleanFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [selectedKeys, setSelectedKeys] = useState<string[]>([]);
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [facets, setFacets] = useState<AdminKitesimPhoneFacets | null>(null);
  const [importOpen, setImportOpen] = useState(false);
  const [detail, setDetail] = useState<AdminKitesimPhoneItem | null>(null);
  const [detailTab, setDetailTab] = useState<DetailTab>("basic");
  const [renewalItem, setRenewalItem] = useState<AdminKitesimPhoneItem | null>(null);
  const [syncingAccountIds, setSyncingAccountIds] = useState<number[]>([]);
  const [rowMutation, setRowMutation] = useState<{ action: LifecycleAction; key: string } | null>(null);
  const [bulkMutation, setBulkMutation] = useState<LifecycleAction | null>(null);
  const canWrite = hasPermissionKey(
    currentUser,
    permissionKey("core:resource", "write"),
  );
  const canOperate = hasPermissionKey(
    currentUser,
    permissionKey("core:resource", "operate"),
  );
  const canRenew = canOperate && hasPermissionKey(
    currentUser,
    permissionKey("system:settings", "write"),
  );
  const canReadMessages = hasPermissionKey(
    currentUser,
    permissionKey("mailmatch:message", "read"),
  );

  useEffect(() => setActivePage(1), [pageSize]);
  const [debouncedSearchKeyword, flushSearchKeyword] =
    useDebouncedValue(searchKeyword);
  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);

  const listFilter = useMemo<AdminKitesimListFilter>(() => {
    const filter: AdminKitesimListFilter = {};
    const search = debouncedSearchKeyword.trim();
    const createdFrom = createdFromISOString(createdAtRange);
    const createdTo = createdToISOString(createdAtRange);
    if (search) filter.search = search;
    if (activeStatus !== "all") filter.status = activeStatus;
    if (autoRenewFilter !== "all") filter.autoRenew = autoRenewFilter === "yes";
    if (tokenFilter !== "all") filter.tokenAvailable = tokenFilter === "yes";
    if (syncFilter !== "all") filter.syncHealthy = syncFilter === "yes";
    if (phoneFilter !== "all") filter.phoneAvailable = phoneFilter === "yes";
    if (createdFrom) filter.createdFrom = createdFrom;
    if (createdTo) filter.createdTo = createdTo;
    return filter;
  }, [
    activeStatus,
    autoRenewFilter,
    createdAtRange,
    debouncedSearchKeyword,
    phoneFilter,
    syncFilter,
    tokenFilter,
  ]);
  const listFilterKey = JSON.stringify(listFilter);

  const loadKitesimBlock = useCallback(
    async (offset: number, limit: number, _cursor: unknown, signal: AbortSignal) => {
      const response = await listAdminKitesimPhones(listFilter, offset, limit, signal);
      return { items: response.items, meta: response.facets, total: response.total };
    },
    [listFilter],
  );

  const {
    loading,
    pagedItems,
    refresh: refreshList,
    total,
  } = useBlockPagedList<AdminKitesimPhoneItem, AdminKitesimPhoneFacets>({
    activePage,
    blockSize: 100,
    filterKey: listFilterKey,
    loadBlock: loadKitesimBlock,
    onError: (error) => {
      Toast.error(getIamErrorMessage(t, error, "Kitesim accounts load failed."));
    },
    onLoaded: (response) => {
      if (response.meta) setFacets(response.meta);
    },
    pageSize,
  });

  useEffect(() => {
    if (!detail || loading) return;
    const updated = pagedItems.find((item) => rowKey(item) === rowKey(detail));
    setDetail(updated ?? null);
  }, [detail && rowKey(detail), loading, pagedItems]);

  const refresh = useCallback(async () => {
    await refreshList();
  }, [refreshList]);

  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const safePage = Math.min(activePage, totalPages);
  useEffect(() => {
    if (safePage !== activePage) setActivePage(safePage);
  }, [activePage, safePage]);

  const stats = useMemo<AdminKitesimPhoneFacets>(() => {
    if (facets) return facets;
    const emptyBoolean = { all: total, yes: 0, no: total };
    return {
      all: total,
      active: 0,
      pending: 0,
      activating: 0,
      expired: 0,
      refunded: 0,
      unsynced: 0,
      disabled: 0,
      exclusive: 0,
      blacklisted: 0,
      autoRenew: emptyBoolean,
      tokenAvailable: emptyBoolean,
      syncHealthy: emptyBoolean,
      phoneAvailable: emptyBoolean,
    };
  }, [facets, total]);

  const resetPageAndSelection = () => {
    setActivePage(1);
    setSelectedKeys([]);
  };

  const selectStatus = (status: StatusFilter) => {
    setActiveStatus(status);
    resetPageAndSelection();
  };

  const resetFilters = () => {
    setSearchKeyword("");
    flushSearchKeyword("");
    setCreatedAtRange([]);
    setActiveStatus("all");
    setAutoRenewFilter("all");
    setTokenFilter("all");
    setSyncFilter("all");
    setPhoneFilter("all");
    resetPageAndSelection();
  };

  const selectedSet = useMemo(() => new Set(selectedKeys), [selectedKeys]);
  const selectedItems = useMemo(
    () => pagedItems.filter((item) => selectedSet.has(rowKey(item))),
    [pagedItems, selectedSet],
  );

  const queueSync = useCallback(
    async (items: AdminKitesimPhoneItem[]) => {
      if (!canOperate) return;
      const accountIds = Array.from(new Set(items.map((item) => item.accountId)));
      if (accountIds.length === 0) return;
      setSyncingAccountIds(accountIds);
      try {
        const results = await Promise.allSettled(
          accountIds.map((accountId) => syncAdminKitesimAccount(accountId)),
        );
        const queued = results.filter((result) => result.status === "fulfilled").length;
        const failed = results.length - queued;
        if (queued > 0) Toast.success(t("Kitesim sync queued.", { count: queued }));
        if (failed > 0) Toast.warning(t("Kitesim sync queue partial failure.", { count: failed }));
        setSelectedKeys([]);
        await refresh();
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Kitesim operation failed."));
      } finally {
        setSyncingAccountIds([]);
      }
    },
    [canOperate, refresh, t],
  );

  const runLifecycle = useCallback(
    async (
      items: AdminKitesimPhoneItem[],
      action: LifecycleAction,
      row?: AdminKitesimPhoneItem,
    ) => {
      if (!canOperate || items.length === 0) return;
      const ids = phoneIDs(items);
      if (action !== "delete" && ids.length === 0) return;
      if (row) setRowMutation({ action, key: rowKey(row) });
      else setBulkMutation(action);
      try {
        const targets = deleteTargets(items);
        const result = action === "disable"
          ? await disableAdminKitesimPhones(ids)
          : action === "enable"
            ? await enableAdminKitesimPhones(ids)
            : await deleteAdminKitesimPhones(targets.phoneIds, targets.accountIds);
        Toast.success(t(
          action === "delete"
            ? "Kitesim phone rows deleted."
            : action === "disable"
              ? "Kitesim phones disabled."
              : "Kitesim phones enabled.",
          { count: result.affected },
        ));
        setSelectedKeys([]);
        if (action === "delete") {
          setActivePage(1);
          if (detail && items.some((item) => rowKey(item) === rowKey(detail))) {
            setDetail(null);
          }
        }
        await refresh();
      } catch (error) {
        Toast.error(getIamErrorMessage(t, error, "Kitesim operation failed."));
      } finally {
        if (row) setRowMutation(null);
        else setBulkMutation(null);
      }
    },
    [canOperate, detail, refresh, t],
  );

  const confirmDelete = useCallback(
    (item: AdminKitesimPhoneItem) => {
      Modal.confirm({
        cancelText: t("Cancel"),
        content: t("Confirm delete Kitesim phone row", {
          value: item.phoneNumber || item.account,
        }),
        okButtonProps: { type: "danger" },
        okText: t("Delete"),
        onOk: () => runLifecycle([item], "delete", item),
        title: t("Confirm delete"),
      });
    },
    [runLifecycle, t],
  );

  const confirmDisableSelected = useCallback(() => {
    const count = phoneIDs(selectedItems).length;
    if (count === 0) return;
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm disable selected Kitesim phones", { count }),
      okText: t("Disable"),
      onOk: () => runLifecycle(selectedItems, "disable"),
      title: t("Disable"),
    });
  }, [runLifecycle, selectedItems, t]);

  const confirmDeleteSelected = useCallback(() => {
    if (selectedItems.length === 0) return;
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm delete selected Kitesim phone rows", { count: selectedItems.length }),
      okButtonProps: { type: "danger" },
      okText: t("Delete"),
      onOk: () => runLifecycle(selectedItems, "delete"),
      title: t("Confirm delete selected"),
    });
  }, [runLifecycle, selectedItems, t]);

  const openDetail = useCallback((item: AdminKitesimPhoneItem, tab: DetailTab = "basic") => {
    setDetailTab(tab);
    setDetail(item);
  }, []);

  useSelectionNotification({
    onClear: () => setSelectedKeys([]),
    onDelete: canOperate ? confirmDeleteSelected : undefined,
    onSell: canOperate && phoneIDs(selectedItems).length > 0 ? confirmDisableSelected : undefined,
    deleteLoading: bulkMutation === "delete",
    selectedCount: selectedKeys.length,
    selectionDescriptionKey: "Selected Kitesim phone numbers",
    sellLabelKey: "Disable",
    sellLoading: bulkMutation === "disable",
    t,
  });

  const renderRowActions = useCallback(
    (item: AdminKitesimPhoneItem) => {
      const syncing = syncingAccountIds.includes(item.accountId);
      const lifecycleAction = rowMutation?.key === rowKey(item) ? rowMutation.action : null;
      const busy = syncing || Boolean(lifecycleAction);
      return (
        <Space spacing={4} wrap={false}>
          <Button disabled={busy} onClick={() => openDetail(item)} size="small" type="tertiary">
            {t("Details")}
          </Button>
          <Button disabled={!canReadMessages || !item.phoneId || busy} onClick={() => openDetail(item, "mails")} size="small" type="tertiary">
            {t("Inbox")}
          </Button>
          <Button disabled={!canRenew || !item.phoneId || busy || (item.status !== "active" && item.status !== "expired")} onClick={() => setRenewalItem(item)} size="small" type="tertiary">
            {t("Renew")}
          </Button>
          <Button disabled={!canOperate || busy} loading={syncing} onClick={() => void queueSync([item])} size="small" type="primary">
            {t("Synchronize")}
          </Button>
          <Button
            disabled={!canOperate || !item.phoneId || busy}
            loading={lifecycleAction === "disable" || lifecycleAction === "enable"}
            onClick={() => void runLifecycle([item], item.status === "disabled" ? "enable" : "disable", item)}
            size="small"
            type="tertiary"
          >
            {item.status === "disabled" ? t("Enable") : t("Disable")}
          </Button>
          <Button
            disabled={!canOperate || busy}
            loading={lifecycleAction === "delete"}
            onClick={() => confirmDelete(item)}
            size="small"
            type="danger"
          >
            {t("Delete")}
          </Button>
        </Space>
      );
    },
    [canOperate, canReadMessages, canRenew, confirmDelete, openDetail, queueSync, rowMutation, runLifecycle, syncingAccountIds, t],
  );

  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "countryCode",
          key: "country",
          title: t("Country"),
          width: 120,
          render: (value: unknown) => (
            <Tag color="white" shape="circle">
              {String(value || "-")}
            </Tag>
          ),
        },
        {
          dataIndex: "phoneNumber",
          key: "phone",
          title: t("Phone number"),
          width: 280,
          render: (value: unknown) =>
            value ? <CopyableTableText copiedText={t("Copied")} copyContent={phoneCopyContent(String(value))} text={String(value)} /> : "-",
        },
        {
          dataIndex: "linkedAccountCount",
          key: "linkedAccounts",
          title: t("Linked accounts"),
          width: 130,
          render: (value: unknown) => String(value ?? 0),
        },
        {
          dataIndex: "account",
          key: "account",
          title: t("Platform account"),
          width: 310,
          render: (value: unknown) => (
            <CopyableTableText copiedText={t("Copied")} text={String(value)} />
          ),
        },
        {
          dataIndex: "status",
          key: "status",
          title: t("Status"),
          width: 120,
          render: (value: unknown, item: AdminKitesimPhoneItem) =>
            statusTag(value as AdminKitesimPhoneStatus, item.lastSafeError, t),
        },
        {
          dataIndex: "autoRenew",
          key: "autoRenew",
          title: t("Auto renew"),
          width: 100,
          render: (value: unknown) => booleanTag(Boolean(value), t),
        },
        {
          dataIndex: "tokenAvailable",
          key: "token",
          title: t("Token available"),
          width: 120,
          render: (value: unknown) => booleanTag(Boolean(value), t),
        },
        {
          dataIndex: "syncStatus",
          key: "sync",
          title: t("Task status"),
          width: 120,
          render: (value: unknown) => syncTag(value as AdminKitesimSyncTaskStatus, t),
        },
        {
          dataIndex: "expireTime",
          key: "expires",
          title: t("Expires at"),
          width: 180,
          render: (value: unknown, item: AdminKitesimPhoneItem) => (
            <span
              className={`whitespace-nowrap text-sm font-medium tabular-nums ${
                item.status === "expired"
                  ? "text-[var(--semi-color-danger)]"
                  : "text-[var(--semi-color-text-1)]"
              }`}
            >
              {formatTime(String(value || ""))}
            </span>
          ),
        },
        {
          dataIndex: "operate",
          fixed: "right",
          key: "operate",
          title: t("Action"),
          width: 360,
          render: (_: unknown, item: AdminKitesimPhoneItem) => renderRowActions(item),
        },
      ] as any[],
    [renderRowActions, t],
  );

  const tableColumns = useMemo(() => {
    if (!compactMode) return columns;
    return columns.map((column) => {
      if (column.dataIndex !== "operate") return column;
      const { fixed: _fixed, ...rest } = column;
      return rest;
    });
  }, [columns, compactMode]);

  const rowSelection = {
    selectedRowKeys: selectedKeys,
    onChange: (keys: Array<string | number>) => setSelectedKeys(keys.map(String)),
  };

  const tabsArea = (
    <Tabs
      activeKey={activeStatus}
      className="mb-2"
      collapsible
      onChange={(key) => selectStatus(key as StatusFilter)}
      type="card"
    >
      <Tabs.TabPane
        itemKey="all"
        tab={
          <span className="flex items-center gap-2">
            {t("All")}
            <Tag color={activeStatus === "all" ? "red" : "grey"} shape="circle">
              {stats.all}
            </Tag>
          </span>
        }
      />
      {STATUS_ORDER.map((status) => (
        <Tabs.TabPane
          itemKey={status}
          key={status}
          tab={
            <span className="flex items-center gap-2">
              <Smartphone size={14} />
              {t(STATUS_META[status].label)}
              <Tag color={activeStatus === status ? "red" : "grey"} shape="circle">
                {stats[status]}
              </Tag>
            </span>
          }
        />
      ))}
    </Tabs>
  );

  const activeFilterCount =
    Number(activeStatus !== "all") +
    Number(autoRenewFilter !== "all") +
    Number(tokenFilter !== "all") +
    Number(syncFilter !== "all") +
    Number(phoneFilter !== "all");

  const actionsArea = (
    <div className="flex w-full flex-col items-center justify-between gap-2 md:flex-row">
      <div className="order-2 flex w-full flex-wrap gap-2 md:order-1 md:w-auto">
        <Button className="flex-1 md:flex-initial" disabled={!canWrite} onClick={() => setImportOpen(true)} size="small" type="primary">
          {t("Import")}
        </Button>
        <Button className="remail-toolbar-fixed-button flex-1 md:flex-none" loading={loading} onClick={() => void refresh()} size="small" type="tertiary">
          {t("Refresh")}
        </Button>
        <Tooltip content={t("Synchronize current page")} mouseEnterDelay={0} mouseLeaveDelay={0.05} position="top">
          <Button className="flex-1 md:flex-initial" disabled={!canOperate || pagedItems.length === 0} loading={syncingAccountIds.length > 0} onClick={() => void queueSync(pagedItems)} size="small" type="tertiary">
            {t("Synchronize")}
          </Button>
        </Tooltip>
        <Tooltip content={t("Disable selected Kitesim phones")} mouseEnterDelay={0} mouseLeaveDelay={0.05} position="top">
          <Button className="flex-1 md:flex-initial" disabled={!canOperate || phoneIDs(selectedItems).length === 0} loading={bulkMutation === "disable"} onClick={confirmDisableSelected} size="small" type="tertiary">
            {t("Disable")}
          </Button>
        </Tooltip>
        <Tooltip content={t("Delete selected Kitesim phone rows")} mouseEnterDelay={0} mouseLeaveDelay={0.05} position="top">
          <Button className="flex-1 md:flex-initial" disabled={!canOperate || selectedItems.length === 0} loading={bulkMutation === "delete"} onClick={confirmDeleteSelected} size="small" type="danger">
            {t("Delete")}
          </Button>
        </Tooltip>
        <CompactModeToggle compactMode={compactMode} setCompactMode={setCompactMode} t={t} />
      </div>

      <div className="order-1 flex w-full flex-col items-center gap-2 md:order-2 md:w-auto md:flex-row">
        <Dropdown
          position="bottomRight"
          render={
            <div className="max-h-[70vh] w-[280px] overflow-auto p-2">
              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">{t("Status")}</div>
              <div className="mb-2 space-y-1">
                {(["all", ...STATUS_ORDER] as StatusFilter[]).map((value) => (
                  <StatisticFilterOption
                    active={activeStatus === value}
                    count={value === "all" ? stats.all : stats[value]}
                    key={value}
                    label={t(value === "all" ? "All" : STATUS_META[value].label)}
                    onSelect={selectStatus}
                    value={value}
                  />
                ))}
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">{t("Auto renew")}</div>
              <div className="mb-2 space-y-1">
                {(["all", "yes", "no"] as BooleanFilter[]).map((value) => (
                  <StatisticFilterOption
                    active={autoRenewFilter === value}
                    count={stats.autoRenew[value]}
                    key={value}
                    label={t(value === "all" ? "All" : value === "yes" ? "Yes" : "No")}
                    onSelect={(next) => { setAutoRenewFilter(next); resetPageAndSelection(); }}
                    value={value}
                  />
                ))}
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">{t("Token available")}</div>
              <div className="mb-2 space-y-1">
                {(["all", "yes", "no"] as BooleanFilter[]).map((value) => (
                  <StatisticFilterOption
                    active={tokenFilter === value}
                    count={stats.tokenAvailable[value]}
                    key={value}
                    label={t(value === "all" ? "All" : value === "yes" ? "Yes" : "No")}
                    onSelect={(next) => { setTokenFilter(next); resetPageAndSelection(); }}
                    value={value}
                  />
                ))}
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">{t("Sync healthy")}</div>
              <div className="mb-2 space-y-1">
                {(["all", "yes", "no"] as BooleanFilter[]).map((value) => (
                  <StatisticFilterOption
                    active={syncFilter === value}
                    count={stats.syncHealthy[value]}
                    key={value}
                    label={t(value === "all" ? "All" : value === "yes" ? "Yes" : "No")}
                    onSelect={(next) => { setSyncFilter(next); resetPageAndSelection(); }}
                    value={value}
                  />
                ))}
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">{t("Phone available")}</div>
              <div className="mb-2 space-y-1">
                {(["all", "yes", "no"] as BooleanFilter[]).map((value) => (
                  <StatisticFilterOption
                    active={phoneFilter === value}
                    count={stats.phoneAvailable[value]}
                    key={value}
                    label={t(value === "all" ? "All" : value === "yes" ? "Yes" : "No")}
                    onSelect={(next) => { setPhoneFilter(next); resetPageAndSelection(); }}
                    value={value}
                  />
                ))}
              </div>
            </div>
          }
          trigger="click"
        >
          <Button className="flex-1 md:flex-initial" icon={<SlidersHorizontal size={14} />} size="small" type="tertiary">
            {activeFilterCount > 0 ? `${t("Filters")} (${activeFilterCount})` : t("Filters")}
          </Button>
        </Dropdown>

        <Input
          className="resources-search-input w-full md:w-56"
          onChange={(value) => { setSearchKeyword(String(value)); resetPageAndSelection(); }}
          placeholder={t("Search account or phone")}
          prefix={<IconSearch />}
          showClear
          size="small"
          style={{ width: isMobile ? "100%" : 224 }}
          value={searchKeyword}
        />

        <DatePicker
          dropdownClassName={DATE_RANGE_DROPDOWN_CLASS}
          format="yyyy-MM-dd HH:mm:ss"
          onChange={(value) => { setCreatedAtRange(normalizeDateRangeValue(value)); resetPageAndSelection(); }}
          placeholder={[t("Start time"), t("End time")]}
          presetPosition="bottom"
          presets={dateRangePresets}
          showClear
          size="small"
          style={{ width: isMobile ? "100%" : 380 }}
          type="dateTimeRange"
          value={createdAtRange}
        />

        <div className="flex w-full gap-2 md:w-auto">
          <Button className="remail-toolbar-fixed-button flex-1 md:flex-none" loading={loading} onClick={() => { flushSearchKeyword(); setActivePage(1); }} size="small" type="tertiary">
            {t("Query")}
          </Button>
          <Button className="flex-1 md:flex-initial" onClick={resetFilters} size="small" type="tertiary">
            {t("Reset")}
          </Button>
        </div>
      </div>
    </div>
  );

  const paginationArea = createCardProPagination({
    currentPage: safePage,
    isMobile,
    onPageChange: (page) => { setActivePage(page); setSelectedKeys([]); },
    onPageSizeChange: (size) => { setPageSize(size); setActivePage(1); setSelectedKeys([]); },
    pageSize,
    total,
    t,
  });

  return (
    <div className="console-content-width py-5">
      <CardPro actionsArea={actionsArea} paginationArea={paginationArea} t={t} tabsArea={tabsArea} type="type3">
        <CardTable
          className="overflow-hidden rounded-xl"
          columns={tableColumns}
          dataSource={pagedItems}
          empty={
            <Empty
              darkModeImage={<IllustrationNoResultDark style={{ height: 150, width: 150 }} />}
              description={t("No Kitesim phone numbers found")}
              image={<IllustrationNoResult style={{ height: 150, width: 150 }} />}
              style={{ padding: 30 }}
            />
          }
          hidePagination
          loading={loading}
          pagination={false}
          rowKey={rowKey}
          rowSelection={rowSelection}
          scroll={{ x: "max(100%, 1960px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <ImportKitesimModal
        onCancel={() => setImportOpen(false)}
        onImported={async () => { setActivePage(1); setSelectedKeys([]); await refresh(); }}
        visible={importOpen && canWrite}
      />

      <KitesimRenewalModal
        item={canRenew ? renewalItem : null}
        onCancel={() => setRenewalItem(null)}
        onQueued={refresh}
      />

      <KitesimDetailSheet
        busy={detail ? syncingAccountIds.includes(detail.accountId) || rowMutation?.key === rowKey(detail) : false}
        canOperate={canOperate}
        canReadMessages={canReadMessages}
        initialTab={detailTab}
        item={detail}
        onCancel={() => setDetail(null)}
        onDelete={confirmDelete}
        onRefresh={refresh}
        onSync={async (item) => { await queueSync([item]); }}
        onToggleDisabled={async (item) => {
          await runLifecycle([item], item.status === "disabled" ? "enable" : "disable", item);
        }}
      />
    </div>
  );
}
