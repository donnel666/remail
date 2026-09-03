import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  Button,
  DatePicker,
  Dropdown,
  Empty,
  Input,
  Modal,
  SideSheet,
  Space,
  Spin,
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
import { History, ShieldCheck, SlidersHorizontal } from "lucide-react";
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
  AdminUserSelect,
  ownersWithCurrentUserFirst,
  type AdminUserSelectOption,
} from "@/components/semi/admin-user-select";
import {
  hasPermissionKey,
  permissionKey,
  useAuth,
} from "@/context/auth-provider";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import {
  batchAllMatchingAdminGmailResources,
  batchAdminGmailResourcesByIds,
  deleteAdminGmailResource,
  getAdminGmailResource,
  importAdminGmailResources,
  listAdminGmailAliases,
  listAdminGmailOwners,
  listAdminGmailResources,
  listAdminGmailTasks,
  recoverAdminGmailResource,
  replaceAdminGmailCredentials,
  scanAdminGmailResourceHistory,
  setAdminGmailResourceEnabled,
  setAdminGmailResourceForSale,
  updateAdminGmailResource,
  validateAdminGmailResource,
  type AdminGmailBatchAction,
  type AdminGmailBulkResponse,
  type AdminGmailImportErrorStrategy,
  type AdminGmailOwner,
  type AdminGmailResourceItem,
  type AdminGmailResourceList,
  type AdminGmailResourceListFilter,
  type AdminGmailResourceStatus,
  type AdminGmailResourceUpdateRequest,
  type AdminGmailTask,
  type AdminGmailTaskListResponse,
} from "@/lib/admin-gmail-api";
import { getIamErrorMessage } from "@/lib/iam-errors";

import {
  AliasPanel,
  RelatedOrdersTable,
  ResourceMailsPanel,
  ServerPaginatedDrawerTable,
} from "./admin-microsoft/microsoft-detail-sheet";
import {
  InfoItem,
  OwnerIdentity,
  renderTaskStatusTag,
  taskKindLabel,
} from "./admin-microsoft/microsoft-meta";
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
type StatusFilter = "all" | AdminGmailResourceStatus;
type BooleanFilter = "all" | "yes" | "no";
type RowAction = "toggle" | "publish" | "delete" | "recover";
type GmailMaintenanceAction = "validate" | "history";
type GmailMaintenanceTarget =
  | { item: AdminGmailResourceItem; mode: "row" }
  | { count: number; mode: "ids"; resourceIds: number[] }
  | {
      count: number;
      filter: AdminGmailResourceListFilter;
      mode: "filter";
    };
const IMPORT_ENTRY_AREA_HEIGHT = 208;

const statusMeta: Record<
  AdminGmailResourceStatus,
  { color: "green" | "grey" | "orange" | "blue" | "red"; label: string }
> = {
  pending: { color: "blue", label: "Pending" },
  validating: { color: "orange", label: "Validating" },
  identifying: { color: "blue", label: "Identifying" },
  normal: { color: "green", label: "Normal" },
  abnormal: { color: "orange", label: "Abnormal" },
  disabled: { color: "grey", label: "Disabled" },
  deleted: { color: "red", label: "Deleted" },
};

const EMPTY_FACETS: AdminGmailResourceList["facets"] = {
  all: 0,
  pending: 0,
  validating: 0,
  identifying: 0,
  normal: 0,
  abnormal: 0,
  disabled: 0,
  deleted: 0,
  forSale: { all: 0, yes: 0, no: 0 },
};

function listGmailAliasesForPanel(
  resourceId: number,
  _kind: "explicit" | "other",
  offset?: number,
  limit?: number,
  signal?: AbortSignal,
) {
  return listAdminGmailAliases(resourceId, offset, limit, signal);
}

function formatTime(value?: string | null) {
  if (!value) return "-";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "-" : date.toLocaleString();
}

function switchButtonClass(active: boolean) {
  return [
    "flex h-12 w-full items-center justify-center gap-2 rounded-lg border-2 px-4 text-sm font-semibold transition-all",
    active
      ? "border-[var(--semi-color-primary)] bg-[var(--semi-color-primary-light-default)] text-[var(--semi-color-primary)]"
      : "border-[var(--semi-color-border)] bg-[var(--semi-color-bg-2)] text-[var(--semi-color-text-1)] hover:border-[var(--semi-color-primary)] hover:bg-[var(--semi-color-fill-0)]",
  ].join(" ");
}

function ConfiguredTag({ configured }: { configured: boolean }) {
  const { t } = useTranslation();
  return (
    <Tag color={configured ? "green" : "grey"} shape="circle" size="small">
      {configured ? t("Configured") : t("Not configured")}
    </Tag>
  );
}

function ownerRoleLabel(role: AdminGmailOwner["role"]) {
  switch (role) {
    case "super_admin":
      return "Super Admin";
    case "admin":
      return "Admin";
    case "supplier":
      return "Supplier";
    default:
      return "User";
  }
}

function ownerOption(
  owner: AdminGmailOwner,
  t: ReturnType<typeof useTranslation>["t"],
): AdminUserSelectOption<AdminGmailOwner> {
  return {
    data: owner,
    disabled: !owner.enabled,
    label: `${owner.email} · ${owner.nickname} · ${t(ownerRoleLabel(owner.role))} · ${owner.groupName}`,
    value: owner.id,
  };
}

function OwnerSelect({
  onChange,
  owners,
  selectedOwner,
  t,
  value,
}: {
  onChange: (ownerId: number) => void;
  owners: AdminGmailOwner[];
  selectedOwner?: AdminGmailOwner;
  t: ReturnType<typeof useTranslation>["t"];
  value?: number;
}) {
  const options = useMemo(
    () => owners.map((owner) => ownerOption(owner, t)),
    [owners, t],
  );
  const selectedOption = useMemo(
    () => (selectedOwner ? ownerOption(selectedOwner, t) : undefined),
    [selectedOwner, t],
  );

  return (
    <AdminUserSelect
      emptyContent={t("No users found")}
      loadOptions={async (keyword) =>
        (await listAdminGmailOwners(keyword)).map((owner) =>
          ownerOption(owner, t)
        )
      }
      onChange={(ownerID) => {
        if (ownerID) onChange(ownerID);
      }}
      options={options}
      placeholder={t("Search user by email, nickname or ID")}
      selectedOption={selectedOption}
      style={{ width: "100%" }}
      value={value}
    />
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
  const previousVisible = useRef(false);
  const lineCount = useMemo(
    () => content.split(/\r?\n/).filter((line) => line.trim()).length,
    [content],
  );

  useEffect(() => {
    const opened = visible && !previousVisible.current;
    previousVisible.current = visible;
    if (!opened) return;
    setContent("");
    setOwnerId(undefined);
    setErrorStrategy("skip");
  }, [visible]);

  useEffect(() => {
    if (!visible || ownerId !== undefined) return;
    setOwnerId(owners.find((owner) => owner.enabled)?.id ?? owners[0]?.id);
  }, [ownerId, owners, visible]);

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
        <label className="block">
          <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
            {t("Owner")} *
          </span>
          <OwnerSelect
            onChange={setOwnerId}
            owners={owners}
            t={t}
            value={ownerId}
          />
        </label>

        <div className="grid grid-cols-2 gap-2">
          <button
            className={switchButtonClass(errorStrategy === "skip")}
            onClick={() => setErrorStrategy("skip")}
            type="button"
          >
            {t("Skip errors")}
          </button>
          <button
            className={switchButtonClass(errorStrategy === "abort")}
            onClick={() => setErrorStrategy("abort")}
            type="button"
          >
            {t("Abort on error")}
          </button>
        </div>

        <label className="block">
          <span className="mb-1.5 flex items-center justify-between text-sm font-medium text-[var(--semi-color-text-0)]">
            <span>{t("Gmail resource entries")} *</span>
            <Text size="small" type="tertiary">
              {t("Parsed entries", { count: lineCount })}
            </Text>
          </span>
          <TextArea
            className="font-mono"
            onChange={setContent}
            placeholder="email@gmail.com----app-password"
            rows={8}
            style={{ height: IMPORT_ENTRY_AREA_HEIGHT, resize: "none" }}
            value={content}
          />
        </label>

        <div className="rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
          <div className="mb-1 text-xs font-medium text-[var(--semi-color-text-0)]">
            {t("Supported format")}
          </div>
          <pre className="overflow-x-auto font-mono text-xs leading-relaxed text-[var(--semi-color-text-2)]">
            {`email@gmail.com----app-password
email@gmail.com----password----app-password
email@gmail.com----password----binding-email----app-password
email@gmail.com----password----2FA----app-password
email@gmail.com----password----binding-email----2FA----app-password`}
          </pre>
        </div>

        <div className="rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs leading-5 text-[var(--semi-color-text-2)]">
          {t(
            "Only the Gmail App Password is verified by IMAP. Spaces in the final App Password field are removed automatically; all credentials remain write-only.",
          )}
        </div>
      </div>
    </Modal>
  );
}

function isGmailAddress(value: string) {
  return /^[^+@\s]+@(gmail\.com|googlemail\.com)$/i.test(value);
}

function ResourceStatusTag({ item }: { item: AdminGmailResourceItem }) {
  const { t } = useTranslation();
  const meta = statusMeta[item.status];
  const tag = (
    <Tag color={meta.color} shape="circle" size="small">
      {t(meta.label)}
    </Tag>
  );
  return item.lastSafeError ? (
    <Tooltip content={item.lastSafeError}>{tag}</Tooltip>
  ) : (
    tag
  );
}

export function EditGmailModal({
  canOperate,
  canWrite,
  onCancel,
  onSaved,
  owners,
  target,
}: {
  canOperate: boolean;
  canWrite: boolean;
  onCancel: () => void;
  onSaved: () => void | Promise<void>;
  owners: AdminGmailOwner[];
  target: AdminGmailResourceItem | null;
}) {
  const { t } = useTranslation();
  const [email, setEmail] = useState("");
  const [bindingEmail, setBindingEmail] = useState("");
  const [ownerId, setOwnerId] = useState<number | undefined>();
  const [password, setPassword] = useState("");
  const [twoFactorSecret, setTwoFactorSecret] = useState("");
  const [appPassword, setAppPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!target) return;
    setEmail(target.email);
    setBindingEmail(target.bindingEmail ?? "");
    setOwnerId(target.owner.id);
    setPassword("");
    setTwoFactorSecret("");
    setAppPassword("");
  }, [target]);

  const submit = async () => {
    if (!target || (canWrite && !ownerId)) return;
    const nextEmail = email.trim().toLowerCase();
    const nextBindingEmail = bindingEmail.trim().toLowerCase();
    if (canWrite && !isGmailAddress(nextEmail)) {
      Toast.warning(t("A valid Gmail address is required."));
      return;
    }
    if (
      canWrite &&
      nextBindingEmail &&
      !/^\S+@\S+\.\S+$/.test(nextBindingEmail)
    ) {
      Toast.warning(t("A valid auxiliary mailbox address is required."));
      return;
    }
    const normalizedAppPassword = appPassword.replace(/\s/g, "");
    const credentialsChanged = canOperate && Boolean(
      password.trim() || twoFactorSecret.trim() || normalizedAppPassword,
    );
    const metadataChanged = canWrite && Boolean(
      nextEmail !== target.email ||
        nextBindingEmail !== (target.bindingEmail ?? "") ||
        ownerId !== target.owner.id,
    );
    if (!metadataChanged && !credentialsChanged) {
      Toast.info(t("No changes to save."));
      return;
    }
    if (!canWrite && !password.trim()) {
      Toast.warning(t("Gmail account password is required."));
      return;
    }

    setSubmitting(true);
    try {
      if (canWrite) {
        const request: AdminGmailResourceUpdateRequest = {
          bindingEmail: nextBindingEmail,
          email: nextEmail,
          ownerId: ownerId!,
          version: target.version,
        };
        if (credentialsChanged) {
          request.password = password;
          request.twoFactorSecret = twoFactorSecret.trim();
          request.appPassword = normalizedAppPassword;
        }
        await updateAdminGmailResource(target.id, request);
      } else {
        await replaceAdminGmailCredentials(target.id, {
          appPassword: normalizedAppPassword,
          password,
          twoFactorSecret: twoFactorSecret.trim(),
          version: target.version,
        });
      }
      Toast.success(t("Gmail resource updated."));
      await onSaved();
      onCancel();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Gmail resource update failed."));
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
      okText={t("Save")}
      title={t("Edit Gmail resource")}
      visible={Boolean(target)}
      width="min(680px, calc(100vw - 32px))"
    >
      {target ? (
        <div className="space-y-4 py-1">
          {canWrite ? (
            <>
              <div className="grid gap-3 sm:grid-cols-2">
                <label className="block">
                  <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("Email")} *
                  </span>
                  <Input
                    className="font-mono"
                    onChange={(value) => setEmail(String(value))}
                    placeholder="name@gmail.com"
                    value={email}
                  />
                </label>
                <label className="block">
                  <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                    {t("Binding email")}
                  </span>
                  <Input
                    className="font-mono"
                    onChange={(value) => setBindingEmail(String(value))}
                    placeholder={t("Optional recovery mailbox")}
                    value={bindingEmail}
                  />
                </label>
              </div>

              <label className="block">
                <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                  {t("Owner")}
                </span>
                <OwnerSelect
                  onChange={setOwnerId}
                  owners={owners}
                  selectedOwner={target.owner}
                  t={t}
                  value={ownerId}
                />
              </label>
            </>
          ) : null}

          {canOperate ? (
            <div className="space-y-3 rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-3">
              <div className="text-sm font-medium text-[var(--semi-color-text-0)]">
                {t("Credentials")}
              </div>
              <div className="rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">
                {t(
                  "Credential values are write-only. Leave a field blank to keep its current value; spaces in App Password are removed automatically.",
                )}
              </div>
              <label className="block">
                <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                  {t("Password")}
                  {!canWrite ? " *" : ""}
                </span>
                <Input
                  autoComplete="new-password"
                  mode="password"
                  onChange={(value) => setPassword(String(value))}
                  placeholder={t(
                    canWrite
                      ? "Leave blank to keep current"
                      : "Enter a replacement password",
                  )}
                  value={password}
                />
              </label>
              <label className="block">
                <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                  2FA
                </span>
                <Input
                  autoComplete="off"
                  className="font-mono"
                  mode="password"
                  onChange={(value) => setTwoFactorSecret(String(value))}
                  placeholder={t("Leave blank to keep current")}
                  value={twoFactorSecret}
                />
              </label>
              <label className="block">
                <span className="mb-1.5 block text-sm font-medium text-[var(--semi-color-text-0)]">
                  {t("App password")}
                </span>
                <Input
                  autoComplete="off"
                  className="font-mono"
                  mode="password"
                  onChange={(value) => setAppPassword(String(value))}
                  placeholder={t("Leave blank to keep current")}
                  value={appPassword}
                />
              </label>
            </div>
          ) : null}
        </div>
      ) : null}
    </Modal>
  );
}

type GmailMaintenanceStatus = AdminGmailTask["status"] | "idle" | "unavailable";

function gmailValidationStatus(
  item: AdminGmailResourceItem,
): GmailMaintenanceStatus {
  switch (item.status) {
    case "pending":
      return "queued";
    case "validating":
      return "running";
    case "identifying":
    case "normal":
      return "succeeded";
    case "abnormal":
      return "failed";
    default:
      return "unavailable";
  }
}

function GmailMaintenanceStatusTag({
  status,
}: {
  status: GmailMaintenanceStatus;
}) {
  const { t } = useTranslation();
  if (status === "idle" || status === "unavailable") {
    return (
      <Tag color="grey" shape="circle" size="small">
        {t(status === "idle" ? "Idle" : "Unavailable")}
      </Tag>
    );
  }
  return renderTaskStatusTag(status, t);
}

function GmailMaintenanceModal({
  canReadTasks,
  onCancel,
  onCompleted,
  target,
}: {
  canReadTasks: boolean;
  onCancel: () => void;
  onCompleted: () => void | Promise<void>;
  target: GmailMaintenanceTarget | null;
}) {
  const { t } = useTranslation();
  const [selected, setSelected] =
    useState<GmailMaintenanceAction>("validate");
  const [tasks, setTasks] = useState<AdminGmailTask[]>([]);
  const [loadingTasks, setLoadingTasks] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!target) return;
    setSelected(
      target.mode === "row" && target.item.status === "identifying"
        ? "history"
        : "validate",
    );
    setTasks([]);
    setLoadingTasks(false);
    if (target.mode !== "row" || !canReadTasks) return;
    const controller = new AbortController();
    setLoadingTasks(true);
    void listAdminGmailTasks(target.item.id, 0, 100, controller.signal)
      .then((response) => {
        if (!controller.signal.aborted) setTasks(response.items);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          Toast.error(
            getIamErrorMessage(t, error, "Gmail task load failed."),
          );
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoadingTasks(false);
      });
    return () => controller.abort();
  }, [canReadTasks, t, target]);

  const latest = useMemo(() => {
    const result = new Map<AdminGmailTask["kind"], AdminGmailTask>();
    for (const task of tasks) {
      if (!result.has(task.kind)) result.set(task.kind, task);
    }
    return result;
  }, [tasks]);

  if (!target) return null;

  const actions = [
    {
      description:
        "Re-run Gmail App Password IMAP validation, then identify projects.",
      disabled:
        target.mode === "row" &&
        (target.item.status === "disabled" || target.item.status === "deleted"),
      disabledReason: "Enable the resource before validation.",
      icon: ShieldCheck,
      key: "validate" as const,
      label: "Validate resource",
      status:
        target.mode === "row"
          ? gmailValidationStatus(target.item)
          : ("idle" as const),
      updatedAt: target.mode === "row" ? target.item.updatedAt : undefined,
    },
    {
      description:
        "Scan Gmail history and restore existing project relationships.",
      disabled:
        target.mode === "row" &&
        ((target.item.status !== "normal" &&
          target.item.status !== "identifying") ||
          !target.item.appPasswordConfigured),
      disabledReason:
        "Project scanning requires a normal resource with an App Password.",
      icon: History,
      key: "history" as const,
      label: "Scan projects",
      status:
        target.mode === "row"
          ? latest.get("history")?.status ?? "idle"
          : ("idle" as const),
      updatedAt:
        target.mode === "row" ? latest.get("history")?.updatedAt : undefined,
    },
  ];
  const selectedAction =
    actions.find((item) => item.key === selected) ?? actions[0];

  const submit = async () => {
    if (selectedAction.disabled) return;
    setSubmitting(true);
    try {
      let response: AdminGmailBulkResponse | undefined;
      if (target.mode === "row") {
        if (selected === "validate") {
          await validateAdminGmailResource(target.item.id);
        } else {
          await scanAdminGmailResourceHistory(target.item.id);
        }
      } else {
        response =
          target.mode === "ids"
            ? await batchAdminGmailResourcesByIds(
                selected,
                target.resourceIds,
              )
            : await batchAllMatchingAdminGmailResources(
                selected,
                target.filter,
              );
      }
      const count = target.mode === "row" ? 1 : response?.affected ?? 0;
      Toast.success(
        t(
          selected === "validate"
            ? "Resource validation batch submitted."
            : "Project scan batch submitted.",
          { count },
        ),
      );
      if (response?.skipped) {
        const reasons = response.reasonCounts
          .map((item) => `${item.reason}: ${item.count}`)
          .join(", ");
        Toast.warning(
          `${t("Succeeded")}: ${response.affected}/${response.requested}` +
            (reasons ? ` · ${t("Reason")}: ${reasons}` : ""),
        );
      }
      await onCompleted();
      onCancel();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Gmail resource operation failed."));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      centered
      confirmLoading={submitting}
      okButtonProps={{ disabled: selectedAction.disabled }}
      okText={t("Submit maintenance task")}
      onCancel={onCancel}
      onOk={() => void submit()}
      title={t("Gmail resource maintenance")}
      visible
      width={680}
    >
      <div className="space-y-4 py-1">
        <div className="flex items-center justify-between gap-3 rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2">
          <div>
            <div className="text-xs text-[var(--semi-color-text-2)]">
              {t(target.mode === "row" ? "Resource" : "Scope")}
            </div>
            <div className="mt-1 break-all text-sm font-medium text-[var(--semi-color-text-0)]">
              {target.mode === "row"
                ? target.item.email
                : t(
                    target.mode === "ids"
                      ? "Selected Gmail resources"
                      : "Matching resources",
                    { count: target.count },
                  )}
            </div>
          </div>
          {target.mode === "row" ? null : (
            <Tag color="blue" shape="circle">
              {target.count}
            </Tag>
          )}
        </div>

        <div className="flex items-center justify-between gap-3">
          <div className="text-sm leading-6 text-[var(--semi-color-text-1)]">
            {t(
              "Choose one maintenance operation. Ineligible resources will be skipped and counted by the server.",
            )}
          </div>
          {loadingTasks ? <Spin size="small" /> : null}
        </div>

        <div className="grid gap-3 sm:grid-cols-2">
          {actions.map((item) => {
            const Icon = item.icon;
            const active = selected === item.key;
            return (
              <button
                aria-pressed={active}
                className={`min-h-32 rounded-xl border p-4 text-left transition-colors ${
                  active
                    ? "border-[var(--semi-color-primary)] bg-[var(--semi-color-primary-light-default)]"
                    : "border-[var(--semi-color-border)] bg-[var(--semi-color-bg-2)] hover:border-[var(--semi-color-primary)] hover:bg-[var(--semi-color-fill-0)]"
                } ${item.disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer"}`}
                disabled={submitting || item.disabled}
                key={item.key}
                onClick={() => setSelected(item.key)}
                type="button"
              >
                <div className="flex items-start gap-3">
                  <span className="rounded-lg bg-[var(--semi-color-fill-0)] p-2 text-[var(--semi-color-primary)]">
                    <Icon aria-hidden size={20} />
                  </span>
                  <span className="min-w-0 flex-1">
                    <span className="flex flex-wrap items-center justify-between gap-2">
                      <span className="font-semibold text-[var(--semi-color-text-0)]">
                        {t(item.label)}
                      </span>
                      <GmailMaintenanceStatusTag status={item.status} />
                    </span>
                    <span className="mt-1.5 block text-xs leading-5 text-[var(--semi-color-text-2)]">
                      {t(
                        item.disabled
                          ? item.disabledReason
                          : item.description,
                      )}
                    </span>
                    {item.updatedAt ? (
                      <span className="mt-2 block text-xs text-[var(--semi-color-text-3)]">
                        {t("Last updated")}: {formatTime(item.updatedAt)}
                      </span>
                    ) : null}
                  </span>
                </div>
              </button>
            );
          })}
        </div>
      </div>
    </Modal>
  );
}

type GmailTaskActionKey = "history" | "validate";

function GmailTaskDiagnostics({
  canOperate,
  item,
  onRefresh,
}: {
  canOperate: boolean;
  item: AdminGmailResourceItem;
  onRefresh: () => void | Promise<void>;
}) {
  const { t } = useTranslation();
  const [busy, setBusy] = useState<GmailTaskActionKey | null>(null);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [page, setPage] = useState(1);
  const [refreshKey, setRefreshKey] = useState(0);
  const [loading, setLoading] = useState(true);
  const [response, setResponse] = useState<AdminGmailTaskListResponse>({
    items: [],
    limit: pageSize,
    offset: 0,
    succeeded: 0,
    total: 0,
  });

  useEffect(() => setPage(1), [item.id, pageSize]);
  useEffect(() => {
    const controller = new AbortController();
    let pollTimer: ReturnType<typeof globalThis.setTimeout> | null = null;
    setLoading(true);
    void listAdminGmailTasks(
      item.id,
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
        if (
          next.items.some(
            (task) => task.status === "queued" || task.status === "running",
          )
        ) {
          pollTimer = globalThis.setTimeout(() => {
            setRefreshKey((value) => value + 1);
          }, 1_500);
        }
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          Toast.error(
            getIamErrorMessage(t, error, "Gmail task load failed."),
          );
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => {
      controller.abort();
      if (pollTimer) globalThis.clearTimeout(pollTimer);
    };
  }, [item.id, page, pageSize, refreshKey, t]);

  const total = response.total;
  const succeeded = response.succeeded;
  const successRate = total > 0 ? Math.round((succeeded / total) * 100) : 0;

  const runAction = async (
    key: GmailTaskActionKey,
    action: (resourceId: number) => Promise<unknown>,
    successKey: string,
  ) => {
    setBusy(key);
    try {
      await action(item.id);
      Toast.success(t(successKey));
      setPage(1);
      setRefreshKey((value) => value + 1);
      await onRefresh();
    } catch (error) {
      Toast.error(
        getIamErrorMessage(t, error, "Gmail resource operation failed."),
      );
    } finally {
      setBusy(null);
    }
  };

  const columns = useMemo(
    () => [
      {
        dataIndex: "kind",
        title: t("Type"),
        width: 140,
        render: (value: unknown) =>
          t(taskKindLabel(value as AdminGmailTask["kind"])),
      },
      {
        dataIndex: "status",
        title: t("Status"),
        width: 110,
        render: (value: unknown) =>
          renderTaskStatusTag(value as AdminGmailTask["status"], t),
      },
      {
        dataIndex: "remainingAttempts",
        title: t("Remaining attempts"),
        width: 120,
        render: (_value: unknown, task: AdminGmailTask) => (
          <span className="font-mono tabular-nums">
            {task.remainingAttempts}
          </span>
        ),
      },
      {
        dataIndex: "queuedAt",
        title: t("Queued at"),
        width: 170,
        render: (value: unknown) =>
          formatTime(value ? String(value) : undefined),
      },
      {
        dataIndex: "startedAt",
        title: t("Started at"),
        width: 170,
        render: (value: unknown) =>
          formatTime(value ? String(value) : undefined),
      },
      {
        dataIndex: "finishedAt",
        title: t("Finished at"),
        width: 170,
        render: (value: unknown) =>
          formatTime(value ? String(value) : undefined),
      },
      {
        dataIndex: "updatedAt",
        title: t("Updated at"),
        width: 170,
        render: (value: unknown) => formatTime(String(value)),
      },
    ],
    [t],
  );

  return (
    <div>
      <div className="mb-4">
        <div className="grid gap-3 sm:grid-cols-3">
          <InfoItem
            label={t("Total tasks")}
            value={<span className="font-mono tabular-nums">{total}</span>}
          />
          <InfoItem
            label={t("Succeeded tasks")}
            value={<span className="font-mono tabular-nums">{succeeded}</span>}
          />
          <InfoItem
            label={t("Success rate")}
            value={
              <span className="font-mono tabular-nums">{successRate}%</span>
            }
          />
        </div>
        {canOperate ? (
          <div className="mt-3 flex flex-wrap gap-2">
            <Button
              disabled={
                item.status === "disabled" ||
                item.status === "deleted" ||
                (busy !== null && busy !== "validate")
              }
              loading={busy === "validate"}
              onClick={() =>
                void runAction(
                  "validate",
                  validateAdminGmailResource,
                  "Resource validation submitted.",
                )
              }
              size="small"
              type="tertiary"
            >
              {t("Validate")}
            </Button>
            <Button
              disabled={
                (item.status !== "normal" &&
                  item.status !== "identifying") ||
                !item.appPasswordConfigured ||
                (busy !== null && busy !== "history")
              }
              loading={busy === "history"}
              onClick={() =>
                void runAction(
                  "history",
                  scanAdminGmailResourceHistory,
                  "Project history scan submitted.",
                )
              }
              size="small"
              type="tertiary"
            >
              {t("Scan projects")}
            </Button>
          </div>
        ) : null}
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

function GmailDetailSheet({
  busyAction,
  canOperate,
  canReadMessages,
  canReadOrders,
  canReadTasks,
  canWrite,
  item,
  loading,
  onCancel,
  onDelete,
  onEdit,
  onMaintain,
  onRecover,
  onRefresh,
  onToggleDisabled,
  onTogglePublish,
}: {
  busyAction: RowAction | null;
  canOperate: boolean;
  canReadMessages: boolean;
  canReadOrders: boolean;
  canReadTasks: boolean;
  canWrite: boolean;
  item: AdminGmailResourceItem | null;
  loading: boolean;
  onCancel: () => void;
  onDelete: (item: AdminGmailResourceItem) => void;
  onEdit: (item: AdminGmailResourceItem) => void;
  onMaintain: (item: AdminGmailResourceItem) => void;
  onRecover: (item: AdminGmailResourceItem) => void;
  onRefresh: () => void | Promise<void>;
  onToggleDisabled: (item: AdminGmailResourceItem) => void;
  onTogglePublish: (item: AdminGmailResourceItem) => void;
}) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [activeTab, setActiveTab] = useState("basic");

  useEffect(() => setActiveTab("basic"), [item?.id]);

  return (
    <SideSheet
      bodyStyle={{ padding: 0 }}
      onCancel={onCancel}
      placement="right"
      title={
        item
          ? `${t("Gmail resource detail")} #${item.id}`
          : t("Gmail resource detail")
      }
      visible={Boolean(item)}
      width={isMobile ? "100%" : 940}
    >
      {item ? (
        <Spin spinning={loading}>
          <div className="flex min-h-full flex-col">
            <div className="sticky top-0 z-10 bg-[var(--semi-color-bg-2)] px-5 pt-2">
              <Tabs
                activeKey={activeTab}
                collapsible
                onChange={setActiveTab}
                type="line"
              >
                <Tabs.TabPane itemKey="basic" tab={t("Basic info")} />
                <Tabs.TabPane itemKey="validation" tab={t("Validation")} />
                {canReadOrders ? (
                  <Tabs.TabPane itemKey="orders" tab={t("Orders")} />
                ) : null}
                <Tabs.TabPane itemKey="other" tab={t("Other aliases")} />
                {canReadTasks ? (
                  <Tabs.TabPane itemKey="tasks" tab={t("Task details")} />
                ) : null}
                {canReadMessages ? (
                  <Tabs.TabPane itemKey="mails" tab={t("Mailbox")} />
                ) : null}
              </Tabs>
            </div>

            <div className="flex-1 p-5">
              {activeTab === "basic" ? (
                <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                  <InfoItem
                    label="ID"
                    value={<span className="font-mono">#{item.id}</span>}
                  />
                  <InfoItem
                    label={t("Email")}
                    value={
                      <CopyableTableText
                        copiedText={t("Copied")}
                        text={item.email}
                      />
                    }
                  />
                  <InfoItem
                    label={t("Binding email")}
                    value={
                      item.bindingEmail ? (
                        <CopyableTableText
                          copiedText={t("Copied")}
                          text={item.bindingEmail}
                        />
                      ) : (
                        t("Not configured")
                      )
                    }
                  />
                  <InfoItem
                    label={t("Owner")}
                    value={<OwnerIdentity owner={item.owner} t={t} />}
                  />
                  <InfoItem
                    label={t("Status")}
                    value={<ResourceStatusTag item={item} />}
                  />
                  <InfoItem
                    label={t("Private")}
                    value={
                      <Tag
                        color={!item.forSale ? "green" : "grey"}
                        shape="circle"
                        size="small"
                      >
                        {!item.forSale ? t("Yes") : t("No")}
                      </Tag>
                    }
                  />
                  <InfoItem
                    label={t("Created at")}
                    value={formatTime(item.createdAt)}
                  />
                  <InfoItem
                    label={t("Updated at")}
                    value={formatTime(item.updatedAt)}
                  />
                  <InfoItem
                    label={t("Last allocated")}
                    value={formatTime(item.lastAllocatedAt)}
                  />
                </div>
              ) : null}

              {activeTab === "validation" ? (
                <div className="space-y-4">
                  <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                    <InfoItem
                      label={t("Password")}
                      value={
                        <ConfiguredTag configured={item.passwordConfigured} />
                      }
                    />
                    <InfoItem
                      label="2FA"
                      value={
                        <ConfiguredTag configured={item.twoFactorConfigured} />
                      }
                    />
                    <InfoItem
                      label={t("App password")}
                      value={
                        <ConfiguredTag
                          configured={item.appPasswordConfigured}
                        />
                      }
                    />
                    <InfoItem
                      label={t("Credential revision")}
                      value={item.credentialRevision}
                    />
                    <InfoItem
                      label={t("Credential updated at")}
                      value={formatTime(item.credentialUpdatedAt)}
                    />
                    <InfoItem
                      label={t("Failure streak")}
                      value={item.validationFailures}
                    />
                    <InfoItem
                      label={t("Last checked")}
                      value={formatTime(item.lastCheckedAt)}
                    />
                  </div>
                  {item.lastSafeError ? (
                    <div className="rounded-lg border border-[var(--semi-color-warning-light-active)] bg-[var(--semi-color-warning-light-default)] px-3 py-2 text-sm text-[var(--semi-color-text-0)]">
                      {item.lastSafeError}
                    </div>
                  ) : null}
                  <div className="rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] px-3 py-2 text-xs leading-5 text-[var(--semi-color-text-2)]">
                    {t(
                      "Only safe configuration flags are visible. Credential values are never returned.",
                    )}
                  </div>
                </div>
              ) : null}

              {activeTab === "other" ? (
                <AliasPanel
                  errorMessage="Gmail alias load failed."
                  kind="other"
                  listAliases={listGmailAliasesForPanel}
                  resourceId={item.id}
                  t={t}
                />
              ) : null}

              {activeTab === "orders" && canReadOrders ? (
                <RelatedOrdersTable
                  resourceId={item.id}
                  resourceType="gmail"
                  t={t}
                />
              ) : null}

              {activeTab === "tasks" && canReadTasks ? (
                <GmailTaskDiagnostics
                  canOperate={canOperate}
                  item={item}
                  onRefresh={onRefresh}
                />
              ) : null}

              {activeTab === "mails" && canReadMessages ? (
                <ResourceMailsPanel
                  fetchEnabled={false}
                  resourceId={item.id}
                  resourceType="gmail"
                  t={t}
                />
              ) : null}
            </div>

            {canWrite || canOperate ? (
              <div className="sticky bottom-0 flex flex-wrap items-center justify-end gap-2 border-t border-[var(--semi-color-border)] bg-[var(--semi-color-bg-0)] px-5 py-3">
                {item.status === "deleted" ? (
                  canOperate ? (
                    <Button
                      disabled={Boolean(busyAction)}
                      loading={busyAction === "recover"}
                      onClick={() => onRecover(item)}
                      type="primary"
                    >
                      {t("Recover")}
                    </Button>
                  ) : null
                ) : (
                  <>
                    {canWrite || canOperate ? (
                      <Button
                        disabled={Boolean(busyAction)}
                        onClick={() => onEdit(item)}
                        type="tertiary"
                      >
                        {t("Edit")}
                      </Button>
                    ) : null}
                    {canOperate ? (
                      <>
                        <Button
                          disabled={Boolean(busyAction)}
                          onClick={() => onMaintain(item)}
                          type="primary"
                        >
                          {t("Maintenance")}
                        </Button>
                        <Button
                          disabled={Boolean(busyAction)}
                          loading={busyAction === "toggle"}
                          onClick={() => onToggleDisabled(item)}
                          type="tertiary"
                        >
                          {item.status === "disabled"
                            ? t("Enable")
                            : t("Disable")}
                        </Button>
                        <Button
                          disabled={Boolean(busyAction)}
                          loading={busyAction === "publish"}
                          onClick={() => onTogglePublish(item)}
                          type="tertiary"
                        >
                          {item.forSale
                            ? t("Convert to private")
                            : t("Put on sale")}
                        </Button>
                        <Button
                          disabled={Boolean(busyAction)}
                          loading={busyAction === "delete"}
                          onClick={() => onDelete(item)}
                          type="danger"
                        >
                          {t("Delete")}
                        </Button>
                      </>
                    ) : null}
                  </>
                )}
              </div>
            ) : null}
          </div>
        </Spin>
      ) : null}
    </SideSheet>
  );
}

export default function AdminGmailEmails() {
  const { t } = useTranslation();
  const { currentUser } = useAuth();
  const isMobile = useIsMobile();
  const [pageSize, setPageSize] = useSharedPageSize();
  const [activePage, setActivePage] = useState(1);
  const [searchKeyword, setSearchKeyword] = useState("");
  const [debouncedSearchKeyword, flushSearchKeyword] =
    useDebouncedValue(searchKeyword);
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [privateFilter, setPrivateFilter] = useState<BooleanFilter>("all");
  const [compactMode, setCompactMode] = useState(false);
  const [response, setResponse] = useState<AdminGmailResourceList | null>(null);
  const [owners, setOwners] = useState<AdminGmailOwner[]>([]);
  const importOwners = useMemo(
    () => ownersWithCurrentUserFirst(owners, currentUser),
    [currentUser, owners],
  );
  const [loading, setLoading] = useState(true);
  const [importVisible, setImportVisible] = useState(false);
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [detail, setDetail] = useState<AdminGmailResourceItem | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [editTarget, setEditTarget] = useState<AdminGmailResourceItem | null>(
    null,
  );
  const [maintenanceTarget, setMaintenanceTarget] =
    useState<GmailMaintenanceTarget | null>(null);
  const [bulkBusy, setBulkBusy] = useState<AdminGmailBatchAction | null>(null);
  const [rowBusy, setRowBusy] = useState<{
    action: RowAction;
    id: number;
  } | null>(null);
  const listRequestRef = useRef<AbortController | null>(null);
  const detailRequestRef = useRef<AbortController | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    void listAdminGmailOwners("", controller.signal)
      .then((items) => {
        if (!controller.signal.aborted) setOwners(items);
      })
      .catch(() => {
        // Owner choices are optional UI data; the resource list remains usable.
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
  const canReadOrders = hasPermissionKey(
    currentUser,
    permissionKey("alloc:allocation", "read"),
  );
  const canReadTasks = hasPermissionKey(
    currentUser,
    permissionKey("governance:task", "read"),
  );
  const canReadMessages = hasPermissionKey(
    currentUser,
    permissionKey("mailmatch:message", "read"),
  );

  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);
  const listFilter = useMemo<AdminGmailResourceListFilter>(
    () => ({
      createdFrom: createdFromISOString(createdAtRange),
      createdTo: createdToISOString(createdAtRange),
      forSale:
        privateFilter === "all" ? undefined : privateFilter === "no",
      search: debouncedSearchKeyword.trim() || undefined,
      status: statusFilter === "all" ? undefined : statusFilter,
    }),
    [
      createdAtRange,
      debouncedSearchKeyword,
      privateFilter,
      statusFilter,
    ],
  );

  const load = useCallback(async () => {
    listRequestRef.current?.abort();
    const controller = new AbortController();
    listRequestRef.current = controller;
    setLoading(true);
    try {
      const next = await listAdminGmailResources({
        ...listFilter,
        limit: pageSize,
        offset: (activePage - 1) * pageSize,
        signal: controller.signal,
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
  }, [activePage, listFilter, pageSize, t]);

  useEffect(() => {
    void load();
    return () => {
      listRequestRef.current?.abort();
      detailRequestRef.current?.abort();
    };
  }, [load]);

  useEffect(
    () => {
      setActivePage(1);
      setSelectedKeys([]);
    },
    [debouncedSearchKeyword, pageSize, privateFilter, statusFilter],
  );

  const loadDetail = useCallback(
    async (resourceId: number, seed?: AdminGmailResourceItem) => {
      detailRequestRef.current?.abort();
      const controller = new AbortController();
      detailRequestRef.current = controller;
      if (seed) setDetail(seed);
      setDetailLoading(true);
      try {
        const next = await getAdminGmailResource(resourceId, controller.signal);
        if (!controller.signal.aborted) setDetail(next);
      } catch (error) {
        if (!controller.signal.aborted) {
          Toast.error(
            getIamErrorMessage(t, error, "Gmail resource detail load failed."),
          );
        }
      } finally {
        if (detailRequestRef.current === controller) {
          detailRequestRef.current = null;
          setDetailLoading(false);
        }
      }
    },
    [t],
  );

  const refreshAfterMutation = useCallback(
    async (resourceId?: number) => {
      await load();
      if (resourceId && detail?.id === resourceId) {
        await loadDetail(resourceId);
      }
    },
    [detail?.id, load, loadDetail],
  );

  const runResourceOperation = useCallback(
    async (
      item: AdminGmailResourceItem,
      action: RowAction,
      operation: () => Promise<unknown>,
      successKey: string,
    ) => {
      setRowBusy({ action, id: item.id });
      try {
        await operation();
        Toast.success(t(successKey));
        await refreshAfterMutation(item.id);
      } catch (error) {
        Toast.error(
          getIamErrorMessage(t, error, "Gmail resource operation failed."),
        );
      } finally {
        setRowBusy(null);
      }
    },
    [refreshAfterMutation, t],
  );

  const toggleResource = useCallback(
    (item: AdminGmailResourceItem) => {
      const enabled = item.status === "disabled";
      return runResourceOperation(
        item,
        "toggle",
        () => setAdminGmailResourceEnabled(item.id, item.version, enabled),
        enabled ? "Gmail account enabled." : "Gmail account disabled.",
      );
    },
    [runResourceOperation],
  );

  const toggleResourceForSale = useCallback(
    (item: AdminGmailResourceItem) =>
      runResourceOperation(
        item,
        "publish",
        () => setAdminGmailResourceForSale(item.id, item.version, !item.forSale),
        item.forSale
          ? "Gmail resource converted to private."
          : "Gmail resource published for public sale.",
      ),
    [runResourceOperation],
  );

  const confirmDelete = useCallback(
    (item: AdminGmailResourceItem) => {
      Modal.confirm({
        cancelText: t("Cancel"),
        content: t("Confirm delete Gmail resource content", {
          email: item.email,
        }),
        okButtonProps: { type: "danger" },
        okText: t("Delete"),
        onOk: () =>
          runResourceOperation(
            item,
            "delete",
            () => deleteAdminGmailResource(item.id, item.version),
            "Gmail resource deleted.",
          ),
        title: t("Confirm delete"),
      });
    },
    [runResourceOperation, t],
  );

  const recoverResource = useCallback(
    (item: AdminGmailResourceItem) =>
      runResourceOperation(
        item,
        "recover",
        () => recoverAdminGmailResource(item.id, item.version),
        "Gmail resource recovered and queued for validation.",
      ),
    [runResourceOperation],
  );

  const facets = response?.facets ?? EMPTY_FACETS;
  const total = response?.total ?? 0;

  const showBulkOutcome = useCallback(
    (result: AdminGmailBulkResponse, successKey: string) => {
      Toast.success(t(successKey, { count: result.affected }));
      if (!result.skipped) return;
      const reasons = result.reasonCounts
        .map((item) => `${item.reason}: ${item.count}`)
        .join(", ");
      Toast.warning(
        `${t("Succeeded")}: ${result.affected}/${result.requested}` +
          (reasons ? ` · ${t("Reason")}: ${reasons}` : ""),
      );
    },
    [t],
  );

  const runBatch = useCallback(
    async (action: AdminGmailBatchAction, allMatching: boolean) => {
      const count = allMatching ? total : selectedKeys.length;
      if (!count) {
        Toast.info(t("No resources to check."));
        return;
      }
      setBulkBusy(action);
      try {
        const result = allMatching
          ? await batchAllMatchingAdminGmailResources(action, listFilter)
          : await batchAdminGmailResourcesByIds(action, selectedKeys);
        const successKey = {
          delete: "Gmail resources deleted.",
          disable: "Gmail resources disabled.",
          history: "Project scan batch submitted.",
          publish: "Gmail resources published for public sale.",
          unpublish: "Gmail resources converted to private.",
          validate: "Resource validation batch submitted.",
        }[action];
        showBulkOutcome(result, successKey);
        setSelectedKeys([]);
        if (action === "delete") setActivePage(1);
        await refreshAfterMutation();
      } catch (error) {
        Toast.error(
          getIamErrorMessage(t, error, "Gmail resource operation failed."),
        );
      } finally {
        setBulkBusy(null);
      }
    },
    [
      listFilter,
      refreshAfterMutation,
      selectedKeys,
      showBulkOutcome,
      t,
      total,
    ],
  );

  const confirmBatch = useCallback(
    (
      action: Exclude<AdminGmailBatchAction, "validate" | "history">,
      allMatching: boolean,
    ) => {
      const count = allMatching ? total : selectedKeys.length;
      if (!count) {
        Toast.info(t("No resources to check."));
        return;
      }
      const contentKey = allMatching
        ? {
            delete: "Confirm delete all matching Gmail resources",
            disable: "Confirm disable all matching Gmail resources",
            publish: "Confirm put all matching Gmail resources on sale",
            unpublish: "Confirm convert all matching Gmail resources to private",
          }[action]
        : {
            delete: "Confirm delete selected Gmail resources",
            disable: "Confirm disable selected Gmail resources",
            publish: "Confirm put selected Gmail resources on sale",
            unpublish: "Confirm convert selected Gmail resources to private",
          }[action];
      Modal.confirm({
        cancelText: t("Cancel"),
        content: t(contentKey, { count }),
        okButtonProps: action === "delete" ? { type: "danger" } : undefined,
        okText:
          action === "publish"
            ? t("Put on sale")
            : action === "unpublish"
              ? t("Convert to private")
              : t(action === "delete" ? "Delete" : "Disable"),
        onOk: () => runBatch(action, allMatching),
        title: t(action === "delete" ? "Confirm delete" : "Confirm operation"),
      });
    },
    [runBatch, selectedKeys.length, t, total],
  );

  const openBulkMaintenance = useCallback(
    (allMatching: boolean) => {
      const count = allMatching ? total : selectedKeys.length;
      if (!count) {
        Toast.info(t("No resources to check."));
        return;
      }
      setMaintenanceTarget(
        allMatching
          ? { count, filter: listFilter, mode: "filter" }
          : { count, mode: "ids", resourceIds: [...selectedKeys] },
      );
    },
    [listFilter, selectedKeys, t, total],
  );

  useSelectionNotification({
    checkLabelKey: "Maintenance",
    deleteLoading: bulkBusy === "delete",
    extraActions: [
      {
        key: "publish",
        labelKey: "Put on sale",
        loading: bulkBusy === "publish",
        onClick: () => confirmBatch("publish", false),
        type: "secondary",
      },
      {
        key: "private",
        labelKey: "Convert to private",
        loading: bulkBusy === "unpublish",
        onClick: () => confirmBatch("unpublish", false),
        type: "tertiary",
      },
    ],
    onCheck: () => openBulkMaintenance(false),
    onClear: () => setSelectedKeys([]),
    onDelete: () => confirmBatch("delete", false),
    onSell: () => confirmBatch("disable", false),
    selectedCount: canOperate ? selectedKeys.length : 0,
    selectionDescriptionKey: "Selected Gmail resources",
    sellLabelKey: "Disable",
    sellLoading: bulkBusy === "disable",
    t,
  });

  const applyStatusFilter = (value: StatusFilter) => {
    setStatusFilter(value);
    setActivePage(1);
    setSelectedKeys([]);
  };

  const resetFilters = () => {
    setSearchKeyword("");
    flushSearchKeyword("");
    setCreatedAtRange([]);
    setStatusFilter("all");
    setPrivateFilter("all");
    setActivePage(1);
    setSelectedKeys([]);
  };

  const activeFilterCount =
    Number(statusFilter !== "all") +
    Number(privateFilter !== "all") +
    Number(createdAtRange.length === 2);

  const renderRowActions = useCallback(
    (item: AdminGmailResourceItem) => {
      const busyAction = rowBusy?.id === item.id ? rowBusy.action : null;
      if (item.status === "deleted") {
        return (
          <Space spacing={4} wrap={isMobile}>
            <Button
              disabled={Boolean(busyAction)}
              onClick={() => void loadDetail(item.id, item)}
              size="small"
              type="tertiary"
            >
              {t("Details")}
            </Button>
            {canOperate ? (
              <Button
                disabled={Boolean(rowBusy && busyAction !== "recover")}
                loading={busyAction === "recover"}
                onClick={() => void recoverResource(item)}
                size="small"
                type="primary"
              >
                {t("Recover")}
              </Button>
            ) : null}
          </Space>
        );
      }

      return (
        <Space spacing={4} wrap={isMobile}>
          <Button
            disabled={Boolean(busyAction)}
            onClick={() => void loadDetail(item.id, item)}
            size="small"
            type="tertiary"
          >
            {t("Details")}
          </Button>
          {canWrite || canOperate ? (
            <Button
              disabled={Boolean(busyAction)}
              onClick={() => setEditTarget(item)}
              size="small"
              type="tertiary"
            >
              {t("Edit")}
            </Button>
          ) : null}
          {canOperate ? (
            <>
              <Button
                disabled={Boolean(busyAction)}
                onClick={() =>
                  setMaintenanceTarget({ item, mode: "row" })
                }
                size="small"
                type="tertiary"
              >
                {t("Maintenance")}
              </Button>
              <Button
                disabled={Boolean(rowBusy && busyAction !== "toggle")}
                loading={busyAction === "toggle"}
                onClick={() => void toggleResource(item)}
                size="small"
                type="tertiary"
              >
                {item.status === "disabled" ? t("Enable") : t("Disable")}
              </Button>
              <Button
                disabled={Boolean(rowBusy && busyAction !== "publish")}
                loading={busyAction === "publish"}
                onClick={() => void toggleResourceForSale(item)}
                size="small"
                type="tertiary"
              >
                {item.forSale ? t("Convert to private") : t("Put on sale")}
              </Button>
              <Button
                disabled={Boolean(rowBusy && busyAction !== "delete")}
                loading={busyAction === "delete"}
                onClick={() => confirmDelete(item)}
                size="small"
                type="danger"
              >
                {t("Delete")}
              </Button>
            </>
          ) : null}
        </Space>
      );
    },
    [
      canOperate,
      canWrite,
      confirmDelete,
      isMobile,
      loadDetail,
      recoverResource,
      rowBusy,
      t,
      toggleResource,
      toggleResourceForSale,
    ],
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
      dataIndex: "owner",
      title: t("Owner"),
      width: 310,
      render: (_value: unknown, item: AdminGmailResourceItem) => (
        <OwnerIdentity owner={item.owner} t={t} />
      ),
    },
    {
      dataIndex: "status",
      title: t("Status"),
      width: 120,
      render: (_value: unknown, item: AdminGmailResourceItem) => (
        <ResourceStatusTag item={item} />
      ),
    },
    {
      dataIndex: "forSale",
      title: t("Private"),
      width: 100,
      render: (value: unknown) => (
        <Tag color={!value ? "green" : "grey"} shape="circle" size="small">
          {!value ? t("Yes") : t("No")}
        </Tag>
      ),
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
      render: (value: unknown) => (
        <span className="text-xs text-[var(--semi-color-text-2)]">
          {formatTime(value ? String(value) : undefined)}
        </span>
      ),
    },
    {
      dataIndex: "createdAt",
      title: t("Created at"),
      width: 180,
      render: (value: unknown) => (
        <span className="text-xs text-[var(--semi-color-text-2)]">
          {formatTime(String(value))}
        </span>
      ),
    },
    {
      dataIndex: "operate",
      fixed: "right" as const,
      key: "operate",
      title: t("Action"),
      width: 360,
      render: (_value: unknown, item: AdminGmailResourceItem) =>
        renderRowActions(item),
    },
  ];

  const tableColumns = compactMode
    ? columns.map((column) => {
        if (column.dataIndex !== "operate") return column;
        const { fixed: _fixed, ...rest } = column;
        return rest;
      })
    : columns;

  const rowSelection = {
    selectedRowKeys: selectedKeys,
    onChange: (keys: Array<string | number>) => {
      setSelectedKeys(keys.map((key) => Number(key)));
    },
  };

  const actionsArea = (
    <div className="flex w-full flex-col items-center justify-between gap-2 md:flex-row">
      <div className="order-2 flex w-full flex-wrap gap-2 md:order-1 md:w-auto">
        <Button
          className="flex-1 md:flex-initial"
          disabled={!canWrite}
          onClick={() => setImportVisible(true)}
          size="small"
          type="primary"
        >
          {t("Import")}
        </Button>
        <Button
          className="remail-toolbar-fixed-button flex-1 md:flex-none"
          loading={loading}
          onClick={() => void load()}
          size="small"
          type="tertiary"
        >
          {t("Refresh")}
        </Button>
        {canOperate ? (
          <>
            <Tooltip
              content={t("Maintain all")}
              mouseEnterDelay={0}
              mouseLeaveDelay={0.05}
              position="top"
            >
              <Button
                className="flex-1 md:flex-initial"
                onClick={() => openBulkMaintenance(true)}
                size="small"
                type="tertiary"
              >
                {t("Maintenance")}
              </Button>
            </Tooltip>
            <Tooltip
              content={t("Put all matching Gmail resources on sale")}
              mouseEnterDelay={0}
              mouseLeaveDelay={0.05}
              position="top"
            >
              <Button
                className="flex-1 md:flex-initial"
                loading={bulkBusy === "publish"}
                onClick={() => confirmBatch("publish", true)}
                size="small"
                type="tertiary"
              >
                {t("Put on sale")}
              </Button>
            </Tooltip>
            <Tooltip
              content={t("Convert all matching Gmail resources to private")}
              mouseEnterDelay={0}
              mouseLeaveDelay={0.05}
              position="top"
            >
              <Button
                className="flex-1 md:flex-initial"
                loading={bulkBusy === "unpublish"}
                onClick={() => confirmBatch("unpublish", true)}
                size="small"
                type="tertiary"
              >
                {t("Convert to private")}
              </Button>
            </Tooltip>
            <Tooltip
              content={t("Delete all matching Gmail resources")}
              mouseEnterDelay={0}
              mouseLeaveDelay={0.05}
              position="top"
            >
              <Button
                className="flex-1 md:flex-initial"
                loading={bulkBusy === "delete"}
                onClick={() => confirmBatch("delete", true)}
                size="small"
                type="danger"
              >
                {t("Delete")}
              </Button>
            </Tooltip>
          </>
        ) : null}
        <CompactModeToggle
          compactMode={compactMode}
          setCompactMode={setCompactMode}
          t={t}
        />
      </div>

      <div className="order-1 flex w-full flex-col items-center gap-2 md:order-2 md:w-auto md:flex-row">
        <Dropdown
          position="bottomRight"
          render={
            <div className="max-h-[70vh] w-[280px] overflow-auto p-2">
              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Status")}
              </div>
              <div className="mb-2 space-y-1">
                <StatisticFilterOption
                  active={statusFilter === "all"}
                  count={facets.all}
                  label={t("All")}
                  onSelect={applyStatusFilter}
                  value="all"
                />
                {(Object.entries(statusMeta) as Array<
                  [
                    AdminGmailResourceStatus,
                    (typeof statusMeta)[AdminGmailResourceStatus],
                  ]
                >).map(([value, meta]) => (
                  <StatisticFilterOption
                    active={statusFilter === value}
                    count={facets[value]}
                    key={value}
                    label={t(meta.label)}
                    onSelect={applyStatusFilter}
                    value={value}
                  />
                ))}
              </div>

              <div className="px-2 pb-1 text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Private")}
              </div>
              <div className="space-y-1">
                {(["all", "yes", "no"] as BooleanFilter[]).map((value) => (
                  <StatisticFilterOption
                    active={privateFilter === value}
                    count={
                      value === "all"
                        ? facets.forSale.all
                        : value === "yes"
                          ? facets.forSale.no
                          : facets.forSale.yes
                    }
                    key={value}
                    label={t(
                      value === "all" ? "All" : value === "yes" ? "Yes" : "No",
                    )}
                    onSelect={(next) => {
                      setPrivateFilter(next);
                      setActivePage(1);
                      setSelectedKeys([]);
                    }}
                    value={value}
                  />
                ))}
              </div>
            </div>
          }
          trigger="click"
        >
          <Button
            className="flex-1 md:flex-initial"
            icon={<SlidersHorizontal size={14} />}
            size="small"
            type="tertiary"
          >
            {activeFilterCount > 0
              ? `${t("Filters")} (${activeFilterCount})`
              : t("Filters")}
          </Button>
        </Dropdown>

        <Input
          className="resources-search-input w-full md:w-56"
          onChange={(value) => {
            setSearchKeyword(String(value));
            setActivePage(1);
            setSelectedKeys([]);
          }}
          onEnterPress={() => {
            flushSearchKeyword();
            setActivePage(1);
          }}
          placeholder={t("Search Gmail, binding email, owner or ID")}
          prefix={<IconSearch />}
          showClear
          size="small"
          style={{ width: isMobile ? "100%" : 224 }}
          value={searchKeyword}
        />

        <DatePicker
          dropdownClassName={DATE_RANGE_DROPDOWN_CLASS}
          format="yyyy-MM-dd HH:mm:ss"
          onChange={(value) => {
            setCreatedAtRange(normalizeDateRangeValue(value));
            setActivePage(1);
            setSelectedKeys([]);
          }}
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
          <Button
            className="remail-toolbar-fixed-button flex-1 md:flex-none"
            loading={loading}
            onClick={() => {
              flushSearchKeyword();
              setActivePage(1);
            }}
            size="small"
            type="tertiary"
          >
            {t("Query")}
          </Button>
          <Button
            className="flex-1 md:flex-initial"
            onClick={resetFilters}
            size="small"
            type="tertiary"
          >
            {t("Reset")}
          </Button>
        </div>
      </div>
    </div>
  );

  const paginationArea = createCardProPagination({
    currentPage: activePage,
    isMobile,
    onPageChange: (page) => {
      setActivePage(page);
      setSelectedKeys([]);
    },
    onPageSizeChange: (size) => {
      setPageSize(size);
      setActivePage(1);
      setSelectedKeys([]);
    },
    pageSize,
    t,
    total,
  });

  return (
    <div className="console-content-width py-5">
      <CardPro
        actionsArea={actionsArea}
        paginationArea={paginationArea}
        t={t}
        type="type3"
      >
        <CardTable
          className="overflow-hidden rounded-xl"
          columns={tableColumns}
          dataSource={response?.items ?? []}
          empty={
            <Empty
              darkModeImage={
                <IllustrationNoResultDark style={{ height: 150, width: 150 }} />
              }
              description={t("No Gmail resources found")}
              image={<IllustrationNoResult style={{ height: 150, width: 150 }} />}
              style={{ padding: 30 }}
            />
          }
          hidePagination
          loading={loading}
          pagination={false}
          rowKey="id"
          rowSelection={canOperate ? rowSelection : undefined}
          scroll={{ x: "max(100%, 2080px)", y: DESKTOP_TABLE_SCROLL_Y }}
          size="middle"
        />
      </CardPro>

      <ImportGmailModal
        onCancel={() => setImportVisible(false)}
        onImported={async () => {
          setSelectedKeys([]);
          if (activePage === 1) {
            await load();
            return;
          }
          setActivePage(1);
        }}
        owners={importOwners}
        visible={importVisible && canWrite}
      />

      <EditGmailModal
        canOperate={canOperate}
        canWrite={canWrite}
        onCancel={() => setEditTarget(null)}
        onSaved={async () => {
          await refreshAfterMutation(editTarget?.id);
        }}
        owners={owners}
        target={canWrite || canOperate ? editTarget : null}
      />

      <GmailMaintenanceModal
        canReadTasks={canReadTasks}
        onCancel={() => setMaintenanceTarget(null)}
        onCompleted={async () => {
          setSelectedKeys([]);
          await refreshAfterMutation(
            maintenanceTarget?.mode === "row"
              ? maintenanceTarget.item.id
              : undefined,
          );
        }}
        target={canOperate ? maintenanceTarget : null}
      />

      <GmailDetailSheet
        busyAction={
          rowBusy && rowBusy.id === detail?.id ? rowBusy.action : null
        }
        canOperate={canOperate}
        canReadMessages={canReadMessages}
        canReadOrders={canReadOrders}
        canReadTasks={canReadTasks}
        canWrite={canWrite}
        item={detail}
        loading={detailLoading}
        onCancel={() => {
          detailRequestRef.current?.abort();
          detailRequestRef.current = null;
          setDetailLoading(false);
          setDetail(null);
        }}
        onDelete={confirmDelete}
        onEdit={setEditTarget}
        onMaintain={(item) => setMaintenanceTarget({ item, mode: "row" })}
        onRecover={(item) => void recoverResource(item)}
        onRefresh={async () => {
          if (detail) await refreshAfterMutation(detail.id);
        }}
        onToggleDisabled={(item) => void toggleResource(item)}
        onTogglePublish={(item) => void toggleResourceForSale(item)}
      />
    </div>
  );
}
