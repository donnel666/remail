import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Button,
  Card,
  Divider,
  Empty,
  Input,
  Modal,
  Pagination,
  Select,
  SideSheet,
  Space,
  Spin,
  Tag,
  Table,
  TextArea,
  Toast,
  Typography,
} from "@douyinfe/semi-ui";
import { IconSearch } from "@douyinfe/semi-icons";
import {
  CirclePlus,
  Eye,
  ExternalLink,
  FolderOpen,
  Globe2,
  LayoutGrid,
  RefreshCw,
  ShoppingCart,
  Table2,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import { useBlockPagedList } from "@/hooks/use-block-paged-list";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { useIsMobile } from "@/hooks/use-is-mobile";
import { useSharedPageSize } from "@/hooks/use-shared-page-size";
import { getIamErrorMessage } from "@/lib/iam-errors";
import {
  createProjectApplication,
  getProject,
  listProjects,
  resubmitProjectApplication,
  type CreateProjectApplicationRequest,
  type ProjectDetailResponse,
  type ProjectItem,
  type ProjectListFilter,
  type ProjectMailRule,
  type ProjectProduct,
  type ProjectProductSummary,
} from "@/lib/projects-api";

const { Text } = Typography;

type AccessFilter = "all" | "public" | "private";
type MatchFilter = "all" | "loose" | "strict";
type ProductTypeFilter = "all" | ProjectProductSummary["type"];
type StatusFilter = "all" | "listed" | "reviewing" | "rejected";
type ApplyModalMode = "create" | "resubmit";
type ProjectSquareViewMode = "card" | "table";

interface ApplyFormState {
  codeMaxPrice: string;
  codeMinPrice: string;
  emailTypes: string[];
  mailBody: string;
  mailSubject: string;
  projectName: string;
  projectURL: string;
  purchaseMaxPrice: string;
  purchaseMinPrice: string;
  remarks: string;
  senderPattern: string;
}

const initialApplyForm: ApplyFormState = {
  codeMaxPrice: "",
  codeMinPrice: "",
  emailTypes: ["microsoft"],
  mailBody: "",
  mailSubject: "",
  projectName: "",
  projectURL: "",
  purchaseMaxPrice: "",
  purchaseMinPrice: "",
  remarks: "",
  senderPattern: "",
};

const applicationDescriptionLabels = {
  codePriceExpectation: ["Code price expectation", "接码价格期望"],
  expectedEmailTypes: ["Expected email types", "期望邮箱类型"],
  mailBody: ["Mail body", "邮件正文"],
  mailSubject: ["Mail subject", "邮件主题"],
  projectURL: ["Project URL", "项目链接"],
  purchasePriceExpectation: ["Purchase price expectation", "购买价格期望"],
  remarks: ["Remarks", "备注"],
  senderPattern: ["Sender email", "发件人邮箱"],
};

function extractTargetPlatform(projectURL: string, projectName: string) {
  const value = projectURL.trim();
  if (!value) return projectName.trim();
  try {
    const parsed = new URL(value.startsWith("http") ? value : `https://${value}`);
    return parsed.hostname.replace(/^www\./, "");
  } catch {
    return value.replace(/^https?:\/\//, "").replace(/^www\./, "").split("/")[0];
  }
}

function formatTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "-";
  return date.toLocaleString();
}

function money(value?: string) {
  const numberValue = Number(value ?? 0);
  if (!Number.isFinite(numberValue) || numberValue <= 0) return "-";
  return numberValue.toFixed(3).replace(/\.?0+$/, "");
}

function productTypeLabel(type: string, t: (key: string) => string) {
  if (type === "microsoft") return t("Microsoft email");
  if (type === "domain") return t("Domain email");
  return type;
}

function isRejectedApplication(project: ProjectItem) {
  return project.status === "delisted" && Boolean(project.applicantUserId);
}

function isOrderableProject(project: ProjectItem) {
  return project.status === "listed";
}

function statusFilterToProjectStatus(statusFilter: StatusFilter) {
  if (statusFilter === "all") return undefined;
  if (statusFilter === "rejected") return "delisted";
  return statusFilter;
}

function accessTag(accessType: string, t: (key: string) => string) {
  return (
    <Tag color={accessType === "private" ? "purple" : "green"} shape="circle">
      {accessType === "private" ? t("Private project") : t("Public project")}
    </Tag>
  );
}

function projectStatusTag(project: ProjectItem, t: (key: string) => string) {
  if (isRejectedApplication(project)) {
    return (
      <Tag color="red" shape="circle">
        {t("Application rejected")}
      </Tag>
    );
  }
  if (project.status === "reviewing") {
    return (
      <Tag color="amber" shape="circle">
        {t("Application pending")}
      </Tag>
    );
  }
  if (project.status === "delisted") {
    return (
      <Tag color="grey" shape="circle">
        {t("Delisted")}
      </Tag>
    );
  }
  return null;
}

function projectInitial(name: string) {
  return (name.trim()[0] || "P").toUpperCase();
}

function projectLinkFromTargetPlatform(targetPlatform: string) {
  const value = targetPlatform.trim();
  if (!value) return "";
  return /^https?:\/\//i.test(value) ? value : `https://${value}`;
}

function parseApplicationDescription(description?: string) {
  const result: Partial<Record<keyof typeof applicationDescriptionLabels, string>> = {};
  const lines = (description ?? "").split(/\r?\n/);
  for (const line of lines) {
    const separatorIndex = line.indexOf(":");
    if (separatorIndex < 0) continue;
    const label = line.slice(0, separatorIndex).trim();
    const value = line.slice(separatorIndex + 1).trim();
    if (!value) continue;
    for (const [key, labels] of Object.entries(applicationDescriptionLabels)) {
      if (labels.includes(label)) {
        result[key as keyof typeof applicationDescriptionLabels] = value;
        break;
      }
    }
  }
  return result;
}

function parsePriceExpectation(value?: string) {
  const [min = "", max = ""] = (value ?? "").split(/\s+-\s+/, 2);
  return {
    max: max === "-" ? "" : max.trim(),
    min: min === "-" ? "" : min.trim(),
  };
}

function parseEmailTypes(value?: string) {
  const types = new Set<string>();
  for (const part of (value ?? "").split(/[,，/]/)) {
    const normalized = part.trim().toLowerCase();
    if (!normalized) continue;
    if (normalized.includes("microsoft") || normalized.includes("微软")) {
      types.add("microsoft");
    }
    if (normalized.includes("domain") || normalized.includes("域名")) {
      types.add("domain");
    }
  }
  return Array.from(types);
}

function mailRulePattern(
  rules: ProjectMailRule[],
  ruleType: ProjectMailRule["ruleType"]
) {
  return (
    rules.find((rule) => rule.ruleType === ruleType && rule.enabled)?.pattern ??
    rules.find((rule) => rule.ruleType === ruleType)?.pattern ??
    ""
  );
}

function applyFormFromProjectDetail(detail: ProjectDetailResponse): ApplyFormState {
  const fields = parseApplicationDescription(detail.project.description);
  const codePrice = parsePriceExpectation(fields.codePriceExpectation);
  const purchasePrice = parsePriceExpectation(fields.purchasePriceExpectation);
  const emailTypes = parseEmailTypes(fields.expectedEmailTypes);
  const productTypes = Array.from(
    new Set(detail.products.map((product) => product.type).filter(Boolean))
  );
  const mailRules = detail.mailRules ?? [];

  return {
    codeMaxPrice: codePrice.max,
    codeMinPrice: codePrice.min,
    emailTypes:
      emailTypes.length > 0
        ? emailTypes
        : productTypes.length > 0
          ? productTypes
          : ["microsoft"],
    mailBody: mailRulePattern(mailRules, "body") || fields.mailBody || "",
    mailSubject:
      mailRulePattern(mailRules, "subject") || fields.mailSubject || "",
    projectName: detail.project.name,
    projectURL:
      fields.projectURL ||
      projectLinkFromTargetPlatform(detail.project.targetPlatform),
    purchaseMaxPrice: purchasePrice.max,
    purchaseMinPrice: purchasePrice.min,
    remarks: fields.remarks ?? "",
    senderPattern:
      mailRulePattern(mailRules, "sender") || fields.senderPattern || "",
  };
}

function buildProjectApplicationPayload(
  form: ApplyFormState,
  t: (key: string) => string
): CreateProjectApplicationRequest {
  const targetPlatform = extractTargetPlatform(form.projectURL, form.projectName);
  const description = [
    `${t("Project URL")}: ${form.projectURL.trim()}`,
    `${t("Sender email")}: ${form.senderPattern.trim()}`,
    `${t("Mail subject")}: ${form.mailSubject.trim()}`,
    `${t("Mail body")}: ${form.mailBody.trim()}`,
    `${t("Expected email types")}: ${form.emailTypes.map((type) => productTypeLabel(type, t)).join(", ")}`,
    form.codeMinPrice || form.codeMaxPrice
      ? `${t("Code price expectation")}: ${form.codeMinPrice || "-"} - ${form.codeMaxPrice || "-"}`
      : "",
    form.purchaseMinPrice || form.purchaseMaxPrice
      ? `${t("Purchase price expectation")}: ${form.purchaseMinPrice || "-"} - ${form.purchaseMaxPrice || "-"}`
      : "",
    form.remarks.trim() ? `${t("Remarks")}: ${form.remarks.trim()}` : "",
  ]
    .filter(Boolean)
    .join("\n");

  return {
    accessType: "public",
    description,
    looseMatch: true,
    mailRules: [
      { enabled: true, pattern: form.senderPattern.trim(), ruleType: "sender" },
      { enabled: true, pattern: "exact", ruleType: "recipient" },
      { enabled: true, pattern: form.mailSubject.trim(), ruleType: "subject" },
      { enabled: true, pattern: form.mailBody.trim(), ruleType: "body" },
    ],
    name: form.projectName.trim(),
    targetPlatform,
  };
}

function ProjectAvatar({
  className = "h-11 w-11",
  project,
}: {
  className?: string;
  project: ProjectItem;
}) {
  if (project.logoUrl) {
    return (
      <img
        alt={project.name}
        className={`${className} rounded-xl border border-[var(--semi-color-border)] object-cover`}
        src={project.logoUrl}
      />
    );
  }
  return (
    <div
      className={`${className} flex shrink-0 items-center justify-center rounded-xl border border-[var(--semi-color-border)] bg-[var(--semi-color-fill-0)] text-base font-semibold text-[var(--semi-color-text-1)]`}
    >
      {projectInitial(project.name)}
    </div>
  );
}

function minProductPrice(products: ProjectProductSummary[], field: "codePrice" | "purchasePrice") {
  const prices = products
    .map((product) => Number(product[field]))
    .filter((value) => Number.isFinite(value) && value > 0);
  if (prices.length === 0) return "";
  return money(String(Math.min(...prices)));
}

function ProductPricePreview({
  compact = false,
  products,
}: {
  compact?: boolean;
  products: ProjectProductSummary[];
}) {
  const { t } = useTranslation();
  const enabledProducts = products.filter((product) => product.status === "enabled");
  if (enabledProducts.length === 0) {
    return (
      <div className="project-square-price-empty">
        {t("No prices configured")}
      </div>
    );
  }
  const productTypes = Array.from(
    new Set(enabledProducts.map((product) => productTypeLabel(product.type, t)))
  );
  const codePrice = minProductPrice(
    enabledProducts.filter((product) => product.codeEnabled),
    "codePrice"
  );
  const purchasePrice = minProductPrice(
    enabledProducts.filter((product) => product.purchaseEnabled),
    "purchasePrice"
  );
  const showFrom = enabledProducts.length > 1;

  return (
    <div
      className={[
        "project-square-price-list",
        compact ? "project-square-price-list-compact" : "",
      ].join(" ")}
    >
      <div className="project-square-price-row">
        <span className="project-square-product-types">
          {productTypes.join(" / ")}
        </span>
        <div className="project-square-price-values">
          {codePrice ? (
            <div className="project-square-price-value">
              <span>{t("Code service")}</span>
              <strong>
                {t("Yuan")}
                {codePrice}
              </strong>
              {showFrom ? <em>{t("From price suffix")}</em> : null}
            </div>
          ) : null}
          {purchasePrice ? (
            <div className="project-square-price-value">
              <span>{t("Purchase service")}</span>
              <strong>
                {t("Yuan")}
                {purchasePrice}
              </strong>
              {showFrom ? <em>{t("From price suffix")}</em> : null}
            </div>
          ) : null}
        </div>
      </div>
    </div>
  );
}

function ProjectCardItem({
  onOrder,
  onView,
  project,
}: {
  onOrder: () => void;
  onView: () => void;
  project: ProjectItem;
}) {
  const { t } = useTranslation();
  const orderable = isOrderableProject(project);
  const status = projectStatusTag(project, t);
  return (
    <div
      className="project-square-card-hitbox"
      onClick={onView}
      onKeyDown={(event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          onView();
        }
      }}
      role="button"
      tabIndex={0}
    >
      <Card bodyStyle={{ height: "100%" }} className="project-square-card cursor-pointer">
        <div className="flex h-full flex-col">
          <div className="mb-3 flex items-start justify-between gap-3">
            <div className="flex min-w-0 items-start gap-3">
              <ProjectAvatar project={project} />
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <h3 className="truncate text-base font-semibold text-[var(--semi-color-text-0)]">
                    {project.name}
                  </h3>
                  <span className="shrink-0 font-mono text-xs text-[var(--semi-color-text-2)]">
                    #{project.id}
                  </span>
                </div>
                <div className="mt-1 flex min-w-0 items-center gap-1 text-xs text-[var(--semi-color-text-2)]">
                  <Globe2 size={13} />
                  <span className="truncate">{project.targetPlatform}</span>
                </div>
              </div>
            </div>
            <div className="flex shrink-0 items-center gap-2">
              <Button
                aria-label={t("Details")}
                icon={<Eye size={14} />}
                onClick={(event) => {
                  event.stopPropagation();
                  onView();
                }}
                size="small"
                theme="outline"
                type="tertiary"
              />
              <Button
                aria-label={t("Order")}
                disabled={!orderable}
                icon={<ShoppingCart size={14} />}
                onClick={(event) => {
                  event.stopPropagation();
                  if (!orderable) return;
                  onOrder();
                }}
                size="small"
                type="primary"
              />
            </div>
          </div>

          <p className="mb-3 line-clamp-2 min-h-10 text-sm leading-5 text-[var(--semi-color-text-2)]">
            {project.description || t("No description")}
          </p>

          <div className="mb-4">
            <ProductPricePreview products={project.products ?? []} />
          </div>

          <div className="mt-auto">
            <div className="flex items-center justify-between gap-2">
              <div className="flex min-w-0 flex-wrap gap-1.5">
                {status}
                {accessTag(project.accessType, t)}
                <Tag color={project.looseMatch ? "amber" : "grey"} shape="circle">
                  {project.looseMatch ? t("Loose match") : t("Strict match")}
                </Tag>
              </div>
              <Text className="shrink-0" size="small" type="tertiary">
                {formatTime(project.updatedAt)}
              </Text>
            </div>
          </div>
        </div>
      </Card>
    </div>
  );
}

function ProjectTableView({
  items,
  loading,
  onOrder,
  onView,
}: {
  items: ProjectItem[];
  loading: boolean;
  onOrder: (project: ProjectItem) => void;
  onView: (project: ProjectItem) => void;
}) {
  const { t } = useTranslation();
  const columns = useMemo(
    () =>
      [
        {
          dataIndex: "name",
          key: "name",
          title: t("Project name"),
          width: 260,
          render: (_: unknown, record: ProjectItem) => (
            <div className="flex min-w-0 items-center gap-3">
              <ProjectAvatar project={record} />
              <div className="min-w-0">
                <div className="truncate font-medium text-[var(--semi-color-text-0)]">
                  {record.name}
                </div>
                <div className="truncate text-xs text-[var(--semi-color-text-2)]">
                  #{record.id}
                </div>
              </div>
            </div>
          ),
        },
        {
          dataIndex: "targetPlatform",
          key: "targetPlatform",
          title: t("Target platform"),
          width: 180,
          render: (value: unknown) => (
            <span className="break-all text-[var(--semi-color-text-1)]">
              {String(value || "-")}
            </span>
          ),
        },
        {
          dataIndex: "products",
          key: "products",
          title: t("Product prices"),
          width: 340,
          render: (_: unknown, record: ProjectItem) => (
            <ProductPricePreview compact products={record.products ?? []} />
          ),
        },
        {
          dataIndex: "accessType",
          key: "accessType",
          title: t("Access type"),
          width: 120,
          render: (value: unknown) => accessTag(String(value), t),
        },
        {
          dataIndex: "status",
          key: "status",
          title: t("Status"),
          width: 120,
          render: (_: unknown, record: ProjectItem) =>
            projectStatusTag(record, t) ?? (
              <Tag color="green" shape="circle">
                {t("Listed")}
              </Tag>
            ),
        },
        {
          dataIndex: "looseMatch",
          key: "looseMatch",
          title: t("Match mode"),
          width: 120,
          render: (value: unknown) => (
            <Tag color={Boolean(value) ? "amber" : "grey"} shape="circle">
              {Boolean(value) ? t("Loose match") : t("Strict match")}
            </Tag>
          ),
        },
        {
          dataIndex: "updatedAt",
          key: "updatedAt",
          title: t("Updated at"),
          width: 160,
          render: (value: unknown) => (
            <span className="text-xs text-[var(--semi-color-text-2)]">
              {formatTime(String(value))}
            </span>
          ),
        },
        {
          dataIndex: "operate",
          fixed: "right",
          key: "operate",
          title: t("Actions"),
          width: 170,
          render: (_: unknown, record: ProjectItem) => (
            <Space>
              <Button
                icon={<Eye size={14} />}
                onClick={(event) => {
                  event.stopPropagation();
                  onView(record);
                }}
                size="small"
                type="tertiary"
              >
                {t("Details")}
              </Button>
              <Button
                disabled={!isOrderableProject(record)}
                icon={<ShoppingCart size={14} />}
                onClick={(event) => {
                  event.stopPropagation();
                  if (!isOrderableProject(record)) return;
                  onOrder(record);
                }}
                size="small"
                type="primary"
              >
                {t("Order")}
              </Button>
            </Space>
          ),
        },
      ] as never[],
    [onOrder, onView, t]
  );

  return (
    <Card className="project-square-table-card">
      <Table
        columns={columns}
        dataSource={items}
        empty={<Empty description={t("No projects found")} style={{ padding: 30 }} />}
        loading={loading}
        onRow={(record) => ({
          onClick: () => onView(record as ProjectItem),
          style: { cursor: "pointer" },
        })}
        pagination={false}
        rowKey="id"
        scroll={{ x: "max-content" }}
      />
    </Card>
  );
}

function ProjectCardGrid({
  items,
  onOrder,
  onView,
}: {
  items: ProjectItem[];
  onOrder: (project: ProjectItem) => void;
  onView: (project: ProjectItem) => void;
}) {
  return (
    <div className="grid grid-cols-1 gap-4 xl:grid-cols-2 2xl:grid-cols-3">
      {items.map((project) => (
        <ProjectCardItem
          key={project.id}
          onOrder={() => onOrder(project)}
          onView={() => onView(project)}
          project={project}
        />
      ))}
    </div>
  );
}

interface ProjectFilterOption<T extends string> {
  count: number;
  label: string;
  value: T;
}

function ProjectFilterGroup<T extends string>({
  activeValue,
  items,
  onChange,
  title,
  variant,
}: {
  activeValue: T;
  items: ProjectFilterOption<T>[];
  onChange: (value: T) => void;
  title: string;
  variant: "amber" | "orange" | "violet";
}) {
  return (
    <section className={`project-square-filter-group project-square-filter-${variant}`}>
      <Divider align="left" margin="12px">
        {title}
      </Divider>
      <div className="project-square-filter-grid">
        {items.map((item) => {
          const active = item.value === activeValue;
          return (
            <button
              className={[
                "project-square-filter-button",
                active ? "project-square-filter-button-active" : "",
              ].join(" ")}
              key={item.value}
              onClick={() => onChange(item.value)}
              type="button"
            >
              <span className="project-square-filter-label">{item.label}</span>
              <span
                className={[
                  "project-square-filter-count",
                  active ? "project-square-filter-count-active" : "",
                ].join(" ")}
              >
                {item.count}
              </span>
            </button>
          );
        })}
      </div>
    </section>
  );
}

function ProjectSquareSidebar({
  accessCounts,
  accessFilter,
  matchCounts,
  matchFilter,
  onAccessChange,
  onMatchChange,
  onProductTypeChange,
  onReset,
  onStatusChange,
  productTypeCounts,
  productTypeFilter,
  statusCounts,
  statusFilter,
}: {
  accessCounts: Record<AccessFilter, number>;
  accessFilter: AccessFilter;
  matchCounts: Record<MatchFilter, number>;
  matchFilter: MatchFilter;
  onAccessChange: (value: AccessFilter) => void;
  onMatchChange: (value: MatchFilter) => void;
  onProductTypeChange: (value: ProductTypeFilter) => void;
  onReset: () => void;
  onStatusChange: (value: StatusFilter) => void;
  productTypeCounts: Record<ProductTypeFilter, number>;
  productTypeFilter: ProductTypeFilter;
  statusCounts: Record<StatusFilter, number>;
  statusFilter: StatusFilter;
}) {
  const { t } = useTranslation();
  return (
    <aside className="project-square-sidebar">
      <div className="mb-6 flex items-center justify-between px-2">
        <div className="text-lg font-semibold text-[var(--semi-color-text-0)]">
          {t("Filters")}
        </div>
        <Button onClick={onReset} size="small" theme="outline" type="tertiary">
          {t("Reset")}
        </Button>
      </div>

      <ProjectFilterGroup<StatusFilter>
        activeValue={statusFilter}
        items={[
          { count: statusCounts.all, label: t("All statuses"), value: "all" },
          { count: statusCounts.listed, label: t("Listed"), value: "listed" },
          {
            count: statusCounts.reviewing,
            label: t("Application pending"),
            value: "reviewing",
          },
          {
            count: statusCounts.rejected,
            label: t("Application rejected"),
            value: "rejected",
          },
        ]}
        onChange={onStatusChange}
        title={t("Status")}
        variant="amber"
      />

      <ProjectFilterGroup<AccessFilter>
        activeValue={accessFilter}
        items={[
          { count: accessCounts.all, label: t("All access"), value: "all" },
          { count: accessCounts.public, label: t("Public project"), value: "public" },
          { count: accessCounts.private, label: t("Private project"), value: "private" },
        ]}
        onChange={onAccessChange}
        title={t("Access type")}
        variant="violet"
      />

      <ProjectFilterGroup<MatchFilter>
        activeValue={matchFilter}
        items={[
          { count: matchCounts.all, label: t("All match modes"), value: "all" },
          { count: matchCounts.loose, label: t("Loose match"), value: "loose" },
          { count: matchCounts.strict, label: t("Strict match"), value: "strict" },
        ]}
        onChange={onMatchChange}
        title={t("Match mode")}
        variant="amber"
      />

      <ProjectFilterGroup<ProductTypeFilter>
        activeValue={productTypeFilter}
        items={[
          { count: productTypeCounts.all, label: t("All product types"), value: "all" },
          {
            count: productTypeCounts.microsoft,
            label: t("Microsoft email"),
            value: "microsoft",
          },
          { count: productTypeCounts.domain, label: t("Domain email"), value: "domain" },
        ]}
        onChange={onProductTypeChange}
        title={t("Product type")}
        variant="orange"
      />
    </aside>
  );
}

function ProjectSquareHeader({
  loading,
  onApply,
  onRefresh,
  onSearchChange,
  onViewModeToggle,
  searchKeyword,
  total,
  viewMode,
}: {
  loading: boolean;
  onApply: () => void;
  onRefresh: () => void;
  onSearchChange: (value: string) => void;
  onViewModeToggle: () => void;
  searchKeyword: string;
  total: number;
  viewMode: ProjectSquareViewMode;
}) {
  const { t } = useTranslation();
  const isTableView = viewMode === "table";
  return (
    <Card
      bodyStyle={{ padding: 12 }}
      className="project-square-header-card"
      cover={
        <div className="project-square-cover">
          <div className="min-w-0 flex-1">
            <div className="mb-2 flex flex-wrap items-center gap-2">
              <h1 className="truncate text-xl font-semibold text-white">
                {t("Project Square")}
              </h1>
              <Tag
                shape="circle"
                size="small"
                style={{
                  backgroundColor: "rgba(255,255,255,0.95)",
                  border: "1px solid rgba(255,255,255,0.8)",
                  color: "#1f2937",
                  fontWeight: 500,
                }}
              >
                {t("Project count", { count: total })}
              </Tag>
            </div>
            <p className="line-clamp-2 text-sm leading-6 text-white/90">
              {t("Choose listed projects and submit new project requirements.")}
            </p>
          </div>
          <div className="project-square-cover-icon hidden h-14 w-14 shrink-0 items-center justify-center rounded-2xl sm:flex">
            <FolderOpen size={30} />
          </div>
        </div>
      }
    >
      <div className="project-square-action-row">
        <div className="min-w-0 flex-1">
          <Input
            onChange={(value) => onSearchChange(String(value))}
            placeholder={t("Search project or platform")}
            prefix={<IconSearch />}
            showClear
            value={searchKeyword}
          />
        </div>
        <Button icon={<CirclePlus size={15} />} onClick={onApply} type="primary">
          {t("Apply project")}
        </Button>
        <Divider className="project-square-toolbar-divider" layout="vertical" margin="8px" />
        <Button
          icon={<RefreshCw size={14} />}
          loading={loading}
          onClick={onRefresh}
          theme="outline"
          type="tertiary"
        >
          {t("Refresh")}
        </Button>
        <Button
          icon={isTableView ? <LayoutGrid size={14} /> : <Table2 size={14} />}
          onClick={onViewModeToggle}
          theme={isTableView ? "solid" : "outline"}
          type={isTableView ? "primary" : "tertiary"}
        >
          {isTableView ? t("Card view") : t("Table view")}
        </Button>
      </div>
    </Card>
  );
}

function ProductRows({ products }: { products: ProjectProduct[] }) {
  const { t } = useTranslation();
  if (products.length === 0) {
    return <Text type="tertiary">{t("No products configured")}</Text>;
  }
  return (
    <div className="project-detail-products">
      {products.map((product) => (
        <div className="project-detail-product" key={product.id}>
          <div className="project-detail-product-heading">
            <Tag color={product.type === "microsoft" ? "amber" : "green"} shape="circle">
              {productTypeLabel(product.type, t)}
            </Tag>
            {product.status !== "enabled" ? (
              <Tag color="grey" shape="circle">
                {t("Disabled")}
              </Tag>
            ) : null}
          </div>
          <div className="project-detail-service-grid">
            {product.codeEnabled ? (
              <div className="project-detail-service-card">
                <div className="project-detail-service-label">{t("Code service")}</div>
                <div className="project-detail-price">
                  <span>{t("Yuan")}</span>
                  {money(product.codePrice)}
                </div>
                <div className="project-detail-meta-row">
                  <span>{t("Code window minutes")}</span>
                  <strong>
                    {product.codeWindowMinutes}
                    {t("Minutes unit")}
                  </strong>
                </div>
              </div>
            ) : null}
            {product.purchaseEnabled ? (
              <div className="project-detail-service-card project-detail-service-card-strong">
                <div className="project-detail-service-label">{t("Purchase service")}</div>
                <div className="project-detail-price">
                  <span>{t("Yuan")}</span>
                  {money(product.purchasePrice)}
                </div>
                <div className="project-detail-meta-row">
                  <span>{t("Activation window minutes")}</span>
                  <strong>
                    {product.activationWindowMinutes}
                    {t("Minutes unit")}
                  </strong>
                </div>
                <div className="project-detail-meta-row">
                  <span>{t("Warranty minutes label")}</span>
                  <strong>
                    {product.warrantyMinutes}
                    {t("Minutes unit")}
                  </strong>
                </div>
              </div>
            ) : null}
            {!product.codeEnabled && !product.purchaseEnabled ? (
              <Text type="tertiary">{t("No prices configured")}</Text>
            ) : null}
          </div>
        </div>
      ))}
    </div>
  );
}

function ProjectDetailApplicationStatus({
  onResubmit,
  project,
}: {
  onResubmit?: () => void;
  project: ProjectItem;
}) {
  const { t } = useTranslation();
  const status = projectStatusTag(project, t);
  const reviewReason = project.reviewReason?.trim() ?? "";
  const fallbackText =
    project.status === "reviewing" ? t("ProjectApplication.PendingHint") : "";

  if (!status && !reviewReason && !fallbackText) return null;

  return (
    <section
      className={[
        "project-detail-application-status",
        isRejectedApplication(project)
          ? "project-detail-application-status-rejected"
          : "project-detail-application-status-pending",
      ].join(" ")}
    >
      {status}
      <span className="project-detail-application-status-text">
        {reviewReason || fallbackText}
      </span>
      {isRejectedApplication(project) && onResubmit ? (
        <Button
          className="project-detail-application-status-action"
          onClick={onResubmit}
          size="small"
          type="primary"
        >
          {t("Resubmit application")}
        </Button>
      ) : null}
    </section>
  );
}

function ProjectDetailTitle({ project }: { project: ProjectItem }) {
  return (
    <div className="project-detail-title">
      <ProjectAvatar className="project-detail-title-avatar" project={project} />
      <div className="min-w-0">
        <div className="truncate text-lg font-semibold text-[var(--semi-color-text-0)]">
          {project.name}
        </div>
        <div className="font-mono text-xs text-[var(--semi-color-text-2)]">
          #{project.id}
        </div>
      </div>
    </div>
  );
}

function ProjectDetailSheet({
  detail,
  onCancel,
  onResubmit,
}: {
  detail: ProjectDetailResponse | null;
  onCancel: () => void;
  onResubmit: (detail: ProjectDetailResponse) => void;
}) {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const projectURL = detail ? projectLinkFromTargetPlatform(detail.project.targetPlatform) : "";

  return (
    <SideSheet
      bodyStyle={{ padding: 0 }}
      className="project-detail-sidesheet"
      onCancel={onCancel}
      placement="right"
      title={detail ? <ProjectDetailTitle project={detail.project} /> : t("Project detail")}
      visible={Boolean(detail)}
      width={isMobile ? "100%" : 600}
    >
      {detail ? (
        <div className="project-detail-body">
          <section className="project-detail-hero">
            <div className="project-detail-hero-content">
              <ProjectAvatar className="project-detail-hero-avatar" project={detail.project} />
              <div className="min-w-0 flex-1">
                <div className="project-detail-hero-label">{t("Project name")}</div>
                <h2>{detail.project.name}</h2>
                <div className="project-detail-link-label">{t("Project URL")}</div>
                {projectURL ? (
                  <a
                    className="project-detail-link"
                    href={projectURL}
                    rel="noreferrer"
                    target="_blank"
                  >
                    <Globe2 size={15} />
                    <span>{projectURL}</span>
                    <ExternalLink size={13} />
                  </a>
                ) : (
                  <div className="project-detail-link project-detail-link-empty">
                    <Globe2 size={15} />
                    <span>-</span>
                  </div>
                )}
              </div>
            </div>
          </section>

          <ProjectDetailApplicationStatus
            onResubmit={() => onResubmit(detail)}
            project={detail.project}
          />

          <Divider margin={0} />

          <section className="project-detail-section">
            <div className="project-detail-section-heading">
              <div className="project-detail-section-icon">
                <ShoppingCart size={16} />
              </div>
              <div>
                <div className="project-detail-section-title">{t("Product prices")}</div>
                <div className="project-detail-section-subtitle">
                  {t("Service window")}
                </div>
              </div>
            </div>
            <ProductRows products={detail.products} />
          </section>
        </div>
      ) : null}
    </SideSheet>
  );
}

function ApplyProjectModal({
  initialValue,
  mode = "create",
  onCancel,
  onSuccess,
  projectId,
  visible,
}: {
  initialValue?: ApplyFormState;
  mode?: ApplyModalMode;
  onCancel: () => void;
  onSuccess: (detail: ProjectDetailResponse) => void;
  projectId?: number;
  visible: boolean;
}) {
  const { t } = useTranslation();
  const [form, setForm] = useState<ApplyFormState>(initialApplyForm);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    if (visible) setForm(initialValue ?? initialApplyForm);
  }, [initialValue, visible]);

  const setField = <K extends keyof ApplyFormState>(key: K, value: ApplyFormState[K]) => {
    setForm((previous) => ({ ...previous, [key]: value }));
  };

  const submit = async () => {
    if (
      !form.projectName.trim() ||
      !form.projectURL.trim() ||
      !form.senderPattern.trim() ||
      !form.mailSubject.trim() ||
      !form.mailBody.trim() ||
      form.emailTypes.length === 0
    ) {
      Toast.error(t("Please complete required project application fields."));
      return;
    }
    if (mode === "resubmit" && !projectId) {
      Toast.error(t("Project application resubmit failed."));
      return;
    }

    const payload = buildProjectApplicationPayload(form, t);

    setSubmitting(true);
    try {
      const response =
        mode === "resubmit" && projectId
          ? await resubmitProjectApplication(projectId, payload)
          : await createProjectApplication(payload);
      Toast.success(
        mode === "resubmit"
          ? t("Project application resubmitted.")
          : t("Project application submitted.")
      );
      onSuccess(response);
      onCancel();
    } catch (error) {
      Toast.error(
        getIamErrorMessage(
          t,
          error,
          mode === "resubmit"
            ? "Project application resubmit failed."
            : "Project application failed."
        )
      );
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Modal
      cancelText={t("Cancel")}
      confirmLoading={submitting}
      okText={mode === "resubmit" ? t("Resubmit application") : t("Submit application")}
      onCancel={onCancel}
      onOk={() => void submit()}
      title={mode === "resubmit" ? t("Resubmit project application") : t("Apply project")}
      visible={visible}
      width={640}
    >
      <div className="grid gap-4">
        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Project name")} *
          </span>
          <Input
            maxLength={120}
            onChange={(value) => setField("projectName", String(value))}
            placeholder={t("Project name placeholder")}
            value={form.projectName}
          />
        </label>

        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Project URL")} *
          </span>
          <Input
            maxLength={300}
            onChange={(value) => setField("projectURL", String(value))}
            placeholder="https://example.com"
            value={form.projectURL}
          />
        </label>

        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Sender email")} *
          </span>
          <Input
            maxLength={500}
            onChange={(value) => setField("senderPattern", String(value))}
            placeholder="noreply@example.com"
            value={form.senderPattern}
          />
        </label>

        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Mail subject")} *
          </span>
          <Input
            maxLength={500}
            onChange={(value) => setField("mailSubject", String(value))}
            placeholder={t("Mail subject placeholder")}
            value={form.mailSubject}
          />
        </label>

        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Mail body")} *
          </span>
          <TextArea
            maxCount={500}
            onChange={(value) => setField("mailBody", String(value))}
            placeholder={t("Mail body placeholder")}
            rows={3}
            value={form.mailBody}
          />
        </label>

        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Expected email types")} *
          </span>
          <Select
            multiple
            onChange={(value) =>
              setField(
                "emailTypes",
                Array.isArray(value) ? value.map(String) : ["microsoft"]
              )
            }
            style={{ width: "100%" }}
            value={form.emailTypes}
          >
            <Select.Option value="microsoft">{t("Microsoft email")}</Select.Option>
            <Select.Option value="domain">{t("Domain email")}</Select.Option>
          </Select>
        </label>

        <div className="grid gap-3 sm:grid-cols-2">
          {[
            ["codeMinPrice", t("Code min price")],
            ["codeMaxPrice", t("Code max price")],
            ["purchaseMinPrice", t("Purchase min price")],
            ["purchaseMaxPrice", t("Purchase max price")],
          ].map(([key, label]) => (
            <label className="block" key={key}>
              <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
                {label}
              </span>
              <Input
                onChange={(value) =>
                  setField(key as keyof ApplyFormState, String(value) as never)
                }
                placeholder="0.00"
                value={form[key as keyof ApplyFormState] as string}
              />
            </label>
          ))}
        </div>

        <label className="block">
          <span className="mb-1 block text-xs font-medium text-[var(--semi-color-text-1)]">
            {t("Remarks")}
          </span>
          <TextArea
            maxCount={500}
            onChange={(value) => setField("remarks", String(value))}
            rows={3}
            value={form.remarks}
          />
        </label>
      </div>
    </Modal>
  );
}

export default function Projects() {
  const { t } = useTranslation();
  const isMobile = useIsMobile();
  const [searchKeyword, setSearchKeyword] = useState("");
  const [accessFilter, setAccessFilter] = useState<AccessFilter>("all");
  const [matchFilter, setMatchFilter] = useState<MatchFilter>("all");
  const [productTypeFilter, setProductTypeFilter] = useState<ProductTypeFilter>("all");
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [viewMode, setViewMode] = useState<ProjectSquareViewMode>("card");
  const [activePage, setActivePage] = useState(1);
  const [pageSize, setPageSize] = useSharedPageSize();

  useEffect(() => setActivePage(1), [pageSize]);
  const [applyOpen, setApplyOpen] = useState(false);
  const [applyMode, setApplyMode] = useState<ApplyModalMode>("create");
  const [applyInitialValue, setApplyInitialValue] = useState<ApplyFormState>();
  const [resubmitProjectId, setResubmitProjectId] = useState<number>();
  const [detail, setDetail] = useState<ProjectDetailResponse | null>(null);
  const [debouncedSearchKeyword] = useDebouncedValue(searchKeyword);
  const [accessCounts, setAccessCounts] = useState<Record<AccessFilter, number>>({
    all: 0,
    private: 0,
    public: 0,
  });
  const [matchCounts, setMatchCounts] = useState<Record<MatchFilter, number>>({
    all: 0,
    loose: 0,
    strict: 0,
  });
  const [productTypeCounts, setProductTypeCounts] = useState<Record<ProductTypeFilter, number>>({
    all: 0,
    domain: 0,
    microsoft: 0,
  });
  const [statusCounts, setStatusCounts] = useState<Record<StatusFilter, number>>({
    all: 0,
    listed: 0,
    rejected: 0,
    reviewing: 0,
  });

  const searchFilter = useMemo<ProjectListFilter>(() => {
    const search = debouncedSearchKeyword.trim();
    return search ? { search, scope: "visible" as const } : { scope: "visible" as const };
  }, [debouncedSearchKeyword]);

  const listFilter = useMemo<ProjectListFilter>(() => {
    const filter: ProjectListFilter = { ...searchFilter };
    const status = statusFilterToProjectStatus(statusFilter);
    if (status) filter.status = status;
    if (accessFilter !== "all") filter.accessType = accessFilter;
    if (matchFilter === "loose") filter.looseMatch = true;
    if (matchFilter === "strict") filter.looseMatch = false;
    if (productTypeFilter !== "all") filter.productType = productTypeFilter;
    return filter;
  }, [accessFilter, matchFilter, productTypeFilter, searchFilter, statusFilter]);

  const loadProjectBlock = useCallback(
    async (offset: number, limit: number) => {
      const listResponse = await listProjects(listFilter, offset, limit);
      const facets = listResponse.facets;
      setStatusCounts({
        all: facets?.status.all ?? listResponse.total,
        listed: facets?.status.listed ?? 0,
        rejected: facets?.status.rejected ?? 0,
        reviewing: facets?.status.reviewing ?? 0,
      });
      setAccessCounts({
        all: facets?.access.all ?? listResponse.total,
        private: facets?.access.private ?? 0,
        public: facets?.access.public ?? 0,
      });
      setMatchCounts({
        all: facets?.match.all ?? listResponse.total,
        loose: facets?.match.loose ?? 0,
        strict: facets?.match.strict ?? 0,
      });
      setProductTypeCounts({
        all: facets?.productType.all ?? listResponse.total,
        domain: facets?.productType.domain ?? 0,
        microsoft: facets?.productType.microsoft ?? 0,
      });
      return { items: listResponse.items, total: listResponse.total };
    },
    [listFilter]
  );

  const {
    loading,
    pagedItems: items,
    refresh,
    total,
  } = useBlockPagedList<ProjectItem>({
    activePage,
    filterKey: JSON.stringify(listFilter),
    loadBlock: loadProjectBlock,
    onError: (error) => {
      Toast.error(getIamErrorMessage(t, error, "Projects load failed."));
    },
    pageSize,
  });

  const resetFilters = () => {
    setSearchKeyword("");
    setAccessFilter("all");
    setMatchFilter("all");
    setProductTypeFilter("all");
    setStatusFilter("all");
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

  const openApplyModal = () => {
    setApplyMode("create");
    setApplyInitialValue(undefined);
    setResubmitProjectId(undefined);
    setApplyOpen(true);
  };

  const openResubmitModal = (projectDetail: ProjectDetailResponse) => {
    setApplyMode("resubmit");
    setApplyInitialValue(applyFormFromProjectDetail(projectDetail));
    setResubmitProjectId(projectDetail.project.id);
    setApplyOpen(true);
  };

  return (
    <div className="project-square-layout">
      {!isMobile ? (
        <ProjectSquareSidebar
          accessCounts={accessCounts}
          accessFilter={accessFilter}
          matchCounts={matchCounts}
          matchFilter={matchFilter}
          onAccessChange={(value) => {
            setAccessFilter(value);
            setActivePage(1);
          }}
          onMatchChange={(value) => {
            setMatchFilter(value);
            setActivePage(1);
          }}
          onProductTypeChange={(value) => {
            setProductTypeFilter(value);
            setActivePage(1);
          }}
          onReset={resetFilters}
          onStatusChange={(value) => {
            setStatusFilter(value);
            setActivePage(1);
          }}
          productTypeCounts={productTypeCounts}
          productTypeFilter={productTypeFilter}
          statusCounts={statusCounts}
          statusFilter={statusFilter}
        />
      ) : null}

      <main className="project-square-content">
        <div className="project-square-search-header">
          <ProjectSquareHeader
            loading={loading}
            onApply={openApplyModal}
            onRefresh={() => void refresh()}
            onSearchChange={(value) => {
              setSearchKeyword(value);
              setActivePage(1);
            }}
            onViewModeToggle={() =>
              setViewMode((previous) => (previous === "card" ? "table" : "card"))
            }
            searchKeyword={searchKeyword}
            total={total}
            viewMode={viewMode}
          />
          {isMobile ? (
            <div className="mt-3 rounded-2xl border border-[var(--semi-color-border)] bg-[var(--semi-color-bg-2)] p-3">
              <ProjectSquareSidebar
                accessCounts={accessCounts}
                accessFilter={accessFilter}
                matchCounts={matchCounts}
                matchFilter={matchFilter}
                onAccessChange={(value) => {
                  setAccessFilter(value);
                  setActivePage(1);
                }}
                onMatchChange={(value) => {
                  setMatchFilter(value);
                  setActivePage(1);
                }}
                onProductTypeChange={(value) => {
                  setProductTypeFilter(value);
                  setActivePage(1);
                }}
                onReset={resetFilters}
                onStatusChange={(value) => {
                  setStatusFilter(value);
                  setActivePage(1);
                }}
                productTypeCounts={productTypeCounts}
                productTypeFilter={productTypeFilter}
                statusCounts={statusCounts}
                statusFilter={statusFilter}
              />
            </div>
          ) : null}
        </div>

        <div className="project-square-view-container">
          {loading && items.length === 0 ? (
            <div className="flex min-h-[360px] items-center justify-center">
              <Spin />
            </div>
          ) : items.length === 0 ? (
            <div className="flex min-h-[360px] items-center justify-center">
              <Empty description={t("No projects found")} />
            </div>
          ) : viewMode === "card" ? (
            <ProjectCardGrid
              items={items}
              onOrder={() => Toast.info(t("Ordering is not implemented yet."))}
              onView={(project) => void openDetail(project.id)}
            />
          ) : (
            <ProjectTableView
              items={items}
              loading={loading}
              onOrder={() => Toast.info(t("Ordering is not implemented yet."))}
              onView={(project) => void openDetail(project.id)}
            />
          )}

          {total > 0 ? (
            <div className="project-square-pagination">
              <Pagination
                currentPage={activePage}
                onPageChange={setActivePage}
                onPageSizeChange={(size) => {
                  setPageSize(size);
                  setActivePage(1);
                }}
                pageSize={pageSize}
                pageSizeOpts={[10, 20, 50, 100]}
                showQuickJumper={isMobile}
                showSizeChanger
                size={isMobile ? "small" : "default"}
                total={total}
              />
            </div>
          ) : null}
        </div>
      </main>

      <ApplyProjectModal
        initialValue={applyInitialValue}
        mode={applyMode}
        onCancel={() => setApplyOpen(false)}
        onSuccess={(response) => {
          setActivePage(1);
          if (applyMode === "resubmit") {
            setDetail(response);
          }
          void refresh();
        }}
        projectId={resubmitProjectId}
        visible={applyOpen}
      />
      <ProjectDetailSheet
        detail={detail}
        onCancel={() => setDetail(null)}
        onResubmit={openResubmitModal}
      />
    </div>
  );
}
