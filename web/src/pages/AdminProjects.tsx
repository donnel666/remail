import { useCallback, useEffect, useMemo, useState } from "react";
import type { ReactNode } from "react";
import {
  Button,
  Checkbox,
  DatePicker,
  Dropdown,
  Empty,
  Input,
  Modal,
  Select,
  SideSheet,
  Space,
  Tag,
  TextArea,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import { SlidersHorizontal } from "lucide-react";
import { useTranslation } from "react-i18next";

import sampleProjectCover from "@/assets/cover-4.webp";
import { CardPro } from "@/components/semi/card-pro";
import { createCardProPagination } from "@/components/semi/card-pro-pagination";
import { CardTable } from "@/components/semi/card-table";
import { CompactModeToggle } from "@/components/semi/compact-mode-toggle";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  approveAdminProject,
  createAdminProject,
  deleteAdminProject,
  deleteAdminProjectsByFilter,
  deleteAdminProjectsByIds,
  delistAdminProject,
  delistAdminProjectsByFilter,
  delistAdminProjectsByIds,
  getProject,
  grantAdminProjectAccess,
  listProjects,
  listAdminProjectAccess,
  rejectAdminProject,
  relistAdminProject,
  relistAdminProjectsByFilter,
  relistAdminProjectsByIds,
  revokeAdminProjectAccess,
  uploadAdminProjectLogo,
  type AdminCreateProjectRequest,
  type AdminUpdateProjectRequest,
  type ProjectAccess,
  type ProjectDetailResponse,
  type ProjectItem,
  type ProjectListFilter,
  type ProjectProductRequest,
  updateAdminProject,
} from "@/lib/projects-api";

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
const projectLogoGalleryStorageKey = "remail.project.logo.gallery.v1";

type ProjectStatusFilter = "all" | "reviewing" | "listed" | "delisted";
type BooleanFilter = "all" | "yes" | "no";
type ProjectProductType = "microsoft" | "domain";
type ProjectProductTypeFilter = "all" | ProjectProductType;
type ProjectMailRuleType = "sender" | "recipient" | "subject" | "body";
type RecipientPattern = "exact" | "dot" | "plus";
type ProjectEditorMode = "create" | "edit" | "approve";

const recipientPatterns: RecipientPattern[] = ["exact", "dot", "plus"];

interface MailRuleDraft {
  enabled: boolean;
  key: string;
  pattern: string;
  ruleType: ProjectMailRuleType;
}

interface ProductDraft {
  activationWindowMinutes: string;
  codeEnabled: boolean;
  codePrice: string;
  codeSupplierPrice: string;
  codeWindowMinutes: string;
  dotWeight: string;
  mainWeight: string;
  plusWeight: string;
  purchaseEnabled: boolean;
  purchasePrice: string;
  purchaseSupplierPrice: string;
  type: ProjectProductType;
  warrantyMinutes: string;
}

interface ProjectDraft {
  accessType: "public" | "private";
  description: string;
  logoUrl: string;
  looseMatch: boolean;
  mailRules: MailRuleDraft[];
  name: string;
  products: ProductDraft[];
  targetPlatform: string;
}

let mailRuleDraftSequence = 0;

function createMailRuleDraft(
  patch: Partial<MailRuleDraft> & Pick<MailRuleDraft, "ruleType">
): MailRuleDraft {
  return {
    enabled: true,
    key: `mail-rule-${Date.now()}-${mailRuleDraftSequence++}`,
    pattern: patch.ruleType === "recipient" ? "exact" : ".*",
    ...patch,
  };
}

function createDefaultMailRules(): MailRuleDraft[] {
  return [
    createMailRuleDraft({ pattern: ".*", ruleType: "sender" }),
    createMailRuleDraft({ pattern: "exact", ruleType: "recipient" }),
  ];
}

function createDefaultProduct(type: ProjectProductType): ProductDraft {
  return {
    activationWindowMinutes: "60",
    codeEnabled: true,
    codePrice: type === "microsoft" ? "0.10" : "0.08",
    codeSupplierPrice: type === "microsoft" ? "0.05" : "0.04",
    codeWindowMinutes: "10",
    dotWeight: "0",
    mainWeight: type === "microsoft" ? "1" : "0",
    plusWeight: "0",
    purchaseEnabled: false,
    purchasePrice: "0",
    purchaseSupplierPrice: "0",
    type,
    warrantyMinutes: "60",
  };
}

function initialDraft(): ProjectDraft {
  return {
    accessType: "public",
    description: "",
    logoUrl: "",
    looseMatch: true,
    mailRules: createDefaultMailRules(),
    name: "",
    products: [createDefaultProduct("microsoft")],
    targetPlatform: "",
  };
}

function statusTag(status: string, t: (key: string) => string) {
  const map: Record<string, { color: "green" | "grey" | "orange"; label: string }> = {
    delisted: { color: "grey", label: t("Delisted") },
    listed: { color: "green", label: t("Listed") },
    reviewing: { color: "orange", label: t("Reviewing") },
  };
  const item = map[status] ?? { color: "grey" as const, label: status };
  return (
    <Tag color={item.color} shape="circle" size="small">
      {item.label}
    </Tag>
  );
}

function booleanTag(value: boolean, t: (key: string) => string) {
  return (
    <Tag color={value ? "green" : "grey"} shape="circle" size="small">
      {value ? t("Yes") : t("No")}
    </Tag>
  );
}

function productTypeLabel(type: string, t: (key: string) => string) {
  if (type === "microsoft") return t("Microsoft email");
  if (type === "domain") return t("Domain email");
  return type;
}

function mailRuleTypeLabel(type: ProjectMailRuleType, t: (key: string) => string) {
  return t(type);
}

function recipientPatternLabel(pattern: string, t: (key: string) => string) {
  if (pattern === "exact") return t("Exact match");
  if (pattern === "dot") return t("Dot alias match");
  if (pattern === "plus") return t("Plus alias match");
  return pattern;
}

function isRecipientPattern(pattern: string): pattern is RecipientPattern {
  return recipientPatterns.includes(pattern as RecipientPattern);
}

function requiredMailRuleTypes(looseMatch: boolean): ProjectMailRuleType[] {
  return looseMatch ? ["sender", "recipient"] : ["sender", "recipient", "subject", "body"];
}

function formatTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return date.toLocaleString();
}

function toNonNegativeInt(value: string) {
  const parsed = Number.parseInt(value || "0", 10);
  return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
}

function normalizedMoney(value: string) {
  const parsed = Number(value || "0");
  if (!Number.isFinite(parsed) || parsed < 0) return "";
  return parsed.toFixed(6);
}

function moneyToDraft(value?: string) {
  if (!value) return "0";
  const parsed = Number(value);
  if (!Number.isFinite(parsed)) return value;
  return parsed.toString();
}

function updateProduct(
  products: ProductDraft[],
  type: ProjectProductType,
  patch: Partial<ProductDraft>
) {
  return products.map((product) =>
    product.type === type ? { ...product, ...patch } : product
  );
}

function detailToDraft(detail: ProjectDetailResponse | null): ProjectDraft {
  if (!detail) return initialDraft();
  const products =
    detail.products.length > 0
      ? detail.products.map((product) => ({
          activationWindowMinutes: String(product.activationWindowMinutes ?? 0),
          codeEnabled: product.codeEnabled,
          codePrice: moneyToDraft(product.codePrice),
          codeSupplierPrice: moneyToDraft(product.codeSupplierPrice),
          codeWindowMinutes: String(product.codeWindowMinutes ?? 0),
          dotWeight: String(product.dotWeight ?? 0),
          mainWeight: String(product.mainWeight ?? 0),
          plusWeight: String(product.plusWeight ?? 0),
          purchaseEnabled: product.purchaseEnabled,
          purchasePrice: moneyToDraft(product.purchasePrice),
          purchaseSupplierPrice: moneyToDraft(product.purchaseSupplierPrice),
          type: product.type as ProjectProductType,
          warrantyMinutes: String(product.warrantyMinutes ?? 0),
        }))
      : [createDefaultProduct("microsoft")];

  return {
    accessType: detail.project.accessType === "private" ? "private" : "public",
    description: detail.project.description ?? "",
    logoUrl: detail.project.logoUrl ?? "",
    looseMatch: detail.project.looseMatch,
    mailRules:
      (detail.mailRules ?? []).length > 0
        ? (detail.mailRules ?? []).map((rule) =>
            createMailRuleDraft({
              enabled: rule.enabled,
              pattern: rule.pattern,
              ruleType: rule.ruleType,
            })
          )
        : createDefaultMailRules(),
    name: detail.project.name,
    products,
    targetPlatform: detail.project.targetPlatform,
  };
}

function buildProjectPayload(
  draft: ProjectDraft,
  t: (key: string) => string
): AdminCreateProjectRequest | null {
  if (!draft.name.trim() || !draft.targetPlatform.trim()) {
    Toast.error(t("Please complete required project fields."));
    return null;
  }
  if (draft.products.length === 0) {
    Toast.error(t("At least one product is required."));
    return null;
  }
  if (draft.mailRules.length === 0) {
    Toast.error(t("At least one mail rule is required."));
    return null;
  }

  const mailRules = draft.mailRules.map((rule) => ({
    enabled: rule.enabled,
    pattern: rule.pattern.trim(),
    ruleType: rule.ruleType,
  }));
  if (mailRules.some((rule) => !rule.pattern)) {
    Toast.error(t("Mail rule pattern is required."));
    return null;
  }
  if (
    mailRules.some(
      (rule) => rule.ruleType === "recipient" && !isRecipientPattern(rule.pattern)
    )
  ) {
    Toast.error(t("Recipient rule must use a built-in strategy."));
    return null;
  }
  const enabledTypes = new Set(
    mailRules.filter((rule) => rule.enabled).map((rule) => rule.ruleType)
  );
  const missingRuleTypes = requiredMailRuleTypes(draft.looseMatch).filter(
    (ruleType) => !enabledTypes.has(ruleType)
  );
  if (missingRuleTypes.length > 0) {
    Toast.error(
      t("Missing mail rules.") +
        missingRuleTypes.map((ruleType) => mailRuleTypeLabel(ruleType, t)).join(", ")
    );
    return null;
  }

  const products: ProjectProductRequest[] = [];
  for (const product of draft.products) {
    if (!product.codeEnabled && !product.purchaseEnabled) {
      Toast.error(t("Each product must enable at least one service."));
      return null;
    }
    if (product.codeEnabled && toNonNegativeInt(product.codeWindowMinutes) <= 0) {
      Toast.error(t("Code window must be positive."));
      return null;
    }
    if (
      product.purchaseEnabled &&
      (toNonNegativeInt(product.activationWindowMinutes) <= 0 ||
        toNonNegativeInt(product.warrantyMinutes) <= 0)
    ) {
      Toast.error(t("Purchase windows must be positive."));
      return null;
    }
    if (
      product.type === "microsoft" &&
      toNonNegativeInt(product.mainWeight) +
        toNonNegativeInt(product.dotWeight) +
        toNonNegativeInt(product.plusWeight) <=
        0
    ) {
      Toast.error(t("Microsoft weights must be positive."));
      return null;
    }

    const productRequest = {
      activationWindowMinutes: toNonNegativeInt(product.activationWindowMinutes),
      codeEnabled: product.codeEnabled,
      codePrice: normalizedMoney(product.codePrice),
      codeSupplierPrice: normalizedMoney(product.codeSupplierPrice),
      codeWindowMinutes: toNonNegativeInt(product.codeWindowMinutes),
      dotWeight: product.type === "microsoft" ? toNonNegativeInt(product.dotWeight) : 0,
      mainWeight: product.type === "microsoft" ? toNonNegativeInt(product.mainWeight) : 0,
      plusWeight: product.type === "microsoft" ? toNonNegativeInt(product.plusWeight) : 0,
      purchaseEnabled: product.purchaseEnabled,
      purchasePrice: normalizedMoney(product.purchasePrice),
      purchaseSupplierPrice: normalizedMoney(product.purchaseSupplierPrice),
      status: "enabled" as const,
      type: product.type,
      warrantyMinutes: toNonNegativeInt(product.warrantyMinutes),
    };
    if (
      !productRequest.codePrice ||
      !productRequest.purchasePrice ||
      !productRequest.codeSupplierPrice ||
      !productRequest.purchaseSupplierPrice
    ) {
      Toast.error(t("Price fields must be non-negative numbers."));
      return null;
    }
    products.push(productRequest);
  }

  return {
    accessType: draft.accessType,
    description: draft.description.trim(),
    logoUrl: draft.logoUrl.trim(),
    looseMatch: draft.looseMatch,
    mailRules,
    name: draft.name.trim(),
    products,
    targetPlatform: draft.targetPlatform.trim(),
  };
}

function InfoItem({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="min-w-0 rounded-lg bg-[var(--semi-color-fill-0)] px-3 py-2">
      <div className="mb-1 text-xs text-[var(--semi-color-text-2)]">{label}</div>
      <div className="break-words text-sm text-[var(--semi-color-text-0)]">{value}</div>
    </div>
  );
}

function ProductDraftCard({
  draft,
  onChange,
}: {
  draft: ProductDraft;
  onChange: (patch: Partial<ProductDraft>) => void;
}) {
  const { t } = useTranslation();
  const isMicrosoft = draft.type === "microsoft";
  const fields: Array<[keyof ProductDraft, string]> = [
    ["codePrice", t("Code price")],
    ["codeSupplierPrice", t("Code supplier price")],
    ["codeWindowMinutes", t("Code window minutes")],
    ["purchasePrice", t("Purchase price")],
    ["purchaseSupplierPrice", t("Purchase supplier price")],
    ["activationWindowMinutes", t("Activation window minutes")],
    ["warrantyMinutes", t("Warranty minutes label")],
  ];

  return (
    <div className="rounded-lg border border-[var(--semi-color-border)] p-3">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
        <Tag color={isMicrosoft ? "blue" : "green"} shape="circle">
          {productTypeLabel(draft.type, t)}
        </Tag>
        <Space wrap>
          <Checkbox
            checked={draft.codeEnabled}
            onChange={(event) => onChange({ codeEnabled: event.target.checked })}
          >
            {t("Code service")}
          </Checkbox>
          <Checkbox
            checked={draft.purchaseEnabled}
            onChange={(event) => onChange({ purchaseEnabled: event.target.checked })}
          >
            {t("Purchase service")}
          </Checkbox>
        </Space>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {fields.map(([key, label]) => (
          <label className="block" key={key}>
            <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
              {label}
            </span>
            <Input
              onChange={(value) => onChange({ [key]: String(value) } as Partial<ProductDraft>)}
              value={String(draft[key])}
            />
          </label>
        ))}
      </div>

      {isMicrosoft ? (
        <div className="mt-3 grid gap-3 sm:grid-cols-3">
          {[
            ["mainWeight", t("Main weight")],
            ["dotWeight", t("Dot weight")],
            ["plusWeight", t("Plus weight")],
          ].map(([key, label]) => (
            <label className="block" key={key}>
              <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
                {label}
              </span>
              <Input
                onChange={(value) => onChange({ [key]: String(value) } as Partial<ProductDraft>)}
                value={String(draft[key as keyof ProductDraft])}
              />
            </label>
          ))}
        </div>
      ) : null}
    </div>
  );
}

function MailRuleDraftList({
  onChange,
  value,
}: {
  onChange: (value: MailRuleDraft[]) => void;
  value: MailRuleDraft[];
}) {
  const { t } = useTranslation();

  const changeRule = (key: string, patch: Partial<MailRuleDraft>) => {
    onChange(
      value.map((rule) => {
        if (rule.key !== key) return rule;
        const nextRuleType = patch.ruleType ?? rule.ruleType;
        const nextPattern =
          patch.pattern ??
          (nextRuleType === "recipient"
            ? "exact"
            : rule.ruleType === "recipient"
              ? ".*"
              : rule.pattern);
        return { ...rule, ...patch, pattern: nextPattern };
      })
    );
  };

  const addRule = () => {
    onChange([...value, createMailRuleDraft({ ruleType: "sender" })]);
  };

  const removeRule = (key: string) => {
    if (value.length <= 1) return;
    onChange(value.filter((rule) => rule.key !== key));
  };

  return (
    <div className="space-y-2">
      {value.map((rule) => (
        <div
          className="grid gap-2 rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-bg-0)] p-3 sm:grid-cols-[132px_1fr_86px_64px]"
          key={rule.key}
        >
          <label className="block">
            <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
              {t("Rule type")}
            </span>
            <Select
              onChange={(nextValue) =>
                changeRule(rule.key, {
                  ruleType: String(nextValue) as ProjectMailRuleType,
                })
              }
              style={{ width: "100%" }}
              value={rule.ruleType}
            >
              {(["sender", "recipient", "subject", "body"] as ProjectMailRuleType[]).map(
                (ruleType) => (
                  <Select.Option key={ruleType} value={ruleType}>
                    {mailRuleTypeLabel(ruleType, t)}
                  </Select.Option>
                )
              )}
            </Select>
          </label>

          <label className="block">
            <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
              {rule.ruleType === "recipient" ? t("Recipient rule") : t("Match expression")}
            </span>
            {rule.ruleType === "recipient" ? (
              <Select
                onChange={(nextValue) =>
                  changeRule(rule.key, { pattern: String(nextValue) })
                }
                style={{ width: "100%" }}
                value={isRecipientPattern(rule.pattern) ? rule.pattern : "exact"}
              >
                {recipientPatterns.map((pattern) => (
                  <Select.Option key={pattern} value={pattern}>
                    {recipientPatternLabel(pattern, t)}
                  </Select.Option>
                ))}
              </Select>
            ) : (
              <Input
                maxLength={500}
                onChange={(nextValue) =>
                  changeRule(rule.key, { pattern: String(nextValue) })
                }
                placeholder={t("Match expression")}
                value={rule.pattern}
              />
            )}
          </label>

          <label className="flex items-end">
            <Checkbox
              checked={rule.enabled}
              onChange={(event) =>
                changeRule(rule.key, { enabled: event.target.checked })
              }
            >
              {t("Enabled")}
            </Checkbox>
          </label>

          <div className="flex items-end">
            <Button
              disabled={value.length <= 1}
              onClick={() => removeRule(rule.key)}
              size="small"
              type="danger"
            >
              {t("Delete")}
            </Button>
          </div>
        </div>
      ))}

      <Button onClick={addRule} size="small" type="tertiary">
        {t("Add rule")}
      </Button>
    </div>
  );
}

function readProjectLogoGallery() {
  if (typeof window === "undefined") return [] as string[];
  try {
    const parsed = JSON.parse(
      window.localStorage.getItem(projectLogoGalleryStorageKey) ?? "[]"
    ) as string[];
    return Array.isArray(parsed)
      ? parsed.filter((item) => typeof item === "string" && item.trim())
      : [];
  } catch {
    return [];
  }
}

function writeProjectLogoGallery(items: string[]) {
  if (typeof window === "undefined") return;
  window.localStorage.setItem(
    projectLogoGalleryStorageKey,
    JSON.stringify(Array.from(new Set(items)).slice(0, 120))
  );
}

function addProjectLogoToGallery(logoUrl: string) {
  const value = logoUrl.trim();
  if (!value) return readProjectLogoGallery();
  const next = [value, ...readProjectLogoGallery().filter((item) => item !== value)];
  writeProjectLogoGallery(next);
  return next;
}

function ProjectLogoPicker({
  gallery,
  onGalleryChange,
  onLogoChange,
  value,
}: {
  gallery: string[];
  onGalleryChange: (items: string[]) => void;
  onLogoChange: (value: string) => void;
  value: string;
}) {
  const { t } = useTranslation();
  const [gallerySearch, setGallerySearch] = useState("");
  const [uploading, setUploading] = useState(false);
  const filteredGallery = useMemo(() => {
    const keyword = gallerySearch.trim().toLowerCase();
    if (!keyword) return gallery;
    return gallery.filter((item) => item.toLowerCase().includes(keyword));
  }, [gallery, gallerySearch]);

  const uploadLogo = async (file?: File) => {
    if (!file) return;
    if (!file.type.startsWith("image/")) {
      Toast.error(t("Please upload an image file."));
      return;
    }
    setUploading(true);
    try {
      const response = await uploadAdminProjectLogo(file);
      onLogoChange(response.logoUrl);
      onGalleryChange(addProjectLogoToGallery(response.logoUrl));
    } catch {
      Toast.error(t("Logo upload failed."));
    } finally {
      setUploading(false);
    }
  };

  return (
    <div className="sm:col-span-2">
      <div className="mb-1 text-xs font-medium text-[var(--semi-color-text-1)]">
        {t("Logo")}
      </div>
      <div className="grid gap-3 rounded-lg border border-[var(--semi-color-border)] p-3 sm:grid-cols-[96px_1fr]">
        <div className="flex h-24 w-24 items-center justify-center overflow-hidden rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)]">
          {value ? (
            <img
              alt={t("Logo preview")}
              className="h-full w-full object-cover"
              src={value}
            />
          ) : (
            <span className="px-2 text-center text-xs text-[var(--semi-color-text-2)]">
              {t("No logo")}
            </span>
          )}
        </div>

        <div className="min-w-0 space-y-3">
          <Input
            maxLength={500}
            onChange={(nextValue) => onLogoChange(String(nextValue))}
            placeholder={t("Logo URL")}
            showClear
            value={value}
          />
          <div className="flex flex-wrap gap-2">
            <Button
              loading={uploading}
              onClick={() => document.getElementById("project-logo-upload")?.click()}
              size="small"
              type="tertiary"
            >
              {t("Upload logo")}
            </Button>
            <Button
              disabled={!value}
              onClick={() => onLogoChange("")}
              size="small"
              type="tertiary"
            >
              {t("Clear logo")}
            </Button>
            <input
              accept="image/*"
              className="hidden"
              id="project-logo-upload"
              onChange={(event) => {
                const file = event.currentTarget.files?.[0];
                event.currentTarget.value = "";
                void uploadLogo(file);
              }}
              type="file"
            />
          </div>
          <div>
            <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
              <div className="text-xs font-medium text-[var(--semi-color-text-2)]">
                {t("Logo gallery")}
                <span className="ml-1 font-normal text-[var(--semi-color-text-2)]">
                  {filteredGallery.length}/{gallery.length}
                </span>
              </div>
              <Input
                onChange={(nextValue) => setGallerySearch(String(nextValue))}
                placeholder={t("Search logos")}
                showClear
                size="small"
                style={{ width: 180 }}
                value={gallerySearch}
              />
            </div>
            <div className="max-h-[260px] overflow-y-auto rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] p-2">
              {filteredGallery.length > 0 ? (
                <div className="grid grid-cols-3 gap-2 sm:grid-cols-4 lg:grid-cols-5">
                  {filteredGallery.map((item) => (
                    <button
                      aria-label={t("Select logo")}
                      className={`group flex aspect-square min-h-[72px] cursor-pointer items-center justify-center overflow-hidden rounded-lg border bg-[var(--semi-color-bg-0)] transition-colors ${
                        item === value
                          ? "border-[var(--semi-color-primary)] ring-1 ring-[var(--semi-color-primary)]"
                          : "border-[var(--semi-color-border)] hover:border-[var(--semi-color-primary)]"
                      }`}
                      key={item}
                      onClick={() => onLogoChange(item)}
                      title={item}
                      type="button"
                    >
                      <img
                        alt={t("Logo")}
                        className="h-full w-full object-cover"
                        loading="lazy"
                        src={item}
                      />
                    </button>
                  ))}
                </div>
              ) : (
                <Empty
                  description={t("No logos found")}
                  style={{ padding: "24px 0" }}
                />
              )}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

function ProjectEditorSheet({
  detail,
  mode,
  onCancel,
  onSubmit,
  visible,
}: {
  detail: ProjectDetailResponse | null;
  mode: ProjectEditorMode;
  onCancel: () => void;
  onSubmit: (payload: AdminCreateProjectRequest | AdminUpdateProjectRequest) => Promise<void>;
  visible: boolean;
}) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [draft, setDraft] = useState<ProjectDraft>(initialDraft);
  const [logoGallery, setLogoGallery] = useState<string[]>([]);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (!visible) return;
    const nextDraft = mode === "create" ? initialDraft() : detailToDraft(detail);
    setDraft(nextDraft);
    setLogoGallery(
      Array.from(
        new Set(
          [nextDraft.logoUrl, sampleProjectCover, ...readProjectLogoGallery()].filter(Boolean)
        )
      )
    );
  }, [detail, mode, visible]);

  const setField = <K extends keyof ProjectDraft>(key: K, value: ProjectDraft[K]) => {
    setDraft((previous) => ({ ...previous, [key]: value }));
  };

  const toggleProductType = (type: ProjectProductType) => {
    setDraft((previous) => {
      const exists = previous.products.some((product) => product.type === type);
      return {
        ...previous,
        products: exists
          ? previous.products.filter((product) => product.type !== type)
          : [...previous.products, createDefaultProduct(type)],
      };
    });
  };

  const submit = async () => {
    const payload = buildProjectPayload(draft, t);
    if (!payload) return;

    setSubmitting(true);
    try {
      await onSubmit(payload);
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <SideSheet
      bodyStyle={{ padding: 0 }}
      onCancel={onCancel}
      placement="right"
      title={
        mode === "approve" && detail
          ? `${t("Approve project")} #${detail.project.id}`
          : mode === "edit" && detail
          ? `${t("Edit project")} #${detail.project.id}`
          : t("New project")
      }
      visible={visible}
      width={isMobile ? "100%" : 720}
    >
      <div className="flex min-h-full flex-col">
        <div className="flex-1 space-y-5 p-5">
          <section>
            <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">
              {t("Basic info")}
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <label className="block">
                <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
                  {t("Project name")} *
                </span>
                <Input
                  maxLength={120}
                  onChange={(value) => setField("name", String(value))}
                  value={draft.name}
                />
              </label>
              <label className="block">
                <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
                  {t("Target platform")} *
                </span>
                <Input
                  maxLength={120}
                  onChange={(value) => setField("targetPlatform", String(value))}
                  value={draft.targetPlatform}
                />
              </label>
              <label className="block">
                <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
                  {t("Private")}
                </span>
                <Select
                  onChange={(value) =>
                    setField("accessType", String(value) as "public" | "private")
                  }
                  style={{ width: "100%" }}
                  value={draft.accessType}
                >
                  <Select.Option value="private">{t("Yes")}</Select.Option>
                  <Select.Option value="public">{t("No")}</Select.Option>
                </Select>
              </label>
              <label className="block">
                <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
                  {t("Loose")}
                </span>
                <Select
                  onChange={(value) => setField("looseMatch", String(value) === "yes")}
                  style={{ width: "100%" }}
                  value={draft.looseMatch ? "yes" : "no"}
                >
                  <Select.Option value="yes">{t("Yes")}</Select.Option>
                  <Select.Option value="no">{t("No")}</Select.Option>
                </Select>
              </label>
              <ProjectLogoPicker
                gallery={logoGallery}
                onGalleryChange={setLogoGallery}
                onLogoChange={(value) => setField("logoUrl", value)}
                value={draft.logoUrl}
              />
              <label className="block sm:col-span-2">
                <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
                  {t("Description")}
                </span>
                <TextArea
                  maxCount={1000}
                  onChange={(value) => setField("description", String(value))}
                  rows={3}
                  value={draft.description}
                />
              </label>
            </div>
          </section>

          <section>
            <div className="mb-3 flex items-center justify-between gap-2">
              <div className="text-sm font-semibold text-[var(--semi-color-text-0)]">
                {t("Products")}
              </div>
              <Space wrap>
                {(["microsoft", "domain"] as ProjectProductType[]).map((type) => {
                  const active = draft.products.some((product) => product.type === type);
                  return (
                    <Button
                      key={type}
                      onClick={() => toggleProductType(type)}
                      size="small"
                      theme={active ? "solid" : "outline"}
                      type={active ? "primary" : "tertiary"}
                    >
                      {productTypeLabel(type, t)}
                    </Button>
                  );
                })}
              </Space>
            </div>
            <div className="space-y-3">
              {draft.products.map((product) => (
                <ProductDraftCard
                  draft={product}
                  key={product.type}
                  onChange={(patch) =>
                    setField("products", updateProduct(draft.products, product.type, patch))
                  }
                />
              ))}
            </div>
          </section>

          <section>
            <div className="mb-3 text-sm font-semibold text-[var(--semi-color-text-0)]">
              {t("Mail rules")}
            </div>
            <MailRuleDraftList
              onChange={(value) => setField("mailRules", value)}
              value={draft.mailRules}
            />
          </section>
        </div>

        <div className="sticky bottom-0 flex items-center justify-end gap-2 border-t border-[var(--semi-color-border)] bg-[var(--semi-color-bg-0)] px-5 py-3">
          <Button onClick={onCancel} type="tertiary">
            {t("Cancel")}
          </Button>
          <Button loading={submitting} onClick={() => void submit()} type="primary">
            {mode === "approve"
              ? t("Approve and list")
              : mode === "edit"
                ? t("Save")
                : t("Create project")}
          </Button>
        </div>
      </div>
    </SideSheet>
  );
}

function ProjectDetailSheet({
  detail,
  onCancel,
}: {
  detail: ProjectDetailResponse | null;
  onCancel: () => void;
}) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [accessItems, setAccessItems] = useState<ProjectAccess[]>([]);
  const [grantUserID, setGrantUserID] = useState("");
  const [accessOperating, setAccessOperating] = useState(false);

  useEffect(() => {
    setAccessItems(detail?.accesses ?? []);
    setGrantUserID("");
  }, [detail]);

  const refreshAccesses = async () => {
    if (!detail) return;
    const response = await listAdminProjectAccess(detail.project.id);
    setAccessItems(response.items ?? []);
  };

  const grantAccess = async () => {
    if (!detail) return;
    const userID = Number(grantUserID.trim());
    if (!Number.isInteger(userID) || userID <= 0) {
      Toast.error(t("Invalid user ID."));
      return;
    }
    setAccessOperating(true);
    try {
      await grantAdminProjectAccess(detail.project.id, userID);
      await refreshAccesses();
      setGrantUserID("");
      Toast.success(t("Project access granted."));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Project operation failed."));
    } finally {
      setAccessOperating(false);
    }
  };

  const revokeAccess = async (userID: number) => {
    if (!detail) return;
    setAccessOperating(true);
    try {
      await revokeAdminProjectAccess(detail.project.id, userID);
      await refreshAccesses();
      Toast.success(t("Project access revoked."));
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Project operation failed."));
    } finally {
      setAccessOperating(false);
    }
  };

  return (
    <SideSheet
      bodyStyle={{ padding: 0 }}
      onCancel={onCancel}
      placement="right"
      title={detail ? `${t("Project detail")} #${detail.project.id}` : t("Project detail")}
      visible={Boolean(detail)}
      width={isMobile ? "100%" : 640}
    >
      {detail ? (
        <div className="space-y-5 p-5">
          <div className="grid gap-3 sm:grid-cols-2">
            <InfoItem label={t("Project name")} value={detail.project.name} />
            <InfoItem label={t("Target platform")} value={detail.project.targetPlatform} />
            <InfoItem label={t("Status")} value={statusTag(detail.project.status, t)} />
            <InfoItem label={t("Private")} value={booleanTag(detail.project.accessType === "private", t)} />
            <InfoItem label={t("Loose")} value={booleanTag(detail.project.looseMatch, t)} />
            <InfoItem label={t("Updated at")} value={formatTime(detail.project.updatedAt)} />
          </div>
          <InfoItem label={t("Description")} value={detail.project.description || "-"} />
          {detail.project.reviewReason ? (
            <InfoItem label={t("Review reason")} value={detail.project.reviewReason} />
          ) : null}

          {detail.project.accessType === "private" ? (
            <section>
              <div className="mb-2 text-sm font-semibold">{t("Authorized users")}</div>
              <div className="mb-3 flex gap-2">
                <Input
                  onChange={(value) => setGrantUserID(String(value))}
                  placeholder={t("User ID")}
                  value={grantUserID}
                />
                <Button
                  loading={accessOperating}
                  onClick={() => void grantAccess()}
                  type="primary"
                >
                  {t("Grant access")}
                </Button>
              </div>
              <div className="grid gap-2">
                {accessItems.length > 0 ? (
                  accessItems.map((access) => (
                    <div
                      className="flex items-center justify-between rounded-lg border border-[var(--semi-color-border)] p-3"
                      key={`${access.projectId}-${access.userId}`}
                    >
                      <div className="min-w-0">
                        <div className="font-mono text-sm">#{access.userId}</div>
                        <div className="text-xs text-[var(--semi-color-text-2)]">
                          {t("Granted by")} #{access.grantedBy}
                        </div>
                      </div>
                      <Button
                        loading={accessOperating}
                        onClick={() => void revokeAccess(access.userId)}
                        size="small"
                        type="danger"
                      >
                        {t("Revoke")}
                      </Button>
                    </div>
                  ))
                ) : (
                  <Empty description={t("No authorized users")} style={{ padding: 24 }} />
                )}
              </div>
            </section>
          ) : null}

          <section>
            <div className="mb-2 text-sm font-semibold">{t("Products")}</div>
            <div className="grid gap-2">
              {detail.products.map((product) => (
                <div
                  className="rounded-lg border border-[var(--semi-color-border)] p-3"
                  key={product.id}
                >
                  <div className="mb-2 flex flex-wrap items-center gap-2">
                    <Tag color={product.type === "microsoft" ? "blue" : "green"} shape="circle">
                      {productTypeLabel(product.type, t)}
                    </Tag>
                    <Tag color={product.status === "enabled" ? "green" : "grey"} shape="circle">
                      {product.status === "enabled" ? t("Enabled") : t("Disabled")}
                    </Tag>
                  </div>
                  <div className="grid gap-2 text-sm sm:grid-cols-3">
                    <InfoItem
                      label={t("Code price")}
                      value={product.codeEnabled ? product.codePrice : t("Disabled")}
                    />
                    <InfoItem
                      label={t("Purchase price")}
                      value={product.purchaseEnabled ? product.purchasePrice : t("Disabled")}
                    />
                    <InfoItem
                      label={t("Service window")}
                      value={`${product.codeWindowMinutes}/${product.activationWindowMinutes}/${product.warrantyMinutes}`}
                    />
                  </div>
                </div>
              ))}
            </div>
          </section>

          <section>
            <div className="mb-2 text-sm font-semibold">{t("Mail rules")}</div>
            <div className="grid gap-2 sm:grid-cols-2">
              {(detail.mailRules ?? []).map((rule) => (
                <div
                  className="rounded-lg border border-[var(--semi-color-border)] p-3"
                  key={rule.id}
                >
                  <div className="mb-1 flex items-center justify-between">
                    <Tag color={rule.enabled ? "green" : "grey"} shape="circle">
                      {t(rule.ruleType)}
                    </Tag>
                    <Text size="small" type="tertiary">
                      {rule.enabled ? t("Enabled") : t("Disabled")}
                    </Text>
                  </div>
                  <Text className="break-all font-mono" size="small">
                    {rule.ruleType === "recipient"
                      ? recipientPatternLabel(rule.pattern, t)
                      : rule.pattern}
                  </Text>
                </div>
              ))}
            </div>
          </section>
        </div>
      ) : null}
    </SideSheet>
  );
}

export default function AdminProjects() {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [items, setItems] = useState<ProjectItem[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [searchKeyword, setSearchKeyword] = useState("");
  const [createdAtRange, setCreatedAtRange] = useState<DateRangeValue>([]);
  const [statusFilter, setStatusFilter] = useState<ProjectStatusFilter>("all");
  const [privateFilter, setPrivateFilter] = useState<BooleanFilter>("all");
  const [looseFilter, setLooseFilter] = useState<BooleanFilter>("all");
  const [productTypeFilter, setProductTypeFilter] = useState<ProjectProductTypeFilter>("all");
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();
  const [compactMode, setCompactMode] = useState(false);
  const [selectedKeys, setSelectedKeys] = useState<number[]>([]);
  const [detail, setDetail] = useState<ProjectDetailResponse | null>(null);
  const [editorMode, setEditorMode] = useState<ProjectEditorMode>("create");
  const [editorDetail, setEditorDetail] = useState<ProjectDetailResponse | null>(null);
  const [editorOpen, setEditorOpen] = useState(false);
  const [operatingProjectID, setOperatingProjectID] = useState<number | null>(null);
  const [bulkOperating, setBulkOperating] = useState<"relist" | "delist" | "delete" | null>(null);

  const listFilter = useMemo<ProjectListFilter>(() => {
    const filter: ProjectListFilter = { scope: "all" };
    const search = searchKeyword.trim();
    const createdFrom = createdFromISOString(createdAtRange);
    const createdTo = createdToISOString(createdAtRange);
    if (search) filter.search = search;
    if (statusFilter !== "all") filter.status = statusFilter;
    if (privateFilter !== "all") filter.accessType = privateFilter === "yes" ? "private" : "public";
    if (looseFilter !== "all") filter.looseMatch = looseFilter === "yes";
    if (productTypeFilter !== "all") filter.productType = productTypeFilter;
    if (createdFrom) filter.createdFrom = createdFrom;
    if (createdTo) filter.createdTo = createdTo;
    return filter;
  }, [createdAtRange, looseFilter, privateFilter, productTypeFilter, searchKeyword, statusFilter]);

  const dateRangePresets = useMemo(() => createDateRangePresets(t), [t]);

  const activeFilterCount =
    (statusFilter !== "all" ? 1 : 0) +
    (privateFilter !== "all" ? 1 : 0) +
    (looseFilter !== "all" ? 1 : 0) +
    (productTypeFilter !== "all" ? 1 : 0);

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const response = await listProjects(
        listFilter,
        (activePage - 1) * pageSize,
        pageSize
      );
      setItems(response.items);
      setTotal(response.total);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Projects load failed."));
    } finally {
      setLoading(false);
    }
  }, [activePage, listFilter, pageSize, t]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const resetFilters = () => {
    setSearchKeyword("");
    setCreatedAtRange([]);
    setStatusFilter("all");
    setPrivateFilter("all");
    setLooseFilter("all");
    setProductTypeFilter("all");
    setSelectedKeys([]);
    setActivePage(1);
  };

  const openDetail = async (projectID: number) => {
    try {
      const response = await getProject(projectID);
      setDetail(response);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Project detail load failed."));
    }
  };

  const openCreate = () => {
    setEditorMode("create");
    setEditorDetail(null);
    setEditorOpen(true);
  };

  const openEdit = async (projectID: number) => {
    try {
      const response = await getProject(projectID);
      setEditorMode("edit");
      setEditorDetail(response);
      setEditorOpen(true);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Project detail load failed."));
    }
  };

  const handleEditorSubmit = async (
    payload: AdminCreateProjectRequest | AdminUpdateProjectRequest
  ) => {
    try {
      if (editorMode === "create") {
        await createAdminProject(payload);
        Toast.success(t("Project created."));
        setActivePage(1);
      } else if (editorMode === "approve" && editorDetail) {
        await approveAdminProject(editorDetail.project.id, payload);
        Toast.success(t("Project approved."));
        setSelectedKeys([]);
      } else if (editorDetail) {
        await updateAdminProject(editorDetail.project.id, payload);
        Toast.success(t("Project updated."));
      }
      setEditorOpen(false);
      setEditorDetail(null);
      await refresh();
    } catch (error) {
      Toast.error(
        getIamErrorMessage(
          t,
          error,
          editorMode === "create"
            ? "Project create failed."
            : editorMode === "approve"
              ? "Project approve failed."
              : "Project update failed."
        )
      );
    }
  };

  const operateProject = async (
    projectID: number,
    action: () => Promise<unknown>,
    successKey: string
  ) => {
    setOperatingProjectID(projectID);
    try {
      await action();
      Toast.success(t(successKey));
      setSelectedKeys([]);
      await refresh();
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Project operation failed."));
    } finally {
      setOperatingProjectID(null);
    }
  };

  const handleApproveProject = async (record: ProjectItem) => {
    setOperatingProjectID(record.id);
    try {
      const response = await getProject(record.id);
      setEditorMode("approve");
      setEditorDetail(response);
      setEditorOpen(true);
    } catch (error) {
      Toast.error(getIamErrorMessage(t, error, "Project detail load failed."));
    } finally {
      setOperatingProjectID(null);
    }
  };

  const confirmRejectProject = (record: ProjectItem) => {
    let reviewReason = "";
    Modal.confirm({
      cancelText: t("Cancel"),
      content: (
        <TextArea
          autosize
          maxCount={500}
          onChange={(value) => {
            reviewReason = String(value);
          }}
          placeholder={t("Please enter review reason")}
          rows={4}
        />
      ),
      okText: t("Reject"),
      onOk: async () => {
        if (!reviewReason.trim()) {
          Toast.error(t("Review reason is required."));
          throw new Error("review reason required");
        }
        await operateProject(
          record.id,
          () => rejectAdminProject(record.id, reviewReason),
          "Project rejected."
        );
      },
      title: t("Reject project"),
    });
  };

  const confirmDeleteProject = (record: ProjectItem) => {
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm delete project content"),
      okText: t("Delete"),
      okButtonProps: { type: "danger" },
      onOk: () =>
        operateProject(
          record.id,
          () => deleteAdminProject(record.id),
          "Project deleted."
        ),
      title: t("Delete project"),
    });
  };

  const confirmBulk = (
    type: "relist" | "delist" | "delete",
    titleKey: string,
    contentKey: string,
    action: () => Promise<{ affected: number }>
  ) => {
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t(contentKey),
      okText:
        type === "delete"
          ? t("Delete")
          : type === "relist"
            ? t("Relist")
            : t("Delist"),
      okButtonProps: type === "delete" ? { type: "danger" } : undefined,
      onOk: async () => {
        setBulkOperating(type);
        try {
          const response = await action();
          Toast.success(t("Projects bulk operation completed.", { count: response.affected }));
          setSelectedKeys([]);
          setActivePage(1);
          await refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Project operation failed."));
        } finally {
          setBulkOperating(null);
        }
      },
      title: t(titleKey),
    });
  };

  const selectedIDSet = useMemo(() => new Set(selectedKeys), [selectedKeys]);
  const selectedListedProjectIDs = useMemo(
    () =>
      items
        .filter((item) => selectedIDSet.has(item.id) && item.status === "listed")
        .map((item) => item.id),
    [items, selectedIDSet]
  );
  const selectedDelistedProjectIDs = useMemo(
    () =>
      items
        .filter((item) => selectedIDSet.has(item.id) && item.status === "delisted")
        .map((item) => item.id),
    [items, selectedIDSet]
  );
  const selectedDeletableProjectIDs = useMemo(
    () =>
      items
        .filter((item) => selectedIDSet.has(item.id) && item.status !== "reviewing")
        .map((item) => item.id),
    [items, selectedIDSet]
  );

  const confirmRelistSelected = useCallback(() => {
    if (selectedDelistedProjectIDs.length === 0) {
      Toast.info(t("No selected projects to relist."));
      return;
    }
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm relist selected projects content", {
        count: selectedDelistedProjectIDs.length,
      }),
      okText: t("Relist"),
      onOk: async () => {
        setBulkOperating("relist");
        try {
          const response = await relistAdminProjectsByIds(selectedDelistedProjectIDs);
          Toast.success(
            t("Projects bulk operation completed.", {
              count: response.affected,
            })
          );
          setSelectedKeys([]);
          await refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Project operation failed."));
        } finally {
          setBulkOperating(null);
        }
      },
      title: t("Confirm relist selected"),
    });
  }, [refresh, selectedDelistedProjectIDs, t]);

  const confirmDelistSelected = useCallback(() => {
    if (selectedListedProjectIDs.length === 0) {
      Toast.info(t("No selected projects to delist."));
      return;
    }
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm delist selected projects content", {
        count: selectedListedProjectIDs.length,
      }),
      okText: t("Delist"),
      onOk: async () => {
        setBulkOperating("delist");
        try {
          const response = await delistAdminProjectsByIds(selectedListedProjectIDs);
          Toast.success(
            t("Projects bulk operation completed.", {
              count: response.affected,
            })
          );
          setSelectedKeys([]);
          await refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Project operation failed."));
        } finally {
          setBulkOperating(null);
        }
      },
      title: t("Confirm delist selected"),
    });
  }, [refresh, selectedListedProjectIDs, t]);

  const confirmDeleteSelected = useCallback(() => {
    if (selectedDeletableProjectIDs.length === 0) {
      Toast.info(t("No selected projects to delete."));
      return;
    }
    Modal.confirm({
      cancelText: t("Cancel"),
      content: t("Confirm delete selected projects content", {
        count: selectedDeletableProjectIDs.length,
      }),
      okButtonProps: { type: "danger" },
      okText: t("Delete"),
      onOk: async () => {
        setBulkOperating("delete");
        try {
          const response = await deleteAdminProjectsByIds(selectedDeletableProjectIDs);
          Toast.success(
            t("Projects bulk operation completed.", {
              count: response.affected,
            })
          );
          setSelectedKeys([]);
          await refresh();
        } catch (error) {
          Toast.error(getIamErrorMessage(t, error, "Project operation failed."));
        } finally {
          setBulkOperating(null);
        }
      },
      title: t("Confirm delete selected"),
    });
  }, [refresh, selectedDeletableProjectIDs, t]);

  useSelectionNotification({
    selectedCount: selectedKeys.length,
    onCheck: confirmRelistSelected,
    onClear: () => setSelectedKeys([]),
    onDelete: selectedDeletableProjectIDs.length > 0 ? confirmDeleteSelected : undefined,
    onSell: confirmDelistSelected,
    checkLabelKey: "Relist",
    deleteLabelKey: "Delete",
    sellLabelKey: "Delist",
    checkLoading: bulkOperating === "relist",
    deleteLoading: bulkOperating === "delete",
    sellLoading: bulkOperating === "delist",
    selectionDescriptionKey: "Selected projects",
    t,
  });

  const rowSelection = {
    selectedRowKeys: selectedKeys,
    onChange: (keys: Array<string | number>) => {
      setSelectedKeys(keys.map((key) => Number(key)));
    },
  };

  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "id",
          key: "id",
          title: t("Project ID"),
          width: "5%",
          render: (_: unknown, record: ProjectItem) => (
            <span className="font-mono text-[var(--semi-color-text-1)]">#{record.id}</span>
          ),
        },
        {
          dataIndex: "logoUrl",
          key: "logoUrl",
          title: t("Logo"),
          width: "5%",
          render: (_: unknown, record: ProjectItem) => {
            const initial = (record.name || "-").trim().slice(0, 1).toUpperCase() || "-";

            return (
              <div className="relative flex h-9 w-9 shrink-0 items-center justify-center overflow-hidden rounded-lg border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] text-sm font-semibold text-[var(--semi-color-text-2)]">
                <span>{initial}</span>
                {record.logoUrl ? (
                  <img
                    alt={`${record.name} ${t("Logo")}`}
                    className="absolute inset-0 h-full w-full object-cover"
                    onError={(event) => {
                      event.currentTarget.style.display = "none";
                    }}
                    src={record.logoUrl}
                  />
                ) : null}
              </div>
            );
          },
        },
        {
          dataIndex: "name",
          key: "name",
          title: t("Project name"),
          width: "24%",
          render: (_: unknown, record: ProjectItem) => (
            <div className="min-w-0">
              <div className="truncate font-medium text-[var(--semi-color-text-0)]">
                {record.name}
              </div>
              <div className="truncate text-xs text-[var(--semi-color-text-2)]">
                {record.description || "-"}
              </div>
            </div>
          ),
        },
        {
          dataIndex: "status",
          key: "status",
          title: t("Status"),
          width: "8%",
          render: (value: unknown) => statusTag(String(value), t),
        },
        {
          dataIndex: "accessType",
          key: "accessType",
          title: t("Private"),
          width: "7%",
          render: (value: unknown) => booleanTag(String(value) === "private", t),
        },
        {
          dataIndex: "looseMatch",
          key: "looseMatch",
          title: t("Loose"),
          width: "7%",
          render: (value: unknown) => booleanTag(Boolean(value), t),
        },
        {
          dataIndex: "products",
          key: "products",
          title: t("Products"),
          width: "11%",
          render: (_: unknown, record: ProjectItem) => (
            <Space spacing={4} wrap>
              {(record.products ?? []).map((product) => (
                <Tag key={`${record.id}-${product.type}`} shape="circle" size="small">
                  {productTypeLabel(product.type, t)}
                </Tag>
              ))}
            </Space>
          ),
        },
        {
          dataIndex: "mailRuleCount",
          key: "mailRuleCount",
          title: t("Rules"),
          width: "6%",
          render: (value: unknown) => <span className="font-mono">{String(value)}</span>,
        },
        {
          dataIndex: "operate",
          fixed: "right",
          key: "operate",
          title: t("Actions"),
          width: 250,
          render: (_: unknown, record: ProjectItem) => {
            const rowLoading = operatingProjectID === record.id;
            return (
              <Space spacing={4} wrap={false}>
                <Button
                  onClick={() => void openDetail(record.id)}
                  size="small"
                  type="tertiary"
                >
                  {t("Details")}
                </Button>
                {record.status !== "reviewing" ? (
                  <Button
                    onClick={() => void openEdit(record.id)}
                    size="small"
                    type="tertiary"
                  >
                    {t("Edit")}
                  </Button>
                ) : null}
                {record.status === "reviewing" ? (
                  <>
                    <Button
                      loading={rowLoading}
                      onClick={() => void handleApproveProject(record)}
                      size="small"
                      type="tertiary"
                    >
                      {t("Approve")}
                    </Button>
                    <Button
                      onClick={() => confirmRejectProject(record)}
                      size="small"
                      type="danger"
                    >
                      {t("Reject")}
                    </Button>
                  </>
                ) : null}
                {record.status === "delisted" ? (
                  <Button
                    loading={rowLoading}
                    onClick={() =>
                      void operateProject(
                        record.id,
                        () => relistAdminProject(record.id),
                        "Project relisted."
                      )
                    }
                    size="small"
                    type="tertiary"
                  >
                    {t("Relist")}
                  </Button>
                ) : null}
                {record.status === "listed" ? (
                  <Button
                    loading={rowLoading}
                    onClick={() =>
                      void operateProject(
                        record.id,
                        () => delistAdminProject(record.id),
                        "Project delisted."
                      )
                    }
                    size="small"
                    type="tertiary"
                  >
                    {t("Delist")}
                  </Button>
                ) : null}
                {record.status !== "reviewing" ? (
                  <Button
                    onClick={() => confirmDeleteProject(record)}
                    size="small"
                    type="danger"
                  >
                    {t("Delete")}
                  </Button>
                ) : null}
              </Space>
            );
          },
        },
      ] as any[],
    [operatingProjectID, t]
  );

  const tableColumns = useMemo(() => {
    if (!compactMode) return columns;
    return columns.map((column) => {
      if (column.dataIndex !== "operate") return column;
      const { fixed: _fixed, ...rest } = column;
      return rest;
    });
  }, [columns, compactMode]);

  const actionsArea = (
    <div className="flex w-full flex-col items-center justify-between gap-2 md:flex-row">
      <div className="order-2 flex w-full flex-wrap gap-2 md:order-1 md:w-auto">
        <Button
          className="flex-1 md:flex-initial"
          onClick={openCreate}
          size="small"
          type="primary"
        >
          {t("Import")}
        </Button>
        <Button
          className="remail-toolbar-fixed-button flex-1 md:flex-none"
          loading={loading}
          onClick={() => void refresh()}
          size="small"
          type="tertiary"
        >
          {t("Refresh")}
        </Button>
        <Button
          className="flex-1 md:flex-initial"
          loading={bulkOperating === "relist"}
          onClick={() =>
            confirmBulk(
              "relist",
              "Confirm relist all",
              "Confirm relist all matching projects content",
              () => relistAdminProjectsByFilter(listFilter)
            )
          }
          size="small"
          type="tertiary"
        >
          {t("Relist all")}
        </Button>
        <Button
          className="flex-1 md:flex-initial"
          loading={bulkOperating === "delist"}
          onClick={() =>
            confirmBulk(
              "delist",
              "Confirm delist all",
              "Confirm delist all matching projects content",
              () => delistAdminProjectsByFilter(listFilter)
            )
          }
          size="small"
          type="tertiary"
        >
          {t("Delist all")}
        </Button>
        {statusFilter !== "reviewing" ? (
          <Button
            className="flex-1 md:flex-initial"
            loading={bulkOperating === "delete"}
            onClick={() =>
              confirmBulk(
                "delete",
                "Confirm delete all",
                "Confirm delete all matching projects content",
                () => deleteAdminProjectsByFilter(listFilter)
              )
            }
            size="small"
            type="danger"
          >
            {t("Delete all")}
          </Button>
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
            <div className="w-[280px] space-y-3 p-3">
              <label className="block">
                <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-2)]">
                  {t("Status")}
                </span>
                <Select
                  onChange={(value) => {
                    setStatusFilter(String(value) as ProjectStatusFilter);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  size="small"
                  style={{ width: "100%" }}
                  value={statusFilter}
                >
                  <Select.Option value="all">{t("All statuses")}</Select.Option>
                  <Select.Option value="listed">{t("Listed")}</Select.Option>
                  <Select.Option value="reviewing">{t("Reviewing")}</Select.Option>
                  <Select.Option value="delisted">{t("Delisted")}</Select.Option>
                </Select>
              </label>
              <label className="block">
                <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-2)]">
                  {t("Private")}
                </span>
                <Select
                  onChange={(value) => {
                    setPrivateFilter(String(value) as BooleanFilter);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  size="small"
                  style={{ width: "100%" }}
                  value={privateFilter}
                >
                  <Select.Option value="all">{t("All")}</Select.Option>
                  <Select.Option value="yes">{t("Yes")}</Select.Option>
                  <Select.Option value="no">{t("No")}</Select.Option>
                </Select>
              </label>
              <label className="block">
                <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-2)]">
                  {t("Loose")}
                </span>
                <Select
                  onChange={(value) => {
                    setLooseFilter(String(value) as BooleanFilter);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  size="small"
                  style={{ width: "100%" }}
                  value={looseFilter}
                >
                  <Select.Option value="all">{t("All")}</Select.Option>
                  <Select.Option value="yes">{t("Yes")}</Select.Option>
                  <Select.Option value="no">{t("No")}</Select.Option>
                </Select>
              </label>
              <label className="block">
                <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-2)]">
                  {t("Product type")}
                </span>
                <Select
                  onChange={(value) => {
                    setProductTypeFilter(String(value) as ProjectProductTypeFilter);
                    setActivePage(1);
                    setSelectedKeys([]);
                  }}
                  size="small"
                  style={{ width: "100%" }}
                  value={productTypeFilter}
                >
                  <Select.Option value="all">{t("All")}</Select.Option>
                  <Select.Option value="microsoft">{t("Microsoft email")}</Select.Option>
                  <Select.Option value="domain">{t("Domain email")}</Select.Option>
                </Select>
              </label>
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
          placeholder={t("Search project or platform")}
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
              setActivePage(1);
              void refresh();
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
    total,
    t,
  });

  return (
    <div className="px-2 pt-5">
      <CardPro
        actionsArea={actionsArea}
        paginationArea={paginationArea}
        t={t}
        type="type3"
      >
        <CardTable
          className="overflow-hidden rounded-xl"
          columns={tableColumns}
          dataSource={items}
          empty={
            <Empty
              description={t("No projects found")}
              style={{ padding: 30 }}
            />
          }
          hidePagination
          loading={loading}
          pagination={false}
          rowKey="id"
          rowSelection={rowSelection}
          scroll={compactMode ? undefined : { x: "max(100%, 1320px)" }}
          size="middle"
        />
      </CardPro>

      <ProjectEditorSheet
        detail={editorDetail}
        mode={editorMode}
        onCancel={() => {
          setEditorOpen(false);
          setEditorDetail(null);
        }}
        onSubmit={handleEditorSubmit}
        visible={editorOpen}
      />
      <ProjectDetailSheet detail={detail} onCancel={() => setDetail(null)} />
    </div>
  );
}
