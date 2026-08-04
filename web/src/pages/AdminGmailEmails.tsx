import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  Input,
  Modal,
  Select,
  Space,
  Tag,
  TextArea,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import { FileText, Mail, RefreshCw, Upload } from "lucide-react";
import { useTranslation } from "react-i18next";

import { CardPro } from "@/components/semi/card-pro";
import { createCardProPagination } from "@/components/semi/card-pro-pagination";
import {
  CardTable,
  DESKTOP_TABLE_SCROLL_Y,
} from "@/components/semi/card-table";
import { CopyableTableText } from "@/components/semi/copyable-table-text";
import {
  hasPermissionKey,
  permissionKey,
  useAuth,
} from "@/context/auth-provider";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import {
  importAdminGmailResources,
  listAdminGmailOwners,
  listAdminGmailResources,
  setAdminGmailResourceEnabled,
  type AdminGmailImportErrorStrategy,
  type AdminGmailOwner,
  type AdminGmailResourceItem,
  type AdminGmailResourceList,
  type AdminGmailResourceStatus,
} from "@/lib/admin-gmail-api";
import { getIamErrorMessage } from "@/lib/iam-errors";

const { Text, Title } = Typography;
type StatusFilter = "all" | AdminGmailResourceStatus;

const statusMeta: Record<
  AdminGmailResourceStatus,
  { color: "green" | "grey" | "orange" | "blue"; label: string }
> = {
  available: { color: "green", label: "Available" },
  pending: { color: "blue", label: "Pending" },
  validating: { color: "orange", label: "Validating" },
  normal: { color: "green", label: "Normal" },
  abnormal: { color: "orange", label: "Abnormal" },
  disabled: { color: "grey", label: "Disabled" },
  leased: { color: "orange", label: "Leased" },
  sold: { color: "blue", label: "Sold" },
};

function formatTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString();
}

function ConfiguredTag({ configured }: { configured: boolean }) {
  const { t } = useTranslation();
  return (
    <Tag color={configured ? "green" : "red"} shape="circle" size="small">
      {configured ? t("Configured") : t("Missing")}
    </Tag>
  );
}

function ImportGmailModal({
  onCancel,
  onImported,
  owners,
  visible,
}: {
  onCancel: () => void;
  onImported: () => Promise<void>;
  owners: AdminGmailOwner[];
  visible: boolean;
}) {
  const { t } = useTranslation();
  const [content, setContent] = useState("");
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [errorStrategy, setErrorStrategy] =
    useState<AdminGmailImportErrorStrategy>("skip");
  const [submitting, setSubmitting] = useState(false);
  const [fileName, setFileName] = useState("");
  const fileRef = useRef<HTMLInputElement | null>(null);
  const lineCount = useMemo(
    () => content.split(/\r?\n/).filter((line) => line.trim()).length,
    [content],
  );

  useEffect(() => {
    if (!visible) return;
    setContent("");
    setOwnerId(owners.find((owner) => owner.enabled)?.id ?? owners[0]?.id);
    setErrorStrategy("skip");
    setFileName("");
    if (fileRef.current) fileRef.current.value = "";
  }, [owners, visible]);

  const selectFile = async (file?: File) => {
    if (!file) return;
    try {
      setContent(await file.text());
      setFileName(file.name);
    } catch {
      Toast.error(t("Gmail import failed."));
    }
  };

  const submit = async () => {
    if (!ownerId) {
      Toast.warning(t("Please select an owner."));
      return;
    }
    if (!lineCount) {
      Toast.warning(t("Please enter Gmail accounts."));
      return;
    }
    setSubmitting(true);
    let result: Awaited<ReturnType<typeof importAdminGmailResources>>;
    try {
      result = await importAdminGmailResources({
        content,
        errorStrategy,
        ownerId,
      });
      if (result.status === "failed") {
        throw new Error(result.lastSafeError || "Gmail import failed.");
      }
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Gmail import failed."));
      setSubmitting(false);
      return;
    }
    Toast.success(
      t("Gmail accounts imported.", {
        count: result.imported,
      }),
    );
    if (result.skipped) {
      Toast.warning(
        t("Gmail import skipped entries.", {
          count: result.skipped,
        }),
      );
    }
    onCancel();
    try {
      await onImported();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Gmail resources load failed."));
    }
    setSubmitting(false);
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      centered
      confirmLoading={submitting}
      onCancel={onCancel}
      onOk={() => void submit()}
      okText={t("Import")}
      title={t("Import Gmail Accounts")}
      visible={visible}
      width="min(666px, calc(100vw - 32px))"
    >
      <div className="space-y-4 py-1">
        <Select
          disabled={owners.length === 0}
          onChange={(value) => setOwnerId(Number(value))}
          optionList={owners.map((owner) => ({
            disabled: !owner.enabled,
            label: `${owner.email} · ${owner.nickname} · ${owner.groupName}`,
            value: owner.id,
          }))}
          placeholder={t("Please select an owner.")}
          style={{ width: "100%" }}
          value={ownerId}
        />
        <div className="flex flex-wrap items-center gap-2">
          <Button icon={<FileText size={14} />} onClick={() => fileRef.current?.click()} type="tertiary">
            {t("TXT file")}
          </Button>
          <input
            accept=".txt,text/plain"
            className="hidden"
            onChange={(event) => void selectFile(event.target.files?.[0])}
            ref={fileRef}
            type="file"
          />
          <Text size="small" type="tertiary">{fileName || t("Manual input")}</Text>
        </div>
        <TextArea
          className="font-mono"
          onChange={setContent}
          placeholder="email@gmail.com----password----2FA----app-password"
          rows={9}
          style={{ resize: "none" }}
          value={content}
        />
        <div className="flex flex-wrap items-center justify-between gap-2">
          <Text size="small" type="tertiary">
            {t("Parsed entries", { count: lineCount })}
          </Text>
          <Select
            onChange={(value) => setErrorStrategy(String(value) as "skip" | "abort")}
            optionList={[
              { label: t("Skip errors"), value: "skip" },
              { label: t("Abort on error"), value: "abort" },
            ]}
            style={{ width: 150 }}
            value={errorStrategy}
          />
        </div>
        <div className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
          <Text strong>{t("Supported format")}</Text>
          <pre className="mt-2 overflow-x-auto font-mono text-xs text-[var(--semi-color-text-2)]">
            email@gmail.com----password----2FA----app-password
          </pre>
          <Text size="small" type="tertiary">
            {t("Gmail credentials are write-only and never returned by the resource API.")}
          </Text>
        </div>
      </div>
    </Modal>
  );
}

export default function AdminGmailEmails() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const isMobile = useIsMobile();
  const [pageSize, setPageSize] = useSharedPageSize();
  const [activePage, setActivePage] = useState(1);
  const [search, setSearch] = useState("");
  const [debouncedSearch, flushSearch] = useDebouncedValue(search);
  const [status, setStatus] = useState<StatusFilter>("all");
  const [response, setResponse] = useState<AdminGmailResourceList | null>(null);
  const [owners, setOwners] = useState<AdminGmailOwner[]>([]);
  const [loading, setLoading] = useState(true);
  const [importVisible, setImportVisible] = useState(false);
  const [busyId, setBusyId] = useState<number | null>(null);
  const listRequestRef = useRef<AbortController | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    void listAdminGmailOwners("", controller.signal)
      .then((items) => {
        if (!controller.signal.aborted) setOwners(items);
      })
      .catch(() => {
        // The resource list remains usable if owner choices cannot be loaded.
      });
    return () => controller.abort();
  }, []);

  const canWrite = hasPermissionKey(
    currentUser,
    permissionKey("core:resource", "write"),
  );
  const canOperate = hasPermissionKey(
    currentUser,
    permissionKey("core:resource", "operate"),
  );

  const load = useCallback(async () => {
    listRequestRef.current?.abort();
    const controller = new AbortController();
    listRequestRef.current = controller;
    setLoading(true);
    try {
      const next = await listAdminGmailResources({
        limit: pageSize,
        offset: (activePage - 1) * pageSize,
        search: debouncedSearch.trim() || undefined,
        signal: controller.signal,
        status: status === "all" ? undefined : status,
      });
      if (controller.signal.aborted) return;
      setResponse(next);
      const lastPage = Math.max(1, Math.ceil(next.total / pageSize));
      if (activePage > lastPage) setActivePage(lastPage);
    } catch (error) {
      if (controller.signal.aborted) return;
      Toast.error(getIamErrorMessage(t, error, "Gmail resources load failed."));
    } finally {
      if (listRequestRef.current === controller) {
        listRequestRef.current = null;
        setLoading(false);
      }
    }
  }, [activePage, debouncedSearch, pageSize, status, t]);

  useEffect(() => {
    void load();
    return () => listRequestRef.current?.abort();
  }, [load]);

  useEffect(() => setActivePage(1), [debouncedSearch, pageSize, status]);

  const toggleResource = async (item: AdminGmailResourceItem) => {
    const enabled = item.status === "disabled";
    setBusyId(item.id);
    try {
      await setAdminGmailResourceEnabled(item.id, enabled);
      Toast.success(t(enabled ? "Gmail account enabled." : "Gmail account disabled."));
      await load();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Gmail resource operation failed."));
    } finally {
      setBusyId(null);
    }
  };

  const facets = response?.facets ?? {
    all: 0,
    available: 0,
    pending: 0,
    validating: 0,
    normal: 0,
    abnormal: 0,
    disabled: 0,
    leased: 0,
    sold: 0,
  };
  const statusOptions = useMemo(
    () => [
      { label: `${t("All")} (${facets.all})`, value: "all" },
      ...Object.entries(statusMeta).map(([value, meta]) => ({
        label: `${t(meta.label)} (${facets[value as AdminGmailResourceStatus]})`,
        value,
      })),
    ],
    [facets, t],
  );

  const columns = [
    {
      dataIndex: "email",
      title: t("Email"),
      width: 250,
      render: (value: unknown) => (
        <CopyableTableText copiedText={t("Copied")} text={String(value)} />
      ),
    },
    {
      dataIndex: "status",
      title: t("Status"),
      width: 120,
      render: (value: unknown) => {
        const meta = statusMeta[value as AdminGmailResourceStatus];
        return meta ? (
          <Tag color={meta.color} shape="circle">
            {t(meta.label)}
          </Tag>
        ) : (
          "-"
        );
      },
    },
    {
      dataIndex: "passwordConfigured",
      title: t("Password"),
      width: 110,
      render: (value: unknown) => <ConfiguredTag configured={Boolean(value)} />,
    },
    {
      dataIndex: "twoFactorConfigured",
      title: "2FA",
      width: 100,
      render: (value: unknown) => <ConfiguredTag configured={Boolean(value)} />,
    },
    {
      dataIndex: "appPasswordConfigured",
      title: t("App password"),
      width: 130,
      render: (value: unknown) => <ConfiguredTag configured={Boolean(value)} />,
    },
    {
      dataIndex: "lastCheckedAt",
      title: t("Last checked"),
      width: 180,
      render: (value: unknown) => formatTime(value ? String(value) : undefined),
    },
    {
      dataIndex: "createdAt",
      title: t("Created at"),
      width: 180,
      render: (value: unknown) => formatTime(String(value)),
    },
    {
      key: "actions",
      fixed: "right" as const,
      title: t("Actions"),
      width: 110,
      render: (_value: unknown, item: AdminGmailResourceItem) =>
        canOperate && item.status !== "leased" && item.status !== "sold" ? (
          <Button
            loading={busyId === item.id}
            onClick={() => void toggleResource(item)}
            size="small"
            type={item.status === "disabled" ? "primary" : "tertiary"}
          >
            {t(item.status === "disabled" ? "Enable" : "Disable")}
          </Button>
        ) : (
          <Text type="tertiary">-</Text>
        ),
    },
  ];

  return (
    <>
      <CardPro
        actionsArea={
          <div className="flex flex-wrap items-center justify-between gap-2">
            <Space wrap>
              <Button
                disabled={!canWrite}
                icon={<Upload size={14} />}
                onClick={() => setImportVisible(true)}
                theme="solid"
                type="primary"
              >
                {t("Import Gmail Accounts")}
              </Button>
              <Button icon={<RefreshCw size={14} />} loading={loading} onClick={() => void load()}>
                {t("Refresh")}
              </Button>
            </Space>
            <Text type="tertiary">
              {t("Gmail resource total", { count: response?.total ?? 0 })}
            </Text>
          </div>
        }
        descriptionArea={
          <div className="flex items-center gap-3">
            <div className="rounded-xl bg-[var(--semi-color-primary-light-default)] p-2 text-[var(--semi-color-primary)]">
              <Mail size={20} />
            </div>
            <div>
              <Title heading={4}>{t("Admin Gmail Emails")}</Title>
              <Text type="tertiary">
                {t("Manage local Gmail account inventory and write-only credentials.")}
              </Text>
            </div>
          </div>
        }
        paginationArea={createCardProPagination({
          currentPage: activePage,
          isMobile,
          onPageChange: setActivePage,
          onPageSizeChange: setPageSize,
          pageSize,
          t,
          total: response?.total ?? 0,
        })}
        searchArea={
          <div className="flex flex-col gap-2 sm:flex-row">
            <Input
              onChange={setSearch}
              onEnterPress={() => flushSearch(search)}
              placeholder={t("Search Gmail address")}
              prefix={<IconSearch />}
              showClear
              value={search}
            />
            <Select
              onChange={(value) => setStatus(String(value) as StatusFilter)}
              optionList={statusOptions}
              style={{ minWidth: 190 }}
              value={status}
            />
          </div>
        }
        t={t}
        type="type1"
      >
        <CardTable
          columns={columns}
          dataSource={response?.items ?? []}
          hidePagination
          loading={loading}
          pagination={false}
          rowKey="id"
          scroll={{ x: 1200, y: DESKTOP_TABLE_SCROLL_Y }}
        />
      </CardPro>

      <ImportGmailModal
        onCancel={() => setImportVisible(false)}
        onImported={load}
        owners={owners}
        visible={importVisible && canWrite}
      />
    </>
  );
}
